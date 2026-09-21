// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

type intentHTTPFixture struct {
	lease, runtime string
	enclosing      int
	in             lifecycleintents.Intent
	states         []string
}

func namedAccountIntent(runtime, generation string, schema int, accountKey string) lifecycleintents.Intent {
	return lifecycleintents.Intent{
		SchemaVersion: schema,
		ID:            uuid.NewString(),
		ProjectID:     42,
		State:         "claimed",
		Revision:      2,
		NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey:             uuid.NewString(),
			Operation:              "start",
			RuntimeID:              runtime,
			RuntimeGeneration:      generation,
			AccountLabel:           "chatgpt",
			AccountKey:             accountKey,
			AgentName:              "fixture",
			DispatchProfileID:      "codex-sol-high",
			DispatchProfileVersion: "2",
		},
	}
}

func (f *intentHTTPFixture) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get(lifecycleintents.RuntimeLeaseHeader) != f.lease {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			_ = json.NewEncoder(w).Encode(struct {
				SchemaVersion int                      `json:"schema_version"`
				Intent        *lifecycleintents.Intent `json:"intent"`
			}{f.enclosing, &f.in})
		case strings.Contains(r.URL.Path, "/intents/") && strings.HasSuffix(r.URL.Path, "/transition"):
			var tr lifecycleintents.Transition
			if json.NewDecoder(r.Body).Decode(&tr) != nil || tr.ExpectedRevision != f.in.Revision || tr.RuntimeID != f.in.Request.RuntimeID || tr.RuntimeGeneration != f.in.Request.RuntimeGeneration {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.in.State = tr.State
			f.in.Reason = tr.Reason
			f.in.ResultSessionID = tr.ResultSessionID
			f.in.Revision++
			f.states = append(f.states, tr.State)
			_ = json.NewEncoder(w).Encode(f.in)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func openHTTPRunner(t *testing.T, base string, lease, generation string, e Executor) (*HTTP, *Runner) {
	t.Helper()
	h, err := NewHTTP(base, 42, lease, func() (string, error) { return "fixture-key", nil })
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(dir, generation, h, e)
	if err != nil {
		t.Fatal(err)
	}
	return h, runner
}

func TestHTTPRunnerNamedAccountSchema2StartExecutingCompleted(t *testing.T) {
	lease, _ := NewProof()
	runtime, generation := uuid.NewString(), uuid.NewString()
	f := &intentHTTPFixture{lease: lease, runtime: runtime, enclosing: 1, in: namedAccountIntent(runtime, generation, lifecycleintents.AccountChoiceSchemaV2, "coordinator")}
	server := f.serve(t)
	defer server.Close()
	e := &fixtureExecutor{}
	_, runner := openHTTPRunner(t, server.URL, lease, generation, e)
	rt := lifecycleintents.Runtime{ID: runtime, ProjectID: 42, Generation: generation, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	if err := runner.Step(context.Background(), rt); err != nil || e.effects != 1 || e.commits != 1 {
		t.Fatal("schema2 HTTP start did not complete", err)
	}
	if strings.Join(f.states, ",") != "executing,completed" || f.in.SchemaVersion != lifecycleintents.AccountChoiceSchemaV2 || f.in.Request.AccountKey != "coordinator" || f.in.State != "completed" {
		t.Fatalf("schema2 path=%v intent=%+v", f.states, f.in)
	}
}

func TestHTTPRunnerLegacySchema1StartExecutingCompleted(t *testing.T) {
	lease, _ := NewProof()
	runtime, generation := uuid.NewString(), uuid.NewString()
	f := &intentHTTPFixture{lease: lease, runtime: runtime, enclosing: 1, in: namedAccountIntent(runtime, generation, lifecycleintents.RuntimeSchemaV1, "")}
	server := f.serve(t)
	defer server.Close()
	e := &fixtureExecutor{}
	h, runner := openHTTPRunner(t, server.URL, lease, generation, e)
	claimed, err := h.Claim(context.Background(), runtime)
	if err != nil || claimed == nil || claimed.SchemaVersion != lifecycleintents.RuntimeSchemaV1 || claimed.Request.AccountKey != "" {
		t.Fatal("legacy claim lost", err)
	}
	rt := lifecycleintents.Runtime{ID: runtime, ProjectID: 42, Generation: generation, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	if err = runner.Step(context.Background(), rt); err != nil || e.effects != 1 || e.commits != 1 || strings.Join(f.states, ",") != "executing,completed" {
		t.Fatal("legacy schema1 HTTP start did not complete", err)
	}
}

func TestHTTPClaimAndTransitionRejectUnknownSchemaAndMismatches(t *testing.T) {
	lease, _ := NewProof()
	runtime, generation := uuid.NewString(), uuid.NewString()
	valid := namedAccountIntent(runtime, generation, lifecycleintents.AccountChoiceSchemaV2, "coordinator")
	cases := []struct {
		name      string
		enclosing int
		in        lifecycleintents.Intent
		claimOK   bool
		trans     lifecycleintents.Transition
	}{
		{name: "unknown3", enclosing: 1, in: namedAccountIntent(runtime, generation, 3, "coordinator"), trans: lifecycleintents.Transition{RuntimeID: runtime, RuntimeGeneration: generation, ExpectedRevision: 2, State: "executing"}},
		{name: "envelope2", enclosing: 2, in: valid},
		{name: "schema2withoutkey", enclosing: 1, in: namedAccountIntent(runtime, generation, lifecycleintents.AccountChoiceSchemaV2, ""), trans: lifecycleintents.Transition{RuntimeID: runtime, RuntimeGeneration: generation, ExpectedRevision: 2, State: "executing"}},
		{name: "schema1withkey", enclosing: 1, in: namedAccountIntent(runtime, generation, lifecycleintents.RuntimeSchemaV1, "coordinator"), trans: lifecycleintents.Transition{RuntimeID: runtime, RuntimeGeneration: generation, ExpectedRevision: 2, State: "executing"}},
		{name: "wrongproject", enclosing: 1, in: func() lifecycleintents.Intent {
			in := namedAccountIntent(runtime, generation, lifecycleintents.AccountChoiceSchemaV2, "coordinator")
			in.ProjectID = 99
			return in
		}(), trans: lifecycleintents.Transition{RuntimeID: runtime, RuntimeGeneration: generation, ExpectedRevision: 2, State: "executing"}},
		{name: "wrongruntime", enclosing: 1, in: namedAccountIntent(uuid.NewString(), generation, lifecycleintents.AccountChoiceSchemaV2, "coordinator"), trans: lifecycleintents.Transition{RuntimeID: runtime, RuntimeGeneration: generation, ExpectedRevision: 2, State: "executing"}},
		{name: "wronggeneration", enclosing: 1, in: valid, claimOK: true, trans: lifecycleintents.Transition{RuntimeID: runtime, RuntimeGeneration: uuid.NewString(), ExpectedRevision: 2, State: "executing"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &intentHTTPFixture{lease: lease, runtime: runtime, enclosing: c.enclosing, in: c.in}
			server := f.serve(t)
			defer server.Close()
			h, err := NewHTTP(server.URL, 42, lease, func() (string, error) { return "fixture-key", nil })
			if err != nil {
				t.Fatal(err)
			}
			_, e := h.Claim(context.Background(), runtime)
			if c.claimOK {
				if e != nil {
					t.Fatal("supported claim refused", e)
				}
			} else if !errors.Is(e, ErrOwnership) {
				t.Fatal("claim accepted unsupported schema or mismatch", e)
			}
			if c.name == "envelope2" {
				return
			}
			if _, e = h.Transition(context.Background(), c.in.ID, c.trans); !errors.Is(e, ErrOwnership) && !errors.Is(e, lifecycleintents.ErrConflict) {
				t.Fatal("transition accepted unsupported schema or mismatch", e)
			}
		})
	}
}

func TestHTTPRunnerMismatchedRuntimeRefusesEffect(t *testing.T) {
	lease, _ := NewProof()
	runtime, generation := uuid.NewString(), uuid.NewString()
	f := &intentHTTPFixture{lease: lease, runtime: runtime, enclosing: 1, in: namedAccountIntent(runtime, generation, lifecycleintents.AccountChoiceSchemaV2, "coordinator")}
	server := f.serve(t)
	defer server.Close()
	e := &fixtureExecutor{}
	_, runner := openHTTPRunner(t, server.URL, lease, generation, e)
	rt := lifecycleintents.Runtime{ID: uuid.NewString(), ProjectID: 42, Generation: generation, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	if !errors.Is(runner.Step(context.Background(), rt), ErrOwnership) || e.effects != 0 || len(f.states) != 0 {
		t.Fatal("mismatched runtime executed")
	}
}
