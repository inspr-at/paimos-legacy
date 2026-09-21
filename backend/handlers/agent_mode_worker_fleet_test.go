// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workerfleet"
)

func TestAgentModeWorkerFleetStrictQueryAndClosedEmptyContract(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Fleet project", "FLT")

	response := ts.get(t, "/api/agent-mode/worker-fleet/v1", ts.adminCookie)
	assertStatus(t, response, http.StatusOK)
	if response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache policy=%q", response.Header.Get("Cache-Control"))
	}
	var snapshot workerfleet.Snapshot
	decode(t, response, &snapshot)
	if snapshot.SchemaVersion != 1 || snapshot.Scope.Kind != "portfolio" || snapshot.Zoom != "10" ||
		snapshot.Band != "overview" || snapshot.SampleLimit != 10 || snapshot.Workers == nil || snapshot.Projects == nil ||
		snapshot.Provenance.Source != "authoritative_database" || snapshot.Provenance.Cache != "none" || snapshot.Provenance.RemoteCache {
		t.Fatalf("empty contract=%+v", snapshot)
	}
	for _, query := range []string{"zoom=", "zoom=0", "zoom=01", "zoom=1.0", "other=1", "zoom=1&zoom=2", "zoom=" + strings.Repeat("9", 65)} {
		invalid := ts.get(t, "/api/agent-mode/worker-fleet/v1?"+query, ts.adminCookie)
		assertStatus(t, invalid, http.StatusBadRequest)
		if invalid.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("invalid cache policy=%q", invalid.Header.Get("Cache-Control"))
		}
	}
	projectResponse := ts.get(t, "/api/agent-mode/projects/"+itoa(projectID)+"/worker-fleet/v1?zoom=1", ts.adminCookie)
	assertStatus(t, projectResponse, http.StatusOK)
	var projectSnapshot workerfleet.Snapshot
	decode(t, projectResponse, &projectSnapshot)
	if projectSnapshot.Scope.Kind != "project" || projectSnapshot.Scope.ProjectID == nil ||
		*projectSnapshot.Scope.ProjectID != projectID || projectSnapshot.SampleLimit != 1 {
		t.Fatalf("project route contract=%+v", projectSnapshot)
	}
	v2Response := ts.get(t, "/api/agent-mode/projects/"+itoa(projectID)+"/worker-fleet/v2?zoom=1", ts.adminCookie)
	assertStatus(t, v2Response, http.StatusOK)
	var v2Snapshot workerfleet.SnapshotV2
	decode(t, v2Response, &v2Snapshot)
	if v2Snapshot.SchemaVersion != workerfleet.SchemaVersionV2 || v2Snapshot.Provenance.ProjectionVersion != workerfleet.SchemaVersionV2 {
		t.Fatalf("v2 route contract=%+v", v2Snapshot)
	}
}

func TestAgentModeWorkerFleetVersionsDoNotCrossWireContracts(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Versioned fleet", "VFL")
	issueResult, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,907,'ticket','Versioned worker','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issueResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'versioned-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	session, _, err := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "versioned-worker", Harness: "codex", Host: "trusted-host",
		SessionRef: "versioned-generation", WorkerLease: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker,
		TicketID: &issueID, WorkShape: "ship", SteerMode: managedharness.SteerNone,
		Workspace: &models.HarnessWorkspaceProvenance{CanonicalPath: "/versioned/workspace",
			Identity: strings.Repeat("a", 64), Kind: "directory", Mode: "exclusive"},
		DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", AccountLabel: "chatgpt",
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedharness.NewService(db.DB).HeartbeatWithActivity(context.Background(), session.ID,
		managedharness.PhaseWorking, managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		t.Fatal(err)
	}
	type rawSnapshot struct {
		SchemaVersion int              `json:"schema_version"`
		Workers       []map[string]any `json:"workers"`
		Provenance    struct {
			ProjectionVersion int `json:"projection_version"`
		} `json:"provenance"`
	}
	v1Response := ts.get(t, "/api/agent-mode/projects/"+itoa(projectID)+"/worker-fleet/v1?zoom=100", ts.adminCookie)
	assertStatus(t, v1Response, http.StatusOK)
	var v1 rawSnapshot
	decode(t, v1Response, &v1)
	if v1.SchemaVersion != 1 || v1.Provenance.ProjectionVersion != 1 || len(v1.Workers) != 1 {
		t.Fatalf("v1 version contract=%+v", v1)
	}
	for _, v2Only := range []string{"machine_id", "workspace_provenance", "dispatch_profile", "account_label", "account_key", "runtime_provenance_trust", "work_shape", "work_contract"} {
		if _, exists := v1.Workers[0][v2Only]; exists {
			t.Fatalf("v2-only field %q entered v1: %+v", v2Only, v1.Workers[0])
		}
	}
	v2Response := ts.get(t, "/api/agent-mode/projects/"+itoa(projectID)+"/worker-fleet/v2?zoom=100", ts.adminCookie)
	assertStatus(t, v2Response, http.StatusOK)
	var v2 rawSnapshot
	decode(t, v2Response, &v2)
	if v2.SchemaVersion != 2 || v2.Provenance.ProjectionVersion != 2 || len(v2.Workers) != 1 ||
		v2.Workers[0]["runtime_provenance_trust"] != workerfleet.RuntimeTrustManagedReporter ||
		v2.Workers[0]["machine_id"] != "trusted-host" || v2.Workers[0]["work_shape"] != "ship" {
		t.Fatalf("v2 version/runtime contract=%+v", v2)
	}
}

func TestAgentModeWorkerFleetConcealsUnauthorizedAndMissingProjects(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Concealed fleet", "CFL")
	paths := []string{
		"/api/agent-mode/projects/999999/worker-fleet/v1",
		"/api/agent-mode/projects/" + itoa(projectID) + "/worker-fleet/v1",
		"/api/agent-mode/projects/999999/worker-fleet/v2",
		"/api/agent-mode/projects/" + itoa(projectID) + "/worker-fleet/v2",
	}
	for _, path := range paths {
		missing := ts.get(t, path, ts.externalCookie)
		assertStatus(t, missing, http.StatusNotFound)
		body, _ := io.ReadAll(missing.Body)
		var problem map[string]any
		if err := json.Unmarshal(body, &problem); err != nil || problem["status"] != float64(http.StatusNotFound) {
			t.Fatalf("concealed response path=%s body=%s err=%v", path, body, err)
		}
		if missing.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("concealed cache policy=%q", missing.Header.Get("Cache-Control"))
		}
	}
	missing := ts.get(t, paths[0], ts.adminCookie)
	assertStatus(t, missing, http.StatusNotFound)
}

func TestAgentModeWorkerFleetKeepsStoppedWorkerWhenBoundTicketIsSoftDeleted(t *testing.T) {
	ts := newTestServer(t)
	projectID := seedBatchProject(t, "Deleted ticket fleet", "DTF")
	issueResult, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,904,'ticket','Deleted binding','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issueResult.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'stopped-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	session, _, err := managedharness.NewService(db.DB).Register(context.Background(), managedharness.RegisterInput{
		ProjectID: projectID, AgentName: "stopped-worker", Harness: "codex", Host: "test-host",
		SessionRef: "deleted-ticket-generation", WorkerLease: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ManagementMode: managedharness.ManagementManaged, Role: managedharness.RoleWorker,
		TicketID: &issueID, WorkShape: "ship", SteerMode: managedharness.SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedharness.NewService(db.DB).Stop(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE issues SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/agent-mode/worker-fleet/v1?zoom=100",
		"/api/agent-mode/projects/" + itoa(projectID) + "/worker-fleet/v1?zoom=100",
	} {
		response := ts.get(t, path, ts.adminCookie)
		assertStatus(t, response, http.StatusOK)
		var snapshot workerfleet.Snapshot
		decode(t, response, &snapshot)
		if len(snapshot.Workers) != 1 || snapshot.Workers[0].HarnessSessionID != session.ID ||
			snapshot.Workers[0].Ticket == nil || snapshot.Workers[0].Ticket.ID != issueID ||
			snapshot.Workers[0].Ticket.DetailsAvailable || snapshot.Workers[0].DeliveryTrust.Reason != "trust_unavailable" {
			t.Fatalf("soft-deleted ticket fleet path=%s snapshot=%+v", path, snapshot)
		}
	}
}
