// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

type modelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModelResolveCLIOnlineAndOfflineCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envURL, "https://models.example.invalid")
	t.Setenv(envAPIKey, "fixture")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	registry := dispatchprofile.NewRegistry([]dispatchprofile.Override{{ProfileID: "claude-fable-xhigh", Version: dispatchprofile.CatalogVersion, State: "conserved", Reason: "instance allowance", Until: time.Now().Add(time.Hour)}})
	http.DefaultTransport = modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var value any
		switch r.URL.Path {
		case "/api/projects":
			value = []map[string]any{{"id": 7, "key": "PAI", "name": "Paimos"}}
		case "/api/projects/7":
			value = map[string]any{"id": 7, "key": "PAI", "name": "Paimos"}
		case "/api/models/catalog":
			value = registry
		case "/api/models/resolve":
			q := r.URL.Query()
			resolved, err := registry.Resolve(dispatchprofile.ResolveRequest{Role: q.Get("role"), AuthorFamily: dispatchprofile.Family(q.Get("author_family")), Harness: q.Get("harness")}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			value = resolved
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		body, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	if _, _, err := executeCLIForTest(t, "sync", "pull", "--kind", "model_catalog", "--project", "PAI", "--workspace", root); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeCLIForTest(t, "model", "resolve", "review-gate", "--author-family", "anthropic", "--workspace", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result dispatchprofile.Resolution
	if json.Unmarshal([]byte(out), &result) != nil || result.Commands == nil || !strings.Contains(result.Commands.Resume, "--sandbox read-only") || result.Stale || result.Profile.ID != "codex-astra-xhigh" || !result.Role.ReadOnly || !strings.Contains(result.Command, "--sandbox read-only") {
		t.Fatalf("online %s", out)
	}
	http.DefaultTransport = modelRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "offline", Name: "models.example.invalid"}}
	})
	out, _, err = executeCLIForTest(t, "model", "resolve", "review-gate", "--author-family", "openai", "--workspace", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(out), &result) != nil || result.Commands == nil || !strings.Contains(result.Commands.Spawn, "--permission-mode plan") || !result.Stale || result.Source != "cache" || result.CachedAt == nil || result.Profile.ID != "claude-opus-xhigh" || !result.Role.ReadOnly || !strings.Contains(result.Command, "--permission-mode plan") {
		t.Fatalf("offline %s", out)
	}
	// The same name at a different origin must not consume the first instance's policy.
	t.Setenv(envURL, "https://different.example.invalid")
	if _, _, err = executeCLIForTest(t, "model", "resolve", "build", "--workspace", root); err == nil {
		t.Fatal("cache crossed instance origin")
	}
	t.Setenv(envURL, "https://models.example.invalid")
	http.DefaultTransport = modelRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"denied"}`))}, nil
	})
	if _, _, err = executeCLIForTest(t, "model", "resolve", "build", "--workspace", root); err == nil {
		t.Fatal("HTTP denial used offline cache")
	}
}

func TestWorkerModelRolePreservesHierarchy(t *testing.T) {
	o := friendlyStartOptions{Role: "build-hard"}
	normalizeFriendlyModelRole(&o)
	if o.Role != "worker" || o.ModelRole != "build-hard" {
		t.Fatalf("%+v", o)
	}
	o = friendlyStartOptions{Role: "review-gate", Coordinator: true}
	normalizeFriendlyModelRole(&o)
	if o.Role != "review-gate" || o.ModelRole != "" {
		t.Fatal("orchestrator accepted model role")
	}
}

func TestWorkerStartResolvesModelRoleBeforeBuildingRequest(t *testing.T) {
	client := newClient(InstanceConfig{URL: "https://models.example.invalid"})
	client.http.Transport = modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var value any
		switch r.URL.Path {
		case "/api/projects":
			value = []orchestratorProject{{ID: 42, Key: "PAI"}}
		case "/api/projects/42/agents":
			value = []map[string]any{{"id": 7, "project_id": 42, "name": "builder"}}
		case "/api/ai/execution-options":
			value = map[string]any{"dispatch_profiles": dispatchprofile.List()}
		case "/api/models/resolve":
			q := r.URL.Query()
			result, err := dispatchprofile.NewRegistry(nil).Resolve(dispatchprofile.ResolveRequest{Role: q.Get("role"), AuthorFamily: dispatchprofile.Family(q.Get("author_family")), Harness: q.Get("harness")}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			value = result
		case "/api/issues/PAI-1049":
			value = map[string]any{"id": 1049, "project_id": 42}
		case "/api/projects/42/harness-sessions":
			value = []map[string]any{{"id": friendlyParentID, "project_id": 42, "agent_name": "root", "harness": "codex", "role": "coordinator", "phase": "working"}}
		case "/api/projects/42/agents/builder.json":
			value = map[string]any{"project": map[string]any{"id": 42, "key": "PAI"}, "agent": map[string]any{"name": "builder", "project_id": 42}}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		body, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	opts := friendlyStartOptions{Project: "PAI", Agent: "builder", Ticket: "PAI-1049", Shape: "ship", Parent: friendlyParentID, Role: "build", Workspace: t.TempDir()}
	normalizeFriendlyModelRole(&opts)
	plan, err := resolveFriendlyStart(context.Background(), client, opts, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ModelResolution == nil || plan.Profile.ID != "codex-terra-high" || plan.request.Role != "worker" || plan.request.DispatchProfileVersion != dispatchprofile.CatalogVersion {
		t.Fatalf("plan %+v", plan)
	}
	opts.Profile = "codex-astra-xhigh@" + dispatchprofile.CatalogVersion
	if _, err = resolveFriendlyStart(context.Background(), client, opts, ""); err == nil {
		t.Fatal("explicit pin overrode role policy")
	}
}

func TestWorkerModelSelectorsAcceptCatalogHarnesses(t *testing.T) {
	for _, harness := range []string{"pi", "cursor"} {
		o := friendlyStartOptions{Project: "PAI", Agent: "builder", Ticket: "PAI-1049", Shape: "ship", Parent: friendlyParentID, Role: "build", Harness: harness, Key: "test-start", ExpectedRevision: -1}
		if harness == "pi" {
			o.Model = "anthropic/claude-sonnet-5"
		}
		normalizeFriendlyModelRole(&o)
		if err := validateFriendlyStart(o); err != nil {
			t.Fatal(err)
		}
	}
}

func TestModelTemplatesOfflineDiscovery(t *testing.T) {
	for _, harness := range []string{"codex", "claude-code", "grok", "pi", "cursor"} {
		out, _, err := executeCLIForTest(t, "model", "templates", harness, "--read-only", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var result dispatchprofile.CommandTemplates
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if result.Run == "" || result.Review == "" || result.Resume == "" || result.Spawn == "" {
			t.Fatal(out)
		}
	}
	if _, _, err := executeCLIForTest(t, "model", "templates", "invalid"); err == nil {
		t.Fatal("unknown harness accepted")
	}
}
