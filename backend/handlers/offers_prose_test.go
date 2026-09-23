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
