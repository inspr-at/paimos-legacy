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
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"log"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/pquerna/otp/totp"

	"github.com/inspr-at/paimos/backend/db"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

func newTOTPToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func jsonOKMap(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ── Setup: generate secret + QR ───────────────────────────────────────────────

// GET /api/auth/totp/setup
// Returns { secret, qr_png_base64, issuer, account } for the authenticated user.
// Does NOT persist yet — user must call /enable with a valid code.
func TOTPSetup(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	if user == nil {
		jsonErr(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      brand.Default.TOTPIssuer,
		AccountName: user.Username,
	})
	if err != nil {
		jsonErr(w, "failed to generate TOTP key", http.StatusInternalServerError)
		return
	}

	// Render QR code as base64 PNG
	img, err := key.Image(200, 200)
	if err != nil {
		jsonErr(w, "failed to render QR", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		jsonErr(w, "failed to encode QR", http.StatusInternalServerError)
		return
	}
	qrB64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	// Store secret temporarily in users.totp_secret (not yet enabled)
	if _, err := db.DB.Exec(
		"UPDATE users SET totp_secret=?, totp_enabled=0 WHERE id=?",
		key.Secret(), user.ID,
	); err != nil {
		jsonErr(w, "failed to store secret", http.StatusInternalServerError)
		return
	}

	jsonOKMap(w, map[string]string{
		"secret":        key.Secret(),
		"qr_png_base64": qrB64,
		"issuer":        brand.Default.TOTPIssuer,
		"account":       user.Username,
	})
}

// ── Enable: verify code and activate 2FA ─────────────────────────────────────

// POST /api/auth/totp/enable  { "code": "123456" }
func TOTPEnable(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	if user == nil {
		jsonErr(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		jsonErr(w, "code required", http.StatusBadRequest)
		return
	}

	var secret string
	if err := db.DB.QueryRow(
		"SELECT totp_secret FROM users WHERE id=?", user.ID,
	).Scan(&secret); err != nil || secret == "" {
		jsonErr(w, "no pending TOTP setup — call /setup first", http.StatusBadRequest)
		return
	}

	if !totp.Validate(body.Code, secret) {
		jsonErr(w, "invalid code", http.StatusUnauthorized)
		return
	}

	if _, err := db.DB.Exec(
		"UPDATE users SET totp_enabled=1 WHERE id=?", user.ID,
	); err != nil {
		jsonErr(w, "failed to enable 2FA", http.StatusInternalServerError)
		return
	}

	jsonOKMap(w, map[string]bool{"enabled": true})
}

// ── Disable: verify password and deactivate 2FA ───────────────────────────────

// POST /api/auth/totp/disable  { "password": "..." }
func TOTPDisable(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	if user == nil {
		jsonErr(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		jsonErr(w, "password required", http.StatusBadRequest)
		return
	}

	var hash string
	if err := db.DB.QueryRow(
		"SELECT password FROM users WHERE id=?", user.ID,
	).Scan(&hash); err != nil {
		jsonErr(w, "user not found", http.StatusNotFound)
		return
	}
	if !CheckPassword(hash, body.Password) {
		jsonErr(w, "invalid password", http.StatusUnauthorized)
		return
	}

	if _, err := db.DB.Exec(
		"UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?", user.ID,
	); err != nil {
		jsonErr(w, "failed to disable 2FA", http.StatusInternalServerError)
		return
	}

	jsonOKMap(w, map[string]bool{"disabled": true})
}

// ── Status: is 2FA enabled for current user ───────────────────────────────────

// GET /api/auth/totp/status
func TOTPStatus(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	if user == nil {
		jsonErr(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var enabled int
	if err := db.DB.QueryRow("SELECT totp_enabled FROM users WHERE id=?", user.ID).Scan(&enabled); err != nil {
		jsonErr(w, "failed to query TOTP status", http.StatusInternalServerError)
		return
	}
	// sso_session (PAI-742): the SPA's local-2FA nag stays quiet for
	// SSO-minted sessions — the second factor is the IdP's policy there.
	jsonOKMap(w, map[string]bool{"enabled": enabled == 1, "sso_session": IsViaOIDC(r.Context())})
}

// ── Login step 2: verify OTP code ─────────────────────────────────────────────

// POST /api/auth/totp/verify  { "totp_token": "...", "code": "123456" }
// Validates the pending token + TOTP code, then creates the real session.
func TOTPVerify(w http.ResponseWriter, r *http.Request) {
	// Pending challenges come only from password login, including challenges
	// issued before the operator disabled it. OIDC never uses this endpoint.
	if !PasswordLoginEnabled() {
		jsonErr(w, "invalid or expired token — please log in again", http.StatusUnauthorized)
		return
	}
	var body struct {
		TOTPToken string `json:"totp_token"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TOTPToken == "" || body.Code == "" {
		jsonErr(w, "totp_token and code required", http.StatusBadRequest)
		return
	}
	if allowed, retryAfter := allowAuthAttempt("totp-verify", r, body.TOTPToken); !allowed {
		log.Printf("auth: totp verify throttled ip=%q token_prefix=%q retry_after=%s", clientIP(r), truncateToken(body.TOTPToken), retryAfter.Round(time.Second))
		setRetryAfter(w, retryAfter)
		jsonErr(w, "too many verification attempts", http.StatusTooManyRequests)
		return
	}

	// Look up pending token
	var userID int64
	err := db.DB.QueryRow(`
		SELECT user_id FROM totp_pending
		WHERE token=? AND expires_at > datetime('now')
	`, body.TOTPToken).Scan(&userID)
	if err != nil {
		recordAuthFailure("totp-verify", r, body.TOTPToken)
		jsonErr(w, "invalid or expired token — please log in again", http.StatusUnauthorized)
		return
	}

	// Get user + secret
	var secret string
	var totpUser models.User
	dests := append([]any{&secret}, userScanDests(&totpUser)...)
	if err := db.DB.QueryRow(
		"SELECT totp_secret, "+userSelectCols+" FROM users u WHERE u.id=?", userID,
	).Scan(dests...); err != nil {
		recordAuthFailure("totp-verify", r, body.TOTPToken)
		jsonErr(w, "user not found", http.StatusUnauthorized)
		return
	}
	if totpUser.Status != "active" {
		recordAuthFailure("totp-verify", r, body.TOTPToken)
		jsonErr(w, "invalid or expired token — please log in again", http.StatusUnauthorized)
		return
	}

	if !totp.Validate(body.Code, secret) {
		recordAuthFailure("totp-verify", r, body.TOTPToken)
		jsonErr(w, "invalid code", http.StatusUnauthorized)
		return
	}
	resetAuthFailures("totp-verify", r, body.TOTPToken)

	// Clean up pending token
	if _, err := db.DB.Exec("DELETE FROM totp_pending WHERE token=?", body.TOTPToken); err != nil {
		log.Printf("TOTPVerify: delete pending token: %v", err)
	}

	// Create real session
	now := time.Now()
	expiresAt := now.Add(sessionDuration)
	sid, err := createSession(r.Context(), userID, now, expiresAt, false, false)
	if err != nil {
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	setSessionCookieValue(w, sid, expiresAt)
	// PAI-113: bind a fresh CSRF token to the new session.
	if _, err := IssueCSRFForSession(w, sid); err != nil {
		log.Printf("TOTPVerify: issue csrf token: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(MeResponse{
		User:                 &totpUser,
		Access:               BuildAccessResponse(&totpUser),
		SuppressSecurityNags: SuppressSecurityNags(&totpUser),
	})
}
