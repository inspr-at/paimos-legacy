// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/workerfleet"
)

// OrchestrationSnapshotV1 composes the frozen fleet v2 contract without giving
// instance coordination the authority of a same-project session parent edge.
type OrchestrationSnapshotV1 struct {
	SchemaVersion       int                    `json:"schema_version"`
	InstanceRoot        OrchestrationRoot      `json:"instance_root"`
	Fleet               workerfleet.SnapshotV2 `json:"fleet"`
	ProjectCoordination []ProjectCoordination  `json:"project_coordination"`
	CoordinationBounds  CoordinationBounds     `json:"coordination_bounds"`
}

type OrchestrationRoot struct {
	ConfiguredIdentity *models.OrchestratorProjection `json:"configured_identity"`
	BindingRevision    int64                          `json:"binding_revision"`
	BindingUpdatedAt   *string                        `json:"binding_updated_at"`
	ActiveGeneration   workerfleet.Orchestrator       `json:"active_generation"`
}

type ProjectCoordination struct {
	Project             workerfleet.ProjectIdentity `json:"project"`
	Coordinator         workerfleet.Orchestrator    `json:"coordinator"`
	Relationship        string                      `json:"relationship"`
	RootBindingRevision *int64                      `json:"root_binding_revision"`
}

type CoordinationBounds struct {
	SampleLimit     int   `json:"sample_limit"`
	TotalProjects   int64 `json:"total_projects"`
	SampledProjects int   `json:"sampled_projects"`
	OmittedProjects int64 `json:"omitted_projects"`
}

func RegisterOrchestrationProjectionRoutes(r chi.Router) {
	r.Get("/orchestration/v1", AgentModeOrchestration)
	r.Get("/projects/{projectID}/orchestration/v1", AgentModeProjectOrchestration)
}

func AgentModeOrchestration(w http.ResponseWriter, r *http.Request) {
	serveOrchestration(w, r, false)
}

func AgentModeProjectOrchestration(w http.ResponseWriter, r *http.Request) {
	serveOrchestration(w, r, true)
}

func serveOrchestration(w http.ResponseWriter, r *http.Request, projectRoute bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	user := auth.GetUser(r)
	if user == nil || user.ID <= 0 {
		workerFleetError(w, r, workerfleet.ErrNotFound)
		return
	}
	request, err := orchestrationRequest(r, user.ID, projectRoute)
	if err != nil {
		workerFleetError(w, r, err)
		return
	}
	snapshot, err := readOrchestration(r.Context(), db.DB, request, loadWorkerFleetTrust)
	if err != nil {
		workerFleetError(w, r, err)
		return
	}
	jsonOK(w, snapshot)
}

func orchestrationRequest(r *http.Request, userID int64, projectRoute bool) (workerfleet.Request, error) {
	request := workerfleet.Request{UserID: userID}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return request, workerfleet.ErrInvalid
	}
	for key, entries := range values {
		if key != "zoom" || len(entries) != 1 || entries[0] == "" {
			return request, workerfleet.ErrInvalid
		}
		request.Zoom = entries[0]
	}
	if projectRoute {
		id, ok := strictQueryInt(chi.URLParam(r, "projectID"), 1, 1<<63-1)
		if !ok {
			return request, workerfleet.ErrInvalid
		}
		request.RouteProjectID = &id
	}
	return request, nil
}

// The fleet's transactional enrichment callback lets every composed fact share
// its authorized SQLite snapshot and observation clock. Reading root identity
// before/after ReadV2 in a separate transaction could mix replacement generations.
func readOrchestration(ctx context.Context, database *sql.DB, request workerfleet.Request, trust workerfleet.TrustLoader) (OrchestrationSnapshotV1, error) {
	out := OrchestrationSnapshotV1{SchemaVersion: 1, ProjectCoordination: []ProjectCoordination{}}
	var rootCandidate sql.NullString
	var emptyProjects []workerfleet.ProjectIdentity
	reader := workerfleet.NewReader(database, workerfleet.ReaderOptions{LoadTrust: func(ctx context.Context, tx *sql.Tx, ids []int64, at time.Time) (map[int64]workerfleet.TrustFact, error) {
		state, err := loadOrchestratorState(ctx, tx)
		if err != nil {
			return nil, err
		}
		var reviewer bool
		if err := tx.QueryRowContext(ctx, "SELECT is_reviewer FROM users WHERE id=?", request.UserID).Scan(&reviewer); err != nil {
			return nil, err
		}
		if reviewer {
			// The instance root can belong to a production project. Neither its
			// label nor its binding revision is part of a reviewer's sandbox.
			state = orchestratorState{}
		}
		out.InstanceRoot = OrchestrationRoot{BindingRevision: state.Revision, BindingUpdatedAt: state.UpdatedAt,
			ActiveGeneration: workerfleet.Orchestrator{State: "unset", Reason: "root_not_configured"}}
		if reviewer {
			out.InstanceRoot.ActiveGeneration = workerfleet.Orchestrator{State: "unknown", Reason: "reviewer_scope"}
		}
		if state.Target != nil {
			out.InstanceRoot.ConfiguredIdentity = &models.OrchestratorProjection{DisplayLabel: state.Target.DisplayLabel}
			out.InstanceRoot.ActiveGeneration = workerfleet.Orchestrator{State: "unknown", Reason: "generation_unavailable"}
			var visible bool
			err = tx.QueryRowContext(ctx, auth.AgentModeAuthorizationCTE+`
    SELECT EXISTS(SELECT 1 FROM agent_mode_projects WHERE project_id=?)`, request.UserID, state.Target.ProjectID).Scan(&visible)
			if err != nil {
				return nil, err
			}
			if visible {
				var candidates int
				err = tx.QueryRowContext(ctx, `SELECT COUNT(*),MAX(id) FROM harness_sessions
     WHERE project_id=? AND project_agent_id=? AND role='coordinator' AND phase<>'stopped'`,
					state.Target.ProjectID, state.Target.ProjectAgentID).Scan(&candidates, &rootCandidate)
				if err != nil {
					return nil, err
				}
				switch {
				case candidates == 0:
					out.InstanceRoot.ActiveGeneration = workerfleet.Orchestrator{State: "unset", Reason: "no_active_root_generation"}
				case candidates > 1:
					out.InstanceRoot.ActiveGeneration = workerfleet.Orchestrator{State: "ambiguous", Reason: "multiple_active_root_generations"}
					rootCandidate = sql.NullString{}
				default:
					out.InstanceRoot.ActiveGeneration = workerfleet.Orchestrator{State: "unknown", Reason: "generation_not_in_sample"}
				}
			}
		}
		routeID := int64(0)
		if request.RouteProjectID != nil {
			routeID = *request.RouteProjectID
		}
		_, _, limit, err := workerfleet.ParseZoom(request.Zoom)
		if err != nil {
			return nil, err
		}
		err = tx.QueryRowContext(ctx, auth.AgentModeAuthorizationCTE+`
   SELECT COUNT(*) FROM agent_mode_projects WHERE (?=0 OR project_id=?)`, request.UserID, routeID, routeID).Scan(&out.CoordinationBounds.TotalProjects)
		if err != nil {
			return nil, err
		}
		// Fleet already samples every populated tree. Add empty trees without
		// loading an unbounded catalog or exposing inaccessible project counts.
		rows, err := tx.QueryContext(ctx, auth.AgentModeAuthorizationCTE+`
   SELECT p.id,p.key,p.name FROM projects p JOIN agent_mode_projects a ON a.project_id=p.id
   WHERE (?=0 OR p.id=?) AND NOT EXISTS(SELECT 1 FROM harness_sessions hs WHERE hs.project_id=p.id)
   ORDER BY p.key,p.id LIMIT ?`, request.UserID, routeID, routeID, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p workerfleet.ProjectIdentity
			if err := rows.Scan(&p.ID, &p.Key, &p.Name); err != nil {
				rows.Close()
				return nil, err
			}
			emptyProjects = append(emptyProjects, p)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if trust != nil {
			return trust(ctx, tx, ids, at)
		}
		return map[int64]workerfleet.TrustFact{}, nil
	}})
	fleet, err := reader.ReadV2(ctx, request)
	if err != nil {
		return OrchestrationSnapshotV1{}, err
	}
	out.Fleet = fleet
	if rootCandidate.Valid {
		for _, worker := range fleet.Workers {
			if worker.HarnessSessionID != rootCandidate.String {
				continue
			}
			out.InstanceRoot.ActiveGeneration.Reason = "root_generation_unknown"
			if worker.Liveness.State == "busy" || worker.Liveness.State == "idle" {
				id := worker.HarnessSessionID
				out.InstanceRoot.ActiveGeneration = workerfleet.Orchestrator{State: "resolved", Reason: "single_active_root_generation", SessionID: &id}
			}
			break
		}
	}
	var revision *int64
	if out.InstanceRoot.ConfiguredIdentity != nil {
		revision = &out.InstanceRoot.BindingRevision
	}
	for _, project := range fleet.Projects {
		out.ProjectCoordination = append(out.ProjectCoordination, ProjectCoordination{
			Project:     workerfleet.ProjectIdentity{ID: project.ID, Key: project.Key, Name: project.Name},
			Coordinator: project.Orchestrator, Relationship: "instance_root_coordination", RootBindingRevision: revision})
	}
	for _, project := range emptyProjects {
		if len(out.ProjectCoordination) >= fleet.SampleLimit {
			break
		}
		out.ProjectCoordination = append(out.ProjectCoordination, ProjectCoordination{Project: project,
			Coordinator:  workerfleet.Orchestrator{State: "unset", Reason: "no_active_coordinator"},
			Relationship: "instance_root_coordination", RootBindingRevision: revision})
	}
	sort.Slice(out.ProjectCoordination, func(i, j int) bool {
		left, right := out.ProjectCoordination[i].Project, out.ProjectCoordination[j].Project
		if left.Key != right.Key {
			return left.Key < right.Key
		}
		return left.ID < right.ID
	})
	out.CoordinationBounds.SampleLimit = fleet.SampleLimit
	out.CoordinationBounds.SampledProjects = len(out.ProjectCoordination)
	out.CoordinationBounds.OmittedProjects = out.CoordinationBounds.TotalProjects - int64(len(out.ProjectCoordination))
	return out, nil
}
