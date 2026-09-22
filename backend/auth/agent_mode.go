// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package auth

import (
	"net/http"

	"github.com/inspr-at/paimos/backend/httpcontract"
)

// AgentModeAuthorizationCTE is the single SQL authorization predicate for
// Agent Mode snapshots and replay. Callers bind one user id to its sole `?`.
// Live roots can enter only through their current issues.project_id; delivery
// hints, run history, requester/claimer identity and old attempt projects are
// intentionally absent.
const AgentModeAuthorizationCTE = `WITH requester AS (
 SELECT u.id AS user_id,u.permissions_epoch,
  CASE
   WHEN u.is_reviewer=1 THEN 'reviewer'
   WHEN u.is_super_admin=1 THEN 'super_admin'
   WHEN u.role_key='member' AND u.role IN ('admin','external') THEN u.role
   WHEN u.role_key IN ('admin','member','external','super_admin') THEN u.role_key
   WHEN u.role IN ('admin','member','external') THEN u.role
   ELSE 'member'
  END AS role
 FROM users u WHERE u.id=? AND u.status='active'
),
agent_mode_projects AS (
 SELECT p.id AS project_id,
  CASE
   WHEN requester.role IN ('admin','super_admin') THEN 'editor'
   WHEN pm.access_level IN ('viewer','editor') THEN pm.access_level
   WHEN requester.role='member' AND pm.access_level IS NULL THEN 'editor'
   ELSE 'none'
  END AS access_level
 FROM requester CROSS JOIN projects p
 LEFT JOIN project_members pm ON pm.user_id=requester.user_id AND pm.project_id=p.id
 WHERE requester.role<>'external' AND p.status<>'deleted'
  AND (requester.role IN ('admin','super_admin') OR pm.access_level IN ('viewer','editor') OR
       (requester.role='member' AND pm.access_level IS NULL))
),
agent_mode_roots AS (
 SELECT i.id AS issue_id,i.project_id,agent_mode_projects.access_level
 FROM issues i JOIN agent_mode_projects ON agent_mode_projects.project_id=i.project_id
 WHERE i.deleted_at IS NULL AND i.project_id IS NOT NULL
)`

// AgentModePrivateNoStore establishes the cache policy before any later gate
// can terminate the request. Snapshot/SSE handlers may strengthen it (for
// example with no-transform), but authentication and password-gate errors must
// never be cacheable on this private supervision surface.
func AgentModePrivateNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		next.ServeHTTP(w, r)
	})
}

// RequireAgentModeInternal differs intentionally from BlockExternal: an
// external account receives the same 404 body as a missing or inaccessible
// Agent Mode resource, even if it has an explicit project grant.
func RequireAgentModeInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := GetUser(r)
		if user == nil || user.Status != "active" || user.Role == "external" {
			httpcontract.WriteAgentModeNotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
