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

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// ListUserProjects returns the viewer/editor grants for an external user.
// GET /api/users/{id}/projects
//
// Historically this endpoint only showed rows for external users, and the
// UI that drives it (the portal access editor) still treats every listed
// row as "viewer". The richer matrix editor lives under /memberships.
func ListUserProjects(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	rows, err := db.DB.Query(`
		SELECT pm.project_id, p.name, p.key
		FROM project_members pm
		JOIN projects p ON p.id = pm.project_id
		WHERE pm.user_id = ? AND pm.access_level IN ('viewer','editor')
		ORDER BY p.name
	`, userID)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type item struct {
		ProjectID int64  `json:"project_id"`
		Name      string `json:"name"`
		Key       string `json:"key"`
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.ProjectID, &i.Name, &i.Key); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		items = append(items, i)
	}
	jsonOK(w, items)
}

// AddUserProject grants viewer access on a project to an external user.
// POST /api/users/{id}/projects  { "project_id": 123 }
func AddUserProject(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var body struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ProjectID == 0 {
		jsonError(w, "project_id required", http.StatusBadRequest)
		return
	}

	oldLvl := lookupCurrentLevel(userID, body.ProjectID)
	_, err = db.DB.Exec(
		`INSERT INTO project_members(user_id, project_id, access_level)
		 VALUES(?, ?, 'viewer')
		 ON CONFLICT(user_id, project_id) DO UPDATE SET access_level='viewer', updated_at=datetime('now')`,
		userID, body.ProjectID,
	)
	if err != nil {
		jsonError(w, "insert failed", http.StatusInternalServerError)
		return
	}

	actor := auth.GetUser(r)
	var actorID int64
	if actor != nil {
		actorID = actor.ID
	}
	action := auth.AuditActionGrant
	if oldLvl != auth.AccessNone {
		action = auth.AuditActionUpdate
	}
	auth.RecordAccessChange(r.Context(), nil, body.ProjectID, userID, action, oldLvl, auth.AccessViewer, actorID)
	auth.BumpPermissionsEpoch(userID) // PAI-320

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]bool{"ok": true})
}

// RemoveUserProject revokes a user's access to a project (deletes the row).
// DELETE /api/users/{id}/projects/{projectId}
func RemoveUserProject(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid user id", http.StatusBadRequest)
		return
	}
	projectID, err := strconv.ParseInt(chi.URLParam(r, "projectId"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}

	oldLvl := lookupCurrentLevel(userID, projectID)
	res, err := db.DB.Exec(
		"DELETE FROM project_members WHERE user_id=? AND project_id=?",
		userID, projectID,
	)
	if err != nil {
		jsonError(w, "delete failed", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}

	actor := auth.GetUser(r)
	var actorID int64
	if actor != nil {
		actorID = actor.ID
	}
	auth.RecordAccessChange(r.Context(), nil, projectID, userID, auth.AuditActionRevoke, oldLvl, auth.AccessNone, actorID)
	auth.BumpPermissionsEpoch(userID) // PAI-320

	w.WriteHeader(http.StatusNoContent)
}

// ── Membership matrix editor ────────────────────────────────────────────────

type membershipRow struct {
	ProjectID   int64  `json:"project_id"`
	ProjectKey  string `json:"project_key"`
	ProjectName string `json:"project_name"`
	AccessLevel string `json:"access_level"`
}

// ListUserMemberships returns, for each non-deleted project, the effective
// access level for userID — including 'none' rows (explicit denials) and
// rows that don't exist yet (shown as the role's default).
// GET /api/users/{id}/memberships
func ListUserMemberships(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Role is needed to compute defaults for rows with no explicit grant.
	var role string
	if err := db.DB.QueryRow("SELECT "+userRoleSelectExpr+" FROM users WHERE id=?", userID).Scan(&role); err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	rows, err := db.DB.Query(`
		SELECT p.id, COALESCE(p.key,''), p.name, COALESCE(pm.access_level, '')
		FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = ?
		WHERE p.status != 'deleted'
		ORDER BY p.name
	`, userID)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []membershipRow{}
	for rows.Next() {
		var m membershipRow
		var lvl string
		if err := rows.Scan(&m.ProjectID, &m.ProjectKey, &m.ProjectName, &lvl); err != nil {
			continue
		}
		if lvl == "" {
			// No explicit row — apply the default for this user's role.
			if auth.HasDefaultProjectAccess(role) {
				m.AccessLevel = "editor"
			} else {
				m.AccessLevel = "none"
			}
		} else {
			m.AccessLevel = lvl
		}
		items = append(items, m)
	}
	jsonOK(w, items)
}

// UpsertUserMembership sets or updates the access level for (userID, projectID).
// PUT /api/users/{id}/memberships/{projectId}  { "access_level": "viewer" }
func UpsertUserMembership(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid user id", http.StatusBadRequest)
		return
	}
	projectID, err := strconv.ParseInt(chi.URLParam(r, "projectId"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var body struct {
		AccessLevel string `json:"access_level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	lvl := auth.AccessLevel(body.AccessLevel)
	if lvl != auth.AccessNone && lvl != auth.AccessViewer && lvl != auth.AccessEditor {
		jsonError(w, "access_level must be none, viewer, or editor", http.StatusBadRequest)
		return
	}
	var reviewer bool
	if err := db.DB.QueryRow("SELECT is_reviewer FROM users WHERE id=?", userID).Scan(&reviewer); err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}
	if reviewer && lvl == auth.AccessEditor {
		jsonError(w, "reviewers may only receive viewer or none access", http.StatusBadRequest)
		return
	}

	oldLvl := lookupCurrentLevel(userID, projectID)
	_, err = db.DB.Exec(
		`INSERT INTO project_members(user_id, project_id, access_level)
		 VALUES(?, ?, ?)
		 ON CONFLICT(user_id, project_id) DO UPDATE
		   SET access_level=excluded.access_level, updated_at=datetime('now')`,
		userID, projectID, string(lvl),
	)
	if err != nil {
		jsonError(w, "upsert failed", http.StatusInternalServerError)
		return
	}

	actor := auth.GetUser(r)
	var actorID int64
	if actor != nil {
		actorID = actor.ID
	}
	action := auth.AuditActionGrant
	if oldLvl != auth.AccessNone {
		action = auth.AuditActionUpdate
	}
	auth.RecordAccessChange(r.Context(), nil, projectID, userID, action, oldLvl, lvl, actorID)
	auth.BumpPermissionsEpoch(userID) // PAI-320

	jsonOK(w, map[string]string{"access_level": string(lvl)})
}

// DeleteUserMembership drops the explicit grant for (userID, projectID),
// reverting the pair to the role default. Returns 204 even when no row
// existed — the post-condition is "no row", which is already true.
// DELETE /api/users/{id}/memberships/{projectId}
func DeleteUserMembership(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid user id", http.StatusBadRequest)
		return
	}
	projectID, err := strconv.ParseInt(chi.URLParam(r, "projectId"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}

	oldLvl := lookupCurrentLevel(userID, projectID)
	if _, err := db.DB.Exec(
		"DELETE FROM project_members WHERE user_id=? AND project_id=?",
		userID, projectID,
	); err != nil {
		jsonError(w, "delete failed", http.StatusInternalServerError)
		return
	}

	if oldLvl != auth.AccessNone {
		actor := auth.GetUser(r)
		var actorID int64
		if actor != nil {
			actorID = actor.ID
		}
		auth.RecordAccessChange(r.Context(), nil, projectID, userID, auth.AuditActionRevoke, oldLvl, auth.AccessNone, actorID)
		auth.BumpPermissionsEpoch(userID) // PAI-320
	}

	w.WriteHeader(http.StatusNoContent)
}

// lookupCurrentLevel returns the stored access level for (userID, projectID),
// or AccessNone if no row exists. Used by the upsert/delete handlers to
// build accurate audit entries.
func lookupCurrentLevel(userID, projectID int64) auth.AccessLevel {
	var lvl string
	err := db.DB.QueryRow(
		"SELECT access_level FROM project_members WHERE user_id=? AND project_id=?",
		userID, projectID,
	).Scan(&lvl)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.AccessNone
	}
	if err != nil {
		log.Printf("lookupCurrentLevel: %v", err)
		return auth.AccessNone
	}
	return auth.AccessLevel(lvl)
}

// ── Access audit log ─────────────────────────────────────────────────────────

// ListAccessAudit returns recent access_audit rows. Supports optional
// filters ?project_id=..., ?user_id=..., ?limit=N (default 100, max 500).
// GET /api/access-audit
func ListAccessAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 500 {
				n = 500
			}
			limit = n
		}
	}

	query := `
		SELECT a.id, a.project_id, a.user_id, a.actor_id, a.action,
		       a.old_level, a.new_level, a.created_at,
		       COALESCE(p.name,''), COALESCE(p.key,''),
		       COALESCE(u.username,''), COALESCE(au.username,'')
		FROM access_audit a
		LEFT JOIN projects p ON p.id = a.project_id
		LEFT JOIN users u    ON u.id = a.user_id
		LEFT JOIN users au   ON au.id = a.actor_id
		WHERE 1=1
	`
	args := []any{}
	if v := q.Get("project_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			query += " AND a.project_id = ?"
			args = append(args, id)
		}
	}
	if v := q.Get("user_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			query += " AND a.user_id = ?"
			args = append(args, id)
		}
	}
	query += " ORDER BY a.created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.DB.Query(query, args...)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type auditRow struct {
		ID          int64  `json:"id"`
		ProjectID   *int64 `json:"project_id"`
		UserID      *int64 `json:"user_id"`
		ActorID     *int64 `json:"actor_id"`
		Action      string `json:"action"`
		OldLevel    string `json:"old_level"`
		NewLevel    string `json:"new_level"`
		CreatedAt   string `json:"created_at"`
		ProjectName string `json:"project_name"`
		ProjectKey  string `json:"project_key"`
		Username    string `json:"username"`
		ActorName   string `json:"actor_name"`
	}
	items := []auditRow{}
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.UserID, &a.ActorID, &a.Action,
			&a.OldLevel, &a.NewLevel, &a.CreatedAt,
			&a.ProjectName, &a.ProjectKey, &a.Username, &a.ActorName); err != nil {
			log.Printf("audit scan: %v", err)
			continue
		}
		items = append(items, a)
	}
	jsonOK(w, items)
}
