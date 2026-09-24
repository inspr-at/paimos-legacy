// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	offerProseMaxDepth = 5
	offerProseMaxNodes = 100
	offerProseMaxText  = 2000
)

// OfferTextNode is an optional paragraph or bullet inside an offer text block.
// Omitted nodes keep body as literal plain text.
type OfferTextNode struct {
	Kind         string  `json:"kind"`
	Text         string  `json:"text"`
	Depth        int     `json:"depth,omitempty"`
	Marker       string  `json:"marker,omitempty"`
	Numbering    string  `json:"numbering,omitempty"`
	ListStart    int     `json:"list_start,omitempty"`
	ListContinue bool    `json:"list_continue,omitempty"`
	SectionBound bool    `json:"section_bound,omitempty"`
	Glyph        string  `json:"glyph,omitempty"`
	MarkerXMM    float64 `json:"marker_x_mm,omitempty"`
	MarkerYMM    float64 `json:"marker_y_mm,omitempty"`
	TextStartMM  float64 `json:"text_start_mm,omitempty"`
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
		return OfferTextNode{Kind: "paragraph", Text: node.Text}
	}
	out := OfferTextNode{Kind: "item", Text: node.Text, Marker: node.Marker}
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
	if len(canon) == 1 && canon[0].Kind == "paragraph" {
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
