//go:build !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const codexHelperEnvironment = "PAIMOS_CODEX_APP_SERVER_HELPER"

// A protocol fixture is not a CLI auth probe. Invoking a Go test binary with
// `login status` would recursively run its whole suite instead of probing auth.
type codexProtocolTestAdapter struct{ *CodexAdapter }

func (codexProtocolTestAdapter) AccountLabel(context.Context) string { return "unknown" }

func TestCodexProcessOwnsExactAppServerSessionForControl(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	var argv []string
	adapter.command = func(_ string, args ...string) *exec.Cmd {
		argv = append([]string(nil), args...)
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=serve")
		return cmd
	}
	events := make(chan AdapterEvent, 16)
	process, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:test", Adapter: AdapterCodex,
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv, []string{"app-server", "--listen", "stdio://"}) || process.PID() <= 0 {
		t.Fatalf("argv=%q pid=%d", argv, process.PID())
	}
	steer, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "delivery-live", Text: "new direction"})
	if err != nil {
		t.Fatal(err)
	}
	if steer.Primitive != "codex app-server turn/steer" || steer.CorrelationID != "delivery-live" || steer.VendorMessageID != "turn-owned" {
		t.Fatalf("steer=%+v", steer)
	}
	interrupt, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "control-live"})
	if err != nil {
		t.Fatal(err)
	}
	if interrupt.Primitive != "codex app-server turn/interrupt" || interrupt.VendorMessageID != "turn-owned" {
		t.Fatalf("interrupt=%+v", interrupt)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	seenSession, seenTurn, seenControl := false, false, false
	for len(events) > 0 {
		event := <-events
		seenSession = seenSession || event.Kind == EventSessionStarted && event.HarnessSessionID == "thread-owned"
		seenTurn = seenTurn || event.Kind == EventTurnStarted
		seenControl = seenControl || event.Kind == EventControlApplied && event.CorrelationID != ""
	}
	if !seenSession || !seenTurn || !seenControl {
		t.Fatalf("events session=%t turn=%t control=%t", seenSession, seenTurn, seenControl)
	}
}

func TestCodexDispatchProfileUsesDocumentedModelAndEffortFields(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=profile")
		return cmd
	}
	profile, err := dispatchprofile.Resolve("codex-sol-xhigh", "2", AdapterCodex)
	if err != nil {
		t.Fatal(err)
	}
	process, err := adapter.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:test", Adapter: AdapterCodex, ResolvedProfile: &profile}, func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "profile-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexStartCancellationReapsInFlightAppServer(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	var child *exec.Cmd
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		child = exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		child.Env = append(os.Environ(), codexHelperEnvironment+"=block")
		return child
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := adapter.Start(ctx, StartRequest{Workspace: t.TempDir(), Prompt: "work", Identity: "codex:test", Adapter: AdapterCodex}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || child == nil || child.Process == nil || child.ProcessState == nil {
		t.Fatalf("err=%v child=%v", err, child)
	}
	if got, want := child.ProcessState.Pid(), child.Process.Pid; got != want {
		t.Fatalf("reaped pid=%d want exact child pid=%d", got, want)
	}
	// ProcessState is populated by Cmd.Wait. A signal-terminated Unix child is
	// reaped even though ProcessState.Exited reports false; ErrProcessDone from
	// the waited Process handle proves this exact child cannot remain running.
	if signalErr := child.Process.Signal(os.Kill); signalErr != os.ErrProcessDone {
		t.Fatalf("signal after cancellation=%v want %v (state=%v)", signalErr, os.ErrProcessDone, child.ProcessState)
	}
}

func TestCodexDrainsFinalCompletionBeforeReapingAppServer(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=complete-and-exit")
		return cmd
	}
	var eventsMu sync.Mutex
	var events []AdapterEvent
	process, err := adapter.Start(context.Background(), StartRequest{
		Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:drain", Adapter: AdapterCodex,
	}, func(event AdapterEvent) {
		// Keep the pipe measurably backlogged after the child exits. Calling
		// Cmd.Wait concurrently with this callback used to close StdoutPipe and
		// discard the final turn/completed frame.
		if event.Kind == EventToolStarted {
			time.Sleep(time.Millisecond)
		}
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("final completion was not drained before reap: %v", err)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	seenCompleted := false
	for _, event := range events {
		if event.ErrorCode == ErrorEventStreamBound {
			t.Fatalf("normal child exit was misreported as a bounded stream error: %+v", events)
		}
		seenCompleted = seenCompleted || event.Kind == EventTurnCompleted
	}
	if !seenCompleted {
		t.Fatalf("documented turn/completed was not reported: %+v", events)
	}
}

func TestCodexPrematureStreamEOFWaitsForExactChildReap(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	var child *exec.Cmd
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		child = exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		child.Env = append(os.Environ(), codexHelperEnvironment+"=close-stdout-and-sleep")
		return child
	}
	process, err := adapter.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:premature-eof", Adapter: AdapterCodex}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil || (err.Error() != "Codex app-server event stream ended before turn completion" && err.Error() != "Codex app-server exited before turn completion") {
		t.Fatalf("wait error=%v", err)
	}
	if child.ProcessState == nil || child.ProcessState.Pid() != child.Process.Pid {
		t.Fatalf("Wait returned before exact child reap: state=%v pid=%d", child.ProcessState, child.Process.Pid)
	}
	if err := child.Process.Signal(os.Kill); err != os.ErrProcessDone {
		t.Fatalf("signal after Wait=%v want %v", err, os.ErrProcessDone)
	}
}

func TestCodexLiveOwnedAppServerSteer(t *testing.T) {
	if os.Getenv("PAIMOS_AGENTD_LIVE_CODEX") != "1" {
		t.Skip("set PAIMOS_AGENTD_LIVE_CODEX=1 with an authenticated codex CLI to run live proof")
	}
	adapter := NewCodexAdapter("", "live-proof")
	profile, err := dispatchprofile.Resolve("codex-sol-high", dispatchprofile.CatalogVersion, AdapterCodex)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	active := make(chan struct{}, 1)
	process, err := adapter.Start(ctx, StartRequest{
		Workspace: t.TempDir(), Adapter: AdapterCodex, Identity: "codex:live-proof",
		ResolvedProfile: &profile,
		Prompt:          "Run the command sleep 20 in the empty workspace, then say done. This bounded delay verifies live owned control.",
	}, func(event AdapterEvent) {
		if event.Kind == EventToolStarted {
			select {
			case active <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "live-proof-cleanup"})
	})
	select {
	case <-active:
	case <-ctx.Done():
		t.Fatal("live Codex active tool boundary unavailable; model access, quota, or execution did not permit the proof")
	}
	if _, err := process.Steer(ctx, ControlRequest{CorrelationID: "live-proof-delivery", Text: "Acknowledge this steer before finishing."}); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Interrupt(ctx, ControlRequest{CorrelationID: "live-proof-interrupt"}); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexOnlyOwnedToolItemsReportToolActivity(t *testing.T) {
	for _, itemType := range []string{"commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch", "imageView", "sleep", "imageGeneration", "agentMessage", "reasoning", "userMessage", "plan", "functionCallOutput", "hookPrompt", "subAgentActivity", "contextCompaction", "unknown"} {
		t.Run(itemType, func(t *testing.T) {
			var events []AdapterEvent
			p := &codexProcess{threadID: "thread-owned", turnID: "turn-owned", observe: func(e AdapterEvent) { events = append(events, e) }}
			raw, _ := json.Marshal(map[string]any{"threadId": "thread-owned", "turnId": "turn-owned", "item": map[string]any{"id": "item-owned", "type": itemType, "text": "private-message", "arguments": "private-arguments"}})
			p.handleNotification(codexRPCMessage{Method: "item/started", Params: raw})
			want := slices.Contains([]string{"commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch", "imageView", "sleep", "imageGeneration"}, itemType)
			if (len(events) == 1) != want || len(events) > 1 {
				t.Fatalf("tool activity events=%+v want tool=%t", events, want)
			}
			if want && events[0] != (AdapterEvent{Kind: EventToolStarted}) {
				t.Fatalf("content leaked or event changed: %+v", events)
			}
		})
	}
	for _, params := range []string{
		`{"threadId":"foreign-thread","turnId":"turn-owned","item":{"type":"commandExecution","id":"item"}}`,
		`{"threadId":"thread-owned","turnId":"foreign-turn","item":{"type":"commandExecution","id":"item"}}`,
		`{"threadId":"thread-owned","turnId":"turn-owned","item":{"type":"commandExecution"}}`,
		`{"item":`,
	} {
		p := &codexProcess{threadID: "thread-owned", turnID: "turn-owned", observe: func(e AdapterEvent) { t.Errorf("unproven item emitted %+v", e) }}
		p.handleNotification(codexRPCMessage{Method: "item/started", Params: json.RawMessage(params)})
	}
}

func TestCodexForeignCompletionCannotMakeOwnedTurnIdle(t *testing.T) {
	for _, ids := range [][2]string{{"other-thread", "turn-owned"}, {"thread-owned", "other-turn"}} {
		p := &codexProcess{persistent: true, threadID: "thread-owned", turnID: "turn-owned", observe: func(e AdapterEvent) { t.Errorf("foreign completion emitted %+v", e) }}
		raw, _ := json.Marshal(map[string]any{"threadId": ids[0], "turn": map[string]string{"id": ids[1], "status": "completed"}})
		p.handleNotification(codexRPCMessage{Method: "turn/completed", Params: raw})
		if p.InboxReady() || p.turnID != "turn-owned" {
			t.Fatal("foreign completion changed owned turn")
		}
	}
}

func TestCodexEarlyCompletionRequiresReturnedOwnedTurn(t *testing.T) {
	for _, returned := range []string{"turn-early", "different-turn"} {
		var events []AdapterEvent
		p := &codexProcess{persistent: true, threadID: "thread-owned", observe: func(e AdapterEvent) { events = append(events, e) }}
		if !p.beginTurn() {
			t.Fatal("could not begin owned turn")
		}
		p.recordCompletion("thread-owned", "turn-early", "completed")
		if len(events) != 0 || p.InboxReady() {
			t.Fatal("unproved early completion appeared idle")
		}
		p.setTurn(returned)
		if returned == "turn-early" {
			if !p.InboxReady() || len(events) != 1 || events[0].Kind != EventTurnCompleted {
				t.Fatalf("proved completion missing: %+v", events)
			}
		} else if p.InboxReady() || len(events) != 0 {
			t.Fatal("wrong early turn changed current turn")
		}
	}
	var events []AdapterEvent
	p := &codexProcess{persistent: true, threadID: "thread-owned", observe: func(e AdapterEvent) { events = append(events, e) }}
	p.recordCompletion("thread-owned", "stale-idle-turn", "completed")
	if len(events) != 0 || p.earlyCompleted != "" {
		t.Fatal("idle history was treated as pending turn")
	}
}

func TestCodexEarlyToolActivityRequiresReturnedOwnedTurn(t *testing.T) {
	for _, returned := range []string{"turn-early", "different-turn"} {
		var events []AdapterEvent
		p := &codexProcess{persistent: true, threadID: "thread-owned", observe: func(e AdapterEvent) { events = append(events, e) }}
		if !p.beginTurn() {
			t.Fatal("begin failed")
		}
		for _, turn := range []string{"turn-early", "foreign-turn"} {
			raw, _ := json.Marshal(map[string]any{"threadId": "thread-owned", "turnId": turn, "item": map[string]string{"id": "item", "type": "commandExecution"}})
			p.handleNotification(codexRPCMessage{Method: "item/started", Params: raw})
		}
		if len(events) != 0 {
			t.Fatal("early tool was not yet proved by RPC")
		}
		p.setTurn(returned)
		if returned == "turn-early" {
			if len(events) != 1 || events[0].Kind != EventToolStarted {
				t.Fatalf("proved early tool lost: %+v", events)
			}
		} else if len(events) != 0 {
			t.Fatal("foreign early tool emitted activity")
		}
	}
}

func TestCodexAmbiguousInboxStartClosesInsteadOfReopeningInbox(t *testing.T) {
	for _, mode := range []string{"persistent-timeout", "persistent-invalid"} {
		t.Run(mode, func(t *testing.T) {
			adapter := NewCodexAdapter(os.Args[0], "test")
			adapter.command = func(string, ...string) *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
				cmd.Env = append(os.Environ(), codexHelperEnvironment+"="+mode)
				return cmd
			}
			events := make(chan AdapterEvent, 32)
			p, err := adapter.Start(context.Background(), StartRequest{KeepAlive: true, Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:ambiguous"}, func(e AdapterEvent) { events <- e })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = p.Stop(context.Background(), ControlRequest{CorrelationID: "ambiguous-cleanup"}) })
			ready := p.(interface{ InboxReady() bool })
			deadline := time.Now().Add(3 * time.Second)
			for !ready.InboxReady() {
				if time.Now().After(deadline) {
					t.Fatal("initial turn never idle")
				}
				time.Sleep(time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			inbox := p.(InboxProcess)
			if _, err = inbox.Inbox(ctx, ControlRequest{CorrelationID: "ambiguous", Text: "fixture-next-input"}); err == nil {
				t.Fatal("ambiguous start succeeded")
			}
			if ready.InboxReady() {
				t.Fatal("ambiguous accepted turn reopened inbox")
			}
			if _, err = inbox.Inbox(context.Background(), ControlRequest{CorrelationID: "must-not-retry", Text: "no extra vendor input"}); !errors.Is(err, ErrCapabilityMissing) {
				t.Fatalf("subsequent inbox not fenced: %v", err)
			}
			seen := false
			for len(events) > 0 {
				e := <-events
				if e.ErrorCode == ErrorAppServerProtocol {
					seen = true
				}
			}
			if !seen {
				t.Fatal("finite ambiguity evidence missing")
			}
			waited := make(chan error, 1)
			go func() { waited <- p.Wait() }()
			select {
			case err := <-waited:
				if err == nil {
					t.Fatal("ambiguous process Wait succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("ambiguous owned process not reaped")
			}
			if !errors.Is(syscall.Kill(-p.PID(), 0), syscall.ESRCH) {
				t.Fatal("ambiguous owned group remains")
			}
		})
	}
}

func TestCodexPersistentTerminalFailureClosesExactOwnedGroup(t *testing.T) {
	for _, tc := range []struct {
		status string
		code   ErrorCode
	}{{"completed", ""}, {"interrupted", ""}, {"failed", ErrorTurnFailed}, {"unknown-status", ErrorAppServerProtocol}} {
		t.Run(tc.status, func(t *testing.T) {
			adapter := NewCodexAdapter(os.Args[0], "test")
			adapter.command = func(string, ...string) *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
				cmd.Env = append(os.Environ(), codexHelperEnvironment+"=terminal-"+tc.status)
				return cmd
			}
			events := make(chan AdapterEvent, 16)
			p, err := adapter.Start(context.Background(), StartRequest{KeepAlive: true, Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:terminal"}, func(e AdapterEvent) { events <- e })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = p.Stop(context.Background(), ControlRequest{CorrelationID: "terminal-cleanup"}) })
			deadline := time.After(3 * time.Second)
			var terminal AdapterEvent
		waitTerminal:
			for {
				select {
				case e := <-events:
					if e.Kind == EventTurnCompleted || e.ErrorCode != "" {
						terminal = e
						break waitTerminal
					}
				case <-deadline:
					t.Fatal("missing terminal evidence")
				}
			}
			if terminal.ErrorCode != tc.code {
				t.Fatalf("terminal=%+v want %s", terminal, tc.code)
			}
			if tc.code == "" {
				if terminal.Kind != EventTurnCompleted || !p.(interface{ InboxReady() bool }).InboxReady() {
					t.Fatalf("legitimate completion not reusable: %+v", terminal)
				}
				return
			}
			if terminal.Kind != "" || p.(interface{ InboxReady() bool }).InboxReady() {
				t.Fatalf("failed completion falsely idle: %+v", terminal)
			}
			waited := make(chan error, 1)
			go func() { waited <- p.Wait() }()
			select {
			case err := <-waited:
				if err == nil {
					t.Fatal("failed turn Wait succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("failed owned process not reaped")
			}
			end := time.Now().Add(time.Second)
			for !errors.Is(syscall.Kill(-p.PID(), 0), syscall.ESRCH) {
				if time.Now().After(end) {
					t.Fatal("exact owned group remains")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestCodexFailureReachesSupervisorTerminalReporterSnapshot(t *testing.T) {
	adapter := NewCodexAdapter(os.Args[0], "test")
	adapter.command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=terminal-failed")
		return cmd
	}
	reporter := &fakeReporter{reports: make(chan Status, 64)}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "codex-failure", StateRoot: t.TempDir(), Adapters: []Adapter{codexProtocolTestAdapter{adapter}}, Reporter: reporter, HeartbeatInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:worker", ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case status := <-reporter.reports:
			for _, got := range status.Sessions {
				if got.ID != session.ID || got.State != StateFailed {
					continue
				}
				if got.PID != 0 || got.ExitedAt == nil || got.LastErrorCode != ErrorTurnFailed || got.LastEventKind == EventTurnCompleted {
					t.Fatalf("failed turn not terminal: %+v", got)
				}
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), "private vendor") || strings.Contains(string(raw), "secret-not-persisted") {
					t.Fatal("vendor content escaped finite failure evidence")
				}
				return
			}
		case <-deadline:
			t.Fatal("failed turn never reached terminal reporter snapshot")
		}
	}
}

func TestCodexAppServerHelperStdoutContainsOnlyProtocolFrames(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
	cmd.Env = append(os.Environ(), codexHelperEnvironment+"=terminal-failed")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	requests := []map[string]any{
		{"id": 1, "method": "initialize", "params": map[string]any{}},
		{"method": "initialized", "params": map[string]any{}},
		{"id": 2, "method": "thread/start", "params": map[string]any{}},
		{"id": 3, "method": "turn/start", "params": map[string]any{
			"threadId": "thread-owned", "input": []map[string]string{{"text": "secret-not-persisted"}},
		}},
	}
	encoder := json.NewEncoder(stdin)
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	frames := 0
	for scanner.Scan() {
		frames++
		if !json.Valid(scanner.Bytes()) {
			t.Fatalf("helper emitted non-protocol stdout frame %q", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if frames != 6 {
		t.Fatalf("helper protocol frames=%d want=6", frames)
	}
}

func helperCodexAdapter(t *testing.T, mode string, registry CodexAccountRegistry) *CodexAdapter {
	t.Helper()
	adapter := NewCodexAdapter(os.Args[0], "test")
	adapter.SetAccounts(registry)
	adapter.command = func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAppServerHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"="+mode)
		return cmd
	}
	return adapter
}

func TestCodexNamedAccountsUseDistinctHomesAndRefuseWrongOrMissingIdentity(t *testing.T) {
	homeOne := writeCodexHome(t, "one@example.invalid")
	homeTwo := writeCodexHome(t, "two@example.invalid")
	registry := testCodexRegistry(t,
		codexAccountRegistryEntry{Key: "one", Home: homeOne, Email: "one@example.invalid"},
		codexAccountRegistryEntry{Key: "two", Home: homeTwo, Email: "two@example.invalid"},
	)
	adapter := helperCodexAdapter(t, "serve", registry)
	first, err := adapter.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:one", Adapter: AdapterCodex, AccountKey: "one"}, func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = first.Stop(context.Background(), ControlRequest{CorrelationID: "one-stop"}) })
	second, err := adapter.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:two", Adapter: AdapterCodex, AccountKey: "two"}, func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = second.Stop(context.Background(), ControlRequest{CorrelationID: "two-stop"}) })
	if first.PID() == second.PID() {
		t.Fatal("two accounts shared one child")
	}
	key, label := first.(accountSelection).AccountSelection()
	if key != "one" || label != "chatgpt" {
		t.Fatalf("first selection=%s %s", key, label)
	}
	if _, err := os.Stat(filepath.Join(homeOne, "spawned")); err != nil {
		t.Fatal("first account did not spawn in its home")
	}
	if _, err := os.Stat(filepath.Join(homeTwo, "spawned")); err != nil {
		t.Fatal("second account did not spawn in its home")
	}
	wrong := helperCodexAdapter(t, "account-wrong", registry)
	if _, err := wrong.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:one", Adapter: AdapterCodex, AccountKey: "one"}, nil); err == nil || strings.Contains(err.Error(), "@") {
		t.Fatalf("wrong identity started a model turn or leaked identity: %v", err)
	}
	missing := helperCodexAdapter(t, "account-missing", registry)
	if _, err := missing.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:one", Adapter: AdapterCodex, AccountKey: "one"}, nil); err == nil {
		t.Fatal("unavailable account/read started a model turn")
	}
	unavailable := helperCodexAdapter(t, "account-unavailable", registry)
	if _, err := unavailable.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:one", Adapter: AdapterCodex, AccountKey: "one"}, nil); err == nil {
		t.Fatal("failed account/read started a model turn")
	}
	legacy, err := helperCodexAdapter(t, "serve", CodexAccountRegistry{}).Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:legacy", Adapter: AdapterCodex}, func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = legacy.Stop(context.Background(), ControlRequest{CorrelationID: "legacy-stop"}) })
	if key, label := legacy.(accountSelection).AccountSelection(); key != "" || label != "" {
		t.Fatalf("legacy start claimed named-account verification: %s %s", key, label)
	}
}

func TestCodexNamedAccountFreezesHomeForSteerAndCleanup(t *testing.T) {
	home := writeCodexHome(t, "frozen@example.invalid")
	registry := testCodexRegistry(t, codexAccountRegistryEntry{Key: "frozen", Home: home, Email: "frozen@example.invalid"})
	adapter := helperCodexAdapter(t, "serve", registry)
	process, err := adapter.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:frozen", Adapter: AdapterCodex, AccountKey: "frozen"}, func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	steer, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "frozen-steer", Text: "new direction"})
	if err != nil || steer.VendorMessageID != "turn-owned" {
		t.Fatalf("steer=%+v err=%v", steer, err)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "frozen-stop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "spawned")); err != nil {
		t.Fatal("frozen account left its home unused")
	}
}

func TestCodexAppServerHelperProcess(t *testing.T) {
	mode := os.Getenv(codexHelperEnvironment)
	if mode == "" {
		t.Skip("helper process")
	}
	if mode == "block" {
		time.Sleep(time.Hour)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		respond := func(result any) {
			if encoder.Encode(map[string]any{"id": request.ID, "result": result}) != nil {
				os.Exit(2)
			}
		}
		switch request.Method {
		case "initialized":
			continue
		case "initialize":
			respond(map[string]any{})
		case "account/read":
			if mode == "account-unavailable" {
				os.Exit(2)
			}
			var params struct {
				RefreshToken *bool `json:"refreshToken"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.RefreshToken == nil || *params.RefreshToken {
				os.Exit(2)
			}
			home := os.Getenv("CODEX_HOME")
			if home != "" {
				_ = os.WriteFile(filepath.Join(home, "spawned"), []byte("1"), 0o600)
			}
			if mode == "account-missing" {
				respond(map[string]any{"requiresOpenaiAuth": true, "account": nil})
				continue
			}
			email := "fixture@example.invalid"
			if home != "" {
				if raw, err := os.ReadFile(filepath.Join(home, "paimos-fixture-identity")); err == nil {
					email = strings.TrimSpace(string(raw))
				}
			}
			if mode == "account-wrong" {
				email = "wrong@example.invalid"
			}
			if email == "" {
				respond(map[string]any{"requiresOpenaiAuth": true, "account": map[string]any{"type": "chatgpt", "email": nil, "planType": "plus"}})
				continue
			}
			respond(map[string]any{"requiresOpenaiAuth": true, "account": map[string]any{"type": "chatgpt", "email": email, "planType": "plus"}})
		case "thread/start":
			if mode == "profile" {
				var params struct {
					Model string `json:"model"`
				}
				if json.Unmarshal(request.Params, &params) != nil || params.Model != "gpt-6-sol" {
					os.Exit(2)
				}
			}
			respond(map[string]any{"thread": map[string]any{"id": "thread-owned"}})
			_ = encoder.Encode(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]any{"id": "thread-owned"}}})
		case "turn/start":
			var params struct {
				ThreadID string `json:"threadId"`
				Model    string `json:"model"`
				Effort   string `json:"effort"`
				Input    []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if strings.HasPrefix(mode, "persistent") {
				if json.Unmarshal(request.Params, &params) != nil || params.ThreadID != "thread-owned" || len(params.Input) != 1 {
					os.Exit(2)
				}
				if params.Input[0].Text == "secret-not-persisted" {
					respond(map[string]any{"turn": map[string]any{"id": "turn-owned", "status": "inProgress"}})
					_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": "completed"}}})
				} else if params.Input[0].Text == "fixture-next-input" {
					if mode == "persistent-timeout" {
						continue
					}
					if mode == "persistent-invalid" {
						respond(map[string]any{"turn": map[string]string{"id": "turn-next", "status": "invalid-terminal-status"}})
						continue
					}
					respond(map[string]any{"turn": map[string]any{"id": "turn-next", "status": "inProgress"}})
				} else {
					os.Exit(2)
				}
				continue
			}
			if json.Unmarshal(request.Params, &params) != nil || params.ThreadID != "thread-owned" || len(params.Input) != 1 || params.Input[0].Text != "secret-not-persisted" {
				fmt.Fprintln(os.Stderr, "invalid turn/start")
				os.Exit(2)
			}
			if mode == "profile" && (params.Model != "gpt-6-sol" || params.Effort != "xhigh") {
				os.Exit(2)
			}
			respond(map[string]any{"turn": map[string]any{"id": "turn-owned", "status": "inProgress"}})
			_ = encoder.Encode(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": "inProgress"}}})
			if mode == "close-stdout-and-sleep" {
				_ = os.Stdout.Close()
				time.Sleep(time.Hour)
			}
			if mode == "complete-and-exit" {
				for range 48 {
					_ = encoder.Encode(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread-owned", "turnId": "turn-owned", "item": map[string]any{"id": "item-owned", "type": "commandExecution"}}})
				}
				_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": "completed"}}})
				os.Exit(0)
			}
			if strings.HasPrefix(mode, "terminal-") {
				_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": strings.TrimPrefix(mode, "terminal-"), "error": map[string]string{"message": "private vendor error must never surface"}}}})
			}
		case "turn/steer":
			respond(map[string]any{"turnId": "turn-owned"})
		case "turn/interrupt":
			respond(map[string]any{})
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-owned", "turn": map[string]any{"id": "turn-owned", "status": "interrupted"}}})
		default:
			os.Exit(2)
		}
	}
	// Returning lets the Go test harness append its plain-text PASS marker to
	// stdout, which is the helper's JSON-RPC transport in these subprocesses.
	os.Exit(0)
}
