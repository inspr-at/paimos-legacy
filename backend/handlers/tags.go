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
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// TagColorPalette is the allowed tag-color set, in canonical order
// (neutrals → blues → warms → greens → cools). The slice is the
// single source of truth: schema discovery (handlers.Schema.Enums)
// reads it directly at init, so adding or reordering a value here
// propagates to /api/schema without any duplication.
//
// Order matters for two reasons: (1) the schema response preserves
// it so clients can render the palette in a stable, designer-
// approved sequence; (2) tests assert that the schema enum equals
// the slice element-for-element.
var TagColorPalette = []string{
	"gray", "slate", "blue", "indigo",
	"purple", "pink", "red", "orange",
	"yellow", "green", "teal", "cyan",
}

// validColorSet is the O(1) membership index derived from
// TagColorPalette at init. Kept unexported because the canonical
// shape callers should reference is the slice.
var validColorSet = func() map[string]struct{} {
	out := make(map[string]struct{}, len(TagColorPalette))
	for _, c := range TagColorPalette {
		out[c] = struct{}{}
	}
	return out
}()

// IsValidTagColor reports whether color is in the canonical palette.
func IsValidTagColor(color string) bool {
	_, ok := validColorSet[color]
	return ok
}

// ── Tag CRUD ────────────────────────────────────────────────────────────────

func ListTags(w http.ResponseWriter, r *http.Request) {
	if user := auth.GetUser(r); user != nil && user.Role == auth.RoleReviewer {
		// A vocabulary entry may itself reveal a production project. Return
		// only tags already attached to an explicitly shared project or issue.
		rows, err := db.DB.Query(`SELECT DISTINCT t.id,t.name,t.color,t.description,t.system,t.created_at
		 FROM tags t WHERE EXISTS(SELECT 1 FROM project_tags pt JOIN project_members pm ON pm.project_id=pt.project_id
		 WHERE pt.tag_id=t.id AND pm.user_id=? AND pm.access_level='viewer')
		 OR EXISTS(SELECT 1 FROM issue_tags it JOIN issues i ON i.id=it.issue_id
		 JOIN project_members pm ON pm.project_id=i.project_id
		 WHERE it.tag_id=t.id AND i.deleted_at IS NULL AND pm.user_id=? AND pm.access_level='viewer') ORDER BY t.name`, user.ID, user.ID)
		if err != nil {
			jsonError(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		tags := []models.Tag{}
		for rows.Next() {
			var tag models.Tag
			if err := rows.Scan(&tag.ID, &tag.Name, &tag.Color, &tag.Description, &tag.System, &tag.CreatedAt); err != nil {
				jsonError(w, "scan failed", http.StatusInternalServerError)
				return
			}
			tags = append(tags, tag)
		}
		jsonOK(w, tags)
		return
	}
	rows, err := db.DB.Query(
		`SELECT id, name, color, description, system, created_at FROM tags ORDER BY name`)
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	tags := []models.Tag{}
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		tags = append(tags, t)
	}
	jsonOK(w, tags)
}

func ListProjectTags(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var exists int
	if err := db.DB.QueryRow(`SELECT 1 FROM projects WHERE id=?`, projectID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonError(w, "project not found", http.StatusNotFound)
			return
		}
		log.Printf("ListProjectTags project=%d: %v", projectID, err)
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	rows, err := db.DB.Query(`
		SELECT t.id, t.name, t.color, t.description, t.system, t.created_at
		FROM project_tags pt
		JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id = ?
		ORDER BY t.name
	`, projectID)
	if err != nil {
		log.Printf("ListProjectTags query project=%d: %v", projectID, err)
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	tags := []models.Tag{}
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err != nil {
			log.Printf("ListProjectTags scan project=%d: %v", projectID, err)
			continue
		}
		tags = append(tags, t)
	}
	jsonOK(w, tags)
}

func CreateTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, "name required", http.StatusBadRequest)
		return
	}
	if body.Color == "" {
		body.Color = "gray"
	}
	if ev := validateEnumField("tag.color", body.Color); ev != nil {
		writeEnumViolation(w, r, ev)
		return
	}

	res, err := db.DB.Exec(
		`INSERT INTO tags(name,color,description) VALUES(?,?,?)`,
		body.Name, body.Color, body.Description,
	)
	if handleDBError(w, err, "tag") {
		return
	}
	id, _ := res.LastInsertId()
	var t models.Tag
	db.DB.QueryRow(`SELECT id,name,color,description,system,created_at FROM tags WHERE id=?`, id).
		Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, t)
}

func UpdateTag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Color       *string `json:"color"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	// PAI-459 / QA: system tags carry semantic identity (CUSTOMERPORTAL
	// is matched by name throughout the codebase, e.g. the visibility
	// helpers in portal.go and the audit pipeline). Renaming would
	// silently break those lookups. The DeleteTag path already enforces
	// this; UpdateTag previously did not, leaving an API loophole admins
	// could trip into.
	if isSystemTag(id) {
		jsonError(w, "system tags cannot be renamed or recoloured", http.StatusForbidden)
		return
	}
	if body.Color != nil {
		if ev := validateEnumField("tag.color", *body.Color); ev != nil {
			writeEnumViolation(w, r, ev)
			return
		}
	}
	if _, err := db.DB.Exec(`
		UPDATE tags SET
			name        = COALESCE(?, name),
			color       = COALESCE(?, color),
			description = COALESCE(?, description)
		WHERE id=?
	`, body.Name, body.Color, body.Description, id); err != nil {
		handleDBError(w, err, "tag")
		return
	}
	var t models.Tag
	if err := db.DB.QueryRow(`SELECT id,name,color,description,system,created_at FROM tags WHERE id=?`, id).
		Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	jsonOK(w, t)
}

func DeleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if isSystemTag(id) {
		jsonError(w, "system tags cannot be deleted", http.StatusForbidden)
		return
	}
	// Explicitly remove associations first — belt-and-suspenders in case
	// ON DELETE CASCADE doesn't fire (e.g. FK pragma not active on this conn).
	if _, err := db.DB.Exec(`DELETE FROM issue_tags WHERE tag_id=?`, id); err != nil {
		log.Printf("DeleteTag issue_tags id=%d: %v", id, err)
		jsonError(w, "delete failed", http.StatusInternalServerError)
		return
	}
	if _, err := db.DB.Exec(`DELETE FROM project_tags WHERE tag_id=?`, id); err != nil {
		log.Printf("DeleteTag project_tags id=%d: %v", id, err)
		jsonError(w, "delete failed", http.StatusInternalServerError)
		return
	}
	res, err := db.DB.Exec(`DELETE FROM tags WHERE id=?`, id)
	if err != nil {
		log.Printf("DeleteTag id=%d: %v", id, err)
		jsonError(w, "delete failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Tag associations ─────────────────────────────────────────────────────────

func AddTagToIssue(w http.ResponseWriter, r *http.Request) {
	issueID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid issue id", http.StatusBadRequest)
		return
	}
	var body struct {
		TagID int64 `json:"tag_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TagID == 0 {
		jsonError(w, "tag_id required", http.StatusBadRequest)
		return
	}
	// Block manual attachment of system tags. CUSTOMERPORTAL is the one
	// system tag end users are expected to toggle (PAI-459 / PAI-463) —
	// rename/delete remain blocked by DeleteTag's isSystemTag check.
	if isSystemTag(body.TagID) && !isPortalVisibilityTag(body.TagID) {
		jsonError(w, "system tags cannot be added manually", http.StatusForbidden)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("AddTagToIssue: begin tx: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if !issueExistsActiveTx(tx, issueID) {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}
	if !tagExistsTx(tx, body.TagID) {
		jsonError(w, "tag not found", http.StatusNotFound)
		return
	}
	before, err := fetchIssueTagMutationSnapshotTx(tx, issueID, body.TagID)
	if err != nil {
		log.Printf("AddTagToIssue: before snapshot issue_id=%d tag_id=%d err=%v", issueID, body.TagID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO issue_tags(issue_id,tag_id) VALUES(?,?)`,
		issueID, body.TagID); err != nil {
		log.Printf("AddTagToIssue: issue_id=%d tag_id=%d err=%v", issueID, body.TagID, err)
		jsonError(w, "failed to attach tag", http.StatusInternalServerError)
		return
	}
	after, err := fetchIssueTagMutationSnapshotTx(tx, issueID, body.TagID)
	if err != nil {
		log.Printf("AddTagToIssue: after snapshot issue_id=%d tag_id=%d err=%v", issueID, body.TagID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	var userID *int64
	if user := auth.GetUser(r); user != nil {
		userID = &user.ID
	}
	if _, err := recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID:    requestIDFromRequest(r),
		UserID:       userID,
		SessionID:    sessionIDFromRequest(r),
		AgentName:    agentNameFromRequest(r),
		MutationType: mutationTypeForRequest(r, "issue.tag.add"),
		SubjectType:  "issue_tag",
		SubjectID:    issueID,
		InverseOp: InverseOp{
			Method: http.MethodDelete,
			Path:   fmt.Sprintf("/issues/%d/tags/%d", issueID, body.TagID),
		},
		BeforeState: before,
		AfterState:  after,
		Undoable:    before.Exists != after.Exists,
	}); err != nil {
		log.Printf("AddTagToIssue: recordMutation: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("AddTagToIssue: commit: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func RemoveTagFromIssue(w http.ResponseWriter, r *http.Request) {
	issueID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid issue id", http.StatusBadRequest)
		return
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid tag id", http.StatusBadRequest)
		return
	}
	// Mirror of the AddTagToIssue exemption — CUSTOMERPORTAL is
	// user-toggleable (PAI-459 / PAI-463); other system tags remain
	// blocked from manual removal.
	if isSystemTag(tagID) && !isPortalVisibilityTag(tagID) {
		jsonError(w, "system tags cannot be removed manually", http.StatusForbidden)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("RemoveTagFromIssue: begin tx: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if !issueExistsActiveTx(tx, issueID) {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}
	if !tagExistsTx(tx, tagID) {
		jsonError(w, "tag not found", http.StatusNotFound)
		return
	}
	before, err := fetchIssueTagMutationSnapshotTx(tx, issueID, tagID)
	if err != nil {
		log.Printf("RemoveTagFromIssue: before snapshot issue_id=%d tag_id=%d err=%v", issueID, tagID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := tx.ExecContext(r.Context(), `DELETE FROM issue_tags WHERE issue_id=? AND tag_id=?`, issueID, tagID); err != nil {
		log.Printf("RemoveTagFromIssue: issue=%d tag=%d: %v", issueID, tagID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	after, err := fetchIssueTagMutationSnapshotTx(tx, issueID, tagID)
	if err != nil {
		log.Printf("RemoveTagFromIssue: after snapshot issue_id=%d tag_id=%d err=%v", issueID, tagID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	var userID *int64
	if user := auth.GetUser(r); user != nil {
		userID = &user.ID
	}
	if _, err := recordMutation(r.Context(), tx, mutationRecordArgs{
		RequestID:    requestIDFromRequest(r),
		UserID:       userID,
		SessionID:    sessionIDFromRequest(r),
		AgentName:    agentNameFromRequest(r),
		MutationType: mutationTypeForRequest(r, "issue.tag.remove"),
		SubjectType:  "issue_tag",
		SubjectID:    issueID,
		InverseOp: InverseOp{
			Method: http.MethodPost,
			Path:   fmt.Sprintf("/issues/%d/tags", issueID),
			Body: map[string]any{
				"tag_id": tagID,
			},
		},
		BeforeState: before,
		AfterState:  after,
		Undoable:    before.Exists != after.Exists,
	}); err != nil {
		log.Printf("RemoveTagFromIssue: recordMutation: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("RemoveTagFromIssue: commit: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func AddTagToProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var body struct {
		TagID int64 `json:"tag_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TagID == 0 {
		jsonError(w, "tag_id required", http.StatusBadRequest)
		return
	}
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO project_tags(project_id,tag_id) VALUES(?,?)`,
		projectID, body.TagID); err != nil {
		jsonError(w, "insert failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func RemoveTagFromProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "tag_id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid tag id", http.StatusBadRequest)
		return
	}
	if _, err := db.DB.Exec(`DELETE FROM project_tags WHERE project_id=? AND tag_id=?`, projectID, tagID); err != nil {
		log.Printf("RemoveTagFromProject: project=%d tag=%d: %v", projectID, tagID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Tag loading helpers (used by other handlers) ──────────────────────────────

// LoadTagsForIssues loads tags for a slice of issues in a single query.
func LoadTagsForIssues(issues []models.Issue) []models.Issue {
	if len(issues) == 0 {
		return issues
	}
	// Collect IDs
	ids := make([]any, len(issues))
	idxByID := make(map[int64]int, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
		idxByID[iss.ID] = i
		issues[i].Tags = []models.Tag{}
	}

	placeholders := buildPlaceholders(len(ids))
	// #nosec G202 -- buildPlaceholders emits ?-only; issue ids are bound args.
	rows, err := db.DB.Query(`
		SELECT it.issue_id, t.id, t.name, t.color, t.description, t.system, t.created_at
		FROM issue_tags it
		JOIN tags t ON t.id = it.tag_id
		WHERE it.issue_id IN (`+placeholders+`)
		ORDER BY t.name
	`, ids...)
	if err != nil {
		return issues
	}
	defer rows.Close()
	for rows.Next() {
		var issueID int64
		var t models.Tag
		if err := rows.Scan(&issueID, &t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		if idx, ok := idxByID[issueID]; ok {
			issues[idx].Tags = append(issues[idx].Tags, t)
		}
	}
	return issues
}

// LoadTagsForProjects loads tags for a slice of projects in a single query.
func LoadTagsForProjects(projects []models.Project) []models.Project {
	if len(projects) == 0 {
		return projects
	}
	ids := make([]any, len(projects))
	idxByID := make(map[int64]int, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
		idxByID[p.ID] = i
		projects[i].Tags = []models.Tag{}
	}
	placeholders := buildPlaceholders(len(ids))
	// #nosec G202 -- buildPlaceholders emits ?-only; project ids are bound args.
	rows, err := db.DB.Query(`
		SELECT pt.project_id, t.id, t.name, t.color, t.description, t.system, t.created_at
		FROM project_tags pt
		JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id IN (`+placeholders+`)
		ORDER BY t.name
	`, ids...)
	if err != nil {
		return projects
	}
	defer rows.Close()
	for rows.Next() {
		var projID int64
		var t models.Tag
		if err := rows.Scan(&projID, &t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		if idx, ok := idxByID[projID]; ok {
			projects[idx].Tags = append(projects[idx].Tags, t)
		}
	}
	return projects
}

// LoadTagsForIssue loads tags for a single issue.
func LoadTagsForIssue(issue *models.Issue) {
	issue.Tags = []models.Tag{}
	rows, err := db.DB.Query(`
		SELECT t.id, t.name, t.color, t.description, t.system, t.created_at
		FROM issue_tags it
		JOIN tags t ON t.id = it.tag_id
		WHERE it.issue_id = ?
		ORDER BY t.name
	`, issue.ID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		issue.Tags = append(issue.Tags, t)
	}
}

// LoadTagsForProject loads tags for a single project.
func LoadTagsForProject(project *models.Project) {
	project.Tags = []models.Tag{}
	rows, err := db.DB.Query(`
		SELECT t.id, t.name, t.color, t.description, t.system, t.created_at
		FROM project_tags pt
		JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id = ?
		ORDER BY t.name
	`, project.ID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.System, &t.CreatedAt); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		project.Tags = append(project.Tags, t)
	}
}

// isSystemTag checks if a tag has the system flag set.
func isSystemTag(tagID int64) bool {
	var sys int
	if err := db.DB.QueryRow(`SELECT system FROM tags WHERE id=?`, tagID).Scan(&sys); err != nil {
		return false
	}
	return sys == 1
}

// CustomerPortalTagName is the canonical name of the system tag that gates
// customer-portal visibility (PAI-458 / PAI-459). The name is the
// permanent identity — code paths that need the tag id look it up by
// name and cache the result in-process.
const CustomerPortalTagName = "CUSTOMERPORTAL"

// isPortalVisibilityTag reports whether tagID points at the
// CUSTOMERPORTAL system tag. This is the one system tag that ordinary
// users with tag:write are allowed to attach and detach via the standard
// issue-tag API, because the entire portal-v2 visibility model rests on
// internal users flipping it from IssueDetailView and IssueList bulk
// actions (PAI-459 + PAI-463). The system=1 flag still protects the tag
// from rename and delete in DeleteTag.
func isPortalVisibilityTag(tagID int64) bool {
	id, ok := customerPortalTagID()
	return ok && id == tagID
}

// customerPortalTagID resolves the CUSTOMERPORTAL tag's id, caching the
// result for the lifetime of the process. The tag is created by migration
// 109 and never renamed (system flag prevents it), so a one-shot lookup is
// safe. Returns ok=false only if the migration hasn't run yet (unit-test
// harnesses that skip migrations); callers must handle that path.
func customerPortalTagID() (int64, bool) {
	if id, ok := customerPortalTagIDCache.Load().(int64); ok && id > 0 {
		return id, true
	}
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM tags WHERE name=?`, CustomerPortalTagName).Scan(&id); err != nil || id == 0 {
		return 0, false
	}
	customerPortalTagIDCache.Store(id)
	return id, true
}

// customerPortalTagIDCache holds the cached lookup result. atomic.Value
// keeps the read path lock-free; one Load + one Store at startup, then
// pure reads forever.
var customerPortalTagIDCache atomic.Value

// customerPortalTagIDTx is the transactional variant: callers in the
// middle of a write tx need the tag id with the same visibility as their
// other writes (e.g. PortalSubmitRequest, which both inserts the issue
// and attaches the tag). Uses the same process cache as customerPortalTagID
// and primes it on a successful query.
func customerPortalTagIDTx(ctx context.Context, tx *sql.Tx) (int64, bool) {
	if id, ok := customerPortalTagIDCache.Load().(int64); ok && id > 0 {
		return id, true
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name=?`, CustomerPortalTagName).Scan(&id); err != nil || id == 0 {
		return 0, false
	}
	customerPortalTagIDCache.Store(id)
	return id, true
}

func buildPlaceholders(n int) string {
	if n == 0 {
		return ""
	}
	b := make([]byte, 0, n*2-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}
