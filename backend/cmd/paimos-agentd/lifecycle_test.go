// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

type lifecycleFixtureAdapter struct {
	mu       sync.Mutex
	requests []agentd.StartRequest
	accounts map[string]bool
}

func (*lifecycleFixtureAdapter) Name() string                        { return "codex" }
func (*lifecycleFixtureAdapter) AccountLabel(context.Context) string { return "chatgpt" }
func (a *lifecycleFixtureAdapter) HasAccount(key string) bool        { return a.accounts[key] }
func (a *lifecycleFixtureAdapter) EnrolledAccountKeys() []string {
	keys := make([]string, 0, len(a.accounts))
	for key, ok := range a.accounts {
		if ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
func (*lifecycleFixtureAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilityStop}
}
func (a *lifecycleFixtureAdapter) Start(_ context.Context, r agentd.StartRequest, observe func(agentd.AdapterEvent)) (agentd.Process, error) {
	a.mu.Lock()
	a.requests = append(a.requests, r)
	a.mu.Unlock()
	observe(agentd.AdapterEvent{Kind: agentd.EventSessionStarted, HarnessSessionID: uuid.NewString()})
	observe(agentd.AdapterEvent{Kind: agentd.EventTurnCompleted})
	return &nativeFixtureProcess{done: make(chan struct{})}, nil
}

type lifecycleFixtureReporter struct {
	controller agentd.Controller
	public     string
}

type queuedLifecycleAuthority struct {
	intent *lifecycleintents.Intent
}

func (*queuedLifecycleAuthority) RegisterRuntime(context.Context, lifecycleintents.Registration) (lifecycleintents.Runtime, error) {
	return lifecycleintents.Runtime{}, errors.New("unused")
}
func (*queuedLifecycleAuthority) RegisterSession(context.Context, string, lifecycleintents.SessionRegistration, string) error {
	return errors.New("unused")
}
func (a *queuedLifecycleAuthority) Claim(context.Context, string) (*lifecycleintents.Intent, error) {
	if a.intent == nil {
		return nil, nil
	}
	out := *a.intent
	return &out, nil
}
func (a *queuedLifecycleAuthority) Transition(_ context.Context, _ string, transition lifecycleintents.Transition) (lifecycleintents.Intent, error) {
	if a.intent == nil || transition.ExpectedRevision != a.intent.Revision {
		return lifecycleintents.Intent{}, lifecycleintents.ErrConflict
	}
	a.intent.State = transition.State
	a.intent.Reason = transition.Reason
	a.intent.Revision++
	return *a.intent, nil
}

func (r *lifecycleFixtureReporter) BindController(c agentd.Controller) error {
	r.controller = c
	return nil
}
func (*lifecycleFixtureReporter) AuthenticatedMachineID(context.Context) (string, error) {
	return "fixture-host", nil
}
func (r *lifecycleFixtureReporter) ReportStatus(ctx context.Context, status agentd.Status) error {
	for _, s := range status.Sessions {
		if s.State == agentd.StateRunning && s.Reporter.PublicSessionID == "" {
			return r.controller.CheckpointReporter(ctx, s.ID, agentd.ControlRequest{Instance: status.Instance, ProjectID: s.ProjectID, Identity: s.Identity}, agentd.ReporterState{PublicSessionID: r.public, Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilityStop}})
		}
	}
	return nil
}

func TestDaemonLifecycleStartsReservedGenerationAndProvesPublicMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	a := &lifecycleFixtureAdapter{}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer controller.Close(context.Background())
	provenance, e := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "fixture", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	runtimeID := uuid.NewString()
	handle := uuid.NewString()
	generation := controller.Status().DaemonID
	reserved := uuid.NewString()
	intent := lifecycleintents.Intent{SchemaVersion: 1, ID: uuid.NewString(), ProjectID: 42, State: "claimed", Revision: 2, NewGeneration: reserved, Request: lifecycleintents.Request{RequestKey: uuid.NewString(), Operation: "start", RuntimeID: runtimeID, RuntimeGeneration: generation, AccountLabel: "chatgpt", TTLSeconds: 300, WorkspaceHandle: handle, AgentName: "worker", DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, Role: "worker", WorkShape: "unknown"}}
	mapped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !lifecycleclient.ValidProof(r.Header.Get(lifecycleintents.RuntimeLeaseHeader)) || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("runtime proof missing")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/agents/worker.json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 42, "key": "FIX"}, "agent": map[string]any{"project_id": 42, "name": "worker", "body": "canonical fixture instructions"}})
		case strings.HasSuffix(r.URL.Path, "/runtimes"):
			var in lifecycleintents.Registration
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Generation != generation || in.Workspaces[0].Identity != provenance.Identity {
				t.Error("configured provenance missing")
			}
			_ = json.NewEncoder(w).Encode(lifecycleintents.Runtime{ID: runtimeID, ProjectID: 42, Generation: generation, MachineID: in.Host, AccountLabel: in.AccountLabel, Workspaces: in.Workspaces, Profiles: in.Profiles, ExpiresAt: time.Now().Add(120 * time.Second).Format(time.RFC3339Nano)})
		case strings.HasSuffix(r.URL.Path, "/sessions"):
			var in lifecycleintents.SessionRegistration
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Generation != reserved || in.SessionID != reporter.public || !lifecycleclient.ValidProof(r.Header.Get(lifecycleintents.HarnessLeaseHeader)) {
				t.Error("public mapping lacked exact generation and worker proof")
			}
			mapped = true
			_ = json.NewEncoder(w).Encode(map[string]bool{"registered": true})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			var in *lifecycleintents.Intent
			if intent.State != "completed" {
				copy := intent
				in = &copy
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "intent": in})
		case strings.HasSuffix(r.URL.Path, "/transition"):
			var in lifecycleintents.Transition
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.ExpectedRevision != intent.Revision {
				t.Error("transition revision mismatch")
			}
			if in.State == "completed" && (!mapped || in.ResultSessionID != reporter.public) {
				t.Error("completion preceded public mapping")
			}
			if in.State == "completed" && intent.Request.Operation == "reassign" && controller.Status().Sessions[0].TicketID != 0 {
				t.Error("local binding changed before server completion CAS")
			}
			intent.State = in.State
			intent.Reason = in.Reason
			intent.Revision++
			intent.ResultSessionID = in.ResultSessionID
			_ = json.NewEncoder(w).Encode(intent)
		case strings.HasSuffix(r.URL.Path, "/message-targets"):
			_ = json.NewEncoder(w).Encode(map[string]any{"targets": []any{}})
		case strings.HasSuffix(r.URL.Path, "/runtime-health"):
			var in agentmessage.RuntimeHealthInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(agentmessage.RuntimeHealthResult{SchemaVersion: 1, State: in.State, Sequence: in.Sequence})
		default:
			t.Errorf("unexpected scoped route %s", r.URL.Path)
			http.Error(w, "unavailable", 404)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	config := lifecycleConfig{Projects: []configuredProject{{ProjectID: 42, AccountLabel: "chatgpt", Profiles: []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}}, Workspaces: []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}}}}}
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "fixture", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	if e = d.projects[0].step(ctx); e != nil {
		t.Fatal(e)
	}
	// The authorized server completion applies binding first; only then may the
	// daemon mirror the binding in its own persisted session record.
	ticket := int64(9)
	intent.ID = uuid.NewString()
	intent.State = "claimed"
	intent.Revision = 2
	intent.ResultSessionID = ""
	intent.Reason = ""
	intent.NewGeneration = ""
	intent.Request.RequestKey = uuid.NewString()
	intent.Request.Operation = "reassign"
	intent.Request.SessionID = reporter.public
	intent.Request.SessionGeneration = reserved
	intent.Request.ExpectedRevision = 10
	intent.Request.TicketID = &ticket
	intent.Request.WorkShape = "ship"
	if e = d.projects[0].step(ctx); e != nil {
		t.Fatal(e)
	}
	if controller.Status().Sessions[0].TicketID != ticket || intent.State != "completed" {
		t.Fatal("committed binding was not mirrored")
	}
	if intent.State != "completed" || !mapped {
		t.Fatal("actual daemon execution incomplete")
	}
	if e = d.projects[0].step(ctx); e != nil {
		t.Fatal(e)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) != 1 || !strings.Contains(a.requests[0].Prompt, "canonical fixture instructions") {
		t.Fatal("canonical prompt missing or duplicate spawn")
	}
	if controller.Status().Sessions[0].ID != reserved {
		t.Fatal("server-reserved generation was not actual local generation")
	}
}

func TestCommittedReadinessPersistsOnlyTheValidatedAuthorityReceipt(t *testing.T) {
	supervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "readiness-commit", StateRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	now := time.Now().UTC()
	baseline := "sha256:" + strings.Repeat("7", 64)
	checks := []lifecycleintents.ReadinessCheck{}
	for _, id := range lifecycleintents.RequiredReadinessChecks {
		checks = append(checks, lifecycleintents.ReadinessCheck{ID: id, Status: "pass", Reason: "verified"})
	}
	in := lifecycleintents.Intent{ID: uuid.NewString(), ProjectID: 440, Request: lifecycleintents.Request{Operation: "readiness", RuntimeID: uuid.NewString(), RuntimeGeneration: supervisor.Status().DaemonID,
		AccountLabel: "chatgpt", TTLSeconds: 300, WorkspaceHandle: uuid.NewString(), DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", BaselineDigest: baseline}}
	in.AcceptedReadiness = &lifecycleintents.ReadinessObservation{ContractVersion: lifecycleintents.ReadinessContractVersion, IntentID: in.ID, ProjectID: in.ProjectID,
		RuntimeID: in.Request.RuntimeID, RuntimeGeneration: in.Request.RuntimeGeneration, AccountLabel: in.Request.AccountLabel, ProfileID: in.Request.DispatchProfileID,
		ProfileVersion: in.Request.DispatchProfileVersion, WorkspaceHandle: in.Request.WorkspaceHandle, WorkspaceIdentity: strings.Repeat("8", 64), WorkspaceMode: "exclusive", BaselineDigest: baseline,
		HostKind: "macos-home-manager", Status: "ready", NextAction: "none", ObservedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), Checks: checks}
	p := &projectLifecycle{owner: &daemonLifecycle{supervisor: supervisor}}
	if err = p.Committed(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err = supervisor.ReadinessReceipt(agentd.ReadinessReceiptRequest{ProjectID: in.ProjectID, RuntimeID: in.Request.RuntimeID, RuntimeGeneration: in.Request.RuntimeGeneration,
		AccountLabel: in.Request.AccountLabel, ProfileID: in.Request.DispatchProfileID, ProfileVersion: in.Request.DispatchProfileVersion, WorkspaceHandle: in.Request.WorkspaceHandle, WorkspaceIdentity: strings.Repeat("8", 64), WorkspaceMode: "exclusive", BaselineDigest: baseline}); err != nil {
		t.Fatalf("validated authority receipt was not retained: %v", err)
	}
	in.AcceptedReadiness.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	if err = p.Committed(context.Background(), in); !errors.Is(err, lifecycleclient.ErrOwnership) {
		t.Fatalf("stale receipt commit error=%v", err)
	}
	in.AcceptedReadiness.ExpiresAt = now.Add(time.Minute).Format(time.RFC3339Nano)
	in.AcceptedReadiness.ProfileVersion = "other"
	if err = p.Committed(context.Background(), in); !errors.Is(err, lifecycleclient.ErrOwnership) {
		t.Fatalf("mismatched receipt commit error=%v", err)
	}
	in.AcceptedReadiness.ProfileVersion = "2"
	in.AcceptedReadiness.WorkspaceMode = "shared"
	if err = p.Committed(context.Background(), in); !errors.Is(err, lifecycleclient.ErrOwnership) {
		t.Fatalf("profile-mode mismatch commit error=%v", err)
	}
}

func TestLifecycleRenewalDiagnosticsPersistBoundedFailureCategoriesAndKeepExpiryFailClosed(t *testing.T) {
	t.Run("runtime registration failure", func(t *testing.T) {
		d, cleanup := daemonLifecycleFromProject(t, "renewal-transport", configuredProject{ProjectID: 42, AccountLabel: "chatgpt"})
		defer cleanup()
		p := d.projects[0]
		if err := p.step(context.Background()); !errors.Is(err, lifecycleclient.ErrTransport) {
			t.Fatalf("transport renewal error=%v", err)
		}
		diagnostic := p.lifecycleRenewalDiagnostic()
		if diagnostic == nil || diagnostic.LastFailureCategory != runtimeconsumer.LifecycleRenewalRuntimeRegistrationFailed || diagnostic.LastFailureAt.IsZero() {
			t.Fatalf("transport diagnostic=%+v", diagnostic)
		}
		if saved := d.journal.Snapshot()[0].LifecycleRenewal; saved.LastFailureCategory != runtimeconsumer.LifecycleRenewalRuntimeRegistrationFailed || saved.LastFailureAt.IsZero() {
			t.Fatalf("transport diagnostic was not persisted: %+v", saved)
		}
	})
	t.Run("workspace identity mismatch", func(t *testing.T) {
		workspace, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d, cleanup := daemonLifecycleFromProject(t, "renewal-config", configuredProject{
			ProjectID: 42, AccountLabel: "chatgpt",
			Workspaces: []configuredWorkspace{{Handle: uuid.NewString(), Path: workspace, Identity: strings.Repeat("0", 64), Label: "Fixture workspace"}},
		})
		defer cleanup()
		p := d.projects[0]
		if err := p.step(context.Background()); !errors.Is(err, lifecycleclient.ErrOwnership) {
			t.Fatalf("configuration renewal error=%v", err)
		}
		diagnostic := p.lifecycleRenewalDiagnostic()
		if diagnostic == nil || diagnostic.LastFailureCategory != runtimeconsumer.LifecycleRenewalWorkspaceIdentityMismatch || diagnostic.LastFailureAt.IsZero() {
			t.Fatalf("configuration diagnostic=%+v", diagnostic)
		}
	})
	t.Run("remote profile resolution failure is stage-only", func(t *testing.T) {
		d, cleanup := daemonLifecycleFromProject(t, "renewal-profile", configuredProject{ProjectID: 42, AccountLabel: "chatgpt"})
		defer cleanup()
		p := d.projects[0]
		const privateFailure = "fixture-private-profile-fetch-error"
		p.owner.reporter.run = func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
			return nil, errors.New(privateFailure)
		}
		if err := p.step(context.Background()); !errors.Is(err, lifecycleclient.ErrOwnership) || strings.Contains(err.Error(), privateFailure) {
			t.Fatalf("profile resolution renewal error=%v", err)
		}
		diagnostic := p.lifecycleRenewalDiagnostic()
		if diagnostic == nil || diagnostic.LastFailureCategory != runtimeconsumer.LifecycleRenewalProfileResolutionFailed || diagnostic.LastFailureAt.IsZero() {
			t.Fatalf("profile resolution diagnostic=%+v", diagnostic)
		}
		raw, err := json.Marshal(diagnostic)
		if err != nil || strings.Contains(string(raw), privateFailure) {
			t.Fatalf("renewal diagnostic leaked remote failure: err=%v json=%s", err, raw)
		}
	})
	t.Run("expired remains ownership lost", func(t *testing.T) {
		d, cleanup := daemonLifecycleFromProject(t, "renewal-expired", configuredProject{ProjectID: 42, AccountLabel: "chatgpt"})
		defer cleanup()
		p := d.projects[0]
		now := time.Now().UTC()
		p.record.Runtime = lifecycleintents.Runtime{ID: uuid.NewString(), ExpiresAt: now.Add(-time.Second).Format(time.RFC3339Nano)}
		p.record.LifecycleRenewal = runtimeconsumer.LifecycleRenewalDiagnostic{LastSuccessfulAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Second)}
		if err := p.step(context.Background()); !errors.Is(err, lifecycleclient.ErrOwnership) || !p.lost {
			t.Fatalf("expired renewal err=%v lost=%t", err, p.lost)
		}
		if diagnostic := p.lifecycleRenewalDiagnostic(); diagnostic == nil || diagnostic.ExpiresAt.After(now) {
			t.Fatalf("expired renewal diagnostic=%+v", diagnostic)
		}
	})
}

func renewalResponseAuthority(t *testing.T, p *projectLifecycle, expiresAt string) func() {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/lifecycle/v1/runtimes") {
			http.NotFound(w, r)
			return
		}
		var registration lifecycleintents.Registration
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			t.Fatalf("decode registration: %v", err)
		}
		_ = json.NewEncoder(w).Encode(lifecycleintents.Runtime{
			ID: uuid.NewString(), ProjectID: p.config.ProjectID, Generation: registration.Generation,
			MachineID: registration.Host, AccountLabel: registration.AccountLabel, Accounts: registration.Accounts,
			Workspaces: registration.Workspaces, Profiles: registration.Profiles, SchemaVersion: registration.SchemaVersion,
			AccountScopes: registration.AccountScopes, Conversation: registration.Conversation, ExpiresAt: expiresAt,
		})
	}))
	authority, err := lifecycleclient.NewHTTP(server.URL, p.config.ProjectID, p.record.Lease, func() (string, error) {
		return "fixture-key", nil
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	previous := p.authority
	p.authority = authority
	return func() {
		p.authority = previous
		server.Close()
	}
}

func TestLifecycleRenewalPreSendCredentialFailureIsStageOnly(t *testing.T) {
	d, cleanup := daemonLifecycleFromProject(t, "renewal-pre-send", configuredProject{ProjectID: 42, AccountLabel: "chatgpt"})
	defer cleanup()
	p := d.projects[0]
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	authority, err := lifecycleclient.NewHTTP(server.URL, p.config.ProjectID, p.record.Lease, func() (string, error) {
		return "", errors.New("fixture-private-credential-read-error")
	})
	if err != nil {
		t.Fatal(err)
	}
	p.authority = authority
	if err := p.step(context.Background()); !errors.Is(err, lifecycleclient.ErrOwnership) {
		t.Fatalf("pre-send renewal error=%v", err)
	}
	if requests != 0 {
		t.Fatalf("pre-send credential error sent %d requests", requests)
	}
	diagnostic := p.lifecycleRenewalDiagnostic()
	if diagnostic == nil || diagnostic.LastFailureCategory != runtimeconsumer.LifecycleRenewalRuntimeRegistrationFailed || diagnostic.LastFailureAt.IsZero() {
		t.Fatalf("pre-send diagnostic=%+v", diagnostic)
	}
	raw, err := json.Marshal(diagnostic)
	if err != nil || strings.Contains(string(raw), "fixture-private-credential-read-error") {
		t.Fatalf("pre-send diagnostic leaked credential error: err=%v json=%s", err, raw)
	}
}

func TestLifecycleRenewalReturnedExpiryPreservesExistingFence(t *testing.T) {
	for _, test := range []struct {
		name      string
		expiresAt string
	}{
		{name: "malformed", expiresAt: "not-a-timestamp"},
		{name: "expired", expiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, cleanup := daemonLifecycleFromProject(t, "renewal-returned-"+test.name, configuredProject{ProjectID: 42, AccountLabel: "chatgpt"})
			defer cleanup()
			p := d.projects[0]
			defer renewalResponseAuthority(t, p, test.expiresAt)()
			if err := p.step(context.Background()); !errors.Is(err, lifecycleclient.ErrOwnership) || p.lost {
				t.Fatalf("initial returned-expiry handling err=%v lost=%t", err, p.lost)
			}
			if p.record.Runtime.ExpiresAt != test.expiresAt || p.record.Runtime.ID == "" {
				t.Fatalf("returned runtime was not preserved: %+v", p.record.Runtime)
			}
			if err := p.step(context.Background()); !errors.Is(err, lifecycleclient.ErrOwnership) || !p.lost {
				t.Fatalf("next-step returned-expiry fence err=%v lost=%t", err, p.lost)
			}
		})
	}
}

func TestDaemonLifecycleAdvertisesTwoAccountsAndRejectsWrongKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	a := &lifecycleFixtureAdapter{accounts: map[string]bool{"coordinator": true, "personal": true}}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "named-accounts", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer controller.Close(context.Background())
	provenance, e := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "named-accounts", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	handle := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agents/worker.json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 42, "key": "FIX"}, "agent": map[string]any{"project_id": 42, "name": "worker", "body": "canonical fixture instructions"}})
			return
		}
		http.Error(w, "unavailable", 404)
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	config := lifecycleConfig{Projects: []configuredProject{{
		ProjectID: 42, AccountLabel: "chatgpt",
		Accounts:   []configuredAccount{{Key: "coordinator", Label: "Coordinator"}, {Key: "personal", Label: "Personal"}},
		Profiles:   []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}},
		Workspaces: []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}},
	}}}
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "named-accounts", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	reg := d.projects[0].registration
	if reg.SchemaVersion != lifecycleintents.AccountChoiceSchemaV2 || len(reg.Accounts) != 2 || reg.Accounts[0].Key != "coordinator" || reg.AccountLabel != "chatgpt" {
		t.Fatalf("advertisement=%+v", reg)
	}
	mixed := config
	mixed.Projects[0].AccountKey = "coordinator"
	mixedRaw, _ := json.Marshal(mixed)
	mixedPath := filepath.Join(t.TempDir(), "runtime.json")
	_ = os.WriteFile(mixedPath, mixedRaw, 0600)
	if _, err := newDaemonLifecycle(mixedPath, root, "named-accounts-mixed", server.URL, keyPath, controller, primary, bridge); err == nil {
		t.Fatal("mixed account_key and accounts was accepted")
	}
	intent := lifecycleintents.Intent{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2, ID: uuid.NewString(), ProjectID: 42, State: "claimed", NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey: uuid.NewString(), Operation: "start", RuntimeID: uuid.NewString(), RuntimeGeneration: controller.Status().DaemonID,
			AccountLabel: "chatgpt", AccountKey: "missing", TTLSeconds: 120, WorkspaceHandle: handle, AgentName: "worker",
			DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, Role: "worker", WorkShape: "unknown",
		},
	}
	if err := d.projects[0].Prepare(ctx, intent); err == nil {
		t.Fatal("unconfigured account was prepared")
	}
	intent.Request.AccountKey = "coordinator"
	if err := d.projects[0].Prepare(ctx, intent); err != nil {
		t.Fatal(err)
	}
	prepared := d.projects[0].prepared[intent.ID]
	if prepared.AccountKey != "coordinator" || prepared.ExpectedAccountLabel != "chatgpt" {
		t.Fatalf("prepared=%+v", prepared)
	}
	intent.ID, intent.Request.AccountKey, intent.NewGeneration = uuid.NewString(), "personal", uuid.NewString()
	if err := d.projects[0].Prepare(ctx, intent); err != nil || d.projects[0].prepared[intent.ID].AccountKey != "personal" {
		t.Fatal("second configured account could not be prepared")
	}
}

func TestDaemonLifecyclePrepareRefusesDetachedAccountAndRestartAdvertisesCommittedSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	a := &lifecycleFixtureAdapter{accounts: map[string]bool{"coordinator": true, "personal": true}}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "account-detach", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	provenance, e := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "account-detach", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	handle := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agents/worker.json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 42, "key": "FIX"}, "agent": map[string]any{"project_id": 42, "name": "worker", "body": "canonical fixture instructions"}})
			return
		}
		http.Error(w, "unavailable", 404)
	}))
	defer server.Close()
	config := lifecycleConfig{Projects: []configuredProject{{
		ProjectID: 42, AccountLabel: "chatgpt",
		Accounts:   []configuredAccount{{Key: "coordinator", Label: "Coordinator"}, {Key: "personal", Label: "Personal"}},
		Profiles:   []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}},
		Workspaces: []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}},
	}}}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "account-detach", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	if _, err := controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "detach-personal", Operation: agentd.AccountLifecycleDisconnect, ProjectID: 42,
		RuntimeGeneration: controller.Status().DaemonID, AccountKey: "personal", Adapter: agentd.AdapterCodex,
	}); err != nil {
		t.Fatal(err)
	}
	intent := lifecycleintents.Intent{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2, ID: uuid.NewString(), ProjectID: 42, State: "claimed", NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey: uuid.NewString(), Operation: "start", RuntimeID: uuid.NewString(), RuntimeGeneration: controller.Status().DaemonID,
			AccountLabel: "chatgpt", AccountKey: "personal", TTLSeconds: 120, WorkspaceHandle: handle, AgentName: "worker",
			DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, Role: "worker", WorkShape: "unknown",
		},
	}
	if err := d.projects[0].Prepare(ctx, intent); err == nil {
		t.Fatal("detached account was prepared")
	}
	intent.ID, intent.Request.AccountKey, intent.NewGeneration = uuid.NewString(), "coordinator", uuid.NewString()
	if err := d.projects[0].Prepare(ctx, intent); err == nil {
		t.Fatal("pre-disconnect advertisement prepared through the new attachment revision")
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "account-detach", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close(context.Background())
	again, e := newDaemonLifecycle(configPath, root, "account-detach", server.URL, keyPath, restarted, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	reg := again.projects[0].registration
	if len(reg.AccountScopes) != 1 || len(reg.AccountScopes[0].Accounts) != 1 || reg.AccountScopes[0].Accounts[0].Key != "coordinator" {
		t.Fatalf("restart advertisement=%+v", reg.AccountScopes)
	}
	if reg.SchemaVersion != lifecycleintents.AccountLifecycleSchemaV4 || len(reg.AccountScopes) != 1 || reg.AccountScopes[0].AttachmentRevision < 1 {
		t.Fatalf("restart did not advertise the committed attachment revision: %+v", reg)
	}
	intent.ID, intent.NewGeneration = uuid.NewString(), uuid.NewString()
	intent.Request.RuntimeGeneration = reg.Generation
	intent.Request.AttachmentRevision = reg.AccountScopes[0].AttachmentRevision
	if err := again.projects[0].Prepare(ctx, intent); err != nil {
		t.Fatalf("current advertised account revision was not prepared: %v", err)
	}
}

func TestDaemonLifecycleLegacyAccountKeyAdvertisesContractValidLabels(t *testing.T) {
	long := strings.Repeat("n", 49)
	cases := []struct {
		name, key, instance string
	}{
		{"ordinary", "coordinator", "legacy-ordinary"},
		{"namespaced", "team:alpha", "legacy-namespaced"},
		{"long", long, "legacy-long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, cleanup := daemonLifecycleFromProject(t, tc.instance, configuredProject{
				ProjectID: 42, AccountLabel: "chatgpt", AccountKey: tc.key,
			})
			defer cleanup()
			reg := d.projects[0].registration
			if reg.SchemaVersion != lifecycleintents.AccountChoiceSchemaV2 || len(reg.Accounts) != 1 {
				t.Fatalf("advertisement=%+v", reg)
			}
			if reg.Accounts[0].Key != tc.key {
				t.Fatalf("legacy key rewritten: %+v", reg.Accounts[0])
			}
			if !lifecycleintents.ValidAccountChoiceLabel(reg.Accounts[0].Label) {
				t.Fatalf("derived label not contract-valid: %+v", reg.Accounts[0])
			}
			if err := lifecycleintents.ValidateAdvertisedAccounts(reg); err != nil {
				t.Fatalf("server contract rejected registration: %v %+v", err, reg.Accounts[0])
			}
			if tc.name == "ordinary" && reg.Accounts[0].Label != "coordinator" {
				t.Fatalf("ordinary key was not reused as label: %+v", reg.Accounts[0])
			}
			if tc.name == "namespaced" && (reg.Accounts[0].Label == tc.key || strings.Contains(reg.Accounts[0].Label, ":")) {
				t.Fatalf("colon key used as label: %+v", reg.Accounts[0])
			}
			if tc.name == "long" && (len(reg.Accounts[0].Label) > 48 || reg.Accounts[0].Label == tc.key) {
				t.Fatalf("long key used as label: %+v", reg.Accounts[0])
			}
		})
	}
}

func TestDaemonLifecycleRejectsInvalidAccountChoiceLabelsBeforeRegistration(t *testing.T) {
	secret := "sk-live-abcdefghijk"
	cases := []struct {
		name, label, cause string
	}{
		{"colon", "Work: Main", "colon not allowed"},
		{"too-long", strings.Repeat("a", 60), "exceeds 48 characters"},
		{"secret-like", secret, "secret-like value"},
		{"whitespace", " Coordinator", "surrounding whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err, cleanup := daemonLifecycleErrFromProject(t, "invalid-label-"+tc.name, configuredProject{
				ProjectID:    42,
				AccountLabel: "chatgpt",
				Accounts:     []configuredAccount{{Key: "coordinator", Label: tc.label}},
			})
			defer cleanup()
			if err == nil {
				t.Fatal("invalid label loaded")
			}
			msg := err.Error()
			if !strings.Contains(msg, "accounts[0].label") || !strings.Contains(msg, tc.cause) {
				t.Fatalf("error=%q", msg)
			}
			if strings.Contains(msg, tc.label) {
				t.Fatalf("error echoed configured label: %q", msg)
			}
		})
	}
	_, err, cleanup := daemonLifecycleErrFromProject(t, "invalid-label-profile", configuredProject{
		ProjectID:    42,
		AccountLabel: "chatgpt",
		Accounts:     []configuredAccount{{Key: "coordinator", Label: "codex-sol-high"}},
	})
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "accounts[0].label collides with profile") {
		t.Fatalf("profile collision error=%v", err)
	}
	_, err, cleanup2 := daemonLifecycleErrFromProject(t, "invalid-label-key-ambiguity", configuredProject{
		ProjectID:    42,
		AccountLabel: "chatgpt",
		Accounts: []configuredAccount{
			{Key: "coordinator", Label: "Coordinator"},
			{Key: "personal", Label: "coordinator"},
		},
	})
	defer cleanup2()
	if err == nil || !strings.Contains(err.Error(), "accounts[1].label collides with account key") {
		t.Fatalf("key/label ambiguity error=%v", err)
	}
}

func daemonLifecycleFromProject(t *testing.T, instance string, project configuredProject) (*daemonLifecycle, func()) {
	t.Helper()
	d, err, cleanup := daemonLifecycleErrFromProject(t, instance, project)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	return d, cleanup
}

func daemonLifecycleErrFromProject(t *testing.T, instance string, project configuredProject) (*daemonLifecycle, error, func()) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	accounts := map[string]bool{"coordinator": true, "personal": true, "team:alpha": true, strings.Repeat("n", 49): true}
	a := &lifecycleFixtureAdapter{accounts: accounts}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: instance, StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, instance, bridge)
	if e != nil {
		controller.Close(context.Background())
		t.Fatal(e)
	}
	primary.controller = controller
	handle := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", 404)
	}))
	cleanup := func() {
		server.Close()
		primary.supervisor.Stop()
		controller.Close(context.Background())
	}
	if project.Profiles == nil && len(project.AccountScopes) == 0 {
		project.Profiles = []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}}
	}
	if project.Workspaces == nil {
		provenance, err := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
		if err != nil {
			cleanup()
			t.Fatal(err)
		}
		project.Workspaces = []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}}
	}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	raw, _ := json.Marshal(lifecycleConfig{Projects: []configuredProject{project}})
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, err := newDaemonLifecycle(configPath, root, instance, server.URL, keyPath, controller, primary, bridge)
	return d, err, cleanup
}

type namedLifecycleAdapter struct {
	lifecycleFixtureAdapter
	name, label string
}

func (a *namedLifecycleAdapter) Name() string                        { return a.name }
func (a *namedLifecycleAdapter) AccountLabel(context.Context) string { return a.label }

func TestDaemonLifecyclePreparesMixedCodexAndCursorScopes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	codexWorkspace, _ := filepath.EvalSymlinks(t.TempDir())
	cursorWorkspace, _ := filepath.EvalSymlinks(t.TempDir())
	codexProfile, _ := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	cursorProfile, _ := dispatchprofile.Resolve("cursor-composer", "2", "cursor")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{codexProfile, cursorProfile}})
	}, newMemoryReporterLeaseStore())
	codex := &namedLifecycleAdapter{lifecycleFixtureAdapter: lifecycleFixtureAdapter{accounts: map[string]bool{"codex-work": true, "codex-home": true, "codex-lab": true}}, name: "codex", label: "chatgpt"}
	cursor := &namedLifecycleAdapter{lifecycleFixtureAdapter: lifecycleFixtureAdapter{accounts: map[string]bool{"cursor-op": true}}, name: "cursor", label: "cursor_context"}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "mixed-scopes", StateRoot: root, Adapters: []agentd.Adapter{codex, cursor}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer controller.Close(context.Background())
	codexProvenance, e := controller.InspectWorkspace(ctx, codexWorkspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	cursorProvenance, e := controller.InspectWorkspace(ctx, cursorWorkspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "mixed-scopes", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	codexHandle, cursorHandle := uuid.NewString(), uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/agents/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 42, "key": "FIX"}, "agent": map[string]any{"project_id": 42, "name": "worker", "body": "canonical fixture instructions"}})
			return
		}
		http.Error(w, "unavailable", 404)
	}))
	defer server.Close()
	config := lifecycleConfig{Projects: []configuredProject{{
		ProjectID: 42,
		AccountScopes: []lifecycleintents.AccountScope{
			{AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "codex-work", Label: "Work"}, {Key: "codex-home", Label: "Home"}, {Key: "codex-lab", Label: "Lab"}}, Profiles: []lifecycleintents.Profile{{ID: codexProfile.ID, Version: codexProfile.Version}}},
			{AccountLabel: "cursor_context", Accounts: []lifecycleintents.AccountChoice{{Key: "cursor-op", Label: "Cursor"}}, Profiles: []lifecycleintents.Profile{{ID: cursorProfile.ID, Version: cursorProfile.Version}}},
		},
		Workspaces: []configuredWorkspace{
			{Handle: codexHandle, Path: codexWorkspace, Identity: codexProvenance.Identity, Label: "Codex"},
			{Handle: cursorHandle, Path: cursorWorkspace, Identity: cursorProvenance.Identity, Label: "Cursor"},
		},
	}}}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "mixed-scopes", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	reg := d.projects[0].registration
	if reg.SchemaVersion != lifecycleintents.AccountScopeSchemaV3 || reg.AccountLabel != "" || len(reg.AccountScopes) != 2 {
		t.Fatalf("advertisement=%+v", reg)
	}
	mixed := config
	mixed.Projects[0].AccountLabel = "chatgpt"
	mixedPath := filepath.Join(t.TempDir(), "mixed.json")
	mixedRaw, _ := json.Marshal(mixed)
	_ = os.WriteFile(mixedPath, mixedRaw, 0600)
	if _, err := newDaemonLifecycle(mixedPath, root, "mixed-scopes-class", server.URL, keyPath, controller, primary, bridge); err == nil {
		t.Fatal("mixed singular class and account_scopes was accepted")
	}
	cursorIntent := lifecycleintents.Intent{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2, ID: uuid.NewString(), ProjectID: 42, State: "claimed", NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey: uuid.NewString(), Operation: "start", RuntimeID: uuid.NewString(), RuntimeGeneration: controller.Status().DaemonID,
			AccountLabel: "cursor_context", AccountKey: "cursor-op", TTLSeconds: 120, WorkspaceHandle: cursorHandle, AgentName: "worker",
			DispatchProfileID: cursorProfile.ID, DispatchProfileVersion: cursorProfile.Version, Role: "worker", WorkShape: "unknown",
		},
	}
	if err := d.projects[0].Prepare(ctx, cursorIntent); err != nil {
		t.Fatal(err)
	}
	prepared := d.projects[0].prepared[cursorIntent.ID]
	if prepared.ExpectedAccountLabel != "cursor_context" || prepared.AccountKey != "cursor-op" || prepared.Adapter != "cursor" {
		t.Fatalf("prepared=%+v", prepared)
	}
	connected, err := controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "attach-codex", Operation: agentd.AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: controller.Status().DaemonID, AccountKey: "codex-work", Adapter: agentd.AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	reg = applyCommittedAccountAdvertisement(controller, config.Projects[0], reg)
	if err := lifecycleintents.ValidateRegistration(reg); err != nil || reg.SchemaVersion != lifecycleintents.AccountLifecycleSchemaV4 {
		t.Fatalf("mixed explicit and legacy advertisement invalid: %+v err=%v", reg, err)
	}
	d.projects[0].registration = reg
	codexRevision, cursorRevision := int64(0), int64(-1)
	for _, scope := range reg.AccountScopes {
		switch scope.AccountLabel {
		case "chatgpt":
			codexRevision = scope.AttachmentRevision
		case "cursor_context":
			cursorRevision = scope.AttachmentRevision
		}
	}
	if codexRevision != connected.Revision || cursorRevision != 0 {
		t.Fatalf("mixed attachment revisions codex=%d cursor=%d", codexRevision, cursorRevision)
	}
	legacyCursor := cursorIntent
	legacyCursor.ID, legacyCursor.NewGeneration, legacyCursor.Request.RequestKey = uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := d.projects[0].Prepare(ctx, legacyCursor); err != nil {
		t.Fatalf("legacy cursor scope was not preserved beside explicit Codex: %v", err)
	}
	codexIntent := cursorIntent
	codexIntent.ID, codexIntent.NewGeneration, codexIntent.Request.RequestKey = uuid.NewString(), uuid.NewString(), uuid.NewString()
	codexIntent.Request.AccountLabel, codexIntent.Request.AccountKey = "chatgpt", "codex-work"
	codexIntent.Request.DispatchProfileID, codexIntent.Request.DispatchProfileVersion = codexProfile.ID, codexProfile.Version
	codexIntent.Request.WorkspaceHandle = codexHandle
	if err := d.projects[0].Prepare(ctx, codexIntent); err == nil {
		t.Fatal("explicit Codex selection without its advertised revision was prepared")
	}
	codexIntent.Request.AttachmentRevision = codexRevision
	if err := d.projects[0].Prepare(ctx, codexIntent); err != nil {
		t.Fatalf("explicit Codex selection at its advertised revision was not prepared: %v", err)
	}
	crossed := cursorIntent
	crossed.ID, crossed.NewGeneration, crossed.Request.RequestKey = uuid.NewString(), uuid.NewString(), uuid.NewString()
	crossed.Request.AccountLabel, crossed.Request.AccountKey = "chatgpt", "codex-work"
	if err := d.projects[0].Prepare(ctx, crossed); err == nil {
		t.Fatal("codex class prepared a cursor profile")
	}
}

func TestDaemonLifecycleAdvertisementPreservesCodexClassMembership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	chatgptProfile, _ := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	apiKeyProfile, _ := dispatchprofile.Resolve("codex-luna-medium", "2", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{chatgptProfile, apiKeyProfile}})
	}, newMemoryReporterLeaseStore())
	a := &lifecycleFixtureAdapter{accounts: map[string]bool{"codex-work": true, "codex-lab": true}}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "codex-classes", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer controller.Close(context.Background())
	provenance, e := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "codex-classes", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	handle := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", 404)
	}))
	defer server.Close()
	config := lifecycleConfig{Projects: []configuredProject{{
		ProjectID: 42,
		AccountScopes: []lifecycleintents.AccountScope{
			{AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "codex-work", Label: "Work"}}, Profiles: []lifecycleintents.Profile{{ID: chatgptProfile.ID, Version: chatgptProfile.Version}}},
			{AccountLabel: "api_key", Accounts: []lifecycleintents.AccountChoice{{Key: "codex-lab", Label: "Lab"}}, Profiles: []lifecycleintents.Profile{{ID: apiKeyProfile.ID, Version: apiKeyProfile.Version}}},
		},
		Workspaces: []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Codex"}},
	}}}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "codex-classes", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	if _, err := controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "attach-work", Operation: agentd.AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: controller.Status().DaemonID, AccountKey: "codex-work", Adapter: agentd.AdapterCodex,
	}); err != nil {
		t.Fatal(err)
	}
	reg := applyCommittedAccountAdvertisement(controller, config.Projects[0], d.projects[0].registration)
	if err := lifecycleintents.ValidateRegistration(reg); err != nil {
		t.Fatalf("class-preserving lifecycle advertisement invalid: %v", err)
	}
	if reg.SchemaVersion != lifecycleintents.AccountLifecycleSchemaV4 || len(reg.AccountScopes) != 2 {
		t.Fatalf("advertisement=%+v", reg)
	}
	byClass := map[string][]string{}
	for _, scope := range reg.AccountScopes {
		for _, choice := range scope.Accounts {
			byClass[scope.AccountLabel] = append(byClass[scope.AccountLabel], choice.Key)
		}
	}
	if len(byClass["chatgpt"]) != 1 || byClass["chatgpt"][0] != "codex-work" {
		t.Fatalf("chatgpt advertisement=%v", byClass["chatgpt"])
	}
	if containsString(byClass["chatgpt"], "codex-lab") {
		t.Fatal("api_key key appeared under chatgpt")
	}
	if len(byClass["api_key"]) != 1 || byClass["api_key"][0] != "codex-lab" {
		t.Fatalf("api_key advertisement=%v", byClass["api_key"])
	}
}

func TestDaemonLifecycleDetachingAllCodexKeysDoesNotAdvertiseAmbient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	a := &lifecycleFixtureAdapter{accounts: map[string]bool{"coordinator": true, "personal": true}}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "codex-empty", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	provenance, e := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "codex-empty", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	handle := uuid.NewString()
	// Both exercised starts are rejected by the local attachment-epoch check,
	// before any remote canonical-agent read is permitted.
	reportURL := "http://127.0.0.1:1"
	config := lifecycleConfig{Projects: []configuredProject{{
		ProjectID: 42, AccountLabel: "chatgpt",
		Accounts:   []configuredAccount{{Key: "coordinator", Label: "Coordinator"}, {Key: "personal", Label: "Personal"}},
		Profiles:   []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}},
		Workspaces: []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}},
	}}}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "codex-empty", reportURL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	generation := controller.Status().DaemonID
	connected, err := controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "attach", Operation: agentd.AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: agentd.AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	advertised := applyCommittedAccountAdvertisement(controller, config.Projects[0], d.projects[0].registration)
	d.projects[0].registration = advertised
	queuedRuntime := lifecycleintents.Runtime{
		ID: uuid.NewString(), ProjectID: 42, Generation: advertised.Generation, MachineID: advertised.Host,
		Workspaces: advertised.Workspaces, AccountScopes: advertised.AccountScopes,
		SchemaVersion: advertised.SchemaVersion, Sessions: []lifecycleintents.SessionProjection{},
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
	queued := &queuedLifecycleAuthority{intent: &lifecycleintents.Intent{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2, ID: uuid.NewString(), ProjectID: 42,
		State: "claimed", Revision: 2, NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey: uuid.NewString(), Operation: "start", RuntimeID: queuedRuntime.ID,
			RuntimeGeneration: generation, AccountLabel: "chatgpt", AccountKey: "coordinator",
			AttachmentRevision: connected.Revision, TTLSeconds: 120, WorkspaceHandle: handle,
			AgentName: "worker", DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
			WorkShape: "unknown", Role: "worker",
		},
	}}
	runner, err := lifecycleclient.NewRunner(filepath.Join(t.TempDir(), "runner"), generation, queued, d.projects[0])
	if err != nil {
		t.Fatal(err)
	}
	disconnected, err := controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "detach-coordinator", Operation: agentd.AccountLifecycleDisconnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: agentd.AdapterCodex, ExpectedRevision: connected.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconnected, err := controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "reconnect-coordinator", Operation: agentd.AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: agentd.AdapterCodex, ExpectedRevision: disconnected.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Step(ctx, queuedRuntime); err != nil {
		t.Fatalf("queued stale start did not settle as failed: %v", err)
	}
	if queued.intent.State != "failed" || len(a.requests) != 0 {
		t.Fatalf("queued stale start state=%s spawned=%d", queued.intent.State, len(a.requests))
	}
	disconnected, err = controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "detach-coordinator-final", Operation: agentd.AccountLifecycleDisconnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "coordinator", Adapter: agentd.AdapterCodex, ExpectedRevision: reconnected.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "detach-personal", Operation: agentd.AccountLifecycleDisconnect, ProjectID: 42,
		RuntimeGeneration: generation, AccountKey: "personal", Adapter: agentd.AdapterCodex, ExpectedRevision: disconnected.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Start(ctx, agentd.StartRequest{
		Adapter: agentd.AdapterCodex, Workspace: workspace, Prompt: "ambient", Identity: "codex:ambient", ProjectID: 42,
	}); !errors.Is(err, agentd.ErrAccountDetached) {
		t.Fatalf("empty named set started ambient Codex: %v", err)
	}
	intent := lifecycleintents.Intent{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2, ID: uuid.NewString(), ProjectID: 42, State: "claimed", NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey: uuid.NewString(), Operation: "start", RuntimeID: uuid.NewString(), RuntimeGeneration: generation,
			AccountLabel: "chatgpt", AccountKey: "coordinator", TTLSeconds: 120, WorkspaceHandle: handle, AgentName: "worker",
			DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, Role: "worker", WorkShape: "unknown",
		},
	}
	if err := d.projects[0].Prepare(ctx, intent); err == nil {
		t.Fatal("detached Codex key was prepared")
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "codex-empty", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close(context.Background())
	again, e := newDaemonLifecycle(configPath, root, "codex-empty", reportURL, keyPath, restarted, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	reg := again.projects[0].registration
	if reg.SchemaVersion != lifecycleintents.AccountLifecycleSchemaV4 || reg.AccountLabel != "" || len(reg.AccountScopes) != 1 ||
		reg.AccountScopes[0].AccountLabel != "chatgpt" || len(reg.AccountScopes[0].Accounts) != 0 ||
		reg.AccountScopes[0].AccountAvailability != lifecycleintents.AccountAvailabilityUnavailable || reg.AccountScopes[0].AttachmentRevision < 1 {
		t.Fatalf("empty named Codex availability was not advertised explicitly: %+v", reg)
	}
	if _, err := restarted.ApplyAccountLifecycle(ctx, agentd.AccountLifecycleRequest{
		IdempotencyKey: "reconnect-after-empty-restart", Operation: agentd.AccountLifecycleConnect, ProjectID: 42,
		RuntimeGeneration: restarted.Status().DaemonID, AccountKey: "coordinator", Adapter: agentd.AdapterCodex,
		ExpectedRevision: reg.AccountScopes[0].AttachmentRevision,
	}); err != nil || !restarted.AccountAttached(42, agentd.AdapterCodex, "coordinator") {
		t.Fatalf("persisted empty named mode could not reconnect a configured account: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
