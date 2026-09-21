// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package lifecycleclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestConversationHTTPUsesExactRunnerWireContract(t *testing.T) {
	project := int64(1027)
	runtimeID, generation, callID, execution := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Second).Format(time.RFC3339)
	claim := testConversationClaim(callID, execution, generation, deadline)
	lease, err := NewProof()
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer runner-key" || request.Header.Get("X-Paimos-Runtime-Lease") != lease {
			t.Error("runner authority headers missing")
		}
		var response any
		switch requests {
		case 1:
			if request.Method != http.MethodPost || request.URL.RequestURI() != "/api/projects/1027/runtimes/"+runtimeID+"/conversation/v1/claim" {
				t.Errorf("claim request=%s %s", request.Method, request.URL.RequestURI())
			}
			var body struct {
				SchemaVersion int    `json:"schema_version"`
				Generation    string `json:"generation"`
			}
			decodeConversationTestBody(t, request, &body)
			if body.SchemaVersion != 1 || body.Generation != generation {
				t.Errorf("claim body=%+v", body)
			}
			response = map[string]any{"schema_version": 1, "claim": claim}
		case 2:
			if request.Method != http.MethodGet || request.URL.Path != "/api/projects/1027/runtimes/"+runtimeID+"/conversation/v1/calls/"+callID+"/control" || request.URL.Query().Get("execution_generation") != execution {
				t.Errorf("control request=%s %s", request.Method, request.URL.RequestURI())
			}
			response = ConversationControl{SchemaVersion: 1, CallID: callID, ExecutionGeneration: execution, Continue: true, DeadlineAt: deadline}
		case 3:
			if request.Method != http.MethodPost || request.URL.Path != "/api/projects/1027/runtimes/"+runtimeID+"/conversation/v1/calls/"+callID+"/events" {
				t.Errorf("event request=%s %s", request.Method, request.URL.RequestURI())
			}
			var body struct {
				SchemaVersion       int               `json:"schema_version"`
				Generation          string            `json:"generation"`
				ExecutionGeneration string            `json:"execution_generation"`
				Event               ConversationEvent `json:"event"`
			}
			decodeConversationTestBody(t, request, &body)
			if body.SchemaVersion != 1 || body.Generation != generation || body.ExecutionGeneration != execution || body.Event.Sequence != 1 || body.Event.Kind != "started" {
				t.Errorf("event body=%+v", body)
			}
			call := claim.Call
			call.State, call.LastSequence = "running", 1
			response = call
		default:
			t.Errorf("unexpected request %d", requests)
		}
		raw, _ := json.Marshal(response)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})
	client, err := NewHTTP("http://127.0.0.1", project, lease, func() (string, error) { return "runner-key", nil })
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = transport
	got, err := client.ClaimConversation(context.Background(), runtimeID, generation)
	if err != nil || got == nil || got.ExecutionGeneration != execution {
		t.Fatalf("claim=%+v err=%v", got, err)
	}
	if _, err = client.ConversationControl(context.Background(), runtimeID, callID, execution); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ReportConversationEvent(context.Background(), runtimeID, generation, callID, execution, ConversationEvent{Sequence: 1, Kind: "started", ThreadID: "thread-owned", TurnID: "turn-owned"}); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func decodeConversationTestBody(t *testing.T, request *http.Request, output any) {
	t.Helper()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil {
		t.Fatal("invalid request body")
	}
}

func TestConversationRunnerClaimsOnceReportsExactDigestAndRetainsPrivateAnswer(t *testing.T) {
	authority, launcher, runtime := testConversationRuntime(t, "answer 🌍")
	launcher.process.beforeWait = func() bool {
		authority.mu.Lock()
		defer authority.mu.Unlock()
		return len(authority.events) == 1 && authority.events[0].Kind == "started"
	}
	runner, err := NewConversationRunner(conversationJournalDir(t), runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	if authority.claims != 1 || launcher.launches != 1 {
		t.Fatalf("claims=%d launches=%d", authority.claims, launcher.launches)
	}
	if !launcher.process.observedStarted {
		t.Fatal("process wait began before the started event was accepted")
	}
	if len(authority.events) != 3 || authority.events[0].Kind != "started" || authority.events[1].Kind != "assistant_delta" || authority.events[1].Text != "answer 🌍" || authority.events[2].Kind != "completed" {
		t.Fatalf("events=%+v", authority.events)
	}
	digest := sha256.Sum256([]byte("answer 🌍"))
	if authority.events[2].OutputSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("digest=%s", authority.events[2].OutputSHA256)
	}
	for index, event := range authority.events {
		if event.Sequence != index+1 || event.ThreadID != "thread-owned" || event.TurnID != "turn-owned" {
			t.Fatalf("event[%d]=%+v", index, event)
		}
	}
	for _, saved := range runner.journal.Snapshot() {
		if saved.Phase != "terminal" || len(saved.Pending) != 0 || !saved.RetainUntil.After(time.Now()) {
			t.Fatalf("journal=%+v", saved)
		}
	}
	if info, err := os.Stat(runner.journal.CheckpointPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private checkpoint mode=%v err=%v", info, err)
	}
}

func TestConversationRunnerStartedReportFailureStopsAndNeverRespawns(t *testing.T) {
	authority, launcher, runtime := testConversationRuntime(t, "private answer")
	authority.failReports = true
	directory := conversationJournalDir(t)
	runner, err := NewConversationRunner(directory, runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(context.Background(), runtime); !errors.Is(err, ErrTransport) {
		t.Fatalf("first step err=%v", err)
	}
	if launcher.launches != 1 {
		t.Fatalf("launches=%d", launcher.launches)
	}
	authority.failReports = false
	authority.returnNilAfterFirst = true
	restarted, err := NewConversationRunner(directory, runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Step(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	if launcher.launches != 1 || len(authority.events) != 2 || authority.events[0].Kind != "started" ||
		authority.events[1].Kind != "failed" || authority.events[1].ErrorCode != "authority_revoked" {
		t.Fatalf("launches=%d events=%+v", launcher.launches, authority.events)
	}
}

func TestConversationRunnerRecoveredLaunchingGenerationIsUncertain(t *testing.T) {
	authority, launcher, runtime := testConversationRuntime(t, "never respawn")
	directory := conversationJournalDir(t)
	runner, err := NewConversationRunner(directory, runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	claim := *authority.claim
	record := conversationRecord{CallID: claim.Call.CallID, Digest: conversationClaimDigest(claim), Phase: "launching", Claim: claim, RetainUntil: time.Now().Add(time.Minute)}
	if err = runner.journal.Put(record); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewConversationRunner(directory, runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Step(context.Background(), runtime); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err=%v", err)
	}
	if launcher.launches != 0 || authority.claims != 0 {
		t.Fatalf("uncertain generation launched=%d claimed=%d", launcher.launches, authority.claims)
	}
}

func TestConversationRunnerCancellationStopsBeforeCancelledEvent(t *testing.T) {
	authority, launcher, runtime := testConversationRuntime(t, "")
	launcher.process.block = make(chan struct{})
	authority.cancelAfterFirstControl = true
	runner, err := NewConversationRunner(conversationJournalDir(t), runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	if !launcher.process.stopped || len(authority.events) != 2 || authority.events[1].Kind != "cancelled" || authority.events[1].ErrorCode != "cancelled" {
		t.Fatalf("stopped=%t events=%+v", launcher.process.stopped, authority.events)
	}
}

func TestConversationFailureVocabularyNormalizesToWireContract(t *testing.T) {
	tests := []struct {
		failure string
		want    string
	}{
		{failure: "protocol_error", want: "malformed_completion"},
		{failure: "output_bound", want: "output_limit"},
		{failure: "event_bound", want: "event_limit"},
		{failure: "transport_ended", want: "execution_failed"},
		{failure: "turn_failed", want: "execution_failed"},
		{failure: "ownership_lost", want: "authority_revoked"},
		{failure: "preflight_failed", want: "runtime_unavailable"},
		{failure: "deadline_exceeded", want: "deadline_exceeded"},
		{failure: "private_native_detail", want: "execution_failed"},
	}
	for _, test := range tests {
		t.Run(test.failure, func(t *testing.T) {
			got := closedConversationFailure(test.failure)
			if got != test.want {
				t.Fatalf("normalized failure=%q want=%q", got, test.want)
			}
			if err := validateConversationEvent(ConversationEvent{Sequence: 1, Kind: "failed", ErrorCode: got}); err != nil {
				t.Fatalf("normalized failure rejected locally: %v", err)
			}
		})
	}
}

func TestConversationRunnerLaunchFailureReportsClosedRuntimeFailure(t *testing.T) {
	authority, _, runtime := testConversationRuntime(t, "")
	runner, err := NewConversationRunner(conversationJournalDir(t), runtime.Generation, authority, failingConversationLauncher{})
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	if len(authority.events) != 1 || authority.events[0].Kind != "failed" || authority.events[0].ErrorCode != "runtime_unavailable" ||
		authority.events[0].ThreadID != "" || authority.events[0].TurnID != "" {
		t.Fatalf("preflight terminal=%+v", authority.events)
	}
}

func TestConversationRunnerCancelObservationWinsCompletedCollectorRace(t *testing.T) {
	authority, _, runtime := testConversationRuntime(t, "")
	process := &completedBeforeStopConversationProcess{
		complete: make(chan struct{}), waitReturned: make(chan struct{}),
	}
	raceAuthority := &cancelOnThirdControlAuthority{
		fakeConversationAuthority: authority, complete: process.complete, waitReturned: process.waitReturned,
	}
	runner, err := NewConversationRunner(conversationJournalDir(t), runtime.Generation, raceAuthority, singleConversationLauncher{process: process})
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	process.mu.Lock()
	stopped := process.stopped
	process.mu.Unlock()
	if !stopped || len(authority.events) != 2 || authority.events[0].Kind != "started" ||
		authority.events[1].Kind != "cancelled" || authority.events[1].ErrorCode != "cancelled" {
		t.Fatalf("stopped=%t events=%+v", stopped, authority.events)
	}
}

func TestConversationRunnerServerCancelAfterFinalControlReplacesOnlyRejectedCompletion(t *testing.T) {
	authority, launcher, runtime := testConversationRuntime(t, "")
	raceAuthority := &cancelAfterFinalControlFakeAuthority{fakeConversationAuthority: authority}
	runner, err := NewConversationRunner(conversationJournalDir(t), runtime.Generation, raceAuthority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	if launcher.launches != 1 || len(authority.events) != 2 || authority.events[0].Kind != "started" ||
		authority.events[1].Kind != "cancelled" || authority.events[1].Sequence != 2 {
		t.Fatalf("launches=%d events=%+v", launcher.launches, authority.events)
	}
	for _, saved := range runner.journal.Snapshot() {
		if saved.Phase != "terminal" || len(saved.Pending) != 0 || saved.Sequence != 2 {
			t.Fatalf("journal=%+v", saved)
		}
	}
}

func TestConversationRunnerRejectsExpiredRuntimeLeaseWithoutClaim(t *testing.T) {
	authority, launcher, runtime := testConversationRuntime(t, "")
	runtime.ExpiresAt = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	runner, err := NewConversationRunner(conversationJournalDir(t), runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Step(context.Background(), runtime); !errors.Is(err, ErrOwnership) {
		t.Fatalf("err=%v", err)
	}
	if authority.claims != 0 || launcher.launches != 0 {
		t.Fatalf("claims=%d launches=%d", authority.claims, launcher.launches)
	}
}

func TestConversationRunnerExpiresPrivateTerminalRecord(t *testing.T) {
	authority, launcher, runtime := testConversationRuntime(t, "expired answer")
	runner, err := NewConversationRunner(conversationJournalDir(t), runtime.Generation, authority, launcher)
	if err != nil {
		t.Fatal(err)
	}
	claim := *authority.claim
	record := conversationRecord{CallID: claim.Call.CallID, Digest: conversationClaimDigest(claim), Phase: "terminal", Claim: claim, RetainUntil: time.Now().Add(-time.Second)}
	if err = runner.journal.Put(record); err != nil {
		t.Fatal(err)
	}
	authority.returnNilAfterFirst = true
	authority.claims = 1
	if err = runner.Step(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	if len(runner.journal.Snapshot()) != 0 || launcher.launches != 0 {
		t.Fatalf("expired record retained or launched")
	}
}

type fakeConversationAuthority struct {
	mu                      sync.Mutex
	claim                   *ConversationClaim
	claims                  int
	controls                int
	events                  []ConversationEvent
	failReports             bool
	cancelAfterFirstControl bool
	returnNilAfterFirst     bool
}

func (a *fakeConversationAuthority) ClaimConversation(context.Context, string, string) (*ConversationClaim, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claims++
	if a.returnNilAfterFirst && a.claims > 1 {
		return nil, nil
	}
	return a.claim, nil
}

func (a *fakeConversationAuthority) ConversationControl(context.Context, string, string, string) (ConversationControl, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.controls++
	cancel := a.cancelAfterFirstControl && a.controls > 1
	return ConversationControl{SchemaVersion: 1, CallID: a.claim.Call.CallID, ExecutionGeneration: a.claim.ExecutionGeneration, Continue: !cancel, CancelRequested: cancel, DeadlineAt: a.claim.Call.DeadlineAt}, nil
}

func (a *fakeConversationAuthority) ReportConversationEvent(_ context.Context, _, _, callID, _ string, event ConversationEvent) (ConversationCall, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failReports {
		return ConversationCall{}, ErrTransport
	}
	if len(a.events) >= event.Sequence {
		return a.callForEvent(event), nil
	}
	if event.Sequence != len(a.events)+1 {
		return ConversationCall{}, ErrOwnership
	}
	a.events = append(a.events, event)
	return a.callForEvent(event), nil
}

func (a *fakeConversationAuthority) callForEvent(event ConversationEvent) ConversationCall {
	call := a.claim.Call
	call.CallID, call.LastSequence = a.claim.Call.CallID, event.Sequence
	call.State = "running"
	if event.Kind == "completed" || event.Kind == "failed" || event.Kind == "cancelled" {
		call.State = event.Kind
	}
	return call
}

type fakeConversationLauncher struct {
	launches int
	process  *fakeConversationProcess
}

func (l *fakeConversationLauncher) LaunchConversation(context.Context, ConversationClaim) (ConversationProcess, error) {
	l.launches++
	return l.process, nil
}

type failingConversationLauncher struct{}

func (failingConversationLauncher) LaunchConversation(context.Context, ConversationClaim) (ConversationProcess, error) {
	return nil, ErrOwnership
}

type singleConversationLauncher struct {
	process ConversationProcess
}

func (l singleConversationLauncher) LaunchConversation(context.Context, ConversationClaim) (ConversationProcess, error) {
	return l.process, nil
}

type cancelOnThirdControlAuthority struct {
	*fakeConversationAuthority
	complete     chan struct{}
	waitReturned chan struct{}
}

type cancelAfterFinalControlFakeAuthority struct {
	*fakeConversationAuthority
	mu              sync.Mutex
	serverCancelled bool
}

func (a *cancelAfterFinalControlFakeAuthority) ConversationControl(ctx context.Context, runtime, callID, execution string) (ConversationControl, error) {
	control, err := a.fakeConversationAuthority.ConversationControl(ctx, runtime, callID, execution)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fakeConversationAuthority.controls == 3 {
		// Return the successful final observation while committing a server
		// cancellation before the runner can report completion.
		a.serverCancelled = true
		return control, err
	}
	if a.serverCancelled {
		control.Continue = false
		control.CancelRequested = true
	}
	return control, err
}

func (a *cancelAfterFinalControlFakeAuthority) ReportConversationEvent(ctx context.Context, runtime, generation, callID, execution string, event ConversationEvent) (ConversationCall, error) {
	a.mu.Lock()
	cancelled := a.serverCancelled
	a.mu.Unlock()
	if cancelled && (event.Kind == "assistant_delta" || event.Kind == "completed") {
		return ConversationCall{}, lifecycleintents.ErrConflict
	}
	return a.fakeConversationAuthority.ReportConversationEvent(ctx, runtime, generation, callID, execution, event)
}

func (a *cancelOnThirdControlAuthority) ConversationControl(context.Context, string, string, string) (ConversationControl, error) {
	a.mu.Lock()
	a.controls++
	controlNumber := a.controls
	callID := a.claim.Call.CallID
	execution := a.claim.ExecutionGeneration
	deadline := a.claim.Call.DeadlineAt
	a.mu.Unlock()
	if controlNumber == 3 {
		close(a.complete)
		<-a.waitReturned
		return ConversationControl{SchemaVersion: 1, CallID: callID, ExecutionGeneration: execution, CancelRequested: true, DeadlineAt: deadline}, nil
	}
	return ConversationControl{SchemaVersion: 1, CallID: callID, ExecutionGeneration: execution, Continue: true, DeadlineAt: deadline}, nil
}

type completedBeforeStopConversationProcess struct {
	complete     chan struct{}
	waitReturned chan struct{}
	mu           sync.Mutex
	stopped      bool
}

func (*completedBeforeStopConversationProcess) Identity() (string, string, error) {
	return "thread-owned", "turn-owned", nil
}

func (p *completedBeforeStopConversationProcess) Wait(context.Context) (ConversationExecutionResult, error) {
	<-p.complete
	close(p.waitReturned)
	return ConversationExecutionResult{Outcome: "completed", ThreadID: "thread-owned", TurnID: "turn-owned", Text: "must-not-escape"}, nil
}

func (p *completedBeforeStopConversationProcess) Stop(context.Context) error {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	return nil
}

type fakeConversationProcess struct {
	result          ConversationExecutionResult
	block           chan struct{}
	stopped         bool
	beforeWait      func() bool
	observedStarted bool
}

func (*fakeConversationProcess) Identity() (string, string, error) {
	return "thread-owned", "turn-owned", nil
}
func (p *fakeConversationProcess) Wait(context.Context) (ConversationExecutionResult, error) {
	if p.beforeWait != nil {
		p.observedStarted = p.beforeWait()
		if !p.observedStarted {
			return ConversationExecutionResult{Outcome: "failed", Failure: "protocol_error", ThreadID: "thread-owned", TurnID: "turn-owned"}, nil
		}
	}
	if p.block != nil {
		<-p.block
	}
	return p.result, nil
}
func (p *fakeConversationProcess) Stop(context.Context) error {
	if !p.stopped {
		p.stopped = true
		p.result = ConversationExecutionResult{Outcome: "cancelled", Failure: "cancelled", ThreadID: "thread-owned", TurnID: "turn-owned"}
		if p.block != nil {
			close(p.block)
		}
	}
	return nil
}

func testConversationRuntime(t *testing.T, answer string) (*fakeConversationAuthority, *fakeConversationLauncher, lifecycleintents.Runtime) {
	t.Helper()
	runtime := lifecycleintents.Runtime{ID: uuid.NewString(), Generation: uuid.NewString(), ProjectID: 1027, ExpiresAt: time.Now().Add(2 * time.Minute).Format(time.RFC3339Nano)}
	deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Second).Format(time.RFC3339)
	claim := testConversationClaim(uuid.NewString(), uuid.NewString(), runtime.Generation, deadline)
	authority := &fakeConversationAuthority{claim: &claim}
	process := &fakeConversationProcess{result: ConversationExecutionResult{Outcome: "completed", ThreadID: "thread-owned", TurnID: "turn-owned", Text: answer}}
	return authority, &fakeConversationLauncher{process: process}, runtime
}

func testConversationClaim(callID, execution, generation, deadline string) ConversationClaim {
	return ConversationClaim{
		Call:                ConversationCall{SchemaVersion: 1, CallID: callID, RequestID: "conversation-phase", State: "claimed", DeadlineAt: deadline},
		ExecutionGeneration: execution, RuntimeGeneration: generation, AccountKey: "conversation-account", AttachmentRevision: 4,
		DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", ExecutionPolicyID: ConversationPolicyV1,
		System: "system input", Messages: []ConversationMessage{{Role: "user", Content: "hello"}}, Purpose: "chat",
		Limits: ConversationLimits{MaxOutputBytes: conversationMaxOutput, MaxEvents: conversationMaxEvents},
	}
}

func conversationJournalDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}
