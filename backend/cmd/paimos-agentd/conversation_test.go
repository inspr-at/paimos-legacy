// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestConfiguredConversationIsExplicitCompleteAndCurrent(t *testing.T) {
	stateRoot := privateConversationTestDir(t)
	accountHome := privateConversationTestDir(t)
	raw, _ := json.Marshal(map[string]any{"accounts": []map[string]string{{
		"key": "conversation-account", "home": accountHome, "email": "conversation@example.invalid",
	}}})
	registry, err := agentd.ParseCodexAccountRegistry(raw)
	if err != nil {
		t.Fatal(err)
	}
	adapter := agentd.NewCodexAdapter("/fixture/codex", "test")
	adapter.SetAccounts(registry)
	supervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "conversation-config", StateRoot: stateRoot, Adapters: []agentd.Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	projectID := int64(1027)
	supervisor.BindAccountLifecycleProjects([]int64{projectID})
	attached, err := supervisor.ApplyAccountLifecycle(context.Background(), agentd.AccountLifecycleRequest{
		IdempotencyKey: "conversation-attach", Operation: agentd.AccountLifecycleConnect, ProjectID: projectID,
		RuntimeGeneration: supervisor.Status().DaemonID, AccountKey: "conversation-account", Adapter: agentd.AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "2", agentd.AdapterCodex)
	registration := lifecycleintents.Registration{
		Generation: supervisor.Status().DaemonID, Host: "fixture", SchemaVersion: lifecycleintents.AccountLifecycleSchemaV4,
		AccountScopes: []lifecycleintents.AccountScope{{
			AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "conversation-account", Label: "Conversation"}},
			Profiles: []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}}, AttachmentRevision: attached.Revision,
			AccountAvailability: lifecycleintents.AccountAvailabilityAvailable,
		}}, Workspaces: []lifecycleintents.Workspace{},
	}
	lease, _ := lifecycleclient.NewProof()
	authority, err := lifecycleclient.NewHTTP("http://127.0.0.1", projectID, lease, func() (string, error) { return "fixture", nil })
	if err != nil {
		t.Fatal(err)
	}
	project := &projectLifecycle{
		owner: &daemonLifecycle{supervisor: supervisor}, registration: registration, authority: authority,
		config: configuredProject{ProjectID: projectID, Conversation: &configuredConversation{
			AccountKey: "conversation-account", AttachmentRevision: attached.Revision, DispatchProfileID: profile.ID,
			DispatchProfileVersion: profile.Version, ExecutionPolicyID: lifecycleclient.ConversationPolicyV1,
			MaxOutputBytes: 256 << 10, MaxEvents: 512,
		}},
	}
	projectDir := filepath.Join(stateRoot, "conversation-project")
	if err = os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = configureProjectConversation(project, projectDir); err != nil || project.conversation == nil {
		t.Fatalf("complete binding not wired: %v", err)
	}
	if project.registration.Conversation == nil || project.registration.Conversation.AccountKey != "conversation-account" ||
		project.registration.Conversation.MaxOutputBytes != 256<<10 || project.registration.Conversation.MaxEvents != 512 {
		t.Fatalf("initialized consumer readiness not advertised: %+v", project.registration.Conversation)
	}
	project.config.Conversation.ExecutionPolicyID = "ordinary-coding"
	if err = configureProjectConversation(project, projectDir); err == nil {
		t.Fatal("unsupported execution policy was accepted")
	}
	project.config.Conversation.ExecutionPolicyID = lifecycleclient.ConversationPolicyV1
	project.config.Conversation.AttachmentRevision++
	if err = configureProjectConversation(project, projectDir); err == nil {
		t.Fatal("stale attachment revision was accepted")
	}
	if project.registration.Conversation != nil || project.conversation != nil {
		t.Fatal("failed consumer configuration retained readiness")
	}
}

func TestMissingConversationConfigurationDoesNotAdvertiseReadiness(t *testing.T) {
	project := &projectLifecycle{registration: lifecycleintents.Registration{Conversation: &lifecycleintents.ConversationCapability{SchemaVersion: 1}}}
	if err := configureProjectConversation(project, privateConversationTestDir(t)); err != nil {
		t.Fatal(err)
	}
	if project.conversation != nil || project.registration.Conversation != nil {
		t.Fatal("absent consumer configuration advertised conversation readiness")
	}
}

func privateConversationTestDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}
