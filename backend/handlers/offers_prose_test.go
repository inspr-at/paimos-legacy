// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// SPDX-License-Identifier: AGPL-3.0-only
package handlers

import "testing"

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
