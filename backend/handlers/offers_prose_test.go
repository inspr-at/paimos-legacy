// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import (
	"math"
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
	body, nodes, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "tief", Depth: 2}})
	if err != nil || len(nodes) != 1 || nodes[0].Depth != 2 || !strings.Contains(body, "tief") {
		t.Fatalf("first-item depth = %q %#v %v", body, nodes, err)
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "tief", Depth: 6}}); err == nil {
		t.Fatal("depth above the maximum was accepted")
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

func TestOfferProseIndentGlyphAndMarkerOffset(t *testing.T) {
	nodes := []OfferTextNode{
		{Kind: "item", Text: "Planungsrahmen", Marker: "decimal", Numbering: "outline", ListStart: 4, SectionBound: true},
		{Kind: "item", Text: "Projektstart", Depth: 2, Marker: "circle", Glyph: "✓", MarkerXMM: -1.5, MarkerYMM: 0.5, TextStartMM: 2},
		{Kind: "item", Text: "Monat", Depth: 3, Marker: "square"},
		{Kind: "item", Text: "Weiter", Marker: "decimal", Numbering: "outline", SectionBound: true},
	}
	body, stored, err := normalizeOfferProseInSection("ignored", nodes, 5)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, ".0") || !strings.Contains(body, "5.4 Planungsrahmen") || !strings.Contains(body, "✓ Projektstart") || !strings.Contains(body, "5.5 Weiter") {
		t.Fatalf("labels = %q", body)
	}
	if stored[1].Depth != 2 || stored[1].Glyph != "✓" || stored[1].Marker != "circle" || stored[1].MarkerXMM != -1.5 || stored[1].MarkerYMM != 0.5 || stored[1].TextStartMM != 2 || stored[2].Depth != 3 {
		t.Fatalf("stored = %#v %#v", stored[1], stored[2])
	}
	long := []OfferTextNode{
		{Kind: "item", Text: "Planungsrahmen", Marker: "decimal", Numbering: "outline", ListStart: 4, SectionBound: true},
		{Kind: "item", Text: "Kind", Depth: 1, Marker: "decimal", Numbering: "outline", SectionBound: true},
	}
	body, _, err = normalizeOfferProseInSection("ignored", long, 5)
	if err != nil || body != "5.4 Planungsrahmen\n  5.4.1 Kind" {
		t.Fatalf("long label = %q %v", body, err)
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "disc", Glyph: "<b>x</b>"}}); err == nil {
		t.Fatal("html glyph was accepted")
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "decimal", Numbering: "outline", Glyph: "✓"}}); err == nil {
		t.Fatal("decimal glyph was accepted")
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "disc", MarkerXMM: 80}}); err == nil {
		t.Fatal("wide marker offset was accepted")
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "disc", MarkerYMM: math.NaN()}}); err == nil {
		t.Fatal("nan marker offset was accepted")
	}
}

func TestRoundMarkerMMMatchesHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		value    float64
		min, max float64
		want     float64
		ok       bool
	}{
		{-1.25, -30, 30, -1.3, true},
		{1.25, -30, 30, 1.3, true},
		{-1.24, -30, 30, -1.2, true},
		{-1.26, -30, 30, -1.3, true},
		{-30.05, -30, 30, 0, false},
		{-30.04, -30, 30, -30, true},
		{30.05, -30, 30, 0, false},
		{30.04, -30, 30, 30, true},
		{-30, -30, 30, -30, true},
		{30, -30, 30, 30, true},
		{-0.05, -30, 30, -0.1, true},
		{0.05, -30, 30, 0.1, true},
		{-29.95, -30, 30, -30, true},
		{29.95, -30, 30, 30, true},
		{-20.05, -20, 20, 0, false},
		{40.05, -20, 40, 0, false},
		{-20.04, -20, 20, -20, true},
	}
	for _, tc := range cases {
		got, ok := roundMarkerMM(tc.value, tc.min, tc.max)
		if ok != tc.ok || (ok && math.Abs(got-tc.want) > 1e-9) {
			t.Fatalf("roundMarkerMM(%v, %v, %v) = %v, %v; want %v, %v", tc.value, tc.min, tc.max, got, ok, tc.want, tc.ok)
		}
	}
	body, stored, err := normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "disc", MarkerXMM: -1.25}})
	if err != nil || math.Abs(stored[0].MarkerXMM-(-1.3)) > 1e-9 || !strings.Contains(body, "A") {
		t.Fatalf("stored tenth = %v %q %v", stored, body, err)
	}
	if _, _, err = normalizeOfferProse("keep", []OfferTextNode{{Kind: "item", Text: "A", Marker: "disc", MarkerXMM: -30.05}}); err == nil {
		t.Fatal("-30.05 was accepted")
	}
}

func TestProseWriterDetectsInitialDepthAndLayoutOnly(t *testing.T) {
	if !proseNodesNeedWriter([]OfferTextNode{{Kind: "item", Text: "A", Depth: 1}}) {
		t.Fatal("initial depth was treated as legacy")
	}
	legacy := []OfferTextNode{{Kind: "item", Text: "A"}, {Kind: "item", Text: "B", Depth: 1}}
	if proseNodesNeedWriter(legacy) {
		t.Fatal("legacy chain required the new writer")
	}
	continued := []OfferTextNode{{Kind: "item", Text: "A", Depth: 2, Marker: "decimal", Numbering: "outline", ListContinue: true}}
	if proseNodesNeedWriter(continued) {
		t.Fatal("continued opening item required the new writer")
	}
	if !proseNodesNeedWriter([]OfferTextNode{{Kind: "item", Text: "A"}, {Kind: "item", Text: "B", Depth: 2}}) {
		t.Fatal("skipped depth was treated as legacy")
	}
	if !proseNodesNeedWriter([]OfferTextNode{{Kind: "paragraph", Text: "p"}, {Kind: "item", Text: "A", Depth: 1}}) {
		t.Fatal("depth after a paragraph was treated as legacy")
	}
	afterContinue := []OfferTextNode{{Kind: "paragraph", Text: "p"}, {Kind: "item", Text: "A", Depth: 1, Marker: "decimal", Numbering: "outline", ListContinue: true}}
	if proseNodesNeedWriter(afterContinue) {
		t.Fatal("continued item after a paragraph required the new writer")
	}
	if !proseNodesNeedWriter([]OfferTextNode{{Kind: "item", Text: "A", Glyph: "✓"}}) {
		t.Fatal("glyph was treated as legacy")
	}
	if !proseNodesNeedWriter([]OfferTextNode{{Kind: "item", Text: "A", MarkerXMM: -1.3}}) {
		t.Fatal("marker offset was treated as legacy")
	}
	if proseNodesNeedWriter([]OfferTextNode{{Kind: "item", Text: "A"}}) {
		t.Fatal("plain item required the new writer")
	}
	if offerProseWriteUnsafe([]OfferBlock{{Nodes: legacy}}, nil, 0) {
		t.Fatal("legacy save was refused")
	}
	initial := []OfferBlock{{Nodes: []OfferTextNode{{Kind: "item", Text: "A", Depth: 1}}}}
	if !offerProseWriteUnsafe(initial, nil, 0) || !offerProseWriteUnsafe(nil, initial, 1) {
		t.Fatal("initial depth was writable without the current version")
	}
	if offerProseWriteUnsafe(initial, nil, offerProseWriterVersion) {
		t.Fatal("capable client could not clear or keep initial depth")
	}
}
