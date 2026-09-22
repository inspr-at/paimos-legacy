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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// PATCH /api/auth/me — self-service profile update.
// Any authenticated user can update their own nickname, first_name, last_name, email.
// { "nickname": "MxB", "first_name": "Alex", "last_name": "Example", "email": "markus@barta.com" }
func UpdateProfile(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)
	var body struct {
		FirstName               *string `json:"first_name"`
		LastName                *string `json:"last_name"`
		Email                   *string `json:"email"`
		MarkdownDefault         *bool   `json:"markdown_default"`
		MonospaceFields         *bool   `json:"monospace_fields"`
		RecentProjectsLimit     *int    `json:"recent_projects_limit"`
		RecentTimersLimit       *int    `json:"recent_timers_limit"`
		Timezone                *string `json:"timezone"`
		ShowAltUnitTable        *bool   `json:"show_alt_unit_table"`
		ShowAltUnitDetail       *bool   `json:"show_alt_unit_detail"`
		Locale                  *string `json:"locale"`
		PreviewHoverDelay       *int    `json:"preview_hover_delay"`
		IssueAutoRefreshEnabled *bool   `json:"issue_auto_refresh_enabled"`
		IssueAutoRefreshSeconds *int    `json:"issue_auto_refresh_interval_seconds"`
		AccrualsStatsEnabled    *bool   `json:"accruals_stats_enabled"`
		AccrualsExtraStatuses   *string `json:"accruals_extra_statuses"`
		SearchScopeShortcut     *string `json:"search_scope_shortcut"`
		// PAI-706: nullable override; 0 clears back to the instance default.
		IntakeConfidenceThreshold *int `json:"intake_confidence_threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Email != nil && !validateUniqueUserEmail(w, r, db.DB, user.ID, *body.Email) {
		return
	}
	// PAI-368: search-scope shortcut. Empty string disables. Otherwise
	// must be a JSON object with `code` and at least one true modifier
	// among ctrl/alt/meta — shift-only chords collide with normal typing.
	// 256-byte cap is generous for {ctrl,shift,alt,meta,code,key,label}.
	if body.SearchScopeShortcut != nil {
		if err := validateSearchScopeShortcut(*body.SearchScopeShortcut); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	// Accruals fields are admin-only — silently drop for non-admins
	if !auth.IsAdmin(user) {
		body.AccrualsStatsEnabled = nil
		body.AccrualsExtraStatuses = nil
	}
	if body.IssueAutoRefreshSeconds != nil && *body.IssueAutoRefreshSeconds < 10 {
		v := 10
		body.IssueAutoRefreshSeconds = &v
	}
	// PAI-706: 0 clears the override (falls back to the instance default);
	// anything else must be inside 50..100.
	if body.IntakeConfidenceThreshold != nil {
		v := *body.IntakeConfidenceThreshold
		if v != 0 && (v < 50 || v > 100) {
			jsonError(w, "intake_confidence_threshold must be between 50 and 100 (or 0 to clear)", http.StatusBadRequest)
			return
		}
	}
	// Convert *bool to *int for SQLite COALESCE (SQLite has no native bool)
	var mdDefault, monoFields, altTable, altDetail, issueAutoRefreshEnabled, accrualsEnabled *int
	if body.MarkdownDefault != nil {
		v := boolToInt(*body.MarkdownDefault)
		mdDefault = &v
	}
	if body.MonospaceFields != nil {
		v := boolToInt(*body.MonospaceFields)
		monoFields = &v
	}
	if body.ShowAltUnitTable != nil {
		v := boolToInt(*body.ShowAltUnitTable)
		altTable = &v
	}
	if body.ShowAltUnitDetail != nil {
		v := boolToInt(*body.ShowAltUnitDetail)
		altDetail = &v
	}
	if body.IssueAutoRefreshEnabled != nil {
		v := boolToInt(*body.IssueAutoRefreshEnabled)
		issueAutoRefreshEnabled = &v
	}
	if body.AccrualsStatsEnabled != nil {
		v := boolToInt(*body.AccrualsStatsEnabled)
		accrualsEnabled = &v
	}

	_, err := db.DB.Exec(`
		UPDATE users SET
			first_name              = COALESCE(?, first_name),
			last_name               = COALESCE(?, last_name),
			email                   = COALESCE(?, email),
			markdown_default        = COALESCE(?, markdown_default),
			monospace_fields        = COALESCE(?, monospace_fields),
			recent_projects_limit   = COALESCE(?, recent_projects_limit),
			recent_timers_limit     = COALESCE(?, recent_timers_limit),
			timezone                = COALESCE(?, timezone),
			show_alt_unit_table     = COALESCE(?, show_alt_unit_table),
			show_alt_unit_detail    = COALESCE(?, show_alt_unit_detail),
			locale                  = COALESCE(?, locale),
			preview_hover_delay     = COALESCE(?, preview_hover_delay),
			issue_auto_refresh_enabled = COALESCE(?, issue_auto_refresh_enabled),
			issue_auto_refresh_interval_seconds = COALESCE(?, issue_auto_refresh_interval_seconds),
			accruals_stats_enabled  = COALESCE(?, accruals_stats_enabled),
			accruals_extra_statuses = COALESCE(?, accruals_extra_statuses),
			search_scope_shortcut   = COALESCE(?, search_scope_shortcut),
			intake_confidence_threshold = CASE WHEN ?=0 THEN NULL ELSE COALESCE(?, intake_confidence_threshold) END
		WHERE id = ?
	`, body.FirstName, body.LastName, body.Email, mdDefault, monoFields, body.RecentProjectsLimit, body.RecentTimersLimit, body.Timezone, altTable, altDetail, body.Locale, body.PreviewHoverDelay, issueAutoRefreshEnabled, body.IssueAutoRefreshSeconds, accrualsEnabled, body.AccrualsExtraStatuses, body.SearchScopeShortcut, body.IntakeConfidenceThreshold, body.IntakeConfidenceThreshold, user.ID)
	if err != nil {
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}

	var u models.User
	scanUser(db.DB.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id=?", user.ID), &u)
	jsonOK(w, u)
}

// POST /api/auth/avatar — upload profile image (multipart/form-data, field "avatar").
// Accepts JPG/PNG, max 3MB. Resizes to max 500×500px, saves as JPEG.
// Returns updated User with new avatar_path.
func UploadAvatar(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)

	// 3MB limit. Hard-cap the request body (plus 1MB multipart overhead) so a
	// malicious client can't exhaust memory before the size check.
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20+1<<20)
	// #nosec G120 -- body is hard-capped by MaxBytesReader above; the rule is syntactic.
	if err := r.ParseMultipartForm(3 << 20); err != nil {
		jsonError(w, "file too large (max 3MB)", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		jsonError(w, "avatar field required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read into memory for content-type check + decode
	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, file); err != nil {
		jsonError(w, "read failed", http.StatusInternalServerError)
		return
	}

	// Detect content type
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(buf.Bytes())
	}
	if ct != "image/jpeg" && ct != "image/png" {
		jsonError(w, "only JPEG and PNG images are accepted", http.StatusBadRequest)
		return
	}

	// Process: resize to max 200×200, convert to JPEG, strip EXIF
	processed, _, err := processImage(bytes.NewReader(buf.Bytes()), 200, 200, 85, false)
	if err != nil {
		jsonError(w, "invalid image", http.StatusBadRequest)
		return
	}

	// Save to DATA_DIR/avatars/{id}.jpg
	avatarsDir := filepath.Join(getDataDir(), "avatars")
	if err := os.MkdirAll(avatarsDir, 0o750); err != nil {
		jsonError(w, "storage error", http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("%d.jpg", user.ID)
	destPath := filepath.Join(avatarsDir, filename)
	// #nosec G703 -- destPath is DATA_DIR plus a filename derived from the authenticated user's numeric ID; no client-controlled path segments.
	if err := os.WriteFile(destPath, processed, 0o600); err != nil {
		jsonError(w, "write error", http.StatusInternalServerError)
		return
	}

	// Store path as /api/avatars/{id}.jpg — served via dedicated route, survives container rebuilds
	relPath := "/api/avatars/" + filename
	if _, err := db.DB.Exec("UPDATE users SET avatar_path=? WHERE id=?", relPath, user.ID); err != nil {
		jsonError(w, "db update failed", http.StatusInternalServerError)
		return
	}

	var u models.User
	scanUser(db.DB.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id=?", user.ID), &u)
	jsonOK(w, u)
}

// DELETE /api/auth/avatar — remove avatar, revert to initial.
func DeleteAvatar(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r)

	// Delete file if exists — strip /api/avatars/ prefix to get filename
	var old string
	if err := db.DB.QueryRow("SELECT avatar_path FROM users WHERE id=?", user.ID).Scan(&old); err != nil {
		log.Printf("scan error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if old != "" {
		filename := filepath.Base(old)
		_ = os.Remove(filepath.Join(getDataDir(), "avatars", filename))
	}

	if _, err := db.DB.Exec("UPDATE users SET avatar_path='' WHERE id=?", user.ID); err != nil {
		log.Printf("DeleteAvatar: user_id=%d: %v", user.ID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	var u models.User
	scanUser(db.DB.QueryRow("SELECT "+userSelectCols+" FROM users WHERE id=?", user.ID), &u)
	jsonOK(w, u)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// validateSearchScopeShortcut accepts an empty string ("disabled") or a
// JSON object describing one keyboard chord. The frontend records the
// chord live (KeyboardEvent → {ctrl,shift,alt,meta,code,key,label}); we
// only validate the shape so a malformed value can't quietly persist
// and confuse the matcher later. Shift-only chords are rejected — they
// collide with normal typing inside the search input.
func validateSearchScopeShortcut(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > 256 {
		return fmt.Errorf("search_scope_shortcut: too long (max 256 bytes)")
	}
	var c struct {
		Ctrl  bool   `json:"ctrl"`
		Shift bool   `json:"shift"`
		Alt   bool   `json:"alt"`
		Meta  bool   `json:"meta"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return fmt.Errorf("search_scope_shortcut: not valid JSON")
	}
	if c.Code == "" {
		return fmt.Errorf("search_scope_shortcut: code is required")
	}
	if !(c.Ctrl || c.Alt || c.Meta) {
		return fmt.Errorf("search_scope_shortcut: must include Ctrl, Alt, or Meta (Cmd)")
	}
	return nil
}

func getDataDir() string {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}
	return "/app/data"
}
