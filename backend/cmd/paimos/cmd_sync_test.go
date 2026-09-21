// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

// PAI-331. End-to-end CLI tests for `paimos sync init/pull/check` and
// the convenience `paimos skill init/pull/check` wrappers. Watch is
// covered separately because its long-lived loop needs a server-driven
// disconnect to assert termination.

import (
	"encoding/json"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeAgentsListJSON = `[{"name":"qa"},{"name":"ops"}]`

func startFakeSyncAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/models/catalog":
			_ = json.NewEncoder(w).Encode(dispatchprofile.NewRegistry(nil))
		case "/api/projects":
			_, _ = w.Write([]byte(`[{"id":7,"key":"ACME","name":"Acme Widgets"}]`))
		case "/api/projects/7/agents":
			_, _ = w.Write([]byte(fakeAgentsListJSON))
		case "/api/projects/7/agents/qa.json":
			_, _ = w.Write([]byte(strings.ReplaceAll(fakeArtifactJSON, `"name": "qa"`, `"name": "qa"`)))
		case "/api/projects/7/agents/ops.json":
			body := strings.ReplaceAll(fakeArtifactJSON, `"name": "qa"`, `"name": "ops"`)
			body = strings.ReplaceAll(body, `"slash_command_name": "qa"`, `"slash_command_name": "ops"`)
			_, _ = w.Write([]byte(body))
		// PAI-394 — unified knowledge surface. The init/pull/check
		// verbs iterate every registered kind; each fetches via
		// `/api/projects/{id}/knowledge?type=<seg>`, which arrives
		// here as a single Path with the discriminator in the
		// query string. One case covers all five types.
		case "/api/projects/7/knowledge":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, `{"error":"unexpected route: `+r.URL.Path+`"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSyncInit_PullsAllAgents(t *testing.T) {
	srv := startFakeSyncAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	work := t.TempDir()
	if _, _, err := executeCLIForTest(t,
		"sync", "init",
		"--project", "ACME",
		"--workspace", work,
	); err != nil {
		t.Fatalf("sync init: %v", err)
	}

	for _, name := range []string{"qa", "ops"} {
		path := filepath.Join(work, ".claude", "commands", name+".md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
		if !strings.HasPrefix(string(body), "<!-- paimos: rendered from ACME/"+name+"@") {
			t.Errorf("%s missing canonical header: %.80q", path, string(body))
		}
	}
}

func TestSyncPull_KindAndNameNarrow(t *testing.T) {
	srv := startFakeSyncAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	work := t.TempDir()
	if _, _, err := executeCLIForTest(t,
		"sync", "pull",
		"--project", "ACME",
		"--workspace", work,
		"--kind", "skill",
		"--name", "qa",
	); err != nil {
		t.Fatalf("sync pull: %v", err)
	}

	if _, err := os.Stat(filepath.Join(work, ".claude", "commands", "qa.md")); err != nil {
		t.Fatalf("qa.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".claude", "commands", "ops.md")); !os.IsNotExist(err) {
		t.Errorf("ops.md should NOT be written when --name=qa: %v", err)
	}
}

func TestSyncPull_NameRequiresKind(t *testing.T) {
	srv := startFakeSyncAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	_, _, err := executeCLIForTest(t,
		"sync", "pull",
		"--project", "ACME",
		"--name", "qa",
	)
	if err == nil {
		t.Fatal("expected usage error for --name without --kind")
	}
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("err type %T, want *usageError", err)
	}
}

func TestSyncCheck_NoDriftExitsZero(t *testing.T) {
	srv := startFakeSyncAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	work := t.TempDir()
	// Seed the local cache.
	if _, _, err := executeCLIForTest(t, "sync", "init",
		"--project", "ACME", "--workspace", work,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, _, err := executeCLIForTest(t, "sync", "check",
		"--project", "ACME", "--workspace", work,
	)
	if err != nil {
		t.Fatalf("sync check should succeed: %v", err)
	}
	if !strings.Contains(out, "no drift") {
		t.Errorf("stdout should report no drift:\n%s", out)
	}
}

func TestSyncCheck_DriftExits1(t *testing.T) {
	srv := startFakeSyncAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	work := t.TempDir()
	if _, _, err := executeCLIForTest(t, "sync", "init",
		"--project", "ACME", "--workspace", work,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Mutate one of the rendered files to force drift.
	target := filepath.Join(work, ".claude", "commands", "qa.md")
	if err := os.WriteFile(target,
		[]byte("<!-- paimos: rendered from ACME/qa@stale harness=claude-code -->\n\nedited\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeCLIForTest(t, "sync", "check",
		"--project", "ACME", "--workspace", work,
	)
	if err == nil {
		t.Fatal("sync check should report drift")
	}
	ce, ok := err.(*checkExitCode)
	if !ok {
		t.Fatalf("err type %T, want *checkExitCode", err)
	}
	if ce.code != 1 {
		t.Errorf("exit = %d, want 1", ce.code)
	}
}

func TestSkillInit_ConvenienceWrapper(t *testing.T) {
	srv := startFakeSyncAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	work := t.TempDir()
	if _, _, err := executeCLIForTest(t, "skill", "init",
		"--project", "ACME", "--workspace", work,
	); err != nil {
		t.Fatalf("skill init: %v", err)
	}
	// Convenience wrapper should produce same effect as `sync init --kind=skill`.
	if _, err := os.Stat(filepath.Join(work, ".claude", "commands", "qa.md")); err != nil {
		t.Fatalf("qa.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".claude", "commands", "ops.md")); err != nil {
		t.Fatalf("ops.md not written: %v", err)
	}
}

func TestSkillPull_AgentFlagAlias(t *testing.T) {
	srv := startFakeSyncAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	work := t.TempDir()
	if _, _, err := executeCLIForTest(t, "skill", "pull",
		"--project", "ACME",
		"--workspace", work,
		"--agent", "qa",
	); err != nil {
		t.Fatalf("skill pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".claude", "commands", "qa.md")); err != nil {
		t.Fatalf("qa.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".claude", "commands", "ops.md")); !os.IsNotExist(err) {
		t.Errorf("ops.md should NOT be written when --agent=qa: %v", err)
	}
}

func TestSyncInit_RequiresProject(t *testing.T) {
	t.Setenv(envURL, "https://example.test")
	t.Setenv(envAPIKey, "test_key")
	_, _, err := executeCLIForTest(t, "sync", "init")
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("err type %T, want *usageError", err)
	}
}

// TestSyncInit_PullsKnowledgePlaneEndToEnd is the PAI-341 acceptance
// check the ticket calls out: `paimos sync init --project PAI` should
// pull skill files plus all five knowledge kinds in one shot. The fake
// server returns one entry per knowledge kind and two skills; the test
// asserts every cache directory ends up populated.
func TestSyncInit_PullsKnowledgePlaneEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/models/catalog":
			_ = json.NewEncoder(w).Encode(dispatchprofile.NewRegistry(nil))
		case "/api/projects":
			_, _ = w.Write([]byte(`[{"id":7,"key":"ACME","name":"Acme"}]`))
		case "/api/projects/7/agents":
			_, _ = w.Write([]byte(fakeAgentsListJSON))
		case "/api/projects/7/agents/qa.json":
			_, _ = w.Write([]byte(fakeArtifactJSON))
		case "/api/projects/7/agents/ops.json":
			body := strings.ReplaceAll(fakeArtifactJSON, `"name": "qa"`, `"name": "ops"`)
			body = strings.ReplaceAll(body, `"slash_command_name": "qa"`, `"slash_command_name": "ops"`)
			_, _ = w.Write([]byte(body))
		case "/api/projects/7/knowledge":
			// PAI-394 unified surface — type comes in on ?type=<seg>.
			switch r.URL.Query().Get("type") {
			case "memory":
				_, _ = w.Write([]byte(`[{"id":1,"project_id":7,"type":"memory","slug":"feedback_x","title":"Mem","body":"m","status":"backlog","metadata":{},"created_at":"","updated_at":""}]`))
			case "runbook":
				_, _ = w.Write([]byte(`[{"id":2,"project_id":7,"type":"runbook","slug":"deploy","title":"Run","body":"r","status":"backlog","metadata":{},"created_at":"","updated_at":""}]`))
			case "external-system":
				_, _ = w.Write([]byte(`[{"id":3,"project_id":7,"type":"external_system","slug":"ch","title":"Ext","body":"e","status":"backlog","metadata":{},"created_at":"","updated_at":""}]`))
			case "related-project":
				_, _ = w.Write([]byte(`[{"id":4,"project_id":7,"type":"related_project","slug":"frontend","title":"Rel","body":"l","status":"backlog","metadata":{},"created_at":"","updated_at":""}]`))
			case "guideline":
				_, _ = w.Write([]byte(`[{"id":5,"project_id":7,"type":"guideline","slug":"no-secrets","title":"Gd","body":"g","status":"backlog","metadata":{},"created_at":"","updated_at":""}]`))
			default:
				http.Error(w, `{"error":"unexpected ?type: `+r.URL.Query().Get("type")+`"}`, http.StatusNotFound)
			}
		default:
			http.Error(w, `{"error":"unexpected route: `+r.URL.Path+`"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	work := t.TempDir()
	if _, _, err := executeCLIForTest(t,
		"sync", "init",
		"--project", "ACME",
		"--workspace", work,
	); err != nil {
		t.Fatalf("sync init: %v", err)
	}

	// Skill files land under .claude/commands.
	for _, name := range []string{"qa", "ops"} {
		if _, err := os.Stat(filepath.Join(work, ".claude", "commands", name+".md")); err != nil {
			t.Errorf("skill %s missing: %v", name, err)
		}
	}
	// Knowledge files land under .paimos/cache/<project>/<subdir>/<slug>.md.
	knowledgeChecks := map[string]string{
		"memory":           "feedback_x",
		"runbooks":         "deploy",
		"external-systems": "ch",
		"related-projects": "frontend",
		"guidelines":       "no-secrets",
	}
	for subdir, slug := range knowledgeChecks {
		identity, err := newInstanceIdentity("env", srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(work, ".paimos", "cache", "instances", identity.Namespace, "ACME", subdir, slug+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("knowledge %s/%s missing: %v", subdir, slug, err)
		}
	}
}
