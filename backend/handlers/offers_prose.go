// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"errors"
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
	Kind         string `json:"kind"`
	Text         string `json:"text"`
	Depth        int    `json:"depth,omitempty"`
	Marker       string `json:"marker,omitempty"`
	Numbering    string `json:"numbering,omitempty"`
	ListStart    int    `json:"list_start,omitempty"`
	ListContinue bool   `json:"list_continue,omitempty"`
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
		return node.Numbering == "" && node.ListStart == 0 && !node.ListContinue
	}
	return !(node.ListStart > 0 && node.ListContinue)
}

func offerItemGlyph(node OfferTextNode) string {
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

func projectOfferNodes(nodes []OfferTextNode) string {
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
				if earlier.Kind == "item" && earlier.Marker == "decimal" && earlier.Depth == depth && sameMode {
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
			parts := make([]string, depth+1)
			for c := 0; c <= depth; c++ {
				n := levels[c]
				if n <= 0 {
					n = 1
				}
				parts[c] = strconv.Itoa(n)
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
		}
		if node.ListStart > 0 {
			out.ListStart = node.ListStart
		} else if node.ListContinue {
			out.ListContinue = true
		}
	}
	return out
}

// normalizeOfferProse accepts either legacy body text or an explicit node list.
// A single paragraph is stored only as body. Invalid structure is rejected.
func normalizeOfferProse(body string, nodes []OfferTextNode) (string, []OfferTextNode, error) {
	if len(nodes) == 0 {
		return body, nil, nil
	}
	if len(nodes) > offerProseMaxNodes {
		return body, nil, errors.New("Ungültige Textstruktur")
	}
	canon := make([]OfferTextNode, 0, len(nodes))
	previous := -1
	for _, node := range nodes {
		if !validProseText(node.Text) || (node.Kind != "paragraph" && node.Kind != "item") || !validOfferMarker(node.Kind, node.Marker) || !validOfferNumbering(node) {
			return body, nil, errors.New("Ungültige Textstruktur")
		}
		if node.Kind == "paragraph" {
			if node.Depth != 0 {
				return body, nil, errors.New("Ungültige Textstruktur")
			}
			previous = -1
			canon = append(canon, canonOfferNode(node, 0))
			continue
		}
		maxDepth := 0
		if previous >= 0 {
			maxDepth = previous + 1
			if maxDepth > offerProseMaxDepth {
				maxDepth = offerProseMaxDepth
			}
		}
		if node.Depth < 0 || node.Depth > offerProseMaxDepth || (node.Depth > maxDepth && !(previous < 0 && node.ListContinue)) {
			return body, nil, errors.New("Ungültige Textstruktur")
		}
		previous = node.Depth
		canon = append(canon, canonOfferNode(node, node.Depth))
	}
	if len(canon) == 1 && canon[0].Kind == "paragraph" {
		return canon[0].Text, nil, nil
	}
	return projectOfferNodes(canon), canon, nil
}

func normalizeOfferBlocks(blocks []OfferBlock) error {
	for i := range blocks {
		body, nodes, err := normalizeOfferProse(blocks[i].Body, blocks[i].Nodes)
		if err != nil {
			return err
		}
		blocks[i].Body = body
		blocks[i].Nodes = nodes
	}
	return nil
}
