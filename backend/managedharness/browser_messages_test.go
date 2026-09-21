// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecyclefence"
	"github.com/inspr-at/paimos/backend/models"
)

type browserMessageFixture struct {
	service    *Service
	principal  auth.Principal
	project    int64
	user       int64
	runtimeKey int64
	session    models.HarnessSession
}

func newBrowserMessageFixture(t *testing.T) browserMessageFixture {
	return newBrowserMessageFixtureWithRuntime(t, "", "")
}

func newBrowserMessageFixtureWithRegistration(t *testing.T, registration func(generation, host string) string) browserMessageFixture {
	t.Helper()
	return newBrowserMessageFixtureRegistration(t, "", "", registration)
}

func newBrowserMessageFixtureWithRuntime(t *testing.T, machine, account string) browserMessageFixture {
	t.Helper()
	return newBrowserMessageFixtureRegistration(t, machine, account, nil)
}

func newBrowserMessageFixtureRegistration(t *testing.T, machine, account string, registrationFn func(generation, host string) string) browserMessageFixture {
	t.Helper()
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "browser-message-test")
	project, _ := openManagedHarnessTestDB(t)
	var user int64
	if db.DB.QueryRow(`SELECT id FROM users WHERE username='harness-actor'`).Scan(&user) != nil {
		t.Fatal("fixture user")
	}
	credential := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at)
		VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'))`, uuid.NewString(), user, credential); err != nil {
		t.Fatal(err)
	}
	principal, _ := auth.NewSessionPrincipal(credential, user, user, false)
	service := NewService(db.DB)
	workspace := &models.HarnessWorkspaceProvenance{CanonicalPath: "/workspace/browser-message",
		GitTopLevel: "/workspace/browser-message", GitBranch: "test/browser-message",
		Identity: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Kind:     "git_worktree", Mode: "exclusive"}
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: project, AgentName: "worker", Harness: "codex", Host: "browser-message-host",
		SessionRef: uuid.NewString(), WorkerLease: testWorkerLease, ManagementMode: ManagementManaged,
		Role: RoleWorker, SteerMode: SteerOwned, AccountLabel: "chatgpt",
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
		Workspace:    workspace, DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	reporter, err := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin)
		VALUES('browser-message-reporter','disabled','admin','super_admin','active',1)`)
	if err != nil {
		t.Fatal(err)
	}
	reporterID, _ := reporter.LastInsertId()
	key, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'browser-message','fixture-browser-message-hash','pfx','agent-controls:runner')`, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := key.LastInsertId()
	runtimeID, runtimeGeneration, sessionGeneration := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if machine == "" {
		machine = session.Host
	}
	if account == "" {
		account = session.AccountLabel
	}
	registration := fmt.Sprintf(`{"generation":%q,"host":%q,"account_label":%q,"workspaces":[],"profiles":[]}`,
		runtimeGeneration, machine, account)
	if registrationFn != nil {
		registration = registrationFn(runtimeGeneration, machine)
	}
	if _, err = db.DB.Exec(`INSERT INTO lifecycle_runtimes(
		id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,expires_at,created_at)
		VALUES(?,?,?,?,?,?,zeroblob(32),?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+10 minutes'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		runtimeID, project, runtimeGeneration, machine, reporterID, keyID, registration); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.Exec(`INSERT INTO lifecycle_runtime_sessions(session_id,runtime_id,generation,created_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, session.ID, runtimeID, sessionGeneration); err != nil {
		t.Fatal(err)
	}
	return browserMessageFixture{service: service, principal: principal, project: project, user: user,
		runtimeKey: keyID, session: session}
}

func browserMessageRequest(session models.HarnessSession, utterance, level, text string) BrowserMessageRequest {
	return BrowserMessageRequest{SchemaVersion: 1, UtteranceID: utterance,
		ExpectedRevision: session.Revision, Text: text, DeliveryLevel: level}
}

func TestBrowserMessageCASPreservesHumanAttributionLevelsAndReplay(t *testing.T) {
	f := newBrowserMessageFixture(t)
	ctx := context.Background()
	simple := browserMessageRequest(f.session, "utt_0123456789abcdef0123456789abcdef", "simple", "First line\nSecond line")
	first, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, simple)
	if err != nil {
		t.Fatal(err)
	}
	steer := browserMessageRequest(f.session, "utt_1123456789abcdef0123456789abcdef", "steer", "Continue without more tools")
	steered, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, steer)
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageID == steered.MessageID || first.DeliveryID == steered.DeliveryID || steered.DeliveryLevel != "steer" {
		t.Fatal("delivery levels did not retain distinct identity")
	}
	var count, sessions, bindings int
	var body, role, from, level, target string
	var sender int64
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM harness_message_receipts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("receipt count=%d err=%v", count, err)
	}
	if err = db.DB.QueryRow(`SELECT message.body,message.role,message.from_address,message.from_user_id,
		delivery.requested_level,delivery.primary_target_id
		FROM agent_messages message JOIN agent_message_deliveries delivery ON delivery.message_row_id=message.id
		WHERE message.message_id=?`, first.MessageID).Scan(&body, &role, &from, &sender, &level, &target); err != nil {
		t.Fatal(err)
	}
	if body != simple.Text || role != "human" || from != fmt.Sprintf("user:%d", f.user) || sender != f.user || level != "simple" || target != f.session.MessageTargetID {
		t.Fatalf("canonical message changed: %q %q %q %d %q %q", body, role, from, sender, level, target)
	}
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM product_sessions WHERE target_project_agent_id=?`, f.session.ProjectAgentID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("product sessions=%d err=%v", sessions, err)
	}
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM harness_conversation_bindings WHERE harness_session_id=?`, f.session.ID).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("bindings=%d err=%v", bindings, err)
	}
	if _, err = f.service.Stop(ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, simple)
	if err != nil || replay != first {
		t.Fatalf("terminal replay=%+v err=%v", replay, err)
	}
	changed := simple
	changed.Text = "Changed text"
	if _, err = f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, changed); err != ErrBrowserConflict {
		t.Fatalf("changed replay error=%v", err)
	}
	if _, err = db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, f.principal.SessionCredentialID()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, simple); err != ErrBrowserUnavailable {
		t.Fatalf("revoked human replay error=%v", err)
	}
}

func TestBrowserMessageCASClosesAuthorityFreshnessAndValidation(t *testing.T) {
	f := newBrowserMessageFixture(t)
	ctx := context.Background()
	request := browserMessageRequest(f.session, "utt_2123456789abcdef0123456789abcdef", "simple", "Safe message")
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`, f.project, f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
		t.Fatalf("viewer mutated: %v", err)
	}
	if _, err := db.DB.Exec(`DELETE FROM project_members WHERE project_id=? AND user_id=?`, f.project, f.user); err != nil {
		t.Fatal(err)
	}
	stale := request
	stale.ExpectedRevision++
	if _, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, stale); err != ErrBrowserConflict {
		t.Fatalf("stale error=%v", err)
	}
	apiPrincipal, _ := auth.NewAPIKeyPrincipal(f.runtimeKey, f.user, auth.ParseScopes("*"))
	if _, err := f.service.SendBrowserMessageCAS(ctx, apiPrincipal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
		t.Fatalf("API key impersonated a human: %v", err)
	}
	for _, changed := range []BrowserMessageRequest{
		{SchemaVersion: 1, UtteranceID: request.UtteranceID, ExpectedRevision: request.ExpectedRevision, Text: "token=abcdef1234567890", DeliveryLevel: "simple"},
		{SchemaVersion: 1, UtteranceID: request.UtteranceID, ExpectedRevision: request.ExpectedRevision, Text: "nul\x00body", DeliveryLevel: "simple"},
		{SchemaVersion: 1, UtteranceID: request.UtteranceID, ExpectedRevision: request.ExpectedRevision, Text: "safe", DeliveryLevel: "unknown"},
	} {
		if _, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, changed); err != ErrBrowserInvalid {
			t.Fatalf("invalid body accepted: %v", err)
		}
	}
	if _, err := db.DB.Exec(`UPDATE agent_message_targets SET enabled=0 WHERE id=?`, f.session.MessageTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
		t.Fatalf("disabled exact target accepted: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE agent_message_targets SET enabled=1 WHERE id=?`, f.session.MessageTargetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE api_keys SET scopes='' WHERE id=?`, f.runtimeKey); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
		t.Fatalf("runtime scope revocation accepted: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE api_keys SET scopes='agent-controls:runner',disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, f.runtimeKey); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendBrowserMessageCAS(ctx, f.principal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
		t.Fatalf("disabled runtime authority accepted: %v", err)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM harness_message_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refusals mutated: count=%d err=%v", count, err)
	}
}

func TestBrowserMessageCASRejectsWrongRuntimeProvenance(t *testing.T) {
	for _, test := range []struct {
		name, machine, account string
	}{
		{name: "machine", machine: "another-machine"},
		{name: "account", account: "another-account"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newBrowserMessageFixtureWithRuntime(t, test.machine, test.account)
			request := browserMessageRequest(f.session, "utt_4123456789abcdef0123456789abcdef", "simple", "Safe message")
			if _, err := f.service.SendBrowserMessageCAS(context.Background(), f.principal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
				t.Fatalf("wrong runtime %s accepted: %v", test.name, err)
			}
		})
	}
}

func TestBrowserMessageCASUsesV3ScopeFence(t *testing.T) {
	v3 := func(scopes string) func(generation, host string) string {
		return func(generation, host string) string {
			return fmt.Sprintf(`{"schema_version":3,"generation":%q,"host":%q,"workspaces":[],"account_scopes":%s}`, generation, host, scopes)
		}
	}
	t.Run("match", func(t *testing.T) {
		f := newBrowserMessageFixtureWithRegistration(t, v3(`[{"account_label":"chatgpt","profiles":[{"id":"codex-sol-high","version":"2"}]},{"account_label":"cursor_context","accounts":[{"key":"cursor-op","label":"Cursor"}],"profiles":[{"id":"cursor-composer","version":"2"}]}]`))
		request := browserMessageRequest(f.session, "utt_6123456789abcdef0123456789abcdef", "simple", "Scoped tell")
		if _, err := f.service.SendBrowserMessageCAS(context.Background(), f.principal, f.project, f.session.ID, request); err != nil {
			t.Fatalf("matching v3 chatgpt scope refused: %v", err)
		}
	})
	t.Run("cross-class", func(t *testing.T) {
		f := newBrowserMessageFixtureWithRegistration(t, v3(`[{"account_label":"cursor_context","accounts":[{"key":"cursor-op","label":"Cursor"}],"profiles":[{"id":"cursor-composer","version":"2"}]}]`))
		request := browserMessageRequest(f.session, "utt_7123456789abcdef0123456789abcdef", "simple", "Cross class")
		if _, err := f.service.SendBrowserMessageCAS(context.Background(), f.principal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
			t.Fatalf("cross-class v3 tell accepted: %v", err)
		}
	})
	t.Run("wrong-key", func(t *testing.T) {
		f := newBrowserMessageFixtureWithRegistration(t, v3(`[{"account_label":"chatgpt","accounts":[{"key":"codex-work","label":"Work"}],"profiles":[{"id":"codex-sol-high","version":"2"}]}]`))
		request := browserMessageRequest(f.session, "utt_8123456789abcdef0123456789abcdef", "simple", "Wrong key")
		if _, err := f.service.SendBrowserMessageCAS(context.Background(), f.principal, f.project, f.session.ID, request); err != ErrBrowserUnavailable {
			t.Fatalf("named chatgpt scope accepted class-only session: %v", err)
		}
	})
}

func TestBrowserMessageCASRejectsStoppingGeneration(t *testing.T) {
	f := newBrowserMessageFixture(t)
	stopping, err := f.service.Heartbeat(context.Background(), f.session.ID, PhaseStopping)
	if err != nil {
		t.Fatal(err)
	}
	request := browserMessageRequest(stopping, "utt_5123456789abcdef0123456789abcdef", "simple", "Safe message")
	if _, err := f.service.SendBrowserMessageCAS(context.Background(), f.principal, f.project, stopping.ID, request); err != ErrBrowserUnavailable {
		t.Fatalf("stopping generation accepted: %v", err)
	}
}

func TestBrowserMessageCASConcurrentReplayCreatesOneEffect(t *testing.T) {
	f := newBrowserMessageFixture(t)
	request := browserMessageRequest(f.session, "utt_3123456789abcdef0123456789abcdef", "steer", "One browser gesture")
	results := make(chan BrowserMessageResponse, 8)
	errors := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			out, err := f.service.SendBrowserMessageCAS(context.Background(), f.principal, f.project, f.session.ID, request)
			results <- out
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	var messageID, deliveryID string
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if messageID != "" && (messageID != result.MessageID || deliveryID != result.DeliveryID) {
			t.Fatal("concurrent replay returned another effect")
		}
		messageID, deliveryID = result.MessageID, result.DeliveryID
	}
	var messages, deliveries, receipts int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE message_id=?`, messageID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries WHERE delivery_id=?`, deliveryID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM harness_message_receipts WHERE utterance_id=?`, request.UtteranceID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || deliveries != 1 || receipts != 1 {
		t.Fatalf("effects=%d/%d/%d", messages, deliveries, receipts)
	}
}

func TestBrowserMessageTextPreservesMultilineAndRejectsControls(t *testing.T) {
	if !validBrowserMessageText("first\r\nsecond\n\tthird") {
		t.Fatal("ordinary multiline text rejected")
	}
	for _, value := range []string{"", " padded", "trailing\n", "x\x00y", "x\x01y", "Bearer abcdefgh12345678", string(make([]byte, browserMessageMaxTextBytes+1))} {
		if validBrowserMessageText(value) {
			t.Fatalf("unsafe text accepted: %q", value)
		}
	}
}

func TestBrowserTargetQueriesKeepOwnershipFence(t *testing.T) {
	for name, query := range map[string]string{
		"simple": browserTargetQuerySimple,
		"steer":  browserTargetQuerySteer,
	} {
		if !strings.Contains(query, lifecyclefence.OwnershipSQLRuntimeHarness) {
			t.Fatalf("%s query lost ownership fence", name)
		}
		if !strings.Contains(query, "harness.account_label") {
			t.Fatalf("%s query missing session alias", name)
		}
	}
	if !strings.Contains(browserTargetQuerySimple, browserTargetLevelSimple) {
		t.Fatal("simple query lost level guard")
	}
	if !strings.Contains(browserTargetQuerySteer, browserTargetLevelSteer) {
		t.Fatal("steer query lost level guard")
	}
}
