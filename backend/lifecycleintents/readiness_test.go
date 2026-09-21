package lifecycleintents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
)

const fixtureBaseline = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func (f *fixture) readinessRequest() Request {
	return Request{
		RequestKey: uuid.NewString(), Operation: "readiness", RuntimeID: f.runtime.ID,
		RuntimeGeneration: f.runtime.Generation, AccountLabel: f.runtime.AccountLabel, TTLSeconds: 300,
		WorkspaceHandle: f.runtime.Workspaces[0].Handle, DispatchProfileID: "codex-sol-high",
		DispatchProfileVersion: "2", BaselineDigest: fixtureBaseline,
	}
}

func (f *fixture) readinessReport() *ReadinessReport {
	report := &ReadinessReport{
		ContractVersion: ReadinessContractVersion, Status: "ready", TTLSeconds: ReadinessTTLSeconds,
		ObservedAt: f.now.Format(time.RFC3339Nano), HostKind: "macos-home-manager", NextAction: "none",
		WorkspaceIdentity: f.runtime.Workspaces[0].Identity, BaselineDigest: fixtureBaseline,
	}
	for _, id := range RequiredReadinessChecks {
		report.Checks = append(report.Checks, ReadinessCheck{ID: id, Status: "pass", Reason: "verified"})
	}
	return report
}

// complete drives one readiness intent through the real protocol and returns
// the transition error, so each case asserts what the authority accepted.
func (f *fixture) completeReadiness(t *testing.T, report *ReadinessReport) error {
	t.Helper()
	ctx := context.Background()
	in := f.submit(t, f.readinessRequest())
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	_, err := f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{
		RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: in.Revision,
		State: "completed", Reason: "applied", Readiness: report,
	})
	return err
}

func (f *fixture) target() ReadinessTarget {
	return ReadinessTarget{
		RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, AccountLabel: f.runtime.AccountLabel,
		ProfileID: "codex-sol-high", ProfileVersion: "2", WorkspaceHandle: f.runtime.Workspaces[0].Handle,
		BaselineDigest: fixtureBaseline,
	}
}

func (f *fixture) current(t *testing.T, now time.Time) (ReadinessObservation, error) {
	t.Helper()
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return CurrentReadinessTx(context.Background(), tx, f.project, f.target(), now)
}

func TestReadinessObservationIsStoredBoundAndClamped(t *testing.T) {
	f := setup(t)
	if err := f.completeReadiness(t, f.readinessReport()); err != nil {
		t.Fatalf("readiness completion: %v", err)
	}
	observation, err := f.current(t, time.Now())
	if err != nil {
		t.Fatalf("stored observation: %v", err)
	}
	if observation.Status != "ready" || observation.WorkspaceIdentity != f.runtime.Workspaces[0].Identity {
		t.Fatalf("observation=%+v", observation)
	}
	if len(observation.Checks) != len(RequiredReadinessChecks) {
		t.Fatalf("checks=%+v", observation.Checks)
	}
	expires, err := time.Parse(time.RFC3339Nano, observation.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	runtimeExpiry, err := time.Parse(time.RFC3339Nano, f.runtime.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	// The contract allows 300s; the runtime registration lives for 120s. The
	// stored deadline may never outlive the ownership that produced it.
	if expires.After(runtimeExpiry) {
		t.Fatalf("freshness %s outlives runtime ownership %s", expires, runtimeExpiry)
	}
	if _, err := f.current(t, expires.Add(time.Second)); err == nil {
		t.Fatal("an expired observation was still returned as current")
	}
	// The observation answers exactly one baseline.
	other := f.target()
	other.BaselineDigest = "sha256:" + strings.Repeat("2", 64)
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := CurrentReadinessTx(context.Background(), tx, f.project, other, time.Now()); err == nil {
		t.Fatal("an observation of one baseline answered another")
	}
}

func TestReadinessCannotClaimAnOccupiedWorkspaceAcrossDaemons(t *testing.T) {
	f := setup(t)
	f.managed(t)
	if err := f.completeReadiness(t, f.readinessReport()); err != nil {
		t.Fatalf("record occupied readiness: %v", err)
	}
	assertOccupiedReadiness(t, f)
}

func TestReadinessObservesAnInFlightStartOnTheSameMachine(t *testing.T) {
	f := setup(t)
	f.submit(t, f.request("start"))
	f.claim(t)
	// Claim deliberately returns the older owned Start until it completes.
	// Exercise the same transactional recorder directly to simulate a second
	// runtime's readiness completion while that Start remains in flight.
	in := f.submit(t, f.readinessRequest())
	tx, err := db.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = f.s.recordReadinessTx(context.Background(), tx, in, f.runtime, f.readinessReport()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertOccupiedReadiness(t, f)
}

func TestReadinessTransitionReturnsTheFinalBoundReceiptAndReplaysIt(t *testing.T) {
	f := setup(t)
	f.managed(t)
	ctx := context.Background()
	in := f.submit(t, f.readinessRequest())
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	completed, err := f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{
		RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: in.Revision,
		State: "completed", Reason: "applied", Readiness: f.readinessReport(),
	})
	if err != nil || completed.AcceptedReadiness == nil {
		t.Fatalf("accepted receipt error=%v receipt=%+v", err, completed.AcceptedReadiness)
	}
	receipt := *completed.AcceptedReadiness
	if err = ValidateReadinessReceipt(receipt, time.Now()); err != nil || receipt.Status != "needs_setup" || receipt.NextAction != "select_declared_workspace" ||
		receipt.RuntimeID != f.runtime.ID || receipt.RuntimeGeneration != f.runtime.Generation || receipt.WorkspaceHandle != f.runtime.Workspaces[0].Handle || receipt.BaselineDigest != fixtureBaseline {
		t.Fatalf("final server receipt=%+v validation=%v", receipt, err)
	}
	f.now = f.now.Add(RuntimeTTLSeconds*time.Second + time.Second)
	replayed, err := f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{
		RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: completed.Revision - 1,
		State: "completed", Reason: "applied", Readiness: f.readinessReport(),
	})
	if err != nil || replayed.AcceptedReadiness == nil || replayed.AcceptedReadiness.Status != receipt.Status || replayed.AcceptedReadiness.IntentID != in.ID {
		t.Fatalf("terminal replay did not return the same accepted receipt: out=%+v err=%v", replayed.AcceptedReadiness, err)
	}
}

func assertOccupiedReadiness(t *testing.T, f *fixture) {
	t.Helper()
	observation, err := f.current(t, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != "needs_setup" || observation.NextAction != "select_declared_workspace" {
		t.Fatalf("occupied workspace was ready: %+v", observation)
	}
	for _, check := range observation.Checks {
		if check.ID == "workspace_isolation" && check.Status == "fail" && check.Reason == "workspace_occupied" && check.Digest == "" {
			return
		}
	}
	t.Fatalf("workspace conflict was not recorded without identity leakage: %+v", observation.Checks)
}

func TestReadinessReportsThatDoNotMatchTheAuthorizedTargetAreRefused(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fixture, *ReadinessReport)
	}{
		{"foreign workspace identity", func(f *fixture, r *ReadinessReport) {
			r.WorkspaceIdentity = strings.Repeat("9", 64)
		}},
		{"foreign baseline", func(f *fixture, r *ReadinessReport) {
			r.BaselineDigest = "sha256:" + strings.Repeat("3", 64)
		}},
		{"unnamed account claimed", func(f *fixture, r *ReadinessReport) { r.AccountKey = "someone-else" }},
		{"unknown contract", func(f *fixture, r *ReadinessReport) { r.ContractVersion = "inspr.readiness.v9" }},
		{"ready with a failing required check", func(f *fixture, r *ReadinessReport) {
			r.Checks[0].Status = "fail"
			r.Checks[0].Reason = "host_kind_mismatch"
		}},
		{"required check omitted", func(f *fixture, r *ReadinessReport) { r.Checks = r.Checks[1:] }},
		{"observation from the future", func(f *fixture, r *ReadinessReport) {
			r.ObservedAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
		}},
		{"observation older than the intent", func(f *fixture, r *ReadinessReport) {
			r.ObservedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
		}},
		{"ttl beyond the contract", func(f *fixture, r *ReadinessReport) { r.TTLSeconds = 3600 }},
		{"unknown host kind", func(f *fixture, r *ReadinessReport) { r.HostKind = "some-laptop" }},
		{"free-text reason", func(f *fixture, r *ReadinessReport) {
			r.Checks[0].Reason = "/Users/someone/workspace is missing"
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := setup(t)
			report := f.readinessReport()
			testCase.mutate(f, report)
			if err := f.completeReadiness(t, report); err == nil {
				t.Fatal("the authority accepted a report it could not bind")
			}
			var stored int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM lifecycle_readiness_observations`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != 0 {
				t.Fatalf("refused report still stored %d observations", stored)
			}
		})
	}
}

func TestReadinessCannotCompleteWithoutAReport(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	in := f.submit(t, f.readinessRequest())
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	if _, err := f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{
		RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: in.Revision,
		State: "completed", Reason: "applied",
	}); err == nil {
		t.Fatal("a readiness intent completed without an observation")
	}
}

func TestReadinessReportRejectedOnANonReadinessIntent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	in := f.submit(t, f.request("start"))
	in = f.claim(t)
	in = f.transition(t, in, "executing", "")
	if _, err := f.s.Transition(ctx, f.reporter, f.project, in.ID, testLease, Transition{
		RuntimeID: f.runtime.ID, RuntimeGeneration: f.runtime.Generation, ExpectedRevision: in.Revision,
		State: "completed", Reason: "applied", Readiness: f.readinessReport(),
	}); err == nil {
		t.Fatal("a start intent smuggled in a readiness observation")
	}
}

func TestReadinessRequestShapeIsClosed(t *testing.T) {
	f := setup(t)
	for _, mutate := range []func(*Request){
		func(r *Request) { r.BaselineDigest = "" },
		func(r *Request) { r.BaselineDigest = "not-a-digest" },
		func(r *Request) { r.AgentName = "worker" },
		func(r *Request) { r.Role = "worker" },
		func(r *Request) { ticket := int64(1); r.TicketID = &ticket },
		func(r *Request) { r.SessionID = uuid.NewString() },
		func(r *Request) { r.WorkspaceHandle = "" },
		func(r *Request) { r.DispatchProfileID = "" },
	} {
		request := f.readinessRequest()
		mutate(&request)
		if _, _, err := f.s.Submit(context.Background(), f.human, f.project, request); err == nil {
			t.Fatalf("readiness request accepted an out-of-shape field: %+v", request)
		}
	}
	// A baseline digest is meaningless on any other operation.
	start := f.request("start")
	start.BaselineDigest = fixtureBaseline
	if _, _, err := f.s.Submit(context.Background(), f.human, f.project, start); err == nil {
		t.Fatal("start accepted a baseline digest")
	}
}
