package lifecycleintents

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

func TestLifecycleV1RegistrationJSONOmitsAccountChoices(t *testing.T) {
	in := Registration{
		Generation:   uuid.NewString(),
		Host:         "fixture-machine",
		AccountLabel: "chatgpt",
		Workspaces:   []Workspace{{Handle: uuid.NewString(), Identity: fmtIdentity(1)}},
		Profiles:     []Profile{{ID: "codex-sol-high", Version: "2"}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("v1 registration is not an object")
	}
	for _, name := range []string{"accounts", "schema_version", "account_key"} {
		if _, exists := fields[name]; exists {
			t.Fatalf("v1 registration leaked %s: %s", name, raw)
		}
	}
}

func fmtIdentity(n int) string {
	raw := make([]byte, 32)
	raw[31] = byte(n)
	out := make([]byte, 64)
	const hex = "0123456789abcdef"
	for i, b := range raw {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out)
}

func (f *fixture) namedAccounts() []AccountChoice {
	return []AccountChoice{
		{Key: "coordinator", Label: "Coordinator"},
		{Key: "personal", Label: "Personal"},
	}
}

func (f *fixture) namedRuntime(t *testing.T) Runtime {
	t.Helper()
	ctx := context.Background()
	f.now = f.now.Add(121 * time.Second)
	if _, err := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, f.registration); err != ErrUnavailable {
		t.Fatal("expired class-only generation revived")
	}
	named := f.registration
	named.Generation = uuid.NewString()
	named.SchemaVersion = AccountChoiceSchemaV2
	named.Accounts = f.namedAccounts()
	runtime, err := f.s.RegisterRuntime(ctx, f.reporter, f.project, testLease, named)
	if err != nil {
		t.Fatal(err)
	}
	f.registration = named
	f.runtime = runtime
	return runtime
}

func TestLifecycleAdvertisesTwoNamedAccountsOnOneRuntime(t *testing.T) {
	f := setup(t)
	runtime := f.namedRuntime(t)
	if runtime.SchemaVersion != AccountChoiceSchemaV2 || len(runtime.Accounts) != 2 {
		t.Fatalf("named advertisement=%+v", runtime)
	}
	if runtime.Accounts[0].Key != "coordinator" || runtime.Accounts[1].Label != "Personal" || runtime.AccountLabel != "chatgpt" {
		t.Fatalf("opaque keys mixed with class or labels: %+v", runtime)
	}
	if runtime.Accounts[0].Key == runtime.AccountLabel || runtime.Accounts[0].Label == runtime.AccountLabel {
		t.Fatal("operator label or key collapsed into account class")
	}
	advertised, err := f.s.Runtimes(context.Background(), f.human, f.project)
	if err != nil || len(advertised) != 1 || advertised[0].ID != runtime.ID || len(advertised[0].Accounts) != 2 {
		t.Fatalf("browser discovery=%+v err=%v", advertised, err)
	}
}

func TestLifecycleNamedAccountStartRetryAndLegacyClassOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	legacy := f.submit(t, f.request("start"))
	if legacy.SchemaVersion != RuntimeSchemaV1 || legacy.Request.AccountKey != "" {
		t.Fatalf("legacy start was versioned as named: %+v", legacy)
	}

	f.namedRuntime(t)
	req := f.request("start")
	req.AccountKey = "coordinator"
	first := f.submit(t, req)
	if first.SchemaVersion != AccountChoiceSchemaV2 || first.Request.AccountKey != "coordinator" {
		t.Fatalf("named start provenance=%+v", first)
	}
	again, created, err := f.s.Submit(ctx, f.human, f.project, req)
	if err != nil || created || again.ID != first.ID || again.Request.AccountKey != "coordinator" {
		t.Fatalf("retry created=%v err=%v got=%+v", created, err, again)
	}
	conflict := req
	conflict.AccountKey = "personal"
	if _, created, err := f.s.Submit(ctx, f.human, f.project, conflict); err != ErrConflict || created {
		t.Fatalf("duplicate request_key with different account created=%v err=%v", created, err)
	}
	second := f.request("start")
	second.AccountKey = "personal"
	if in := f.submit(t, second); in.Request.AccountKey != "personal" {
		t.Fatal("second configured account was not distinct")
	}
}

func TestLifecycleNamedAccountWrongKeyStaleGenerationAndClassOnlyKeyFailClosed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	classOnly := f.request("start")
	classOnly.AccountKey = "coordinator"
	if _, created, err := f.s.Submit(ctx, f.human, f.project, classOnly); err != ErrUnavailable || created {
		t.Fatalf("class-only runtime accepted forged key created=%v err=%v", created, err)
	}

	f.namedRuntime(t)
	for _, key := range []string{"missing", "chatgpt", "console"} {
		req := f.request("start")
		req.AccountKey = key
		_, created, err := f.s.Submit(ctx, f.human, f.project, req)
		if created {
			t.Fatalf("key %q created a start", key)
		}
		if key == "chatgpt" || key == "console" {
			if err != ErrInvalid {
				t.Fatalf("class-label key %q err=%v", key, err)
			}
			continue
		}
		if err != ErrUnavailable {
			t.Fatalf("unconfigured key %q err=%v", key, err)
		}
	}
	empty := f.request("start")
	if _, created, err := f.s.Submit(ctx, f.human, f.project, empty); err != ErrUnavailable || created {
		t.Fatalf("named runtime accepted class-only start created=%v err=%v", created, err)
	}
	stale := f.request("start")
	stale.AccountKey = "coordinator"
	stale.RuntimeGeneration = uuid.NewString()
	if _, created, err := f.s.Submit(ctx, f.human, f.project, stale); err != ErrUnavailable || created {
		t.Fatalf("stale generation created=%v err=%v", created, err)
	}
	path := f.request("start")
	path.AccountKey = "/tmp/codex"
	if _, created, err := f.s.Submit(ctx, f.human, f.project, path); err != ErrInvalid || created {
		t.Fatalf("path key created=%v err=%v", created, err)
	}
	repair := f.request("repair")
	if in := f.submit(t, repair); in.SchemaVersion != RuntimeSchemaV1 || in.Request.AccountKey != "" {
		t.Fatalf("named runtime refused class-only repair: %+v", in)
	}
}

func TestLifecycleNamedAccountCannotAdoptWorkerFromAnotherChoice(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.namedRuntime(t)
	current := f.managedNamed(t, "coordinator")
	generation := uuid.NewString()
	if err := f.s.RegisterSession(ctx, f.reporter, f.project, f.runtime.ID, testLease, testLease, SessionRegistration{SessionID: current.ID, Generation: generation}); err != nil {
		t.Fatal(err)
	}
	hs := managedharness.NewService(db.DB)
	var err error
	current, err = hs.HeartbeatWithActivity(ctx, current.ID, "yielded", managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_completed"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = time.Now().UTC().Add(time.Second)
	res, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,title,type) VALUES(?,1,'Named account target','ticket')`, f.project)
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := res.LastInsertId()
	req := f.request("reassign")
	req.AccountKey = "personal"
	req.SessionID = current.ID
	req.SessionGeneration = generation
	req.ExpectedRevision = current.Revision
	req.TicketID = &ticket
	req.WorkShape = "ship"
	if _, created, err := f.s.Submit(ctx, f.human, f.project, req); err != ErrUnavailable || created {
		t.Fatalf("adopted foreign named account created=%v err=%v", created, err)
	}
	req.AccountKey = "coordinator"
	in := f.submit(t, req)
	if in.Request.AccountKey != "coordinator" {
		t.Fatal("matching named account was not frozen on the intent")
	}
}

func (f *fixture) managedNamed(t *testing.T, key string) models.HarnessSession {
	t.Helper()
	s, _, err := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: f.project, AgentName: "worker", Harness: "codex", Host: f.runtime.MachineID, SessionRef: uuid.NewString(),
		WorkerLease: testLease, ManagementMode: "managed", Role: "worker", SteerMode: "none",
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true},
		Workspace: &models.HarnessWorkspaceProvenance{
			CanonicalPath: "/fixture/workspace", Kind: "directory", Mode: "exclusive", Identity: f.runtime.Workspaces[0].Identity,
		},
		DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", AccountLabel: f.runtime.AccountLabel, AccountKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
