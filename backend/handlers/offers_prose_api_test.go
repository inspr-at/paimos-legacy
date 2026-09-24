// SPDX-License-Identifier: AGPL-3.0-only
package handlers_test

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/inspr-at/paimos/backend/handlers"
)

func TestOfferProseRoundTripPreservesLegacyAndLists(t *testing.T) {
	ts := newTestServer(t)
	settings := offerSettingsFixture()
	settings.Defaults.Blocks = []handlers.OfferBlock{
		{Heading: "Alt", Body: "- Punkt\n- Zweiter"},
		{Heading: "Neu", Body: "ignored", Nodes: []handlers.OfferTextNode{
			{Kind: "paragraph", Text: "Einleitung."},
			{Kind: "item", Text: "Analyse"},
			{Kind: "item", Text: "Interviews", Depth: 1},
		}},
	}
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	assertStatus(t, resp, 200)
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if json.Unmarshal(raw, &saved) != nil {
		t.Fatal("settings json")
	}
	blocks := saved["defaults"].(map[string]any)["blocks"].([]any)
	legacy := blocks[0].(map[string]any)
	if _, ok := legacy["nodes"]; ok || legacy["body"] != "- Punkt\n- Zweiter" {
		t.Fatalf("legacy settings changed: %#v", legacy)
	}
	listed := blocks[1].(map[string]any)
	if listed["body"] != "Einleitung.\n• Analyse\n  ◦ Interviews" {
		t.Fatalf("projection = %#v", listed["body"])
	}
	if nodes, _ := listed["nodes"].([]any); len(nodes) != 3 {
		t.Fatalf("nodes = %#v", listed["nodes"])
	}
	bad := offerSettingsFixture()
	bad.Defaults.Blocks[0].Nodes = []handlers.OfferTextNode{{Kind: "item", Text: "tief", Depth: 6}}
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, bad)
	assertStatus(t, resp, 400)
	resp.Body.Close()

	resp = ts.post(t, "/api/customers", ts.adminCookie, map[string]any{"name": "Testkunde", "address": "Gasse 2\n1010 Wien", "contact_name": "Eva Test", "contact_email": "eva@example.test"})
	assertStatus(t, resp, 201)
	var customer struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &customer)
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": customer.ID})
	assertStatus(t, resp, 201)
	var offer handlers.Offer
	decode(t, resp, &offer)
	offer.Document.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Body: "- Punkt\n- Zweiter"}}
	offer.Document.Positions = []handlers.OfferPosition{{ShortText: "Beratung", Quantity: 2, Unit: "Tage", UnitPriceCents: 10000}}
	path := "/api/offers/" + jsonNumber(offer.ID)
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document})
	assertStatus(t, resp, 200)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(body, &offer) != nil {
		t.Fatal("offer json")
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		t.Fatal("offer map")
	}
	block := payload["document"].(map[string]any)["blocks"].([]any)[0].(map[string]any)
	if _, ok := block["nodes"]; ok || block["body"] != "- Punkt\n- Zweiter" || offer.Document.NetTotalCents != 20000 {
		t.Fatalf("legacy offer changed: %#v total=%d", block, offer.Document.NetTotalCents)
	}
	offer.Document.Blocks[0].Nodes = []handlers.OfferTextNode{{Kind: "item", Text: "Analyse"}, {Kind: "item", Text: "Workshop", Depth: 1}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document})
	assertStatus(t, resp, 200)
	decode(t, resp, &offer)
	if offer.Document.NetTotalCents != 20000 || offer.Document.Blocks[0].Body != "• Analyse\n  ◦ Workshop" || len(offer.Document.Blocks[0].Nodes) != 2 {
		t.Fatalf("list offer = %#v total=%d", offer.Document.Blocks[0], offer.Document.NetTotalCents)
	}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document, "finalize": true})
	assertStatus(t, resp, 200)
	decode(t, resp, &offer)
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document})
	assertStatus(t, resp, 409)
	resp.Body.Close()
}

func jsonNumber(id int64) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}
