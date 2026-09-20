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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"golang.org/x/crypto/bcrypt"
)

// cookieSecure mirrors the COOKIE_SECURE env var.
// Set COOKIE_SECURE=true on live (HTTPS); leave unset for staging/local (HTTP).
var cookieSecure = os.Getenv("COOKIE_SECURE") == "true"

// sessionRandomReader is a narrow fail-closed seam for session identity
// creation tests. Production always uses the operating system CSPRNG.
var sessionRandomReader io.Reader = rand.Reader

var errInvalidSession = errors.New("invalid session")

const totpPendingTTLAuth = 5 * time.Minute

// PAI-322: 30-day sliding window with a 90-day absolute cap.
//
//   - sessionDuration is the sliding window. Every authenticated request
//     that finds the remaining TTL below half of this value bumps
//     expires_at back to now+sessionDuration. So an active user never
//     gets logged out; an inactive user is logged out 30 days after
//     their last request.
//
//   - sessionAbsoluteLifetime is a hard ceiling measured from the row's
//     created_at. Even a perpetually-active session is forced to
//     re-login once it crosses this. Catches stolen-cookie risk and
//     keeps a clean session-table when users churn devices.
//
//   - sessionRenewThreshold is the "below half" decision: don't UPDATE
//     on every request — only when it earns us at least half a window.
//     For 30d sliding, that means we write at most every ~15 days per
//     session under normal use.
//
// The cookie's Expires attribute is set to sessionAbsoluteLifetime so
// the browser keeps the cookie alive for the full possible lifetime
// of the row; the DB row is the source of truth and renewal re-issues
// the cookie alongside the DB UPDATE.
const sessionDuration = 30 * 24 * time.Hour
const sessionAbsoluteLifetime = 90 * 24 * time.Hour
const sessionRenewThreshold = sessionDuration / 2

const authRateLimitWindow = 10 * time.Minute
const authRateLimitMaxAttempts = 5

// MinPasswordLen is the single source of truth for the minimum length of
// any password a user or admin sets. Every flow that writes a password —
// self-service reset, self-change, admin create, admin reset — enforces
// this same constant so the policy can't silently drift between them.
const MinPasswordLen = 8

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// CurrentSessionID returns the session id carried by the request's session
// cookie, or "" if there is none (e.g. API-key auth). Lets handlers outside
// this package preserve "the current session" when pruning sessions on a
// password change without duplicating the cookie name.
func CurrentSessionID(r *http.Request) string {
	if c, err := readSessionCookie(r); err == nil {
		return c.Value
	}
	return ""
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(sessionRandomReader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newCredentialID() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(sessionRandomReader, b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // UUID version 4.
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant.
	encoded := hex.EncodeToString(b)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32], nil
}

// createSession is the only production path that mints a browser session.
// The bearer id and the safe durable credential id are independent random
// values; callers receive only the bearer id for the cookie.
func createSession(ctx context.Context, userID int64, createdAt, expiresAt time.Time, viaDevLogin, viaOIDC bool) (string, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return "", err
	}
	credentialID, err := newCredentialID()
	if err != nil {
		return "", err
	}
	result, err := db.DB.ExecContext(ctx, `
		INSERT INTO sessions(
		 id,user_id,expires_at,created_at,via_dev_login,via_oidc,credential_id
		)
		SELECT ?,u.id,?,?,?,?,? FROM users u WHERE u.id=? AND u.status='active'
	`, sessionID,
		expiresAt.UTC().Format("2006-01-02 15:04:05"),
		createdAt.UTC().Format("2006-01-02 15:04:05"),
		boolToInt(viaDevLogin), boolToInt(viaOIDC), credentialID, userID)
	if err != nil {
		return "", err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return "", fmt.Errorf("session principal is unavailable")
	}
	return sessionID, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// Middleware — attaches *models.User to context if session valid.
type contextKey string

const UserKey contextKey = "user"

// devLoginKey is the context flag set by Middleware when the request's
// session was created via dev-login (PAI-267). MeHandler reads it to
// surface `via_dev_login: true` to the frontend banner.
type devLoginKeyType struct{}

var devLoginKey = devLoginKeyType{}

func withDevLoginFlag(ctx context.Context, v bool) context.Context {
	return context.WithValue(ctx, devLoginKey, v)
}

// IsViaDevLogin reports whether the current request authenticated via
// the dev-login route. Returns false for API-key auth, normal session
// auth, or unauthenticated requests.
func IsViaDevLogin(ctx context.Context) bool {
	v, _ := ctx.Value(devLoginKey).(bool)
	return v
}

// oidcKey is the context flag set by Middleware when the request's
// session was minted by the OIDC callback (PAI-742). TOTPStatus reads
// it so the local-2FA nag stays quiet for SSO sessions.
type oidcKeyType struct{}

var oidcKey = oidcKeyType{}

func withOIDCFlag(ctx context.Context, v bool) context.Context {
	return context.WithValue(ctx, oidcKey, v)
}

// IsViaOIDC reports whether the current request rides an SSO-minted
// session. False for API-key auth, password sessions, dev-login, or
// unauthenticated requests.
func IsViaOIDC(ctx context.Context) bool {
	v, _ := ctx.Value(oidcKey).(bool)
	return v
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Try API key: Authorization: Bearer <BRAND_API_KEY_PREFIX>...
		if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer "+brand.Default.APIKeyPrefix) {
			rawKey := strings.TrimPrefix(hdr, "Bearer ")
			user, principal, err := ResolveAPIKeyPrincipal(rawKey)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if principal.Kind() == PrincipalMachineNotifier && !machineNotifierRouteAllowed(r) {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			if principal.Kind() == PrincipalConversationService && !conversationServiceRouteAllowed(r) {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			// PAI-320: API-key callers also get the epoch so any
			// frontend that talks via a key picks up role / membership
			// changes on the next request.
			w.Header().Set("X-Permissions-Epoch", strconv.FormatInt(GetPermissionsEpoch(user.ID), 10))
			ctx := context.WithValue(r.Context(), UserKey, user)
			ctx = WithAccessCache(ctx)
			// PAI-379: api-key scopes ride along on the context so
			// RequireScope can narrow specific routes without a second
			// DB query. Pre-M104 keys backfill to `*` (= ScopeAll) so
			// existing callers behave unchanged.
			ctx = WithScopes(ctx, principal.Scopes())
			ctx = WithPrincipal(ctx, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// 2. Fall back to session cookie
		cookie, err := readSessionCookie(r)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		rec, err := loadSession(cookie.Value)
		if err != nil {
			if errors.Is(err, errInvalidSession) {
				clearSessionCookie(w)
			}
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// PAI-322: enforce the 90-day absolute cap. created_at can be
		// zero for sessions that pre-date the M89 migration in unusual
		// edge cases (the migration UPDATEs existing rows, but a zero
		// value is still defensive); skip the cap check in that case
		// rather than fail a legitimate session.
		now := time.Now().UTC()
		if !rec.createdAt.IsZero() && (now.Before(rec.createdAt) || !now.Before(rec.createdAt.Add(sessionAbsoluteLifetime))) {
			if _, derr := db.DB.Exec("DELETE FROM sessions WHERE id=?", cookie.Value); derr != nil {
				log.Printf("Middleware: delete capped session credential_id=%s: %v", rec.credentialID, derr)
			}
			clearSessionCookie(w)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// PAI-322: sliding renewal — bump expires_at if remaining TTL
		// is below half. Throttling the write to "remaining < half"
		// means an active user triggers an UPDATE at most every
		// sessionRenewThreshold (≈15 days for the 30d window) per
		// session. The new expiry is also clamped to the absolute cap
		// so we never slide past it.
		remaining := time.Until(rec.expiresAt)
		if remaining < sessionRenewThreshold {
			newExpiry := time.Now().Add(sessionDuration)
			if !rec.createdAt.IsZero() {
				absoluteEnd := rec.createdAt.Add(sessionAbsoluteLifetime)
				if newExpiry.After(absoluteEnd) {
					newExpiry = absoluteEnd
				}
			}
			if _, uerr := db.DB.Exec(
				"UPDATE sessions SET expires_at = ? WHERE id = ?",
				newExpiry.UTC().Format("2006-01-02 15:04:05"), cookie.Value,
			); uerr != nil {
				// A renewal failure is recoverable — log it and let the
				// request proceed; the next request retries the slide.
				log.Printf("Middleware: slide renewal credential_id=%s: %v", rec.credentialID, uerr)
			} else {
				rec.expiresAt = newExpiry
				setSessionCookieValue(w, cookie.Value, time.Now().Add(sessionAbsoluteLifetime))
			}
		}

		// PAI-322: surface the session's current expiry to the SPA so
		// it can show a low-key "expires in N minutes" toast as the
		// absolute cap approaches. Sliding sessions almost never reach
		// this — by design, the toast is rare.
		w.Header().Set("X-Session-Expires-At", rec.expiresAt.UTC().Format(time.RFC3339))

		// PAI-320: surface the per-user permissions epoch so the SPA
		// re-fetches /auth/me when role / status / membership has
		// changed on another tab or via an admin action. Backend
		// permission checks already see fresh values per request; this
		// header exists to invalidate the SPA's local access cache.
		w.Header().Set("X-Permissions-Epoch", strconv.FormatInt(rec.permissionsEpoch, 10))

		// PAI-113: lazy-upgrade pre-M72 sessions that have no token yet.
		// The first authenticated request after deployment issues one and
		// sets the cookie. Avoids forcing every existing session to log in
		// again at deploy time; new sessions get a token at login.
		if rec.csrfTok == "" {
			if t, err := IssueCSRFForSession(w, cookie.Value); err == nil {
				rec.csrfTok = t
			}
		} else if _, err := readCSRFCookie(r); err == http.ErrNoCookie {
			// PAI-370: DB has a token but the browser doesn't carry one.
			// Pre-fix the CSRF cookie was browser-session-only while the
			// session cookie persisted 90 days, so any browser restart
			// stranded the user with 403s on every POST. Re-set the
			// cookie with the EXISTING DB token (don't rotate — that
			// would invalidate in-flight requests on other tabs).
			SetCSRFCookie(w, rec.csrfTok)
		}
		ctx := context.WithValue(r.Context(), UserKey, rec.user)
		ctx = WithAccessCache(ctx)
		ctx = withSessionAuth(ctx, rec.csrfTok)
		ctx = withDevLoginFlag(ctx, rec.viaDevLogin)
		ctx = withOIDCFlag(ctx, rec.viaOIDC)
		ctx = withSessionID(ctx, rec.sessionID)
		ctx = withImpersonation(ctx, rec.actor, rec.user, rec.impersonating)
		// PAI-379: browser sessions are never narrowed; attach the
		// all-scopes set so RequireScope is a uniform downstream check
		// regardless of auth method.
		sessionScopes := ScopeSet{ScopeAll: {}}
		ctx = WithScopes(ctx, sessionScopes)
		principal, err := NewSessionPrincipal(rec.credentialID, rec.actor.ID, rec.user.ID, rec.impersonating)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		ctx = WithPrincipal(ctx, principal)
		req := r.WithContext(ctx)
		if rec.impersonating {
			rw := &impersonationAuditResponseWriter{ResponseWriter: w}
			next.ServeHTTP(rw, req)
			recordImpersonatedActionAudit(req, rec, rw.status, requestIDForImpersonationAudit(req, rw.Header()))
			return
		}
		next.ServeHTTP(w, req)
	})
}

func machineNotifierRouteAllowed(r *http.Request) bool {
	if r == nil {
		return false
	}
	const messages = "/api/machine-notifier/messages"
	if r.Method == http.MethodPost && r.URL.Path == messages {
		return true
	}
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, messages+"/") {
		return false
	}
	rest := strings.TrimPrefix(r.URL.Path, messages+"/")
	parts := strings.Split(rest, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] == "receipt"
}

func conversationServiceRouteAllowed(r *http.Request) bool {
	if r == nil {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 || parts[0] != "api" || parts[1] != "projects" || parts[2] == "" ||
		parts[3] != "conversation" || parts[4] != "v1" || parts[5] != "calls" {
		return false
	}
	switch len(parts) {
	case 6:
		return r.Method == http.MethodPost
	case 7:
		return parts[6] != "" && r.Method == http.MethodGet
	case 8:
		return parts[6] != "" && ((parts[7] == "events" && r.Method == http.MethodGet) ||
			(parts[7] == "cancel" && r.Method == http.MethodPost))
	default:
		return false
	}
}

// clearSessionCookie writes a Set-Cookie that immediately expires the
// session cookie on the client. Used when the server kills a session
// (absolute cap, account disabled, etc.) so the browser doesn't keep
// presenting a value the server has already deleted.
func clearSessionCookie(w http.ResponseWriter) {
	clearSessionCookies(w)
}

// userScanDests returns the scan destination pointers for userSelectCols.
// Useful when the query has extra prefix/suffix columns around the user cols.
//
// IMPORTANT: this list must stay in lock-step with `userSelectCols` AND with the
// twin `handlers/userSelectCols` / `handlers/scanUser`. Adding a new user column
// requires updating BOTH packages — see Test_APIKeyAuth for the regression that
// caused this to be a recurring footgun.
func userScanDests(u *models.User) []any {
	return []any{
		&u.ID, &u.Username, &u.Role, &u.Status, &u.CreatedAt,
		&u.Nickname, &u.FirstName, &u.LastName, &u.Email, &u.AvatarPath,
		&u.MarkdownDefault, &u.MonospaceFields, &u.RecentProjectsLimit,
		&u.InternalRateHourly, &u.ShowAltUnitTable, &u.ShowAltUnitDetail, &u.Locale,
		&u.RecentTimersLimit, &u.Timezone, &u.PreviewHoverDelay,
		&u.IssueAutoRefreshEnabled, &u.IssueAutoRefreshIntervalSeconds, &u.LastLoginAt,
		&u.AccrualsStatsEnabled, &u.AccrualsExtraStatuses,
		&u.IsSuperAdmin,              // PAI-335
		&u.SearchScopeShortcut,       // PAI-368 / M103
		&u.IntakeConfidenceThreshold, // PAI-706 / M135
		&u.CommandPaletteShortcut,    // PAI-866 / M166
	}
}

// userSelectCols is the full qualified column list for the users table. Role
// reads are canonicalized through role_key; the legacy role/is_super_admin
// columns stay as compatibility shims.
const userRoleSelectExpr = `CASE
	WHEN u.is_super_admin = 1 THEN 'super_admin'
	WHEN u.role_key = 'member' AND u.role IN ('admin','external') THEN u.role
	WHEN u.role_key IN ('admin','member','external','super_admin') THEN u.role_key
	WHEN u.role IN ('admin','member','external') THEN u.role
	ELSE 'member'
END`
const userSuperAdminSelectExpr = `CASE WHEN ` + userRoleSelectExpr + ` = 'super_admin' OR u.is_super_admin = 1 THEN 1 ELSE 0 END`
const userSelectCols = `u.id, u.username, ` + userRoleSelectExpr + `, u.status, u.created_at, u.nickname, u.first_name, u.last_name, u.email, u.avatar_path, u.markdown_default, u.monospace_fields, u.recent_projects_limit, u.internal_rate_hourly, u.show_alt_unit_table, u.show_alt_unit_detail, u.locale, u.recent_timers_limit, u.timezone, u.preview_hover_delay, u.issue_auto_refresh_enabled, u.issue_auto_refresh_interval_seconds, u.last_login_at, u.accruals_stats_enabled, u.accruals_extra_statuses, ` + userSuperAdminSelectExpr + `, u.search_scope_shortcut, u.intake_confidence_threshold, u.command_palette_shortcut`

func userSelectColsFor(alias string) string {
	return strings.ReplaceAll(userSelectCols, "u.", alias+".")
}

// sessionRecord is the full row needed to make slide / cap / surface
// decisions in Middleware. Times are parsed from the SQLite TEXT
// columns; a zero time means the column was empty (pre-migration row
// or unparseable string) — callers must check .IsZero() before relying
// on the value.
type sessionRecord struct {
	user             *models.User
	actor            *models.User
	sessionID        string
	credentialID     string
	csrfTok          string
	viaDevLogin      bool
	viaOIDC          bool
	impersonating    bool
	expiresAt        time.Time
	createdAt        time.Time
	permissionsEpoch int64
}

// loadSession resolves a session id to its full record. Enforces the
// expires_at > now sliding-window check in SQL (so a long-expired
// session is invisible to the rest of the code) and disables the
// session inline if the user has been deactivated.
func loadSession(sessionID string) (*sessionRecord, error) {
	return loadSessionAt(sessionID, time.Now().UTC())
}

func loadSessionAt(sessionID string, now time.Time) (*sessionRecord, error) {
	rec := &sessionRecord{user: &models.User{}, actor: &models.User{}, sessionID: sessionID}
	var csrfTok string
	var viaDevLoginInt int
	var viaOIDCInt int
	var impersonatingInt int
	var expiresStr, createdStr string
	var epoch int64
	var credentialID string
	dests := append(
		[]any{&csrfTok, &viaDevLoginInt, &viaOIDCInt, &expiresStr, &createdStr, &credentialID, &epoch, &impersonatingInt},
		userScanDests(rec.user)...,
	)
	dests = append(dests, userScanDests(rec.actor)...)
	// #nosec G202 -- userSelectCols / userSelectColsFor are fixed column lists; the session id binds as a placeholder.
	row := db.DB.QueryRow(`
		SELECT s.csrf_token, s.via_dev_login, s.via_oidc, s.expires_at, s.created_at,
		       s.credential_id,
		       u.permissions_epoch,
		       CASE WHEN s.acting_as_user_id IS NOT NULL THEN 1 ELSE 0 END,
		       `+userSelectCols+`, `+userSelectColsFor("actor")+`
		FROM sessions s
		JOIN users actor ON actor.id = COALESCE(s.actor_user_id, s.user_id)
		JOIN users u ON u.id = COALESCE(s.acting_as_user_id, s.user_id)
		WHERE s.id = ? AND s.expires_at > datetime('now')
	`, sessionID)
	if err := row.Scan(dests...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No durable row was resolved, so there is nothing to clean up.
			// Avoid turning arbitrary garbage cookies into serialized writes;
			// Middleware still clears the client cookie for this typed error.
			return nil, fmt.Errorf("%w: missing_or_expired", errInvalidSession)
		}
		return nil, err
	}
	rec.permissionsEpoch = epoch
	rec.credentialID = credentialID
	if rec.credentialID == sessionID {
		return nil, rejectSessionRow(sessionID, "", "credential_alias")
	}
	rec.impersonating = impersonatingInt != 0
	if rec.actor.Status != "active" {
		return nil, rejectSessionRow(sessionID, rec.credentialID, "actor_inactive")
	}
	if rec.impersonating && !IsSuperAdmin(rec.actor) {
		return nil, rejectSessionRow(sessionID, rec.credentialID, "actor_demoted")
	}
	if rec.user.Status != "active" {
		return nil, rejectSessionRow(sessionID, rec.credentialID, "effective_user_inactive")
	}
	rec.csrfTok = csrfTok
	rec.viaDevLogin = viaDevLoginInt != 0
	rec.viaOIDC = viaOIDCInt != 0
	// Credential times are authorization inputs. M89 populated created_at for
	// every legacy row, so an empty or malformed value is corruption rather
	// than a compatibility case and must not bypass the absolute cap or be
	// renewed into a valid session.
	expiresAt, err := parseCredentialTimestamp(expiresStr)
	if err != nil || !expiresAt.After(now) {
		return nil, rejectSessionRow(sessionID, rec.credentialID, "invalid_expiry")
	}
	createdAt, err := parseCredentialTimestamp(createdStr)
	if err != nil || now.Before(createdAt) || now.Sub(createdAt) >= sessionAbsoluteLifetime {
		return nil, rejectSessionRow(sessionID, rec.credentialID, "invalid_creation_time")
	}
	rec.expiresAt = expiresAt
	rec.createdAt = createdAt
	return rec, nil
}

// rejectSessionRow centralizes invalid-credential cleanup and log redaction.
// The raw bearer is used only as a bound DELETE parameter and is never logged.
func rejectSessionRow(sessionID, credentialID, reason string) error {
	safeCredentialID := "unavailable"
	if credentialID != sessionID && validSessionCredentialID(credentialID) {
		safeCredentialID = credentialID
	}
	if _, err := db.DB.Exec("DELETE FROM sessions WHERE id=?", sessionID); err != nil {
		log.Printf("loadSession: delete rejected session reason=%s credential_id=%s: %v", reason, safeCredentialID, err)
	}
	return fmt.Errorf("%w: %s", errInvalidSession, reason)
}

// Login handler
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if allowed, retryAfter := allowAuthAttempt("login", r, body.Username); !allowed {
		log.Printf("audit: login_throttled username=%q ip=%s retry_after=%s", body.Username, clientIP(r), retryAfter.Round(time.Second))
		setRetryAfter(w, retryAfter)
		http.Error(w, `{"error":"too many login attempts"}`, http.StatusTooManyRequests)
		return
	}
	if !PasswordLoginEnabled() {
		recordAuthFailure("login", r, body.Username)
		log.Printf("audit: login_failed username=%q ip=%s", body.Username, clientIP(r))
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	var hash string
	var totpEnabled int
	var loginUser models.User
	dests := append([]any{&hash, &totpEnabled}, userScanDests(&loginUser)...)
	err := db.DB.QueryRow(
		"SELECT password, totp_enabled, "+userSelectCols+" FROM users u WHERE u.username=?", body.Username,
	).Scan(dests...)
	if err != nil || !CheckPassword(hash, body.Password) {
		recordAuthFailure("login", r, body.Username)
		log.Printf("audit: login_failed username=%q ip=%s", body.Username, clientIP(r))
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	if loginUser.Status != "active" {
		log.Printf("audit: login_blocked username=%q ip=%s reason=account_disabled", body.Username, clientIP(r))
		http.Error(w, `{"error":"account disabled"}`, http.StatusForbidden)
		return
	}
	resetAuthFailures("login", r, body.Username)

	// If 2FA is enabled, issue a short-lived pending token instead of a session
	if totpEnabled == 1 {
		tok, err := newSessionID()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		exp := time.Now().Add(totpPendingTTLAuth).UTC().Format("2006-01-02 15:04:05")
		if _, err := db.DB.Exec(
			"INSERT INTO totp_pending(token,user_id,expires_at) VALUES(?,?,?)", tok, loginUser.ID, exp,
		); err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"totp_required": true,
			"totp_token":    tok,
		})
		return
	}

	now := time.Now()
	expiresAt := now.Add(sessionDuration)
	// PAI-322: created_at is the anchor for the absolute cap. We write
	// it explicitly here (rather than relying on a SQL default) so the
	// row is unambiguously stamped with the login moment in app-time,
	// matching whatever the server clock says when expires_at is read.
	sid, err := createSession(r.Context(), loginUser.ID, now, expiresAt, false, false)
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	// PAI-322: cookie outlives the sliding window — expires at the
	// absolute cap so the browser keeps the cookie even when the
	// backend has slid expires_at past the current value. The DB row
	// is the source of truth; the cookie just has to survive long
	// enough for the next request to renew it.
	setSessionCookieValue(w, sid, now.Add(sessionAbsoluteLifetime))
	// PAI-113: bind a fresh CSRF token to the new session and expose it
	// to the SPA via a non-HttpOnly cookie. Do not fail the login if this
	// fails — the lazy-upgrade path in Middleware will retry.
	if _, err := IssueCSRFForSession(w, sid); err != nil {
		log.Printf("LoginHandler: issue csrf token: %v", err)
	}

	// Update last_login_at
	if _, err := db.DB.Exec("UPDATE users SET last_login_at=datetime('now') WHERE id=?", loginUser.ID); err != nil {
		log.Printf("LoginHandler: update last_login_at user_id=%d: %v", loginUser.ID, err)
	}

	log.Printf("audit: login_ok username=%q ip=%s", body.Username, clientIP(r))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(MeResponse{
		User:                 &loginUser,
		Access:               BuildAccessResponse(&loginUser),
		SuppressSecurityNags: SuppressSecurityNags(&loginUser),
	})
}

func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := readSessionCookie(r)
	if err == nil {
		if _, err := db.DB.Exec("DELETE FROM sessions WHERE id=?", cookie.Value); err != nil {
			log.Printf("LogoutHandler: delete session: %v", err)
		}
	}
	clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

// MeResponse wraps the authenticated user and their per-project access
// summary. Returned by /auth/me, /auth/login (on success), and
// /auth/totp/verify so the client can hydrate its access cache in one
// round-trip instead of issuing a separate request per project.
//
// PAI-267: ViaDevLogin is true iff the current request authenticated
// via the dev-login route. The frontend renders a non-dismissable
// red banner whenever this is set so the operator can never confuse
// a dev session for a real one. Always false on production builds
// (the dev_login_prod stub never sets the context flag).
type MeResponse struct {
	User                 *models.User           `json:"user"`
	Access               AccessResponse         `json:"access"`
	ViaDevLogin          bool                   `json:"via_dev_login,omitempty"`
	SuppressSecurityNags bool                   `json:"suppress_security_nags,omitempty"`
	Impersonation        *ImpersonationResponse `json:"impersonation,omitempty"`
}

func MeHandler(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	var impersonation *ImpersonationResponse
	if imp := GetImpersonation(r); imp.Active {
		impersonation = &ImpersonationResponse{
			Active: true,
			Actor:  imp.Actor,
			Target: imp.Target,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(MeResponse{
		User:                 user,
		Access:               BuildAccessResponse(user),
		ViaDevLogin:          IsViaDevLogin(r.Context()),
		SuppressSecurityNags: SuppressSecurityNags(user),
		Impersonation:        impersonation,
	})
}

// ChangePassword — authenticated user changes their own password.
// POST /api/auth/password  { "current_password": "...", "new_password": "..." }
func ChangePassword(w http.ResponseWriter, r *http.Request) {
	user := GetUser(r)
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if !PasswordLoginEnabled() {
		http.Error(w, `{"error":"password authentication is disabled"}`, http.StatusForbidden)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.CurrentPassword == "" || body.NewPassword == "" {
		http.Error(w, `{"error":"current_password and new_password required"}`, http.StatusBadRequest)
		return
	}
	if len(body.NewPassword) < MinPasswordLen {
		http.Error(w, fmt.Sprintf(`{"error":"new password must be at least %d characters"}`, MinPasswordLen), http.StatusBadRequest)
		return
	}

	var hash string
	if err := db.DB.QueryRow(
		"SELECT password FROM users WHERE id=?", user.ID,
	).Scan(&hash); err != nil {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	if !CheckPassword(hash, body.CurrentPassword) {
		http.Error(w, `{"error":"current password is incorrect"}`, http.StatusUnauthorized)
		return
	}

	newHash, err := HashPassword(body.NewPassword)
	if err != nil {
		http.Error(w, `{"error":"hash failed"}`, http.StatusInternalServerError)
		return
	}
	if _, err := db.DB.Exec("UPDATE users SET password=? WHERE id=?", newHash, user.ID); err != nil {
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}

	// PAI-321: clear the first-login gate. Called BEFORE the session
	// pruning below so a user who was sitting on the change-password
	// screen across multiple tabs gets unblocked everywhere on the
	// next request. (The pruning will then trim those other tabs to
	// a fresh re-login.)
	ClearMustChangePassword(user.ID)

	// PAI-322: kill every other session for this user so a stolen or
	// abandoned cookie on another device stops working immediately.
	// The current session is preserved by id-exclusion so the user
	// stays logged in here. API-key-authenticated callers don't carry
	// a session cookie — for them, all sessions are nuked, which is
	// the conservative choice (an automated client doesn't have a
	// "this one" to keep).
	currentSID := ""
	if c, err := readSessionCookie(r); err == nil {
		currentSID = c.Value
	}
	if currentSID != "" {
		if _, err := db.DB.Exec(
			"DELETE FROM sessions WHERE user_id=? AND id != ?",
			user.ID, currentSID,
		); err != nil {
			log.Printf("ChangePassword: prune sessions user_id=%d: %v", user.ID, err)
		}
	} else {
		if _, err := db.DB.Exec(
			"DELETE FROM sessions WHERE user_id=?", user.ID,
		); err != nil {
			log.Printf("ChangePassword: prune all sessions user_id=%d: %v", user.ID, err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := GetUser(r)
		if !IsAdmin(user) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BlockExternal blocks external users from internal API routes (403).
func BlockExternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := GetUser(r)
		if user != nil && user.Role == "external" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePortalAccess allows only external users and admins (for testing).
func RequirePortalAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := GetUser(r)
		if user == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if user.Role != "external" && !IsAdmin(user) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GetAccessibleProjectIDs returns the project IDs the user can at least
// view. Returns nil for admins (meaning all projects). Backed by the
// project_members table (see access.go).
//
// Deprecated: prefer AccessibleProjectIDs — kept for backwards compatibility
// with portal code.
func GetAccessibleProjectIDs(r *http.Request) []int64 {
	return AccessibleProjectIDs(r)
}

// HasProjectAccess reports whether the user can at least view projectID.
// Backed by project_members via CanViewProject.
//
// Deprecated: prefer CanViewProject / CanEditProject.
func HasProjectAccess(r *http.Request, projectID int64) bool {
	return CanViewProject(r, projectID)
}
