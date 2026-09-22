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

// PAI-379 — api-key scope narrowing.
//
// Today an api-key inherits its owning user's role wholesale. That makes
// "give an automation only the power it needs" impossible: a script
// that just creates projects has to carry an admin's full credentials.
//
// Scopes narrow a key. The sentinel "*" means "everything the owner role
// allows" and is the default for keys created before this feature (and
// for keys whose creator doesn't pass an explicit scope list). Named
// scopes like "projects:write" restrict the key to exactly the endpoints
// in the catalog below.
//
// Policy (PAI-379 line 1, "B"): scopes only NARROW. They never let a
// caller do something their role couldn't already do — handlers that
// gate on a scope additionally gate on the underlying role (see
// `RequireAdmin, RequireScope("projects:write")` on POST /api/projects).
// A member's keyring can never carry "projects:write" because the
// catalog requires admin role to attach it.
//
// Session-cookie callers (the browser SPA) are never narrowed: the
// middleware attaches the all-scopes set on the session-auth branch so
// `RequireScope` is a uniform check downstream.

package auth

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ScopeAll is the sentinel that grants every scope. Backfilled onto
// every existing api-key row by migration M104 so behavior is unchanged
// for unscoped callers.
const ScopeAll = "*"

// Named scopes. Add new entries to scopeCatalog below, not here as
// constants, so the catalog stays the single source of truth.
const (
	ScopeProjectsWrite = "projects:write"

	// PAI-809 — supervisory control. Two scopes, not one, because the
	// two principals are genuinely different: a human operator issues
	// grants and commands from Agent Mode, while a runner process leases
	// a capability and answers input requests. A key minted for one must
	// not silently carry the other.
	ScopeFlowCredentialsWrite = "flow:credentials:write"
	ScopeAgentControlsWrite   = "agent-controls:write"
	ScopeAgentControlsRunner  = "agent-controls:runner"
)

// ScopeDef describes one named scope: what role you must already have
// to attach it to a key, what endpoints it unlocks (informational, used
// by /api/schema so agents can self-discover), and a one-line human
// blurb for the UI / docs.
type ScopeDef struct {
	Name         string   `json:"name"`
	RequiredRole string   `json:"required_role"`
	Endpoints    []string `json:"endpoints"`
	Description  string   `json:"description"`
}

// scopeCatalog is the authoritative list of named scopes. Keep this
// short on purpose: a scope is only worth adding once an endpoint
// actually wires `RequireScope` to it. Speculative scopes rot.
//
// PAI-809 is the one deliberate exception to "route first, scope after":
// the supervisory-control route families are frozen, and the catalog
// lands ahead of them so the route lane cannot invent a second spelling
// of the same authority. The Endpoints lists below are the frozen path
// families (informational, surfaced by /api/schema) — no method, status,
// or payload shape is claimed here.
var scopeCatalog = map[string]ScopeDef{
	ScopeFlowCredentialsWrite: {
		Name: ScopeFlowCredentialsWrite, RequiredRole: RoleAdmin,
		Endpoints:   []string{"POST /api/auth/flow-projection-credentials", "DELETE /api/auth/flow-projection-credentials/{id}"},
		Description: "Provision or revoke an expiring single-project Flow projection credential for an explicit human reviewer.",
	},
	ScopeProjectsWrite: {
		Name:         ScopeProjectsWrite,
		RequiredRole: RoleAdmin,
		Endpoints:    []string{"POST /api/projects"},
		Description:  "Create new projects. Combined with the existing admin-role gate on the endpoint.",
	},
	ScopeAgentControlsWrite: {
		Name:         ScopeAgentControlsWrite,
		RequiredRole: RoleMember,
		Endpoints: []string{
			"/api/agent-mode/deliveries/{deliveryKey}/control-capability-grants",
			"/api/agent-mode/deliveries/{deliveryKey}/control-commands",
			"/api/agent-mode/control-capability-grants/{id}",
			"/api/agent-mode/control-commands/{id}",
			"/api/control-commands/{id}",
		},
		Description: "Operator side of supervisory control: capability grants and control commands. Narrows a key only — the route's own project-access gate still applies.",
	},
	ScopeAgentControlsRunner: {
		Name:         ScopeAgentControlsRunner,
		RequiredRole: RoleMember,
		Endpoints: []string{
			"/api/runs/{id}/control-capability-leases",
			"/api/runs/{id}/input-requests",
			"/api/runs/{id}/control-commands",
			"/api/control-capability-leases/{id}",
			"/api/control-commands/{id}",
		},
		Description: "Runner side of supervisory control: capability leases and input requests for a run. Narrows a key only — the route's own project-access gate still applies.",
	},
}

// ScopeCatalog returns the catalog sorted by name. Used by the schema
// endpoint to expose discoverable scope metadata.
func ScopeCatalog() []ScopeDef {
	out := make([]ScopeDef, 0, len(scopeCatalog))
	for _, d := range scopeCatalog {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ScopeSet is the parsed in-memory form of an api-key's scopes column.
// The empty/nil set is interpreted as "no scopes" (denies everything
// gated by RequireScope) — different from a set containing only the
// sentinel ScopeAll, which grants everything.
type ScopeSet map[string]struct{}

// Has reports whether the set unlocks the given scope. The sentinel
// ScopeAll satisfies every check.
func (s ScopeSet) Has(scope string) bool {
	if s == nil {
		return false
	}
	if _, ok := s[ScopeAll]; ok {
		return true
	}
	_, ok := s[scope]
	return ok
}

// ParseScopes turns the CSV form stored in api_keys.scopes into a
// ScopeSet. Whitespace around entries is tolerated. An empty value denies
// every scoped operation: M104 backfilled legacy keys to the explicit "*"
// sentinel, so treating later empty/corrupt rows as broad authority would
// turn scope removal into privilege escalation.
func ParseScopes(csv string) ScopeSet {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return ScopeSet{}
	}
	out := ScopeSet{}
	for raw := range strings.SplitSeq(csv, ",") {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		out[t] = struct{}{}
	}
	if len(out) == 0 {
		return ScopeSet{}
	}
	return out
}

// FormatScopes is the inverse of ParseScopes. Used by the api-key
// creation handler to store the validated set back as CSV.
func FormatScopes(s ScopeSet) string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// ValidateScopesForRole rejects scope sets that the requesting user's
// role isn't allowed to attach. The sentinel ScopeAll is always
// allowed (it's a no-op narrowing). Named scopes must be in the
// catalog AND the user's role must meet the catalog's RequiredRole.
//
// This is the gate that enforces policy "B": members cannot conjure a
// projects:write key for themselves, because the catalog says
// projects:write requires admin role.
func ValidateScopesForRole(s ScopeSet, role string) error {
	for name := range s {
		if name == ScopeAll {
			continue
		}
		def, ok := scopeCatalog[name]
		if !ok {
			return fmt.Errorf("unknown scope %q", name)
		}
		if !roleMeetsScopeMinimum(def.RequiredRole, role) {
			return fmt.Errorf("scope %q requires %s role", name, def.RequiredRole)
		}
	}
	return nil
}

// roleMeetsScopeMinimum is the catalog's role table. Admin and
// super_admin satisfy the admin minimum; member, admin, and super_admin
// satisfy the member minimum. External never satisfies any minimum —
// every scoped surface is internal, and an external account with a
// project grant is still not an operator.
//
// A minimum the table doesn't know denies. A typo in a catalog entry
// must cost that scope its keys, not silently widen every key that
// carries it.
func roleMeetsScopeMinimum(minimumRole, role string) bool {
	if role == RoleExternal || role == RoleReviewer || !IsValidRole(role) {
		return false
	}
	switch minimumRole {
	case RoleAdmin:
		return IsAdminRole(role)
	case RoleMember:
		return IsInternalRole(role)
	default:
		return false
	}
}

// ── context plumbing ─────────────────────────────────────────────────

type scopesKeyType struct{}

var scopesKey = scopesKeyType{}

// WithScopes attaches a ScopeSet to a request context. Called by the
// auth middleware on both the api-key branch (parsed from the DB
// column) and the session-cookie branch (the all-scopes set, since
// browser sessions are never narrowed).
func WithScopes(ctx context.Context, s ScopeSet) context.Context {
	return context.WithValue(ctx, scopesKey, s)
}

// GetScopes returns the set attached to the request context. A nil
// return means the middleware didn't run (treat as no scopes — safest
// default for tests that construct requests by hand).
func GetScopes(r *http.Request) ScopeSet {
	v, _ := r.Context().Value(scopesKey).(ScopeSet)
	return v
}

// HasScope is the convenience form for handlers that hold the
// *http.Request rather than the resolved set.
func HasScope(r *http.Request, scope string) bool {
	return GetScopes(r).Has(scope)
}

// ── middleware ───────────────────────────────────────────────────────

// RequireScope gates a route on a named scope. Composes with
// RequireAdmin (or other role checks) — wire them together in the
// router so role and scope are both enforced.
//
// Session-cookie auth always passes because the auth middleware
// attaches the all-scopes set on that branch. Only api-key callers can
// be narrowed below their owner's role.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !HasScope(r, scope) {
				http.Error(w, `{"error":"forbidden: scope `+scope+` required"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
