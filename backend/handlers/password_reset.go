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

// Package handlers — password reset flow (forgot / reset / validate).
//
// Design:
//
//  1. POST /api/auth/forgot  { "email": "..." }
//     - Always returns 202 regardless of whether the email exists.
//     This prevents user enumeration via timing or status codes.
//     - On a real match, generates a 32-byte crypto-random token,
//     stores its sha256 hash in password_reset_tokens with a 60min
//     TTL, and either:
//     * SMTP_HOST set → sends the magic link via net/smtp
//     * SMTP_HOST unset → logs the link to stdout (dev/staging mode)
//
//  2. GET /api/auth/reset/validate?token=...
//     - Returns { "valid": true } or { "valid": false, "reason": ... }
//     - Used by the reset view to show "link expired" before asking
//     the user to type a new password.
//
//  3. POST /api/auth/reset  { "token": "...", "new_password": "..." }
//     - Looks up the hashed token, verifies not expired / not used.
//     - Updates users.password with a fresh bcrypt hash.
//     - Marks the token used_at=now (single-use).
//     - DELETE FROM sessions WHERE user_id=? — invalidates every
//     existing session as defense in depth.
//     - Returns 200 on success, 400 with a generic message otherwise.
//
// Security knobs:
//
//   - Tokens are high-entropy (32 bytes from crypto/rand), so sha256 is
//     sufficient as the DB-side hash — bcrypt is for low-entropy inputs
//     (passwords), not for random tokens.
//   - The raw token is in the email URL only; it never hits the DB.
//   - Rate limited via auth.AllowAttempt with scopes "forgot" and "reset"
//     — shares the same 5-per-10-minute IP+identity window as login.
//   - On successful reset, all sessions for that user are dropped so a
//     stolen password can't survive a reset.
//   - Minimum password length is 8 chars (matches existing convention).
package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/secretinput"
)

const (
	passwordResetTTL        = 60 * time.Minute
	passwordResetTokenBytes = 32
	// passwordResetMinLen mirrors the shared policy so every password-write
	// flow enforces the same floor (see auth.MinPasswordLen).
	passwordResetMinLen = auth.MinPasswordLen
)

// ── Token generation + hashing ────────────────────────────────────────────

// newResetToken returns a URL-safe random token string suitable for
// embedding in a magic link. 32 bytes → 43 base64url chars.
func newResetToken() (string, error) {
	b := make([]byte, passwordResetTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashResetToken returns a deterministic hex sha256 of the raw token for
// DB storage + lookup. Deterministic so we can index/select by it.
func hashResetToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ── SMTP sending ──────────────────────────────────────────────────────────

// sendResetEmail either delivers the reset link via SMTP or, when SMTP is
// explicitly running in local-dev mode (PAIMOS_DEV_MODE=true), logs the
// full link to stdout so a developer can grab it from container output.
//
// PAI-115: previously the no-SMTP branch logged the raw link unconditionally.
// That meant any shared staging or accidental production misconfiguration
// dropped working magic links into application logs. The dev-only opt-in
// closes that gap; in any other unconfigured-SMTP scenario we now refuse
// to send and surface an error so operators notice the issue immediately
// instead of silently routing reset links into log aggregators.
func sendResetEmail(toEmail, link string) error {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		if os.Getenv("PAIMOS_DEV_MODE") == "true" {
			log.Printf("[password-reset] PAIMOS_DEV_MODE=true — reset link for %s: %s", toEmail, link)
			return nil
		}
		// SMTP missing AND not in explicit dev mode. Don't log the link;
		// surface the misconfiguration so the operator notices instead
		// of leaking secrets into shared logs.
		log.Printf("[password-reset] SMTP_HOST unset and PAIMOS_DEV_MODE not set — refusing to send reset link for %s", toEmail)
		return fmt.Errorf("SMTP not configured")
	}

	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	from := brand.Default.EmailFrom
	if from == "" {
		// SMTP is configured but no From set — fall back to a noreply at the
		// product website. Operators should set BRAND_EMAIL_FROM explicitly.
		from = "noreply@" + hostFromURL(brand.Default.WebsiteURL)
	}
	user := os.Getenv("SMTP_USER")
	pass, err := secretinput.Optional("SMTP_PASS")
	if err != nil {
		return fmt.Errorf("SMTP configuration: %w", err)
	}

	subject := brand.Default.ProductName + " password reset"
	body := fmt.Sprintf(
		"You (or someone using your email) requested a password reset for "+brand.Default.ProductName+".\r\n"+
			"\r\n"+
			"Open this link within 60 minutes to choose a new password:\r\n"+
			"\r\n"+
			"  %s\r\n"+
			"\r\n"+
			"If you did not request this, ignore this email. Your password\r\n"+
			"will not change unless you follow the link.\r\n",
		link,
	)
	msg := []byte("From: " + from + "\r\n" +
		"To: " + toEmail + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		body)

	addr := host + ":" + port
	var authMech smtp.Auth
	if user != "" {
		authMech = smtp.PlainAuth("", user, pass, host)
	}
	if err := smtp.SendMail(addr, authMech, from, []string{toEmail}, msg); err != nil {
		log.Printf("[password-reset] SMTP send to %s failed: %v", toEmail, err)
		return err
	}
	return nil
}

// appBaseURL returns the public URL PAIMOS is reachable at for building
// magic links. Reads BRAND_PUBLIC_URL; falls back to BRAND_WEBSITE_URL.
func appBaseURL() string {
	if brand.Default.PublicURL != "" {
		return brand.Default.PublicURL
	}
	return strings.TrimRight(brand.Default.WebsiteURL, "/")
}

// hostFromURL extracts the host portion of a URL for fallback From-address
// construction (e.g. "https://paimos.com/foo" → "paimos.com"). Returns
// "localhost" if parsing fails so we never emit an invalid From header.
func hostFromURL(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "localhost"
	}
	return s
}

// ── POST /api/auth/forgot ─────────────────────────────────────────────────

func ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Even malformed input returns 202 — don't leak parser behaviour.
		respondForgotAccepted(w)
		return
	}
	email := strings.TrimSpace(strings.ToLower(body.Email))

	// Rate limit by IP and (if parseable) email. allowAuthAttempt keys
	// by both so one IP can't hit forgot with 1000 different emails.
	if allowed, retryAfter := auth.AllowAttempt("forgot", r, email); !allowed {
		auth.SetRetryAfter(w, retryAfter)
		respondForgotAccepted(w)
		return
	}

	// Always respond 202 regardless of what happens next. The user sees
	// the same response whether the email existed or not.
	defer respondForgotAccepted(w)

	if !auth.PasswordLoginEnabled() || email == "" {
		return
	}

	// Look up user by email. Silent failure on no-match — don't leak.
	var userID int64
	err := db.DB.QueryRow(
		"SELECT id FROM users WHERE lower(email) = ? AND status = 'active'",
		email,
	).Scan(&userID)
	if err == sql.ErrNoRows {
		// Count as a "failed attempt" against the rate limiter so
		// enumeration probes still trip the limit.
		auth.RecordFailure("forgot", r, email)
		log.Printf("[password-reset] forgot requested for unknown email %q from %s", email, auth.ClientIP(r))
		return
	}
	if err != nil {
		log.Printf("[password-reset] DB lookup error for %q: %v", email, err)
		return
	}

	// Generate, hash, insert, send.
	raw, err := newResetToken()
	if err != nil {
		log.Printf("[password-reset] rand.Read failed: %v", err)
		return
	}
	now := time.Now().UTC()
	_, err = db.DB.Exec(
		`INSERT INTO password_reset_tokens(user_id, token_hash, created_at, expires_at, ip_address)
		 VALUES(?, ?, ?, ?, ?)`,
		userID,
		hashResetToken(raw),
		now.Format(time.RFC3339),
		now.Add(passwordResetTTL).Format(time.RFC3339),
		auth.ClientIP(r),
	)
	if err != nil {
		log.Printf("[password-reset] insert token failed for user=%d: %v", userID, err)
		return
	}

	link := fmt.Sprintf("%s/reset/%s", appBaseURL(), url.PathEscape(raw))
	if err := sendResetEmail(email, link); err != nil {
		// Token is already persisted — user can retry or admin can send
		// the link manually from logs. Don't roll back.
		log.Printf("[password-reset] send failed but token persisted for user=%d: %v", userID, err)
	} else {
		log.Printf("[password-reset] token issued for user=%d from %s", userID, auth.ClientIP(r))
	}
}

func respondForgotAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "accepted",
		"message": "If an account with that email exists, a reset link has been sent.",
	})
}

// ── GET /api/auth/reset/validate ──────────────────────────────────────────

// ValidateResetToken lets the reset view check link validity before
// asking the user to type a new password. Returns {"valid":true} or
// {"valid":false,"reason":"expired|used|unknown"}.
func ValidateResetToken(w http.ResponseWriter, r *http.Request) {
	if !auth.PasswordLoginEnabled() {
		respondValidate(w, false, "unknown")
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("token"))
	if raw == "" {
		respondValidate(w, false, "unknown")
		return
	}
	// Rate-limit validate too — it's effectively a token-guessing oracle
	// otherwise. Same scope as reset.
	if allowed, retryAfter := auth.AllowAttempt("reset", r, ""); !allowed {
		auth.SetRetryAfter(w, retryAfter)
		respondValidate(w, false, "rate_limited")
		return
	}

	var expiresAt, usedAt sql.NullString
	err := db.DB.QueryRow(
		`SELECT expires_at, used_at FROM password_reset_tokens WHERE token_hash = ?`,
		hashResetToken(raw),
	).Scan(&expiresAt, &usedAt)
	if err == sql.ErrNoRows {
		respondValidate(w, false, "unknown")
		return
	}
	if err != nil {
		respondValidate(w, false, "unknown")
		return
	}
	if usedAt.Valid && usedAt.String != "" {
		respondValidate(w, false, "used")
		return
	}
	if exp, perr := time.Parse(time.RFC3339, expiresAt.String); perr != nil || time.Now().UTC().After(exp) {
		respondValidate(w, false, "expired")
		return
	}
	respondValidate(w, true, "")
}

func respondValidate(w http.ResponseWriter, valid bool, reason string) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"valid": valid}
	if !valid && reason != "" {
		resp["reason"] = reason
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// ── POST /api/auth/reset ──────────────────────────────────────────────────

func ResetPassword(w http.ResponseWriter, r *http.Request) {
	if !auth.PasswordLoginEnabled() {
		jsonError(w, "invalid or expired token", http.StatusBadRequest)
		return
	}
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(body.Token)
	newPassword := body.NewPassword
	if token == "" || len(newPassword) < passwordResetMinLen {
		jsonError(w, fmt.Sprintf("token required; new password must be at least %d characters", passwordResetMinLen), http.StatusBadRequest)
		return
	}

	if allowed, retryAfter := auth.AllowAttempt("reset", r, ""); !allowed {
		auth.SetRetryAfter(w, retryAfter)
		jsonError(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}

	// Look up unused, unexpired token.
	var tokenID, userID int64
	var expiresAtStr string
	var usedAt sql.NullString
	err := db.DB.QueryRow(
		`SELECT id, user_id, expires_at, used_at
		 FROM password_reset_tokens
		 WHERE token_hash = ?`,
		hashResetToken(token),
	).Scan(&tokenID, &userID, &expiresAtStr, &usedAt)
	if err == sql.ErrNoRows {
		auth.RecordFailure("reset", r, "")
		jsonError(w, "invalid or expired token", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("[password-reset] lookup error: %v", err)
		jsonError(w, "invalid or expired token", http.StatusBadRequest)
		return
	}
	if usedAt.Valid && usedAt.String != "" {
		auth.RecordFailure("reset", r, "")
		jsonError(w, "invalid or expired token", http.StatusBadRequest)
		return
	}
	if exp, perr := time.Parse(time.RFC3339, expiresAtStr); perr != nil || time.Now().UTC().After(exp) {
		auth.RecordFailure("reset", r, "")
		jsonError(w, "invalid or expired token", http.StatusBadRequest)
		return
	}

	// Hash and update the password.
	newHash, err := auth.HashPassword(newPassword)
	if err != nil {
		log.Printf("[password-reset] bcrypt failed: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Do the three writes (update password, mark token used, invalidate
	// sessions) in a transaction so we can't end up with a changed
	// password but surviving sessions.
	tx, err := db.DB.Begin()
	if err != nil {
		log.Printf("[password-reset] begin tx failed: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE users SET password = ? WHERE id = ?`, newHash, userID); err != nil {
		log.Printf("[password-reset] update password failed for user=%d: %v", userID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(
		`UPDATE password_reset_tokens SET used_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), tokenID,
	); err != nil {
		log.Printf("[password-reset] mark used failed for token=%d: %v", tokenID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		log.Printf("[password-reset] delete sessions failed for user=%d: %v", userID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[password-reset] commit failed: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	auth.ResetFailures("reset", r, "")
	log.Printf("[password-reset] password reset completed for user=%d from %s", userID, auth.ClientIP(r))

	jsonOK(w, map[string]any{"status": "ok"})
}
