// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func mixedHarnessRegistration(t *testing.T, host string) Registration {
	t.Helper()
	return Registration{
		Generation:    uuid.NewString(),
		Host:          host,
		SchemaVersion: AccountScopeSchemaV3,
		Workspaces:    []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1), Label: "Codex"}, {Handle: uuid.NewString(), Identity: fmtIdentity(2), Label: "Cursor"}},
		AccountScopes: []AccountScope{
			{
				AccountLabel: "chatgpt",
				Accounts: []AccountChoice{
					{Key: "codex-work", Label: "Work"},
					{Key: "codex-home", Label: "Home"},
					{Key: "codex-lab", Label: "Lab"},
				},
				Profiles: []Profile{
					{ID: "codex-luna-medium", Version: "2"},
					{ID: "codex-sol-high", Version: "2"},
					{ID: "codex-sol-xhigh", Version: "2"},
					{ID: "codex-terra-high", Version: "2"},
				},
			},
			{
				AccountLabel: "cursor_context",
				Accounts:     []AccountChoice{{Key: "cursor-op", Label: "Cursor"}},
				Profiles: []Profile{
					{ID: "cursor-composer", Version: "2"},
					{ID: "cursor-grok", Version: "2"},
				},
			},
		},
	}
}

func TestV3RegistrationJSONOmitsSingularClass(t *testing.T) {
	in := mixedHarnessRegistration(t, "fixture-machine")
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("v3 registration is not an object")
	}
	for _, name := range []string{"account_label", "accounts", "profiles"} {
		if _, exists := fields[name]; exists {
			t.Fatalf("v3 registration leaked %s: %s", name, raw)
		}
	}
	if string(fields["schema_version"]) != "3" {
		t.Fatalf("schema_version=%s", fields["schema_version"])
	}
	var round Registration
	if json.Unmarshal(raw, &round) != nil {
		t.Fatal("v3 registration did not round-trip")
	}
	if !round.MatchScope("chatgpt", "codex-work", "codex-sol-high", "2", true) || !round.MatchScope("cursor_context", "cursor-op", "cursor-composer", "2", true) {
		t.Fatal("round-trip lost mixed membership")
	}
}

func TestV3RegistrationRejectsMixedVersionAndUnknownSchema(t *testing.T) {
	in := mixedHarnessRegistration(t, "fixture-machine")
	if err := validateRegistration(in); err != nil {
		t.Fatal(err)
	}
	in.AccountLabel = "chatgpt"
	if err := validateRegistration(in); err != ErrInvalid {
		t.Fatalf("singular class on v3: %v", err)
	}
	in.AccountLabel = ""
	in.Profiles = []Profile{}
	if err := validateRegistration(in); err != ErrInvalid {
		t.Fatalf("empty profiles key on v3 struct: %v", err)
	}
	for _, body := range []string{
		`{"generation":"` + uuid.NewString() + `","host":"fixture-machine","schema_version":3,"workspaces":[],"account_scopes":[],"account_label":null}`,
		`{"generation":"` + uuid.NewString() + `","host":"fixture-machine","schema_version":3,"workspaces":[],"account_scopes":[],"profiles":[]}`,
		`{"generation":"` + uuid.NewString() + `","host":"fixture-machine","schema_version":4,"workspaces":[],"account_label":"chatgpt","profiles":[]}`,
		`{"generation":"` + uuid.NewString() + `","host":"fixture-machine","schema_version":3,"workspaces":[],"account_scopes":null}`,
	} {
		var parsed Registration
		if json.Unmarshal([]byte(body), &parsed) == nil && validateRegistration(parsed) == nil {
			t.Fatalf("accepted mixed or unknown body %s", body)
		}
	}
}

func TestV2RegistrationStillAcceptsMixedHarnessProfiles(t *testing.T) {
	in := Registration{
		Generation: uuid.NewString(), Host: "fixture-machine", AccountLabel: "chatgpt",
		SchemaVersion: AccountChoiceSchemaV2,
		Accounts:      []AccountChoice{{Key: "coordinator", Label: "Coordinator"}},
		Workspaces:    []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		Profiles:      []Profile{{ID: "codex-sol-high", Version: "2"}, {ID: "cursor-composer", Version: "2"}},
	}
	if err := validateRegistration(in); err != nil {
		t.Fatal(err)
	}
}

func TestMatchScopeDoesNotSilentlyPickOverlappingCodexClasses(t *testing.T) {
	in := Registration{
		Generation: uuid.NewString(), Host: "fixture-machine", SchemaVersion: AccountScopeSchemaV3,
		Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		AccountScopes: []AccountScope{
			{AccountLabel: "chatgpt", Accounts: []AccountChoice{{Key: "codex-work", Label: "Work"}}, Profiles: []Profile{{ID: "codex-sol-high", Version: "2"}}},
			{AccountLabel: "api_key", Accounts: []AccountChoice{{Key: "codex-api", Label: "API"}}, Profiles: []Profile{{ID: "codex-sol-high", Version: "2"}}},
		},
	}
	if err := validateRegistration(in); err != nil {
		t.Fatal(err)
	}
	if in.MatchScope("chatgpt", "codex-work", "codex-sol-high", "2", true) == false {
		t.Fatal("chatgpt named choice")
	}
	if in.MatchScope("api_key", "codex-api", "codex-sol-high", "2", true) == false {
		t.Fatal("api_key named choice")
	}
	if in.MatchScope("chatgpt", "codex-api", "codex-sol-high", "2", true) || in.MatchScope("api_key", "codex-work", "codex-sol-high", "2", true) {
		t.Fatal("crossed class and key")
	}
	if in.MatchScope("", "codex-work", "codex-sol-high", "2", true) {
		t.Fatal("missing class was treated as unique")
	}
}

func TestRegistrationRejectsUnknownNestedFields(t *testing.T) {
	generation, host := uuid.NewString(), "fixture-machine"
	v1 := `{"generation":"` + generation + `","host":"` + host + `","account_label":"chatgpt","workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `"}],"profiles":[{"id":"codex-sol-high","version":"2"}]}`
	v2 := `{"generation":"` + generation + `","host":"` + host + `","account_label":"chatgpt","schema_version":2,"accounts":[{"key":"coordinator","label":"Coordinator"}],"workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `"}],"profiles":[{"id":"codex-sol-high","version":"2"}]}`
	v3 := `{"generation":"` + generation + `","host":"` + host + `","schema_version":3,"workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `"}],"account_scopes":[{"account_label":"chatgpt","accounts":[{"key":"codex-work","label":"Work"}],"profiles":[{"id":"codex-sol-high","version":"2"}]}]}`
	for _, body := range []string{v1, v2, v3} {
		var parsed Registration
		if json.Unmarshal([]byte(body), &parsed) != nil {
			t.Fatalf("canonical closed body refused: %s", body)
		}
		if err := validateRegistration(parsed); err != nil {
			t.Fatalf("canonical closed body invalid: %v %s", err, body)
		}
	}
	for _, body := range []string{
		`{"generation":"` + generation + `","host":"` + host + `","account_label":"chatgpt","workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `","path":"/secret"}],"profiles":[{"id":"codex-sol-high","version":"2"}]}`,
		`{"generation":"` + generation + `","host":"` + host + `","account_label":"chatgpt","workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `"}],"profiles":[{"id":"codex-sol-high","version":"2","harness":"codex"}]}`,
		`{"generation":"` + generation + `","host":"` + host + `","account_label":"chatgpt","schema_version":2,"accounts":[{"key":"coordinator","label":"Coordinator","home":"~/.codex"}],"workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `"}],"profiles":[{"id":"codex-sol-high","version":"2"}]}`,
		`{"generation":"` + generation + `","host":"` + host + `","schema_version":3,"workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `","path":"/secret"}],"account_scopes":[{"account_label":"chatgpt","profiles":[{"id":"codex-sol-high","version":"2"}]}]}`,
		`{"generation":"` + generation + `","host":"` + host + `","schema_version":3,"workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `"}],"account_scopes":[{"account_label":"chatgpt","profiles":[{"id":"codex-sol-high","version":"2","harness":"codex"}]}]}`,
		`{"generation":"` + generation + `","host":"` + host + `","schema_version":3,"workspaces":[{"handle":"` + uuid.NewString() + `","identity":"` + fmtIdentity(1) + `"}],"account_scopes":[{"account_label":"chatgpt","accounts":[{"key":"codex-work","label":"Work","home":"~/.codex"}],"profiles":[{"id":"codex-sol-high","version":"2"}]}]}`,
	} {
		var parsed Registration
		if json.Unmarshal([]byte(body), &parsed) == nil {
			t.Fatalf("unknown nested field silently dropped: %s", body)
		}
	}
}

func TestV3RegistrationRejectsEmptyAccountsArrayAndKeepsClassOnlyOmission(t *testing.T) {
	generation, handle := uuid.NewString(), uuid.NewString()
	classOnly := `{"generation":"` + generation + `","host":"fixture-machine","schema_version":3,"workspaces":[{"handle":"` + handle + `","identity":"` + fmtIdentity(1) + `"}],"account_scopes":[{"account_label":"chatgpt","profiles":[{"id":"codex-sol-high","version":"2"}]}]}`
	var parsed Registration
	if json.Unmarshal([]byte(classOnly), &parsed) != nil {
		t.Fatal("omitted accounts refused")
	}
	if err := validateRegistration(parsed); err != nil || len(parsed.AccountScopes[0].Accounts) != 0 {
		t.Fatalf("class-only chatgpt: %+v err=%v", parsed, err)
	}
	empty := `{"generation":"` + generation + `","host":"fixture-machine","schema_version":3,"workspaces":[{"handle":"` + handle + `","identity":"` + fmtIdentity(1) + `"}],"account_scopes":[{"account_label":"chatgpt","accounts":[],"profiles":[{"id":"codex-sol-high","version":"2"}]}]}`
	if json.Unmarshal([]byte(empty), &parsed) == nil && validateRegistration(parsed) == nil {
		t.Fatal("empty accounts array canonicalized as class-only")
	}
}

func TestV4RegistrationCarriesRevisionAndExplicitEmptyAvailability(t *testing.T) {
	base := Registration{
		Generation: uuid.NewString(), Host: "fixture-machine", SchemaVersion: AccountLifecycleSchemaV4,
		Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		AccountScopes: []AccountScope{
			{AccountLabel: "chatgpt", Accounts: []AccountChoice{{Key: "codex-work", Label: "Work"}}, Profiles: []Profile{{ID: "codex-sol-high", Version: "2"}}, AttachmentRevision: 7, AccountAvailability: AccountAvailabilityAvailable},
			{AccountLabel: "cursor_context", Profiles: []Profile{{ID: "cursor-composer", Version: "2"}}, AttachmentRevision: 9, AccountAvailability: AccountAvailabilityUnavailable},
		},
	}
	if err := validateRegistration(base); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var round Registration
	if err := json.Unmarshal(raw, &round); err != nil || validateRegistration(round) != nil {
		t.Fatalf("v4 round trip err=%v body=%s", err, raw)
	}
	if !round.MatchScopeAtRevision("chatgpt", "codex-work", "codex-sol-high", "2", true, 7) ||
		round.MatchScopeAtRevision("chatgpt", "codex-work", "codex-sol-high", "2", true, 6) ||
		round.MatchScope("cursor_context", "", "cursor-composer", "2", true) {
		t.Fatalf("v4 revision or empty availability was widened: %+v", round.AccountScopes)
	}
	for _, mutate := range []func(*Registration){
		func(r *Registration) { r.AccountScopes[0].AttachmentRevision = 0 },
		func(r *Registration) { r.AccountScopes[0].AccountAvailability = "" },
		func(r *Registration) { r.AccountScopes[0].AccountAvailability = AccountAvailabilityUnavailable },
		func(r *Registration) { r.AccountScopes[1].AccountAvailability = AccountAvailabilityAvailable },
	} {
		candidate := round
		candidate.AccountScopes = append([]AccountScope(nil), round.AccountScopes...)
		mutate(&candidate)
		if err := validateRegistration(candidate); err != ErrInvalid {
			t.Fatalf("invalid v4 lifecycle scope accepted: %+v err=%v", candidate.AccountScopes, err)
		}
	}
	legacy := round
	legacy.AccountScopes = append([]AccountScope(nil), round.AccountScopes...)
	legacy.AccountScopes[0].AttachmentRevision = 0
	legacy.AccountScopes[0].AccountAvailability = ""
	if err := validateRegistration(legacy); err != nil || !legacy.MatchScopeAtRevision("chatgpt", "codex-work", "codex-sol-high", "2", true, 0) {
		t.Fatalf("v4 registration did not preserve a legacy named scope: %+v err=%v", legacy.AccountScopes, err)
	}
}

func TestV4ConversationCapabilityRoundTripsWithoutWidening(t *testing.T) {
	in := Registration{
		Generation: uuid.NewString(), Host: "fixture-machine", SchemaVersion: AccountLifecycleSchemaV4,
		Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		AccountScopes: []AccountScope{{
			AccountLabel: "chatgpt", Accounts: []AccountChoice{{Key: "codex-work", Label: "Work"}},
			Profiles: []Profile{{ID: "codex-sol-high", Version: "2"}}, AttachmentRevision: 7,
			AccountAvailability: AccountAvailabilityAvailable,
		}},
		Conversation: &ConversationCapability{
			SchemaVersion: ConversationSchemaV1, AccountKey: "codex-work", AttachmentRevision: 7,
			DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2",
			ExecutionPolicyID: ConversationExecutionPolicyV1, MaxOutputBytes: 256 << 10, MaxEvents: 512,
		},
	}
	if err := validateRegistration(in); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var round Registration
	if err := json.Unmarshal(body, &round); err != nil || validateRegistration(round) != nil ||
		round.Conversation == nil || *round.Conversation != *in.Conversation {
		t.Fatalf("conversation round trip err=%v body=%s capability=%+v", err, body, round.Conversation)
	}
	runtime := runtimeProjection(uuid.NewString(), 7, round, "2027-01-01T00:00:00.000Z")
	if !runtime.MatchConversation("codex-work", 7, "codex-sol-high", "2", ConversationExecutionPolicyV1, 256<<10, 512) ||
		runtime.MatchConversation("codex-work", 7, "codex-sol-high", "2", ConversationExecutionPolicyV1, 128<<10, 512) {
		t.Fatalf("conversation capability match widened: %+v", runtime.Conversation)
	}

	absent := in
	absent.Conversation = nil
	if err := validateRegistration(absent); err != nil {
		t.Fatalf("schema4 coding-only producer rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ConversationCapability){
		"wrong_account":  func(c *ConversationCapability) { c.AccountKey = "other" },
		"stale_revision": func(c *ConversationCapability) { c.AttachmentRevision-- },
		"wrong_profile":  func(c *ConversationCapability) { c.DispatchProfileID = "codex-luna-medium" },
		"wrong_policy":   func(c *ConversationCapability) { c.ExecutionPolicyID = "ordinary-coding" },
		"wrong_caps":     func(c *ConversationCapability) { c.MaxEvents = 513 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := in
			capability := *in.Conversation
			mutate(&capability)
			candidate.Conversation = &capability
			if err := validateRegistration(candidate); err != ErrInvalid {
				t.Fatalf("invalid conversation capability accepted: %+v err=%v", capability, err)
			}
		})
	}
	unknown := strings.Replace(string(body), `"max_events":512`, `"max_events":512,"credential":"forbidden"`, 1)
	if json.Unmarshal([]byte(unknown), &round) == nil {
		t.Fatal("unknown conversation capability field was accepted")
	}
}

func TestV4RegistrationAllowsExplicitAndLegacyNamedScopesWithoutConflatingEpochs(t *testing.T) {
	in := Registration{
		Generation: uuid.NewString(), Host: "fixture-machine", SchemaVersion: AccountLifecycleSchemaV4,
		Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		AccountScopes: []AccountScope{
			{AccountLabel: "chatgpt", Accounts: []AccountChoice{{Key: "codex-work", Label: "Work"}}, Profiles: []Profile{{ID: "codex-sol-high", Version: "2"}}, AttachmentRevision: 4, AccountAvailability: AccountAvailabilityAvailable},
			{AccountLabel: "cursor_context", Accounts: []AccountChoice{{Key: "cursor-op", Label: "Cursor"}}, Profiles: []Profile{{ID: "cursor-composer", Version: "2"}}},
		},
	}
	if err := validateRegistration(in); err != nil {
		t.Fatal(err)
	}
	if !in.MatchScopeAtRevision("chatgpt", "codex-work", "codex-sol-high", "2", true, 4) ||
		in.MatchScopeAtRevision("chatgpt", "codex-work", "codex-sol-high", "2", true, 0) ||
		!in.MatchScopeAtRevision("cursor_context", "cursor-op", "cursor-composer", "2", true, 0) {
		t.Fatalf("mixed lifecycle epochs were conflated: %+v", in.AccountScopes)
	}
}

func TestV3RegistrationRejectsCursorWithoutKeysAndClaudeNamedKeys(t *testing.T) {
	host := "fixture-machine"
	cursor := Registration{
		Generation: uuid.NewString(), Host: host, SchemaVersion: AccountScopeSchemaV3,
		Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		AccountScopes: []AccountScope{
			{AccountLabel: "cursor_context", Profiles: []Profile{{ID: "cursor-composer", Version: "2"}}},
		},
	}
	if err := validateRegistration(cursor); err != ErrInvalid {
		t.Fatalf("cursor class-only: %v", err)
	}
	claude := Registration{
		Generation: uuid.NewString(), Host: host, SchemaVersion: AccountScopeSchemaV3,
		Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		AccountScopes: []AccountScope{
			{AccountLabel: "claude_ai_max", Accounts: []AccountChoice{{Key: "claude-home", Label: "Claude"}}, Profiles: []Profile{{ID: "claude-opus-xhigh", Version: "2"}}},
		},
	}
	if err := validateRegistration(claude); err != ErrInvalid {
		t.Fatalf("claude named key: %v", err)
	}
}

func TestLifecycleAdvertisesMixedCodexAndCursorScopesOnOneRuntime(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.now = f.now.Add(121 * time.Second)
	if _, err := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, f.registration); err != ErrUnavailable {
		t.Fatal("expired class-only generation revived")
	}
	in := mixedHarnessRegistration(t, f.registration.Host)
	runtime, err := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, in)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.SchemaVersion != AccountScopeSchemaV3 || runtime.AccountLabel != "" || len(runtime.AccountScopes) != 2 {
		t.Fatalf("runtime=%+v", runtime)
	}
	codex := f.request("start")
	codex.RuntimeID, codex.RuntimeGeneration = runtime.ID, runtime.Generation
	codex.AccountLabel, codex.AccountKey = "chatgpt", "codex-work"
	codex.DispatchProfileID, codex.DispatchProfileVersion = "codex-sol-high", "2"
	codex.WorkspaceHandle = in.Workspaces[0].Handle
	crossed := f.request("start")
	crossed.RuntimeID, crossed.RuntimeGeneration = runtime.ID, runtime.Generation
	crossed.AccountLabel, crossed.AccountKey = "chatgpt", "codex-work"
	crossed.DispatchProfileID, crossed.DispatchProfileVersion = "cursor-composer", "2"
	crossed.WorkspaceHandle = in.Workspaces[1].Handle
	if _, _, err = f.s.Submit(ctx, f.human, f.project, crossed); err != ErrUnavailable {
		t.Fatalf("codex key with cursor profile: %v", err)
	}
	wrongKey := f.request("start")
	wrongKey.RuntimeID, wrongKey.RuntimeGeneration = runtime.ID, runtime.Generation
	wrongKey.AccountLabel, wrongKey.AccountKey = "cursor_context", "codex-work"
	wrongKey.DispatchProfileID, wrongKey.DispatchProfileVersion = "cursor-composer", "2"
	wrongKey.WorkspaceHandle = in.Workspaces[1].Handle
	if _, _, err = f.s.Submit(ctx, f.human, f.project, wrongKey); err != ErrUnavailable {
		t.Fatalf("codex key under cursor class: %v", err)
	}
	if _, _, err = f.s.Submit(ctx, f.human, f.project, codex); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleQueueBindsNamedRequestToAdvertisedAttachmentRevision(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.now = f.now.Add(121 * time.Second)
	registration := Registration{
		Generation: uuid.NewString(), Host: f.registration.Host, SchemaVersion: AccountLifecycleSchemaV4,
		Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(3)}},
		AccountScopes: []AccountScope{{
			AccountLabel: "chatgpt", Accounts: []AccountChoice{{Key: "codex-work", Label: "Work"}},
			Profiles:           []Profile{{ID: "codex-sol-high", Version: "2"}},
			AttachmentRevision: 12, AccountAvailability: AccountAvailabilityAvailable,
		}},
	}
	runtime, err := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, registration)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		RequestKey: uuid.NewString(), Operation: "start", RuntimeID: runtime.ID, RuntimeGeneration: runtime.Generation,
		AccountLabel: "chatgpt", AccountKey: "codex-work", TTLSeconds: 120,
		WorkspaceHandle: registration.Workspaces[0].Handle, AgentName: "worker",
		DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", WorkShape: "unknown", Role: "worker",
	}
	if _, _, err := f.s.Submit(ctx, f.human, f.project, request); err != ErrUnavailable {
		t.Fatalf("explicit named request without revision err=%v", err)
	}
	request.RequestKey = uuid.NewString()
	request.AttachmentRevision = 11
	if _, _, err := f.s.Submit(ctx, f.human, f.project, request); err != ErrUnavailable {
		t.Fatalf("stale named request err=%v", err)
	}
	request.RequestKey = uuid.NewString()
	request.AttachmentRevision = 12
	queued, created, err := f.s.Submit(ctx, f.human, f.project, request)
	if err != nil || !created || queued.Request.AttachmentRevision != 12 {
		t.Fatalf("queued=%+v created=%v err=%v", queued, created, err)
	}
	claimed, err := f.s.Claim(ctx, f.reporter, f.project, runtime.ID, testLease)
	if err != nil || claimed == nil || claimed.Request.AttachmentRevision != 12 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
}
