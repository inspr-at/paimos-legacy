package agentd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNativeMessageClosedInput(t *testing.T) {
	valid := `{"to":"claude:peer","body":"status","reply_to":"","is_action_request":false,"expects_reply":false}`
	if _, err := DecodeNativeMessage([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(valid, `"body":"status"`, `"body":"`+strings.Repeat("x", 4097)+`"`, 1), valid + `{}`, strings.Replace(valid, `"body":"status"`, `"body":"\u0000"`, 1), strings.Replace(valid, `"to":"claude:peer"`, `"to":"--config"`, 1)} {
		if _, err := DecodeNativeMessage([]byte(raw)); err == nil {
			t.Fatal("invalid tool input accepted")
		}
	}
	for _, field := range []string{"sender", "project_id", "session_id", "worker_lease", "thread_id", "metadata", "delivery_level", "idempotency_key"} {
		if _, err := DecodeNativeMessage([]byte(strings.TrimSuffix(valid, "}") + `,"` + field + `":"forged"}`)); err == nil {
			t.Fatalf("model supplied %s", field)
		}
	}
	raw, _ := json.Marshal(StartRequest{})
	if strings.Contains(string(raw), "sendNativeMessage") {
		t.Fatal("callback crossed public start wire")
	}
}

func TestNativeMessageExecutorBoundsAndCancels(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	e := newNativeMessageExecutor(func(ctx context.Context, _ string, _ NativeMessage) NativeMessageReceipt {
		close(started)
		<-ctx.Done()
		close(canceled)
		return NativeMessageReceipt{Error: "canceled"}
	})
	done := make(chan struct{})
	results := make(chan NativeMessageReceipt, 2)
	body := []byte(`{"to":"claude:peer","body":"status"}`)
	e.run(done, "first", body, func(r NativeMessageReceipt) { results <- r })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("send not started")
	}
	e.run(done, "second", body, func(r NativeMessageReceipt) { results <- r })
	if r := <-results; r.Error != "send_busy" {
		t.Fatalf("result=%+v", r)
	}
	close(done)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("send survived child shutdown")
	}
}

func TestNativeMessageExecutorReleasesSlotBeforeReply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed bool
		first   NativeMessageReceipt
	}{
		{"delivered", true, NativeMessageReceipt{Delivered: true}},
		{"send_failed", true, NativeMessageReceipt{Error: "send_failed"}},
		{"gate_rejected", false, NativeMessageReceipt{Error: "sender_unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delivered := NativeMessageReceipt{Delivered: true}
			e := newNativeMessageExecutor(func(_ context.Context, id string, _ NativeMessage) NativeMessageReceipt {
				if id == "first" {
					return tc.first
				}
				return delivered
			})
			done := make(chan struct{})
			defer close(done)
			results := make(chan NativeMessageReceipt, 2)
			body := []byte(`{"to":"claude:peer","body":"status"}`)
			e.run(done, "first", body, func(receipt NativeMessageReceipt) {
				results <- receipt
				// The child can send its next call as soon as it sees a reply.
				// Start it inside the callback to force that ordering.
				e.run(done, "second", body, func(receipt NativeMessageReceipt) { results <- receipt })
			}, func(context.Context) bool { return tc.allowed })
			for i, want := range []NativeMessageReceipt{tc.first, delivered} {
				select {
				case got := <-results:
					if got != want {
						t.Fatalf("reply %d = %+v, want %+v", i+1, got, want)
					}
				case <-time.After(time.Second):
					t.Fatalf("missing reply %d", i+1)
				}
			}
		})
	}
}
