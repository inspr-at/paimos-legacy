//go:build !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

const claudeBridgeHelperEnvironment = "PAIMOS_CLAUDE_BRIDGE_HELPER"

func newTestClaudeAdapter(t *testing.T, node string) *ClaudeAdapter {
	t.Helper()
	sdkPath := filepath.Join(t.TempDir(), "sdk.mjs")
	if err := os.WriteFile(sdkPath, []byte(fakeClaudeSDKModule), 0600); err != nil {
		t.Fatal(err)
	}
	adapter := NewClaudeAdapter(os.Args[0], node, sdkPath)
	adapter.claudeVersion = func(context.Context, string) (string, error) { return claudeMinimumCLIVersion, nil }
	adapter.validateSDK = func(string) (string, error) { return sdkPath, nil }
	return adapter
}

func TestClaudeProcessBindsSteerAndInterruptToOneLiveQuery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	logPath := t.TempDir() + "/sdk-events.log"
	t.Setenv("PAIMOS_CLAUDE_TEST_LOG", logPath)
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "hold_initial_turn")
	adapter := newTestClaudeAdapter(t, node)
	events := make(chan AdapterEvent, 32)
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test",
		Prompt: "alpha content must not escape",
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	claude, ok := process.(*claudeProcess)
	if !ok || process.PID() <= 0 {
		t.Fatalf("process=%T pid=%d", process, process.PID())
	}
	runtimeDir := claude.runtimeDir

	interrupt, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "control-live-850"})
	if err != nil {
		t.Fatal(err)
	}
	if interrupt.Primitive != claudeInterruptPrimitive || interrupt.CorrelationID != "control-live-850" {
		t.Fatalf("interrupt=%+v", interrupt)
	}
	steer, err := process.Steer(context.Background(), ControlRequest{
		CorrelationID: "delivery-live-850", Text: "beta direction must not escape",
	})
	if err != nil {
		t.Fatal(err)
	}
	if steer.Primitive != claudeSteerPrimitive || steer.CorrelationID != "delivery-live-850" || steer.VendorMessageID == "" {
		t.Fatalf("steer=%+v", steer)
	}

	deadline := time.After(2 * time.Second)
	seenReaction := false
	var observed []AdapterEvent
	for !seenReaction {
		select {
		case event := <-events:
			observed = append(observed, event)
			seenReaction = event.Kind == EventTurnStarted && event.CorrelationID == "delivery-live-850"
		case <-deadline:
			t.Fatal("steered turn did not react on the live Query")
		}
	}
	stop, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "stop-live-850"})
	if err != nil {
		t.Fatal(err)
	}
	if stop.Primitive != claudeStopPrimitive || stop.CorrelationID != "stop-live-850" {
		t.Fatalf("stop=%+v", stop)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	for len(events) > 0 {
		observed = append(observed, <-events)
	}
	evidence, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("ephemeral SDK runtime was not removed: %v", err)
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBody)
	if strings.Count(logText, "query=owned-query") < 3 || !strings.Contains(logText, "close query=owned-query") {
		t.Fatalf("same live Query evidence missing: %q", logText)
	}
	for _, privateText := range []string{"alpha content must not escape", "beta direction must not escape"} {
		if strings.Contains(logText, privateText) || strings.Contains(string(evidence), privateText) {
			t.Fatalf("content leaked into evidence log: %q", privateText)
		}
	}
}

func TestClaudeInitModelEvidenceIsBoundedAndTruthful(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	profile, err := dispatchprofile.Resolve("claude-opus-xhigh", "2", AdapterClaude)
	if err != nil {
		t.Fatal(err)
	}
	exactProfile := profile
	exactProfile.ID = "claude-opus-fixture-exact"
	exactProfile.Version = "fixture-1"
	exactProfile.Model = "claude-opus-fixture-20260909"

	t.Run("exact claim matches vendor identity", func(t *testing.T) {
		adapter := newTestClaudeAdapter(t, node)
		process, err := adapter.Start(context.Background(), StartRequest{
			Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:model-exact",
			Prompt: "private input", ResolvedProfile: &exactProfile,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer process.Stop(context.Background(), ControlRequest{CorrelationID: "model-exact-cleanup"})
	})

	for name, mode := range map[string]string{"mismatch": "", "missing": "missing_model"} {
		t.Run("exact claim "+name+" fails closed", func(t *testing.T) {
			if mode != "" {
				t.Setenv("PAIMOS_CLAUDE_TEST_MODE", mode)
			}
			mismatched := exactProfile
			if name == "mismatch" {
				mismatched.Model = "claude-sonnet-fixture-20260909"
			}
			adapter := newTestClaudeAdapter(t, node)
			if _, err := adapter.Start(context.Background(), StartRequest{
				Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:model-exact-invalid",
				Prompt: "private input", ResolvedProfile: &mismatched,
			}, nil); err == nil {
				t.Fatal("unverified exact model claim started an owned session")
			}
		})
	}

	t.Run("vendor reported identity stays distinct from requested alias", func(t *testing.T) {
		adapter := newTestClaudeAdapter(t, node)
		events := make(chan AdapterEvent, 16)
		process, err := adapter.Start(context.Background(), StartRequest{
			Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:model-evidence",
			Prompt: "private input", ResolvedProfile: &profile,
		}, func(event AdapterEvent) { events <- event })
		if err != nil {
			t.Fatal(err)
		}
		defer process.Stop(context.Background(), ControlRequest{CorrelationID: "model-evidence-cleanup"})
		var started AdapterEvent
		for len(events) > 0 {
			event := <-events
			if event.Kind == EventSessionStarted {
				started = event
			}
		}
		if started.HarnessSessionID != "claude-owned-session" || started.EffectiveModel != "claude-opus-fixture-20260909" ||
			started.ModelEvidence != ModelEvidenceVendorReported {
			t.Fatalf("session evidence=%+v", started)
		}
		if started.EffectiveModel == profile.Model {
			t.Fatal("requested alias was presented as the effective vendor model")
		}
	})

	t.Run("missing identity is explicitly unverified", func(t *testing.T) {
		t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "missing_model")
		adapter := newTestClaudeAdapter(t, node)
		events := make(chan AdapterEvent, 16)
		process, err := adapter.Start(context.Background(), StartRequest{
			Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:model-missing", Prompt: "private input",
		}, func(event AdapterEvent) { events <- event })
		if err != nil {
			t.Fatal(err)
		}
		defer process.Stop(context.Background(), ControlRequest{CorrelationID: "model-missing-cleanup"})
		var started AdapterEvent
		for len(events) > 0 {
			event := <-events
			if event.Kind == EventSessionStarted {
				started = event
			}
		}
		if started.HarnessSessionID != "claude-owned-session" || started.EffectiveModel != "" ||
			started.ModelEvidence != ModelEvidenceUnverified {
			t.Fatalf("session evidence=%+v", started)
		}
	})

	t.Run("identical repeated init remains one observation", func(t *testing.T) {
		t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "repeated_init")
		adapter := newTestClaudeAdapter(t, node)
		events := make(chan AdapterEvent, 16)
		process, err := adapter.Start(context.Background(), StartRequest{
			Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:model-repeat", Prompt: "private input",
		}, func(event AdapterEvent) { events <- event })
		if err != nil {
			t.Fatal(err)
		}
		defer process.Stop(context.Background(), ControlRequest{CorrelationID: "model-repeat-cleanup"})
		count := 0
		for len(events) > 0 {
			if event := <-events; event.Kind == EventSessionStarted {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("session evidence observations=%d, want 1", count)
		}
	})

	for _, mode := range []string{"malformed_model", "changed_model", "foreign_session"} {
		t.Run(mode+" fails closed", func(t *testing.T) {
			t.Setenv("PAIMOS_CLAUDE_TEST_MODE", mode)
			adapter := newTestClaudeAdapter(t, node)
			process, err := adapter.Start(context.Background(), StartRequest{
				Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:model-invalid", Prompt: "private input",
			}, nil)
			if err == nil {
				// The SDK emits init only after accepting the first input, and Start
				// may observe the first valid init before a conflicting repeat. The
				// owned bridge must still terminate rather than retaining authority.
				if waitErr := process.Wait(); waitErr == nil {
					t.Fatal("conflicting init evidence left the owned session running")
				}
			}
		})
	}
}

func TestClaudeSupervisorReturnsUnverifiedMissingModelEvidence(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "missing_model")
	profile, err := dispatchprofile.Resolve("claude-opus-xhigh", "2", AdapterClaude)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(SupervisorConfig{
		Adapters: []Adapter{newTestClaudeAdapter(t, node)}, StateRoot: t.TempDir(), Instance: "ppm-claude-model-missing",
		DispatchResolver: &profileResolver{profile: profile},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:model-missing", ProjectID: 984,
		Prompt: "private input", DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ModelEvidence == nil || session.ModelEvidence.RequestedModel != "opus" ||
		session.ModelEvidence.EffectiveModel != "" || session.ModelEvidence.Status != ModelEvidenceUnverified ||
		session.ModelEvidence.HarnessSessionID != session.HarnessSessionID {
		t.Fatalf("model evidence=%+v harness_session_id=%q", session.ModelEvidence, session.HarnessSessionID)
	}
	status := supervisor.Status()
	if len(status.Sessions) != 1 || status.Sessions[0].ModelEvidence == nil ||
		*status.Sessions[0].ModelEvidence != *session.ModelEvidence {
		t.Fatalf("status sessions=%+v", status.Sessions)
	}
}

func TestClaudeInterruptRejectsIdleQueryWithoutAppliedActivity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	logPath := t.TempDir() + "/sdk-events.log"
	t.Setenv("PAIMOS_CLAUDE_TEST_LOG", logPath)
	adapter := newTestClaudeAdapter(t, node)
	events := make(chan AdapterEvent, 16)
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "finish",
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == EventTurnCompleted {
				goto idle
			}
		case <-deadline:
			t.Fatal("initial Query did not become idle")
		}
	}

idle:
	if _, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "idle-interrupt"}); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("idle interrupt err=%v", err)
	}
	for len(events) > 0 {
		event := <-events
		if event.Kind == EventControlApplied && event.CorrelationID == "idle-interrupt" {
			t.Fatal("idle interrupt emitted applied activity")
		}
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "idle-stop"}); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(logPath); err != nil || strings.Contains(string(body), "interrupt query=owned-query") {
		t.Fatalf("idle interrupt reached SDK: body=%q err=%v", body, err)
	}
}

func TestClaudeInterruptRejectsTurnThatCompletesBeforeReceipt(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	logPath := t.TempDir() + "/sdk-events.log"
	t.Setenv("PAIMOS_CLAUDE_TEST_LOG", logPath)
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "complete_before_interrupt_receipt")
	adapter := newTestClaudeAdapter(t, node)
	events := make(chan AdapterEvent, 16)
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "finish during interrupt",
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}

	if _, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "raced-interrupt"}); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("interrupt completed after turn err=%v", err)
	}
	seenCompleted := false
	for len(events) > 0 {
		event := <-events
		if event.Kind == EventTurnCompleted {
			seenCompleted = true
		}
		if event.Kind == EventControlApplied && event.CorrelationID == "raced-interrupt" {
			t.Fatal("interrupt completed after turn emitted applied activity")
		}
	}
	if !seenCompleted {
		t.Fatal("turn completion was not observed before interrupt rejection")
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "raced-stop"}); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(body), "interrupt query=owned-query") {
		t.Fatalf("active interrupt did not reach SDK before completion: body=%q err=%v", body, err)
	}
}

func TestClaudeInterruptAcceptsSubsequentActivityWithoutReactionUUID(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "subsequent_activity_without_reaction")
	adapter := newTestClaudeAdapter(t, node)
	events := make(chan AdapterEvent, 16)
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "initial turn",
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	seenCompleted := false
	for {
		select {
		case event := <-events:
			seenCompleted = seenCompleted || event.Kind == EventTurnCompleted
			if seenCompleted && event.Kind == EventToolStarted {
				goto active
			}
		case <-deadline:
			t.Fatal("subsequent uncorrelated vendor activity was not observed")
		}
	}

active:
	interrupt, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "subsequent-interrupt"})
	if err != nil {
		t.Fatalf("interrupt subsequent uncorrelated activity: %v", err)
	}
	if interrupt.Primitive != claudeInterruptPrimitive || interrupt.CorrelationID != "subsequent-interrupt" {
		t.Fatalf("interrupt=%+v", interrupt)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "subsequent-stop"}); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAdapterFailsClosedWithoutDocumentedRuntime(t *testing.T) {
	adapter := NewClaudeAdapter("/missing/operator-claude", "/missing/node", "/missing/sdk.mjs")
	_, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "Node.js") {
		t.Fatalf("err=%v", err)
	}
}

func TestClaudeStartCancellationReapsBridgeAndRemovesRuntime(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "block_init")
	adapter := newTestClaudeAdapter(t, node)
	var child *exec.Cmd
	adapter.command = func(name string, args ...string) *exec.Cmd {
		child = exec.Command(name, args...)
		return child
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = adapter.Start(ctx, StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "startup private content",
	}, nil)
	if err == nil || child == nil || child.ProcessState == nil {
		t.Fatalf("err=%v child=%v state=%v", err, child, child.ProcessState)
	}
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("start failure cleanup held the supervisor start lock for %s", elapsed)
	}
	if len(child.Args) < 2 {
		t.Fatalf("child args=%q", child.Args)
	}
	if _, statErr := os.Stat(filepath.Dir(child.Args[1])); !os.IsNotExist(statErr) {
		t.Fatalf("startup runtime was not removed: %v", statErr)
	}
	if slices.Contains(child.Args, "startup private content") {
		t.Fatalf("prompt leaked into process arguments: %q", child.Args)
	}
}

func TestClaudeAdapterRejectsOldNodeBeforeSpawning(t *testing.T) {
	adapter := NewClaudeAdapter(os.Args[0], os.Args[0], "/missing/sdk.mjs")
	adapter.nodeMajor = func(context.Context, string) (int, error) { return 17, nil }
	adapter.command = func(string, ...string) *exec.Cmd {
		t.Fatal("old Node runtime must fail before spawn")
		return nil
	}
	_, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "Node.js >=18") {
		t.Fatalf("err=%v", err)
	}
}

func TestClaudeAdapterRejectsOldCLIAndMissingExplicitSDK(t *testing.T) {
	oldCLI := NewClaudeAdapter(os.Args[0], os.Args[0], "/missing/sdk.mjs")
	oldCLI.nodeMajor = func(context.Context, string) (int, error) { return 24, nil }
	oldCLI.claudeVersion = func(context.Context, string) (string, error) { return "2.1.250", nil }
	oldCLI.validateSDK = func(string) (string, error) {
		t.Fatal("old Claude CLI must fail before SDK validation")
		return "", nil
	}
	if _, err := oldCLI.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil); err == nil || !strings.Contains(err.Error(), "Claude CLI >=2.1.251") {
		t.Fatalf("old CLI err=%v", err)
	}

	missingSDK := NewClaudeAdapter(os.Args[0], os.Args[0], "")
	missingSDK.nodeMajor = func(context.Context, string) (int, error) { return 24, nil }
	missingSDK.claudeVersion = func(context.Context, string) (string, error) { return claudeMinimumCLIVersion, nil }
	if _, err := missingSDK.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil); err == nil || !strings.Contains(err.Error(), "--claude-sdk-path") || !strings.Contains(err.Error(), "@anthropic-ai/claude-agent-sdk@0.3.251") {
		t.Fatalf("missing SDK err=%v", err)
	}
}

func TestClaudeStartReportsMissingInterruptReceiptCapability(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "missing_interrupt_receipt")
	adapter := newTestClaudeAdapter(t, node)
	_, err = adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "interrupt_receipt_v1") {
		t.Fatalf("err=%v", err)
	}
}

func TestClaudeAdapterRejectsMissingCLIAndCorruptEmbeddedSDK(t *testing.T) {
	missing := NewClaudeAdapter("/missing/operator-claude", os.Args[0], "/missing/sdk.mjs")
	missing.nodeMajor = func(context.Context, string) (int, error) { return 24, nil }
	if _, err := missing.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil); err == nil || !strings.Contains(err.Error(), "operator-authenticated Claude CLI") {
		t.Fatalf("missing CLI err=%v", err)
	}

	corruptSDK := filepath.Join(t.TempDir(), "sdk.mjs")
	if err := os.WriteFile(corruptSDK, []byte("not the pinned SDK"), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := NewClaudeAdapter(os.Args[0], os.Args[0], corruptSDK)
	corrupt.nodeMajor = func(context.Context, string) (int, error) { return 24, nil }
	corrupt.claudeVersion = func(context.Context, string) (string, error) { return claudeMinimumCLIVersion, nil }
	corrupt.command = func(string, ...string) *exec.Cmd {
		t.Fatal("corrupt SDK must fail before spawn")
		return nil
	}
	if _, err := corrupt.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "work",
	}, nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("corrupt SDK err=%v", err)
	}
}

func TestClaudeBridgeBoundsEventsAndReapsProtocolFailure(t *testing.T) {
	adapter := newTestClaudeAdapter(t, os.Args[0])
	adapter.nodeMajor = func(context.Context, string) (int, error) { return 24, nil }
	var child *exec.Cmd
	adapter.command = func(string, ...string) *exec.Cmd {
		child = exec.Command(os.Args[0], "-test.run=^TestClaudeBridgeHelperProcess$")
		child.Env = append(os.Environ(), claudeBridgeHelperEnvironment+"=oversize")
		return child
	}
	events := make(chan AdapterEvent, 4)
	_, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "private bounded input",
	}, func(event AdapterEvent) { events <- event })
	if err == nil || child == nil || child.ProcessState == nil {
		t.Fatalf("err=%v child=%v state=%v", err, child, child.ProcessState)
	}
	select {
	case event := <-events:
		if event.ErrorCode != ErrorEventStreamBound || event.HarnessSessionID != "" || event.CorrelationID != "" {
			t.Fatalf("event=%+v", event)
		}
	default:
		t.Fatal("bounded protocol failure emitted no content-free event")
	}
}

func TestClaudeAdapterAcceptsFullProcessContractPromptBound(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	adapter := newTestClaudeAdapter(t, node)
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test",
		Prompt: strings.Repeat("\x01", maxPromptBytes),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "full-prompt-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeBridgeHelperProcess(t *testing.T) {
	if os.Getenv(claudeBridgeHelperEnvironment) != "oversize" {
		t.Skip("helper process")
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", maxClaudeBridgeEventFrame+1))
	select {}
}

func TestClaudeSupervisorRestartLosesOwnershipWithoutPersistingContent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	adapter := newTestClaudeAdapter(t, node)
	profile, err := dispatchprofile.Resolve("claude-opus-xhigh", "2", AdapterClaude)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &profileResolver{profile: profile}
	root := t.TempDir()
	first, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{adapter}, StateRoot: root, Instance: "ppm-claude-850", DispatchResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close(context.Background()) })
	session, err := first.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:owned",
		Prompt: "private initial words 850", ProjectID: 850,
		DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || session.ModelEvidence == nil || session.ModelEvidence.RequestedModel != "opus" ||
		session.ModelEvidence.EffectiveModel != "claude-opus-fixture-20260909" ||
		session.ModelEvidence.Status != ModelEvidenceVendorReported ||
		session.ModelEvidence.HarnessSessionID != session.HarnessSessionID {
		t.Fatalf("model evidence=%+v harness_session_id=%q resolver_calls=%d", session.ModelEvidence, session.HarnessSessionID, resolver.calls)
	}
	durable := session
	durable.PID = 0
	durable.Steerable = false
	if err := validateRegistryRecord(registryRecord{Session: durable}); err != nil {
		t.Fatalf("valid model evidence record: %v", err)
	}
	legacy := durable
	legacy.ModelEvidence = nil
	if err := validateRegistryRecord(registryRecord{Session: legacy}); err != nil {
		t.Fatalf("historical record without model evidence: %v", err)
	}
	for name, mutate := range map[string]func(*ModelEvidence){
		"foreign session":     func(evidence *ModelEvidence) { evidence.HarnessSessionID = "foreign-session" },
		"requested mismatch":  func(evidence *ModelEvidence) { evidence.RequestedModel = "sonnet" },
		"malformed effective": func(evidence *ModelEvidence) { evidence.EffectiveModel = "private model text" },
		"false unverified":    func(evidence *ModelEvidence) { evidence.Status = ModelEvidenceUnverified },
	} {
		t.Run("journal rejects "+name, func(t *testing.T) {
			candidate := durable
			evidence := *durable.ModelEvidence
			mutate(&evidence)
			candidate.ModelEvidence = &evidence
			if err := validateRegistryRecord(registryRecord{Session: candidate}); err == nil {
				t.Fatalf("accepted model evidence=%+v", evidence)
			}
		})
	}
	receipt, err := first.Steer(context.Background(), session.ID,
		scopedControl(first, session, "delivery-durable-850", "private steering words 850"))
	if err != nil || receipt.VendorMessageID == "" || receipt.CorrelationID != "delivery-durable-850" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	statusJSON, err := json.Marshal(first.Status())
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := InstanceStateDir(root, "ppm-claude-850")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join(stateDir, "sessions.journal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{statusJSON, journal} {
		for _, forbidden := range []string{"private initial words 850", "private steering words 850", "\"prompt\"", "\"pid\""} {
			if string(body) == string(statusJSON) && forbidden == "\"pid\"" {
				continue
			}
			if strings.Contains(strings.ToLower(string(body)), strings.ToLower(forbidden)) {
				t.Fatalf("content leaked into status/journal: %q", forbidden)
			}
		}
	}
	second, err := NewSupervisor(SupervisorConfig{Adapters: []Adapter{adapter}, StateRoot: root, Instance: "ppm-claude-850"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close(context.Background()) })
	recovered := second.Status().Sessions
	if len(recovered) != 1 || recovered[0].ID != session.ID || recovered[0].State != StateOwnershipLost || recovered[0].Steerable || recovered[0].PID != 0 {
		t.Fatalf("recovered=%+v", recovered)
	}
	if recovered[0].ModelEvidence == nil || *recovered[0].ModelEvidence != *session.ModelEvidence {
		t.Fatalf("recovered model evidence=%+v want=%+v", recovered[0].ModelEvidence, session.ModelEvidence)
	}
	if _, err := second.Steer(context.Background(), session.ID, scopedControl(second, recovered[0], "delivery-after-restart", "must-fail")); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("restart steer error=%v", err)
	}
	if _, err := first.Stop(context.Background(), session.ID, scopedControl(first, session, "cleanup-original-owner", "")); err != nil {
		t.Fatal(err)
	}
	// Match t.Cleanup's LIFO order explicitly, then prove Close is a complete
	// terminal-persistence barrier before TempDir removes the shared StateRoot.
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entry, err := first.get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entry.monitorDone:
	default:
		t.Fatal("Supervisor.Close returned before terminal journal persistence completed")
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove finalized supervisor StateRoot: %v", err)
	}
}

func TestClaudeReleaseUsesPinnedOperatorSDKWithoutBundledVendorBytes(t *testing.T) {
	if claudeAgentSDKVersion != "0.3.251" || claudeAgentSDKSHA256 != "9235fac983c29e614d7f572a578406dc5dbda006305faa99f9447f577738eb93" {
		t.Fatalf("version=%q sha=%q", claudeAgentSDKVersion, claudeAgentSDKSHA256)
	}
	for _, name := range []string{"claudeassets/sdk.mjs", "claudeassets/LICENSE.txt", "claudeassets/manifest.json"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("proprietary release asset %q is still present: %v", name, err)
		}
	}
	bridge := string(claudeAgentSDKBridge)
	if strings.Contains(bridge, `"Bash"`) || !strings.Contains(bridge, `const DEFAULT_TOOLS = ["Read", "Glob", "Grep", "Edit", "Write"]`) ||
		!strings.Contains(bridge, `allowedTools = [...DEFAULT_TOOLS, "mcp__paimos__send_message"]`) || !strings.Contains(bridge, "tools: DEFAULT_TOOLS") {
		t.Fatalf("bridge default tool policy widened beyond repository edits")
	}
	if !strings.Contains(bridge, `...(start.model ? { model: start.model, effort: start.effort } : {})`) ||
		!strings.Contains(bridge, `!["low", "medium", "high", "xhigh", "max"].includes(start.effort)`) {
		t.Fatal("bridge does not apply the exact validated dispatch profile through documented SDK fields")
	}
}

func TestClaudeBridgeHasNoLegacyPromptStreamSteerQueue(t *testing.T) {
	bridge := string(claudeAgentSDKBridge)
	for _, legacy := range []string{"push(message)", "acknowledged", "rejected", "settle(item", "inFlight"} {
		if strings.Contains(bridge, legacy) {
			t.Fatalf("legacy prompt-stream steer machinery remains: %q", legacy)
		}
	}
	if !strings.Contains(bridge, "queryHandle.streamInput(controlInput)") {
		t.Fatal("steer is not bound to the live Query.streamInput method")
	}
}

func TestClaudeSteerAcceptsInterruptReceiptAfterInputWasAlreadyConsumed(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "consumed_before_receipt")
	adapter := newTestClaudeAdapter(t, node)
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "initial",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "consumed-race", Text: "steer"})
	if err != nil || receipt.CorrelationID != "consumed-race" || receipt.VendorMessageID == "" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "consumed-race-stop"})
}

func TestClaudeRepeatedStopHonorsContext(t *testing.T) {
	p := &claudeProcess{ownedProcess: newOwnedProcess(exec.Command(os.Args[0])), stopped: true}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.Stop(ctx, ControlRequest{CorrelationID: "bounded-repeat"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("repeated Stop ignored its context")
	}
}

func TestClaudeCorrelationExpiryPreventsCapacityOutage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "consumed_before_receipt")
	adapter := newTestClaudeAdapter(t, node)
	adapter.bridge = []byte(strings.Replace(string(adapter.bridge),
		"const CORRELATION_TTL_MS = 60 * 1000;", "const CORRELATION_TTL_MS = 10;", 1))
	digest := sha256.Sum256(adapter.bridge)
	adapter.bridgeSHA256 = hex.EncodeToString(digest[:])
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "initial",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxClaudePendingControls+2; i++ {
		if i == maxClaudePendingControls/2 {
			time.Sleep(25 * time.Millisecond)
		}
		_, err := process.Steer(context.Background(), ControlRequest{
			CorrelationID: fmt.Sprintf("expiry-%03d", i), Text: "steer",
		})
		if err != nil {
			t.Fatalf("steer %d: %v", i, err)
		}
	}
	_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "expiry-stop"})
}

func TestClaudeQueryAbortRejectsControlAndReaps(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "abort_with_pending_input")
	adapter := newTestClaudeAdapter(t, node)
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "initial",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := process.Steer(ctx, ControlRequest{CorrelationID: "pending-abort", Text: "steer"}); err == nil {
		t.Fatal("aborted Query unexpectedly acknowledged control")
	}
	waited := make(chan error, 1)
	go func() { waited <- process.Wait() }()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("aborted control wedged Query reap")
	}
}

func TestClaudeInputDeliveryTimeoutReleasesControlChain(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "wedge_stream_input")
	adapter := newTestClaudeAdapter(t, node)
	adapter.bridge = []byte(strings.Replace(string(adapter.bridge),
		"const CONTROL_INPUT_TIMEOUT_MS = 30 * 1000;", "const CONTROL_INPUT_TIMEOUT_MS = 10;", 1))
	digest := sha256.Sum256(adapter.bridge)
	adapter.bridgeSHA256 = hex.EncodeToString(digest[:])
	process, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:test", Prompt: "initial",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "wedge-steer", Text: "steer"}); err == nil {
		t.Fatal("unconsumed SDK streamInput unexpectedly succeeded")
	}
	interruptStarted := time.Now()
	if _, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "after-wedge"}); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("idle interrupt after bounded input failure err=%v", err)
	}
	if elapsed := time.Since(interruptStarted); elapsed > 250*time.Millisecond {
		t.Fatalf("control chain remained wedged after bounded input failure: %s", elapsed)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "wedge-stop"}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeLiveOwnedQuerySteerAndInterrupt(t *testing.T) {
	if os.Getenv("PAIMOS_AGENTD_LIVE_CLAUDE") != "1" {
		t.Skip("set PAIMOS_AGENTD_LIVE_CLAUDE=1 and PAIMOS_AGENTD_LIVE_CLAUDE_SDK=/absolute/sdk.mjs with authenticated local Claude to run live proof")
	}
	events := make(chan AdapterEvent, 64)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "proof.txt"), []byte(strings.Repeat("PAI-850 live Query marker\n", 20_000)), 0600); err != nil {
		t.Fatal(err)
	}
	sdkPath := os.Getenv("PAIMOS_AGENTD_LIVE_CLAUDE_SDK")
	process, err := NewClaudeAdapter("", "", sdkPath).Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: workspace, Identity: "claude:live-proof",
		Prompt: "Use the Read tool to inspect proof.txt before replying with ORIGINAL_TURN_FINISHED.",
	}, func(event AdapterEvent) { events <- event })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "live-proof-cleanup"})
	})
	toolDeadline := time.After(30 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == EventToolStarted {
				goto steer
			}
		case <-toolDeadline:
			t.Fatal("long live turn did not start a tool")
		}
	}

steer:
	receipt, err := process.Steer(context.Background(), ControlRequest{
		CorrelationID: "live-delivery-850", Text: "Stop the original work and acknowledge this steer now.",
	})
	if err != nil || receipt.VendorMessageID == "" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	reactionDeadline := time.After(30 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == EventTurnStarted && event.CorrelationID == "live-delivery-850" {
				t.Logf("live steer correlation=%s query_input_uuid=%s reaction=turn_started", receipt.CorrelationID, receipt.VendorMessageID)
				goto interrupt
			}
		case <-reactionDeadline:
			t.Fatal("live Query did not react to the correlated steer")
		}
	}

interrupt:
	interruptReceipt, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "live-interrupt-850"})
	if err != nil {
		t.Fatal(err)
	}
	stopReceipt, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "live-stop-850"})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	claude := process.(*claudeProcess)
	if claude.cmd.ProcessState == nil {
		t.Fatal("owned bridge was not reaped")
	}
	if _, err := os.Stat(claude.runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("temporary bridge runtime remains: %v", err)
	}
	t.Logf("live interrupt correlation=%s stop correlation=%s bridge_reaped=true", interruptReceipt.CorrelationID, stopReceipt.CorrelationID)
}

func TestClaudeLiveOwnedQueryDefaultToolsRejectBash(t *testing.T) {
	if os.Getenv("PAIMOS_AGENTD_LIVE_CLAUDE") != "1" {
		t.Skip("set PAIMOS_AGENTD_LIVE_CLAUDE=1 and PAIMOS_AGENTD_LIVE_CLAUDE_SDK=/absolute/sdk.mjs with authenticated local Claude to run live proof")
	}
	workspace := t.TempDir()
	sentinel := filepath.Join(workspace, "pai850-bash-sentinel")
	process, err := NewClaudeAdapter("", "", os.Getenv("PAIMOS_AGENTD_LIVE_CLAUDE_SDK")).Start(context.Background(), StartRequest{
		Adapter: AdapterClaude, Workspace: workspace, Identity: "claude:live-safe-tools",
		Prompt: "Use the Bash tool exactly once to run: printf enabled > pai850-bash-sentinel. Do not use Write, Edit, or any other tool to create the file. If Bash is unavailable, do not create the file.",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "live-safe-tools-cleanup"})
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sentinel); statErr == nil {
			t.Fatal("Bash executed despite the default owned-Query tool boundary")
		} else if !os.IsNotExist(statErr) {
			t.Fatal(statErr)
		}
		time.Sleep(250 * time.Millisecond)
	}
	stopReceipt, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "live-safe-tools-stop-850"})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	claude := process.(*claudeProcess)
	if claude.cmd.ProcessState == nil {
		t.Fatal("owned safe-tools bridge was not reaped")
	}
	if _, err := os.Stat(claude.runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("temporary safe-tools runtime remains: %v", err)
	}
	t.Logf("live safe-tools sentinel_created=false stop_correlation=%s bridge_reaped=true", stopReceipt.CorrelationID)
}

const fakeClaudeSDKModule = `
import { appendFileSync } from "node:fs";

const log = (line) => {
  const path = process.env.PAIMOS_CLAUDE_TEST_LOG;
  if (path) appendFileSync(path, line + "\n", { mode: 0o600 });
};

class Queue {
  constructor() { this.items = []; this.waiters = []; this.closed = false; }
  push(value) {
    const waiter = this.waiters.shift();
    if (waiter) waiter({ value, done: false }); else this.items.push(value);
  }
  close() {
    this.closed = true;
    for (const waiter of this.waiters.splice(0)) waiter({ done: true });
  }
  next() {
    if (this.items.length) return Promise.resolve({ value: this.items.shift(), done: false });
    if (this.closed) return Promise.resolve({ done: true });
    return new Promise((resolve) => this.waiters.push(resolve));
  }
}

export function tool(name, description, schema, handler) { return { name, handler }; }
export function createSdkMcpServer(options) { return options; }
export function query({ prompt, options }) {
  const output = new Queue();
  const queued = [];
  let first = true;
  let initialResultPending = false;
  const initFrame = (sessionID = "claude-owned-session", model = "claude-opus-fixture-20260909") => {
    const frame = { type: "system", subtype: "init", session_id: sessionID,
      capabilities: process.env.PAIMOS_CLAUDE_TEST_MODE === "missing_interrupt_receipt" ? [] : ["interrupt_receipt_v1"] };
    if (process.env.PAIMOS_CLAUDE_TEST_MODE !== "missing_model") frame.model = model;
    return frame;
  };
  (async () => {
    for await (const message of prompt) {
      if (first) {
        first = false;
        if (process.env.PAIMOS_CLAUDE_TEST_MODE !== "block_init") {
          const mode = process.env.PAIMOS_CLAUDE_TEST_MODE;
          output.push(initFrame("claude-owned-session", mode === "malformed_model" ? "not a model" : "claude-opus-fixture-20260909"));
          if (mode === "repeated_init") output.push(initFrame());
          if (mode === "changed_model") output.push(initFrame("claude-owned-session", "claude-sonnet-fixture-20260909"));
          if (mode === "foreign_session") output.push(initFrame("claude-foreign-session"));
          output.push({ type: "stream_event", session_id: "claude-owned-session" });
          output.push({ type: "assistant", session_id: "claude-owned-session", message: { content: [{ type: "text", text: message.message.content[0].text }] } });
          if (mode === "native_message") {
            if (options.tools.includes("Bash") || !options.allowedTools.includes("mcp__paimos__send_message") || Object.keys(options.mcpServers).join() !== "paimos") throw new Error("tool policy");
            const args = {to:"codex:peer",body:"model-originated fixture",reply_to:"",is_action_request:false,expects_reply:false};
            const block = {type:"tool_use",id:"native-tool",name:"mcp__paimos__send_message",input:args};
            output.push({type:"assistant",session_id:"claude-owned-session",message:{content:[block]}});
            output.push({type:"stream_event",session_id:"claude-owned-session",event:{type:"content_block_start",content_block:block}});
            output.push({type:"stream_event",session_id:"claude-owned-session",event:{type:"content_block_delta",delta:{type:"input_json_delta",partial_json:JSON.stringify(args)}}});
            const receipt = await options.mcpServers.paimos.tools[0].handler(args);
            output.push({type:"user",session_id:"claude-owned-session",message:{content:[{type:"tool_result",tool_use_id:"native-tool",content:JSON.stringify({...receipt,echo:args})}]}});
            if (receipt.isError || JSON.parse(receipt.content[0].text).message_id !== "receipt") throw new Error("tool result");
            log("native receipt accepted");
          }
		  if (["hold_initial_turn", "complete_before_interrupt_receipt"].includes(process.env.PAIMOS_CLAUDE_TEST_MODE)) initialResultPending = true;
		  else output.push({ type: "result", session_id: "claude-owned-session" });
		  if (process.env.PAIMOS_CLAUDE_TEST_MODE === "subsequent_activity_without_reaction") {
			setTimeout(() => output.push({ type: "assistant", session_id: "claude-owned-session", message: { content: [{ type: "tool_use", name: "bounded_fixture" }] } }), 10);
		  }
		  if (process.env.PAIMOS_CLAUDE_TEST_MODE === "abort_with_pending_input") {
			setTimeout(() => output.close(), 100);
			await new Promise(() => {});
		  }
        }
      } else {
        if (process.env.PAIMOS_CLAUDE_TEST_MODE !== "consumed_before_receipt") queued.push(message);
        log("stream query=owned-query uuid=" + message.uuid);
      }
    }
  })().catch(() => output.close());
  const handleInput = async (stream) => {
    for await (const message of stream) {
      if (process.env.PAIMOS_CLAUDE_TEST_MODE === "abort_with_pending_input" || process.env.PAIMOS_CLAUDE_TEST_MODE === "wedge_stream_input") {
        await new Promise(() => {});
      }
      if (process.env.PAIMOS_CLAUDE_TEST_MODE !== "consumed_before_receipt") queued.push(message);
      log("stream query=owned-query uuid=" + message.uuid);
      if (process.env.PAIMOS_CLAUDE_TEST_MODE === "consumed_before_receipt") {
        output.push({ type: "stream_event", session_id: "claude-owned-session", user_message_uuid: message.uuid });
      }
      if (process.env.PAIMOS_CLAUDE_TEST_MODE === "native_reply_before_input_receipt") {
        const receipt = await options.mcpServers.paimos.tools[0].handler({to:"codex:peer",body:"native inbox reply",reply_to:"incoming-message",is_action_request:false,expects_reply:false});
        if (receipt.isError || JSON.parse(receipt.content[0].text).message_id !== "receipt") throw new Error("native reply unavailable");
        log("native reply accepted before input receipt");
        output.push({ type: "stream_event", session_id: "claude-owned-session", user_message_uuid: message.uuid });
        output.push({ type: "result", session_id: "claude-owned-session", user_message_uuid: message.uuid });
      }
    }
  };
  return {
    [Symbol.asyncIterator]() { return this; },
    next() { return output.next(); },
    async streamInput(stream) {
      if (process.env.PAIMOS_CLAUDE_TEST_MODE === "wedge_stream_input") await new Promise(() => {});
      await handleInput(stream);
    },
    async interrupt() {
      if (process.env.PAIMOS_CLAUDE_TEST_MODE === "abort_with_pending_input") throw new Error("query aborted");
      log("interrupt query=owned-query");
      if (initialResultPending && process.env.PAIMOS_CLAUDE_TEST_MODE === "complete_before_interrupt_receipt") {
        initialResultPending = false;
        output.push({ type: "result", session_id: "claude-owned-session" });
		await new Promise((resolve) => setTimeout(resolve, 10));
      }
      const messages = queued.splice(0);
      const still_queued = messages.map((message) => message.uuid);
      for (const message of messages) {
        const uuid = message.uuid;
        output.push(initFrame());
        output.push({ type: "stream_event", session_id: "claude-owned-session", user_message_uuid: uuid });
        output.push({ type: "assistant", session_id: "claude-owned-session", user_message_uuid: uuid, message: { content: [{ type: "text", text: message.message.content[0].text }] } });
		output.push({ type: "result", session_id: "claude-owned-session", user_message_uuid: uuid });
      }
      return { still_queued };
    },
    close() { log("close query=owned-query"); output.close(); }
  };
}
`

func TestClaudeInboxStreamsWithoutInterruptingQuery(t *testing.T) {
	// This fixture emits a matching input reaction without requiring interrupt.
	// Merely consuming streamInput is deliberately insufficient for acceptance.
	t.Setenv("PAIMOS_CLAUDE_TEST_MODE", "consumed_before_receipt")
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node runtime unavailable")
	}
	logPath := filepath.Join(t.TempDir(), "events")
	t.Setenv("PAIMOS_CLAUDE_TEST_LOG", logPath)
	adapter := newTestClaudeAdapter(t, node)
	process, err := adapter.Start(context.Background(), StartRequest{Adapter: AdapterClaude, Workspace: t.TempDir(), Identity: "claude:fixture", Prompt: "initial fixture"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Stop(context.Background(), ControlRequest{CorrelationID: "cleanup"})
	effect, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "inbox-fixture", Text: "simple fixture input"})
	if err != nil || effect.Primitive != "claude Query.streamInput" || effect.VendorMessageID == "" || effect.CorrelationID != "inbox-fixture" {
		t.Fatal("simple Query handoff unconfirmed")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "interrupt") {
		t.Fatal("simple inbox interrupted Query")
	}
}
