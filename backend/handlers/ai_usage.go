// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// PAI-161. AI usage cap — soft daily budget per user.
//
// Why soft, not hard
// ------------------
// We don't bill users; the cap exists to prevent a runaway loop or a
// curious admin from accidentally racking up a $50 OpenRouter bill
// in an afternoon. So:
//   - Admins are exempt from the block (they get a header that the UI
//     can use to warn). Admins setting up a fresh instance need to
//     experiment freely.
//   - Per-user override (`users.ai_cap_override_tokens`) lets admins
//     bump or zero a specific user.
//   - The default cap is generous (100k tokens/day ≈ 50 optimize
//     calls on a typical issue field). A team that needs more bumps
//     it via env var or per-user override.
//
// Accounting model
// ----------------
//   - One row per (user_id, day_UTC). `request_count` increments by
//     one per AI call, regardless of outcome — even failures consumed
//     network and provider time.
//   - `prompt_tokens` / `completion_tokens` increment with the values
//     the upstream returned. Failure modes that didn't reach the
//     provider report 0/0 and don't move the meter.
//   - Increments happen AFTER the audit line is written, so the
//     audit log and the meter agree.
//
// Endpoint surface
// ----------------
//   - GET /api/ai/usage — admin-only summary of today's usage and
//     per-user totals. Used by the Settings → AI usage panel.
//   - The cap check is a function used by the dispatcher (PAI-163)
//     and the ai_optimize handler — not its own endpoint.

package handlers

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

const (
	// defaultDailyTokenCap is the per-user default. Roughly 50
	// optimize calls on a typical issue field with a model that
	// returns ~2k completion tokens. Override via env var when
	// the team needs more.
	defaultDailyTokenCap = 100_000

	// envVarDailyTokenCap is the env name that overrides the default.
	// Set in the PAIMOS container (compose / k8s manifest) to raise
	// or lower the team-wide default.
	envVarDailyTokenCap = "PAIMOS_AI_DAILY_CAP_TOKENS"
)

// dayUTC returns the YYYY-MM-DD bucket the usage row is keyed by.
// UTC keeps day boundaries from wandering for teams across multiple
// timezones — admins reading the meter at 3am local won't see
// yesterday's row come back to life.
func dayUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

// resolvedCapForUser is the cap that applies to one user. Falls back
// through user override → env override → compiled default.
//
// A value of 0 explicitly disables AI for the user (admin-set zero).
// Negative values are coerced to default — defensive against bad
// admin input via the user editor.
func resolvedCapForUser(userID int64) int64 {
	defaultCap := int64(defaultDailyTokenCap)
	if envS := os.Getenv(envVarDailyTokenCap); envS != "" {
		if v, err := strconv.ParseInt(envS, 10, 64); err == nil && v >= 0 {
			defaultCap = v
		}
	}
	var override sql.NullInt64
	err := db.DB.QueryRow(
		`SELECT ai_cap_override_tokens FROM users WHERE id = ?`, userID,
	).Scan(&override)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultCap
	}
	if err != nil {
		log.Printf("ai_usage: resolve cap for user %d: %v", userID, err)
		return defaultCap
	}
	if !override.Valid {
		return defaultCap
	}
	if override.Int64 < 0 {
		return defaultCap
	}
	return override.Int64
}

// usedTodayForUser sums prompt + completion tokens for the user on
// today's UTC day. Returns 0 (not error) on missing row — the row
// is created on first increment.
func usedTodayForUser(userID int64) (int64, error) {
	var p, c sql.NullInt64
	err := db.DB.QueryRow(
		`SELECT prompt_tokens, completion_tokens FROM ai_usage WHERE user_id = ? AND day = ?`,
		userID, dayUTC(),
	).Scan(&p, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return p.Int64 + c.Int64, nil
}

// CheckUsageCap is the gate any AI handler calls before issuing a
// provider request. Returns:
//   - ok=true and remaining (positive) when the user has budget left.
//   - ok=false and over=true when the cap is exceeded — the caller
//     should refuse with 429 and a structured body.
//   - ok=true and bypass=true when the user is admin (exempt block);
//     UI can still warn.
func CheckUsageCap(userID int64, isAdmin bool) (ok bool, remaining int64, over bool, bypass bool) {
	cap := resolvedCapForUser(userID)
	used, err := usedTodayForUser(userID)
	if err != nil {
		log.Printf("ai_usage: read used: %v", err)
		// Fail open: a DB hiccup must not block AI. The audit line
		// still records the call, so post-hoc analysis is unaffected.
		return true, cap, false, false
	}
	if cap == 0 {
		// Explicit admin-set zero disables AI for the user. Admins
		// themselves still bypass — they need to test before re-enabling
		// for everyone.
		if isAdmin {
			return true, 0, true, true
		}
		return false, 0, true, false
	}
	remaining = cap - used
	if remaining <= 0 {
		if isAdmin {
			return true, remaining, true, true
		}
		return false, remaining, true, false
	}
	return true, remaining, false, false
}

// RecordUsage increments the meter for a single call. Idempotent
// per (user, day) via UPSERT. Always called AFTER the audit line is
// written so log + DB stay aligned.
func RecordUsage(userID int64, promptTokens, completionTokens int) {
	if userID == 0 {
		return
	}
	_, err := db.DB.Exec(
		`INSERT INTO ai_usage(user_id, day, prompt_tokens, completion_tokens, request_count, updated_at)
		 VALUES (?, ?, ?, ?, 1, datetime('now'))
		 ON CONFLICT(user_id, day) DO UPDATE SET
			prompt_tokens     = prompt_tokens     + excluded.prompt_tokens,
			completion_tokens = completion_tokens + excluded.completion_tokens,
			request_count     = request_count     + 1,
			updated_at        = datetime('now')`,
		userID, dayUTC(), promptTokens, completionTokens,
	)
	if err != nil {
		log.Printf("ai_usage: record: %v", err)
	}
}

// aiUsageRow is one entry in the per-user list returned by the
// admin endpoint.
type aiUsageRow struct {
	UserID           int64  `json:"user_id"`
	Username         string `json:"username"`
	IsAdmin          bool   `json:"is_admin"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	RequestCount     int64  `json:"request_count"`
	CapEffective     int64  `json:"cap_effective"`
	CapOverride      *int64 `json:"cap_override"`
	OverCap          bool   `json:"over_cap"`
}

// aiUsageResponse is the body served at GET /api/ai/usage. Day is
// the UTC bucket the rows belong to; the SPA renders it next to
// the totals so admins know which timezone they're seeing.
type aiUsageResponse struct {
	Day                 string       `json:"day"`
	DefaultCap          int64        `json:"default_cap"`
	OrgPromptTokens     int64        `json:"org_prompt_tokens"`
	OrgCompletionTokens int64        `json:"org_completion_tokens"`
	OrgRequestCount     int64        `json:"org_request_count"`
	Users               []aiUsageRow `json:"users"`
}

// AIUsage handles GET /api/ai/usage — admin-only summary.
func AIUsage(w http.ResponseWriter, r *http.Request) {
	day := dayUTC()
	resp := aiUsageResponse{
		Day:        day,
		DefaultCap: int64(defaultDailyTokenCap),
	}
	if envS := os.Getenv(envVarDailyTokenCap); envS != "" {
		if v, err := strconv.ParseInt(envS, 10, 64); err == nil && v >= 0 {
			resp.DefaultCap = v
		}
	}

	const q = `
SELECT u.id, u.username,
       CASE
         WHEN u.is_reviewer = 1 THEN 'reviewer'
         WHEN u.is_super_admin = 1 THEN 'super_admin'
         WHEN u.role_key = 'member' AND u.role IN ('admin','external') THEN u.role
         WHEN u.role_key IN ('admin','member','external','super_admin') THEN u.role_key
         WHEN u.role IN ('admin','member','external') THEN u.role
         ELSE 'member'
       END,
       u.ai_cap_override_tokens,
       COALESCE(au.prompt_tokens, 0)     AS pt,
       COALESCE(au.completion_tokens, 0) AS ct,
       COALESCE(au.request_count, 0)     AS rc
FROM users u
LEFT JOIN ai_usage au ON au.user_id = u.id AND au.day = ?
ORDER BY (COALESCE(au.prompt_tokens, 0) + COALESCE(au.completion_tokens, 0)) DESC,
         u.username ASC
`
	rows, err := db.DB.Query(q, day)
	if err != nil {
		log.Printf("ai_usage: list: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var row aiUsageRow
		var role string
		var override sql.NullInt64
		if err := rows.Scan(&row.UserID, &row.Username, &role, &override, &row.PromptTokens, &row.CompletionTokens, &row.RequestCount); err != nil {
			log.Printf("ai_usage: scan: %v", err)
			continue
		}
		row.IsAdmin = auth.IsAdminRole(role)
		if override.Valid {
			v := override.Int64
			row.CapOverride = &v
			row.CapEffective = v
		} else {
			row.CapEffective = resp.DefaultCap
		}
		used := row.PromptTokens + row.CompletionTokens
		row.OverCap = row.CapEffective > 0 && used >= row.CapEffective
		resp.Users = append(resp.Users, row)
		resp.OrgPromptTokens += row.PromptTokens
		resp.OrgCompletionTokens += row.CompletionTokens
		resp.OrgRequestCount += row.RequestCount
	}
	jsonOK(w, resp)
}
