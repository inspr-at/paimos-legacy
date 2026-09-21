// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const NativeMessageTool = "paimos_send_message"
const NativeMessageDescription = "Send a durable Paimos message of at most 4096 UTF-8 bytes under your owned identity. Use reply_to from the received envelope when replying; stop when the exchange is complete. Requests for action must set is_action_request and are held for human review. Receipt means ledger acceptance, not receiver completion."
const MaxNativeMessageBody = 4096

// NativeMessage is the entire model-writable surface. Identity, project,
// session, ticket, credentials and idempotency are supplied by the owner.
type NativeMessage struct {
	To            string `json:"to"`
	Body          string `json:"body"`
	ReplyTo       string `json:"reply_to"`
	ActionRequest bool   `json:"is_action_request"`
	ExpectsReply  bool   `json:"expects_reply"`
}

type NativeMessageReceipt struct {
	MessageID  string `json:"message_id,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	Delivered  bool   `json:"delivered"`
	HeldReason string `json:"held_reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

type NativeMessageReporter interface {
	SendNativeMessage(context.Context, Session, string, NativeMessage) NativeMessageReceipt
}
type nativeMessageSender func(context.Context, string, NativeMessage) NativeMessageReceipt

// Even rejection replies must leave the stdout reader immediately. A bounded
// writer queue prevents an input/output pipe cycle without unbounded goroutines.
type nativeReplyQueue struct {
	pending chan func()
	done    <-chan struct{}
}

func newNativeReplyQueue(done <-chan struct{}) *nativeReplyQueue {
	q := &nativeReplyQueue{pending: make(chan func(), 16), done: done}
	go func() {
		for {
			select {
			case <-done:
				return
			case write := <-q.pending:
				write()
			}
		}
	}()
	return q
}
func (q *nativeReplyQueue) enqueue(write func()) bool {
	select {
	case <-q.done:
		return false
	default:
	}
	select {
	case q.pending <- write:
		return true
	default:
		return false
	}
}

// A single outstanding send per child bounds work independently of model
// parallel tool requests. Shutdown cancels the transport, not the ledger.
type nativeMessageExecutor struct {
	send nativeMessageSender
	slot chan struct{}
}

func newNativeMessageExecutor(send nativeMessageSender) *nativeMessageExecutor {
	if send == nil {
		return nil
	}
	return &nativeMessageExecutor{send: send, slot: make(chan struct{}, 1)}
}

func (e *nativeMessageExecutor) run(done <-chan struct{}, id string, raw []byte, reply func(NativeMessageReceipt), gates ...func(context.Context) bool) {
	message, err := DecodeNativeMessage(raw)
	if e == nil || err != nil || !validOpaqueID(id) {
		reply(NativeMessageReceipt{Error: "invalid_message"})
		return
	}
	select {
	case e.slot <- struct{}{}:
	default:
		reply(NativeMessageReceipt{Error: "send_busy"})
		return
	}
	go func() {
		var receipt *NativeMessageReceipt
		defer func() {
			// The child can issue its next call as soon as the reply is visible.
			// Release admission before publishing either a send or gate result.
			<-e.slot
			if receipt != nil {
				reply(*receipt)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
		select {
		case <-done:
			return
		default:
		}
		for _, allowed := range gates {
			if !allowed(ctx) {
				receipt = &NativeMessageReceipt{Error: "sender_unavailable"}
				return
			}
		}
		result := e.send(ctx, id, message)
		receipt = &result
	}()
}

var nativeAddress = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}:[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func DecodeNativeMessage(raw []byte) (NativeMessage, error) {
	var message NativeMessage
	if len(raw) > 32<<10 || !utf8.Valid(raw) {
		return message, errors.New("invalid native message")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&message) != nil || d.Decode(new(any)) != io.EOF {
		return message, errors.New("invalid native message")
	}
	if !nativeAddress.MatchString(message.To) || strings.TrimSpace(message.Body) == "" || len(message.Body) > MaxNativeMessageBody || strings.ContainsRune(message.Body, 0) ||
		(message.ReplyTo != "" && !validOpaqueID(message.ReplyTo)) {
		return message, errors.New("invalid native message")
	}
	return message, nil
}

func nativeMessageSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"to":                map[string]any{"type": "string", "description": "Recipient harness:agent address"},
			"body":              map[string]any{"type": "string", "maxLength": MaxNativeMessageBody},
			"reply_to":          map[string]any{"type": "string", "description": "Incoming message_id when replying; empty for a new exchange"},
			"is_action_request": map[string]any{"type": "boolean"},
			"expects_reply":     map[string]any{"type": "boolean"}},
		"required": []string{"to", "body", "reply_to", "is_action_request", "expects_reply"}}
}
