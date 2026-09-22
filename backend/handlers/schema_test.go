// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package handlers_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/backend/handlers"
	"github.com/inspr-at/paimos/backend/handlers/knowledge"
)

// TestSchemaPayloadHash is the "nobody edits the schema without bumping
// the version" regression. When this fails:
//
//  1. If you INTENDED to change the schema: bump handlers.SchemaVersion
//     (in backend/handlers/schema.go) AND update expectedHash below in
//     the SAME commit, so there's a single reviewable diff.
//  2. If you DID NOT intend to change it: revert the change.
//
// The hash is computed over the marshaled schemaJSON bytes (including the
// version string), so a version bump alone also shifts it.
func TestSchemaPayloadHash(t *testing.T) {
	const expectedVersion = "2.10.0"
	const expectedHash = "7a179cf6ab2f3a6a5e97072acfe2c0a70c3045342c02855a362251b03127a5f5"

	if handlers.SchemaVersion != expectedVersion {
		t.Errorf("SchemaVersion = %q, test expects %q — update either the code or the test constant",
			handlers.SchemaVersion, expectedVersion)
	}

	// Hash the canonical bytes the handler ships.
	req := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
	rec := httptest.NewRecorder()
	handlers.GetAPISchema(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAPISchema: code=%d, want 200", rec.Code)
	}
	h := sha256.Sum256(rec.Body.Bytes())
	got := hex.EncodeToString(h[:])

	if expectedHash == "cafe-placeholder-update-on-first-run" {
		t.Logf("first run — record this hash in expectedHash: %s", got)
		return
	}
	if got != expectedHash {
		t.Errorf("schema payload hash drifted.\n"+
			"  got:      %s\n"+
			"  expected: %s\n"+
			"If the change is intentional, bump SchemaVersion in schema.go "+
			"AND update expectedHash in this test.", got, expectedHash)
	}
}

// TestSchemaKnowledgeBlockMatchesRegistry asserts the discoverable
// `knowledge` block lists every registered Module and that each
// row's (type, url_segment, label, default_status) matches the
// runtime values. PAI-394 collapsed five URLs into one resource;
// this test makes sure the discoverable description and the actual
// routing logic stay in lockstep so a future Module addition can't
// silently miss the schema.
func TestSchemaKnowledgeBlockMatchesRegistry(t *testing.T) {
	block := handlers.Schema.Knowledge
	if block == nil {
		t.Fatal("Schema.Knowledge missing — PAI-394 regression")
	}
	wantTypes := knowledge.AllTypes()
	if len(block.Types) != len(wantTypes) {
		t.Fatalf("knowledge.types length = %d, want %d", len(block.Types), len(wantTypes))
	}
	for i, typ := range wantTypes {
		row := block.Types[i]
		if row.Type != typ {
			t.Errorf("knowledge.types[%d].type = %q, want %q", i, row.Type, typ)
		}
		mod, err := knowledge.RouteByType(typ)
		if err != nil {
			t.Fatalf("RouteByType(%q): %v", typ, err)
		}
		if row.URLSegment != knowledge.URLSegmentForType(typ) {
			t.Errorf("knowledge.types[%d].url_segment = %q, want %q", i, row.URLSegment, knowledge.URLSegmentForType(typ))
		}
		if row.Label != mod.Label() {
			t.Errorf("knowledge.types[%d].label = %q, want %q", i, row.Label, mod.Label())
		}
		if row.DefaultStatus != mod.DefaultStatus() {
			t.Errorf("knowledge.types[%d].default_status = %q, want %q", i, row.DefaultStatus, mod.DefaultStatus())
		}
	}
	// The enums mirror must list the same types — single source of
	// truth check at both ends.
	enumTypes := handlers.Schema.Enums["knowledge_types"]
	if len(enumTypes) != len(wantTypes) {
		t.Fatalf("enums.knowledge_types length = %d, want %d", len(enumTypes), len(wantTypes))
	}
	for i, typ := range wantTypes {
		if enumTypes[i] != typ {
			t.Errorf("enums.knowledge_types[%d] = %q, want %q", i, enumTypes[i], typ)
		}
	}
	// Required routes must be advertised. Specific routes are an
	// API contract; any rename here would silently break clients.
	for _, key := range []string{"list", "filter", "get", "rev", "create", "update", "delete"} {
		if _, ok := block.Routes[key]; !ok {
			t.Errorf("knowledge.routes[%q] missing", key)
		}
	}
}

// TestSchemaTagColorsMatchValidator asserts the discoverable
// tag_colors enum equals the server-side TagColorPalette
// element-for-element, including order. The whole point of PAI-393
// is that the schema and the validator can't drift; this test makes
// the contract explicit so a future edit to one without the other
// fails CI.
func TestSchemaTagColorsMatchValidator(t *testing.T) {
	got, ok := handlers.Schema.Enums["tag_colors"]
	if !ok {
		t.Fatal("Schema.Enums[\"tag_colors\"] missing — PAI-393 regression")
	}
	want := handlers.TagColorPalette
	if len(got) != len(want) {
		t.Fatalf("tag_colors length = %d, want %d (drift between schema and validator)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag_colors[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Membership round-trip: every published value must be accepted
	// by the validator. Catches the case where someone adds a value
	// to the slice but forgets to rebuild validColorSet.
	for _, c := range got {
		if !handlers.IsValidTagColor(c) {
			t.Errorf("tag_colors lists %q but IsValidTagColor rejects it", c)
		}
	}
}

// TestSchemaEnumFieldsResolve asserts every published field binding points
// at a real enum domain. The binding map is how clients and agents know
// which schema enum validates which request field.
func TestSchemaEnumFieldsResolve(t *testing.T) {
	want := map[string]string{
		"issue.type":                             "type",
		"issue.status":                           "status",
		"issue.priority":                         "priority",
		"relation.type":                          "relation",
		"tag.color":                              "tag_colors",
		"knowledge.type":                         "knowledge_types",
		"knowledge.status":                       "knowledge_status",
		"external_stage_handoff.state":           "external_stage_handoff_state",
		"external_stage_handoff.reporter_class":  "external_stage_reporter_class",
		"external_stage_handoff.reporter_role":   "external_stage_reporter_role",
		"external_stage_evidence.kind":           "external_stage_evidence_kind",
		"external_stage_artifact.version_scheme": "external_stage_version_scheme",
	}
	for binding, domain := range want {
		got, ok := handlers.Schema.EnumFields[binding]
		if !ok {
			t.Errorf("enum_fields[%q] missing", binding)
			continue
		}
		if got != domain {
			t.Errorf("enum_fields[%q] = %q, want %q", binding, got, domain)
		}
		if len(handlers.Schema.Enums[got]) == 0 {
			t.Errorf("enum_fields[%q] points at empty or unknown enum domain %q", binding, got)
		}
	}
	for binding, domain := range handlers.Schema.EnumFields {
		if len(handlers.Schema.Enums[domain]) == 0 {
			t.Errorf("enum_fields[%q] points at empty or unknown enum domain %q", binding, domain)
		}
	}
}

// TestSchemaTransitionsCoverAllStatuses ensures every status in enums has
// a transitions entry (even terminal ones, which have an empty list).
// Catches the "added a new status, forgot to wire it into transitions"
// class of bug.
func TestSchemaTransitionsCoverAllStatuses(t *testing.T) {
	statuses := handlers.Schema.Enums["status"]
	trans := handlers.Schema.Transitions["status"]
	for _, s := range statuses {
		if _, ok := trans[s]; !ok {
			t.Errorf("status %q has no transitions entry — add it (possibly empty) to Schema.Transitions[\"status\"]", s)
		}
	}
	// Reverse check: no phantom transitions from a status not in enums.
	for k := range trans {
		found := false
		for _, s := range statuses {
			if s == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("transitions from %q but that status isn't in enums.status — did the enum rename?", k)
		}
	}
}

// TestSchemaTransitionTargetsAreKnown ensures every target status listed
// in transitions is itself a valid status enum value. No silent typos.
func TestSchemaTransitionTargetsAreKnown(t *testing.T) {
	statuses := handlers.Schema.Enums["status"]
	known := map[string]bool{}
	for _, s := range statuses {
		known[s] = true
	}
	for from, targets := range handlers.Schema.Transitions["status"] {
		for _, to := range targets {
			if !known[to] {
				t.Errorf("transition %q → %q points at unknown status", from, to)
			}
		}
	}
}

// TestGetAPISchemaETag_ConditionalGET asserts the 304 Not Modified path.
func TestGetAPISchemaETag_ConditionalGET(t *testing.T) {
	// First request: grab ETag.
	req := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
	rec := httptest.NewRecorder()
	handlers.GetAPISchema(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first GET: code=%d, want 200", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first GET: ETag header missing")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control = %q, want \"public, max-age=300\"", cc)
	}
	if v := rec.Header().Get("X-Schema-Version"); v != handlers.SchemaVersion {
		t.Errorf("X-Schema-Version = %q, want %q", v, handlers.SchemaVersion)
	}

	// Second request with matching If-None-Match → 304, empty body.
	req2 := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	handlers.GetAPISchema(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("second GET with If-None-Match: code=%d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 response has a body (%d bytes); should be empty", rec2.Body.Len())
	}

	// Stale If-None-Match → 200 again.
	req3 := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
	req3.Header.Set("If-None-Match", `"stale-etag"`)
	rec3 := httptest.NewRecorder()
	handlers.GetAPISchema(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("stale If-None-Match: code=%d, want 200", rec3.Code)
	}

	// Strong-form If-None-Match should also match the weak server ETag
	// (RFC 7232 §2.3.2). Compression middleware in prod may add/remove
	// the W/ prefix in flight, so the comparison has to be lenient.
	stripped := etag
	if len(stripped) > 2 && stripped[:2] == "W/" {
		stripped = stripped[2:]
	}
	req4 := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
	req4.Header.Set("If-None-Match", stripped)
	rec4 := httptest.NewRecorder()
	handlers.GetAPISchema(rec4, req4)
	if rec4.Code != http.StatusNotModified {
		t.Errorf("strong-form If-None-Match against weak server ETag: code=%d, want 304 (RFC 7232 weak-compare)", rec4.Code)
	}
}

// TestGetAPISchemaDeterministicBytes ensures back-to-back requests
// produce identical bytes (no map-iteration-order bleed into the wire).
func TestGetAPISchemaDeterministicBytes(t *testing.T) {
	body := func() []byte {
		req := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
		rec := httptest.NewRecorder()
		handlers.GetAPISchema(rec, req)
		return rec.Body.Bytes()
	}
	a, b := body(), body()
	if string(a) != string(b) {
		t.Errorf("/api/schema response bytes differ between calls — something non-deterministic leaked in")
	}
}

// TestSchemaJSONParses guards against marshaling silently producing
// something that isn't valid JSON (can't happen unless Go regresses,
// but cheap to assert).
func TestSchemaJSONParses(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
	rec := httptest.NewRecorder()
	handlers.GetAPISchema(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	for _, k := range []string{"version", "enums", "transitions", "entities", "enum_fields", "conventions"} {
		if _, ok := out[k]; !ok {
			t.Errorf("response missing top-level key %q", k)
		}
	}
}
