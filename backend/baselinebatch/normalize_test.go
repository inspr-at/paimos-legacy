// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestNormalizeDraftEmptyLists(t *testing.T) {
	d := Draft{}
	normalizeDraft(&d)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, field := range []string{`"unresolved":null`, `"requirements":null`, `"constraints":null`, `"requirement_refs":null`, `"constraint_refs":null`} {
		if strings.Contains(body, field) {
			t.Fatalf("normalized draft still has %s in %s", field, body)
		}
	}
	if d.Unresolved == nil || d.Requirements == nil || d.Constraints == nil {
		t.Fatalf("normalize left nil slices: %+v", d)
	}
}

func TestNormalizeWorkflowEmptyLists(t *testing.T) {
	w := Workflow{Draft: &Draft{}}
	normalizeWorkflow(&w)
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, `"unresolved":null`) || strings.Contains(body, `"batches":null`) || strings.Contains(body, `"runtimes":null`) {
		t.Fatalf("normalized workflow still has null lists: %s", body)
	}
}

func TestNormalizeWorkflowKeepsV3AccountScopesUnflattened(t *testing.T) {
	w := Workflow{Choices: WorkflowChoices{Runtimes: []RuntimeChoice{{
		RuntimeID: "runtime", RuntimeGeneration: "gen", SchemaVersion: 3,
		AccountScopes: []lifecycleintents.AccountScope{
			{AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "codex-work", Label: "Work"}}, Profiles: []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}}},
			{AccountLabel: "cursor_context", Accounts: []lifecycleintents.AccountChoice{{Key: "cursor-op", Label: "Cursor"}}, Profiles: []lifecycleintents.Profile{{ID: "cursor-composer", Version: "2"}}},
		},
	}}}}
	normalizeWorkflow(&w)
	if len(w.Choices.Runtimes) != 1 || len(w.Choices.Runtimes[0].AccountScopes) != 2 || len(w.Choices.Runtimes[0].Accounts) != 0 || len(w.Choices.Runtimes[0].Profiles) != 0 {
		t.Fatalf("v3 choice flattened: %+v", w.Choices.Runtimes[0])
	}
}
