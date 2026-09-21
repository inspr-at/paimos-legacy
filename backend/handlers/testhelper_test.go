// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package handlers_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
	"github.com/inspr-at/paimos/backend/handlers/knowledge"
	"github.com/inspr-at/paimos/backend/httpcontract"
	"github.com/inspr-at/paimos/backend/internal/testdb"
	"github.com/inspr-at/paimos/backend/publicbase"

	_ "modernc.org/sqlite"
)

// testServer holds a running httptest.Server and convenience helpers.
type testServer struct {
	srv            *httptest.Server
	adminCookie    string
	memberCookie   string
	externalCookie string
}

// newTestServer opens an isolated copy of the migrated SQLite template, seeds
// admin + member users, wires the real router, and starts an httptest.Server.
func newTestServer(t *testing.T) *testServer {
	t.Helper()

	// Keep every test process on its own temporary SQLite DSN and restore the
	// environment during cleanup, including when a test fails early.
	t.Setenv("DATA_DIR", t.TempDir())
	// Retain the existing test-mode connection behavior.
	t.Setenv("PAIMOS_TEST_MODE", "1")

	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})

	// Seed admin user.
	adminHash := bcryptHash(t, "adminpass")
	adminRes, _ := db.DB.Exec("INSERT INTO users(username,password,role,status) VALUES(?,?,?,?)", "admin", adminHash, "admin", "active")
	if adminRes != nil {
		if id, _ := adminRes.LastInsertId(); id > 0 {
			auth.SeedAccessForUser(id, "admin")
		}
	}

	// Seed member user.
	memberHash := bcryptHash(t, "memberpass")
	memberRes, _ := db.DB.Exec("INSERT INTO users(username,password,role,status) VALUES(?,?,?,?)", "member", memberHash, "member", "active")
	if memberRes != nil {
		if id, _ := memberRes.LastInsertId(); id > 0 {
			auth.SeedAccessForUser(id, "member")
		}
	}

	// Seed external user. Externals are not auto-seeded — access is granted per-project.
	externalHash := bcryptHash(t, "externalpass")
	db.DB.Exec("INSERT INTO users(username,password,role,status) VALUES(?,?,?,?)", "external", externalHash, "external", "active")

	// Seed a global tag.
	db.DB.Exec("INSERT INTO tags(name,color,description) VALUES(?,?,?)", "bug", "red", "Bug tag")

	// Ensure system tags (At Risk etc.) are set up — mirrors main.go
	handlers.EnsureAtRiskTag()

	// In-memory limiter state survives across tests in one package run;
	// reset it like the DB so cases stay independent (PAI-724).
	handlers.ResetVoiceLimitsForTest()

	r := buildRouter()
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	ts := &testServer{srv: srv}
	ts.adminCookie = ts.login(t, "admin", "adminpass")
	ts.memberCookie = ts.login(t, "member", "memberpass")
	ts.externalCookie = ts.login(t, "external", "externalpass")
	return ts
}

// buildRouter mirrors main.go router setup but without static file serving.
func buildRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(handlers.ClassifiedControlCachePolicyMiddleware)
	r.Use(handlers.ControlAwareRecoverer)
	r.Use(publicbase.RejectOutsideAndStrip(publicbase.Current()))
	r.Use(handlers.OfferPrivacyMiddleware)
	r.Use(handlers.SessionAuditMiddleware) // PAI-97 — off unless PAIMOS_AUDIT_SESSIONS=true
	r.Use(handlers.RequestIDMiddleware)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		})

		// Public whitelist — mirrors main.go exactly. ACME-1 relies
		// on this list being minimal.
		handlers.RegisterPublicOfferRoutes(r)
		r.Get("/branding", handlers.GetBranding)
		r.Post("/auth/login", auth.LoginHandler)
		r.Post("/auth/forgot", handlers.ForgotPassword)
		r.Get("/auth/reset/validate", handlers.ValidateResetToken)
		r.Post("/auth/reset", handlers.ResetPassword)

		// Auth (open to all roles) — mirrors main.go group including
		// the four endpoints moved inside the auth group by ACME-1.
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware)
			r.Use(auth.MustChangePasswordGate) // PAI-321
			r.Post("/auth/logout", auth.LogoutHandler)
			r.Get("/auth/me", auth.MeHandler)
			r.Post("/auth/impersonation/start", handlers.StartImpersonation)
			r.Post("/auth/impersonation/end", handlers.EndImpersonation)
			r.Patch("/auth/me", handlers.UpdateProfile)
			r.Post("/auth/password", auth.ChangePassword)
			r.Get("/instance", func(w http.ResponseWriter, req *http.Request) {
				// Minimal stand-in for the real instanceHandler — the test
				// only asserts that this endpoint is *auth-gated*, not the
				// content of the response.
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"label":"TEST","hostname":"test"}`))
			})
			r.Get("/brandings", handlers.ListBrandings)
			r.Get("/projektberichte/accept/{code}", handlers.GetProjectReportAcceptance)
			r.Post("/projektberichte/accept/{code}", handlers.AcceptProjectReport)
			r.Put("/projektberichte/accept/{code}/signed", handlers.LinkProjectReportSignedArtifact)
			r.Get("/projektberichte/{code}/pdf", handlers.GetProjectReportPDF)
			r.Get("/logos/{filename}", func(w http.ResponseWriter, req *http.Request) {
				// Same — just a stub that returns 200 so the authed
				// probe doesn't get a 404 disguising a routing bug.
				w.WriteHeader(http.StatusOK)
			})
			r.Get("/avatars/{filename}", func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Post("/runs/{id}/telemetry", handlers.IngestAgentRunTelemetry)
			r.Get("/runs/{id}/telemetry", handlers.ListAgentRunTelemetry)
			r.Get("/runs/{id}/telemetry/latest", handlers.GetLatestAgentRunTelemetry)
		})

		r.Route("/agent-mode", func(r chi.Router) {
			r.Use(auth.AgentModePrivateNoStore)
			r.Use(auth.Middleware)
			r.Use(auth.RequireAgentModeInternal)
			r.Use(auth.CSRFMiddleware)
			r.Use(auth.MustChangePasswordGate)
			r.NotFound(httpcontract.WriteAgentModeNotFound)
			r.MethodNotAllowed(httpcontract.WriteAgentModeNotFound)
			r.Get("/deliveries/events", handlers.AgentModeEvents)
			r.Get("/deliveries", handlers.AgentModeDeliveries)
			r.Get("/projects/{projectID}/deliveries", handlers.AgentModeProjectDeliveries)
			r.Get("/deliveries/{deliveryKey}", handlers.AgentModeDelivery)
			r.Get("/worker-fleet/v1", handlers.AgentModeWorkerFleet)
			r.Get("/projects/{projectID}/worker-fleet/v1", handlers.AgentModeProjectWorkerFleet)
			r.Get("/worker-fleet/v2", handlers.AgentModeWorkerFleetV2)
			r.Get("/projects/{projectID}/worker-fleet/v2", handlers.AgentModeProjectWorkerFleetV2)
			r.Post("/voice/transcribe", handlers.TranscribeAgentModeVoice)
			r.Post("/voice/speak", handlers.SpeakAgentModeVoice)
		})

		// PAI-861 mirrors production's dedicated middleware order: no-store
		// must run before authentication/password/external early exits.
		r.Group(func(r chi.Router) {
			r.Use(auth.AgentModePrivateNoStore)
			r.Use(auth.Middleware)
			r.Use(auth.CSRFMiddleware)
			r.Use(auth.MustChangePasswordGate)
			r.Use(auth.BlockExternal)
			handlers.RegisterSessionHomeRoutes(r)
			handlers.RegisterOrchestratorRoutes(r)
			handlers.RegisterCommandPaletteRoutes(r)
		})

		// PAI-863 mirrors production's sole dedicated private/no-store group.
		r.Group(func(r chi.Router) {
			r.Use(auth.AgentModePrivateNoStore)
			r.Use(auth.Middleware)
			r.Use(auth.CSRFMiddleware)
			r.Use(auth.MustChangePasswordGate)
			r.Use(auth.BlockExternal)
			handlers.RegisterStructuredKnowledgeRoutes(r)
		})

		// Portal (external + admin)
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware)
			r.Use(auth.MustChangePasswordGate) // PAI-321
			r.Use(auth.RequirePortalAccess)
			r.Get("/portal/overview", handlers.PortalOverview)
			r.Get("/portal/projects", handlers.PortalListProjects)
			r.Get("/portal/projects/{id}", handlers.PortalGetProject)
			r.Get("/portal/projects/{id}/issues", handlers.PortalListIssues)
			r.Get("/portal/projects/{id}/issues/{issueId}", handlers.PortalGetIssue)
			r.Post("/portal/projects/{id}/requests", handlers.PortalSubmitRequest)
			r.Get("/portal/issues/{id}/comments", handlers.PortalListIssueComments)
			r.Post("/portal/issues/{id}/accept", handlers.PortalAcceptIssue)
			r.Get("/portal/projects/{id}/summary", handlers.PortalProjectSummary)
			r.Get("/portal/projects/{id}/projektberichte", handlers.ListProjectReports)
			handlers.RegisterPortalReleaseAcceptanceRoutes(r)
		})

		// Internal (blocked for external)
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware)
			r.Use(auth.MustChangePasswordGate) // PAI-321
			r.Use(auth.BlockExternal)

			r.Get("/changes", handlers.ChangesStream)

			r.Get("/projects", handlers.ListProjects)
			r.With(auth.RequireAdmin).Post("/projects", handlers.CreateProject)
			r.With(auth.RequireProjectView).Get("/projects/{id}", handlers.GetProject)
			r.With(auth.RequireAdmin).Put("/projects/{id}", handlers.UpdateProject)
			r.With(auth.RequireAdmin).Delete("/projects/{id}", handlers.DeleteProject)
			r.With(auth.RequireProjectView).Get("/projects/{id}/repos", handlers.ListProjectRepos)
			r.With(auth.RequireProjectEdit).Post("/projects/{id}/repos", handlers.CreateProjectRepo)
			r.With(auth.RequireProjectEdit).Put("/projects/{id}/repos/{repoId}", handlers.UpdateProjectRepo)
			r.With(auth.RequireProjectEdit).Delete("/projects/{id}/repos/{repoId}", handlers.DeleteProjectRepo)
			r.With(auth.RequireProjectView).Get("/projects/{id}/anchors", handlers.ListProjectAnchors)
			// PAI-326 — declarable agents. Mirrors main.go auth.
			r.With(auth.RequireProjectView).Get("/projects/{id}/agents", handlers.ListProjectAgents)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/agents", handlers.CreateProjectAgent)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/agents/{name}", handlers.UpdateProjectAgent)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/agents/{name}", handlers.DeleteProjectAgent)
			// PAI-329 — canonical agent artifact + project-level
			// inventories (environments, deploy recipes).
			r.With(auth.RequireProjectView).Get("/projects/{id}/agents/{name}.json", handlers.GetProjectAgentArtifact)
			r.With(auth.RequireProjectView).Get("/projects/{id}/agents/{name}.md", handlers.GetProjectAgentArtifactMarkdown)
			handlers.RegisterAgentMessageRoutes(r)
			handlers.RegisterHarnessSessionRoutes(r)
			handlers.RegisterProductSessionRoutes(r)
			handlers.RegisterNodeRoutes(r)
			r.With(auth.RequireProjectView).Get("/projects/{id}/environments", handlers.ListProjectEnvironments)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/environments", handlers.CreateProjectEnvironment)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/environments/{envId}", handlers.UpdateProjectEnvironment)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/environments/{envId}", handlers.DeleteProjectEnvironment)
			r.With(auth.RequireProjectView).Get("/projects/{id}/deploy-recipes", handlers.ListProjectDeployRecipes)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/deploy-recipes", handlers.CreateProjectDeployRecipe)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/deploy-recipes/{recipeId}", handlers.UpdateProjectDeployRecipe)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/deploy-recipes/{recipeId}", handlers.DeleteProjectDeployRecipe)

			// PAI-394 — unified knowledge surface. Mirror main.go.
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge", knowledge.ListAllHandler)
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/graph", knowledge.GraphHandler)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/knowledge", knowledge.CreateHandler)
			r.With(auth.RequireProjectView).Post("/projects/{id}/knowledge/memory/references", handlers.BumpMemoryReferences)
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/stale", handlers.ListStaleMemory)
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/proposed/stale", handlers.ListStaleProposedMemory)
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/needs-review", knowledge.NeedsReviewHandler)
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/memory/{slug}/dependents", knowledge.MemoryDependentsHandler)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/knowledge/memory/{slug}/reviewed", handlers.MarkMemoryReviewed)
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/{type}/{slug}", knowledge.GetHandler)
			r.With(auth.RequireProjectView).Get("/projects/{id}/knowledge/{type}/{slug}.rev", handlers.KnowledgeRevHandler)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/knowledge/{type}/{slug}", knowledge.UpdateHandler)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Delete("/projects/{id}/knowledge/{type}/{slug}", knowledge.DeleteHandler)

			// PAI-345 — user / instance memory + promotion. Mirror main.go.
			r.Get("/users/me/memory", handlers.ListUserMemory)
			r.Post("/users/me/memory", handlers.CreateUserMemory)
			r.Get("/users/me/memory/{slug}", handlers.GetUserMemory)
			r.Put("/users/me/memory/{slug}", handlers.UpdateUserMemory)
			r.Delete("/users/me/memory/{slug}", handlers.DeleteUserMemory)
			r.Get("/instance/memory", handlers.ListInstanceMemory)
			r.With(auth.RequireAdmin).Post("/instance/memory", handlers.CreateInstanceMemory)
			r.Get("/instance/memory/{slug}", handlers.GetInstanceMemory)
			r.With(auth.RequireAdmin).Put("/instance/memory/{slug}", handlers.UpdateInstanceMemory)
			r.With(auth.RequireAdmin).Delete("/instance/memory/{slug}", handlers.DeleteInstanceMemory)
			r.Post("/memory/{slug}/promote", handlers.PromoteMemory)

			// PAI-394 moved PAI-347 / PAI-349 memory subroutes
			// under /knowledge/memory/ — registered above.
			// PAI-358: manifest endpoints removed.
			r.With(auth.RequireProjectEdit).Post("/projects/{id}/anchors", handlers.IngestProjectAnchors)
			r.With(auth.RequireProjectView).Get("/projects/{id}/graph", handlers.ListProjectEntityRelations)
			r.With(auth.RequireProjectView).Get("/projects/{id}/graph/blast-radius", handlers.BlastRadius)
			r.With(auth.RequireProjectView).Post("/projects/{id}/retrieve", handlers.RetrieveProjectContext)
			handlers.RegisterBaselineBatchRoutes(r)
			handlers.RegisterReleaseAcceptanceRoutes(r)
			r.Get("/projects/suggest-key", handlers.SuggestProjectKey)

			r.With(auth.RequireProjectView).Get("/projects/{id}/issues", handlers.ListIssues)
			r.With(auth.RequireProjectView).Get("/projects/{id}/issues/tree", handlers.GetIssueTree)
			r.With(auth.RequireProjectEdit, handlers.IdempotencyMiddleware).Post("/projects/{id}/issues", handlers.CreateIssue)
			r.With(auth.RequireProjectView).Get("/projects/{id}/cost-units", handlers.ListCostUnits)
			r.With(auth.RequireProjectView).Get("/projects/{id}/releases", handlers.ListReleases)

			r.Get("/issues/recent", handlers.RecentIssues)
			r.With(auth.RequireAdmin).Get("/issues/trash", handlers.ListTrashIssues)
			r.Get("/issues", handlers.ListOrLookupIssues)
			r.With(auth.RequireAdmin).Patch("/issues", handlers.UpdateIssuesBatch)
			r.With(auth.RequireAdmin, handlers.IdempotencyMiddleware).Post("/projects/{key}/issues/batch", handlers.CreateIssuesBatch)
			r.With(auth.RequireIssueAccess).Get("/issues/{id}", handlers.GetIssue)
			r.With(auth.RequireIssueEdit).Put("/issues/{id}", handlers.UpdateIssue)
			r.With(auth.RequireIssueEdit).Patch("/issues/{id}", handlers.UpdateIssue)
			r.With(auth.RequireIssueEdit).Post("/issues/{id}/move", handlers.MoveIssue)
			r.Post("/issues/move", handlers.MoveIssuesBulk)
			r.With(auth.RequireIssueEdit).Post("/issues/{id}/clone", handlers.CloneIssue)
			// PAI-606: "Implement this" run lifecycle (mirrors main.go).
			r.With(auth.RequireIssueEdit).Post("/issues/{id}/implement", handlers.ImplementIssue)
			r.With(auth.RequireIssueAccess).Get("/issues/{id}/runs", handlers.ListIssueRuns)
			r.Get("/runs/{id}", handlers.GetAgentRun)
			r.Patch("/runs/{id}", handlers.PatchAgentRun)
			r.With(auth.RequireAdmin).Delete("/issues/{id}", handlers.DeleteIssue)
			r.With(auth.RequireAdmin).Post("/issues/{id}/restore", handlers.RestoreIssue)
			r.With(auth.RequireAdmin).Delete("/issues/{id}/purge", handlers.PurgeIssue)
			r.Get("/issues/{id}/history", handlers.GetIssueHistory)
			r.Get("/issues/{id}/children", handlers.GetIssueChildren)
			r.Get("/issues/{id}/anchors", handlers.ListIssueAnchors)
			r.Get("/issues/{id}/ai-activity", handlers.AIListIssueActivity)
			r.Get("/issues/{id}/activity", handlers.ListIssueMutationActivity)
			// PAI-704: voice-intake workbench sessions (mirrors main.go).
			r.Post("/intake/sessions", handlers.CreateIntakeSession)
			r.Get("/intake/sessions", handlers.ListIntakeSessions)
			r.Get("/intake/sessions/{id}", handlers.GetIntakeSession)
			r.Patch("/intake/sessions/{id}", handlers.PatchIntakeSession)
			r.Delete("/intake/sessions/{id}", handlers.AbandonIntakeSession)
			r.Post("/intake/sessions/{id}/transcript", handlers.IngestIntakeTranscript)
			r.Post("/intake/sessions/{id}/checkpoints", handlers.CreateIntakeCheckpoint)
			r.Get("/intake/sessions/{id}/events", handlers.ListIntakeEvents)
			r.Get("/intake/sessions/{id}/state", handlers.GetIntakeState)
			r.Post("/intake/sessions/{id}/restore", handlers.RestoreIntakeSession)
			r.Post("/intake/sessions/{id}/refresh", handlers.RefreshIntakeSession)
			r.Post("/intake/sessions/{id}/audio", handlers.TranscribeIntakeAudio)
			r.Post("/intake/sessions/{id}/tts", handlers.SpeakIntakeSummary)
			r.With(auth.RequireProjectEdit, handlers.IdempotencyMiddleware).Post("/projects/{id}/intake-sessions/{sessionID}/issue", handlers.CreateIntakeIssue)
			r.Get("/intake/sessions/{id}/stream", handlers.IntakeSessionStream)
			r.Get("/undo/activity", handlers.ListMyMutationActivity)
			r.Post("/undo/{id}", handlers.UndoMutation)
			r.Post("/undo/{id}/resolve", handlers.ResolveUndoMutation)
			r.Post("/undo/request/{requestID}", handlers.UndoMutationByRequestID)
			r.Post("/redo/{id}", handlers.RedoMutation)
			r.Post("/redo/{id}/resolve", handlers.ResolveRedoMutation)
			r.Post("/redo/request/{requestID}", handlers.RedoMutationByRequestID)

			r.Post("/issues/{id}/tags", handlers.AddTagToIssue)
			r.Delete("/issues/{id}/tags/{tag_id}", handlers.RemoveTagFromIssue)
			r.With(auth.RequireIssueAccess).Get("/issues/{id}/portal-visibility", handlers.GetIssuePortalVisibility)
			r.Post("/issues/batch/tags", handlers.BatchTagIssues)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/admin/projects/{id}/portal-visibility", handlers.GetAdminProjectPortalVisibility)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/admin/projects/{id}/portal-visibility.csv", handlers.GetAdminProjectPortalVisibilityCSV)
			r.Get("/projects/{id}/tags", handlers.ListProjectTags)
			r.Post("/projects/{id}/tags", handlers.AddTagToProject)
			r.Delete("/projects/{id}/tags/{tag_id}", handlers.RemoveTagFromProject)

			r.With(auth.RequireIssueAccess).Get("/issues/{id}/comments", handlers.ListComments)
			r.With(auth.RequireIssueEdit, handlers.CommentIdempotencyMiddleware).Post("/issues/{id}/comments", handlers.CreateComment)
			r.With(auth.RequireCommentEdit).Patch("/comments/{id}", handlers.UpdateCommentVisibility)
			r.With(auth.RequireCommentAccess).Delete("/comments/{id}", handlers.DeleteComment)

			r.Get("/tags", handlers.ListTags)
			r.With(auth.RequireAdmin).Post("/tags", handlers.CreateTag)
			r.With(auth.RequireAdmin).Put("/tags/{id}", handlers.UpdateTag)
			r.With(auth.RequireAdmin).Delete("/tags/{id}", handlers.DeleteTag)
			r.Get("/cost-units", handlers.ListAllCostUnits)
			r.Get("/releases", handlers.ListAllReleases)

			r.Get("/users", handlers.ListUsers)
			r.With(auth.RequireAdmin).Post("/users", handlers.CreateUser)
			r.With(auth.RequireAdmin).Put("/users/{id}", handlers.UpdateUser)

			// User project access + membership matrix
			r.With(auth.RequireAdmin).Get("/users/{id}/projects", handlers.ListUserProjects)
			r.With(auth.RequireAdmin).Post("/users/{id}/projects", handlers.AddUserProject)
			r.With(auth.RequireAdmin).Delete("/users/{id}/projects/{projectId}", handlers.RemoveUserProject)
			r.With(auth.RequireAdmin).Get("/users/{id}/memberships", handlers.ListUserMemberships)
			r.With(auth.RequireAdmin).Put("/users/{id}/memberships/{projectId}", handlers.UpsertUserMembership)
			r.With(auth.RequireAdmin).Delete("/users/{id}/memberships/{projectId}", handlers.DeleteUserMembership)
			r.Get("/permissions/matrix", handlers.GetPermissionsMatrix)
			r.With(auth.RequireAdmin).Get("/access-audit", handlers.ListAccessAudit)
			r.With(auth.RequireAdmin, auth.RequireCapability(auth.CapabilitySuperAdminAuditRead)).Get("/super-admin-activity", handlers.ListSuperAdminActivity)
			r.With(auth.RequireAdmin).Get("/sessions/{id}/activity", handlers.GetSessionActivity)

			r.Get("/auth/api-keys", handlers.ListAPIKeys)
			r.Post("/auth/api-keys", handlers.CreateAPIKey)
			r.Delete("/auth/api-keys/{id}", handlers.DeleteAPIKey)

			// PAI-331 — auto-watch sync subscriptions + SSE endpoint.
			r.Get("/auth/auto-watch", handlers.ListAutoWatch)
			r.Put("/auth/auto-watch/{deviceID}/{projectID}", handlers.UpsertAutoWatch)
			r.Delete("/auth/auto-watch/{deviceID}/{projectID}", handlers.DeleteAutoWatch)
			r.With(auth.RequireProjectView).Get("/projects/{id}/agents/events", handlers.AgentsEventsStream)
			r.With(auth.RequireProjectView).Get("/projects/{id}/agents/{name}.rev", handlers.AgentRevHandler)
			r.With(auth.RequireProjectView).Get("/projects/{id}/runners", handlers.ListProjectRunners)
			r.With(auth.RequireProjectView).Get("/projects/{id}/runs", handlers.ListProjectRuns)

			// Branding write endpoints — mirrors main.go. GET is in the
			// public group above; writes are admin-gated.
			r.With(auth.RequireAdmin).Put("/branding", handlers.PutBranding)
			r.With(auth.RequireAdmin).Post("/branding/logo", handlers.UploadBrandingLogo)
			r.With(auth.RequireAdmin).Post("/branding/favicon", handlers.UploadBrandingFavicon)

			// AI
			r.With(auth.RequireAdmin).Get("/ai/settings", handlers.GetAISettings)
			r.With(auth.RequireAdmin).Put("/ai/settings", handlers.PutAISettings)
			r.With(auth.RequireAdmin).Get("/ai/voice-settings", handlers.GetVoiceSettings)
			r.With(auth.RequireAdmin).Put("/ai/voice-settings", handlers.PutVoiceSettings)
			r.With(auth.RequireAdmin).Post("/ai/test", handlers.AITestConnection)
			r.With(auth.RequireAdmin).Get("/ai/models", handlers.AIListModels)
			r.With(auth.RequireAdmin).Get("/ai/usage", handlers.AIUsage)
			r.With(auth.RequireAdmin).Get("/ai/calls", handlers.AIListCalls)
			r.With(auth.RequireAdmin).Get("/ai/calls/export.csv", handlers.AIExportCallsCSV)
			r.With(auth.RequireAdmin).Get("/ai/calls/{id}", handlers.AIGetCall)
			r.With(auth.RequireAdmin).Get("/ai/prompts", handlers.AIListPrompts)
			r.With(auth.RequireAdmin).Post("/ai/prompts", handlers.AICreatePrompt)
			r.With(auth.RequireAdmin).Put("/ai/prompts/{id}", handlers.AIUpdatePrompt)
			r.With(auth.RequireAdmin).Delete("/ai/prompts/{id}", handlers.AIDeletePrompt)
			r.With(auth.RequireAdmin).Post("/ai/prompts/{id}/reset", handlers.AIResetPrompt)
			r.With(auth.RequireAdmin).Post("/ai/prompts/{id}/dry-run", handlers.AIDryRunPrompt)
			r.Get("/ai/actions", handlers.AIListActions)
			r.Get("/ai/execution-options", handlers.AIExecutionOptions)
			r.Get("/models/catalog", handlers.ModelCatalog)
			r.Get("/models/resolve", handlers.ModelResolve)
			r.With(auth.RequireAdmin).Put("/models/overrides", handlers.PutModelOverrides)
			r.Get("/ai/status", handlers.AIStatus)
			r.Get("/ai/calls/me", handlers.AIListMyCalls)
			r.Get("/ai/calls/me/export.csv", handlers.AIExportMyCallsCSV)
			r.Post("/ai/action", handlers.AIAction)
			r.Get("/ai/bulk-cost-estimate", handlers.AIBulkCostEstimate)
			r.With(auth.RequireIssueAccess).Get("/issues/{id}/ai-calls", handlers.AIListIssueCalls)

			r.Get("/search", handlers.Search)

			// Views
			r.Get("/views", handlers.ListViews)
			r.Post("/views", handlers.CreateView)
			r.Put("/views/{id}", handlers.UpdateView)
			r.Delete("/views/{id}", handlers.DeleteView)
			r.With(auth.RequireAdmin).Patch("/views/order", handlers.ReorderViews)
			r.Post("/views/{id}/pin", handlers.PinView)
			r.Delete("/views/{id}/pin", handlers.UnpinView)

			r.With(auth.RequireProjectView).Get("/projects/{id}/export/csv", handlers.ExportCSV)

			// Issue relations (sprint assignment, groups, etc.)
			r.Get("/issues/{id}/relations", handlers.ListIssueRelations)
			r.With(handlers.IdempotencyMiddleware).Post("/issues/{id}/relations", handlers.CreateIssueRelation)
			r.With(auth.RequireAdmin).Delete("/issues/{id}/relations", handlers.DeleteIssueRelation)
			r.Get("/issues/{id}/members", handlers.ListIssuesByRelation)
			r.Get("/issues/{id}/aggregation", handlers.GetIssueAggregation)
			// PAI-342 — applicable memories (read-only convenience).
			r.Get("/issues/{id}/applicable-memories", handlers.ListApplicableMemories)
			// PAI-343 — lesson-capture trigger detection.
			r.Get("/issues/{id}/lesson-capture-prompt", handlers.LessonCapturePrompt)

			// Reports
			r.With(auth.RequireProjectView).Get("/projects/{id}/reports/lieferbericht", handlers.GetLieferbericht)
			r.With(auth.RequireProjectView).Get("/projects/{id}/reports/lieferbericht/pdf", handlers.GetLieferberichtPDF)
			r.With(auth.RequireProjectView).Get("/projects/{id}/reports/projektbericht", handlers.GetLieferbericht)
			r.With(auth.RequireProjectView).Get("/projects/{id}/reports/projektbericht/pdf", handlers.GetLieferberichtPDF)
			r.With(auth.RequireProjectView).Get("/projects/{id}/time-report", handlers.GetProjectTimeReport)
			r.With(auth.RequireProjectView).Get("/projects/{id}/projektberichte", handlers.ListProjectReports)
			r.With(auth.RequireProjectView).Get("/projects/{id}/report-permissions", handlers.ListProjectReportPermissions)
			r.With(auth.RequireAdmin, auth.RequireProjectView).Put("/projects/{id}/report-permissions", handlers.PutProjectReportPermissions)

			// Attachments
			r.Get("/issues/{id}/attachments", handlers.ListAttachments)
			r.Post("/issues/{id}/attachments", handlers.UploadAttachment)
			r.Get("/attachments/{id}/meta", handlers.GetAttachmentMeta)
			r.Get("/attachments/{id}", handlers.GetAttachmentFile)
			r.Delete("/attachments/{id}", handlers.DeleteAttachment)
			r.Post("/attachments", handlers.UploadPendingAttachment)
			r.Patch("/attachments/link", handlers.LinkAttachments)

			// Time entries
			r.Get("/issues/{id}/time-entries", handlers.ListTimeEntries)
			r.Post("/issues/{id}/time-entries", handlers.CreateTimeEntry)
			r.Get("/time-entries/running", handlers.GetRunningTimers)
			r.Get("/time-entries/recent", handlers.GetRecentTimers)
			r.Get("/time-entries/today-summary", handlers.GetTimeEntriesTodaySummary)
			r.Get("/time-entries/week", handlers.GetWeekTimeEntries)
			r.Get("/time-entries/{id}", handlers.GetTimeEntry)
			r.Put("/time-entries/{id}", handlers.UpdateTimeEntry)
			r.Delete("/time-entries/{id}", handlers.DeleteTimeEntry)

			// Purge time entries (admin)
			r.With(auth.RequireAdmin).Get("/projects/{id}/time-entries/users", handlers.PurgeUsers)
			r.With(auth.RequireAdmin).Post("/projects/{id}/time-entries/purge-preview", handlers.PurgePreview)
			r.With(auth.RequireAdmin).Post("/projects/{id}/time-entries/purge", handlers.PurgeTimeEntries)

			// Cross-project issue list + orphan sprint creation.
			// GET + PATCH + POST-batch already registered above — keep
			// the orphan create here since it's the only one missing there.
			r.Post("/issues", handlers.CreateOrphanIssue)

			// Sprint listing
			r.Get("/sprints", handlers.ListSprints)

			// GDPR + retention
			r.With(auth.RequireAdmin).Get("/users/{id}/gdpr-export", handlers.ExportSubject)
			r.With(auth.RequireAdmin).Post("/users/{id}/gdpr-erase", handlers.EraseSubject)
			r.With(auth.RequireAdmin).Get("/gdpr/retention", handlers.GetRetentionPolicy)
			r.With(auth.RequireAdmin).Get("/system/settings", handlers.GetSystemSettings)
			r.With(auth.RequireAdmin).Put("/system/settings", handlers.PutSystemSettings)
			// PAI-980: instance-level CRM module switch — mirrors main.go.
			r.With(auth.RequireAdmin).Get("/integrations/crm/module", handlers.GetCRMModule)
			r.With(auth.RequireAdmin).Put("/integrations/crm/module", handlers.PutCRMModule)
			r.With(auth.RequireAdmin).Get("/integrations/crm/offers", handlers.GetOfferSettings)
			r.With(auth.RequireAdmin).Put("/integrations/crm/offers", handlers.PutOfferSettings)
			r.Get("/customers/{id}/offers", handlers.ListCustomerOffers)
			r.Get("/offers/acceptances", handlers.ListOfferAcceptances)
			r.Get("/offers/summary", handlers.ListOfferSummary)
			r.With(auth.RequireAdmin).Put("/offers/{id}/deleted", handlers.SetOfferDeleted)
			r.With(auth.RequireAdmin).Get("/offers/readiness", handlers.GetOfferDeliveryReadiness)
			r.With(auth.RequireAdmin).Post("/offers/{id}/confirmation/retry", handlers.RetryOfferConfirmation)
			r.Get("/offers/{id}/pdf", handlers.GetOfferPDF)
			r.With(auth.RequireAdmin).Post("/offers/{id}/link", handlers.CreateOfferLink)
			r.Get("/offers/{id}", handlers.GetOffer)
			r.With(auth.RequireAdmin).Post("/offers", handlers.CreateOffer)
			r.With(auth.RequireAdmin).Put("/offers/{id}", handlers.PutOffer)

			// Incident log
			r.With(auth.RequireAdmin).Get("/incidents/export", handlers.ExportIncidents)
			r.With(auth.RequireAdmin).Get("/incidents", handlers.ListIncidents)
			r.With(auth.RequireAdmin).Post("/incidents", handlers.CreateIncident)
			r.With(auth.RequireAdmin).Get("/incidents/{id}", handlers.GetIncident)
			r.With(auth.RequireAdmin).Patch("/incidents/{id}", handlers.UpdateIncident)
			r.With(auth.RequireAdmin).Delete("/incidents/{id}", handlers.DeleteIncident)

			// Customers + contacts (PAI-53 / PAI-273). buildRouter is
			// intentionally lighter than main.go but anything a handler
			// test needs to drive end-to-end has to be wired here.
			r.Get("/customers", handlers.ListCustomers)
			r.Get("/customers/{id}", handlers.GetCustomer)
			r.With(auth.RequireAdmin).Post("/customers", handlers.CreateCustomer)
			r.With(auth.RequireAdmin).Put("/customers/{id}", handlers.UpdateCustomer)
			r.With(auth.RequireAdmin).Post("/customers/{id}/number/reformat", handlers.ReformatCustomerNumber)
			r.With(auth.RequireAdmin).Delete("/customers/{id}", handlers.DeleteCustomer)
			r.Get("/customers/{id}/contacts", handlers.ListCustomerContacts)
			r.With(auth.RequireAdmin).Post("/customers/{id}/contacts", handlers.CreateCustomerContact)
			r.Get("/contacts/{id}", handlers.GetContact)
			r.With(auth.RequireAdmin).Put("/contacts/{id}", handlers.UpdateContact)
			r.With(auth.RequireAdmin).Delete("/contacts/{id}", handlers.DeleteContact)
			r.With(auth.RequireAdmin).Post("/contacts/{id}/promote-primary", handlers.PromoteContactPrimary)

			// OpenAPI
			r.Get("/openapi.json", handlers.GetOpenAPI)

			// Dev test reports
			r.With(auth.RequireAdmin).Post("/dev/test-reports", handlers.UploadTestReport)
			r.With(auth.RequireAdmin).Get("/dev/test-reports", handlers.ListTestReports)
			r.With(auth.RequireAdmin).Get("/dev/test-reports/summary", handlers.GetTestReportSummary)
			r.With(auth.RequireAdmin).Get("/dev/test-reports/{filename}", handlers.GetTestReport)
		})
	})

	// Public branding assets — mirrors main.go. Registered outside /api
	// so the URL is /brand/<filename>, matching what the frontend uses.
	r.Get("/brand/{filename}", handlers.ServeBrandingAsset)
	return r
}

// login posts credentials and returns the session cookie value.
func (ts *testServer) login(t *testing.T, username, password string) string {
	t.Helper()
	resp := ts.post(t, "/api/auth/login", "", map[string]string{
		"username": username,
		"password": password,
	})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login %s: status %d: %s", username, resp.StatusCode, body)
	}
	for _, name := range []string{"paimos_session", "session"} {
		for _, c := range resp.Cookies() {
			if c.Name == name {
				return c.Name + "=" + c.Value
			}
		}
	}
	t.Fatalf("login %s: no session cookie", username)
	return ""
}

func cookieSessionID(cookie string) string {
	switch {
	case strings.HasPrefix(cookie, "paimos_session="):
		return strings.TrimPrefix(cookie, "paimos_session=")
	case strings.HasPrefix(cookie, "session="):
		return strings.TrimPrefix(cookie, "session=")
	default:
		return cookie
	}
}

// get performs a GET request with the given cookie.
func (ts *testServer) get(t *testing.T, path, cookie string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.srv.URL+path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// getBearer performs a GET request with an Authorization: Bearer <token> header.
func (ts *testServer) getBearer(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET (bearer) %s: %v", path, err)
	}
	return resp
}

// post performs a POST request with JSON body and the given cookie.
func (ts *testServer) post(t *testing.T, path, cookie string, body interface{}) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// put performs a PUT request with JSON body and the given cookie.
func (ts *testServer) put(t *testing.T, path, cookie string, body interface{}) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, ts.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return resp
}

func (ts *testServer) patch(t *testing.T, path, cookie string, body interface{}) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPatch, ts.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	return resp
}

// del performs a DELETE request with the given cookie.
func (ts *testServer) del(t *testing.T, path, cookie string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, ts.srv.URL+path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

// delWithBody performs a DELETE request with a JSON body and the given cookie.
func (ts *testServer) delWithBody(t *testing.T, path, cookie string, body interface{}) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, ts.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

// decode reads a JSON response body into v.
func decode(t *testing.T, resp *http.Response, v interface{}) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// assertStatus fails the test if the response status doesn't match.
func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status: got %d, want %d — body: %s", resp.StatusCode, want, body)
	}
}

// id extracts the integer "id" field from a JSON response body.
func responseID(t *testing.T, resp *http.Response) int64 {
	t.Helper()
	var v struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &v)
	return v.ID
}

// tagID returns the id of the first tag in the DB.
func firstTagID(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := db.DB.QueryRow("SELECT id FROM tags WHERE system=0 ORDER BY id LIMIT 1").Scan(&id); err != nil {
		t.Fatalf("firstTagID: %v", err)
	}
	return id
}

// postMultipart performs a multipart/form-data POST with a single file field.
func (ts *testServer) postMultipart(t *testing.T, path, cookie, fieldName, fileName string, fileContent []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	fw.Write(fileContent)
	w.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.srv.URL+path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST multipart %s: %v", path, err)
	}
	return resp
}

// unused import guard
var _ = sql.ErrNoRows
var _ = fmt.Sprintf
