// SPDX-License-Identifier: AGPL-3.0-only
package handlers_test

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

func takeOffer(t *testing.T, resp *http.Response) handlers.Offer {
	t.Helper()
	var offer handlers.Offer
	decode(t, resp, &offer)
	return offer
}

func proseResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOfferProseWriterVersionKeepsNewFormatting(t *testing.T) {
	ts := newTestServer(t)
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, offerSettingsFixture())
	assertStatus(t, resp, 200)
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
	offer.Document.Positions = []handlers.OfferPosition{{ShortText: "Beratung", Quantity: 1, Unit: "Pauschale", UnitPriceCents: 10000}}
	path := "/api/offers/" + jsonNumber(offer.ID)
	offer.Document.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Nodes: []handlers.OfferTextNode{
		{Kind: "item", Text: "Analyse"},
		{Kind: "item", Text: "Workshop", Depth: 1},
	}}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document})
	assertStatus(t, resp, 200)
	offer = takeOffer(t, resp)
	if len(offer.Document.Blocks[0].Nodes) != 2 || offer.Document.Blocks[0].Nodes[1].Depth != 1 {
		t.Fatalf("legacy list = %#v", offer.Document.Blocks[0])
	}

	stripped := offer.Document
	stripped.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Body: "nur Text"}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": stripped})
	assertStatus(t, resp, 200)
	offer = takeOffer(t, resp)
	if offer.Document.Blocks[0].Nodes != nil || offer.Document.Blocks[0].Body != "nur Text" {
		t.Fatalf("legacy clear = %#v", offer.Document.Blocks[0])
	}

	initial := offer.Document
	initial.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Nodes: []handlers.OfferTextNode{
		{Kind: "item", Text: "Erste Ebene", Depth: 1, Marker: "disc"},
	}}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": initial})
	body := proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("initial depth without writer: %d %s", resp.StatusCode, body)
	}
	resp = ts.get(t, path, ts.adminCookie)
	offer = takeOffer(t, resp)
	if offer.Document.Blocks[0].Body != "nur Text" || offer.Document.Blocks[0].Nodes != nil {
		t.Fatalf("refused create changed the offer: %#v", offer.Document.Blocks[0])
	}

	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 2, "document": initial})
	assertStatus(t, resp, 200)
	offer = takeOffer(t, resp)
	node := offer.Document.Blocks[0].Nodes[0]
	if node.Depth != 1 || node.Text != "Erste Ebene" || node.Glyph != "" || node.MarkerXMM != 0 || node.MarkerYMM != 0 || node.TextStartMM != 0 {
		t.Fatalf("initial depth = %#v", node)
	}
	revision := offer.Revision
	stale := offer.Document
	stale.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Body: "ohne Knoten"}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": revision, "document": stale})
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("stale strip: %d %s", resp.StatusCode, body)
	}
	resp = ts.get(t, path, ts.adminCookie)
	offer = takeOffer(t, resp)
	if offer.Revision != revision || offer.Document.Blocks[0].Nodes[0].Depth != 1 || offer.Document.Blocks[0].Nodes[0].Text != "Erste Ebene" {
		t.Fatalf("stale write changed revision %d or nodes %#v", offer.Revision, offer.Document.Blocks[0])
	}

	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": customer.ID, "duplicate_id": offer.ID})
	assertStatus(t, resp, 201)
	var copy handlers.Offer
	decode(t, resp, &copy)
	if copy.ID == offer.ID || copy.Document.Blocks[0].Nodes[0].Depth != 1 {
		t.Fatalf("duplicate = %#v", copy.Document.Blocks[0])
	}

	cleared := offer.Document
	cleared.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Body: "Klartext"}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 2, "document": cleared})
	assertStatus(t, resp, 200)
	offer = takeOffer(t, resp)
	if offer.Document.Blocks[0].Nodes != nil || offer.Document.Blocks[0].Body != "Klartext" {
		t.Fatalf("capable clear = %#v", offer.Document.Blocks[0])
	}

	laid := offer.Document
	laid.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Nodes: []handlers.OfferTextNode{
		{Kind: "item", Text: "Markiert", Marker: "disc", Glyph: "✓", MarkerXMM: -1.25},
	}}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 2, "document": laid})
	assertStatus(t, resp, 200)
	offer = takeOffer(t, resp)
	laidNode := offer.Document.Blocks[0].Nodes[0]
	if laidNode.Glyph != "✓" || math.Abs(laidNode.MarkerXMM-(-1.3)) > 1e-9 || laidNode.Depth != 0 {
		t.Fatalf("layout = %#v", laidNode)
	}
	plain := offer.Document
	plain.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Nodes: []handlers.OfferTextNode{{Kind: "item", Text: "Markiert", Marker: "disc"}}}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": plain})
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("stale layout strip: %d %s", resp.StatusCode, body)
	}
	resp = ts.get(t, path, ts.adminCookie)
	offer = takeOffer(t, resp)
	if offer.Document.Blocks[0].Nodes[0].Glyph != "✓" || math.Abs(offer.Document.Blocks[0].Nodes[0].MarkerXMM-(-1.3)) > 1e-9 {
		t.Fatalf("layout was stripped: %#v", offer.Document.Blocks[0].Nodes[0])
	}
	wide := offer.Document
	wide.Blocks = []handlers.OfferBlock{{Heading: offer.Document.Blocks[0].Heading, Body: offer.Document.Blocks[0].Body, Nodes: []handlers.OfferTextNode{offer.Document.Blocks[0].Nodes[0]}}}
	wide.Blocks[0].Nodes[0].MarkerXMM = -30.05
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 2, "document": wide})
	if resp.StatusCode != 400 {
		t.Fatalf("-30.05 status %d %s", resp.StatusCode, proseResponseBody(t, resp))
	}
	resp.Body.Close()
	resp = ts.get(t, path, ts.adminCookie)
	offer = takeOffer(t, resp)
	if math.Abs(offer.Document.Blocks[0].Nodes[0].MarkerXMM-(-1.3)) > 1e-9 {
		t.Fatalf("rejected offset was stored: %#v", offer.Document.Blocks[0].Nodes[0])
	}

	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "document": offer.Document, "finalize": true})
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("stale finalize: %d %s", resp.StatusCode, body)
	}
	resp = ts.get(t, path, ts.adminCookie)
	offer = takeOffer(t, resp)
	if offer.Status != "draft" || offer.Document.Blocks[0].Nodes[0].Glyph != "✓" {
		t.Fatalf("finalize changed the draft: %s %#v", offer.Status, offer.Document.Blocks[0].Nodes[0])
	}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 2, "document": offer.Document, "finalize": true})
	assertStatus(t, resp, 200)
	offer = takeOffer(t, resp)
	if offer.Status != "sent" || offer.Document.Blocks[0].Nodes[0].Glyph != "✓" {
		t.Fatalf("capable finalize = %s %#v", offer.Status, offer.Document.Blocks[0])
	}
}

func TestOfferProseCreateAndDefaultsRejectIncapableV2(t *testing.T) {
	ts := newTestServer(t)
	settings := offerSettingsFixture()
	settings.Defaults.Blocks = []handlers.OfferBlock{{Heading: "Neu", Nodes: []handlers.OfferTextNode{
		{Kind: "item", Text: "Erste Ebene", Depth: 1, Marker: "disc"},
	}}}
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	body := proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("settings without writer: %d %s", resp.StatusCode, body)
	}
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, map[string]any{
		"sender": settings.Sender, "defaults": settings.Defaults, "prose_writer_version": 2,
	})
	assertStatus(t, resp, 200)
	resp.Body.Close()

	resp = ts.post(t, "/api/customers", ts.adminCookie, map[string]any{"name": "Testkunde", "address": "Gasse 2\n1010 Wien", "contact_name": "Eva Test", "contact_email": "eva@example.test"})
	assertStatus(t, resp, 201)
	var customer struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &customer)
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{
		"customer_id": customer.ID,
		"document": map[string]any{"blocks": []handlers.OfferBlock{{Heading: "Fremd", Nodes: []handlers.OfferTextNode{
			{Kind: "item", Text: "Nicht speichern", Depth: 1},
		}}}},
	})
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("create with v2 document: %d %s", resp.StatusCode, body)
	}
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": customer.ID})
	assertStatus(t, resp, 201)
	var offer handlers.Offer
	decode(t, resp, &offer)
	if offer.Document.Blocks[0].Nodes[0].Depth != 1 || offer.Document.Blocks[0].Nodes[0].Text != "Erste Ebene" {
		t.Fatalf("create did not copy stored defaults: %#v", offer.Document.Blocks[0])
	}
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{
		"customer_id": customer.ID, "prose_writer_version": 2,
		"document": map[string]any{"blocks": []handlers.OfferBlock{{Heading: "Fremd", Body: "Client"}}},
	})
	assertStatus(t, resp, 201)
	offer = takeOffer(t, resp)
	if offer.Document.Blocks[0].Body == "Client" || offer.Document.Blocks[0].Nodes[0].Text != "Erste Ebene" {
		t.Fatalf("create applied the client document: %#v", offer.Document.Blocks[0])
	}

	plain := settings
	plain.Defaults.Blocks = []handlers.OfferBlock{{Heading: "Neu", Body: "Klartext"}}
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, plain)
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("settings strip: %d %s", resp.StatusCode, body)
	}
	resp = ts.get(t, "/api/integrations/crm/offers", ts.adminCookie)
	var saved handlers.OfferSettings
	decode(t, resp, &saved)
	if saved.Defaults.Blocks[0].Nodes[0].Depth != 1 {
		t.Fatalf("settings were stripped: %#v", saved.Defaults.Blocks[0])
	}
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, map[string]any{
		"sender": plain.Sender, "defaults": plain.Defaults, "prose_writer_version": 2,
	})
	assertStatus(t, resp, 200)
	saved = handlers.OfferSettings{}
	decode(t, resp, &saved)
	if saved.Defaults.Blocks[0].Nodes != nil || saved.Defaults.Blocks[0].Body != "Klartext" {
		t.Fatalf("capable settings clear = %#v", saved.Defaults.Blocks[0])
	}
}

func TestOfferSettingsOverlappingLegacySaveCannotStripV2(t *testing.T) {
	ts := newTestServer(t)
	settings := offerSettingsFixture()
	settings.Defaults.Blocks = []handlers.OfferBlock{{Heading: "Alt", Body: "Klartext"}}
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	assertStatus(t, resp, 200)
	resp.Body.Close()

	v2 := settings
	v2.Defaults.Blocks = []handlers.OfferBlock{{Heading: "Neu", Nodes: []handlers.OfferTextNode{
		{Kind: "item", Text: "Erste Ebene", Depth: 1, Marker: "disc"},
	}}}
	legacy := settings
	legacy.Defaults.Blocks = []handlers.OfferBlock{{Heading: "Neu", Body: "ohne Knoten"}}

	held := make(chan struct{})
	release := make(chan struct{})
	var pauseOnce sync.Once
	handlers.SetOfferSettingsWriteHookForTest(func() {
		pauseOnce.Do(func() {
			close(held)
			<-release
		})
	})
	t.Cleanup(func() { handlers.SetOfferSettingsWriteHookForTest(nil) })

	type result struct {
		status int
		body   string
	}
	capableDone := make(chan result, 1)
	go func() {
		resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, map[string]any{
			"sender": v2.Sender, "defaults": v2.Defaults, "prose_writer_version": 2,
		})
		capableDone <- result{status: resp.StatusCode, body: proseResponseBody(t, resp)}
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("version-2 save did not reach the write transaction")
	}

	legacyDone := make(chan result, 1)
	go func() {
		resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, legacy)
		legacyDone <- result{status: resp.StatusCode, body: proseResponseBody(t, resp)}
	}()
	select {
	case early := <-legacyDone:
		t.Fatalf("legacy save finished while the version-2 transaction held the writer lock: %d %s", early.status, early.body)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)

	var capable result
	select {
	case capable = <-capableDone:
	case <-time.After(10 * time.Second):
		t.Fatal("version-2 save did not finish")
	}
	var stripped result
	select {
	case stripped = <-legacyDone:
	case <-time.After(10 * time.Second):
		t.Fatal("legacy save did not finish")
	}
	if capable.status != 200 {
		t.Fatalf("version-2 save = %d %s", capable.status, capable.body)
	}
	if stripped.status != 409 || !strings.Contains(stripped.body, "neu laden") {
		t.Fatalf("overlapping legacy save = %d %s", stripped.status, stripped.body)
	}
	resp = ts.get(t, "/api/integrations/crm/offers", ts.adminCookie)
	var saved handlers.OfferSettings
	decode(t, resp, &saved)
	if len(saved.Defaults.Blocks) != 1 || saved.Defaults.Blocks[0].Nodes[0].Depth != 1 || saved.Defaults.Blocks[0].Nodes[0].Text != "Erste Ebene" {
		t.Fatalf("overlapping legacy save stripped the template: %#v", saved.Defaults.Blocks)
	}
}

func TestOfferInlineMarksNeedWriterThree(t *testing.T) {
	ts := newTestServer(t)
	settings := offerSettingsFixture()
	resp := ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, settings)
	assertStatus(t, resp, 200)
	resp.Body.Close()
	resp = ts.post(t, "/api/customers", ts.adminCookie, map[string]any{"name": "Testkunde", "address": "Gasse 2\n1010 Wien", "contact_name": "Eva Test", "contact_email": "eva@example.test"})
	assertStatus(t, resp, 201)
	var customer struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &customer)
	resp = ts.post(t, "/api/offers", ts.adminCookie, map[string]any{"customer_id": customer.ID})
	assertStatus(t, resp, 201)
	offer := takeOffer(t, resp)
	offer.Document.Positions = []handlers.OfferPosition{{ShortText: "Beratung", Quantity: 1, Unit: "Pauschale", UnitPriceCents: 10000}}
	path := "/api/offers/" + jsonNumber(offer.ID)
	styled := offer.Document
	styled.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Nodes: []handlers.OfferTextNode{{
		Kind: "paragraph", Text: "Hallo", Marks: []handlers.OfferInlineMark{{Start: 0, End: 2, Bold: true, Italic: true}},
	}}}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 2, "document": styled})
	body := proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("version 2 stored marks: %d %s", resp.StatusCode, body)
	}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 3, "document": styled})
	assertStatus(t, resp, 200)
	offer = takeOffer(t, resp)
	mark := offer.Document.Blocks[0].Nodes[0].Marks
	if offer.Document.Blocks[0].Body != "Hallo" || len(mark) != 1 || !mark[0].Bold || !mark[0].Italic || mark[0].End != 2 {
		t.Fatalf("stored marks = %#v", offer.Document.Blocks[0])
	}
	stripped := offer.Document
	stripped.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Body: "Hallo"}}
	resp = ts.put(t, path, ts.adminCookie, map[string]any{"revision": offer.Revision, "prose_writer_version": 2, "document": stripped})
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("version 2 stripped marks: %d %s", resp.StatusCode, body)
	}
	resp = ts.get(t, path, ts.adminCookie)
	offer = takeOffer(t, resp)
	if len(offer.Document.Blocks[0].Nodes[0].Marks) != 1 || !offer.Document.Blocks[0].Nodes[0].Marks[0].Bold {
		t.Fatalf("marks were erased: %#v", offer.Document.Blocks[0])
	}

	settings.Defaults.Blocks = styled.Blocks
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, map[string]any{
		"sender": settings.Sender, "defaults": settings.Defaults, "prose_writer_version": 2,
	})
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("template writer 2: %d %s", resp.StatusCode, body)
	}
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, map[string]any{
		"sender": settings.Sender, "defaults": settings.Defaults, "prose_writer_version": 3,
	})
	assertStatus(t, resp, 200)
	resp.Body.Close()
	plain := settings
	plain.Defaults.Blocks = []handlers.OfferBlock{{Heading: "Leistung", Body: "Klartext"}}
	resp = ts.put(t, "/api/integrations/crm/offers", ts.adminCookie, map[string]any{
		"sender": plain.Sender, "defaults": plain.Defaults, "prose_writer_version": 2,
	})
	body = proseResponseBody(t, resp)
	if resp.StatusCode != 409 || !strings.Contains(body, "neu laden") {
		t.Fatalf("template strip: %d %s", resp.StatusCode, body)
	}
}
