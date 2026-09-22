// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

// PAI-332. Pin the public-registry endpoint contract:
//
//   - GET /api/registry/adapters returns 200 + JSON.
//   - The response carries protocol_version="1" and a count.
//   - The bundled claude-code adapter is listed with a v1 manifest.
//   - Non-passing-conformance entries are hidden by default but
//     included when ?include=pending is set (so adapter authors can
//     verify their PR landed even before they pass conformance).
//   - The response shape parses as the same v1 manifest the
//     conformance suite + `paimos skill list-adapters` consume — no
//     wire-format drift between the three surfaces.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters"
	"github.com/inspr-at/paimos/backend/cmd/paimos/adapters/native"
	"github.com/inspr-at/paimos/backend/handlers"
)

type registryEntry struct {
	Manifest    map[string]any `json:"manifest"`
	Homepage    string         `json:"homepage"`
	Source      string         `json:"source"`
	Maintainer  string         `json:"maintainer"`
	Conformance struct {
		Passes       bool   `json:"passes"`
		LastVerified string `json:"last_verified"`
		Note         string `json:"note"`
	} `json:"conformance"`
}

type registryResponse struct {
	ProtocolVersion string          `json:"protocol_version"`
	GeneratedAt     string          `json:"generated_at"`
	Count           int             `json:"count"`
	Adapters        []registryEntry `json:"adapters"`
}

func fetchRegistry(t *testing.T, query string) registryResponse {
	t.Helper()
	url := "/api/registry/adapters"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	handlers.GetAdapterRegistry(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got == "" {
		t.Fatal("missing Content-Type")
	}
	var out registryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	return out
}

func TestAdapterRegistry_DefaultListsClaudeCode(t *testing.T) {
	resp := fetchRegistry(t, "")
	if resp.ProtocolVersion != "1" {
		t.Fatalf("protocol_version=%q want 1", resp.ProtocolVersion)
	}
	if resp.Count == 0 {
		t.Fatalf("expected at least one adapter in registry; got 0")
	}
	if resp.Count != len(resp.Adapters) {
		t.Fatalf("count=%d but adapters has %d", resp.Count, len(resp.Adapters))
	}

	var foundClaude bool
	for _, e := range resp.Adapters {
		if name, _ := e.Manifest["name"].(string); name == "claude-code" {
			foundClaude = true
			if pv, _ := e.Manifest["protocol_version"].(string); pv != "1" {
				t.Fatalf("claude-code manifest protocol_version=%q want 1", pv)
			}
			if sup, _ := e.Manifest["supports"].(string); sup == "" {
				t.Fatal("claude-code manifest missing supports range")
			}
			if !e.Conformance.Passes {
				t.Fatalf("claude-code conformance should pass in registry")
			}
		}
	}
	if !foundClaude {
		t.Fatalf("claude-code not listed; got %v", resp.Adapters)
	}
}

func TestAdapterRegistry_HidesPendingByDefault(t *testing.T) {
	def := fetchRegistry(t, "")
	pending := fetchRegistry(t, "include=pending")
	if pending.Count <= def.Count {
		t.Fatalf("?include=pending should expose more entries: default=%d pending=%d",
			def.Count, pending.Count)
	}
	// The opencode stub in the static file is conformance.passes=false
	// — verify it shows up only with include=pending.
	hasOpencode := func(rs registryResponse) bool {
		for _, e := range rs.Adapters {
			if name, _ := e.Manifest["name"].(string); name == "opencode" {
				return true
			}
		}
		return false
	}
	if hasOpencode(def) {
		t.Fatal("default registry must hide non-passing entries (opencode stub leaked)")
	}
	if !hasOpencode(pending) {
		t.Fatal("?include=pending must expose the opencode stub")
	}
}

// TestAdapterRegistry_StableOrdering: deterministic name-sorted order
// keeps client-side ETag caching simple.
func TestAdapterRegistry_StableOrdering(t *testing.T) {
	resp := fetchRegistry(t, "include=pending")
	prev := ""
	for _, e := range resp.Adapters {
		name, _ := e.Manifest["name"].(string)
		if prev != "" && name < prev {
			t.Fatalf("adapters not sorted by name: %q < %q", name, prev)
		}
		prev = name
	}
}

func TestNativeAdapterRegistryMatchesBuiltIns(t *testing.T) {
	response := fetchRegistry(t, "")
	for _, adapter := range native.All() {
		found := false
		for _, entry := range response.Adapters {
			if entry.Manifest["name"] == adapter.Name() {
				found = true
				expected := adapters.ManifestOf(adapter)
				raw, _ := json.Marshal(expected)
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				for key, value := range fields {
					if entry.Manifest[key] != value {
						t.Fatalf("%s %s mismatch", adapter.Name(), key)
					}
				}
			}
		}
		if !found {
			t.Fatalf("missing %s", adapter.Name())
		}
	}
}
