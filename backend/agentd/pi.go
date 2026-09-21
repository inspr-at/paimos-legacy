// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/pirpc"
)

const (
	piSteerPrimitive    = "pi rpc steer"
	piFollowUpPrimitive = "pi rpc follow_up"
	piPromptPrimitive   = "pi rpc prompt"
	piPausePrimitive    = "pi rpc clear_queue+abort"
	piResumePrimitive   = "pi rpc resume-queue"
	piStopPrimitive     = "pi rpc stop"
	piOperationTimeout  = 30 * time.Second
)

var errPiPaused = errors.New("pi session is paused")

type PiAdapter struct {
	path     string
	command  func(string, ...string) *exec.Cmd
	accounts PiAccountRegistry
}

func NewPiAdapter(path string) *PiAdapter {
	return &PiAdapter{path: strings.TrimSpace(path), command: exec.Command}
}

func (a *PiAdapter) SetAccounts(registry PiAccountRegistry) {
	a.accounts = registry
}

func (a *PiAdapter) HasAccount(key string) bool {
	return a.accounts.HasAccount(key)
}

func (a *PiAdapter) EnrolledAccountKeys() []string {
	return a.accounts.EnrolledAccountKeys()
}

func (*PiAdapter) Name() string { return AdapterPi }

func (*PiAdapter) Capabilities() []Capability {
	return []Capability{CapabilityInbox, CapabilityStatus, CapabilitySteer, CapabilityInterrupt, CapabilityStop}
}

func (a *PiAdapter) AccountLabel(context.Context) string {
	// Pi exposes no stable provider account id. Unknown remains the only honest
	// probe label; named starts bind an isolated context instead of claiming identity.
	return "unknown"
}

func (a *PiAdapter) executable() (string, error) {
	return resolvePinnedExecutable(a.path, "pi", "operator-authenticated Pi CLI")
}

func (a *PiAdapter) Start(ctx context.Context, request StartRequest, observe func(AdapterEvent)) (_ Process, returnErr error) {
	if request.ProjectID <= 0 {
		return nil, errors.New("managed project is unavailable")
	}
	accountKey := strings.TrimSpace(request.AccountKey)
	if accountKey == "" {
		return nil, errors.New("managed account selection is unavailable")
	}
	account, ok := a.accounts.lookup(accountKey)
	if !ok {
		return nil, errors.New("managed account selection is unavailable")
	}
	if request.ExpectedAccountLabel != "" && request.ExpectedAccountLabel != AccountPiContext {
		return nil, errors.New("managed account selection is unavailable")
	}
	if request.ResolvedProfile == nil || request.ResolvedProfile.Harness != AdapterPi {
		return nil, ErrDispatchProfile
	}
	provider, model, thinking, err := piLaunchIntent(*request.ResolvedProfile)
	if err != nil {
		return nil, err
	}
	path, err := a.executable()
	if err != nil {
		return nil, err
	}
	command := a.command
	if command == nil {
		command = exec.Command
	}
	session, err := pirpc.Launch(ctx, pirpc.LaunchConfig{
		Executable:    path,
		Provider:      provider,
		Model:         model,
		ThinkingLevel: thinking,
		Workspace:     request.Workspace,
		AgentDir:      account.agentDir,
		NoSession:     true,
		Command:       command,
	})
	if err != nil {
		return nil, err
	}
	if session.FrozenConfig().AgentDir != account.agentDir {
		_ = session.Stop(context.Background())
		return nil, errors.New("managed account context changed before launch")
	}
	process := newPiProcess(session, observe, pirpc.ExpectedState{
		Provider: provider, ModelID: model, ThinkingLevel: thinking,
	}, accountKey, account.agentDir, request)
	defer func() {
		if returnErr != nil {
			_, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "agentd-pi-start-failed"})
		}
	}()
	state, err := session.GetState(ctx, "agentd-pi-session")
	if err != nil {
		return nil, errors.New("pi rpc session state unavailable")
	}
	if err := pirpc.ValidateEffectiveState(state, process.expected); err != nil {
		return nil, err
	}
	if state.SessionID != "" {
		process.observeEvent(AdapterEvent{Kind: EventSessionStarted, HarnessSessionID: state.SessionID})
	}
	acc, err := session.Prompt(ctx, "agentd-pi-start", request.Prompt, "")
	if err != nil || !acc.Accepted {
		return nil, errors.New("pi rpc initial prompt rejected")
	}
	process.observeEvent(AdapterEvent{Kind: EventTurnStarted, CorrelationID: "agentd-pi-start"})
	return process, nil
}

func piLaunchIntent(profile dispatchprofile.Profile) (provider, model, thinking string, err error) {
	if err := dispatchprofile.ValidateSnapshot(profile); err != nil || profile.Harness != AdapterPi {
		return "", "", "", ErrDispatchProfile
	}
	thinking = strings.TrimSpace(profile.Effort)
	switch thinking {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return "", "", "", ErrDispatchProfile
	}
	parts := strings.SplitN(strings.TrimSpace(profile.Model), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", ErrDispatchProfile
	}
	return parts[0], parts[1], thinking, nil
}

type piProcess struct {
	*ownedProcess
	session    *pirpc.Session
	observe    func(AdapterEvent)
	expected   pirpc.ExpectedState
	accountKey string
	agentDir   string
	queue      *piQueueStore
	scope      piQueueScope

	controlMu sync.Mutex
	stateMu   sync.Mutex
	closeOnce sync.Once
	streaming bool
	settled   bool
	faulted   bool
	paused    bool
}

func newPiProcess(session *pirpc.Session, observe func(AdapterEvent), expected pirpc.ExpectedState, accountKey, agentDir string, request StartRequest) *piProcess {
	profileID, profileVersion := "", ""
	if request.ResolvedProfile != nil {
		profileID = request.ResolvedProfile.ID
		profileVersion = request.ResolvedProfile.Version
	}
	generation := strings.TrimSpace(request.generation)
	if generation == "" {
		generation = "unbound"
	}
	process := &piProcess{
		ownedProcess: newOwnedProcess(session.OwnedCommand()),
		session:      session,
		observe:      observe,
		expected:     expected,
		accountKey:   accountKey,
		agentDir:     agentDir,
		queue:        request.queue,
		scope: piQueueScope{
			Generation:     generation,
			ProjectID:      request.ProjectID,
			Identity:       request.Identity,
			AccountKey:     accountKey,
			AccountLabel:   AccountPiContext,
			ProfileID:      profileID,
			ProfileVersion: profileVersion,
		},
	}
	go process.readEvents()
	return process
}

func (p *piProcess) AccountSelection() (string, string) {
	return p.accountKey, AccountPiContext
}

func (p *piProcess) observeEvent(event AdapterEvent) {
	if p.observe != nil {
		p.observe(event)
	}
}

func (p *piProcess) readEvents() {
	defer func() {
		p.closeSessionInput()
		p.finishAfterDrain()
	}()
	for {
		select {
		case event, ok := <-p.session.Events():
			if !ok {
				p.observeEvent(AdapterEvent{ErrorCode: ErrorEventStreamBound})
				return
			}
			if event.ProtocolFault {
				p.protocolFailure()
				return
			}
			switch {
			case event.Settled:
				p.stateMu.Lock()
				p.streaming = false
				p.settled = true
				p.stateMu.Unlock()
				p.observeEvent(AdapterEvent{Kind: EventTurnCompleted})
			case event.Ended:
				// agent_end is not terminal; queued continuations may still run.
			case event.Type == "agent_start":
				p.stateMu.Lock()
				p.streaming = true
				p.settled = false
				p.stateMu.Unlock()
				p.observeEvent(AdapterEvent{Kind: EventTurnStarted})
			}
		case <-p.session.StreamDone():
			p.observeEvent(AdapterEvent{ErrorCode: ErrorEventStreamBound})
			return
		}
	}
}

func (p *piProcess) protocolFailure() {
	p.stateMu.Lock()
	p.faulted = true
	p.stateMu.Unlock()
	p.observeEvent(AdapterEvent{ErrorCode: ErrorAppServerProtocol})
	p.closeSessionInput()
	_, _ = p.signalOwned(true)
}

func (p *piProcess) closeSessionInput() {
	p.closeOnce.Do(func() {
		_ = p.session.Stop(context.Background())
	})
}

func (p *piProcess) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, piOperationTimeout)
}

func (p *piProcess) verifyEffectiveState(ctx context.Context) error {
	opCtx, cancel := p.operationContext(ctx)
	defer cancel()
	state, err := p.session.GetState(opCtx, "agentd-pi-verify")
	if err != nil {
		return err
	}
	if p.agentDir != "" && p.session.FrozenConfig().AgentDir != p.agentDir {
		return errors.New("managed account context changed")
	}
	return pirpc.ValidateEffectiveState(state, p.expected)
}

func (p *piProcess) DeliveryHeld() bool {
	p.stateMu.Lock()
	paused := p.paused
	p.stateMu.Unlock()
	return paused || (p.queue != nil && p.queue.deliveryHeld(p.scope.Generation))
}

func (p *piProcess) pauseAccounted() bool {
	p.stateMu.Lock()
	paused := p.paused
	p.stateMu.Unlock()
	return paused && p.queue != nil && p.queue.pauseAccounted(p.scope.Generation)
}

func (p *piProcess) Steer(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	if request.Text == "" || len(request.Text) > maxTextBytes || !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, 0) {
		return ControlEffect{}, errors.New("pi steer text is invalid")
	}
	if p.DeliveryHeld() {
		return ControlEffect{}, errPiPaused
	}
	if p.queue == nil {
		return ControlEffect{}, errPiDurableUnavailable
	}
	if err := p.verifyEffectiveState(ctx); err != nil {
		return ControlEffect{}, err
	}
	if err := p.queue.reserve(p.scope, piQueueKindSteer, request.Text, request.CorrelationID); err != nil {
		return ControlEffect{}, err
	}
	opCtx, cancel := p.operationContext(ctx)
	defer cancel()
	acc, err := p.session.Steer(opCtx, request.CorrelationID, request.Text)
	if err != nil || !acc.Accepted {
		_ = p.queue.markRejected(p.scope.Generation, request.CorrelationID, piQueueKindSteer)
		return ControlEffect{}, errors.New("pi rpc steer rejected")
	}
	if err := p.queue.markQueued(p.scope.Generation, request.CorrelationID, piQueueKindSteer); err != nil {
		return ControlEffect{}, err
	}
	p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: piSteerPrimitive, CorrelationID: request.CorrelationID}, nil
}

func (p *piProcess) Interrupt(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	if p.pauseAccounted() {
		return ControlEffect{Primitive: piPausePrimitive, CorrelationID: request.CorrelationID}, nil
	}
	if p.queue == nil {
		return ControlEffect{}, errPiDurableUnavailable
	}
	if p.queue.hasAmbiguous(p.scope.Generation) {
		return ControlEffect{}, errPiQueueAmbiguous
	}
	if err := p.verifyEffectiveState(ctx); err != nil {
		return ControlEffect{}, err
	}
	opCtx, cancel := p.operationContext(ctx)
	defer cancel()
	if !p.queue.holdCommitted(p.scope.Generation) {
		if err := p.queue.beginHold(p.scope.Generation, request.CorrelationID); err != nil {
			return ControlEffect{}, err
		}
		data, acc, err := p.session.ClearQueue(opCtx, request.CorrelationID+"-clear")
		if err != nil || !acc.Accepted {
			_ = p.queue.rollbackHold(p.scope.Generation)
			return ControlEffect{}, errors.New("pi rpc clear_queue rejected")
		}
		if err := p.queue.commitHold(p.scope.Generation, request.CorrelationID, data, false); err != nil {
			return ControlEffect{}, err
		}
	}
	abort, err := p.session.Abort(opCtx, request.CorrelationID)
	if err != nil || !abort.Accepted {
		return ControlEffect{}, errors.New("pi rpc abort rejected")
	}
	p.stateMu.Lock()
	p.paused = true
	p.streaming = false
	p.stateMu.Unlock()
	p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: piPausePrimitive, CorrelationID: request.CorrelationID}, nil
}

func (p *piProcess) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	var residual error
	switch {
	case p.queue == nil:
		residual = errPiDurableUnavailable
	case p.queue.hasAmbiguous(p.scope.Generation):
		residual = errPiQueueAmbiguous
	default:
		opCtx, cancel := p.operationContext(ctx)
		defer cancel()
		if p.queue.holdCommitted(p.scope.Generation) || p.pauseAccounted() {
			if err := p.queue.markTerminal(p.scope.Generation, request.CorrelationID); err != nil {
				residual = err
			}
		} else if err := p.queue.beginHold(p.scope.Generation, request.CorrelationID); err != nil {
			residual = err
		} else {
			data, acc, err := p.session.ClearQueue(opCtx, request.CorrelationID+"-stop-clear")
			if err != nil || !acc.Accepted {
				_ = p.queue.rollbackHold(p.scope.Generation)
				residual = errors.New("pi rpc clear_queue rejected")
			} else if err := p.queue.commitHold(p.scope.Generation, request.CorrelationID, data, true); err != nil {
				residual = err
			}
		}
	}
	p.closeSessionInput()
	effect, err := p.ownedProcess.Stop(ctx, request)
	if err != nil {
		return effect, err
	}
	if residual == nil {
		p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	}
	effect.Primitive = piStopPrimitive
	return effect, residual
}

func (p *piProcess) Inbox(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	if request.Text == "" || len(request.Text) > maxTextBytes || !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, 0) {
		return ControlEffect{}, errors.New("pi inbox text is invalid")
	}
	if p.DeliveryHeld() {
		return ControlEffect{}, errPiPaused
	}
	if err := p.verifyEffectiveState(ctx); err != nil {
		return ControlEffect{}, err
	}
	opCtx, cancel := p.operationContext(ctx)
	defer cancel()
	state, err := p.session.GetState(opCtx, request.CorrelationID+"-state")
	if err != nil {
		return ControlEffect{}, err
	}
	if err := pirpc.ValidateEffectiveState(state, p.expected); err != nil {
		return ControlEffect{}, err
	}
	var acc pirpc.Acceptance
	var primitive string
	if state.IsStreaming {
		if p.queue == nil {
			return ControlEffect{}, errPiDurableUnavailable
		}
		if err := p.queue.reserve(p.scope, piQueueKindFollowUp, request.Text, request.CorrelationID); err != nil {
			return ControlEffect{}, err
		}
		acc, err = p.session.FollowUp(opCtx, request.CorrelationID, request.Text)
		primitive = piFollowUpPrimitive
		if err != nil || !acc.Accepted {
			_ = p.queue.markRejected(p.scope.Generation, request.CorrelationID, piQueueKindFollowUp)
			return ControlEffect{}, fmt.Errorf("pi rpc %s rejected", acc.Command)
		}
		if err := p.queue.markQueued(p.scope.Generation, request.CorrelationID, piQueueKindFollowUp); err != nil {
			return ControlEffect{}, err
		}
	} else {
		acc, err = p.session.Prompt(opCtx, request.CorrelationID, request.Text, "")
		primitive = piPromptPrimitive
		if err != nil || !acc.Accepted {
			return ControlEffect{}, fmt.Errorf("pi rpc %s rejected", acc.Command)
		}
	}
	p.observeEvent(AdapterEvent{Kind: EventTurnStarted, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: primitive, CorrelationID: request.CorrelationID}, nil
}

func (p *piProcess) ResumeQueue(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	if p.queue == nil {
		return ControlEffect{}, errPiDurableUnavailable
	}
	p.stateMu.Lock()
	paused := p.paused
	p.stateMu.Unlock()
	if !paused {
		return ControlEffect{}, errPiResumeUnavailable
	}
	if p.queue.hasAmbiguous(p.scope.Generation) {
		return ControlEffect{}, errPiQueueAmbiguous
	}
	if err := p.verifyEffectiveState(ctx); err != nil {
		return ControlEffect{}, err
	}
	steering, followUp, err := p.queue.liveHeld(p.scope.Generation)
	if err != nil {
		return ControlEffect{}, err
	}
	opCtx, cancel := p.operationContext(ctx)
	defer cancel()
	for i, record := range steering {
		text, err := p.queue.readPayload(record.Key, record.TextSHA256, record.TextBytes)
		if err != nil {
			return ControlEffect{}, err
		}
		correlation := fmt.Sprintf("%s-steer-%d", request.CorrelationID, i)
		if err := p.queue.reserve(p.scope, piQueueKindSteer, text, correlation); err != nil {
			return ControlEffect{}, err
		}
		acc, err := p.session.Steer(opCtx, correlation, text)
		if err != nil || !acc.Accepted {
			_ = p.queue.markRejected(p.scope.Generation, correlation, piQueueKindSteer)
			return ControlEffect{}, errors.New("pi rpc steer rejected")
		}
		if err := p.queue.markQueued(p.scope.Generation, correlation, piQueueKindSteer); err != nil {
			return ControlEffect{}, err
		}
		if err := p.queue.markResumed(record.Key, request.CorrelationID); err != nil {
			return ControlEffect{}, err
		}
	}
	for i, record := range followUp {
		text, err := p.queue.readPayload(record.Key, record.TextSHA256, record.TextBytes)
		if err != nil {
			return ControlEffect{}, err
		}
		correlation := fmt.Sprintf("%s-follow-%d", request.CorrelationID, i)
		if err := p.queue.reserve(p.scope, piQueueKindFollowUp, text, correlation); err != nil {
			return ControlEffect{}, err
		}
		acc, err := p.session.FollowUp(opCtx, correlation, text)
		if err != nil || !acc.Accepted {
			_ = p.queue.markRejected(p.scope.Generation, correlation, piQueueKindFollowUp)
			return ControlEffect{}, errors.New("pi rpc follow_up rejected")
		}
		if err := p.queue.markQueued(p.scope.Generation, correlation, piQueueKindFollowUp); err != nil {
			return ControlEffect{}, err
		}
		if err := p.queue.markResumed(record.Key, request.CorrelationID); err != nil {
			return ControlEffect{}, err
		}
	}
	p.stateMu.Lock()
	p.paused = false
	p.stateMu.Unlock()
	p.observeEvent(AdapterEvent{Kind: EventControlApplied, CorrelationID: request.CorrelationID})
	return ControlEffect{Primitive: piResumePrimitive, CorrelationID: request.CorrelationID}, nil
}

func (p *piProcess) InboxReady() bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return !p.streaming && !p.faulted && !p.paused
}

func (p *piProcess) Wait() error {
	err := p.session.Wait()
	p.stateMu.Lock()
	faulted := p.faulted
	p.stateMu.Unlock()
	if faulted {
		return errors.New("pi rpc protocol failed")
	}
	return err
}
