// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package workerfleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/internal/testdb"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workshape"
)

const fleetTestWorkerLease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type fleetTestDB struct {
	userID       int64
	externalID   int64
	projectOneID int64
	projectTwoID int64
}

func openFleetTestDB(t *testing.T) fleetTestDB {
	t.Helper()
	previousDataDir := os.Getenv("DATA_DIR")
	previousTestMode := os.Getenv("PAIMOS_TEST_MODE")
	previousSecret := os.Getenv("PAIMOS_SECRET_KEY")
	t.Cleanup(func() {
		_ = paimosdb.DB.Close()
		paimosdb.DB = nil
		_ = os.Setenv("DATA_DIR", previousDataDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", previousTestMode)
		_ = os.Setenv("PAIMOS_SECRET_KEY", previousSecret)
	})
	_ = os.Setenv("DATA_DIR", t.TempDir())
	_ = os.Setenv("PAIMOS_TEST_MODE", "1")
	_ = os.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	testdb.Prepare(t)
	if err := paimosdb.Open(); err != nil {
		t.Fatal(err)
	}
	insertUser := func(name, role string) int64 {
		result, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES(?, 'x', ?, 'active')`, name, role)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	insertProject := func(name, key string) int64 {
		result, err := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES(?,?)`, name, key)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	return fleetTestDB{userID: insertUser("fleet-admin", "admin"), externalID: insertUser("fleet-external", "external"),
		projectOneID: insertProject("Alpha", "ALP"), projectTwoID: insertProject("Beta", "BET")}
}

func registerFleetSession(t *testing.T, projectID int64, name, role string, parent *string, ticket *int64) models.HarnessSession {
	t.Helper()
	result, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name)
	if err != nil {
		t.Fatal(err)
	}
	_ = result
	shape := ""
	if ticket != nil {
		shape = workshape.Ship
	}
	workspace := &models.HarnessWorkspaceProvenance{CanonicalPath: fmt.Sprintf("/workspace/%d/%s", projectID, name),
		Identity: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", projectID, name)))),
		Kind:     "directory", Mode: "exclusive"}
	session, _, err := managedharness.NewService(paimosdb.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: name, Harness: "codex", Host: "test-host", SessionRef: "ref-" + name,
		WorkerLease: fleetTestWorkerLease, ManagementMode: managedharness.ManagementManaged, Role: role,
		ParentSessionID: parent, TicketID: ticket, WorkShape: shape, SteerMode: managedharness.SteerOwned,
		Workspace: workspace, DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", AccountLabel: "chatgpt",
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func registerFleetUnmanaged(t *testing.T, projectID int64, name string) models.HarnessSession {
	t.Helper()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,?)`, projectID, name); err != nil {
		t.Fatal(err)
	}
	session, _, err := managedharness.NewService(paimosdb.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: name, Harness: "claude", Host: "test-host", SessionRef: "ref-" + name,
		WorkerLease: fleetTestWorkerLease, ManagementMode: managedharness.ManagementUnmanaged,
		Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone,
		Workspace: &models.HarnessWorkspaceProvenance{CanonicalPath: "/untrusted/workspace",
			Identity: fmt.Sprintf("%x", sha256.Sum256([]byte("untrusted:"+name))), Kind: "directory", Mode: "exclusive"},
		DispatchProfileID: "claude-opus-xhigh", DispatchProfileVersion: "2", AccountLabel: "claude_ai_max",
		Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func addFleetTicket(t *testing.T, projectID, number int64, title string) int64 {
	t.Helper()
	result, err := paimosdb.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,?,'ticket',?,'in-progress')`, projectID, number, title)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func heartbeatFleetSession(t *testing.T, session models.HarnessSession, sequence int64, kind string) {
	t.Helper()
	_, err := managedharness.NewService(paimosdb.DB).HeartbeatWithActivity(context.Background(), session.ID,
		managedharness.PhaseWorking, managedharness.ActivityEvidence{Sequence: sequence, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseZoomClosedBands(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw, zoom, band string
		limit           int
	}{
		{"", "10", "overview", 10}, {"1", "1", "detail", 1}, {"99", "99", "overview", 99},
		{"100", "100", "aggregate", 100}, {strings.Repeat("9", 64), strings.Repeat("9", 64), "far", 100},
	} {
		zoom, band, limit, err := ParseZoom(test.raw)
		if err != nil || zoom != test.zoom || band != test.band || limit != test.limit {
			t.Fatalf("parse %q=(%q,%q,%d,%v)", test.raw, zoom, band, limit, err)
		}
	}
	for _, raw := range []string{"0", "01", "-1", "1.0", strings.Repeat("9", 65)} {
		if _, _, _, err := ParseZoom(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("zoom %q err=%v", raw, err)
		}
	}
}

func TestWorkerFleetV1ContractFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "contracts", "fixtures", "worker-fleet", "snapshot-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != SchemaVersion || !snapshot.SampleTruncated || snapshot.SampleLimit != 2 ||
		snapshot.Totals.OmittedWorkers != 3 || snapshot.Totals.OmittedProjects != 1 || len(snapshot.Workers) != 2 ||
		snapshot.Workers[0].Liveness.Reason != managedharness.ActivityStale ||
		snapshot.Workers[1].Liveness.Reason != managedharness.ActivityUnmanaged ||
		snapshot.Provenance.TerminalGenerationsPerAgent != TerminalGenerationsPerAgent ||
		len(snapshot.Workers[0].RecentCommunication)+int(snapshot.Workers[0].RecentCommunicationOmitted) != 1 ||
		snapshot.Workers[0].RecentCommunication[0].Attribution != "project_agent" {
		t.Fatalf("fixture lost truncation/omission/provenance truth: %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot.Workers[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, addedV2Field := range []string{"machine_id", "workspace_provenance", "dispatch_profile", "account_label", "runtime_provenance_trust", "work_shape", "work_contract"} {
		if strings.Contains(string(encoded), `"`+addedV2Field+`"`) {
			t.Fatalf("v2 field %q entered frozen v1 worker: %s", addedV2Field, encoded)
		}
	}
}

func TestWorkerFleetV2ContractFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "contracts", "fixtures", "worker-fleet", "snapshot-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot SnapshotV2
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != SchemaVersionV2 || snapshot.Provenance.ProjectionVersion != SchemaVersionV2 || len(snapshot.Workers) != 2 {
		t.Fatalf("v2 fixture version drift: %+v", snapshot)
	}
	trusted, untrusted := snapshot.Workers[0], snapshot.Workers[1]
	if trusted.RuntimeProvenanceTrust != RuntimeTrustManagedReporter || trusted.MachineID == nil || trusted.WorkspaceProvenance == nil ||
		trusted.DispatchProfile == nil || trusted.WorkShape != workshape.Ship || trusted.WorkContract.Shape != workshape.Ship {
		t.Fatalf("v2 fixture lost trusted runtime/shape fields: %+v", trusted)
	}
	if untrusted.RuntimeProvenanceTrust != RuntimeTrustUntrusted || untrusted.ManagementMode != managedharness.ManagementUnmanaged ||
		untrusted.MachineID != nil || untrusted.WorkspaceProvenance != nil || untrusted.DispatchProfile != nil || untrusted.AccountLabel != "unknown" || untrusted.AccountKey != "" {
		t.Fatalf("v2 fixture elevated untrusted runtime fields: %+v", untrusted)
	}
}

func TestFleetProjectionBoundedSharedAndTruthful(t *testing.T) {
	testDB := openFleetTestDB(t)
	now := time.Now().UTC()
	ticketID := addFleetTicket(t, testDB.projectOneID, 904, "Fleet projection")
	coordinator := registerFleetSession(t, testDB.projectOneID, "coordinator", managedharness.RoleCoordinator, nil, &ticketID)
	heartbeatFleetSession(t, coordinator, 1, "turn_started")
	parent := coordinator.ID
	worker := registerFleetSession(t, testDB.projectOneID, "worker", managedharness.RoleWorker, &parent, &ticketID)
	heartbeatFleetSession(t, worker, 1, "turn_completed")
	other := registerFleetSession(t, testDB.projectTwoID, "other", managedharness.RoleCoordinator, nil, nil)
	heartbeatFleetSession(t, other, 1, "turn_started")

	reader := NewReader(paimosdb.DB, ReaderOptions{Clock: func() time.Time { return now }, LoadTrust: func(_ context.Context, _ *sql.Tx, _ []int64, _ time.Time) (map[int64]TrustFact, error) {
		progress := 42
		eta := now.Add(time.Hour)
		return map[int64]TrustFact{ticketID: {ProgressTrusted: true, ETATrusted: true, TrustRevision: "trust-v1", ObservedAt: &now, Progress: &progress, ETA: &eta}}, nil
	}})
	portfolio, err := reader.ReadV2(context.Background(), Request{UserID: testDB.userID, Zoom: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if portfolio.Totals != (Totals{Projects: 2, SampledProjects: 1, OmittedProjects: 1, Workers: 3, SampledWorkers: 1, OmittedWorkers: 2}) ||
		!portfolio.SampleTruncated || len(portfolio.Workers) != 1 || portfolio.Workers[0].HarnessSessionID != coordinator.ID {
		t.Fatalf("bounded portfolio=%+v workers=%+v", portfolio.Totals, portfolio.Workers)
	}
	if portfolio.Projects[0].Orchestrator.State != "resolved" || portfolio.Workers[0].Liveness.State != "busy" ||
		!portfolio.Workers[0].Capabilities.Steer || !portfolio.Workers[0].DeliveryTrust.ProgressTrusted ||
		portfolio.Workers[0].DeliveryTrust.Progress == nil || *portfolio.Workers[0].DeliveryTrust.Progress != 42 ||
		portfolio.Workers[0].WorkShape != workshape.Ship || portfolio.Workers[0].WorkContract.OutputKind != workshape.OutputDelivery ||
		portfolio.Workers[0].MachineID == nil || *portfolio.Workers[0].MachineID != "test-host" ||
		portfolio.Workers[0].RuntimeProvenanceTrust != RuntimeTrustManagedReporter || portfolio.Workers[0].WorkspaceProvenance == nil ||
		portfolio.Workers[0].WorkspaceProvenance.Kind != "directory" || portfolio.Workers[0].WorkspaceProvenance.Mode != "exclusive" ||
		portfolio.Workers[0].DispatchProfile == nil || portfolio.Workers[0].DispatchProfile.ID != "codex-sol-high" ||
		portfolio.Workers[0].DispatchProfile.Model != "gpt-6-sol" || portfolio.Workers[0].DispatchProfile.Effort != "high" ||
		portfolio.Workers[0].AccountLabel != "chatgpt" {
		t.Fatalf("truth projection=%+v project=%+v", portfolio.Workers[0], portfolio.Projects[0])
	}
	project, err := reader.ReadV2(context.Background(), Request{UserID: testDB.userID, RouteProjectID: &testDB.projectOneID, Zoom: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if project.Scope.Kind != "project" || len(project.Workers) != 2 || project.Totals.Workers != 2 || project.Totals.Projects != 1 {
		t.Fatalf("project projection=%+v", project)
	}
	if project.Workers[1].HarnessSessionID != worker.ID || !project.Workers[1].ParentInSample || project.Workers[1].Liveness.State != "idle" {
		t.Fatalf("hierarchy projection=%+v", project.Workers[1])
	}
	fullPortfolio, err := reader.ReadV2(context.Background(), Request{UserID: testDB.userID, Zoom: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := mustWorkerJSON(t, findWorker(t, fullPortfolio.Workers, worker.ID)), mustWorkerJSON(t, findWorker(t, project.Workers, worker.ID)); got != want {
		t.Fatalf("project/portfolio disagreement\nportfolio=%s\nproject=%s", got, want)
	}
	if _, err := reader.Read(context.Background(), Request{UserID: testDB.externalID, RouteProjectID: &testDB.projectOneID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("external route oracle err=%v", err)
	}
	if _, err := reader.Read(context.Background(), Request{UserID: testDB.externalID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("external portfolio oracle err=%v", err)
	}
}

func TestFleetProjectionRejectsMalformedPersistedDispatchSnapshot(t *testing.T) {
	testDB := openFleetTestDB(t)
	ticketID := addFleetTicket(t, testDB.projectOneID, 907, "Malformed dispatch")
	session := registerFleetSession(t, testDB.projectOneID, "worker", managedharness.RoleWorker, nil, &ticketID)
	heartbeatFleetSession(t, session, 1, "turn_started")
	if _, err := paimosdb.DB.Exec(`DROP TRIGGER trg_harness_sessions_provenance_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET dispatch_effort='ultra' WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(paimosdb.DB, ReaderOptions{})
	_, err := reader.ReadV2(context.Background(), Request{UserID: testDB.userID, Zoom: "100"})
	if !errors.Is(err, ErrInvariant) || !strings.Contains(err.Error(), "invalid dispatch profile snapshot") {
		t.Fatalf("malformed dispatch snapshot error=%v", err)
	}
	if _, err := reader.Read(context.Background(), Request{UserID: testDB.userID, Zoom: "100"}); err != nil {
		t.Fatalf("frozen v1 was coupled to a v2-only dispatch invariant: %v", err)
	}
}

func TestFleetStaleEvidenceAndRecentCommunicationAreBounded(t *testing.T) {
	testDB := openFleetTestDB(t)
	now := time.Now().UTC()
	ticketID := addFleetTicket(t, testDB.projectOneID, 904, "Fleet projection")
	session := registerFleetSession(t, testDB.projectOneID, "worker", managedharness.RoleWorker, nil, &ticketID)
	heartbeatFleetSession(t, session, 1, "turn_started")
	unmanaged := registerFleetUnmanaged(t, testDB.projectOneID, "external-worker")
	preHeartbeat := registerFleetSession(t, testDB.projectOneID, "legacy-pre-heartbeat", managedharness.RoleWorker, nil, nil)
	old := now.Add(-2 * managedharness.DefaultActivityHeartbeatTimeout).Format("2006-01-02T15:04:05.000Z")
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET heartbeat_at=? WHERE id=?`, old, session.ID); err != nil {
		t.Fatal(err)
	}
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT project_agent_id FROM harness_sessions WHERE id=?`, session.ID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	senderResult, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, testDB.projectOneID)
	if err != nil {
		t.Fatal(err)
	}
	senderID, _ := senderResult.LastInsertId()
	for index := 0; index < 6; index++ {
		messageID := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		result, insertErr := paimosdb.DB.Exec(`INSERT INTO agent_messages(
		 from_agent_id,to_agent_id,body,message_id,context_id,task_id,parts_json,metadata_json,
		 from_address,to_address,thread_id,session_id,delivery_level)
		 VALUES(?,?,?,?,'ALP','ALP-904','[]','{}','paimos:sender','codex:worker','thread','session','steer')`,
			senderID, agentID, "bounded communication", messageID)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		rowID, _ := result.LastInsertId()
		if _, insertErr = paimosdb.DB.Exec(`INSERT INTO agent_message_deliveries(
		 delivery_id,message_row_id,instance,requested_level,state,fallback_reason,last_error_code)
		 VALUES(?,?,'ppm','steer','blocked','not_steerable','adapter_unavailable')`, "delivery-"+messageID, rowID); insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	reader := NewReader(paimosdb.DB, ReaderOptions{Clock: func() time.Time { return now }})
	snapshot, err := reader.ReadV2(context.Background(), Request{UserID: testDB.userID, RouteProjectID: &testDB.projectOneID, Zoom: "10"})
	if err != nil {
		t.Fatal(err)
	}
	worker := findWorker(t, snapshot.Workers, session.ID)
	if worker.Liveness.State != "unknown" || worker.Liveness.Reason != "heartbeat_stale" || worker.Liveness.ReporterAgeSeconds == nil ||
		worker.Capabilities.Steer || !worker.Capabilities.Interrupt || !worker.Capabilities.Stop || len(worker.RecentCommunication) != RecentMessagesPerWorker ||
		worker.RecentCommunicationOmitted != 2 || worker.DeliveryTrust.Reason != "trust_unavailable" || worker.MachineID == nil || *worker.MachineID != "test-host" ||
		worker.RuntimeProvenanceTrust != RuntimeTrustManagedReporter {
		t.Fatalf("stale/bounded worker=%+v", worker)
	}
	unmanagedWorker := findWorker(t, snapshot.Workers, unmanaged.ID)
	if unmanagedWorker.Liveness.State != "unknown" || unmanagedWorker.Liveness.Reason != "unmanaged_evidence" ||
		unmanagedWorker.Liveness.Source != "unmanaged" || unmanagedWorker.Liveness.ReporterAgeSeconds != nil ||
		!unmanagedWorker.Capabilities.Status || unmanagedWorker.Capabilities.Steer || unmanagedWorker.Capabilities.Interrupt || unmanagedWorker.Capabilities.Stop ||
		unmanagedWorker.WorkShape != workshape.Unknown || unmanagedWorker.WorkContract.Shape != workshape.Unknown ||
		unmanagedWorker.RuntimeProvenanceTrust != RuntimeTrustUntrusted || unmanagedWorker.MachineID != nil || unmanagedWorker.WorkspaceProvenance != nil ||
		unmanagedWorker.DispatchProfile != nil || unmanagedWorker.AccountLabel != "unknown" {
		t.Fatalf("unmanaged worker gained inferred truth or controls: %+v", unmanagedWorker)
	}
	legacyWorker := findWorker(t, snapshot.Workers, preHeartbeat.ID)
	if legacyWorker.RuntimeProvenanceTrust != RuntimeTrustUntrusted || legacyWorker.MachineID != nil ||
		legacyWorker.WorkspaceProvenance != nil || legacyWorker.DispatchProfile != nil || legacyWorker.AccountLabel != "unknown" {
		t.Fatalf("pre-heartbeat managed row gained authenticated runtime truth: %+v", legacyWorker)
	}
	raw, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"bounded communication", "parts_json", "metadata_json", "target_ref", "/workspace/", "untrusted/workspace", "workspace_identity", "claude_ai_max"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("private field %q leaked: %s", forbidden, raw)
		}
	}
}

func TestFleetRetainsNewestTerminalGenerationAndToleratesSmallClockSkew(t *testing.T) {
	testDB := openFleetTestDB(t)
	now := time.Now().UTC()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'generation-worker')`, testDB.projectOneID); err != nil {
		t.Fatal(err)
	}
	service := managedharness.NewService(paimosdb.DB)
	register := func(ref string) models.HarnessSession {
		t.Helper()
		session, _, err := service.Register(context.Background(), managedharness.RegisterInput{
			ProjectID: testDB.projectOneID, AgentName: "generation-worker", Harness: "codex", Host: "test-host",
			SessionRef: ref, WorkerLease: fleetTestWorkerLease, ManagementMode: managedharness.ManagementManaged,
			Role: managedharness.RoleWorker, SteerMode: managedharness.SteerNone,
			Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	oldest := register("generation-1")
	if _, err := service.Stop(context.Background(), oldest.ID); err != nil {
		t.Fatal(err)
	}
	newestTerminal := register("generation-2")
	if _, err := service.Stop(context.Background(), newestTerminal.ID); err != nil {
		t.Fatal(err)
	}
	active := register("generation-3")
	future := now.Add(3 * time.Second).Format("2006-01-02T15:04:05.000Z")
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET phase='working',heartbeat_at=?,activity_state='busy',
		activity_reason='adapter_activity',activity_event_kind='turn_started',activity_at=? WHERE id=?`, future, future, active.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewReader(paimosdb.DB, ReaderOptions{Clock: func() time.Time { return now }}).ReadV2(
		context.Background(), Request{UserID: testDB.userID, RouteProjectID: &testDB.projectOneID, Zoom: "100"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Totals.Workers != 2 || snapshot.Provenance.TerminalGenerationsPerAgent != 1 {
		t.Fatalf("terminal retention totals=%+v provenance=%+v", snapshot.Totals, snapshot.Provenance)
	}
	if findWorker(t, snapshot.Workers, active.ID).Liveness.State != managedharness.ActivityBusy {
		t.Fatalf("small server-time skew was treated as malformed: %+v", findWorker(t, snapshot.Workers, active.ID).Liveness)
	}
	findWorker(t, snapshot.Workers, newestTerminal.ID)
	for _, worker := range snapshot.Workers {
		if worker.HarnessSessionID == oldest.ID {
			t.Fatal("superseded terminal generation entered retained fleet")
		}
	}
}

func TestFleetTrustFailureIsConsistentAcrossScopes(t *testing.T) {
	testDB := openFleetTestDB(t)
	ticketID := addFleetTicket(t, testDB.projectOneID, 904, "Fleet projection")
	registerFleetSession(t, testDB.projectOneID, "worker", managedharness.RoleWorker, nil, &ticketID)
	want := errors.New("bounded trust failed")
	reader := NewReader(paimosdb.DB, ReaderOptions{LoadTrust: func(context.Context, *sql.Tx, []int64, time.Time) (map[int64]TrustFact, error) {
		return nil, want
	}})
	for _, request := range []Request{
		{UserID: testDB.userID},
		{UserID: testDB.userID, RouteProjectID: &testDB.projectOneID},
	} {
		if _, err := reader.Read(context.Background(), request); !errors.Is(err, want) {
			t.Fatalf("scope %+v trust err=%v", request, err)
		}
	}
}

func findWorker(t *testing.T, workers []WorkerV2, id string) WorkerV2 {
	t.Helper()
	for _, worker := range workers {
		if worker.HarnessSessionID == id {
			return worker
		}
	}
	t.Fatalf("worker %s absent", id)
	return WorkerV2{}
}

func mustWorkerJSON(t *testing.T, worker WorkerV2) string {
	t.Helper()
	raw, err := json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
