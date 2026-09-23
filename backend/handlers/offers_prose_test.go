// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"strings"
	"testing"
)

func TestNormalizeOfferProseKeepsLegacyTextAndStoresLists(t *testing.T) {
	legacy := "- Punkt\n- Zweiter"
	body, nodes, err := normalizeOfferProse(legacy, nil)
	if err != nil || nodes != nil || body != legacy {
		t.Fatalf("legacy = %q nodes=%v err=%v", body, nodes, err)
	}
	body, nodes, err = normalizeOfferProse("plain", []OfferTextNode{{Kind: "paragraph", Text: "plain"}})
	if err != nil || nodes != nil || body != "plain" {
		t.Fatalf("single paragraph = %q nodes=%v err=%v", body, nodes, err)
	}
	sample := []OfferTextNode{
		{Kind: "paragraph", Text: "Einleitung."},
		{Kind: "item", Text: "Analyse"},
		{Kind: "item", Text: "Interviews", Depth: 1},
		{Kind: "paragraph", Text: "Abschluss."},
	}
	body, nodes, err = normalizeOfferProse("ignored", sample)
	if err != nil {
		t.Fatal(err)
	}
	const want = "Einleitung.\n• Analyse\n  ◦ Interviews\nAbschluss."
	if body != want || len(nodes) != 4 || nodes[2].Depth != 1 || nodes[2].Text != "Interviews" {
		t.Fatalf("list = %q %#v", body, nodes)
	}
	script := "<script>alert(1)</script>"
	body, nodes, err = normalizeOfferProse("", []OfferTextNode{{Kind: "item", Text: script}})
	if err != nil || len(nodes) != 1 || nodes[0].Text != script || body != "• "+script {
		t.Fatalf("script text = %q %#v %v", body, nodes, err)
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "tief", Depth: 2}}); err == nil {
		t.Fatal("skipped depth was accepted")
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "html", Text: "x"}}); err == nil {
		t.Fatal("unknown kind was accepted")
	}
}

func TestNormalizeOfferProseKeepsLongLegacyTextAndRejectsNodeControls(t *testing.T) {
	long := strings.Repeat("A", 2002)
	body, nodes, err := normalizeOfferProse(long, nil)
	if err != nil || nodes != nil || body != long {
		t.Fatalf("long legacy = %q nodes=%v err=%v", body, nodes, err)
	}
	crlf := "Alpha\r\nBeta"
	body, nodes, err = normalizeOfferProse(crlf, nil)
	if err != nil || nodes != nil || body != crlf {
		t.Fatalf("crlf legacy = %q nodes=%v err=%v", body, nodes, err)
	}
	if _, _, err = normalizeOfferProse("Alpha\nBeta", []OfferTextNode{{Kind: "item", Text: crlf}}); err == nil {
		t.Fatal("carriage return in a node was accepted")
	}
	if _, _, err = normalizeOfferProse(long, []OfferTextNode{{Kind: "item", Text: long}}); err == nil {
		t.Fatal("overlong item was accepted")
	}
}

func TestNormalizeOfferProseMarkersAndFooter(t *testing.T) {
	nodes := []OfferTextNode{
		{Kind: "paragraph", Text: "Einleitung."},
		{Kind: "item", Text: "Analyse", Marker: "decimal"},
		{Kind: "item", Text: "Interviews", Depth: 1, Marker: "decimal"},
		{Kind: "item", Text: "Punkt", Marker: "disc"},
		{Kind: "item", Text: "Weiter", Marker: "decimal"},
	}
	body, stored, err := normalizeOfferProse("ignored", nodes)
	if err != nil {
		t.Fatal(err)
	}
	const want = "Einleitung.\n1. Analyse\n  1. Interviews\n• Punkt\n1. Weiter"
	if body != want || len(stored) != 5 || stored[1].Marker != "decimal" || stored[0].Marker != "" {
		t.Fatalf("markers = %q %#v", body, stored)
	}
	plain := []OfferTextNode{{Kind: "item", Text: "Analyse"}, {Kind: "item", Text: "Workshop", Depth: 1}}
	body, stored, err = normalizeOfferProse("ignored", plain)
	if err != nil || body != "• Analyse\n  ◦ Workshop" || stored[0].Marker != "" {
		t.Fatalf("legacy list = %q %#v %v", body, stored, err)
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "x", Marker: "image"}}); err == nil {
		t.Fatal("marker image was accepted")
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "paragraph", Text: "x", Marker: "disc"}}); err == nil {
		t.Fatal("paragraph marker was accepted")
	}
	doc := OfferDocument{OfferDate: "2026-09-23", ValidUntil: "2026-10-23", Footer: &OfferFooterLayout{LogoWidthMM: 50.04, LogoOffsetMM: 2}}
	if err = calculateOffer(&doc, false); err != nil || doc.Footer.LogoWidthMM != 50 || doc.Footer.LogoOffsetMM != 2 {
		t.Fatalf("footer = %#v %v", doc.Footer, err)
	}
	legacy := OfferDocument{OfferDate: "2026-09-23", ValidUntil: "2026-10-23"}
	if err = calculateOffer(&legacy, false); err != nil || legacy.Footer != nil {
		t.Fatalf("legacy footer = %#v %v", legacy.Footer, err)
	}
	wide := OfferDocument{OfferDate: "2026-09-23", ValidUntil: "2026-10-23", Footer: &OfferFooterLayout{LogoWidthMM: 200, LogoOffsetMM: 2}}
	if err = calculateOffer(&wide, false); err == nil {
		t.Fatal("wide footer was accepted")
	}
}

func TestOutlineNumberingAndSignedFooterOffset(t *testing.T) {
	nodes := []OfferTextNode{
		{Kind: "item", Text: "A", Marker: "decimal", Numbering: "outline", ListStart: 3},
		{Kind: "item", Text: "B", Depth: 1, Marker: "decimal", Numbering: "outline"},
		{Kind: "item", Text: "C", Depth: 2, Marker: "decimal", Numbering: "outline"},
		{Kind: "paragraph", Text: ""},
		{Kind: "item", Text: "D", Depth: 2, Marker: "decimal", Numbering: "outline", ListContinue: true},
		{Kind: "item", Text: "E", Marker: "decimal", Numbering: "outline", ListStart: 1},
		{Kind: "item", Text: "F", Marker: "decimal", Numbering: "outline"},
	}
	body, stored, err := normalizeOfferProse("ignored", nodes)
	const want = "3 A\n  3.1 B\n    3.1.1 C\n\n    3.1.2 D\n1 E\n2 F"
	if err != nil || body != want || stored[0].ListStart != 3 || stored[4].ListContinue != true || stored[3].Numbering != "" {
		t.Fatalf("outline = %q %#v %v", body, stored, err)
	}
	independent := []OfferTextNode{
		{Kind: "item", Text: "A", Marker: "decimal", Numbering: "outline", ListStart: 3},
		{Kind: "paragraph", Text: ""},
		{Kind: "item", Text: "B", Marker: "decimal", Numbering: "outline"},
	}
	body, _, err = normalizeOfferProse("ignored", independent)
	if err != nil || body != "3 A\n\n1 B" {
		t.Fatalf("independent = %q %v", body, err)
	}
	continued := []OfferTextNode{
		{Kind: "item", Text: "A", Marker: "decimal"},
		{Kind: "paragraph", Text: "dazwischen"},
		{Kind: "item", Text: "B", Marker: "decimal", ListContinue: true},
	}
	body, _, err = normalizeOfferProse("ignored", continued)
	if err != nil || body != "1. A\ndazwischen\n2. B" {
		t.Fatalf("continue = %q %v", body, err)
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "decimal", Numbering: "outline", ListStart: 3, ListContinue: true}}); err == nil {
		t.Fatal("start and continue together were accepted")
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "disc", Numbering: "outline"}}); err == nil {
		t.Fatal("outline on a bullet was accepted")
	}
	for _, offset := range []float64{-6, 0, 2, 10} {
		doc := OfferDocument{OfferDate: "2026-09-23", ValidUntil: "2026-10-23", Footer: &OfferFooterLayout{LogoWidthMM: 43.3, LogoOffsetMM: offset}}
		if err = calculateOffer(&doc, false); err != nil || doc.Footer.LogoOffsetMM != offset {
			t.Fatalf("offset %v = %#v %v", offset, doc.Footer, err)
		}
	}
	for _, offset := range []float64{-6.1, 10.1} {
		doc := OfferDocument{OfferDate: "2026-09-23", ValidUntil: "2026-10-23", Footer: &OfferFooterLayout{LogoWidthMM: 43.3, LogoOffsetMM: offset}}
		if err = calculateOffer(&doc, false); err == nil {
			t.Fatalf("offset %v was accepted", offset)
		}
	}
}

func TestSectionBoundOutlineUsesSectionIndex(t *testing.T) {
	nodes := []OfferTextNode{
		{Kind: "item", Text: "A", Marker: "decimal", Numbering: "outline", SectionBound: true},
		{Kind: "item", Text: "Kind", Depth: 1, Marker: "decimal", Numbering: "outline", SectionBound: true},
		{Kind: "item", Text: "B", Marker: "decimal", Numbering: "outline", SectionBound: true},
	}
	body, stored, err := normalizeOfferProseInSection("ignored", nodes, 2)
	if err != nil || body != "2.1 A\n  2.1.1 Kind\n2.2 B" || !stored[0].SectionBound || stored[0].Text != "A" {
		t.Fatalf("section bound = %q %#v %v", body, stored, err)
	}
	body, _, err = normalizeOfferProseInSection("ignored", nodes, 3)
	if err != nil || body != "3.1 A\n  3.1.1 Kind\n3.2 B" {
		t.Fatalf("moved section = %q %v", body, err)
	}
}
