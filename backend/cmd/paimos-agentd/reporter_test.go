// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	localReporterSession  = "11111111-1111-4111-8111-111111111111"
	publicReporterSession = "22222222-2222-4222-8222-222222222222"
	reporterControlID     = "33333333-3333-4333-8333-333333333333"
)

func TestCLIReporterResolvesOnlyExactLocallyPinnedDispatchProfile(t *testing.T) {
	profile, err := dispatchprofile.Resolve("codex-sol-high", "2", "codex")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	runner := func(_ context.Context, _ string, args, _ []string, stdin io.Reader) ([]byte, error) {
		if !slices.Equal(args, []string{"--json", "curl", "/api/ai/execution-options?dispatch_only=1"}) || stdin != nil {
			t.Fatalf("dispatch lookup argv=%q stdin=%v", args, stdin)
		}
		seen = true
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}
	reporter, err := newCLIReporterWithRunner("ppm", "mbp0", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	got, err := reporter.ResolveDispatchProfile(context.Background(), profile.ID, profile.Version, profile.Harness)
	if err != nil || got != profile || !seen {
		t.Fatalf("resolved=%+v seen=%v err=%v", got, seen, err)
	}
	profile.Model = "gpt-6-terra"
	if _, err := reporter.ResolveDispatchProfile(context.Background(), profile.ID, profile.Version, profile.Harness); err == nil {
		t.Fatal("authority drifted from the locally pinned profile")
	}
}

func TestCLIReporterRejectsChildOnAuthorityWorkspaceConflict(t *testing.T) {
	process := &reporterTestProcess{done: make(chan struct{})}
	stateRoot := t.TempDir()
	supervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "ppm", StateRoot: stateRoot, Adapters: []agentd.Adapter{reporterTestAdapter{process: process}}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Start(context.Background(), agentd.StartRequest{
		Adapter: agentd.AdapterCodex, Workspace: t.TempDir(), Prompt: "fixture", Identity: "codex:worker", ProjectID: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if len(args) < 3 || args[2] != "register" {
			return nil, errors.New("unexpected reporter command")
		}
		if slices.Contains(args, "--account-label") {
			t.Fatalf("unknown account label must be omitted for rolling compatibility: %v", args)
		}
		if slices.Contains(args, "--account-key") {
			t.Fatalf("empty account key must be omitted for rolling compatibility: %v", args)
		}
		return []byte(`{"error":"workspace conflict","code":409,"error_code":"harness_session_conflict"}`), errors.New("API conflict")
	}
	reporter, err := newCLIReporterWithRunner("ppm", "mbp0", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.BindController(supervisor); err != nil {
		t.Fatal(err)
	}
	if err := reporter.ReportStatus(context.Background(), supervisor.Status()); err != nil {
		t.Fatalf("enforce authority conflict: %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(time.Second):
		t.Fatal("authority conflict left owned child running")
	}
	got := supervisor.Status().Sessions
	if len(got) != 1 || got[0].ID != session.ID || got[0].State != agentd.StateFailed || got[0].LastErrorCode != agentd.ErrorWorkspaceConflict || !got[0].Reporter.Closed {
		t.Fatalf("rejected session = %+v", got)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "ppm", StateRoot: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	recovered := restarted.Status().Sessions
	if len(recovered) != 1 || recovered[0].State != agentd.StateFailed || recovered[0].LastErrorCode != agentd.ErrorWorkspaceConflict || !recovered[0].Reporter.Closed {
		t.Fatalf("recovered rejection = %+v", recovered)
	}
}

func TestReporterCommandReturnsBoundedJSONErrorForTypedHandling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paimos-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '{\"error_code\":\"harness_session_conflict\"}' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := runReporterCommand(context.Background(), path, nil, os.Environ(), nil)
	if err == nil || reporterErrorCode(raw) != "harness_session_conflict" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
}

func TestReporterExecutionMatchesLegacyUnknownAccountOmission(t *testing.T) {
	if !reporterExecutionMatches(harnessSessionResponse{}, agentd.Session{AccountLabel: "unknown"}) {
		t.Fatal("legacy response without account label did not normalize to unknown")
	}
}

func reporterSessionEvidence(agent, phase string) []byte {
	raw, _ := json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: agent, Harness: "codex", Phase: phase})
	return raw
}

func reporterCompletionEvidence(kind, state, reason string) []byte {
	raw, _ := json.Marshal(harnessControlResponse{ID: reporterControlID, HarnessSessionID: publicReporterSession, Kind: kind, State: state, Reason: reason})
	return raw
}

func ownedControlReporterRunner(t *testing.T, complete func([]string)) reporterCommand {
	t.Helper()
	return func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "register":
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"})
		case "heartbeat":
			return reporterSessionEvidence("worker", "working"), nil
		case "mark-stopped":
			return reporterSessionEvidence("worker", "stopped"), nil
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"}, Controls: []harnessControlResponse{{ID: reporterControlID, HarnessSessionID: publicReporterSession, Kind: "interrupt", State: "claimed"}}})
		case "complete-control":
			if complete != nil {
				complete(args)
			}
			state, reason := "applied", "applied"
			if slices.Contains(args, "rejected") {
				state = "rejected"
				for _, candidate := range []string{"ownership_lost", "failed", "not_running", "unsupported"} {
					if slices.Contains(args, candidate) {
						reason = candidate
					}
				}
			}
			return reporterCompletionEvidence("interrupt", state, reason), nil
		}
		return nil, errors.New("unexpected reporter command")
	}
}

type recordingReporterController struct {
	interruptSession string
	interruptRequest agentd.ControlRequest
	checkpoints      []agentd.ReporterState
	partialReceipt   bool
}

type checkpointFailOnceController struct {
	recordingReporterController
	failed bool
}

type statefulReporterController struct {
	state      agentd.ReporterState
	interrupts int
	fail       func(agentd.ReporterState) bool
}

type failingDeleteLeaseStore struct {
	reporterLeaseStore
	fail bool
}

func (s *failingDeleteLeaseStore) Delete(sessionID string) error {
	if s.fail {
		return errors.New("delete failed")
	}
	return s.reporterLeaseStore.Delete(sessionID)
}

func (c *statefulReporterController) Interrupt(_ context.Context, session string, request agentd.ControlRequest) (agentd.Receipt, error) {
	c.interrupts++
	return agentd.Receipt{Operation: "interrupt", SessionID: session, Instance: request.Instance, ProjectID: request.ProjectID,
		Identity: request.Identity, RequestedLevel: "steer", EffectiveLevel: "steer", Primitive: "test interrupt",
		CorrelationID: request.CorrelationID, AppliedAt: time.Now().UTC()}, nil
}
func (*statefulReporterController) Stop(context.Context, string, agentd.ControlRequest) (agentd.Receipt, error) {
	return agentd.Receipt{}, errors.New("unexpected stop")
}
func (*statefulReporterController) Reject(context.Context, string, agentd.ControlRequest, agentd.ErrorCode) error {
	return errors.New("unexpected reject")
}
func (c *statefulReporterController) CheckpointReporter(_ context.Context, _ string, _ agentd.ControlRequest, state agentd.ReporterState) error {
	if c.fail != nil && c.fail(state) {
		return errors.New("journal unavailable")
	}
	c.state = state
	return nil
}

func (c *checkpointFailOnceController) CheckpointReporter(ctx context.Context, session string, request agentd.ControlRequest, state agentd.ReporterState) error {
	if state.PublicSessionID != "" && !c.failed {
		c.failed = true
		return errors.New("journal unavailable")
	}
	return c.recordingReporterController.CheckpointReporter(ctx, session, request, state)
}

type reporterTestProcess struct {
	done       chan struct{}
	stopOnce   sync.Once
	interrupts atomic.Int32
}

func (p *reporterTestProcess) PID() int    { return 4242 }
func (p *reporterTestProcess) Wait() error { <-p.done; return nil }
func (p *reporterTestProcess) Steer(context.Context, agentd.ControlRequest) (agentd.ControlEffect, error) {
	return agentd.ControlEffect{}, agentd.ErrCapabilityMissing
}
func (p *reporterTestProcess) Interrupt(_ context.Context, request agentd.ControlRequest) (agentd.ControlEffect, error) {
	p.interrupts.Add(1)
	return agentd.ControlEffect{Primitive: "test interrupt", CorrelationID: request.CorrelationID}, nil
}
func (p *reporterTestProcess) Stop(_ context.Context, request agentd.ControlRequest) (agentd.ControlEffect, error) {
	p.stopOnce.Do(func() { close(p.done) })
	return agentd.ControlEffect{Primitive: "test stop", CorrelationID: request.CorrelationID}, nil
}

type reporterTestAdapter struct{ process *reporterTestProcess }

func (a reporterTestAdapter) Name() string { return agentd.AdapterCodex }
func (a reporterTestAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt, agentd.CapabilityStop}
}
func (a reporterTestAdapter) Start(_ context.Context, _ agentd.StartRequest, observe func(agentd.AdapterEvent)) (agentd.Process, error) {
	observe(agentd.AdapterEvent{Kind: agentd.EventSessionStarted, HarnessSessionID: localReporterSession})
	return a.process, nil
}

func (c *recordingReporterController) Interrupt(_ context.Context, session string, request agentd.ControlRequest) (agentd.Receipt, error) {
	c.interruptSession, c.interruptRequest = session, request
	if c.partialReceipt {
		return agentd.Receipt{Operation: "interrupt", SessionID: session, CorrelationID: request.CorrelationID}, nil
	}
	return agentd.Receipt{Operation: "interrupt", SessionID: session, Instance: request.Instance, ProjectID: request.ProjectID,
		Identity: request.Identity, RequestedLevel: "steer", EffectiveLevel: "steer", Primitive: "test interrupt",
		CorrelationID: request.CorrelationID, AppliedAt: time.Now().UTC()}, nil
}

func (*recordingReporterController) Stop(context.Context, string, agentd.ControlRequest) (agentd.Receipt, error) {
	return agentd.Receipt{}, errors.New("unexpected stop")
}

func (*recordingReporterController) Reject(context.Context, string, agentd.ControlRequest, agentd.ErrorCode) error {
	return errors.New("unexpected reject")
}

func (c *recordingReporterController) CheckpointReporter(_ context.Context, _ string, _ agentd.ControlRequest, state agentd.ReporterState) error {
	c.checkpoints = append(c.checkpoints, state)
	return nil
}

func TestCLIReporterRegistersExactScopeAndAppliesTypedControl(t *testing.T) {
	commands := make([][]string, 0, 4)
	var workerLease string
	runner := func(_ context.Context, _ string, args, environment []string, stdin io.Reader) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if slices.Contains(args, "--instance") {
			t.Fatal("headless reporter must use its pinned URL/file target without conflicting --instance")
		}
		if !slices.Contains(environment, "PAIMOS_URL=https://ppm.example") || !slices.Contains(environment, "PAIMOS_API_KEY_FILE=/run/credentials/ppm") {
			t.Fatalf("environment=%v", environment)
		}
		switch args[2] {
		case "register":
			body, _ := io.ReadAll(stdin)
			var registration struct {
				SessionRef  string `json:"harness_session_ref"`
				WorkerLease string `json:"worker_lease"`
			}
			if json.Unmarshal(body, &registration) != nil {
				t.Fatalf("registration body is invalid")
			}
			workerLease = registration.WorkerLease
			if registration.SessionRef != localReporterSession || len(workerLease) != 43 || slices.Contains(args, workerLease) || slices.Contains(args, "inbox") || slices.Contains(args, "steer") ||
				!slices.Contains(args, "status,interrupt,stop") || !slices.Contains(args, "--parent-session") || !slices.Contains(args, "33333333-3333-4333-8333-333333333333") ||
				!slices.Contains(args, "--ticket-id") || !slices.Contains(args, "903") || !slices.Contains(args, "--work-shape") || !slices.Contains(args, "ship") || !slices.Contains(args, "worker") {
				t.Fatalf("unsafe registration args=%v body=%q", args, body)
			}
			parent := "33333333-3333-4333-8333-333333333333"
			ticket := int64(903)
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex", Role: "worker", ParentSessionID: &parent, TicketID: &ticket, WorkShape: "ship"})
		case "heartbeat", "yield", "complete-control":
			body, _ := io.ReadAll(stdin)
			if string(body) != workerLease || slices.Contains(args, workerLease) {
				t.Fatalf("worker lease transport escaped stdin")
			}
			if args[2] == "heartbeat" {
				return reporterSessionEvidence("worker", "working"), nil
			}
			if args[2] == "yield" {
				return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"},
					Controls: []harnessControlResponse{{ID: reporterControlID, HarnessSessionID: publicReporterSession, Kind: "interrupt", State: "claimed"}}})
			}
			if !slices.Contains(args, reporterControlID) || !slices.Contains(args, "applied") {
				t.Fatalf("completion args=%v", args)
			}
			return reporterCompletionEvidence("interrupt", "applied", "applied"), nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	}
	reporter, err := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", reporterEnvironment(nil, "https://ppm.example", "/run/credentials/ppm"), runner, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	controller := &recordingReporterController{}
	if err := reporter.BindController(controller); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		Role: "worker", ParentSessionID: "33333333-3333-4333-8333-333333333333", TicketID: 903, WorkShape: "ship",
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilitySteer, agentd.CapabilityInterrupt, agentd.CapabilityStop}}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", HeartbeatAt: time.Now(), Sessions: []agentd.Session{session}}); err != nil {
		t.Fatal(err)
	}
	seenAppliedCheckpoint := false
	for _, checkpoint := range controller.checkpoints {
		seenAppliedCheckpoint = seenAppliedCheckpoint || checkpoint.Pending != nil && checkpoint.Pending.ControlID == reporterControlID && checkpoint.Pending.Outcome == "applied"
	}
	if len(commands) != 4 || len(controller.checkpoints) < 5 || !seenAppliedCheckpoint || controller.interruptSession != localReporterSession || controller.interruptRequest != (agentd.ControlRequest{
		Instance: "ppm", ProjectID: 6, Identity: "codex:worker", CorrelationID: reporterControlID,
	}) {
		t.Fatalf("commands=%d session=%q request=%+v", len(commands), controller.interruptSession, controller.interruptRequest)
	}
}

func TestCLIReporterHeartbeatsBeforeYieldAndCarriesActivityEvidence(t *testing.T) {
	var commands []string
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		commands = append(commands, args[2])
		switch args[2] {
		case "register":
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"})
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex", Phase: "yielded"}})
		case "heartbeat":
			if !slices.Contains(args, "--activity-sequence") || !slices.Contains(args, "3") || !slices.Contains(args, "--activity-kind") || !slices.Contains(args, "tool_started") {
				t.Fatalf("activity evidence args=%v", args)
			}
			return reporterSessionEvidence("worker", "working"), nil
		default:
			return nil, errors.New("unexpected reporter command")
		}
	}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	controller := &recordingReporterController{}
	if err := reporter.BindController(controller); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{
		ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true, State: agentd.StateRunning,
		Capabilities: []agentd.Capability{agentd.CapabilityStatus}, ActivitySequence: 3, LastEventKind: agentd.EventToolStarted,
	}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(commands, ","); got != "register,heartbeat,yield" {
		t.Fatalf("report order=%s", got)
	}
}

func TestCLIReporterYieldFailureStillRecordsHeartbeatFirst(t *testing.T) {
	var commands []string
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		commands = append(commands, args[2])
		switch args[2] {
		case "heartbeat":
			return reporterSessionEvidence("worker", "working"), nil
		case "yield":
			return nil, errors.New("yield unavailable")
		default:
			return nil, errors.New("unexpected reporter command")
		}
	}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	if err := reporter.BindController(&recordingReporterController{}); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus},
		Reporter: agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus}}}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err == nil {
		t.Fatal("yield failure was not surfaced")
	}
	if got := strings.Join(commands, ","); got != "heartbeat,yield" {
		t.Fatalf("report order=%s", got)
	}
}

func TestCLIReporterRejectsMismatchedAndMalformedRemoteScope(t *testing.T) {
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if args[2] == "register" {
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 7, AgentName: "worker", Harness: "codex"})
		}
		return nil, errors.New("unexpected command")
	}
	reporter, err := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.BindController(&recordingReporterController{}); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus}}
	err = reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}})
	if err == nil || !strings.Contains(err.Error(), "failed for 1 session") {
		t.Fatalf("error=%v", err)
	}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "foreign"}); err == nil {
		t.Fatal("foreign instance was accepted")
	}
}

func TestCLIReporterRejectsUnboundSuccessfulAcknowledgements(t *testing.T) {
	base := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt},
		Reporter: agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt}}}
	for name, prepare := range map[string]func(*agentd.Session) string{
		"heartbeat_empty": func(_ *agentd.Session) string { return "heartbeat" },
		"completion_empty": func(s *agentd.Session) string {
			s.Reporter.Pending = &agentd.ReporterCompletion{ControlID: reporterControlID, Kind: "interrupt", Outcome: "applied", Reason: "applied"}
			return "complete-control"
		},
		"stopped_scope_mismatch": func(s *agentd.Session) string { s.State = agentd.StateOwnershipLost; return "mark-stopped" },
	} {
		t.Run(name, func(t *testing.T) {
			session := base
			command := prepare(&session)
			runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
				if args[2] != command {
					return nil, errors.New("unexpected command")
				}
				if command == "mark-stopped" {
					return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 7, AgentName: "worker", Harness: "codex", Phase: "stopped"})
				}
				return []byte(`{}`), nil
			}
			controller := &recordingReporterController{}
			reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
			_ = reporter.BindController(controller)
			if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err == nil {
				t.Fatal("unbound 200 response advanced reporter state")
			}
			for _, state := range controller.checkpoints {
				if state.Closed || state.RemoteClosed || (command == "complete-control" && state.Pending == nil) {
					t.Fatalf("unsafe state advance: %+v", state)
				}
			}
		})
	}
}

func TestReporterEnvironmentRemovesAmbientCredentialTargets(t *testing.T) {
	got := reporterEnvironment([]string{"PATH=/bin", "PAIMOS_API_KEY=secret", "PPM_URL=https://wrong", "PPMAPIKEY=wrong", "PAIMOS_AGENT_NAME=launcher", "PAIMOS_SESSION_ID=foreign"}, "https://ppm.example", "/run/credentials/ppm")
	want := []string{"PATH=/bin", "PAIMOS_URL=https://ppm.example", "PAIMOS_API_KEY_FILE=/run/credentials/ppm"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment=%v", got)
	}
}

func TestCLIReporterOldestFailureDoesNotStarveLaterControl(t *testing.T) {
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "register":
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "later", Harness: "codex"})
		case "heartbeat":
			return reporterSessionEvidence("later", "working"), nil
		case "complete-control":
			return reporterCompletionEvidence("interrupt", "applied", "applied"), nil
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "later", Harness: "codex"}, Controls: []harnessControlResponse{{ID: reporterControlID, HarnessSessionID: publicReporterSession, Kind: "interrupt", State: "claimed"}}})
		}
		return nil, errors.New("unexpected command")
	}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	controller := &recordingReporterController{}
	if err := reporter.BindController(controller); err != nil {
		t.Fatal(err)
	}
	sessions := []agentd.Session{
		{ID: "44444444-4444-4444-8444-444444444444", ProjectID: 6, Identity: "malformed", Adapter: "codex", Managed: true, State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus}},
		{ID: localReporterSession, ProjectID: 6, Identity: "codex:later", Adapter: "codex", Managed: true, State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt}},
	}
	err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: sessions})
	if err == nil || controller.interruptSession != localReporterSession || controller.interruptRequest.CorrelationID != reporterControlID {
		t.Fatalf("error=%v later_session=%q request=%+v", err, controller.interruptSession, controller.interruptRequest)
	}
}

func TestCLIReporterRetriesPublicMappingAfterCheckpointFailure(t *testing.T) {
	registers := 0
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "register":
			registers++
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"})
		case "heartbeat":
			return reporterSessionEvidence("worker", "working"), nil
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"}})
		}
		return nil, errors.New("unexpected command")
	}
	leases := newMemoryReporterLeaseStore()
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	controller := &checkpointFailOnceController{}
	if err := reporter.BindController(controller); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus}}
	status := agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}
	if err := reporter.ReportStatus(context.Background(), status); err == nil {
		t.Fatal("expected first public checkpoint failure")
	}
	recovered := session
	recovered.Reporter = controller.checkpoints[0]
	restarted, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	if err := restarted.BindController(controller); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{recovered}}); err != nil {
		t.Fatal(err)
	}
	if registers != 2 {
		t.Fatalf("registration attempts=%d want 2", registers)
	}
}

func TestCLIReporterRejectsPartialOwnedReceipt(t *testing.T) {
	completedRejected := false
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "register":
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"})
		case "heartbeat":
			return reporterSessionEvidence("worker", "working"), nil
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"}, Controls: []harnessControlResponse{{ID: reporterControlID, HarnessSessionID: publicReporterSession, Kind: "interrupt", State: "claimed"}}})
		case "complete-control":
			completedRejected = slices.Contains(args, "rejected") && slices.Contains(args, "failed")
			return reporterCompletionEvidence("interrupt", "rejected", "failed"), nil
		}
		return nil, errors.New("unexpected command")
	}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
	controller := &recordingReporterController{partialReceipt: true}
	if err := reporter.BindController(controller); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt}}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err != nil {
		t.Fatal(err)
	}
	if !completedRejected {
		t.Fatal("partial owned receipt was reported applied")
	}
}

func TestCLIReporterRestartRetriesJournaledCompletionWithoutSecondEffect(t *testing.T) {
	root := t.TempDir()
	leases, err := newDiskReporterLeaseStore(root, "ppm")
	if err != nil {
		t.Fatal(err)
	}
	var registeredLease string
	process := &reporterTestProcess{done: make(chan struct{})}
	firstSupervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "ppm", StateRoot: root, Adapters: []agentd.Adapter{reporterTestAdapter{process: process}}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := firstSupervisor.Start(context.Background(), agentd.StartRequest{Adapter: agentd.AdapterCodex, Workspace: t.TempDir(), Prompt: "private", Identity: "codex:worker", ProjectID: 6})
	if err != nil {
		t.Fatal(err)
	}
	completionAttempts := 0
	firstRunner := func(_ context.Context, _ string, args, _ []string, stdin io.Reader) ([]byte, error) {
		switch args[2] {
		case "register":
			raw, _ := io.ReadAll(stdin)
			var secret struct {
				WorkerLease string `json:"worker_lease"`
			}
			if json.Unmarshal(raw, &secret) != nil || len(secret.WorkerLease) != 43 {
				t.Fatal("registration lease missing")
			}
			registeredLease = secret.WorkerLease
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex", Workspace: &session.WorkspaceProvenance, AccountLabel: "unknown"})
		case "heartbeat":
			if raw, _ := io.ReadAll(stdin); string(raw) != registeredLease {
				t.Fatal("heartbeat lease drift")
			}
			return reporterSessionEvidence("worker", "working"), nil
		case "yield":
			if raw, _ := io.ReadAll(stdin); string(raw) != registeredLease {
				t.Fatal("yield lease drift")
			}
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"}, Controls: []harnessControlResponse{{ID: reporterControlID, HarnessSessionID: publicReporterSession, Kind: "interrupt", State: "claimed"}}})
		case "complete-control":
			completionAttempts++
			return nil, errors.New("network unavailable")
		}
		return nil, errors.New("unexpected command")
	}
	firstReporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, firstRunner, leases)
	if err := firstReporter.BindController(firstSupervisor); err != nil {
		t.Fatal(err)
	}
	if err := firstReporter.ReportStatus(context.Background(), firstSupervisor.Status()); err == nil {
		t.Fatal("expected remote completion failure")
	}
	if process.interrupts.Load() != 1 || completionAttempts != 1 {
		t.Fatalf("interrupts=%d completions=%d", process.interrupts.Load(), completionAttempts)
	}
	if err := firstSupervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	secondSupervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "ppm", StateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer secondSupervisor.Close(context.Background())
	secondRunner := func(_ context.Context, _ string, args, _ []string, stdin io.Reader) ([]byte, error) {
		switch args[2] {
		case "complete-control":
			if raw, _ := io.ReadAll(stdin); string(raw) != registeredLease {
				t.Fatal("restart changed worker lease")
			}
			completionAttempts++
			if !slices.Contains(args, reporterControlID) || !slices.Contains(args, publicReporterSession) {
				t.Fatalf("recovery completion lost exact IDs: %v", args)
			}
			return reporterCompletionEvidence("interrupt", "applied", "applied"), nil
		case "mark-stopped":
			if raw, _ := io.ReadAll(stdin); string(raw) != registeredLease {
				t.Fatal("stop lease drift")
			}
			return reporterSessionEvidence("worker", "stopped"), nil
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"}})
		}
		return nil, errors.New("unexpected recovery command")
	}
	secondReporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, secondRunner, leases)
	if err := secondReporter.BindController(secondSupervisor); err != nil {
		t.Fatal(err)
	}
	status := secondSupervisor.Status()
	if len(status.Sessions) != 1 || status.Sessions[0].ID != session.ID || status.Sessions[0].State != agentd.StateOwnershipLost || status.Sessions[0].Reporter.Pending == nil {
		t.Fatalf("recovered status=%+v", status)
	}
	if err := secondReporter.ReportStatus(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	if process.interrupts.Load() != 1 || completionAttempts != 2 {
		t.Fatalf("restart duplicated effect: interrupts=%d completions=%d", process.interrupts.Load(), completionAttempts)
	}
}

func TestDiskReporterLeaseStorePersistsAndRejectsUnsafeCustody(t *testing.T) {
	root := t.TempDir()
	store, err := newDiskReporterLeaseStore(root, "ppm")
	if err != nil {
		t.Fatal(err)
	}
	firstID := localReporterSession
	first, err := store.GetOrCreate(firstID)
	if err != nil || len(first) != 43 {
		t.Fatalf("lease=%q err=%v", first, err)
	}
	reopened, err := newDiskReporterLeaseStore(root, "ppm")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.GetOrCreate(firstID)
	if err != nil || second != first {
		t.Fatalf("lease changed across reopen: err=%v", err)
	}
	stateDir, _ := agentd.InstanceStateDir(root, "ppm")
	directory := filepath.Join(stateDir, "reporter-leases")
	leasePath := filepath.Join(directory, firstID)
	info, err := os.Lstat(leasePath)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("unsafe lease file info=%v err=%v", info, err)
	}
	if err := os.Chmod(leasePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.GetOrCreate(firstID); err == nil {
		t.Fatal("world-readable lease accepted")
	}
	if err := os.Chmod(leasePath, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkID := "44444444-4444-4444-8444-444444444444"
	if err := os.Symlink(leasePath, filepath.Join(directory, symlinkID)); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.GetOrCreate(symlinkID); err == nil {
		t.Fatal("symlink lease accepted")
	}
	hardlinkID := "55555555-5555-4555-8555-555555555555"
	if err := os.Link(leasePath, filepath.Join(directory, hardlinkID)); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.GetOrCreate(hardlinkID); err == nil {
		t.Fatal("hard-linked lease accepted")
	}
}

func TestDiskReporterLeaseStoreSweepsOnlyExactCrashResidue(t *testing.T) {
	root := t.TempDir()
	if _, err := newDiskReporterLeaseStore(root, "ppm"); err != nil {
		t.Fatal(err)
	}
	stateDir, _ := agentd.InstanceStateDir(root, "ppm")
	directory := filepath.Join(stateDir, "reporter-leases")
	tempName := "." + localReporterSession + ".66666666-6666-4666-8666-666666666666"
	if err := os.WriteFile(filepath.Join(directory, tempName), []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "unrelated"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDiskReporterLeaseStore(root, "ppm"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, tempName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash residue remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "unrelated")); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
}

func TestCLIReporterRemoteCloseWaitsForDurableLeaseReleaseBeforePrunable(t *testing.T) {
	marks := 0
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if args[2] != "mark-stopped" {
			return nil, errors.New("unexpected command")
		}
		marks++
		return reporterSessionEvidence("worker", "stopped"), nil
	}
	leases := &failingDeleteLeaseStore{reporterLeaseStore: newMemoryReporterLeaseStore(), fail: true}
	controller := &statefulReporterController{}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true, State: agentd.StateOwnershipLost,
		Reporter: agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop}}}
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = reporter.BindController(controller)
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err == nil {
		t.Fatal("expected durable lease release failure")
	}
	if !controller.state.RemoteClosed || controller.state.Closed || marks != 1 {
		t.Fatalf("state=%+v marks=%d", controller.state, marks)
	}
	leases.fail = false
	recovered := session
	recovered.Reporter = controller.state
	restarted, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = restarted.BindController(controller)
	if err := restarted.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{recovered}}); err != nil {
		t.Fatal(err)
	}
	if !controller.state.Closed || marks != 1 {
		t.Fatalf("recovery repeated remote close: state=%+v marks=%d", controller.state, marks)
	}
}

func TestCLIReporterTerminalMarkStoppedReplayConverges(t *testing.T) {
	marks := 0
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if args[2] != "mark-stopped" {
			return nil, errors.New("unexpected terminal command")
		}
		marks++
		return reporterSessionEvidence("worker", "stopped"), nil
	}
	status := agentd.Status{Instance: "ppm", Sessions: []agentd.Session{{ID: localReporterSession, ProjectID: 6,
		Identity: "codex:worker", Adapter: "codex", Managed: true, State: agentd.StateOwnershipLost,
		Reporter: agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop}}}}}
	for range 2 {
		reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
		if err := reporter.BindController(&recordingReporterController{}); err != nil {
			t.Fatal(err)
		}
		if err := reporter.ReportStatus(context.Background(), status); err != nil {
			t.Fatal(err)
		}
	}
	if marks != 2 {
		t.Fatalf("idempotent terminal marks=%d want 2", marks)
	}
}

func TestCLIReporterCodexTurnFailureClosesPublicSession(t *testing.T) {
	for _, code := range []agentd.ErrorCode{agentd.ErrorTurnFailed, agentd.ErrorAppServerProtocol} {
		t.Run(string(code), func(t *testing.T) {
			marks := 0
			runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
				if args[2] != "mark-stopped" || !slices.Contains(args, "process_failed") {
					t.Fatalf("failed Codex turn reported as live/idle: %v", args)
				}
				marks++
				return reporterSessionEvidence("worker", "stopped"), nil
			}
			controller := &statefulReporterController{}
			reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, newMemoryReporterLeaseStore())
			_ = reporter.BindController(controller)
			status := agentd.Status{Instance: "ppm", Sessions: []agentd.Session{{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true, State: agentd.StateFailed, LastErrorCode: code,
				Reporter: agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop}}}}}
			if err := reporter.ReportStatus(context.Background(), status); err != nil {
				t.Fatal(err)
			}
			if marks != 1 || !controller.state.RemoteClosed || !controller.state.Closed {
				t.Fatalf("public failure closure incomplete: marks=%d state=%+v", marks, controller.state)
			}
		})
	}
}

func TestCLIReporterTerminalReasonDriftAfterRemoteCloseCrashConverges(t *testing.T) {
	remoteReason := ""
	requestedReasons := []string{}
	runner := func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		if args[2] != "mark-stopped" {
			return nil, errors.New("unexpected terminal command")
		}
		reason := ""
		for index := range args {
			if args[index] == "--reason" && index+1 < len(args) {
				reason = args[index+1]
			}
		}
		requestedReasons = append(requestedReasons, reason)
		if remoteReason == "" {
			remoteReason = reason
		}
		return reporterSessionEvidence("worker", "stopped"), nil
	}
	leases := newMemoryReporterLeaseStore()
	controller := &statefulReporterController{}
	failed := false
	controller.fail = func(state agentd.ReporterState) bool {
		if state.RemoteClosed && !failed {
			failed = true
			return true
		}
		return false
	}
	base := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateFailed, Reporter: agentd.ReporterState{PublicSessionID: publicReporterSession, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop}}}
	first, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = first.BindController(controller)
	if err := first.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{base}}); err == nil {
		t.Fatal("expected journal failure after accepted remote close")
	}
	controller.fail = nil
	recovered := base
	recovered.State = agentd.StateOwnershipLost
	second, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = second.BindController(controller)
	if err := second.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{recovered}}); err != nil {
		t.Fatal(err)
	}
	if remoteReason != "process_failed" || !slices.Equal(requestedReasons, []string{"process_failed", "ownership_lost"}) || !controller.state.Closed {
		t.Fatalf("remote_reason=%q requested=%v state=%+v", remoteReason, requestedReasons, controller.state)
	}
}

func TestCLIReporterRemoteCompletionSuccessThenClearFailureRecoversWithoutSecondEffect(t *testing.T) {
	completionCalls, sawApplied, failedClear := 0, false, false
	controller := &statefulReporterController{}
	controller.fail = func(state agentd.ReporterState) bool {
		if state.Pending != nil && state.Pending.Outcome == "applied" {
			sawApplied = true
		}
		if sawApplied && state.Pending == nil && state.PublicSessionID != "" && !failedClear {
			failedClear = true
			return true
		}
		return false
	}
	runner := ownedControlReporterRunner(t, func([]string) { completionCalls++ })
	leases := newMemoryReporterLeaseStore()
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = reporter.BindController(controller)
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt}}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err == nil {
		t.Fatal("expected post-ack checkpoint failure")
	}
	if controller.interrupts != 1 || controller.state.Pending == nil || controller.state.Pending.Outcome != "applied" {
		t.Fatalf("effect=%d state=%+v", controller.interrupts, controller.state)
	}
	controller.fail = nil
	recovered := session
	recovered.State, recovered.Reporter = agentd.StateOwnershipLost, controller.state
	restarted, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = restarted.BindController(controller)
	if err := restarted.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{recovered}}); err != nil {
		t.Fatal(err)
	}
	if controller.interrupts != 1 || completionCalls != 2 || controller.state.Pending != nil {
		t.Fatalf("restart duplicated/lost result: effects=%d completions=%d state=%+v", controller.interrupts, completionCalls, controller.state)
	}
}

func TestCLIReporterEffectCheckpointFailureRecoversConservativeOutcomeWithoutReplay(t *testing.T) {
	failedApplied, completedRejected := false, false
	controller := &statefulReporterController{}
	controller.fail = func(state agentd.ReporterState) bool {
		if state.Pending != nil && state.Pending.Outcome == "applied" && !failedApplied {
			failedApplied = true
			return true
		}
		return false
	}
	runner := ownedControlReporterRunner(t, func(args []string) {
		completedRejected = slices.Contains(args, "rejected") && slices.Contains(args, "ownership_lost")
	})
	leases := newMemoryReporterLeaseStore()
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = reporter.BindController(controller)
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt}}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err == nil {
		t.Fatal("expected applied checkpoint failure")
	}
	if controller.interrupts != 1 || controller.state.Pending == nil || controller.state.Pending.Reason != "ownership_lost" {
		t.Fatalf("fallback was not durable before effect: effects=%d state=%+v", controller.interrupts, controller.state)
	}
	controller.fail = nil
	recovered := session
	recovered.State, recovered.Reporter = agentd.StateOwnershipLost, controller.state
	restarted, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, runner, leases)
	_ = restarted.BindController(controller)
	if err := restarted.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{recovered}}); err != nil {
		t.Fatal(err)
	}
	if controller.interrupts != 1 || !completedRejected {
		t.Fatalf("unsafe replay/outcome: effects=%d rejected=%v", controller.interrupts, completedRejected)
	}
}

func TestCLIReporterClaimCheckpointFailureNeverInvokesOwnedEffect(t *testing.T) {
	controller := &statefulReporterController{}
	controller.fail = func(state agentd.ReporterState) bool { return state.Pending != nil }
	reporter, _ := newCLIReporterWithRunner("ppm", "camyb-box", "/opt/paimos", nil, ownedControlReporterRunner(t, nil), newMemoryReporterLeaseStore())
	_ = reporter.BindController(controller)
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true,
		State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt}}
	if err := reporter.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err == nil {
		t.Fatal("expected pre-effect checkpoint failure")
	}
	if controller.interrupts != 0 {
		t.Fatalf("owned effect ran before durable claim fallback: %d", controller.interrupts)
	}
}

func TestAuthenticatedMachineProvenanceUsesProtectedReporter(t *testing.T) {
	unavailable := false
	reporter, err := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", []string{"PAIMOS_API_KEY_FILE=/fixture/protected"}, func(_ context.Context, _ string, args, environment []string, input io.Reader) ([]byte, error) {
		if !slices.Equal(args, []string{"--json", "auth", "whoami"}) || input != nil || !slices.Equal(environment, []string{"PAIMOS_API_KEY_FILE=/fixture/protected"}) {
			t.Fatal("machine probe bypassed protected reporter")
		}
		if unavailable {
			return nil, errors.New("unavailable")
		}
		return []byte(`{}`), nil
	}, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	if machine, err := reporter.AuthenticatedMachineID(context.Background()); err != nil || machine != "fixture-host" {
		t.Fatal("missing authenticated host provenance")
	}
	unavailable = true
	if machine, err := reporter.AuthenticatedMachineID(context.Background()); err == nil || machine != "" {
		t.Fatal("local host hint survived failed authentication")
	}
}

type nativeRecordingController struct {
	recordingReporterController
	supported bool
}

func (c *nativeRecordingController) SupportsInbox(string) bool { return c.supported }
func TestNativeReporterAdvertisesOnlyOwnedInboxWithMatchingAuthority(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported_%t", supported), func(t *testing.T) {
			controller := &nativeRecordingController{supported: supported}
			session := agentd.Session{ID: localReporterSession, Identity: "codex:worker", ProjectID: 6, Adapter: "codex", Role: "worker", Managed: true, State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilitySteer, agentd.CapabilityStop}}
			response := models.HarnessSession{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex", Host: "fixture-host", ManagementMode: "managed", MessageTargetID: reporterControlID, Role: "worker", Phase: "working"}
			response.Capabilities.Inbox = supported
			response.Capabilities.Steer = supported
			registered := false
			r, err := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
				switch args[2] {
				case "register":
					caps := args[slices.Index(args, "--capability")+1]
					mode := args[slices.Index(args, "--steer-mode")+1]
					if strings.Contains(caps, "inbox") != supported || (mode == "owned") != supported {
						t.Fatal("unowned capability advertised")
					}
					registered = true
					return json.Marshal(response)
				case "heartbeat":
					return json.Marshal(response)
				case "yield":
					return json.Marshal(map[string]any{"session": response})
				}
				return nil, errors.New("unexpected fixture command")
			}, newMemoryReporterLeaseStore())
			if err != nil {
				t.Fatal(err)
			}
			r.nativeDelivery = true
			if err := r.BindController(controller); err != nil {
				t.Fatal(err)
			}
			if err := r.ReportStatus(context.Background(), agentd.Status{Instance: "fixture", Sessions: []agentd.Session{session}}); err != nil || !registered {
				t.Fatal("native registration failed")
			}
		})
	}
}

func TestNativeReporterAcceptsProductionRegistrationWire(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		for _, tc := range []struct {
			name   string
			change func(*models.HarnessSession)
		}{
			{name: "owned_inbox_and_steer"},
			{name: "missing_inbox", change: func(s *models.HarnessSession) { s.Capabilities.Inbox = false }},
			{name: "missing_steer", change: func(s *models.HarnessSession) { s.Capabilities.Steer = false }},
			{name: "wrong_host", change: func(s *models.HarnessSession) { s.Host = "other-host" }},
			{name: "missing_target", change: func(s *models.HarnessSession) { s.MessageTargetID = "" }},
		} {
			t.Run(harness+"/"+tc.name, func(t *testing.T) {
				controller := &nativeRecordingController{supported: true}
				session := agentd.Session{ID: localReporterSession, Identity: harness + ":worker", ProjectID: 6, Adapter: harness,
					Role: "worker", Managed: true, State: agentd.StateRunning,
					Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilitySteer, agentd.CapabilityStop}}
				// Serialize the server's production response type, independently of
				// the reporter decoder, so a mistaken JSON tag cannot validate itself.
				response := models.HarnessSession{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: harness,
					Host: "fixture-host", ManagementMode: "managed", MessageTargetID: reporterControlID, Role: "worker", Phase: "working",
					Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Stop: true}}
				if tc.change != nil {
					tc.change(&response)
				}
				var commands []string
				r, err := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil,
					func(_ context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
						commands = append(commands, args[2])
						switch args[2] {
						case "register", "heartbeat":
							return json.Marshal(response)
						case "yield":
							return json.Marshal(map[string]any{"session": response})
						default:
							return nil, errors.New("unexpected fixture command")
						}
					}, newMemoryReporterLeaseStore())
				if err != nil {
					t.Fatal(err)
				}
				r.nativeDelivery = true
				if err := r.BindController(controller); err != nil {
					t.Fatal(err)
				}
				err = r.ReportStatus(context.Background(), agentd.Status{Instance: "fixture", Sessions: []agentd.Session{session}})
				accepted := tc.change == nil
				if (err == nil) != accepted {
					t.Fatalf("registration accepted=%t want=%t: %v", err == nil, accepted, err)
				}
				checkpointed := false
				for _, checkpoint := range controller.checkpoints {
					if checkpoint.PublicSessionID == publicReporterSession {
						checkpointed = true
					}
				}
				_, mapped := r.sessions[session.ID]
				if checkpointed != accepted || mapped != accepted {
					t.Fatalf("public mapping checkpointed=%t mapped=%t want=%t", checkpointed, mapped, accepted)
				}
				wantCommands := []string{"register"}
				if accepted {
					wantCommands = append(wantCommands, "heartbeat", "yield")
				}
				if !slices.Equal(commands, wantCommands) {
					t.Fatalf("reporter commands=%v want=%v", commands, wantCommands)
				}
			})
		}
	}
}
