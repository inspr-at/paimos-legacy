// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package conversationturns

import (
	"testing"

	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestBindingMatchesOnlyExactCurrentConversationCapability(t *testing.T) {
	runtime := lifecycleintents.Runtime{
		ID: "11111111-1111-4111-8111-111111111111", Generation: "22222222-2222-4222-8222-222222222222",
		MachineID: "owned-host", SchemaVersion: lifecycleintents.AccountLifecycleSchemaV4,
		AccountScopes: []lifecycleintents.AccountScope{{
			AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "acct-main", Label: "Main"}},
			Profiles: []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}}, AttachmentRevision: 7,
			AccountAvailability: lifecycleintents.AccountAvailabilityAvailable,
		}},
		Conversation: &lifecycleintents.ConversationCapability{
			SchemaVersion: lifecycleintents.ConversationSchemaV1, AccountKey: "acct-main", AttachmentRevision: 7,
			DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2",
			ExecutionPolicyID: ExecutionPolicyID, MaxOutputBytes: MaximumOutputBytes, MaxEvents: MaximumEvents,
		},
	}
	binding := Binding{
		RuntimeID: runtime.ID, RuntimeGeneration: runtime.Generation, HostID: runtime.MachineID, AccountLabel: "chatgpt",
		AccountKey: "acct-main", AttachmentRevision: 7, DispatchProfileID: "codex-sol-high",
		DispatchProfileVersion: "2", ExecutionPolicyID: ExecutionPolicyID,
		Limits: Limits{MaxOutputBytes: MaximumOutputBytes, MaxEvents: MaximumEvents},
	}
	if !bindingMatchesRuntime(binding, runtime) {
		t.Fatal("exact initialized capability did not match")
	}

	for name, mutate := range map[string]func(*lifecycleintents.Runtime){
		"absent": func(r *lifecycleintents.Runtime) { r.Conversation = nil },
		"account": func(r *lifecycleintents.Runtime) {
			r.AccountScopes[0].Accounts[0].Key = "acct-other"
			r.Conversation.AccountKey = "acct-other"
		},
		"revision": func(r *lifecycleintents.Runtime) {
			r.AccountScopes[0].AttachmentRevision = 8
			r.Conversation.AttachmentRevision = 8
		},
		"profile": func(r *lifecycleintents.Runtime) {
			r.AccountScopes[0].Profiles[0].ID = "codex-luna-medium"
			r.Conversation.DispatchProfileID = "codex-luna-medium"
		},
		"policy":     func(r *lifecycleintents.Runtime) { r.Conversation.ExecutionPolicyID = "next-policy" },
		"output_cap": func(r *lifecycleintents.Runtime) { r.Conversation.MaxOutputBytes = 128 << 10 },
		"event_cap":  func(r *lifecycleintents.Runtime) { r.Conversation.MaxEvents = 256 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := runtime
			candidate.AccountScopes = append([]lifecycleintents.AccountScope(nil), runtime.AccountScopes...)
			candidate.AccountScopes[0].Accounts = append([]lifecycleintents.AccountChoice(nil), runtime.AccountScopes[0].Accounts...)
			candidate.AccountScopes[0].Profiles = append([]lifecycleintents.Profile(nil), runtime.AccountScopes[0].Profiles...)
			capability := *runtime.Conversation
			candidate.Conversation = &capability
			mutate(&candidate)
			if bindingMatchesRuntime(binding, candidate) {
				t.Fatalf("stale binding matched changed %s capability", name)
			}
		})
	}
}
