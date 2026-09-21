package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

func TestHarnessRegisterOmitsUnsetHierarchyFieldsForOldServer(t *testing.T) {
	registrationFile := filepath.Join(t.TempDir(), "registration.json")
	if err := os.WriteFile(registrationFile, []byte(`{"harness_session_ref":"session-1","worker_lease":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var posted map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/api/projects?status=all":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/6/harness-sessions":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"id":"11111111-1111-4111-8111-111111111111"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "test_key")
	if _, _, err := executeCLIForTest(t, "--json", "harness", "register", "--project", "PAI", "--agent", "worker", "--harness", "codex", "--host", "mbp0", "--registration-file", registrationFile, "--management", "managed", "--role", "worker", "--capability", "status"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"parent_harness_session_id", "ticket_id", "work_shape", "workspace", "dispatch_profile_id", "dispatch_profile_version", "account_label", "account_key"} {
		if _, ok := posted[field]; ok {
			t.Fatalf("unset forward-compatible field %s sent to old server: %s", field, posted[field])
		}
	}
}

func TestHarnessRegisterSendsTypedExecutionProvenance(t *testing.T) {
	registrationFile := filepath.Join(t.TempDir(), "registration.json")
	if err := os.WriteFile(registrationFile, []byte(`{"harness_session_ref":"session-1","worker_lease":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var posted map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"11111111-1111-4111-8111-111111111111"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "test_key")
	identity := strings.Repeat("a", 64)
	_, _, err := executeCLIForTest(t, "--json", "harness", "register", "--project", "PAI", "--agent", "worker", "--harness", "codex", "--host", "mbp0",
		"--registration-file", registrationFile, "--management", "managed", "--role", "worker", "--steer-mode", "owned", "--capability", "inbox,status,steer,interrupt,stop",
		"--workspace", "/workspace/paimos", "--git-top-level", "/workspace/paimos", "--git-branch", "feat/pai-906-dispatch-profiles",
		"--workspace-identity", identity, "--workspace-kind", "git_worktree", "--workspace-mode", "exclusive",
		"--dispatch-profile", "codex-sol-high", "--dispatch-profile-version", dispatchprofile.CatalogVersion, "--account-label", "chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"workspace", "dispatch_profile_id", "dispatch_profile_version", "account_label"} {
		if len(posted[field]) == 0 {
			t.Fatalf("missing %s in payload: %v", field, posted)
		}
	}
	for field, want := range map[string]string{"dispatch_profile_id": "codex-sol-high", "dispatch_profile_version": dispatchprofile.CatalogVersion} {
		var got string
		if err := json.Unmarshal(posted[field], &got); err != nil || got != want {
			t.Fatalf("%s = %s, want %q", field, posted[field], want)
		}
	}
}

func TestHarnessRegisterSendsClosedWorkShapeWithTicket(t *testing.T) {
	registrationFile := filepath.Join(t.TempDir(), "registration.json")
	if err := os.WriteFile(registrationFile, []byte(`{"harness_session_ref":"session-1","worker_lease":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/api/projects?status=all":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/6/harness-sessions":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"id":"11111111-1111-4111-8111-111111111111"}`))
		default:
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "test_key")
	_, _, err := executeCLIForTest(t, "--json", "harness", "register", "--project", "PAI", "--agent", "worker", "--harness", "codex", "--host", "mbp0",
		"--registration-file", registrationFile, "--management", "managed", "--role", "worker", "--ticket-id", "907", "--work-shape", "scout", "--capability", "status")
	if err != nil {
		t.Fatal(err)
	}
	if posted["ticket_id"] != float64(907) || posted["work_shape"] != "scout" {
		t.Fatalf("explicit task-shape payload=%+v", posted)
	}
}

func TestHarnessCommandsRejectImplicitOrOpenWorkShapes(t *testing.T) {
	registration := harnessRegisterCmd()
	registration.SetArgs([]string{"--project", "PAI", "--agent", "worker", "--harness", "codex", "--host", "mbp0", "--ticket-id", "907"})
	if err := registration.Execute(); err == nil || !strings.Contains(err.Error(), "--work-shape") {
		t.Fatalf("ticket registration without shape error=%v", err)
	}
	for _, shape := range []string{"", "research", "ship"} {
		binding := harnessBindCmd()
		args := []string{"--project", "PAI", "--session", "11111111-1111-4111-8111-111111111111", "--revision", "1", "--parent-session", "", "--ticket-id", "0"}
		if shape != "" {
			args = append(args, "--work-shape", shape)
		}
		binding.SetArgs(args)
		if err := binding.Execute(); err == nil {
			t.Fatalf("detached binding shape %q accepted", shape)
		}
	}
}

func TestHarnessNounDoesNotCollideWithAttributionSession(t *testing.T) {
	root := rootCmd()
	if root.Commands()[0] == nil {
		t.Fatal("root command unexpectedly empty")
	}
	harness, _, err := root.Find([]string{"harness"})
	if err != nil || harness == root || harness.Name() != "harness" {
		t.Fatalf("harness command missing: command=%v err=%v", harness, err)
	}
	session, _, err := root.Find([]string{"session", "start"})
	if err != nil || session.Name() != "start" {
		t.Fatalf("existing attribution session command changed: command=%v err=%v", session, err)
	}
	for _, child := range harness.Commands() {
		if child.Name() == "start" {
			t.Fatal("managed harness control plane must not define a second ambiguous start command")
		}
	}
	for _, name := range []string{"orchestrator", "bind", "drain", "complete-delivery", "drain-steer", "complete-steer"} {
		child, _, findErr := root.Find([]string{"harness", name})
		if findErr != nil || child.Name() != name {
			t.Fatalf("harness %s command missing: command=%v err=%v", name, child, findErr)
		}
	}
	controlGet, _, err := root.Find([]string{"harness", "control", "get"})
	if err != nil || controlGet.Name() != "get" {
		t.Fatalf("harness control get command missing: command=%v err=%v", controlGet, err)
	}
}

func TestHarnessRegisterRejectsCapabilityEscalationBeforeNetwork(t *testing.T) {
	refFile := filepath.Join(t.TempDir(), "session-ref")
	leaseFile := filepath.Join(t.TempDir(), "worker-lease")
	if err := os.WriteFile(refFile, []byte("session-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaseFile, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := harnessRegisterCmd()
	command.SetArgs([]string{"--project", "PAI", "--agent", "worker", "--harness", "claude", "--host", "mbp0",
		"--harness-session-file", refFile, "--worker-lease-file", leaseFile, "--message-target-id", "11111111-1111-4111-8111-111111111111", "--management", "unmanaged", "--role", "worker", "--capability", "inbox,status,steer",
		"--steer-mode", "codex_external"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unmanaged steer") {
		t.Fatalf("error=%v", err)
	}
}

func TestHarnessRegisterHasNoPlaintextSessionFlag(t *testing.T) {
	command := harnessRegisterCmd()
	if command.Flags().Lookup("harness-session") != nil {
		t.Fatal("private harness session reference must never be accepted as a process argument")
	}
	if command.Flags().Lookup("harness-session-file") == nil {
		t.Fatal("protected harness session file flag missing")
	}
	if command.Flags().Lookup("worker-lease") != nil || command.Flags().Lookup("worker-lease-file") == nil {
		t.Fatal("worker lease must be accepted only through a protected file")
	}
}

func TestHarnessRegistrationSecretsRejectUnknownDuplicateAndTrailingJSON(t *testing.T) {
	lease := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for name, raw := range map[string]string{
		"unknown":   `{"harness_session_ref":"ref","worker_lease":"` + lease + `","extra":true}`,
		"duplicate": `{"harness_session_ref":"ref","worker_lease":"` + lease + `","worker_lease":"` + lease + `"}`,
		"trailing":  `{"harness_session_ref":"ref","worker_lease":"` + lease + `"} true`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeHarnessRegistrationSecrets([]byte(raw)); err == nil {
				t.Fatal("unsafe registration JSON accepted")
			}
		})
	}
}

func TestHarnessControlGetUsesExactReadOnlyScopedRoute(t *testing.T) {
	sessionID := "11111111-1111-4111-8111-111111111111"
	controlID := "22222222-2222-4222-8222-222222222222"
	var routeSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/api/projects?status=all":
			_, _ = w.Write([]byte(`[{"id":6,"key":"PAI"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/6/harness-sessions/"+sessionID+"/controls/"+controlID:
			routeSeen = true
			if r.Header.Get("X-Paimos-Harness-Worker-Lease") != "" {
				t.Error("read-only operator route sent a worker lease")
			}
			_, _ = w.Write([]byte(`{"id":"` + controlID + `","project_id":6,"harness_session_id":"` + sessionID + `","correlation_id":"` + controlID + `","sequence":1,"kind":"interrupt","state":"applied","outcome":"applied","reason":"applied","requested_at":"2026-09-01T08:00:00Z","claimed_at":"2026-09-01T08:00:01Z","completed_at":"2026-09-01T08:00:02Z"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(envURL, server.URL)
	t.Setenv(envAPIKey, "test_key")

	out, _, err := executeCLIForTest(t, "--json", "harness", "control", "get", "--project", "PAI", "--session", sessionID, "--control-id", controlID)
	if err != nil {
		t.Fatal(err)
	}
	if !routeSeen || !strings.Contains(out, `"correlation_id":"`+controlID+`"`) || !strings.Contains(out, `"outcome":"applied"`) {
		t.Fatalf("routeSeen=%v output=%s", routeSeen, out)
	}
}
