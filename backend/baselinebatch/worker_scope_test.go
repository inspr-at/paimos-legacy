// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestWorkerMatchesRegistrationPreservesScopedBindings(t *testing.T) {
	workspace := uuid.NewString()
	mixed := lifecycleintents.Registration{
		SchemaVersion: lifecycleintents.AccountScopeSchemaV3,
		Workspaces:    []lifecycleintents.Workspace{{Handle: workspace, Identity: strings.Repeat("a", 64)}},
		AccountScopes: []lifecycleintents.AccountScope{
			{AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "codex-work", Label: "Work"}}, Profiles: []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}}},
			{AccountLabel: "cursor_context", Accounts: []lifecycleintents.AccountChoice{{Key: "cursor-op", Label: "Cursor"}}, Profiles: []lifecycleintents.Profile{{ID: "cursor-composer", Version: "2"}}},
		},
	}
	classOnly := lifecycleintents.Registration{
		SchemaVersion: lifecycleintents.AccountScopeSchemaV3,
		Workspaces:    []lifecycleintents.Workspace{{Handle: workspace, Identity: strings.Repeat("a", 64)}},
		AccountScopes: []lifecycleintents.AccountScope{
			{AccountLabel: "claude_ai_max", Profiles: []lifecycleintents.Profile{{ID: "claude-opus-xhigh", Version: "2"}}},
		},
	}
	v2 := lifecycleintents.Registration{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2,
		AccountLabel:  "chatgpt",
		Accounts:      []lifecycleintents.AccountChoice{{Key: "coordinator", Label: "Coordinator"}},
		Profiles:      []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}},
		Workspaces:    []lifecycleintents.Workspace{{Handle: workspace, Identity: strings.Repeat("a", 64)}},
	}
	v4 := lifecycleintents.Registration{
		SchemaVersion: lifecycleintents.AccountLifecycleSchemaV4,
		Workspaces:    []lifecycleintents.Workspace{{Handle: workspace, Identity: strings.Repeat("a", 64)}},
		AccountScopes: []lifecycleintents.AccountScope{{
			AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "codex-work", Label: "Work"}},
			Profiles:           []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}},
			AttachmentRevision: 5, AccountAvailability: lifecycleintents.AccountAvailabilityAvailable,
		}},
	}
	base := WorkerSelection{
		WorkerName: "builder", RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(),
		AccountLabel: "chatgpt", AccountKey: "codex-work", ProfileID: "codex-sol-high", ProfileVersion: "2",
		WorkspaceHandle: workspace,
	}
	cases := []struct {
		name   string
		reg    lifecycleintents.Registration
		worker WorkerSelection
		want   string
	}{
		{name: "v3 mixed chatgpt", reg: mixed, worker: base},
		{name: "v4 exact attachment revision", reg: v4, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "chatgpt", AccountKey: "codex-work", AttachmentRevision: 5,
			ProfileID: "codex-sol-high", ProfileVersion: "2", WorkspaceHandle: workspace,
		}},
		{name: "v4 missing attachment revision", reg: v4, worker: base, want: "advertised scope"},
		{name: "v3 mixed cursor", reg: mixed, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "cursor_context", AccountKey: "cursor-op", ProfileID: "cursor-composer", ProfileVersion: "2",
			WorkspaceHandle: workspace,
		}},
		{name: "class-only claude omits key", reg: classOnly, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "claude_ai_max", ProfileID: "claude-opus-xhigh", ProfileVersion: "2",
			WorkspaceHandle: workspace,
		}},
		{name: "v2 named key", reg: v2, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "chatgpt", AccountKey: "coordinator", ProfileID: "codex-sol-high", ProfileVersion: "2",
			WorkspaceHandle: workspace,
		}},
		{name: "cross-scope profile", reg: mixed, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "chatgpt", AccountKey: "codex-work", ProfileID: "cursor-composer", ProfileVersion: "2",
			WorkspaceHandle: workspace,
		}, want: "advertised scope"},
		{name: "cross-scope key", reg: mixed, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "chatgpt", AccountKey: "cursor-op", ProfileID: "codex-sol-high", ProfileVersion: "2",
			WorkspaceHandle: workspace,
		}, want: "advertised scope"},
		{name: "cursor class-only refused", reg: mixed, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "cursor_context", ProfileID: "cursor-composer", ProfileVersion: "2",
			WorkspaceHandle: workspace,
		}, want: "advertised scope"},
		{name: "invented claude key refused", reg: classOnly, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "claude_ai_max", AccountKey: "claude-home", ProfileID: "claude-opus-xhigh", ProfileVersion: "2",
			WorkspaceHandle: workspace,
		}, want: "advertised scope"},
		{name: "unknown schema", reg: lifecycleintents.Registration{SchemaVersion: 9, Workspaces: v2.Workspaces, AccountLabel: "chatgpt", Profiles: v2.Profiles}, worker: base, want: "unknown runtime account schema"},
		{name: "missing v3 scopes", reg: lifecycleintents.Registration{SchemaVersion: 3, Workspaces: v2.Workspaces}, worker: base, want: "missing account scopes"},
		{name: "ambiguous v3 scopes", reg: lifecycleintents.Registration{
			SchemaVersion: 3, Workspaces: v2.Workspaces,
			AccountScopes: []lifecycleintents.AccountScope{
				{AccountLabel: "chatgpt", Profiles: []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}}},
				{AccountLabel: "chatgpt", Profiles: []lifecycleintents.Profile{{ID: "codex-sol-xhigh", Version: "2"}}},
			},
		}, worker: base, want: "ambiguous account scopes"},
		{name: "workspace mismatch", reg: mixed, worker: WorkerSelection{
			WorkerName: "builder", RuntimeID: base.RuntimeID, RuntimeGeneration: base.RuntimeGeneration,
			AccountLabel: "chatgpt", AccountKey: "codex-work", ProfileID: "codex-sol-high", ProfileVersion: "2",
			WorkspaceHandle: uuid.NewString(),
		}, want: "workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := workerMatchesRegistration(tc.worker, tc.reg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

func TestRequireAgentWorkerBindingDoesNotInventAKey(t *testing.T) {
	err := requireAgentWorkerBinding(WorkerSelection{
		RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(), AccountLabel: "claude_ai_max",
		ProfileID: "claude-opus-xhigh", ProfileVersion: "2", WorkspaceHandle: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("class-only binding: %v", err)
	}
	if err := requireAgentWorkerBinding(WorkerSelection{AccountLabel: "chatgpt"}); err == nil {
		t.Fatal("incomplete binding accepted")
	}
}

func TestReviewBindingIncludesClassAndGeneration(t *testing.T) {
	draft := Draft{
		Revision: 1,
		Baseline: BaselineClaim{ContentDigest: "sha256:digest", RevisionSeal: "sha256:seal"},
	}
	base := WorkerSelection{
		WorkerName: "builder", RuntimeID: "rt", RuntimeGeneration: "gen-a",
		AccountLabel: "chatgpt", AccountKey: "codex-work", ProfileID: "codex-sol-high", ProfileVersion: "2",
		WorkspaceHandle: "ws",
	}
	left := reviewBinding(draft, ModeAssisted, []string{"req.a"}, base)
	class := base
	class.AccountLabel = "cursor_context"
	if left == reviewBinding(draft, ModeAssisted, []string{"req.a"}, class) {
		t.Fatal("account class change reused the reviewed binding")
	}
	generation := base
	generation.RuntimeGeneration = "gen-b"
	if left == reviewBinding(draft, ModeAssisted, []string{"req.a"}, generation) {
		t.Fatal("runtime generation change reused the reviewed binding")
	}
	revision := base
	revision.AttachmentRevision = 2
	if left == reviewBinding(draft, ModeAssisted, []string{"req.a"}, revision) {
		t.Fatal("attachment revision change reused the reviewed binding")
	}
}
