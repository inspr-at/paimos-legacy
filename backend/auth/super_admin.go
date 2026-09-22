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

// PAI-336 — role helpers.
//
// `super_admin` is now a canonical public role. The legacy
// is_super_admin flag remains a read/write compatibility shim so older
// rows and older callers still resolve through the same helper methods.

package auth

import (
	"net/http"

	"github.com/inspr-at/paimos/backend/models"
)

const (
	RoleAdmin      = "admin"
	RoleMember     = "member"
	RoleExternal   = "external"
	RoleReviewer   = "reviewer"
	RoleSuperAdmin = "super_admin"
)

// IsValidRole reports whether role is one of the persisted public roles.
func IsValidRole(role string) bool {
	switch role {
	case RoleAdmin, RoleMember, RoleExternal, RoleSuperAdmin, RoleReviewer:
		return true
	default:
		return false
	}
}

// IsAdminRole reports whether role should pass admin-only application
// gates. Super-admin inherits admin powers, then adds explicit
// capability-gated powers on top.
func IsAdminRole(role string) bool {
	return role == RoleAdmin || role == RoleSuperAdmin
}

func IsInternalRole(role string) bool {
	return HasDefaultProjectAccess(role) || role == RoleReviewer
}

// HasDefaultProjectAccess excludes reviewers: being an internal human does
// not imply an instance-wide project grant.
func HasDefaultProjectAccess(role string) bool {
	return IsAdminRole(role) || role == RoleMember
}

func LegacyRoleForPublicRole(role string) string {
	if role == RoleReviewer {
		return RoleExternal // older readers fail closed to the portal role
	}
	if role == RoleSuperAdmin {
		return RoleAdmin
	}
	return role
}

// IsAdmin is nil-safe for direct handler checks.
func IsAdmin(u *models.User) bool {
	return u != nil && IsAdminRole(u.Role)
}

// IsSuperAdmin reports whether the given user is a super-admin. It accepts
// either the canonical role or the legacy flag.
func IsSuperAdmin(u *models.User) bool {
	if u == nil {
		return false
	}
	return u.Role == RoleSuperAdmin || u.IsSuperAdmin
}

// IsSuperAdminRequest is the convenience form for handlers that hold
// the *http.Request rather than the resolved user. Same nil-safety.
func IsSuperAdminRequest(r *http.Request) bool {
	return IsSuperAdmin(GetUser(r))
}

// RequireSuperAdmin gates a route to super-admin callers only. Any
// non-super-admin (including regular admins) gets 403. Today no route
// uses this directly — time-entry handlers do their own per-call
// branch because the gate is conditional on `body.user_id != caller`,
// not on the route itself. The middleware exists so the next
// super-admin-only endpoint doesn't have to reinvent the check.
func RequireSuperAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsSuperAdminRequest(r) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
