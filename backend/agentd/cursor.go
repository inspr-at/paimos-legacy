// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/ownedprocess"
)

const (
	cursorSupportedCLIVersion = "2026.09.02-c22c1a3"
	cursorInitializeVersion   = 1
	maxCursorFrameBytes       = 8 << 20
	cursorOperationTimeout    = 20 * time.Second
	cursorPromptPrimitive     = "cursor acp session/prompt"
	cursorCancelPrimitive     = "cursor acp session/cancel"
	cursorStopPrimitive       = "cursor acp owned process-group stop"
	cursorComposerArgv        = "composer-2.5"
	cursorComposerACPModel    = "composer-2.5[fast=true]"
	cursorGrokArgv            = "grok-4.7-high"
	cursorGrokACPModel        = "grok-4.7-high"
)

// CursorAdapter owns one documented `agent acp` stdio child. Composer and Grok
// are catalog models inside this harness, not unmanaged Grok Bot/Build.
type CursorAdapter struct {
	path          string
	clientVersion string
	command       func(string, ...string) *exec.Cmd
	cliVersion    func(context.Context, string) (string, error)
	statusJSON    func(context.Context, string) ([]byte, error)
	accounts      CursorAccountRegistry
}

func NewCursorAdapter(path, clientVersion string) *CursorAdapter {
	return &CursorAdapter{
		path: strings.TrimSpace(path), clientVersion: strings.TrimSpace(clientVersion),
		command: exec.Command, cliVersion: probeCursorCLIVersion,
	}
}

func (a *CursorAdapter) SetAccounts(registry CursorAccountRegistry) {
	a.accounts = registry
}

func (a *CursorAdapter) HasAccount(key string) bool {
	return a.accounts.HasAccount(key)
}

func (a *CursorAdapter) EnrolledAccountKeys() []string {
	return a.accounts.EnrolledAccountKeys()
}

func (*CursorAdapter) Name() string { return AdapterCursor }

func (*CursorAdapter) Capabilities() []Capability {
	return []Capability{CapabilityInbox, CapabilityStatus, CapabilityInterrupt, CapabilityStop}
}

func (a *CursorAdapter) AccountLabel(ctx context.Context) string {
	path, err := a.executable()
	if err != nil {
		return "unknown"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := a.readStatusJSON(probeCtx, path)
	if err != nil {
		return "unknown"
	}
	_, _, ok := parseCursorStatusIdentity(output)
	if !ok {
		return "unknown"
	}
	// Official status documents authentication, not a selected trusted
	// identity mapping. cursor_context is bound only after a named start.
	return "unknown"
}

func (a *CursorAdapter) executable() (string, error) {
	return resolvePinnedExecutable(a.path, "cursor-agent", "operator-authenticated Cursor CLI")
}

func (a *CursorAdapter) readStatusJSON(ctx context.Context, path string) ([]byte, error) {
	if a.statusJSON != nil {
		return a.statusJSON(ctx, path)
	}
	return runAccountProbe(ctx, path, 1024, accountProbeCursor)
}

func (a *CursorAdapter) Start(ctx context.Context, request StartRequest, observe func(AdapterEvent)) (_ Process, returnErr error) {
	accountKey := strings.TrimSpace(request.AccountKey)
	if accountKey == "" {
		return nil, errors.New("managed account selection is unavailable")
	}
	account, ok := a.accounts.lookup(accountKey)
	if !ok {
		return nil, errors.New("managed account selection is unavailable")
	}
	if request.ExpectedAccountLabel != "" && request.ExpectedAccountLabel != AccountCursorContext {
		return nil, errors.New("managed account selection is unavailable")
	}
	argvModel, expectedACP, err := cursorLaunchModel(request)
	if err != nil {
		return nil, err
	}
	path, err := a.executable()
	if err != nil {
		return nil, err
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
	statusOutput, err := a.readStatusJSON(probeCtx, path)
	cancelProbe()
	if err != nil {
		return nil, errors.New("managed account identity could not be verified")
	}
	if err := verifyCursorAccountIdentity(statusOutput, account); err != nil {
		return nil, err
	}
	version, err := a.cliVersion(ctx, path)
	if err != nil || version != cursorSupportedCLIVersion {
		return nil, errors.New("pinned Cursor CLI version is unavailable")
	}
	command := a.command
	if command == nil {
		command = exec.Command
	}
	cmd := command(path, "--trust", "--model", argvModel, "acp") // #nosec G204 G702 -- fixed adapter argv and operator-selected executable.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("open Cursor ACP stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("open Cursor ACP stdout")
	}
	cmd.Stderr = io.Discard
	configured := ownedprocess.Configure(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errors.New("start Cursor ACP child")
	}
	if err := ownedprocess.Verify(cmd, configured); err != nil {
		_ = ownedprocess.Signal(cmd, true)
		_ = cmd.Wait()
		return nil, err
	}
	process := newCursorProcess(cmd, stdin, stdout, observe, request)
	defer func() {
		if returnErr != nil {
			_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "agentd-cursor-start-failed"})
		}
	}()

	operationCtx, cancel := context.WithTimeout(ctx, cursorOperationTimeout)
	defer cancel()
	clientVersion := a.clientVersion
	if clientVersion == "" {
		clientVersion = "dev"
	}
	var initialize cursorInitializeResult
	if err := process.call(operationCtx, "initialize", map[string]any{
		"protocolVersion": cursorInitializeVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "paimos-agentd", "title": "PAIMOS agentd", "version": clientVersion},
	}, &initialize); err != nil {
		return nil, fmt.Errorf("initialize Cursor ACP: %w", err)
	}
	if initialize.ProtocolVersion != cursorInitializeVersion {
		return nil, errors.New("Cursor ACP protocol version is unsupported")
	}
	var created cursorSessionNewResult
	if err := process.call(operationCtx, "session/new", map[string]any{
		"cwd": request.Workspace, "mcpServers": []any{},
	}, &created); err != nil {
		return nil, fmt.Errorf("create Cursor ACP session: %w", err)
	}
	if err := acknowledgeCursorSession(created, expectedACP); err != nil {
		return nil, err
	}
	if err := process.setSession(created.SessionID); err != nil {
		return nil, err
	}
	process.observeEvent(AdapterEvent{Kind: EventSessionStarted, HarnessSessionID: created.SessionID})
	if err := process.startPrompt(request.Prompt, "agentd-cursor-start"); err != nil {
		return nil, err
	}
	return process, nil
}

// ACP IDs are protocol data, independent of CLI selectors. The current pin
// remains unchanged. Unknown acknowledgements fail closed; add an observed
// mapping here only after verifying that CLI's session/new response.
type cursorModelRoute struct{ model, effort, argv, acp string }

func cursorModelRoutes() []cursorModelRoute {
	routes := []cursorModelRoute{
		{cursorComposerArgv, "default", cursorComposerArgv, cursorComposerACPModel},
		{"composer-2.5-fast", "default", "composer-2.5-fast", cursorComposerACPModel},
	}
	for _, effort := range []string{"low", "medium", "high", "xhigh"} {
		for _, suffix := range []string{"", "-fast"} {
			model := "grok-4.7-" + effort + suffix
			routes = append(routes, cursorModelRoute{model, effort, model, model})
		}
	}
	return routes
}

func cursorLaunchModel(request StartRequest) (argv, expectedACP string, err error) {
	return cursorLaunchModelFromRoutes(request, cursorModelRoutes())
}

func cursorLaunchModelFromRoutes(request StartRequest, routes []cursorModelRoute) (argv, expectedACP string, err error) {
	if request.ResolvedProfile == nil || request.ResolvedProfile.Harness != AdapterCursor {
		return "", "", ErrDispatchProfile
	}
	if err := dispatchprofile.ValidateSnapshot(*request.ResolvedProfile); err != nil {
		return "", "", ErrDispatchProfile
	}
	for _, route := range routes {
		if request.ResolvedProfile.Model == route.model && request.ResolvedProfile.Effort == route.effort {
			return route.argv, route.acp, nil
		}
	}
	return "", "", errors.New("Cursor model/effort has no approved ACP mapping")
}

func parseCursorStatusIdentity(output []byte) (email, userID string, ok bool) {
	var status struct {
		Status          string `json:"status"`
		IsAuthenticated bool   `json:"isAuthenticated"`
		UserInfo        *struct {
			Email  string          `json:"email"`
			UserID json.RawMessage `json:"userId"`
		} `json:"userInfo"`
	}
	if json.Unmarshal(output, &status) != nil || status.Status != "authenticated" || !status.IsAuthenticated || status.UserInfo == nil {
		return "", "", false
	}
	email = strings.TrimSpace(status.UserInfo.Email)
	if !validExpectedEmail(email) {
		return "", "", false
	}
	userID, idOK := parseCursorUserID(status.UserInfo.UserID)
	if !idOK {
		return "", "", false
	}
	return email, userID, true
}

func parseCursorUserID(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		value = strings.TrimSpace(value)
		if !validCursorUserID(value) {
			return "", false
		}
		return value, true
	}
	if !canonicalPositiveDecimalID(raw) {
		return "", false
	}
	id := string(raw)
	if !validCursorUserID(id) {
		return "", false
	}
	return id, true
}

func canonicalPositiveDecimalID(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 128 || raw[0] == '0' {
		return false
	}
	for _, b := range raw {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

func verifyCursorAccountIdentity(output []byte, expected cursorAccount) error {
	email, userID, ok := parseCursorStatusIdentity(output)
	if !ok || !strings.EqualFold(email, expected.email) {
		return errors.New("managed account identity could not be verified")
	}
	if expected.userID != "" && (userID == "" || userID != expected.userID) {
		return errors.New("managed account identity could not be verified")
	}
	return nil
}

func acknowledgeCursorSession(created cursorSessionNewResult, expectedACP string) error {
	if !validOpaqueID(created.SessionID) {
		return errors.New("Cursor ACP returned an invalid session")
	}
	if created.Modes == nil || strings.TrimSpace(created.Modes.CurrentModeID) == "" {
		return errors.New("Cursor ACP session modes were not acknowledged")
	}
	if created.Models == nil {
		return errors.New("Cursor ACP session models were not acknowledged")
	}
	current := strings.TrimSpace(created.Models.CurrentModelID)
	if current == "" || current == "default[]" || strings.HasPrefix(current, "default[") {
		return errors.New("Cursor Auto model is refused")
	}
	configModel := ""
	for _, option := range created.ConfigOptions {
		if option.ID == "model" {
			configModel = strings.TrimSpace(option.CurrentValue)
			break
		}
	}
	if configModel == "" || configModel != current {
		return errors.New("Cursor ACP model acknowledgement is inconsistent")
	}
	if current != expectedACP {
		return errors.New("Cursor ACP model acknowledgement does not match the selected profile")
	}
	listed := false
	for _, model := range created.Models.AvailableModels {
		if strings.TrimSpace(model.ModelID) == current {
			listed = true
			break
		}
	}
	if !listed {
		return errors.New("Cursor ACP model acknowledgement does not match the selected profile")
	}
	return nil
}

func probeCursorCLIVersion(ctx context.Context, path string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, path, "--version").Output() // #nosec G204 -- exact operator-selected local runtime.
	if err != nil || len(output) > 256 {
		return "", errors.New("read Cursor CLI version")
	}
	version := strings.TrimSpace(string(output))
	if version == "" || strings.ContainsAny(version, "\x00\r\n") {
		return "", errors.New("parse Cursor CLI version")
	}
	return version, nil
}

type cursorRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cursorRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *cursorRPCError `json:"error"`
}

type cursorInitializeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
}

type cursorSessionNewResult struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID string `json:"currentModeId"`
	} `json:"modes"`
	Models *struct {
		CurrentModelID  string `json:"currentModelId"`
		AvailableModels []struct {
			ModelID string `json:"modelId"`
			Name    string `json:"name"`
		} `json:"availableModels"`
	} `json:"models"`
	ConfigOptions []struct {
		ID           string `json:"id"`
		CurrentValue string `json:"currentValue"`
	} `json:"configOptions"`
}

type cursorPromptResult struct {
	StopReason string `json:"stopReason"`
}

type cursorProcess struct {
	persistent bool
	model      string
	accountKey string
	generation string
	*ownedProcess
	stdin    io.WriteCloser
	observe  func(AdapterEvent)
	evidence *cursorEvidenceStore

	writeMu      sync.Mutex
	rpcMu        sync.Mutex
	nextID       int
	pending      map[string]chan cursorRPCMessage
	sessionNewID string

	stateMu           sync.Mutex
	sessionID         string
	promptID          string
	promptCorrelation string
	startingPrompt    bool
	terminalFailure   bool
	decisionTTL       time.Duration
	held              map[string]*cursorHeldDecision
	refusals          []DecisionRefusal
	visible           strings.Builder
	visibleTruncated  bool
	streamDone        chan struct{}
	streamDoneOnce    sync.Once
}

func newCursorProcess(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader, observe func(AdapterEvent), requests ...StartRequest) *cursorProcess {
	p := &cursorProcess{ownedProcess: newOwnedProcess(cmd), stdin: stdin, observe: observe,
		pending: map[string]chan cursorRPCMessage{}, held: map[string]*cursorHeldDecision{},
		decisionTTL: cursorDecisionTTL, streamDone: make(chan struct{})}
	if len(requests) > 0 {
		p.persistent = requests[0].KeepAlive
		p.accountKey = strings.TrimSpace(requests[0].AccountKey)
		p.generation = strings.TrimSpace(requests[0].generation)
		if p.generation == "" {
			p.generation = "unbound"
		}
		p.evidence = requests[0].cursorEvidence
		if p.evidence == nil {
			p.evidence = &cursorEvidenceStore{}
		}
		if requests[0].ResolvedProfile != nil {
			p.model = requests[0].ResolvedProfile.Model
		}
	}
	go p.readLoop(stdout)
	return p
}

func (p *cursorProcess) AccountSelection() (string, string) {
	return p.accountKey, AccountCursorContext
}

func (p *cursorProcess) setSession(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !validOpaqueID(sessionID) {
		return errors.New("Cursor ACP session is invalid")
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.sessionID != "" {
		if p.sessionID != sessionID {
			return errors.New("Cursor ACP session is already bound")
		}
		return nil
	}
	p.sessionID = sessionID
	return nil
}

func (p *cursorProcess) observeEvent(event AdapterEvent) {
	if p.observe != nil {
		p.observe(event)
	}
}

func (p *cursorProcess) readLoop(reader io.Reader) {
	defer func() {
		p.closeInput()
		p.streamDoneOnce.Do(func() { close(p.streamDone) })
		p.finishAfterDrain()
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCursorFrameBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var message cursorRPCMessage
		if json.Unmarshal(line, &message) != nil || (message.JSONRPC != "" && message.JSONRPC != "2.0") {
			p.observeEvent(AdapterEvent{ErrorCode: ErrorAppServerProtocol})
			p.abortStream()
			return
		}
		if len(message.ID) > 0 && message.Method == "" {
			id := string(message.ID)
			p.bindSessionFromResponse(id, message.Result)
			p.rpcMu.Lock()
			response := p.pending[id]
			p.rpcMu.Unlock()
			if response != nil {
				select {
				case response <- message:
				default:
				}
			}
			continue
		}
		if message.Method != "" {
			p.handlePeer(message)
		}
	}
	if scanner.Err() != nil {
		p.observeEvent(AdapterEvent{ErrorCode: ErrorEventStreamBound})
		p.abortStream()
	}
}

func (p *cursorProcess) abortStream() {
	p.cancelHeldDecisions()
	p.clearVisible()
	p.closeInput()
	_, _ = p.signalOwned(true)
}

func (p *cursorProcess) handlePeer(message cursorRPCMessage) {
	switch message.Method {
	case "session/update":
		p.handleUpdate(message.Params)
	case "session/request_permission", "cursor/ask_question", "cursor/create_plan":
		p.holdPeerDecision(message)
	case "cursor/update_todos", "cursor/task", "cursor/generate_image":
		if len(message.ID) == 0 {
			// Documented fire-and-forget notifications. No approval is implied.
			return
		}
		fallthrough
	default:
		if len(message.ID) > 0 {
			p.failClosedPeer(message.ID, -32601, "method not found")
			p.noteRefusal(closedPeerMethod(message.Method), "unknown_method")
			p.recordEvidence(cursorEvidenceRecord{Generation: p.generation, Method: closedPeerMethod(message.Method), Outcome: "unknown_method"})
			p.observeEvent(AdapterEvent{ErrorCode: ErrorDecisionRefused})
		}
	}
}

func (p *cursorProcess) handleUpdate(raw json.RawMessage) {
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			ToolCallID    string `json:"toolCallId"`
			Status        string `json:"status"`
			Kind          string `json:"kind"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	p.stateMu.Lock()
	owned := params.SessionID != "" && params.SessionID == p.sessionID && !p.terminalFailure
	p.stateMu.Unlock()
	if !owned {
		return
	}
	if params.Update.SessionUpdate == "tool_call" || (params.Update.SessionUpdate == "tool_call_update" && params.Update.Status == "in_progress") {
		p.observeEvent(AdapterEvent{Kind: EventToolStarted})
	}
	switch params.Update.SessionUpdate {
	case "agent_message_chunk":
		p.appendVisible(params.Update.Content.Text)
		p.recordOutputDigest("message", params.Update.Content.Text)
	case "agent_thought_chunk":
		return
	}
}

func (p *cursorProcess) failClosedPeer(id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		return
	}
	_ = p.send(map[string]any{
		"jsonrpc": "2.0", "id": rawJSON(id),
		"error": map[string]any{"code": code, "message": message},
	})
}

func rawJSON(value json.RawMessage) any {
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return nil
	}
	return decoded
}

func (p *cursorProcess) bindSessionFromResponse(id string, result json.RawMessage) {
	p.rpcMu.Lock()
	expect := p.sessionNewID
	p.rpcMu.Unlock()
	if expect == "" || id != expect || len(result) == 0 {
		return
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(result, &created) != nil {
		return
	}
	_ = p.setSession(created.SessionID)
}

func (p *cursorProcess) send(value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode Cursor ACP request")
	}
	body = append(body, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return ErrSessionNotRunning
	}
	if _, err := p.stdin.Write(body); err != nil {
		return errors.New("write Cursor ACP request")
	}
	return nil
}

func (p *cursorProcess) call(ctx context.Context, method string, params, result any) error {
	message, err := p.roundTrip(ctx, method, params)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if len(message.Result) == 0 || json.Unmarshal(message.Result, result) != nil {
		return errors.New("Cursor ACP returned an invalid response")
	}
	return nil
}

func (p *cursorProcess) roundTrip(ctx context.Context, method string, params any) (cursorRPCMessage, error) {
	p.rpcMu.Lock()
	p.nextID++
	requestID := p.nextID
	id := strconv.Itoa(requestID)
	response := make(chan cursorRPCMessage, 1)
	p.pending[id] = response
	if method == "session/new" {
		p.sessionNewID = id
	}
	p.rpcMu.Unlock()
	defer func() {
		p.rpcMu.Lock()
		delete(p.pending, id)
		p.rpcMu.Unlock()
	}()
	if err := p.send(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": method, "params": params}); err != nil {
		return cursorRPCMessage{}, err
	}
	select {
	case message := <-response:
		if message.Error != nil {
			return cursorRPCMessage{}, fmt.Errorf("Cursor ACP rejected %s (code %d)", method, message.Error.Code)
		}
		return message, nil
	case <-ctx.Done():
		return cursorRPCMessage{}, ctx.Err()
	case <-p.done:
		return cursorRPCMessage{}, errors.New("Cursor ACP exited during request")
	case <-p.streamDone:
		return cursorRPCMessage{}, errors.New("Cursor ACP event stream ended during request")
	}
}

func (p *cursorProcess) startPrompt(text, correlation string) error {
	if text == "" || len(text) > maxPromptBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return errors.New("Cursor ACP prompt is invalid")
	}
	p.stateMu.Lock()
	sessionID := p.sessionID
	if sessionID == "" || p.promptID != "" || p.startingPrompt || p.terminalFailure {
		p.stateMu.Unlock()
		return ErrCapabilityMissing
	}
	p.startingPrompt = true
	p.promptCorrelation = correlation
	p.stateMu.Unlock()
	p.clearVisible()

	p.rpcMu.Lock()
	p.nextID++
	requestID := p.nextID
	id := strconv.Itoa(requestID)
	response := make(chan cursorRPCMessage, 1)
	p.pending[id] = response
	p.rpcMu.Unlock()

	p.stateMu.Lock()
	p.promptID = id
	p.startingPrompt = false
	p.stateMu.Unlock()

	if err := p.send(map[string]any{
		"jsonrpc": "2.0", "id": requestID, "method": "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]string{{"type": "text", "text": text}},
		},
	}); err != nil {
		p.failAmbiguousPrompt()
		p.rpcMu.Lock()
		delete(p.pending, id)
		p.rpcMu.Unlock()
		return err
	}
	go p.awaitPrompt(id, response)
	p.observeEvent(AdapterEvent{Kind: EventTurnStarted, CorrelationID: correlation})
	return nil
}

func (p *cursorProcess) awaitPrompt(id string, response <-chan cursorRPCMessage) {
	defer func() {
		p.rpcMu.Lock()
		delete(p.pending, id)
		p.rpcMu.Unlock()
	}()
	select {
	case message := <-response:
		p.completePrompt(id, message)
	case <-p.done:
		p.failAmbiguousPrompt()
	case <-p.streamDone:
		p.failAmbiguousPrompt()
	}
}

func (p *cursorProcess) completePrompt(id string, message cursorRPCMessage) {
	p.stateMu.Lock()
	if p.promptID != id || p.terminalFailure {
		p.stateMu.Unlock()
		return
	}
	var result cursorPromptResult
	failed := message.Error != nil || json.Unmarshal(message.Result, &result) != nil
	if !failed {
		switch result.StopReason {
		case "end_turn", "cancelled", "max_tokens", "max_turn_requests", "refusal":
		default:
			failed = true
		}
	}
	p.promptID = ""
	p.promptCorrelation = ""
	p.terminalFailure = failed
	p.stateMu.Unlock()
	if failed {
		p.observeEvent(AdapterEvent{ErrorCode: ErrorTurnFailed})
		p.abortStream()
		return
	}
	p.observeEvent(AdapterEvent{Kind: EventTurnCompleted})
	if !p.persistent {
		p.abortStream()
	}
}

func (p *cursorProcess) failAmbiguousPrompt() {
	p.stateMu.Lock()
	p.terminalFailure = true
	p.startingPrompt = false
	p.promptID = ""
	p.promptCorrelation = ""
	p.stateMu.Unlock()
	p.observeEvent(AdapterEvent{ErrorCode: ErrorAppServerProtocol})
	p.abortStream()
}

func (*cursorProcess) Steer(context.Context, ControlRequest) (ControlEffect, error) {
	return ControlEffect{}, ErrCapabilityMissing
}

func (p *cursorProcess) Interrupt(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	p.cancelHeldDecisions()
	p.stateMu.Lock()
	sessionID := p.sessionID
	promptID := p.promptID
	p.stateMu.Unlock()
	if sessionID == "" {
		return ControlEffect{}, ErrCapabilityMissing
	}
	if promptID == "" {
		return ControlEffect{Primitive: cursorCancelPrimitive, CorrelationID: request.CorrelationID, VendorMessageID: sessionID}, nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, cursorOperationTimeout)
	defer cancel()
	if err := p.send(map[string]any{
		"jsonrpc": "2.0", "method": "session/cancel",
		"params": map[string]any{"sessionId": sessionID},
	}); err != nil {
		return ControlEffect{}, err
	}
	deadline := time.NewTimer(cursorOperationTimeout)
	defer deadline.Stop()
	for {
		p.stateMu.Lock()
		idle := p.promptID == "" && !p.startingPrompt
		failed := p.terminalFailure
		p.stateMu.Unlock()
		if idle || failed {
			break
		}
		select {
		case <-operationCtx.Done():
			return ControlEffect{}, operationCtx.Err()
		case <-deadline.C:
			return ControlEffect{}, errors.New("Cursor ACP cancel was not acknowledged")
		case <-p.done:
			return ControlEffect{}, errors.New("Cursor ACP exited during cancel")
		case <-time.After(10 * time.Millisecond):
		}
	}
	p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: cursorCancelPrimitive, CorrelationID: request.CorrelationID, VendorMessageID: sessionID}, nil
}

func (p *cursorProcess) closeInput() {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
}

func (p *cursorProcess) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	_, _ = p.Interrupt(ctx, request)
	p.clearVisible()
	p.closeInput()
	effect, err := p.ownedProcess.Stop(ctx, request)
	if err == nil {
		effect.Primitive = cursorStopPrimitive
		p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	}
	return effect, err
}

func (p *cursorProcess) Inbox(_ context.Context, request ControlRequest) (ControlEffect, error) {
	if !p.InboxReady() {
		return ControlEffect{}, ErrCapabilityMissing
	}
	if err := p.startPrompt(request.Text, request.CorrelationID); err != nil {
		return ControlEffect{}, err
	}
	p.stateMu.Lock()
	sessionID := p.sessionID
	p.stateMu.Unlock()
	return ControlEffect{Primitive: cursorPromptPrimitive, CorrelationID: request.CorrelationID, VendorMessageID: sessionID}, nil
}

func (p *cursorProcess) InboxReady() bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.persistent && p.sessionID != "" && p.promptID == "" && !p.startingPrompt && !p.terminalFailure
}
