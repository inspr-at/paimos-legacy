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
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func normalizeUserRole(role string) (publicRole string, legacyRole string, superAdminFlag int, ok bool) {
	publicRole = strings.TrimSpace(role)
	if publicRole == "" {
		publicRole = auth.RoleMember
	}
	if !auth.IsValidRole(publicRole) {
		return "", "", 0, false
	}
	legacyRole = auth.LegacyRoleForPublicRole(publicRole)
	if publicRole == auth.RoleSuperAdmin {
		superAdminFlag = 1
	}
	return publicRole, legacyRole, superAdminFlag, true
}

// Preserve the existing SQLite role-key constraint and fail closed to external
// on older versions. M198's discriminator is the canonical reviewer identity.
func reviewerStorageRole(role string) string {
	if role == auth.RoleReviewer {
		return auth.RoleExternal
	}
	return role
}

func ListUsers(w http.ResponseWriter, r *http.Request) {
	if user := auth.GetUser(r); user != nil && user.Role == auth.RoleReviewer {
		jsonOK(w, []models.User{*user})
		return
	}
	// By default only active + inactive. Pass ?status=deleted for trash.
	status := r.URL.Query().Get("status")
	var rows interface {
		Next() bool
		Scan(...any) error
		Close() error
	}
	var err error
	if status == "deleted" {
		rows, err = db.DB.Query(
			"SELECT " + userSelectColsWithTOTP + " FROM users WHERE status='deleted' ORDER BY username",
		)
	} else {
		rows, err = db.DB.Query(
			"SELECT " + userSelectColsWithTOTP + " FROM users WHERE status != 'deleted' ORDER BY username",
		)
	}
	if err != nil {
		jsonError(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	users := []models.User{}
	for rows.Next() {
		var u models.User
		if err := scanUserWithTOTP(rows, &u); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		users = append(users, u)
	}
	jsonOK(w, users)
}

func CreateUser(w http.ResponseWriter, r *http.Request) {
	// PAI-321: must_change_password is a *pointer to bool* so the
	// admin-side checkbox can be sent as `false` distinctly from
	// "field omitted, use default". Omitted → default ON. The
	// frontend's create-user form ships the checkbox checked by
	// default.
	var body struct {
		Username           string   `json:"username"`
		Password           string   `json:"password"`
		Role               string   `json:"role"`
		MustChangePassword *bool    `json:"must_change_password"`
		Nickname           string   `json:"nickname"`
		Email              string   `json:"email"`
		InternalRateHourly *float64 `json:"internal_rate_hourly"`
		Locale             string   `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
		jsonError(w, "username and password required", http.StatusBadRequest)
		return
	}
	if len(body.Password) < auth.MinPasswordLen {
		jsonError(w, fmt.Sprintf("password must be at least %d characters", auth.MinPasswordLen), http.StatusBadRequest)
		return
	}
	// Nickname shares the same 3-rune cap as UpdateUser so create/edit
	// validate the field identically.
	if len([]rune(body.Nickname)) > 3 {
		jsonError(w, "nickname must be 3 characters or fewer", http.StatusBadRequest)
		return
	}
	if body.Role == "" {
		body.Role = "member"
	}
	locale := body.Locale
	if locale == "" {
		locale = "en"
	}
	publicRole, legacyRole, superAdminFlag, ok := normalizeUserRole(body.Role)
	if !ok {
		jsonError(w, "role must be admin, member, reviewer, external, or super_admin", http.StatusBadRequest)
		return
	}
	actor := auth.GetUser(r)
	if publicRole == auth.RoleSuperAdmin && !auth.HasCapability(r.Context(), actor, auth.CapabilityUsersGrantSuperAdmin) {
		jsonError(w, "only super-admin can grant super-admin", http.StatusForbidden)
		return
	}
	mustChange := 1 // default ON
	if body.MustChangePassword != nil && !*body.MustChangePassword {
		mustChange = 0
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		jsonError(w, "hash failed", http.StatusInternalServerError)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("CreateUser: begin tx: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO users(username,password,role,role_key,is_super_admin,is_reviewer,status,must_change_password,nickname,email,internal_rate_hourly,locale)
		 VALUES(?,?,?,?,?,?,'active',?,?,?,?,?)`,
		body.Username, hash, legacyRole, reviewerStorageRole(publicRole), superAdminFlag, publicRole == auth.RoleReviewer, mustChange,
		body.Nickname, body.Email, body.InternalRateHourly, locale,
	)
	if handleDBError(w, err, "username") {
		return
	}
	id, _ := res.LastInsertId()
	if publicRole == auth.RoleSuperAdmin {
		if err := recordSuperAdminAuditTx(r.Context(), tx, r, actor, id, auth.CapabilityUsersGrantSuperAdmin, map[string]any{
			"action": "create_user",
			"role":   publicRole,
		}); err != nil {
			log.Printf("CreateUser: super-admin audit user=%d: %v", id, err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("CreateUser: commit: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Seed editor access on all active projects for internal roles. External
	// users are not seeded — they must be granted access explicitly.
	auth.SeedAccessForUser(id, publicRole)
	var u models.User
	scanUser(db.DB.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id=?", id), &u)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, u)
}

func UpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	var body struct {
		Username           *string  `json:"username"`
		Password           *string  `json:"password"`
		MustChangePassword *bool    `json:"must_change_password"`
		Role               *string  `json:"role"`
		Status             *string  `json:"status"`
		Nickname           *string  `json:"nickname"`
		Email              *string  `json:"email"`
		InternalRateHourly *float64 `json:"internal_rate_hourly"`
		Locale             *string  `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Role != nil {
		if publicRole, _, _, ok := normalizeUserRole(*body.Role); !ok {
			jsonError(w, "role must be admin, member, reviewer, external, or super_admin", http.StatusBadRequest)
			return
		} else {
			*body.Role = publicRole
		}
	}

	if body.Status != nil {
		s := *body.Status
		if s != "active" && s != "inactive" && s != "deleted" {
			jsonError(w, "status must be active, inactive, or deleted", http.StatusBadRequest)
			return
		}
	}

	// Validate nickname length
	if body.Nickname != nil && len([]rune(*body.Nickname)) > 3 {
		jsonError(w, "nickname must be 3 characters or fewer", http.StatusBadRequest)
		return
	}

	// Capture the current role up front so we can detect a role change
	// and sync project_members accordingly. Role transitions change the
	// user's access model (member default-editor vs. external no-default),
	// so a bare UPDATE isn't enough.
	var oldRole string
	if err := db.DB.QueryRow("SELECT "+userRoleSelectExpr+" FROM users WHERE id=?", id).Scan(&oldRole); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	actor := auth.GetUser(r)
	if body.Role != nil && *body.Role != oldRole && (*body.Role == auth.RoleSuperAdmin || oldRole == auth.RoleSuperAdmin) {
		if !auth.HasCapability(r.Context(), actor, auth.CapabilityUsersGrantSuperAdmin) {
			jsonError(w, "only super-admin can grant or revoke super-admin", http.StatusForbidden)
			return
		}
	}

	if body.Username != nil || body.Role != nil || body.Status != nil || body.Nickname != nil || body.Email != nil || body.InternalRateHourly != nil || body.Locale != nil {
		var roleKeyArg any
		var legacyRoleArg any
		var superAdminArg any
		var reviewerArg any
		if body.Role != nil {
			roleKey, legacyRole, superAdminFlag, _ := normalizeUserRole(*body.Role)
			roleKeyArg = reviewerStorageRole(roleKey)
			legacyRoleArg = legacyRole
			superAdminArg = superAdminFlag
			reviewerArg = roleKey == auth.RoleReviewer
		}
		tx, err := db.DB.BeginTx(r.Context(), nil)
		if err != nil {
			log.Printf("UpdateUser: begin tx user=%d: %v", id, err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if body.Role != nil && *body.Role == auth.RoleReviewer && oldRole != auth.RoleReviewer {
			var grants int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM project_members WHERE user_id=? AND access_level != 'none'", id).Scan(&grants); err != nil {
				jsonError(w, "cannot verify existing project grants", http.StatusInternalServerError)
				return
			}
			if grants != 0 {
				jsonError(w, "revoke existing project grants before assigning reviewer", http.StatusBadRequest)
				return
			}
		}
		_, err = tx.ExecContext(r.Context(), `
			UPDATE users SET
				username             = COALESCE(?, username),
				role                 = COALESCE(?, role),
				role_key             = COALESCE(?, role_key),
				is_super_admin       = COALESCE(?, is_super_admin),
				is_reviewer          = COALESCE(?, is_reviewer),
				status               = COALESCE(?, status),
				nickname             = COALESCE(?, nickname),
				email                = COALESCE(?, email),
				internal_rate_hourly = COALESCE(?, internal_rate_hourly),
				locale               = COALESCE(?, locale)
			WHERE id = ?
		`, body.Username, legacyRoleArg, roleKeyArg, superAdminArg, reviewerArg, body.Status, body.Nickname, body.Email, body.InternalRateHourly, body.Locale, id)
		if handleDBError(w, err, "user") {
			return
		}
		if body.Role != nil && *body.Role != oldRole && (*body.Role == auth.RoleSuperAdmin || oldRole == auth.RoleSuperAdmin) {
			action := "grant_super_admin"
			if oldRole == auth.RoleSuperAdmin {
				action = "revoke_super_admin"
			}
			if err := recordSuperAdminAuditTx(r.Context(), tx, r, actor, id, auth.CapabilityUsersGrantSuperAdmin, map[string]any{
				"action":   action,
				"old_role": oldRole,
				"new_role": *body.Role,
			}); err != nil {
				log.Printf("UpdateUser: super-admin audit user=%d: %v", id, err)
				jsonError(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("UpdateUser: commit user=%d: %v", id, err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		// PAI-320: bump the epoch only when something that affects
		// permissions changed (role or status). Cosmetic updates
		// (nickname, email, etc.) shouldn't force every open tab of
		// the affected user to refetch /auth/me.
		if body.Role != nil || body.Status != nil {
			auth.BumpPermissionsEpoch(id)
		}
	}

	// Sync project_members to the new role when it changed.
	//
	// member/admin → external: the user loses their default editor access
	// on every project. Strip the seeded 'editor' rows so they fall back to
	// "no access unless explicitly granted".
	//
	// external → member/admin: seed editor rows so the user regains the
	// default internal access on non-deleted projects.
	if body.Role != nil && *body.Role != oldRole {
		newRole := *body.Role
		var actorID int64
		if actor != nil {
			actorID = actor.ID
		}
		wasInternal := auth.HasDefaultProjectAccess(oldRole)
		nowInternal := auth.HasDefaultProjectAccess(newRole)
		ctx := r.Context()
		switch {
		case wasInternal && !nowInternal:
			// Demotion: drop the auto-seeded editor rows. Emit audit rows
			// per revoked project so the change is traceable.
			rows, qErr := db.DB.Query(
				"SELECT project_id FROM project_members WHERE user_id=? AND access_level='editor'", id,
			)
			if qErr == nil {
				var revoked []int64
				for rows.Next() {
					var pid int64
					if err := rows.Scan(&pid); err == nil {
						revoked = append(revoked, pid)
					}
				}
				rows.Close()
				if _, err := db.DB.Exec(
					"DELETE FROM project_members WHERE user_id=? AND access_level='editor'", id,
				); err != nil {
					log.Printf("UpdateUser: demote cleanup user=%d: %v", id, err)
				}
				for _, pid := range revoked {
					auth.RecordAccessChange(ctx, nil, pid, id, auth.AuditActionRevoke, auth.AccessEditor, auth.AccessNone, actorID)
				}
			}
			auth.SeedAccessForUser(id, newRole)
		case !wasInternal && nowInternal:
			// Promotion: seed editor rows. SeedAccessForUser uses
			// INSERT OR IGNORE, so existing explicit grants are preserved.
			// We read the set of projects that will receive a new row so
			// we can emit audit entries for each grant.
			rows, qErr := db.DB.Query(`
				SELECT p.id FROM projects p
				WHERE p.status != 'deleted'
				  AND NOT EXISTS (
				    SELECT 1 FROM project_members pm
				    WHERE pm.user_id = ? AND pm.project_id = p.id
				  )
			`, id)
			var granted []int64
			if qErr == nil {
				for rows.Next() {
					var pid int64
					if err := rows.Scan(&pid); err == nil {
						granted = append(granted, pid)
					}
				}
				rows.Close()
			}
			auth.SeedAccessForUser(id, newRole)
			for _, pid := range granted {
				auth.RecordAccessChange(ctx, nil, pid, id, auth.AuditActionGrant, auth.AccessNone, auth.AccessEditor, actorID)
			}
		}
	}

	if body.Password != nil && *body.Password != "" {
		if len(*body.Password) < auth.MinPasswordLen {
			jsonError(w, fmt.Sprintf("new password must be at least %d characters", auth.MinPasswordLen), http.StatusBadRequest)
			return
		}
		hash, err := auth.HashPassword(*body.Password)
		if err != nil {
			jsonError(w, "hash failed", http.StatusInternalServerError)
			return
		}

		// An admin resetting another user's password forces that user to
		// rotate the admin-known value on next login (matches the
		// must_change_password=ON default on creation, PAI-321) and must
		// invalidate their live sessions so the reset actually locks them
		// out — the way the self-service reset and self-change flows
		// already behave. When admins edit their OWN password it's a
		// deliberate set, so we neither force a re-change nor kill the
		// session they're acting from. An explicit must_change_password in
		// the request always wins.
		actorIsTarget := actor != nil && actor.ID == id
		forceChange := 1
		if actorIsTarget {
			forceChange = 0
		}
		if body.MustChangePassword != nil {
			if *body.MustChangePassword {
				forceChange = 1
			} else {
				forceChange = 0
			}
		}

		ptx, err := db.DB.BeginTx(r.Context(), nil)
		if err != nil {
			log.Printf("UpdateUser: begin pw tx user=%d: %v", id, err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer ptx.Rollback()
		if _, err := ptx.ExecContext(r.Context(),
			"UPDATE users SET password=?, must_change_password=? WHERE id=?", hash, forceChange, id); err != nil {
			log.Printf("UpdateUser: password update user=%d: %v", id, err)
			jsonError(w, "password update failed", http.StatusInternalServerError)
			return
		}
		// Drop the target's sessions. Keep the actor's current session
		// only when they are resetting their own password.
		if keepSID := auth.CurrentSessionID(r); actorIsTarget && keepSID != "" {
			_, err = ptx.ExecContext(r.Context(), "DELETE FROM sessions WHERE user_id=? AND id != ?", id, keepSID)
		} else {
			_, err = ptx.ExecContext(r.Context(), "DELETE FROM sessions WHERE user_id=?", id)
		}
		if err != nil {
			log.Printf("UpdateUser: prune sessions user=%d: %v", id, err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := ptx.Commit(); err != nil {
			log.Printf("UpdateUser: commit pw tx user=%d: %v", id, err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	var u models.User
	if err := scanUserWithTOTP(db.DB.QueryRow("SELECT "+userSelectColsWithTOTP+" FROM users WHERE id=?", id), &u); err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	jsonOK(w, u)
}

// ResetUserTOTP disables 2FA for a user — admin only, no password required.
// POST /api/users/{id}/reset-totp
func ResetUserTOTP(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if _, err := db.DB.Exec("UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?", id); err != nil {
		jsonError(w, "failed to reset 2FA", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]bool{"reset": true})
}

// DisableUser sets status to 'inactive' — account disabled, data preserved.
func DisableUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	caller := auth.GetUser(r)
	if caller != nil && caller.ID == id {
		jsonError(w, "cannot disable your own account", http.StatusBadRequest)
		return
	}
	res, err := db.DB.Exec("UPDATE users SET status='inactive' WHERE id=? AND status='active'", id)
	if err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, "not found or not active", http.StatusNotFound)
		return
	}
	// invalidate sessions
	if _, err := db.DB.Exec("DELETE FROM sessions WHERE user_id=?", id); err != nil {
		log.Printf("DisableUser: delete sessions user_id=%d: %v", id, err)
	}
	auth.BumpPermissionsEpoch(id) // PAI-320
	w.WriteHeader(http.StatusNoContent)
}

// DeleteUser sets status to 'deleted' — hidden from UI, restorable via DB.
func DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	caller := auth.GetUser(r)
	if caller != nil && caller.ID == id {
		jsonError(w, "cannot delete your own account", http.StatusBadRequest)
		return
	}
	res, err := db.DB.Exec("UPDATE users SET status='deleted' WHERE id=? AND status != 'deleted'", id)
	if err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		jsonError(w, "not found or already deleted", http.StatusNotFound)
		return
	}
	if _, err := db.DB.Exec("DELETE FROM sessions WHERE user_id=?", id); err != nil {
		log.Printf("DeleteUser: delete sessions user_id=%d: %v", id, err)
	}
	if _, err := db.DB.Exec("DELETE FROM api_keys WHERE user_id=?", id); err != nil {
		log.Printf("DeleteUser: delete api_keys user_id=%d: %v", id, err)
	}
	auth.BumpPermissionsEpoch(id) // PAI-320
	w.WriteHeader(http.StatusNoContent)
}
