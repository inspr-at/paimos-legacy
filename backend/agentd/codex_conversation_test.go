//go:build !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

func testConversationCollector(t *testing.T, bytes, events int) *codexConversationCollector {
	t.Helper()
	collector, err := newCodexConversationCollector(CodexConversationOptions{MaxOutputBytes: bytes, MaxEvents: events})
	if err != nil {
		t.Fatal(err)
	}
	collector.bindThread("thread-owned")
	collector.bindTurn("turn-owned")
	return collector
}

func sendConversationNotification(t *testing.T, collector *codexConversationCollector, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	collector.handleNotification(codexRPCMessage{Method: method, Params: raw})
}

func conversationDelta(threadID, turnID, itemID, text string) map[string]any {
	return map[string]any{"threadId": threadID, "turnId": turnID, "itemId": itemID, "delta": text}
}

func conversationItem(threadID, turnID, itemID, text, phase string) map[string]any {
	item := map[string]any{"id": itemID, "type": "agentMessage", "text": text}
	if phase != "" {
		item["phase"] = phase
	}
	return map[string]any{"threadId": threadID, "turnId": turnID, "completedAtMs": 1, "item": item}
}

func conversationTerminal(threadID, turnID, status string, items ...map[string]any) map[string]any {
	return map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": status, "items": items}}
}

func waitCollector(t *testing.T, collector *codexConversationCollector) CodexConversationResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, _ := collector.wait(ctx)
	return result
}

func TestCodexConversationStreamsAndDeduplicatesCompletedAnswer(t *testing.T) {
	collector := testConversationCollector(t, len("Héllo 🌍"), 8)
	sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-answer", "Hé"))
	sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-answer", "llo 🌍"))
	sendConversationNotification(t, collector, "item/completed", conversationItem("thread-owned", "turn-owned", "item-answer", "Héllo 🌍", "final_answer"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed"))

	result := waitCollector(t, collector)
	if result.Outcome != ConversationCompleted || result.Failure != ConversationFailureNone || result.TerminalStatus != "completed" || result.Text != "Héllo 🌍" || result.ItemID != "item-answer" || result.FinalCursor != 2 {
		t.Fatalf("result=%+v", result)
	}
	events, err := collector.replay(0)
	if err != nil || len(events) != 2 || events[0].Cursor != 1 || events[1].Cursor != 2 || events[0].Text+events[1].Text != result.Text {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	second, err := collector.replay(1)
	if err != nil || !slices.Equal(second, events[1:]) {
		t.Fatalf("cursor replay=%+v err=%v", second, err)
	}
	if _, err := collector.replay(3); !errors.Is(err, ErrCodexConversationCursor) {
		t.Fatalf("future cursor error=%v", err)
	}
	if _, err := collector.replay(^uint64(0)); !errors.Is(err, ErrCodexConversationCursor) {
		t.Fatalf("unrepresentable cursor error=%v", err)
	}
}

func TestCodexConversationCorrelatesExactOwnedTurnAndIgnoresMalformedContent(t *testing.T) {
	collector := testConversationCollector(t, 64, 16)
	for _, scoped := range [][2]string{{"foreign-thread", "turn-owned"}, {"thread-owned", "stale-turn"}} {
		sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta(scoped[0], scoped[1], "item-foreign", "wrong"))
		sendConversationNotification(t, collector, "item/completed", conversationItem(scoped[0], scoped[1], "item-foreign", "wrong", "final_answer"))
		sendConversationNotification(t, collector, "turn/completed", conversationTerminal(scoped[0], scoped[1], "completed"))
	}
	sendConversationNotification(t, collector, "item/completed", map[string]any{
		"threadId": "thread-owned", "turnId": "turn-owned", "item": map[string]any{"id": "bad", "type": "agentMessage", "phase": "invented", "text": "wrong"},
	})
	sendConversationNotification(t, collector, "item/completed", conversationItem("thread-owned", "turn-owned", "item-owned", "right", "final_answer"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed"))

	result := waitCollector(t, collector)
	if result.Outcome != ConversationCompleted || result.Text != "right" || result.ItemID != "item-owned" {
		t.Fatalf("foreign, stale, or malformed input affected result: %+v", result)
	}
}

func TestCodexConversationRejectsNonterminalCompletionStatus(t *testing.T) {
	collector := testConversationCollector(t, 64, 8)
	sendConversationNotification(t, collector, "item/completed", conversationItem("thread-owned", "turn-owned", "item-owned", "must-not-succeed", "final_answer"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "inProgress"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed"))
	result := waitCollector(t, collector)
	if result.Outcome != ConversationFailed || result.Failure != ConversationFailureProtocol || result.TerminalStatus != "inProgress" || result.Text != "" {
		t.Fatalf("result=%+v", result)
	}

	malformed := testConversationCollector(t, 64, 8)
	sendConversationNotification(t, malformed, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed",
		map[string]any{"id": "item-bad", "type": "agentMessage", "phase": "invalid", "text": "must-not-succeed"},
	))
	if result := waitCollector(t, malformed); result.Outcome != ConversationFailed || result.Failure != ConversationFailureProtocol || result.Text != "" {
		t.Fatalf("malformed terminal result=%+v", result)
	}
}

func TestCodexConversationReplaysNotificationsThatPrecedeTurnStartResponse(t *testing.T) {
	collector, err := newCodexConversationCollector(CodexConversationOptions{MaxOutputBytes: 32, MaxEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	collector.bindThread("thread-owned")
	sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-early", "item-early", "early"))
	sendConversationNotification(t, collector, "item/completed", conversationItem("thread-owned", "turn-early", "item-early", "early answer", "final_answer"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-early", "completed"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "foreign-turn", "failed"))
	collector.bindTurn("turn-early")

	result := waitCollector(t, collector)
	if result.Outcome != ConversationCompleted || result.Text != "early answer" || result.TurnID != "turn-early" {
		t.Fatalf("reordered result=%+v", result)
	}
}

func TestCodexConversationBoundsUTF8WithoutTruncating(t *testing.T) {
	collector := testConversationCollector(t, len("é🙂")-1, 8)
	sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "é"))
	sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "🙂"))
	result := waitCollector(t, collector)
	if result.Outcome != ConversationFailed || result.Failure != ConversationFailureOutputBound || result.Text != "" || result.FinalCursor != 1 {
		t.Fatalf("bounded result=%+v", result)
	}
	events, err := collector.replay(0)
	if err != nil || len(events) != 1 || events[0].Text != "é" {
		t.Fatalf("UTF-8 was truncated: events=%+v err=%v", events, err)
	}

	eventBound := testConversationCollector(t, 64, 1)
	sendConversationNotification(t, eventBound, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "one"))
	sendConversationNotification(t, eventBound, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "two"))
	if result := waitCollector(t, eventBound); result.Outcome != ConversationFailed || result.Failure != ConversationFailureEventBound || result.Text != "" {
		t.Fatalf("event bound result=%+v", result)
	}
}

func TestCodexConversationTerminalOutcomesNeverPromotePartialOutput(t *testing.T) {
	for _, tc := range []struct {
		status  string
		outcome ConversationOutcome
		failure ConversationFailure
	}{{"failed", ConversationFailed, ConversationFailureTurnFailed}, {"interrupted", ConversationCancelled, ConversationFailureCancelled}} {
		t.Run(tc.status, func(t *testing.T) {
			collector := testConversationCollector(t, 64, 8)
			sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "partial"))
			sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", tc.status))
			result := waitCollector(t, collector)
			if result.Outcome != tc.outcome || result.Failure != tc.failure || result.TerminalStatus != tc.status || result.Text != "" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
	collector := testConversationCollector(t, 64, 8)
	sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "partial"))
	collector.transportEnded()
	if result := waitCollector(t, collector); result.Outcome != ConversationFailed || result.Failure != ConversationFailureTransportEnded || result.Text != "" || result.TerminalStatus != "" {
		t.Fatalf("premature transport result=%+v", result)
	}
}

func TestCodexConversationCancellationDeadlineAndClosedResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		failure ConversationFailure
	}{
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, ConversationFailureCancelled},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), time.Nanosecond)
		}, ConversationFailureDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := testConversationCollector(t, 64, 8)
			ctx, cancel := tc.ctx()
			defer cancel()
			result, won := collector.wait(ctx)
			if !won || result.Outcome != ConversationCancelled || result.Failure != tc.failure || result.Text != "" {
				t.Fatalf("result=%+v won=%t", result, won)
			}
			sendConversationNotification(t, collector, "item/completed", conversationItem("thread-owned", "turn-owned", "item-owned", "late", "final_answer"))
			sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed"))
			if after := waitCollector(t, collector); after != result {
				t.Fatalf("late completion changed closed result: before=%+v after=%+v", result, after)
			}
		})
	}
}

func TestCodexConversationDuplicateAndLateFinalAreDeterministic(t *testing.T) {
	collector := testConversationCollector(t, 64, 8)
	final := conversationItem("thread-owned", "turn-owned", "item-owned", "answer", "final_answer")
	sendConversationNotification(t, collector, "item/completed", final)
	sendConversationNotification(t, collector, "item/completed", final)
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed"))
	result := waitCollector(t, collector)
	sendConversationNotification(t, collector, "item/completed", conversationItem("thread-owned", "turn-owned", "item-owned", "changed", "final_answer"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "failed"))
	if after := waitCollector(t, collector); result != after || after.Outcome != ConversationCompleted || after.Text != "answer" {
		t.Fatalf("closed result changed: before=%+v after=%+v", result, after)
	}
}

func TestCodexConversationUsesFinalAssistantItemFromCompletedTurn(t *testing.T) {
	collector := testConversationCollector(t, 64, 8)
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed",
		map[string]any{"id": "item-commentary", "type": "agentMessage", "text": "working", "phase": "commentary"},
		map[string]any{"id": "item-answer", "type": "agentMessage", "text": "answer", "phase": "final_answer"},
	))
	result := waitCollector(t, collector)
	if result.Outcome != ConversationCompleted || result.ItemID != "item-answer" || result.Text != "answer" || result.TerminalStatus != "completed" {
		t.Fatalf("result=%+v", result)
	}
}

func TestCodexConversationCollectorConcurrentReplayAndLateEvents(t *testing.T) {
	collector := testConversationCollector(t, 4096, 256)
	for i := 0; i < 32; i++ {
		sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "x"))
	}
	sendConversationNotification(t, collector, "item/completed", conversationItem("thread-owned", "turn-owned", "item-owned", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "final_answer"))
	sendConversationNotification(t, collector, "turn/completed", conversationTerminal("thread-owned", "turn-owned", "completed"))
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = collector.replay(0)
			sendConversationNotification(t, collector, "item/agentMessage/delta", conversationDelta("thread-owned", "turn-owned", "item-owned", "late"))
			collector.cancel(ConversationFailureCancelled)
		}()
	}
	group.Wait()
	if result := waitCollector(t, collector); result.Outcome != ConversationCompleted || result.Text != "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" || result.FinalCursor != 32 {
		t.Fatalf("concurrent late event changed result: %+v", result)
	}
}

func TestCodexConversationEntryPointIsExplicitAndCallable(t *testing.T) {
	adapter, request := conversationTestAdapter(t)
	var argv []string
	adapter.command = func(_ string, args ...string) *exec.Cmd {
		argv = append([]string(nil), args...)
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexConversationHelperProcess$")
		cmd.Env = append(os.Environ(), codexHelperEnvironment+"=conversation-answer")
		return cmd
	}
	var eventsMu sync.Mutex
	var events []AdapterEvent
	scratchRoot := canonicalTempDir(t)
	execution, err := adapter.StartConversation(context.Background(), request, CodexConversationOptions{MaxOutputBytes: 64, MaxEvents: 8, ScratchRoot: scratchRoot}, func(event AdapterEvent) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) < 4 || !slices.Equal(argv[:4], []string{"app-server", "--listen", "stdio://", "--strict-config"}) || slices.Contains(argv, "tools.view_image=false") || !slices.Contains(argv, "features.view_image=false") || !slices.Contains(argv, `default_permissions="aithema-conversation-v1"`) {
		t.Fatalf("restricted app-server argv=%q", argv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := execution.WaitConversation(ctx)
	if err != nil || result.Outcome != ConversationCompleted || result.Text != "Héllo 🌍" || result.ThreadID != "thread-owned" || result.TurnID != "turn-owned" || result.ItemID != "item-answer" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if entries, err := os.ReadDir(scratchRoot); err != nil || len(entries) != 0 {
		t.Fatalf("conversation scratch survived reap: entries=%v err=%v", entries, err)
	}
	deltas, err := execution.ReplayConversation(0)
	if err != nil || len(deltas) != 2 || deltas[0].Text+deltas[1].Text != result.Text {
		t.Fatalf("deltas=%+v err=%v", deltas, err)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	for _, event := range events {
		raw, _ := json.Marshal(event)
		if slices.ContainsFunc([]string{"private prompt", "Héllo", "🌍"}, func(secret string) bool { return len(secret) > 0 && bytes.Contains(raw, []byte(secret)) }) {
			t.Fatalf("assistant content entered AdapterEvent: %+v", event)
		}
	}

	ordinary := helperCodexAdapter(t, "serve", CodexAccountRegistry{})
	process, err := ordinary.Start(context.Background(), StartRequest{Workspace: t.TempDir(), Prompt: "secret-not-persisted", Identity: "codex:ordinary", Adapter: AdapterCodex}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "ordinary-cleanup"}) })
	if _, exposed := process.(CodexConversationExecution); exposed {
		t.Fatal("ordinary coding Start exposed conversation collection")
	}
}

func TestCodexConversationCleanupRejectsReplacedScratch(t *testing.T) {
	parent := canonicalTempDir(t)
	scratch := filepath.Join(parent, "paimos-conversation-owned")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	_, identity, err := canonicalCodexConversationScratch(scratch, true)
	if err != nil {
		t.Fatal(err)
	}
	outside := canonicalTempDir(t)
	outsideFixture := filepath.Join(outside, "outside-fixture")
	if err := os.WriteFile(outsideFixture, []byte("harmless fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(scratch); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, scratch); err != nil {
		t.Fatal(err)
	}

	execution := &codexConversationExecution{scratch: scratch, scratchIdentity: identity}
	execution.cleanupScratch()
	if content, err := os.ReadFile(outsideFixture); err != nil || string(content) != "harmless fixture" {
		t.Fatalf("cleanup escaped replaced scratch: content=%q err=%v", content, err)
	}
	if info, err := os.Lstat(scratch); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unowned replacement was removed: info=%v err=%v", info, err)
	}
}

func TestCodexConversationWaitCancellationAndDeadlineReapOwnedChild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		failure ConversationFailure
	}{
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, ConversationFailureCancelled},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 10*time.Millisecond)
		}, ConversationFailureDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, request := conversationTestAdapter(t)
			var child *exec.Cmd
			adapter.command = func(_ string, _ ...string) *exec.Cmd {
				child = exec.Command(os.Args[0], "-test.run=^TestCodexConversationHelperProcess$")
				child.Env = append(os.Environ(), codexHelperEnvironment+"=conversation-hang")
				return child
			}
			execution, err := adapter.StartConversation(context.Background(), request, CodexConversationOptions{MaxOutputBytes: 64, MaxEvents: 8}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := tc.context()
			defer cancel()
			result, err := execution.WaitConversation(ctx)
			if err != nil || result.Outcome != ConversationCancelled || result.Failure != tc.failure || result.Text != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if child == nil || child.Process == nil || child.ProcessState == nil || child.ProcessState.Pid() != child.Process.Pid {
				t.Fatalf("WaitConversation returned before exact child reap: child=%v", child)
			}
		})
	}
}

func TestCodexConversationHelperProcess(t *testing.T) {
	mode := os.Getenv(codexHelperEnvironment)
	if mode != "conversation-answer" && mode != "conversation-hang" {
		t.Skip("helper process")
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	step := 0
	for scanner.Scan() {
		var request struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		respond := func(result any) { _ = encoder.Encode(map[string]any{"id": request.ID, "result": result}) }
		expect := func(want int) {
			if step != want {
				os.Exit(3)
			}
			step++
		}
		switch request.Method {
		case "initialized":
			expect(1)
		case "initialize":
			expect(0)
			respond(map[string]any{})
		case "account/read":
			expect(2)
			respond(map[string]any{"account": map[string]any{"type": "chatgpt", "email": "conversation@example.invalid"}})
		case "experimentalFeature/list":
			expect(3)
			var data []codexConversationFeatureState
			for _, feature := range codexConversationRequiredFeatures() {
				feature.Stage = "stable"
				if feature.Name == "skip_host_skill_discovery" {
					feature.Stage = "underDevelopment"
				}
				data = append(data, feature)
			}
			respond(codexConversationExperimentalFeatureListResponse{Data: data})
		case "permissionProfile/list":
			expect(4)
			respond(codexConversationPermissionProfileListResponse{Data: []codexConversationPermissionProfileState{{ID: codexConversationPermissionProfile, Allowed: true}}})
		case "thread/start":
			expect(5)
			var params codexConversationThreadStartParams
			if json.Unmarshal(request.Params, &params) != nil {
				os.Exit(4)
			}
			respond(map[string]any{
				"thread":                  map[string]any{"id": "thread-owned", "cwd": params.CWD, "environments": []any{}, "ephemeral": true, "model": params.Model, "modelProvider": "openai", "parentThreadId": nil, "path": nil},
				"activePermissionProfile": map[string]any{"id": codexConversationPermissionProfile, "extends": nil},
				"approvalPolicy":          "never", "approvalsReviewer": "user", "cwd": params.CWD,
				"instructionSources": []any{}, "model": params.Model, "modelProvider": "openai",
				"multiAgentMode": "explicitRequestOnly", "runtimeWorkspaceRoots": []any{},
				"sandbox": map[string]any{"type": "workspaceWrite", "networkAccess": false, "writableRoots": []any{}},
			})
		case "turn/start":
			expect(6)
			respond(map[string]any{"turn": map[string]any{"id": "turn-owned", "status": "inProgress"}})
			if mode == "conversation-hang" {
				continue
			}
			_ = encoder.Encode(map[string]any{"method": "item/agentMessage/delta", "params": conversationDelta("thread-owned", "turn-owned", "item-answer", "Hé")})
			_ = encoder.Encode(map[string]any{"method": "item/agentMessage/delta", "params": conversationDelta("thread-owned", "turn-owned", "item-answer", "llo 🌍")})
			_ = encoder.Encode(map[string]any{"method": "item/completed", "params": conversationItem("thread-owned", "turn-owned", "item-answer", "Héllo 🌍", "final_answer")})
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": conversationTerminal("thread-owned", "turn-owned", "completed")})
		default:
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func conversationTestAdapter(t *testing.T) (*CodexAdapter, StartRequest) {
	t.Helper()
	home := writeCodexHome(t, "conversation@example.invalid")
	registry := testCodexRegistry(t, codexAccountRegistryEntry{Key: "conversation-account", Home: home, Email: "conversation@example.invalid"})
	adapter := NewCodexAdapter(os.Args[0], "test")
	adapter.SetAccounts(registry)
	profile, err := dispatchprofile.Resolve("codex-sol-high", "2", AdapterCodex)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, StartRequest{
		Prompt: "private prompt", Identity: "codex:conversation", Adapter: AdapterCodex,
		AccountKey: "conversation-account", DispatchProfileID: profile.ID,
		DispatchProfileVersion: profile.Version, ResolvedProfile: &profile,
	}
}
