// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

// PAI-330. End-to-end CLI tests for `paimos skill render` and
// `paimos skill list-adapters`. The fake HTTP server returns a
// canonical artifact resembling PAI-329's shape; the test then
// asserts on the file the CLI wrote (or, in --check mode, on the
// exit code path).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeArtifactJSON = `{
  "project": {"id": 7, "name": "Acme Widgets", "key": "ACME"},
  "agent": {
    "name": "qa",
    "description": "Test the widgets.",
    "slash_command_name": "qa",
    "lane_tags": ["qa"],
    "metadata": {},
    "body": "Ship green or not at all.",
    "bootstrap_steps": [
      {"title": "Run e2e", "command": "npm run e2e", "rationale": "spot regressions early"}
    ],
    "non_negotiable_rules": [
      {"title": "No flaky merges", "body": "Re-run twice before approving.", "memory_ref": "feedback_no_flaky_merges"}
    ]
  },
  "repos": [],
  "environments": [],
  "deploy_recipes": []
}`

// startFakeArtifactAPI serves a single agent artifact for project key
// "ACME" (id 7). Returns a server scoped to the test.
func startFakeArtifactAPI(t *testing.T) *httptest.Server {
	t.Helper()
	// Isolate the test from any real $PAIMOS_ADAPTER_PATH on the
	// developer's box. Hermetic tests don't pick up local installs.
	t.Setenv("PAIMOS_ADAPTER_PATH", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":7,"key":"ACME","name":"Acme Widgets"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/7/agents/qa.json":
			_, _ = w.Write([]byte(fakeArtifactJSON))
		default:
			http.Error(w, `{"error":"unexpected route: `+r.Method+" "+r.URL.Path+`"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSkillRender_WritesFileWithHeader(t *testing.T) {
	srv := startFakeArtifactAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	tmp := t.TempDir()
	out := filepath.Join(tmp, "qa.md")

	if _, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--out", out,
	); err != nil {
		t.Fatalf("render: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read rendered file: %v", err)
	}
	got := string(body)

	// The header line: "<!-- paimos: rendered from ACME/qa@<rev> harness=claude-code -->"
	if !strings.HasPrefix(got, "<!-- paimos: rendered from ACME/qa@") {
		t.Fatalf("header missing or malformed:\n%s", got[:min(200, len(got))])
	}
	if !strings.Contains(got, "harness=claude-code -->") {
		t.Fatalf("harness field missing: %s", got)
	}
	if !strings.Contains(got, "Acme Widgets") {
		t.Fatalf("project name missing: %s", got)
	}
	if !strings.Contains(got, "## Bootstrap") {
		t.Fatal("bootstrap section missing")
	}
	if !strings.Contains(got, "feedback_no_flaky_merges") {
		t.Fatal("memory_ref pass-through missing")
	}
}

func TestSkillRender_UsesAdapterSuggestedPathByDefault(t *testing.T) {
	srv := startFakeArtifactAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	workspace := t.TempDir()

	if _, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--workspace", workspace,
	); err != nil {
		t.Fatalf("render: %v", err)
	}

	suggested := filepath.Join(workspace, ".claude", "commands", "qa.md")
	if _, err := os.Stat(suggested); err != nil {
		t.Fatalf("expected adapter to write to %s: %v", suggested, err)
	}
}

func TestSkillRender_CheckIdenticalExitsZero(t *testing.T) {
	srv := startFakeArtifactAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	tmp := t.TempDir()
	out := filepath.Join(tmp, "qa.md")

	// First, do a real render to produce the canonical file on disk.
	if _, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--out", out,
	); err != nil {
		t.Fatalf("seed render: %v", err)
	}

	// Now --check should declare it identical (no error → exit 0).
	stdoutS, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--out", out,
		"--check",
	)
	if err != nil {
		t.Fatalf("--check identical should be err=nil (exit 0), got %v", err)
	}
	if !strings.Contains(stdoutS, "identical") {
		t.Fatalf("stdout should mention identical, got %q", stdoutS)
	}
}

func TestSkillRender_CheckDiffExits1(t *testing.T) {
	srv := startFakeArtifactAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	tmp := t.TempDir()
	out := filepath.Join(tmp, "qa.md")

	// Seed render then mutate.
	if _, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--out", out,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(out,
		[]byte("<!-- paimos: rendered from ACME/qa@stale harness=claude-code -->\n\nuser-edited content\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--out", out,
		"--check",
	)
	if err == nil {
		t.Fatal("expected non-nil error for diff")
	}
	ce, ok := err.(*checkExitCode)
	if !ok {
		t.Fatalf("err type %T, want *checkExitCode (so main exits with explicit code)", err)
	}
	if ce.code != 1 {
		t.Fatalf("exit code = %d, want 1 for diff", ce.code)
	}
}

func TestSkillRender_CheckHeaderMissingExits2(t *testing.T) {
	srv := startFakeArtifactAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	tmp := t.TempDir()
	out := filepath.Join(tmp, "qa.md")

	// Plant a hand-edited file with NO paimos header.
	if err := os.WriteFile(out, []byte("# QA notes\n\nhand-authored.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--out", out,
		"--check",
	)
	if err == nil {
		t.Fatal("expected non-nil error for missing header")
	}
	ce, ok := err.(*checkExitCode)
	if !ok {
		t.Fatalf("err type %T, want *checkExitCode", err)
	}
	if ce.code != 2 {
		t.Fatalf("exit code = %d, want 2 for header-missing", ce.code)
	}
}

func TestSkillRender_VersionMismatchYieldsClearError(t *testing.T) {
	// Custom server that emits a future canonical_schema_version the
	// claude-code adapter does not support (>=1.0.0 <2.0.0).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/projects":
			_, _ = w.Write([]byte(`[{"id":7,"key":"ACME","name":"Acme"}]`))
		case r.URL.Path == "/api/projects/7/agents/qa.json":
			_, _ = w.Write([]byte(`{
				"canonical_schema_version": "2.5.0",
				"project": {"id": 7, "key": "ACME", "name": "Acme"},
				"agent": {"name": "qa", "slash_command_name": "qa"}
			}`))
		default:
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	tmp := t.TempDir()
	_, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness", "claude-code",
		"--out", filepath.Join(tmp, "qa.md"),
	)
	if err == nil {
		t.Fatal("expected version-mismatch error")
	}
	if !strings.Contains(err.Error(), "claude-code") {
		t.Fatalf("error should name the adapter, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "2.5.0") {
		t.Fatalf("error should name the offending version, got %q", err.Error())
	}
}

func TestSkillListAdapters_IncludesClaudeCode(t *testing.T) {
	out, _, err := executeCLIForTest(t, "skill", "list-adapters")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "claude-code") {
		t.Fatalf("list-adapters should include claude-code:\n%s", out)
	}
	if !strings.Contains(out, ">=1.0.0 <2.0.0") {
		t.Fatalf("list-adapters should show supports range:\n%s", out)
	}
}

func TestSkillListAdapters_JSONShape(t *testing.T) {
	out, _, err := executeCLIForTest(t, "--json", "skill", "list-adapters")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(got) == 0 {
		t.Fatal("no adapters in JSON output")
	}
	found := false
	for _, a := range got {
		if a["name"] == "claude-code" {
			found = true
			if a["supports"] == "" {
				t.Errorf("claude-code supports field is empty")
			}
			if a["version"] == "" {
				t.Errorf("claude-code version field is empty")
			}
		}
	}
	if !found {
		t.Fatalf("claude-code adapter missing from JSON: %v", got)
	}
}

func TestSkillRender_HarnessFromFileEscapeHatch(t *testing.T) {
	srv := startFakeArtifactAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")

	tmp := t.TempDir()
	manifestPath := filepath.Join(tmp, "fake.json")
	manifest := `{
		"name": "fake-harness",
		"version": "0.1.0",
		"supports": ">=1.0.0 <2.0.0",
		"describe": "test-only manifest adapter",
		"suggested_path": ".fake/{{.agent.name}}.txt",
		"body": "FAKE: {{.project.key}}/{{.agent.name}}"
	}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(tmp, "out.txt")
	if _, _, err := executeCLIForTest(t,
		"skill", "render",
		"--project", "ACME",
		"--agent", "qa",
		"--harness-from-file", manifestPath,
		"--out", out,
	); err != nil {
		t.Fatalf("render with manifest: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "FAKE: ACME/qa") {
		t.Fatalf("manifest template did not render: %s", body)
	}
	if !strings.HasPrefix(string(body), "<!-- paimos: rendered from ACME/qa@") {
		t.Fatalf("manifest adapter output missing canonical header: %s", body)
	}
}

func TestNativeSkillRenderAndDriftCheck(t *testing.T) {
	srv := startFakeArtifactAPI(t)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "test_key")
	for harness, root := range map[string]string{"codex": ".agents", "grok": ".grok", "pi": ".pi", "cursor": ".cursor"} {
		t.Run(harness, func(t *testing.T) {
			workspace := t.TempDir()
			args := []string{"skill", "render", "--project", "ACME", "--agent", "qa", "--harness", harness, "--workspace", workspace}
			if _, _, err := executeCLIForTest(t, args...); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workspace, root, "skills", "qa", "SKILL.md")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(body), "---\n") {
				t.Fatal("frontmatter must be first")
			}
			if _, _, err := executeCLIForTest(t, append(args, "--check")...); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(body, []byte("drift")...), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := executeCLIForTest(t, append(args, "--check")...); err == nil {
				t.Fatal("missed drift")
			}
		})
	}
}
