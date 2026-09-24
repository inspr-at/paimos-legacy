// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	offerProseMaxDepth = 5
	offerProseMaxNodes = 100
	offerProseMaxText  = 2000
	// 3 understands character marks. 2 still round-trips depth, symbols and marker layout.
	// Older editors omit the version and would save newer prose as plain text.
	offerProseWriterVersion      = 3
	offerProseStaleWriterMessage = "Die gespeicherte Textformatierung ist neuer als dieser Editor. Bitte die Seite neu laden, bevor Sie speichern."
)

// OfferTextNode is an optional paragraph or bullet inside an offer text block.
// Omitted nodes keep body as literal plain text.
type OfferTextNode struct {
	Kind         string            `json:"kind"`
	Text         string            `json:"text"`
	Depth        int               `json:"depth,omitempty"`
	Marker       string            `json:"marker,omitempty"`
	Numbering    string            `json:"numbering,omitempty"`
	ListStart    int               `json:"list_start,omitempty"`
	ListContinue bool              `json:"list_continue,omitempty"`
	SectionBound bool              `json:"section_bound,omitempty"`
	Glyph        string            `json:"glyph,omitempty"`
	MarkerXMM    float64           `json:"marker_x_mm,omitempty"`
	MarkerYMM    float64           `json:"marker_y_mm,omitempty"`
	TextStartMM  float64           `json:"text_start_mm,omitempty"`
	Marks        []OfferInlineMark `json:"marks,omitempty"`
}

// OfferInlineMark is a bold and/or italic range in UTF-16 code units.
type OfferInlineMark struct {
	Start  int  `json:"start"`
	End    int  `json:"end"`
	Bold   bool `json:"bold,omitempty"`
	Italic bool `json:"italic,omitempty"`
}

func offerBullet(depth int) string {
	switch {
	case depth <= 0:
		return "•"
	case depth == 1:
		return "◦"
	default:
		return "▪"
	}
}

func validProseText(text string) bool {
	if utf8.RuneCountInString(text) > offerProseMaxText {
		return false
	}
	for _, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validOfferMarker(kind, marker string) bool {
	if marker == "" {
		return true
	}
	if kind != "item" {
		return false
	}
	switch marker {
	case "disc", "circle", "square", "dash", "decimal":
		return true
	default:
		return false
	}
}

func validOfferNumbering(node OfferTextNode) bool {
	if node.Numbering != "" && node.Numbering != "outline" {
		return false
	}
	if node.ListStart < 0 || node.ListStart > 9999 {
		return false
	}
	if node.Kind != "item" || node.Marker != "decimal" {
		return node.Numbering == "" && node.ListStart == 0 && !node.ListContinue && !node.SectionBound
	}
	if node.SectionBound && node.Numbering != "outline" {
		return false
	}
	return !(node.ListStart > 0 && node.ListContinue)
}

// roundMarkerMM keeps one decimal place, half away from zero, then the inclusive range.
// −1.25 and 1.25 both become 1.3. −30.05 becomes −30.1 and falls outside −30..30.
func roundMarkerMM(value, min, max float64) (float64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	scaled := math.Round(value*10) / 10
	if scaled < min || scaled > max {
		return 0, false
	}
	return scaled, true
}

// proseNodesNeedWriter reports prose the previous editor cannot round-trip.
// That editor rejects a depth the previous item cannot own, except a continued
// item opening a chain, and it drops a custom symbol and marker offsets on save.
func proseNodesNeedWriter(nodes []OfferTextNode) bool {
	previous := -1
	for _, node := range nodes {
		if node.Kind != "item" {
			previous = -1
			continue
		}
		if node.Glyph != "" || node.MarkerXMM != 0 || node.MarkerYMM != 0 || node.TextStartMM != 0 {
			return true
		}
		depth := node.Depth
		if depth < 0 {
			depth = 0
		}
		if depth > offerProseMaxDepth {
			depth = offerProseMaxDepth
		}
		maxDepth := 0
		if previous >= 0 {
			maxDepth = previous + 1
			if maxDepth > offerProseMaxDepth {
				maxDepth = offerProseMaxDepth
			}
		}
		if depth > maxDepth && !(previous < 0 && node.ListContinue) {
			return true
		}
		previous = depth
	}
	return false
}

func offerBlocksNeedWriter(blocks []OfferBlock) bool {
	for _, block := range blocks {
		if proseNodesNeedWriter(block.Nodes) {
			return true
		}
	}
	return false
}

// offerProseWriteUnsafe is true when this client would replace newer prose.
// A client that sends offerProseWriterVersion may remove that prose on purpose.
// The offer revision does not help: the older editor already loaded this revision.
func proseNodesHaveMarks(nodes []OfferTextNode) bool {
	for _, node := range nodes {
		if len(node.Marks) > 0 {
			return true
		}
	}
	return false
}

func offerBlocksHaveMarks(blocks []OfferBlock) bool {
	for _, block := range blocks {
		if proseNodesHaveMarks(block.Nodes) {
			return true
		}
	}
	return false
}

func offerProseWriteUnsafe(stored, incoming []OfferBlock, writer int) bool {
	if writer >= offerProseWriterVersion {
		return false
	}
	if offerBlocksHaveMarks(stored) || offerBlocksHaveMarks(incoming) {
		return true
	}
	if writer >= 2 {
		return false
	}
	return offerBlocksNeedWriter(stored) || offerBlocksNeedWriter(incoming)
}

func utf16Boundary(units []uint16, offset int) bool {
	if offset <= 0 || offset >= len(units) {
		return true
	}
	prev := units[offset-1]
	next := units[offset]
	return !(prev >= 0xD800 && prev <= 0xDBFF && next >= 0xDC00 && next <= 0xDFFF)
}

func canonInlineMarks(text string, marks []OfferInlineMark) ([]OfferInlineMark, bool) {
	if len(marks) == 0 {
		return nil, true
	}
	units := utf16.Encode([]rune(text))
	ordered := append([]OfferInlineMark(nil), marks...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start == ordered[j].Start {
			return ordered[i].End < ordered[j].End
		}
		return ordered[i].Start < ordered[j].Start
	})
	out := make([]OfferInlineMark, 0, len(ordered))
	for _, mark := range ordered {
		if mark.Start < 0 || mark.End <= mark.Start || mark.End > len(units) || (!mark.Bold && !mark.Italic) {
			return nil, false
		}
		if !utf16Boundary(units, mark.Start) || !utf16Boundary(units, mark.End) {
			return nil, false
		}
		if len(out) > 0 && mark.Start < out[len(out)-1].End {
			return nil, false
		}
		if len(out) > 0 {
			prev := &out[len(out)-1]
			if mark.Start == prev.End && mark.Bold == prev.Bold && mark.Italic == prev.Italic {
				prev.End = mark.End
				continue
			}
		}
		out = append(out, OfferInlineMark{Start: mark.Start, End: mark.End, Bold: mark.Bold, Italic: mark.Italic})
	}
	return out, true
}

func plainItemGlyph(glyph string, marker string) (string, bool) {
	if glyph == "" {
		return "", true
	}
	if marker == "decimal" || utf8.RuneCountInString(glyph) > 4 || strings.TrimSpace(glyph) != glyph {
		return "", false
	}
	if strings.ContainsAny(glyph, "<>&") {
		return "", false
	}
	for _, r := range glyph {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return glyph, true
}

func offerItemGlyph(node OfferTextNode) string {
	if node.Glyph != "" && node.Marker != "decimal" {
		return node.Glyph
	}
	switch node.Marker {
	case "disc":
		return "•"
	case "circle":
		return "◦"
	case "square":
		return "▪"
	case "dash":
		return "–"
	default:
		return offerBullet(node.Depth)
	}
}

func projectOfferNodes(nodes []OfferTextNode, sectionNumber int) string {
	plain := make([]int, offerProseMaxDepth+1)
	outline := make([]int, offerProseMaxDepth+1)
	snaps := make([][]int, len(nodes))
	lines := make([]string, len(nodes))
	for i, node := range nodes {
		if node.Kind != "item" {
			for c := range plain {
				plain[c] = 0
				outline[c] = 0
			}
			lines[i] = node.Text
			continue
		}
		depth := node.Depth
		if depth < 0 {
			depth = 0
		}
		if depth > offerProseMaxDepth {
			depth = offerProseMaxDepth
		}
		if node.Marker != "decimal" {
			for c := depth; c < len(plain); c++ {
				plain[c] = 0
				outline[c] = 0
			}
			lines[i] = strings.Repeat("  ", node.Depth) + offerItemGlyph(node) + " " + node.Text
			continue
		}
		source := plain
		if node.Numbering == "outline" {
			source = outline
		}
		levels := append([]int(nil), source...)
		for c := depth + 1; c < len(levels); c++ {
			levels[c] = 0
		}
		if node.ListStart > 0 {
			levels[depth] = node.ListStart
		} else if node.ListContinue {
			previous := -1
			for j := i - 1; j >= 0; j-- {
				earlier := nodes[j]
				sameMode := (earlier.Numbering == "outline") == (node.Numbering == "outline")
				sameRoot := earlier.SectionBound == node.SectionBound
				if earlier.Kind == "item" && earlier.Marker == "decimal" && earlier.Depth == depth && sameMode && sameRoot {
					previous = j
					break
				}
			}
			if previous >= 0 {
				snap := snaps[previous]
				for c := 0; c <= depth && c < len(snap); c++ {
					levels[c] = snap[c]
				}
			}
			levels[depth]++
		} else if levels[depth] > 0 {
			levels[depth]++
		} else {
			levels[depth] = 1
		}
		snaps[i] = append([]int(nil), levels...)
		if node.Numbering == "outline" {
			copy(outline, levels)
			parts := make([]string, 0, depth+2)
			if node.SectionBound && sectionNumber > 0 {
				parts = append(parts, strconv.Itoa(sectionNumber))
			}
			for c := 0; c <= depth; c++ {
				n := levels[c]
				if n <= 0 {
					if c < depth {
						continue
					}
					n = 1
				}
				parts = append(parts, strconv.Itoa(n))
			}
			lines[i] = strings.Repeat("  ", node.Depth) + strings.Join(parts, ".") + " " + node.Text
			continue
		}
		copy(plain, levels)
		lines[i] = strings.Repeat("  ", node.Depth) + strconv.Itoa(levels[depth]) + ". " + node.Text
	}
	return strings.Join(lines, "\n")
}

func canonOfferNode(node OfferTextNode, depth int) OfferTextNode {
	if node.Kind != "item" {
		return OfferTextNode{Kind: "paragraph", Text: node.Text, Marks: node.Marks}
	}
	out := OfferTextNode{Kind: "item", Text: node.Text, Marker: node.Marker, Marks: node.Marks}
	if depth > 0 {
		out.Depth = depth
	}
	if node.Marker == "decimal" {
		if node.Numbering == "outline" {
			out.Numbering = "outline"
			if node.SectionBound {
				out.SectionBound = true
			}
		}
		if node.ListStart > 0 {
			out.ListStart = node.ListStart
		} else if node.ListContinue {
			out.ListContinue = true
		}
	}
	if node.Marker != "decimal" {
		out.Glyph = node.Glyph
	}
	out.MarkerXMM = node.MarkerXMM
	out.MarkerYMM = node.MarkerYMM
	out.TextStartMM = node.TextStartMM
	return out
}

// normalizeOfferProse accepts either legacy body text or an explicit node list.
// A single paragraph is stored only as body. Invalid structure is rejected.
func normalizeOfferProse(body string, nodes []OfferTextNode) (string, []OfferTextNode, error) {
	return normalizeOfferProseInSection(body, nodes, 0)
}

func normalizeOfferProseInSection(body string, nodes []OfferTextNode, sectionNumber int) (string, []OfferTextNode, error) {
	if len(nodes) == 0 {
		return body, nil, nil
	}
	if len(nodes) > offerProseMaxNodes {
		return body, nil, errors.New("Ungültige Textstruktur")
	}
	canon := make([]OfferTextNode, 0, len(nodes))
	for _, node := range nodes {
		if !validProseText(node.Text) || (node.Kind != "paragraph" && node.Kind != "item") || !validOfferMarker(node.Kind, node.Marker) || !validOfferNumbering(node) {
			return body, nil, errors.New("Ungültige Textstruktur")
		}
		glyph, glyphOK := plainItemGlyph(node.Glyph, node.Marker)
		markerX, xOK := roundMarkerMM(node.MarkerXMM, -30, 30)
		markerY, yOK := roundMarkerMM(node.MarkerYMM, -20, 20)
		textStart, textOK := roundMarkerMM(node.TextStartMM, -20, 40)
		if !glyphOK || !xOK || !yOK || !textOK {
			return body, nil, errors.New("Ungültige Textstruktur")
		}
		marks, marksOK := canonInlineMarks(node.Text, node.Marks)
		if !marksOK {
			return body, nil, errors.New("Ungültige Textstruktur")
		}
		node.Marks = marks
		node.Glyph = glyph
		node.MarkerXMM = markerX
		node.MarkerYMM = markerY
		node.TextStartMM = textStart
		if node.Kind == "paragraph" {
			if node.Depth != 0 || node.Glyph != "" || node.MarkerXMM != 0 || node.MarkerYMM != 0 || node.TextStartMM != 0 {
				return body, nil, errors.New("Ungültige Textstruktur")
			}
			canon = append(canon, canonOfferNode(node, 0))
			continue
		}
		if node.Depth < 0 || node.Depth > offerProseMaxDepth {
			return body, nil, errors.New("Ungültige Textstruktur")
		}
		canon = append(canon, canonOfferNode(node, node.Depth))
	}
	if len(canon) == 1 && canon[0].Kind == "paragraph" && len(canon[0].Marks) == 0 {
		return canon[0].Text, nil, nil
	}
	return projectOfferNodes(canon, sectionNumber), canon, nil
}

func normalizeOfferBlocks(blocks []OfferBlock) error {
	for i := range blocks {
		body, nodes, err := normalizeOfferProseInSection(blocks[i].Body, blocks[i].Nodes, i+1)
		if err != nil {
			return err
		}
		blocks[i].Body = body
		blocks[i].Nodes = nodes
	}
	return nil
}
