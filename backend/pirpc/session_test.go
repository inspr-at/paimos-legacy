// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/pirpctest"
)

func launchTestSession(t *testing.T, mode string, extraEnv ...string) *Session {
	t.Helper()
	var argv []string
	session, err := Launch(context.Background(), LaunchConfig{
		Executable:    os.Args[0],
		Provider:      "anthropic",
		Model:         "claude-sonnet-5",
		ThinkingLevel: "high",
		NoSession:     true,
		Command: func(path string, args ...string) *exec.Cmd {
			argv = append([]string(nil), args...)
			return pirpctest.Command(piRPCHelperTest, mode, extraEnv...)(path, args...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Stop(context.Background())
	})
	if mode == "argv-echo" {
		want := []string{"--mode", "rpc", "--no-session", "--provider", "anthropic", "--model", "claude-sonnet-5", "--thinking", "high"}
		if !slices.Equal(argv, want) {
			t.Fatalf("argv=%q want %q", argv, want)
		}
	}
	return session
}

func TestSessionOwnedProcessPromptAcceptanceNotCompletion(t *testing.T) {
	session := launchTestSession(t, "accept-not-complete")
	acc, err := session.Prompt(context.Background(), "delivery-accept", "work", "")
	if err != nil || !acc.Accepted || acc.Command != "prompt" {
		t.Fatalf("acceptance=%+v err=%v", acc, err)
	}
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case event := <-session.Events():
			if event.Settled {
				t.Fatal("acceptance must not imply terminal settlement")
			}
		case <-deadline:
			return
		}
	}
}

func TestSessionSteerAndFollowUpQueueEvidence(t *testing.T) {
	session := launchTestSession(t, "serve")
	steer, err := session.Steer(context.Background(), "steer-live", "change direction")
	if err != nil || !steer.Accepted {
		t.Fatalf("steer=%+v err=%v", steer, err)
	}
	follow, err := session.FollowUp(context.Background(), "follow-live", "after done")
	if err != nil || !follow.Accepted {
		t.Fatalf("follow=%+v err=%v", follow, err)
	}
	deadline := time.After(500 * time.Millisecond)
	seenSteerQueue, seenFollowQueue := false, false
	for {
		select {
		case event := <-session.Events():
			if event.Type == "queue_update" && event.SteeringQueued > 0 {
				seenSteerQueue = true
			}
			if event.Type == "queue_update" && event.FollowUpQueued > 0 {
				seenFollowQueue = true
			}
		case <-deadline:
			if !seenSteerQueue || !seenFollowQueue {
				t.Fatalf("queue events steer=%t follow=%t", seenSteerQueue, seenFollowQueue)
			}
			return
		}
	}
}

func TestSessionAbortPreservesQueuedSteer(t *testing.T) {
	session := launchTestSession(t, "queue-abort")
	if _, err := session.Steer(context.Background(), "steer-before-abort", "hold this"); err != nil {
		t.Fatal(err)
	}
	abort, err := session.Abort(context.Background(), "abort-live")
	if err != nil || !abort.Accepted {
		t.Fatalf("abort=%+v err=%v", abort, err)
	}
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case event := <-session.Events():
			if event.Type == "queue_update" && event.SteeringQueued > 0 {
				return
			}
		case <-deadline:
			t.Fatal("abort alone should preserve queued steering evidence")
		}
	}
}

func TestSessionClearQueueReturnsPreservedMessages(t *testing.T) {
	session := launchTestSession(t, "queue-abort")
	data, acc, err := session.ClearQueue(context.Background(), "clear-live")
	if err != nil || !acc.Accepted {
		t.Fatalf("clear=%+v data=%+v err=%v", acc, data, err)
	}
	if len(data.Steering) == 0 {
		t.Fatalf("expected preserved steering text: %+v", data)
	}
}

func TestSessionAbortAloneContinuesNativeQueuedFollowUp(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	session := launchTestSession(t, "native-queue", pirpctest.TraceEnv+"="+trace)
	if _, err := session.Prompt(context.Background(), "prompt-live", "work", ""); err != nil {
		t.Fatal(err)
	}
	follow := "after\u2028done"
	if _, err := session.FollowUp(context.Background(), "follow-live", follow); err != nil {
		t.Fatal(err)
	}
	waitQueueUpdate(t, session, true)
	if _, err := session.Abort(context.Background(), "abort-only"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case event := <-session.Events():
			if event.Type == "agent_start" {
				return
			}
		case <-deadline:
			t.Fatal("abort without clear_queue must continue queued follow-up")
		}
	}
}

func TestSessionClearQueueThenAbortDoesNotContinueQueuedFollowUp(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	session := launchTestSession(t, "native-queue", pirpctest.TraceEnv+"="+trace)
	if _, err := session.Prompt(context.Background(), "prompt-live", "work", ""); err != nil {
		t.Fatal(err)
	}
	want := "restore\u2028this\u2029exact"
	if _, err := session.FollowUp(context.Background(), "follow-live", want); err != nil {
		t.Fatal(err)
	}
	waitQueueUpdate(t, session, true)
	data, acc, err := session.ClearQueue(context.Background(), "clear-live")
	if err != nil || !acc.Accepted {
		t.Fatalf("clear=%+v err=%v", acc, err)
	}
	if len(data.FollowUp) != 1 || data.FollowUp[0] != want {
		t.Fatalf("preserved follow-up=%q", data.FollowUp)
	}
	if _, err := session.Abort(context.Background(), "abort-after-clear"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case event := <-session.Events():
			if event.Type == "agent_start" {
				t.Fatal("queued follow-up continued after clear_queue then abort")
			}
			if event.Settled {
				raw, readErr := os.ReadFile(trace)
				if readErr != nil || strings.Contains(string(raw), "continued=true") {
					t.Fatalf("trace=%q err=%v", raw, readErr)
				}
				return
			}
		case <-deadline:
			t.Fatal("expected settlement without queued continuation")
		}
	}
}

func waitQueueUpdate(t *testing.T, session *Session, followUp bool) {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case event := <-session.Events():
			if event.Type == "queue_update" && (!followUp || event.FollowUpQueued > 0) {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for queue_update")
		}
	}
}

func TestSessionCorruptChildProtocolFault(t *testing.T) {
	_, err := Launch(context.Background(), LaunchConfig{
		Executable: os.Args[0],
		NoSession:  true,
		Command:    pirpctest.Command(piRPCHelperTest, "corrupt"),
	})
	if err == nil {
		t.Fatal("expected launch failure on corrupt protocol")
	}
}

func TestSessionEOFAfterAcceptance(t *testing.T) {
	session, err := Launch(context.Background(), LaunchConfig{
		Executable:    os.Args[0],
		Provider:      "anthropic",
		Model:         "claude-sonnet-5",
		ThinkingLevel: "high",
		NoSession:     true,
		Command:       pirpctest.Command(piRPCHelperTest, "eof-after-accept"),
	})
	if err != nil {
		t.Fatal(err)
	}
	acc, err := session.Prompt(context.Background(), "eof-accept", "work", "")
	if err != nil || !acc.Accepted {
		t.Fatalf("acceptance=%+v err=%v", acc, err)
	}
	_ = session.Stop(context.Background())
}

func TestSessionFrozenProviderModelArgv(t *testing.T) {
	_ = launchTestSession(t, "argv-echo")
}

func TestReaderUnicodeChildLine(t *testing.T) {
	cmd := piHelperCommand("unicode-line")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(stdout)
	line, err := reader.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload["content"], "\u2028") || !strings.Contains(payload["content"], "\u2029") {
		t.Fatalf("unicode separators lost: %q", payload["content"])
	}
	_ = cmd.Wait()
}

func TestSessionOversizeFrameRejected(t *testing.T) {
	_, err := Launch(context.Background(), LaunchConfig{
		Executable: os.Args[0],
		NoSession:  true,
		Command:    pirpctest.Command(piRPCHelperTest, "oversize"),
	})
	if err == nil {
		t.Fatal("expected launch failure on oversize frame")
	}
}

func TestSessionPIDPositive(t *testing.T) {
	session := launchTestSession(t, "serve")
	if session.PID() <= 0 {
		t.Fatalf("pid=%d", session.PID())
	}
}

func TestSessionPinsProtectedAgentDirAndIgnoresAmbient(t *testing.T) {
	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	ambient := t.TempDir()
	session, err := Launch(context.Background(), LaunchConfig{
		Executable:    os.Args[0],
		Provider:      "anthropic",
		Model:         "claude-sonnet-5",
		ThinkingLevel: "high",
		NoSession:     true,
		AgentDir:      canonical,
		Command:       pirpctest.Command(piRPCHelperTest, "agent-dir-echo", "PI_CODING_AGENT_DIR="+ambient),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Stop(context.Background()) })
	if session.FrozenConfig().AgentDir != canonical {
		t.Fatalf("frozen=%q want %q", session.FrozenConfig().AgentDir, canonical)
	}
	state, err := session.GetState(context.Background(), "agent-dir")
	if err != nil || state.SessionID != canonical {
		t.Fatalf("child env=%q want %q err=%v", state.SessionID, canonical, err)
	}
	if _, err := Launch(context.Background(), LaunchConfig{
		Executable: os.Args[0],
		NoSession:  true,
		AgentDir:   "relative-pi",
		Command:    pirpctest.Command(piRPCHelperTest, "serve"),
	}); err == nil {
		t.Fatal("relative agent dir was accepted")
	}
}
