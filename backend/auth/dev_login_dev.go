// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

//go:build dev_login

// PAI-267 — agent-driveable dev login.
//
// This file ONLY compiles when the `dev_login` build tag is set. The
// release Dockerfile builds without it, so the handler symbol does not
// exist in production binaries — flipping a runtime flag cannot
// re-enable it. The companion file `dev_login_prod.go` provides
// no-op stubs for non-dev builds; keep their public surfaces identical
// so main.go links cleanly either way.
//
// Defence layers (per PAI-267 spec):
//   1. Build tag (this file)               — load-bearing
//   2. PAIMOS_DEV_LOGIN_TOKEN per-call     — long random token
//   3. Boot panic if PAIMOS_ENV=production — belt + suspenders
//   4. Audit + via_dev_login banner + 24h  — runtime visibility cap

package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// DevLoginEnabled reports whether the dev_login build tag is active.
// Used by main.go to decide whether to mount POST /api/auth/dev-login.
// The prod stub returns false.
func DevLoginEnabled() bool { return true }

// devLoginToken is the validated PAIMOS_DEV_LOGIN_TOKEN, captured at
// boot. Empty string means dev-login is disabled (no env var set) —
// the handler refuses every request rather than panicking, so an
// operator who builds with the tag but forgets the env var gets a
// clear 503 + log line instead of a crash.
var devLoginToken string

// ValidateDevLoginConfig is called from main.go before the HTTP server
// starts. Three layers of refusal:
//
//  1. PAIMOS_ENV=production → panic. No dev binary may run in prod.
//  2. PAIMOS_DEV_LOGIN_TOKEN unset/empty → handler stays disabled,
//     warning logged. Operator can still run a server with the dev
//     build (e.g. for local testing without dev-login enabled), they
//     just can't dev-login.
//  3. PAIMOS_DEV_LOGIN_TOKEN present → must be ≥32 chars and not on
//     the dummy-value blocklist. Anything else → panic, since the
//     operator clearly meant to enable dev-login but did so unsafely.
func ValidateDevLoginConfig() {
	if env := strings.ToLower(strings.TrimSpace(os.Getenv("PAIMOS_ENV"))); env == "production" || env == "prod" {
		panic("dev_login build tag is active but PAIMOS_ENV=" + env + " — refusing to start. Rebuild without -tags dev_login for production.")
	}
	tok := os.Getenv("PAIMOS_DEV_LOGIN_TOKEN")
	if tok == "" {
		// Reset, so re-running ValidateDevLoginConfig (e.g. across
		// test cases) returns the handler to the disabled state.
		devLoginToken = ""
		log.Printf("⚠️  dev_login build tag active but PAIMOS_DEV_LOGIN_TOKEN is unset — POST /api/auth/dev-login will return 503")
		return
	}
	if len(tok) < 32 {
		panic(fmt.Sprintf("PAIMOS_DEV_LOGIN_TOKEN must be at least 32 chars (got %d) — generate via `openssl rand -hex 32`", len(tok)))
	}
	switch strings.ToLower(strings.TrimSpace(tok)) {
	case "1", "true", "dev", "password", "secret", "token":
		panic("PAIMOS_DEV_LOGIN_TOKEN looks like a placeholder — generate a real token via `openssl rand -hex 32`")
	}
	devLoginToken = tok
	sum := sha256.Sum256([]byte(tok))
	log.Printf("⚠️  DEV-LOGIN ROUTE ENABLED — token sha256 prefix: %s — DO NOT USE IN PRODUCTION", hex.EncodeToString(sum[:])[:8])
}

// devLoginSessionTTL caps any session created via dev-login at 24h
// regardless of the global sessionDuration. A forgotten browser tab
// running a dev session can't grant indefinite access.
const devLoginSessionTTL = 24 * time.Hour

// DevLoginHandler is POST /api/auth/dev-login. Expects
// {username, token}; mints a session cookie + CSRF, flags the session
// `via_dev_login=1`, hard-caps expiry at 24h, writes a `audit:` log
// line. Errors return 401 with the same message regardless of cause
// so an attacker probing the endpoint can't tell whether the username
// existed or the token was wrong.
func DevLoginHandler(w http.ResponseWriter, r *http.Request) {
	if !PasswordLoginEnabled() {
		http.Error(w, `{"error":"invalid dev-login credentials"}`, http.StatusUnauthorized)
		return
	}
	if devLoginToken == "" {
		http.Error(w, `{"error":"dev-login is not configured"}`, http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Token == "" {
		http.Error(w, `{"error":"username and token required"}`, http.StatusBadRequest)
		return
	}
	// Constant-time compare of the token; bail with 401 + uniform error
	// regardless of which leg failed.
	if subtle.ConstantTimeCompare([]byte(body.Token), []byte(devLoginToken)) != 1 {
		log.Printf("audit: dev_login_failed username=%q ip=%s reason=token_mismatch", body.Username, clientIP(r))
		http.Error(w, `{"error":"invalid dev-login credentials"}`, http.StatusUnauthorized)
		return
	}
	var loginUser models.User
	if err := db.DB.QueryRow(
		"SELECT "+userSelectCols+" FROM users u WHERE u.username=?", body.Username,
	).Scan(userScanDests(&loginUser)...); err != nil {
		log.Printf("audit: dev_login_failed username=%q ip=%s reason=user_not_found", body.Username, clientIP(r))
		http.Error(w, `{"error":"invalid dev-login credentials"}`, http.StatusUnauthorized)
		return
	}
	if loginUser.Status != "active" {
		log.Printf("audit: dev_login_failed username=%q ip=%s reason=account_disabled", body.Username, clientIP(r))
		http.Error(w, `{"error":"invalid dev-login credentials"}`, http.StatusUnauthorized)
		return
	}

	now := time.Now()
	expiresAt := now.Add(devLoginSessionTTL)
	sid, err := createSession(r.Context(), loginUser.ID, now, expiresAt, true, false)
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	setSessionCookieValue(w, sid, expiresAt)
	if _, err := IssueCSRFForSession(w, sid); err != nil {
		log.Printf("DevLoginHandler: issue csrf token: %v", err)
	}
	if _, err := db.DB.Exec("UPDATE users SET last_login_at=datetime('now') WHERE id=?", loginUser.ID); err != nil {
		log.Printf("DevLoginHandler: update last_login_at user_id=%d: %v", loginUser.ID, err)
	}

	log.Printf("audit: dev_login_ok username=%q user_id=%d ip=%s", body.Username, loginUser.ID, clientIP(r))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":            true,
		"user":          loginUser,
		"via_dev_login": true,
		"expires_at":    expiresAt.UTC().Format(time.RFC3339),
	})
}
