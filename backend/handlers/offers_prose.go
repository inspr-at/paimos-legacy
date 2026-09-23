// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"errors"
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
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Depth int    `json:"depth,omitempty"`
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

func projectOfferNodes(nodes []OfferTextNode) string {
	lines := make([]string, len(nodes))
	for i, node := range nodes {
		if node.Kind != "item" {
			lines[i] = node.Text
			continue
		}
		lines[i] = strings.Repeat("  ", node.Depth) + offerBullet(node.Depth) + " " + node.Text
	}
	return strings.Join(lines, "\n")
}

func canonOfferNode(node OfferTextNode, depth int) OfferTextNode {
	if node.Kind == "item" && depth > 0 {
		return OfferTextNode{Kind: "item", Text: node.Text, Depth: depth}
	}
	if node.Kind == "item" {
		return OfferTextNode{Kind: "item", Text: node.Text}
	}
	return OfferTextNode{Kind: "paragraph", Text: node.Text}
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
		if !validProseText(node.Text) || (node.Kind != "paragraph" && node.Kind != "item") {
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
		if node.Depth < 0 || node.Depth > maxDepth {
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
