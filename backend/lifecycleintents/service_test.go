package lifecycleintents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecyclefence"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

func TestConversationCapabilityPersistsInRuntimeRegistration(t *testing.T) {
	f := setup(t)
	f.now = f.now.Add(RuntimeTTLSeconds*time.Second + time.Second)
	in := Registration{
		Generation: uuid.NewString(), Host: f.registration.Host, SchemaVersion: AccountLifecycleSchemaV4,
		Workspaces: f.registration.Workspaces,
		AccountScopes: []AccountScope{{
			AccountLabel: "chatgpt", Accounts: []AccountChoice{{Key: "acct-main", Label: "Main"}},
			Profiles: []Profile{{ID: "codex-sol-high", Version: "2"}}, AttachmentRevision: 7,
			AccountAvailability: AccountAvailabilityAvailable,
		}},
		Conversation: &ConversationCapability{
			SchemaVersion: ConversationSchemaV1, AccountKey: "acct-main", AttachmentRevision: 7,
			DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2",
			ExecutionPolicyID: ConversationExecutionPolicyV1, MaxOutputBytes: 256 << 10, MaxEvents: 512,
		},
	}
	runtime, err := f.s.RegisterRuntime(context.Background(), f.reporter, f.project, testLease, in)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.DB.QueryRow(`SELECT registration_json FROM lifecycle_runtimes WHERE id=?`, runtime.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var round Registration
	if err := json.Unmarshal([]byte(stored), &round); err != nil || round.Conversation == nil ||
		*round.Conversation != *in.Conversation || runtime.Conversation == nil || *runtime.Conversation != *in.Conversation {
		t.Fatalf("persisted capability mismatch err=%v body=%s runtime=%+v", err, stored, runtime.Conversation)
	}
}

const testLease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type fixture struct {
	s               *Service
	project, user   int64
	human, reporter auth.Principal
	runtime         Runtime
	registration    Registration
	now             time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	return setupWithClock(t, time.Now)
}

func setupWithClock(t *testing.T, now func() time.Time) *fixture {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	openLifecycleFixtureDB(t)
	t.Cleanup(func() { db.DB.Close(); db.DB = nil })
	f := &fixture{s: NewService(db.DB), now: now().UTC()}
	f.s.now = func() time.Time { return f.now }
	res, e := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin) VALUES('lifecycle-admin','disabled','admin','super_admin','active',1)`)
	if e != nil {
		t.Fatal(e)
	}
	f.user, _ = res.LastInsertId()
	res, e = db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Lifecycle fixture','LCF')`)
	if e != nil {
		t.Fatal(e)
	}
	f.project, _ = res.LastInsertId()
	_, e = db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, f.project)
	if e != nil {
		t.Fatal(e)
	}
	credential := uuid.NewString()
	_, e = db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at) VALUES(?,?,?,?,?)`, uuid.NewString(), f.user, credential, stamp(f.now.Add(time.Hour)), stamp(f.now))
	if e != nil {
		t.Fatal(e)
	}
	f.human, e = auth.NewSessionPrincipal(credential, f.user, f.user, false)
	if e != nil {
		t.Fatal(e)
	}
	res, e = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'fixture','not-a-credential','fixture','*')`, f.user)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := res.LastInsertId()
	f.reporter, e = auth.NewAPIKeyPrincipal(key, f.user, auth.ParseScopes("*"))
	if e != nil {
		t.Fatal(e)
	}
	f.registration = Registration{Generation: uuid.NewString(), Host: "fixture-machine", AccountLabel: "chatgpt", Workspaces: []Workspace{{Handle: uuid.NewString(), Identity: fmt.Sprintf("%064x", 1)}}, Profiles: []Profile{{ID: "codex-sol-high", Version: "2"}}}
	f.runtime, e = f.s.RegisterRuntime(context.Background(), f.reporter, f.project, testLease, f.registration)
	if e != nil {
		t.Fatal(e)
	}
	return f
}

func TestLifecycleFixtureClockAndFutureCreatedCredential(t *testing.T) {
	// Keep the service clock independent of wall time so a fixture that creates
	// credentials with SQLite datetime('now') fails deterministically.
	f := setupWithClock(t, func() time.Time {
		return time.Date(2026, time.January, 2, 3, 4, 5, 123000000, time.UTC)
	})
	f.submit(t, f.request("repair"))

	// Crossing the next whole second reproduces the original authority refusal:
	// a credential created after the frozen service clock must remain unusable.
	created := f.now.Truncate(time.Second).Add(time.Second)
	credential := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at) VALUES(?,?,?,?,?)`, uuid.NewString(), f.user, credential, stamp(created.Add(time.Hour)), stamp(created)); err != nil {
		t.Fatal(err)
	}
	var err error
	f.human, err = auth.NewSessionPrincipal(credential, f.user, f.user, false)
	if err != nil {
		t.Fatal(err)
	}
	request := f.request("repair")
	if _, created, err := f.s.Submit(context.Background(), f.human, f.project, request); !errors.Is(err, ErrUnavailable) || created {
		t.Fatalf("future-created credential: created=%v error=%v", created, err)
	}
	var intents int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM lifecycle_intents`).Scan(&intents); err != nil || intents != 1 {
		t.Fatalf("refused credential changed ledger: intents=%d error=%v", intents, err)
	}
	// Once the same clock reaches creation, the unchanged credential and request
	// are valid; no auth rule or production clock needs to be relaxed.
	f.now = created
	f.submit(t, request)
}

func (f *fixture) request(operation string) Request {
	r := Request{RequestKey: uuid.NewString(), Operation: operation, RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, AccountLabel: f.runtime.AccountLabel, TTLSeconds: 120}
	if operation == "repair" {
		r.RepairLayer = "reporter"
	} else {
		r.WorkspaceHandle = f.runtime.Workspaces[0].Handle
		r.AgentName = "worker"
		r.DispatchProfileID = "codex-sol-high"
		r.DispatchProfileVersion = "2"
		r.WorkShape = "unknown"
		r.Role = "worker"
	}
	return r
}
func (f *fixture) submit(t *testing.T, r Request) Intent {
	t.Helper()
	in, created, e := f.s.Submit(context.Background(), f.human, f.project, r)
	if e != nil || !created {
		t.Fatalf("submit created=%v error=%v", created, e)
	}
	return in
}
func (f *fixture) claim(t *testing.T) Intent {
	t.Helper()
	in, e := f.s.Claim(context.Background(), f.reporter, f.project, f.runtime.ID, testLease)
	if e != nil || in == nil {
		t.Fatalf("claim error=%v", e)
	}
	return *in
}
func (f *fixture) transition(t *testing.T, in Intent, state, result string) Intent {
	t.Helper()
	reason := ""
	if state == "completed" {
		reason = "applied"
	}
	out, e := f.s.Transition(context.Background(), f.reporter, f.project, in.ID, testLease, Transition{RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: in.Revision, State: state, Reason: reason, ResultSessionID: result})
	if e != nil {
		t.Fatalf("transition %s: %v", state, e)
	}
	return out
}

func prepareAttachIntent(t *testing.T) (*fixture, *managedharness.Service, models.HarnessSession, int64, Request) {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	current := f.managed(t)
	generation := uuid.NewString()
	if err := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: current.ID, Generation: generation}); err != nil {
		t.Fatal(err)
	}
	hs := managedharness.NewService(db.DB)
	current, err := hs.HeartbeatWithActivity(ctx, current.ID, "yielded", managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_completed"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = time.Now().UTC().Add(time.Second)
	result, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,title,type) VALUES(?,1,'Attach target','ticket')`, f.project)
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := result.LastInsertId()
	req := f.request("attach")
	req.SessionID = current.ID
	req.SessionGeneration = generation
	req.ExpectedRevision = current.Revision
	req.TicketID = &ticket
	req.WorkShape = "ship"
	return f, hs, current, ticket, req
}

func replayReporterPoll(t *testing.T, f *fixture, hs *managedharness.Service, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := hs.HeartbeatWithActivity(ctx, sessionID, "working", managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_completed"}); err != nil {
		t.Fatal(err)
	}
	if result, err := hs.Yield(ctx, sessionID); err != nil || len(result.Controls) != 0 {
		t.Fatalf("replay-equivalent yield=%+v err=%v", result, err)
	}
	f.now = time.Now().UTC().Add(time.Second)
}

func TestLifecycleAttachSurvivesReplayEquivalentReporterPolls(t *testing.T) {
	f, hs, current, ticket, req := prepareAttachIntent(t)
	in := f.submit(t, req)
	replayReporterPoll(t, f, hs, current.ID)
	in = f.claim(t)
	replayReporterPoll(t, f, hs, current.ID)
	in = f.transition(t, in, "executing", "")
	replayReporterPoll(t, f, hs, current.ID)
	in = f.transition(t, in, "completed", current.ID)
	bound, err := hs.Get(context.Background(), f.project, current.ID)
	if err != nil || in.ResultSessionID != current.ID || bound.TicketID == nil || *bound.TicketID != ticket || bound.Revision <= req.ExpectedRevision {
		t.Fatalf("attach result=%+v bound=%+v err=%v", in, bound, err)
	}
	var laterEvents int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=? AND event_sequence>? AND operation<>'binding_changed'`, current.ID, req.ExpectedRevision).Scan(&laterEvents); err != nil || laterEvents != 0 {
		t.Fatalf("replay polls emitted semantic events=%d err=%v", laterEvents, err)
	}
}

func TestLifecycleAttachRejectsInterveningSemanticChanges(t *testing.T) {
	t.Run("stale submit", func(t *testing.T) {
		f, hs, current, _, req := prepareAttachIntent(t)
		replayReporterPoll(t, f, hs, current.ID)
		if _, created, err := f.s.Submit(context.Background(), f.human, f.project, req); err != ErrUnavailable || created {
			t.Fatalf("stale submit created=%v err=%v", created, err)
		}
	})

	t.Run("binding", func(t *testing.T) {
		f, hs, current, _, req := prepareAttachIntent(t)
		in := f.submit(t, req)
		other, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,title,type) VALUES(?,2,'Concurrent binding','ticket')`, f.project)
		if err != nil {
			t.Fatal(err)
		}
		otherTicket, _ := other.LastInsertId()
		if _, err = hs.AssignBinding(context.Background(), managedharness.BindingInput{ProjectID: f.project, SessionID: current.ID, ExpectedRevision: current.Revision, TicketID: &otherTicket, WorkShape: "ship"}); err != nil {
			t.Fatal(err)
		}
		claimed, err := f.s.Claim(context.Background(), f.reporter, f.project, f.runtime.ID, testLease)
		if err != nil || claimed != nil {
			t.Fatalf("semantic binding claim=%+v err=%v", claimed, err)
		}
		failed, err := f.s.Get(context.Background(), f.human, f.project, in.ID)
		if err != nil || failed.State != "failed" || failed.Reason != "ownership_lost" {
			t.Fatalf("semantic binding outcome=%+v err=%v", failed, err)
		}
	})

	t.Run("control", func(t *testing.T) {
		f, hs, current, _, req := prepareAttachIntent(t)
		in := f.submit(t, req)
		if _, err := hs.RequestControl(context.Background(), current.ID, "interrupt", f.user); err != nil {
			t.Fatal(err)
		}
		if result, err := hs.Yield(context.Background(), current.ID); err != nil || len(result.Controls) != 1 {
			t.Fatalf("control yield=%+v err=%v", result, err)
		}
		f.now = time.Now().UTC().Add(time.Second)
		claimed, err := f.s.Claim(context.Background(), f.reporter, f.project, f.runtime.ID, testLease)
		if err != nil || claimed != nil {
			t.Fatalf("semantic control claim=%+v err=%v", claimed, err)
		}
		failed, err := f.s.Get(context.Background(), f.human, f.project, in.ID)
		if err != nil || failed.State != "failed" || failed.Reason != "ownership_lost" {
			t.Fatalf("semantic control outcome=%+v err=%v", failed, err)
		}
	})

	t.Run("stopping", func(t *testing.T) {
		f, hs, current, _, req := prepareAttachIntent(t)
		in := f.submit(t, req)
		if _, err := hs.HeartbeatWithActivity(context.Background(), current.ID, "stopping", managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_completed"}); err != nil {
			t.Fatal(err)
		}
		f.now = time.Now().UTC().Add(time.Second)
		claimed, err := f.s.Claim(context.Background(), f.reporter, f.project, f.runtime.ID, testLease)
		if err != nil || claimed != nil {
			t.Fatalf("stopping claim=%+v err=%v", claimed, err)
		}
		failed, err := f.s.Get(context.Background(), f.human, f.project, in.ID)
		if err != nil || failed.State != "failed" || failed.Reason != "ownership_lost" {
			t.Fatalf("stopping outcome=%+v err=%v", failed, err)
		}
	})
}

func TestLifecycleRepairDurableReplayAndConcurrency(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := f.request("repair")
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, _, e := NewService(db.DB).Submit(ctx, f.human, f.project, r)
			ids <- in.ID
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("duplicate request made multiple intents")
		}
		id = v
	}
	r.TTLSeconds++
	if _, _, e := f.s.Submit(ctx, f.human, f.project, r); !errors.Is(e, ErrConflict) {
		t.Fatal("conflicting request key accepted")
	}
	in := f.claim(t)
	if in.ID != id || in.State != "claimed" {
		t.Fatal("incorrect claim")
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := NewService(db.DB).Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease)
			if e != nil || out == nil || out.ID != id || out.Revision != 2 {
				t.Error("claim retry changed authority")
			}
		}()
	}
	wg.Wait()
	in = f.transition(t, in, "executing", "")
	// Simulated handler/service crash: a fresh service sees executing, never a
	// newly claimable request. No transport is permitted to treat this as spawn.
	recovered, e := NewService(db.DB).Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease)
	if e != nil || recovered.State != "executing" || recovered.ID != in.ID {
		t.Fatal("crash replay lost executing state")
	}
	prior := in
	in = f.transition(t, in, "completed", "")
	again := f.transition(t, prior, "completed", "")
	if again.Revision != in.Revision {
		t.Fatal("completion not idempotent")
	}
	if _, e = f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: prior.Revision, State: "failed", Reason: "failed"}); !errors.Is(e, ErrConflict) {
		t.Fatal("terminal outcome changed")
	}
	events, e := f.s.Events(ctx, f.human, f.project, in.ID)
	if e != nil || len(events) != 4 {
		t.Fatalf("events=%d error=%v", len(events), e)
	}
	for i, state := range []string{"requested", "claimed", "executing", "completed"} {
		if events[i].State != state || events[i].Revision != int64(i+1) {
			t.Fatal("audit sequence mismatch")
		}
	}
	if _, e = db.DB.Exec(`UPDATE lifecycle_intent_events SET reason='failed' WHERE intent_id=?`, in.ID); e == nil {
		t.Fatal("audit mutable")
	}
	if _, e = db.DB.Exec(`UPDATE lifecycle_intents SET state='failed',revision=revision+1 WHERE id=?`, in.ID); e == nil {
		t.Fatal("terminal mutable")
	}
}
func TestLifecycleAuthorizationRevocationAndExpiry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := f.request("repair")
	in := f.submit(t, r)
	if _, _, e := f.s.Submit(ctx, f.reporter, f.project, r); e != ErrUnavailable {
		t.Fatal("API key submitted browser intent")
	}
	if _, e := f.s.Claim(ctx, f.human, f.project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("browser claimed")
	}
	for _, id := range []string{f.runtime.ID, uuid.NewString()} {
		if _, e := f.s.Claim(ctx, f.reporter, f.project, id, "invalid"); e != ErrUnavailable {
			t.Fatal("runtime oracle")
		}
	}
	otherKey := f.reporter.APIKeyID() + 100
	other, _ := auth.NewAPIKeyPrincipal(otherKey, f.user, auth.ParseScopes("*"))
	if _, e := f.s.Claim(ctx, other, f.project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("wrong principal claimed")
	}
	if _, e := f.s.Claim(ctx, f.reporter, f.project+999, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("wrong project claimed")
	}
	// A different currently valid API key owned by the same administrator
	// still cannot assume the reporter's exact credential binding.
	extra, e := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'other-fixture','other-noncredential','fixture','*')`, f.user)
	if e != nil {
		t.Fatal(e)
	}
	extraID, _ := extra.LastInsertId()
	validOther, _ := auth.NewAPIKeyPrincipal(extraID, f.user, auth.ParseScopes("*"))
	if _, e = f.s.Claim(ctx, validOther, f.project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("foreign valid key assumed reporter ownership")
	}
	wrongProof := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	if _, e = f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, wrongProof); e != ErrUnavailable {
		t.Fatal("different valid lease assumed generation ownership")
	}
	in = f.claim(t)
	_, e = f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{RuntimeID: f.runtime.ID, RuntimeGeneration: uuid.NewString(), ExpectedRevision: 2, State: "executing"})
	if e != ErrUnavailable {
		t.Fatal("wrong generation executed")
	}
	_, e = db.DB.Exec(`DELETE FROM sessions WHERE credential_id=?`, f.human.SessionCredentialID())
	if e != nil {
		t.Fatal(e)
	}
	if claimed, e := f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease); e != nil || claimed != nil {
		t.Fatal("revoked human claim survived")
	}
	var state, reason string
	if db.DB.QueryRow(`SELECT state,reason FROM lifecycle_intents WHERE id=?`, in.ID).Scan(&state, &reason) != nil || state != "failed" || reason != "authority_revoked" {
		t.Fatal("revocation not durably recorded")
	}
	// Live reporters are removed from browser discovery on scope revocation.
	if _, e = db.DB.Exec(`UPDATE api_keys SET scopes='projects:write' WHERE id=?`, f.reporter.APIKeyID()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("revoked scope accepted")
	}
}
func TestLifecycleCancelAndClaimCrashExpiry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	in := f.submit(t, f.request("repair"))
	in = f.claim(t)
	out, e := f.s.Cancel(ctx, f.human, f.project, in.ID, in.Revision)
	if e != nil || out.State != "cancelled" {
		t.Fatal("cancel failed")
	}
	retry, e := f.s.Cancel(ctx, f.human, f.project, in.ID, in.Revision)
	if e != nil || retry.Revision != out.Revision {
		t.Fatal("cancel retry changed")
	}
	if _, e = f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: in.Revision, State: "executing"}); e != ErrConflict {
		t.Fatal("cancel raced into execution")
	}
	r := f.request("repair")
	r.TTLSeconds = 30
	in = f.submit(t, r)
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	if _, e = f.s.Cancel(ctx, f.human, f.project, in.ID, in.Revision); e != ErrConflict {
		t.Fatal("executing work pretended cancelled")
	}
	f.now = f.now.Add(31 * time.Second)
	out, e = f.s.Get(ctx, f.human, f.project, in.ID)
	if e != nil || out.State != "expired" || out.Reason != "outcome_unknown" {
		t.Fatal("ambiguous crash expiry not preserved")
	}
	if _, e = f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease); e != nil {
		t.Fatal(e)
	}
	f.now = f.now.Add(120 * time.Second)
	if _, e = f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, f.registration); e != ErrUnavailable {
		t.Fatal("expired daemon generation revived")
	}
	fresh := f.registration
	fresh.Generation = uuid.NewString()
	if _, e = f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, fresh); e != nil {
		t.Fatal("fresh explicit generation refused")
	}
	if _, e = f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("old daemon adopted new lease")
	}
}
func (f *fixture) managed(t *testing.T) models.HarnessSession {
	t.Helper()
	s, _, e := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{ProjectID: f.project, AgentName: "worker", Harness: "codex", Host: f.runtime.MachineID, SessionRef: uuid.NewString(), WorkerLease: testLease, ManagementMode: "managed", Role: "worker", SteerMode: "none", Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true}, Workspace: &models.HarnessWorkspaceProvenance{CanonicalPath: "/fixture/workspace", Kind: "directory", Mode: "exclusive", Identity: f.runtime.Workspaces[0].Identity}, DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", AccountLabel: f.runtime.AccountLabel})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestLifecycleStartRequiresOwnedNewGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	in := f.submit(t, f.request("start"))
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	if _, e := f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: in.Revision, State: "completed", Reason: "applied"}); e != ErrUnavailable {
		t.Fatal("unproved success accepted")
	}
	s := f.managed(t)
	if e := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, "invalid", SessionRegistration{SessionID: s.ID, Generation: in.NewGeneration}); e != ErrUnavailable {
		t.Fatal("wrong worker lease accepted")
	}
	if e := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: s.ID, Generation: in.NewGeneration}); e != nil {
		t.Fatal(e)
	}
	advertised, e := f.s.Runtimes(ctx, f.human, f.project)
	if e != nil || len(advertised) != 1 || len(advertised[0].Sessions) != 1 || advertised[0].Sessions[0].Generation != in.NewGeneration {
		t.Fatal("discovery omitted owned generation proof")
	}
	in = f.transition(t, in, "completed", s.ID)
	if in.ResultSessionID != s.ID {
		t.Fatal("completion missing public session")
	}
	if e := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: s.ID, Generation: uuid.NewString()}); e != ErrUnavailable {
		t.Fatal("generation rebound")
	}
}

func TestLifecycleBindingCASRestartAndNegativeTargets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	current := f.managed(t)
	generation := uuid.NewString()
	if e := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: current.ID, Generation: generation}); e != nil {
		t.Fatal(e)
	}
	hs := managedharness.NewService(db.DB)
	var e error
	current, e = hs.HeartbeatWithActivity(ctx, current.ID, "yielded", managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_completed"})
	if e != nil {
		t.Fatal(e)
	}
	f.now = time.Now().UTC()
	res, e := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,title,type) VALUES(?,1,'Fixture ticket','ticket')`, f.project)
	if e != nil {
		t.Fatal(e)
	}
	ticket, _ := res.LastInsertId()
	req := f.request("reassign")
	req.SessionID = current.ID
	req.SessionGeneration = generation
	req.ExpectedRevision = current.Revision
	req.TicketID = &ticket
	req.WorkShape = "ship"
	for _, mutate := range []func(*Request){
		func(r *Request) { r.ExpectedRevision++ }, func(r *Request) { r.SessionGeneration = uuid.NewString() }, func(r *Request) { r.AccountLabel = "console" }, func(r *Request) { r.DispatchProfileVersion = "999" }, func(r *Request) { r.WorkspaceHandle = uuid.NewString() }, func(r *Request) { r.ParentSessionID = &r.SessionID }, func(r *Request) { v := int64(99999); r.TicketID = &v }, func(r *Request) { v := uuid.NewString(); r.ParentSessionID = &v },
	} {
		bad := req
		bad.RequestKey = uuid.NewString()
		mutate(&bad)
		if _, _, e = f.s.Submit(ctx, f.human, f.project, bad); e != ErrUnavailable {
			t.Fatalf("invalid target: %v", e)
		}
	}
	foreign, e := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Other fixture','OTH')`)
	if e != nil {
		t.Fatal(e)
	}
	otherProject, _ := foreign.LastInsertId()
	if _, e = db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'parent')`, otherProject); e != nil {
		t.Fatal(e)
	}
	parent, _, e := hs.Register(ctx, managedharness.RegisterInput{ProjectID: otherProject, AgentName: "parent", Harness: "codex", Host: "other-machine", SessionRef: uuid.NewString(), WorkerLease: testLease, ManagementMode: "managed", Role: "worker", SteerMode: "none", Capabilities: models.HarnessCapabilities{Status: true}})
	if e != nil {
		t.Fatal(e)
	}
	bad := req
	bad.RequestKey = uuid.NewString()
	bad.ParentSessionID = &parent.ID
	if _, _, e = f.s.Submit(ctx, f.human, f.project, bad); e != ErrUnavailable {
		t.Fatal("cross-project parent accepted")
	}
	in := f.submit(t, req)
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	prior := in
	in = f.transition(t, in, "completed", current.ID)
	f.transition(t, prior, "completed", current.ID)
	bound, e := hs.Get(ctx, f.project, current.ID)
	if e != nil || bound.TicketID == nil || *bound.TicketID != ticket || bound.Revision != current.Revision+1 {
		t.Fatal("binding CAS failed")
	}
	var assignments int
	if db.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=? AND operation='binding_changed'`, current.ID).Scan(&assignments) != nil || assignments != 1 {
		t.Fatal("duplicate binding event")
	}
	// A terminal session is the only restart target; active/unknown cannot be
	// relabeled dead. The reserved generation must be distinct from the old one.
	restart := req
	restart.Operation = "restart"
	restart.RequestKey = uuid.NewString()
	restart.ExpectedRevision = bound.Revision
	if _, _, e = f.s.Submit(ctx, f.human, f.project, restart); e != ErrUnavailable {
		t.Fatal("restarted live generation")
	}
	current, e = hs.StopWithReason(ctx, current.ID, "stopped")
	if e != nil {
		t.Fatal(e)
	}
	restart.ExpectedRevision = current.Revision
	in = f.submit(t, restart)
	if in.NewGeneration == generation || !validID(in.NewGeneration) {
		t.Fatal("restart did not reserve fresh generation")
	}
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	// Register a new generation with exactly the desired binding.
	created, _, e := hs.Register(ctx, managedharness.RegisterInput{ProjectID: f.project, AgentName: "worker", Harness: "codex", Host: f.runtime.MachineID, SessionRef: uuid.NewString(), WorkerLease: testLease, ManagementMode: "managed", Role: "worker", TicketID: &ticket, WorkShape: "ship", SteerMode: "none", Capabilities: models.HarnessCapabilities{Status: true}, Workspace: &models.HarnessWorkspaceProvenance{CanonicalPath: "/fixture/workspace", Kind: "directory", Mode: "exclusive", Identity: f.runtime.Workspaces[0].Identity}, DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", AccountLabel: f.runtime.AccountLabel})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: created.ID, Generation: in.NewGeneration}); e != nil {
		t.Fatal(e)
	}
	in = f.transition(t, in, "completed", created.ID)
	if in.ResultSessionID == current.ID {
		t.Fatal("restart reused terminal generation")
	}
}
func TestLifecycleDemotionAndAdvertisementLimits(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	in := f.submit(t, f.request("repair"))
	_ = in
	if _, e := db.DB.Exec(`UPDATE users SET role_key='admin',is_super_admin=0 WHERE id=?`, f.user); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.s.Submit(ctx, f.human, f.project, f.request("start")); e != ErrUnavailable {
		t.Fatal("lower role escalated through reporter")
	}
	if _, e := f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("demoted reporter claimed")
	}
	if _, e := db.DB.Exec(`UPDATE users SET role_key='super_admin',is_super_admin=1 WHERE id=?`, f.user); e != nil {
		t.Fatal(e)
	}
	changed := f.registration
	changed.AccountLabel = "console"
	if _, e := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, changed); e != ErrUnavailable {
		t.Fatal("immutable advertisement changed")
	}
	changed = f.registration
	changed.Generation = uuid.NewString()
	changed.Host = "different-fixture"
	changed.Workspaces = make([]Workspace, 17)
	if _, e := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, changed); e != ErrInvalid {
		t.Fatal("unbounded advertisement")
	}
	for i := 1; i < 32; i++ {
		f.submit(t, f.request("repair"))
	}
	if _, _, e := f.s.Submit(ctx, f.human, f.project, f.request("repair")); e != ErrConflict {
		t.Fatal("unbounded pending intents")
	}
}

func TestLifecycleQueuedStartsCannotSpawnAgainAfterCompletedGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	first := f.submit(t, f.request("start"))
	second := f.submit(t, f.request("start"))
	in := f.claim(t)
	in = f.transition(t, in, "executing", "")
	current := f.managed(t)
	if e := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: current.ID, Generation: in.NewGeneration}); e != nil {
		t.Fatal(e)
	}
	f.transition(t, in, "completed", current.ID)
	if claimed, e := f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease); e != nil || claimed != nil {
		t.Fatal("queued duplicate start was permitted to spawn")
	}
	pendingID := second.ID
	if pendingID == in.ID {
		pendingID = first.ID
	}
	closed, e := f.s.Get(ctx, f.human, f.project, pendingID)
	if e != nil || closed.State != "failed" {
		t.Fatal("queued conflicting start not durably refused")
	}
	// Deleting a key must be possible without deleting its audit identity.
	if _, e = db.DB.Exec(`DELETE FROM api_keys WHERE id=?`, f.reporter.APIKeyID()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Claim(ctx, f.reporter, f.project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("deleted reporter still authorized")
	}
	runtimes, e := f.s.Runtimes(ctx, f.human, f.project)
	if e != nil || len(runtimes) != 0 {
		t.Fatal("revoked reporter advertised as available")
	}
}

func TestLifecycleExecutingRetryCannotExtendIntentDeadline(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := f.request("repair")
	r.TTLSeconds = 30
	f.submit(t, r)
	claimed := f.claim(t)
	executing := f.transition(t, claimed, "executing", "")
	f.now = f.now.Add(31 * time.Second)
	_, e := f.s.Transition(ctx, f.reporter, f.project, executing.ID, testLease, Transition{RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: claimed.Revision, State: "executing"})
	if e != ErrConflict {
		t.Fatal("executing replay renewed expired authority")
	}
	result, e := f.s.Get(ctx, f.human, f.project, executing.ID)
	if e != nil || result.State != "expired" || result.Reason != "outcome_unknown" {
		t.Fatal("expired retry outcome lost")
	}
}

func TestLifecycleSameDaemonCanAdvertiseSeparateProjectHandles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, e := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Second project','LCS')`)
	if e != nil {
		t.Fatal(e)
	}
	project, _ := res.LastInsertId()
	second, e := f.s.RegisterRuntime(ctx, f.reporter, project, testLease, f.registration)
	if e != nil || second.ID == f.runtime.ID || second.Generation != f.runtime.Generation {
		t.Fatal("one daemon could not bind projects independently")
	}
	if _, e = f.s.Claim(ctx, f.reporter, project, f.runtime.ID, testLease); e != ErrUnavailable {
		t.Fatal("foreign-project runtime handle accepted")
	}
}

func TestLifecycleClaimsReserveBeforeAnyDaemonSpawns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	first := f.submit(t, f.request("start"))
	registration := f.registration
	registration.Generation = uuid.NewString()
	registration.Host = "second-machine"
	secondRuntime, e := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, registration)
	if e != nil {
		t.Fatal(e)
	}
	req := f.request("start")
	req.RuntimeID = secondRuntime.ID
	req.RuntimeGeneration = secondRuntime.Generation
	second := f.submit(t, req)
	// First claim creates the reservation even though no managed session exists.
	claimed := f.claim(t)
	if claimed.ID != first.ID {
		t.Fatal("unexpected first claimant")
	}
	secondClaim, e := f.s.Claim(ctx, f.reporter, f.project, secondRuntime.ID, testLease)
	if e != nil || secondClaim != nil {
		t.Fatal("two daemons authorized concurrent spawn")
	}
	result, e := f.s.Get(ctx, f.human, f.project, second.ID)
	if e != nil || result.State != "failed" {
		t.Fatal("reservation conflict outcome not durable")
	}
}

func TestLifecycleExpiredClaimDoesNotPoisonExplicitFreshRequest(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := f.request("start")
	r.TTLSeconds = 30
	old := f.submit(t, r)
	f.claim(t)
	f.now = f.now.Add(31 * time.Second)
	fresh := f.submit(t, f.request("start"))
	claimed := f.claim(t)
	if claimed.ID != fresh.ID {
		t.Fatal("expired claim blocked explicit fresh request")
	}
	old, e := f.s.Get(ctx, f.human, f.project, old.ID)
	if e != nil || old.State != "expired" || old.Reason != "outcome_unknown" {
		t.Fatal("expiry lost ambiguous original outcome")
	}
}

func TestListOwnedRuntimeSessionsSQLKeepsOwnershipFence(t *testing.T) {
	if !strings.Contains(listOwnedRuntimeSessionsSQL, lifecyclefence.OwnershipSQLRuntimeS) {
		t.Fatal("projection query lost ownership fence")
	}
	if !strings.Contains(listOwnedRuntimeSessionsSQL, "runtime.registration_json") {
		t.Fatal("missing runtime alias")
	}
	if !strings.Contains(listOwnedRuntimeSessionsSQL, "s.account_label") {
		t.Fatal("missing session alias")
	}
}
