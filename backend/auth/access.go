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

package auth

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// formatInt64 is a package-local alias for strconv.FormatInt. Named
// separately so the call site in int64ToString reads cleanly.
func formatInt64(n int64) string { return strconv.FormatInt(n, 10) }

// AccessLevel is a per-(user, project) permission grant stored in
// the project_members table. Levels are ordered: none < viewer < editor.
//
// AccessUnset is the zero-value sentinel returned by lookups when no row
// exists — distinct from AccessNone ('none') which is an explicit denial
// stored in the table. Role defaults (e.g. member → editor) only apply
// for AccessUnset; AccessNone always wins.
type AccessLevel string

const (
	AccessUnset  AccessLevel = ""
	AccessNone   AccessLevel = "none"
	AccessViewer AccessLevel = "viewer"
	AccessEditor AccessLevel = "editor"
)

// accessCacheKey is the context key for the per-request memoization cache.
// Using a unique unexported type prevents collisions with other packages'
// context keys (std library recommendation).
type accessCacheKeyType struct{}

var accessCacheKey = accessCacheKeyType{}

// accessCache memoizes project_members lookups for a single request so that
// a handler touching CanViewProject, CanEditProject, and AccessibleProjectIDs
// issues at most one SQL query. Populated lazily on first lookup.
type accessCache struct {
	userID int64
	loaded bool
	levels map[int64]AccessLevel
}

// WithAccessCache attaches a fresh per-request access cache to ctx. Call
// this from top-level middleware so nested handler calls share a single
// memoized view of project_members.
func WithAccessCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, accessCacheKey, &accessCache{})
}

// cacheFromCtx returns the attached cache, or nil if none was attached.
func cacheFromCtx(ctx context.Context) *accessCache {
	c, _ := ctx.Value(accessCacheKey).(*accessCache)
	return c
}

// loadLevels populates the cache with every project_members row for user.
// Called once per request; subsequent calls are a no-op.
func (c *accessCache) loadLevels(userID int64) error {
	if c.loaded && c.userID == userID {
		return nil
	}
	c.userID = userID
	c.levels = map[int64]AccessLevel{}
	rows, err := db.DB.Query(
		"SELECT project_id, access_level FROM project_members WHERE user_id=?", userID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var lvl string
		if err := rows.Scan(&pid, &lvl); err != nil {
			log.Printf("accessCache.loadLevels: scan: %v", err)
			continue
		}
		c.levels[pid] = AccessLevel(lvl)
	}
	c.loaded = true
	return nil
}

// lookup returns the explicit access level stored for (userID, projectID),
// or AccessUnset if no row exists. Uses the context cache when available,
// otherwise falls through to a one-shot query.
//
// Callers MUST distinguish AccessUnset (no row — role default may apply)
// from AccessNone (explicit 'none' denial — always blocks).
func lookup(ctx context.Context, userID, projectID int64) AccessLevel {
	if c := cacheFromCtx(ctx); c != nil {
		if err := c.loadLevels(userID); err == nil {
			if lvl, ok := c.levels[projectID]; ok {
				return lvl
			}
			return AccessUnset
		}
		// fall through on error
	}
	var lvl string
	err := db.DB.QueryRow(
		"SELECT access_level FROM project_members WHERE user_id=? AND project_id=?",
		userID, projectID,
	).Scan(&lvl)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessUnset
	}
	if err != nil {
		log.Printf("access.lookup: user=%d project=%d: %v", userID, projectID, err)
		return AccessUnset
	}
	return AccessLevel(lvl)
}

// effectiveLevel resolves the user's access to projectID, taking role
// defaults into account:
//   - admin/super-admin always get editor (bypass)
//   - inactive/deleted users always get none
//   - members default to editor unless an explicit row says otherwise
//   - external users get only what's explicitly granted
func effectiveLevel(ctx context.Context, user *models.User, projectID int64) AccessLevel {
	if user == nil {
		return AccessNone
	}
	if user.Status != "active" {
		return AccessNone
	}
	if IsAdmin(user) {
		return AccessEditor
	}
	lvl := lookup(ctx, user.ID, projectID)
	if lvl != AccessUnset {
		// Explicit row — honor it, including 'none' which denies access
		// even when the role default would otherwise grant it.
		return lvl
	}
	// No explicit row. Members default to editor; external users default
	// to no access and must be granted explicitly.
	if user.Role == "member" {
		return AccessEditor
	}
	return AccessNone
}

// CanViewProject reports whether the request's user may view projectID.
// Uses the request context (cache + user).
func CanViewProject(r *http.Request, projectID int64) bool {
	user := GetUser(r)
	lvl := effectiveLevel(r.Context(), user, projectID)
	return lvl == AccessViewer || lvl == AccessEditor
}

// CanEditProject reports whether the request's user may edit projectID.
func CanEditProject(r *http.Request, projectID int64) bool {
	user := GetUser(r)
	lvl := effectiveLevel(r.Context(), user, projectID)
	return lvl == AccessEditor
}

// CanViewProjectTx applies the production role-default and explicit-membership
// rules inside a caller-owned authority transaction. It avoids request caches
// at admission and execution boundaries where membership must be current.
func CanViewProjectTx(ctx context.Context, tx *sql.Tx, userID, projectID int64) bool {
	if tx == nil || userID < 1 || projectID < 1 {
		return false
	}
	var status, role string
	var explicit sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT u.status,`+userRoleSelectExpr+`,membership.access_level
		FROM users u LEFT JOIN project_members membership
		 ON membership.user_id=u.id AND membership.project_id=?
		WHERE u.id=?`, projectID, userID).Scan(&status, &role, &explicit)
	if err != nil || status != "active" {
		return false
	}
	if IsAdminRole(role) {
		return true
	}
	if explicit.Valid {
		return explicit.String == string(AccessViewer) || explicit.String == string(AccessEditor)
	}
	return role == RoleMember
}

// ProjectAccessLevel returns the effective access level for (user, project)
// as seen by the handler. Useful for per-capability checks beyond view/edit.
func ProjectAccessLevel(r *http.Request, projectID int64) AccessLevel {
	return effectiveLevel(r.Context(), GetUser(r), projectID)
}

// AccessibleProjectIDs returns the set of project IDs the current user can
// at least view. Returns nil for admins (meaning "all projects"). For
// members and external users, returns an explicit list.
//
// For members, the list is computed as: all non-deleted projects MINUS any
// where an explicit 'none' row removes their default editor access. For
// external users, the list contains only projects with an explicit
// 'viewer' or 'editor' grant.
func AccessibleProjectIDs(r *http.Request) []int64 {
	user := GetUser(r)
	if user == nil || user.Status != "active" {
		return []int64{}
	}
	if IsAdmin(user) {
		return nil
	}

	// Prime the cache if attached so repeat calls are free.
	var explicit map[int64]AccessLevel
	if c := cacheFromCtx(r.Context()); c != nil {
		if err := c.loadLevels(user.ID); err == nil {
			explicit = c.levels
		}
	}
	if explicit == nil {
		explicit = map[int64]AccessLevel{}
		rows, err := db.DB.Query(
			"SELECT project_id, access_level FROM project_members WHERE user_id=?",
			user.ID,
		)
		if err != nil {
			log.Printf("AccessibleProjectIDs: query: %v", err)
			return []int64{}
		}
		defer rows.Close()
		for rows.Next() {
			var pid int64
			var lvl string
			if err := rows.Scan(&pid, &lvl); err != nil {
				continue
			}
			explicit[pid] = AccessLevel(lvl)
		}
	}

	if user.Role == RoleExternal || user.Role == RoleReviewer {
		ids := make([]int64, 0, len(explicit))
		for pid, lvl := range explicit {
			if lvl == AccessViewer || lvl == AccessEditor {
				ids = append(ids, pid)
			}
		}
		return ids
	}

	// Members: all non-deleted projects except those with an explicit 'none'.
	rows, err := db.DB.Query(
		"SELECT id FROM projects WHERE status != 'deleted'",
	)
	if err != nil {
		log.Printf("AccessibleProjectIDs: list projects: %v", err)
		return []int64{}
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			continue
		}
		if lvl, ok := explicit[pid]; ok && lvl == AccessNone {
			continue
		}
		ids = append(ids, pid)
	}
	return ids
}

// AccessibleProjectIDsForUser is the request-free variant of
// AccessibleProjectIDs for background workers (PAI-706 intake project
// detection). Same semantics: nil = admin (all projects), empty = none.
// Recompute per use — permission changes must take effect immediately
// (INV-INTAKE-03: the intake candidate set is derived from this).
func AccessibleProjectIDsForUser(userID int64) []int64 {
	var role, status string
	if err := db.DB.QueryRow(
		"SELECT role, status FROM users WHERE id=?", userID).Scan(&role, &status); err != nil || status != "active" {
		return []int64{}
	}
	if IsAdminRole(role) {
		return nil
	}
	explicit := map[int64]AccessLevel{}
	rows, err := db.DB.Query(
		"SELECT project_id, access_level FROM project_members WHERE user_id=?", userID)
	if err != nil {
		log.Printf("AccessibleProjectIDsForUser: query: %v", err)
		return []int64{}
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var lvl string
		if err := rows.Scan(&pid, &lvl); err != nil {
			continue
		}
		explicit[pid] = AccessLevel(lvl)
	}
	if role == RoleExternal || role == RoleReviewer {
		ids := make([]int64, 0, len(explicit))
		for pid, lvl := range explicit {
			if lvl == AccessViewer || lvl == AccessEditor {
				ids = append(ids, pid)
			}
		}
		return ids
	}
	prows, err := db.DB.Query("SELECT id FROM projects WHERE status != 'deleted'")
	if err != nil {
		log.Printf("AccessibleProjectIDsForUser: list projects: %v", err)
		return []int64{}
	}
	defer prows.Close()
	ids := []int64{}
	for prows.Next() {
		var pid int64
		if err := prows.Scan(&pid); err != nil {
			continue
		}
		if lvl, ok := explicit[pid]; ok && lvl == AccessNone {
			continue
		}
		ids = append(ids, pid)
	}
	return ids
}

// SeedAccessForUser auto-grants editor access to all non-deleted projects
// for a newly created admin/member. External users are not seeded — they
// must receive explicit grants via the user-memberships endpoints.
func SeedAccessForUser(userID int64, role string) {
	if !HasDefaultProjectAccess(role) {
		return
	}
	_, err := db.DB.Exec(`
		INSERT OR IGNORE INTO project_members(user_id, project_id, access_level)
		SELECT ?, p.id, 'editor'
		FROM projects p
		WHERE p.status != 'deleted'
	`, userID)
	if err != nil {
		log.Printf("SeedAccessForUser: user=%d: %v", userID, err)
	}
}

// SeedAccessForProject auto-grants editor access to all active admin/member
// users for a newly created project. External users are not seeded.
func SeedAccessForProject(projectID int64) {
	_, err := db.DB.Exec(`
		INSERT OR IGNORE INTO project_members(user_id, project_id, access_level)
		SELECT u.id, ?, 'editor'
		FROM users u
		WHERE u.is_reviewer = 0 AND CASE
		        WHEN u.is_super_admin = 1 THEN 'super_admin'
		        WHEN u.role_key = 'member' AND u.role IN ('admin','external') THEN u.role
		        WHEN u.role_key IN ('admin','member','external','super_admin') THEN u.role_key
		        WHEN u.role IN ('admin','member','external') THEN u.role
		        ELSE 'member'
		      END IN ('admin','super_admin','member')
		  AND u.status = 'active'
	`, projectID)
	if err != nil {
		log.Printf("SeedAccessForProject: project=%d: %v", projectID, err)
	}
}

// AccessResponse is the shape returned alongside the user in the login
// and /auth/me responses. It lets the frontend hydrate its permission
// cache in a single round trip instead of querying each project.
//
//   - AllProjects is true for admins (shortcut meaning "every project").
//   - Levels maps project_id (as string, for JSON) to the effective access
//     level the user has on that project. For members, projects without an
//     explicit row are listed as "editor" (default). For externals, only
//     projects with an explicit viewer/editor grant appear.
type AccessResponse struct {
	AllProjects bool              `json:"all_projects"`
	Levels      map[string]string `json:"levels"`
}

// BuildAccessResponse computes the access summary for a user. Called by
// the login / TOTP / me handlers so the client can cache per-project
// capabilities for its router guard and UI-gating code.
func BuildAccessResponse(user *models.User) AccessResponse {
	resp := AccessResponse{Levels: map[string]string{}}
	if user == nil || user.Status != "active" {
		return resp
	}
	if IsAdmin(user) {
		resp.AllProjects = true
		return resp
	}

	// Collect explicit rows once.
	explicit := map[int64]AccessLevel{}
	rows, err := db.DB.Query(
		"SELECT project_id, access_level FROM project_members WHERE user_id=?",
		user.ID,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pid int64
			var lvl string
			if err := rows.Scan(&pid, &lvl); err != nil {
				continue
			}
			explicit[pid] = AccessLevel(lvl)
		}
	}

	if user.Role == RoleExternal || user.Role == RoleReviewer {
		for pid, lvl := range explicit {
			if lvl == AccessViewer || lvl == AccessEditor {
				resp.Levels[int64ToString(pid)] = string(lvl)
			}
		}
		return resp
	}

	// member: default editor for every non-deleted project unless denied.
	prows, err := db.DB.Query("SELECT id FROM projects WHERE status != 'deleted'")
	if err != nil {
		log.Printf("BuildAccessResponse: list projects: %v", err)
		return resp
	}
	defer prows.Close()
	for prows.Next() {
		var pid int64
		if err := prows.Scan(&pid); err != nil {
			continue
		}
		if lvl, ok := explicit[pid]; ok {
			if lvl == AccessNone {
				continue
			}
			resp.Levels[int64ToString(pid)] = string(lvl)
			continue
		}
		resp.Levels[int64ToString(pid)] = string(AccessEditor)
	}
	return resp
}

// int64ToString formats an int64 for use as a JSON map key.
func int64ToString(n int64) string {
	// strconv.FormatInt allocates on every call; for dozens of projects
	// this is fine and the code is simpler than a cached formatter.
	return formatInt64(n)
}

// ProjectIDForIssue returns the project_id for an issue. The boolean
// returns distinguish the three outcomes the middleware needs:
//
//   - found=true,  orphan=false → projectID is valid; run access check
//   - found=true,  orphan=true  → the row exists but has NULL project_id
//     (e.g. a sprint); caller may pass the request through
//   - found=false                → row missing or DB error; fail closed (404)
func ProjectIDForIssue(issueID int64) (projectID int64, found bool, orphan bool) {
	var pid sql.NullInt64
	err := db.DB.QueryRow("SELECT project_id FROM issues WHERE id=?", issueID).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false
	}
	if err != nil {
		log.Printf("ProjectIDForIssue: id=%d: %v", issueID, err)
		return 0, false, false
	}
	if !pid.Valid {
		return 0, true, true
	}
	return pid.Int64, true, false
}

// ProjectIDForAttachment returns the project_id for an attachment, via the
// owning issue. See ProjectIDForIssue for the (found, orphan) contract.
func ProjectIDForAttachment(attachmentID int64) (projectID int64, found bool, orphan bool) {
	var pid sql.NullInt64
	err := db.DB.QueryRow(`
		SELECT i.project_id FROM attachments a
		LEFT JOIN issues i ON i.id = a.issue_id
		WHERE a.id = ?
	`, attachmentID).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false
	}
	if err != nil {
		log.Printf("ProjectIDForAttachment: id=%d: %v", attachmentID, err)
		return 0, false, false
	}
	if !pid.Valid {
		return 0, true, true
	}
	return pid.Int64, true, false
}

// ProjectIDForTimeEntry returns the project_id for a time entry, via the
// owning issue. See ProjectIDForIssue for the (found, orphan) contract.
func ProjectIDForTimeEntry(timeEntryID int64) (projectID int64, found bool, orphan bool) {
	var pid sql.NullInt64
	err := db.DB.QueryRow(`
		SELECT i.project_id FROM time_entries te
		JOIN issues i ON i.id = te.issue_id
		WHERE te.id = ?
	`, timeEntryID).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false
	}
	if err != nil {
		log.Printf("ProjectIDForTimeEntry: id=%d: %v", timeEntryID, err)
		return 0, false, false
	}
	if !pid.Valid {
		return 0, true, true
	}
	return pid.Int64, true, false
}

// ProjectIDForComment returns the project_id for a comment, via the owning
// issue. See ProjectIDForIssue for the (found, orphan) contract.
func ProjectIDForComment(commentID int64) (projectID int64, found bool, orphan bool) {
	var pid sql.NullInt64
	err := db.DB.QueryRow(`
		SELECT i.project_id FROM comments c
		JOIN issues i ON i.id = c.issue_id
		WHERE c.id = ?
	`, commentID).Scan(&pid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false
	}
	if err != nil {
		log.Printf("ProjectIDForComment: id=%d: %v", commentID, err)
		return 0, false, false
	}
	if !pid.Valid {
		return 0, true, true
	}
	return pid.Int64, true, false
}
