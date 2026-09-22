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

// PAI-120 / PAI-680: enterprise SSO via OpenID Connect — single provider,
// end-to-end, invite-only by default with optional JIT user provisioning.
// The implementation deliberately uses only the standard library and the
// existing session machinery to keep the dependency surface minimal:
//
//   - Discovery: GET ${OIDC_ISSUER_URL}/.well-known/openid-configuration
//   - Authorisation: redirect with state + PKCE (S256), nonce, scope.
//   - Token exchange: POST to token_endpoint with the code + verifier.
//   - User info: GET userinfo_endpoint with the access token.
//
// We rely on the userinfo round trip rather than verifying the id_token
// signature ourselves. Because every step happens over TLS direct to
// the issuer, the trust boundary is the same; skipping JWKS handling
// trims ~300 lines of crypto code that would have to be audited
// independently. This trade-off is documented and noted in the README.
//
// Configuration is via env vars; if any required var is unset the OIDC
// routes return 503 so an operator who has not configured SSO does not
// see a confusing error page.

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/inspr-at/paimos/backend/publicbase"
	"github.com/inspr-at/paimos/backend/secretinput"
)

// oidcConfig captures the env-driven configuration for the OIDC flow.
// Loaded lazily on the first request so an operator who flips the env
// vars at runtime sees the change without a restart.
type oidcConfig struct {
	IssuerURL      string
	ClientID       string
	ClientSecret   string
	RedirectURL    string
	Scopes         string
	Prompt         string
	ProvisionMode  string
	AutoCreateRole string

	// Discovery results — filled from .well-known/openid-configuration.
	AuthorizationEndpoint string
	TokenEndpoint         string
	UserinfoEndpoint      string

	loaded bool
}

var (
	oidcCfg     oidcConfig
	oidcCfgOnce sync.Mutex
)

const (
	oidcProvisionInviteOnly = "invite-only"
	oidcProvisionAutoCreate = "auto-create"
)

// loadOIDCConfig hydrates oidcCfg from env + discovery. Idempotent;
// returns an error when any required env var is missing or when
// discovery fails.
func loadOIDCConfig(ctx context.Context) (oidcConfig, error) {
	oidcCfgOnce.Lock()
	defer oidcCfgOnce.Unlock()
	cfg, err := oidcConfigFromEnv()
	if err != nil {
		return cfg, err
	}
	if oidcCfg.loaded && oidcCfg.sameConfigInput(cfg) {
		return oidcCfg, nil
	}
	doc, err := fetchDiscovery(ctx, cfg.IssuerURL)
	if err != nil {
		return cfg, fmt.Errorf("oidc discovery: %w", err)
	}
	cfg.AuthorizationEndpoint = doc.AuthorizationEndpoint
	cfg.TokenEndpoint = doc.TokenEndpoint
	cfg.UserinfoEndpoint = doc.UserinfoEndpoint
	cfg.loaded = true
	oidcCfg = cfg
	return cfg, nil
}

func oidcConfigFromEnv() (oidcConfig, error) {
	clientSecret, err := secretinput.Optional("OIDC_CLIENT_SECRET")
	if err != nil {
		return oidcConfig{}, err
	}

	cfg := oidcConfig{
		IssuerURL:      strings.TrimRight(os.Getenv("OIDC_ISSUER_URL"), "/"),
		ClientID:       strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID")),
		ClientSecret:   clientSecret,
		RedirectURL:    strings.TrimSpace(os.Getenv("OIDC_REDIRECT_URL")),
		Scopes:         envDefault("OIDC_SCOPES", "openid email profile"),
		Prompt:         strings.TrimSpace(os.Getenv("OIDC_PROMPT")),
		ProvisionMode:  strings.ToLower(strings.TrimSpace(envDefault("OIDC_PROVISION_MODE", oidcProvisionInviteOnly))),
		AutoCreateRole: strings.ToLower(strings.TrimSpace(envDefault("OIDC_AUTO_CREATE_ROLE", "member"))),
	}
	if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return cfg, errors.New("OIDC not configured")
	}
	if cfg.ProvisionMode != oidcProvisionInviteOnly && cfg.ProvisionMode != oidcProvisionAutoCreate {
		return cfg, fmt.Errorf("OIDC_PROVISION_MODE must be %q or %q", oidcProvisionInviteOnly, oidcProvisionAutoCreate)
	}
	if cfg.AutoCreateRole != "member" && cfg.AutoCreateRole != "external" {
		return cfg, errors.New("OIDC_AUTO_CREATE_ROLE must be member or external")
	}
	if err := validateOIDCRedirectURL(cfg.RedirectURL); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func validateOIDCRedirectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("OIDC_REDIRECT_URL must be an absolute URL with scheme and host")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("OIDC_REDIRECT_URL must be an http(s) URL")
	}
	want := publicbase.Current().Join("/api/auth/oidc/callback")
	if u.EscapedPath() != want && u.Path != want {
		return fmt.Errorf("OIDC_REDIRECT_URL path must be exactly %s", want)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("OIDC_REDIRECT_URL must not include query or fragment")
	}
	return nil
}

func (c oidcConfig) sameConfigInput(other oidcConfig) bool {
	return c.IssuerURL == other.IssuerURL &&
		c.ClientID == other.ClientID &&
		c.ClientSecret == other.ClientSecret &&
		c.RedirectURL == other.RedirectURL &&
		c.Scopes == other.Scopes &&
		c.Prompt == other.Prompt &&
		c.ProvisionMode == other.ProvisionMode &&
		c.AutoCreateRole == other.AutoCreateRole
}

// envDefault returns the env value or fallback when unset.
func envDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

type oidcDiscoveryDoc struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

func fetchDiscovery(ctx context.Context, issuer string) (oidcDiscoveryDoc, error) {
	var doc oidcDiscoveryDoc
	url := issuer + "/.well-known/openid-configuration"
	// #nosec G704 -- issuer is the operator-configured OIDC_ISSUER_URL; discovery against it is the SSO feature.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return doc, err
	}
	resp, err := httpClient.Do(req) // #nosec G704 -- request targets the operator-configured OIDC issuer.
	if err != nil {
		return doc, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return doc, fmt.Errorf("discovery status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&doc); err != nil {
		return doc, err
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.UserinfoEndpoint == "" {
		return doc, errors.New("discovery missing required endpoints")
	}
	return doc, nil
}

// httpClient is shared so we don't spin up a fresh transport per request.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// ── Cookies (OIDC handshake state) ──────────────────────────────────

// setShortCookie writes a SameSite=Lax HttpOnly cookie with a 10-minute
// lifetime. Used for the state/PKCE/nonce values that survive only the
// authorisation redirect.
func setShortCookie(w http.ResponseWriter, canonical, legacy, value string) {
	// #nosec G124 -- HttpOnly + SameSite=Lax are set; Secure mirrors COOKIE_SECURE (true on HTTPS deployments).
	base := http.Cookie{
		Value:    value,
		Path:     "/",
		Expires:  time.Now().Add(10 * time.Minute),
		HttpOnly: true,
		Secure:   cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
	for _, name := range cookieWriteNames(canonical, legacy) {
		setHTTPCookie(w, name, base)
	}
}

func clearOIDCCookies(w http.ResponseWriter) {
	expireNamedCookies(w, oidcStateCookie, oidcVerifCookie, oidcNonceCookie, oidcReturnCookie)
	if !sharedOriginCookies() {
		expireNamedCookies(w, legacyOIDCStateCookie, legacyOIDCVerifCookie, legacyOIDCNonceCookie, legacyOIDCReturnCookie)
	}
}

// ── Handlers ─────────────────────────────────────────────────────────

// OIDCStatus — GET /api/auth/oidc/status
//
// Public. Returns whether OIDC is configured so the SPA can decide
// whether to render an "SSO" button next to the password form.
func OIDCStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadOIDCConfig(r.Context())
	enabled := err == nil && cfg.AuthorizationEndpoint != ""
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{
		"enabled": enabled,
		// label lets operators rebrand the SSO button without a code
		// change (e.g. "Sign in with Acme SSO").
		"label": envDefault("OIDC_BUTTON_LABEL", "Sign in with SSO"),
	}
	// Preserve the default response bytes for existing deployments.
	if !PasswordLoginEnabled() {
		response["password_disabled"] = true
	}
	_ = json.NewEncoder(w).Encode(response)
}

// OIDCLogin — GET /api/auth/oidc/login
//
// Generates state + PKCE verifier + nonce, stores them in short-lived
// cookies, and 302s the browser to the IdP authorise endpoint. Public.
func OIDCLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadOIDCConfig(r.Context())
	if err != nil {
		http.Error(w, `{"error":"OIDC not configured"}`, http.StatusServiceUnavailable)
		return
	}

	state := mustRandom(16)
	verifier := mustRandom(32)
	challenge := pkceChallengeS256(verifier)
	nonce := mustRandom(16)

	setShortCookie(w, oidcStateCookie, legacyOIDCStateCookie, state)
	setShortCookie(w, oidcVerifCookie, legacyOIDCVerifCookie, verifier)
	setShortCookie(w, oidcNonceCookie, legacyOIDCNonceCookie, nonce)
	if ret := safeOIDCReturnPath(r.URL.Query().Get("redirect")); ret != "" {
		setShortCookie(w, oidcReturnCookie, legacyOIDCReturnCookie, ret)
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURL)
	q.Set("scope", cfg.Scopes)
	if cfg.Prompt != "" {
		q.Set("prompt", cfg.Prompt)
	}
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	// PAI-743: the identifier-first login already asked who the user
	// is — forward it so the IdP can skip its own username prompt.
	// Advisory per OIDC core §3.1.2.1: the IdP may ignore it, and it
	// never substitutes for authentication.
	if hint := strings.TrimSpace(r.URL.Query().Get("login_hint")); hint != "" && len(hint) <= 320 {
		q.Set("login_hint", hint)
	}

	target := cfg.AuthorizationEndpoint + "?" + q.Encode()
	http.Redirect(w, r, target, http.StatusFound)
}

// OIDCCallback — GET /api/auth/oidc/callback
//
// Validates state, exchanges code for tokens, fetches userinfo, finds
// or creates the local user, issues a session cookie + CSRF token, then
// 302s back to the SPA root. On failure, redirects to /login with an
// `?sso_error=…` query string the SPA can render.
func OIDCCallback(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadOIDCConfig(r.Context())
	if err != nil {
		ssoError(w, r, "not_configured")
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		ssoError(w, r, e)
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		ssoError(w, r, "missing_params")
		return
	}
	stateCookie, err := readOIDCCookie(r, oidcStateCookie, legacyOIDCStateCookie)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != state {
		ssoError(w, r, "bad_state")
		return
	}
	verifierCookie, err := readOIDCCookie(r, oidcVerifCookie, legacyOIDCVerifCookie)
	if err != nil || verifierCookie.Value == "" {
		ssoError(w, r, "missing_verifier")
		return
	}
	returnPath := ""
	if retCookie, err := readOIDCCookie(r, oidcReturnCookie, legacyOIDCReturnCookie); err == nil {
		returnPath = safeOIDCReturnPath(retCookie.Value)
	}
	// One-shot — clear regardless of outcome below.
	clearOIDCCookies(w)

	tok, err := exchangeCode(r.Context(), cfg, code, verifierCookie.Value)
	if err != nil {
		log.Printf("oidc: token exchange: %v", err)
		ssoError(w, r, "exchange_failed")
		return
	}
	info, err := fetchUserinfo(r.Context(), cfg, tok.AccessToken)
	if err != nil {
		log.Printf("oidc: userinfo: %v", err)
		ssoError(w, r, "userinfo_failed")
		return
	}
	if info.Email == "" || info.EmailVerified == nil || !*info.EmailVerified {
		log.Printf("oidc: refusing unverified or missing email: sub=%q", info.Sub)
		ssoError(w, r, "email_required")
		return
	}

	user, err := provisionOIDCUser(info, cfg)
	if err != nil {
		log.Printf("oidc: provision: %v", err)
		switch {
		case errors.Is(err, errOIDCInviteRequired):
			ssoError(w, r, "invite_required")
		case errors.Is(err, errOIDCAccountDisabled):
			ssoError(w, r, "account_disabled")
		default:
			ssoError(w, r, "provision_failed")
		}
		return
	}

	// Mint a session + CSRF token using the same surface as password login.
	now := time.Now()
	expiresAt := now.Add(sessionDuration)
	// via_oidc marks the session as SSO-minted (PAI-742): the local-2FA
	// nag stays quiet because the second factor is the IdP's policy.
	sid, err := createSession(r.Context(), user.ID, now, expiresAt, false, true)
	if err != nil {
		ssoError(w, r, "session_failed")
		return
	}
	setSessionCookieValue(w, sid, now.Add(sessionAbsoluteLifetime))
	if _, err := IssueCSRFForSession(w, sid); err != nil {
		log.Printf("oidc: issue csrf: %v", err)
	}
	if _, err := db.DB.Exec("UPDATE users SET last_login_at=datetime('now') WHERE id=?", user.ID); err != nil {
		log.Printf("oidc: update last_login_at: %v", err)
	}
	log.Printf("audit: oidc_login_ok user_id=%d email=%q ip=%s", user.ID, info.Email, ClientIP(r))

	// Final hop — back to the SPA. Use the operator-configured public
	// URL when available so a redirect never lands on the literal IdP
	// referer. Join the native prefix unless the operator already supplied
	// a fully-qualified URL.
	dest := browserPath(envDefault("OIDC_POST_LOGIN_REDIRECT", "/"))
	if returnPath != "" {
		dest = returnPath
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// ── helpers ─────────────────────────────────────────────────────────

type oidcTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func exchangeCode(ctx context.Context, cfg oidcConfig, code, verifier string) (*oidcTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", cfg.RedirectURL)
	form.Set("client_id", cfg.ClientID)
	form.Set("code_verifier", verifier)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var out oidcTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}
	return &out, nil
}

type oidcUserinfo struct {
	Sub               string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     *bool  `json:"email_verified,omitempty"`
	Name              string `json:"name"`
	GivenName         string `json:"given_name"`
	FamilyName        string `json:"family_name"`
	PreferredUsername string `json:"preferred_username"`
}

func fetchUserinfo(ctx context.Context, cfg oidcConfig, accessToken string) (*oidcUserinfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	var info oidcUserinfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

var (
	errOIDCInviteRequired  = errors.New("oidc user not invited")
	errOIDCAccountDisabled = errors.New("oidc user account disabled")
)

// provisionOIDCUser finds an existing PAIMOS user by verified email
// (case-insensitive). Unknown users are refused by default; operators can opt
// into auto-create via OIDC_PROVISION_MODE=auto-create.
func provisionOIDCUser(info *oidcUserinfo, cfg oidcConfig) (*models.User, error) {
	email := strings.ToLower(strings.TrimSpace(info.Email))
	var id int64
	var status string
	var matches int
	if err := db.DB.QueryRow("SELECT COUNT(*),COALESCE(MIN(id),0),COALESCE(MIN(status),'') FROM users WHERE lower(trim(email))=?", email).Scan(&matches, &id, &status); err != nil {
		return nil, err
	}
	if matches > 1 {
		return nil, errors.New("ambiguous OIDC email identity")
	}
	if matches == 1 {
		if status != "active" {
			return nil, fmt.Errorf("%w: user_id=%d status=%s", errOIDCAccountDisabled, id, status)
		}
		// Verified SSO identity does not require the invite's local password.
		// Fail the login if clearing the flag fails rather than minting a
		// session that is permanently trapped by MustChangePasswordGate.
		if _, err := db.DB.Exec("UPDATE users SET must_change_password=0 WHERE id=?", id); err != nil {
			return nil, fmt.Errorf("clear OIDC password rotation requirement: %w", err)
		}
		u := &models.User{}
		if err := db.DB.QueryRow(
			"SELECT "+userSelectCols+" FROM users u WHERE u.id=?", id,
		).Scan(userScanDests(u)...); err != nil {
			return nil, err
		}
		return u, nil
	}

	if cfg.ProvisionMode != oidcProvisionAutoCreate {
		return nil, fmt.Errorf("%w: email=%q", errOIDCInviteRequired, email)
	}

	role := cfg.AutoCreateRole
	if role != "member" && role != "external" {
		return nil, errors.New("invalid OIDC auto-create role")
	}

	// New user — JIT provision with the configured low-privilege role.
	// Username defaults to the OIDC `preferred_username`, falling back to the
	// email local-part. Any future username collision gets a random suffix to
	// keep INSERT atomic.
	username := strings.ToLower(strings.TrimSpace(info.PreferredUsername))
	if username == "" {
		if at := strings.Index(email, "@"); at > 0 {
			username = email[:at]
		} else {
			username = "sso-user"
		}
	}
	username = sanitiseUsername(username)
	if username == "" {
		username = "sso-user"
	}
	// Try the preferred name first; fall back to a random suffix on UNIQUE
	// violation. Try only twice — if a name is that contended, surfacing
	// the error is the right call.
	res, err := db.DB.Exec(`
		INSERT INTO users(username, password, role, role_key, status, email, first_name, last_name, must_change_password)
		VALUES(?, '', ?, ?, 'active', ?, ?, ?, 0)
	`, username, role, role, email, info.GivenName, info.FamilyName)
	if err != nil {
		username = username + "-" + mustRandom(4)
		res, err = db.DB.Exec(`
			INSERT INTO users(username, password, role, role_key, status, email, first_name, last_name, must_change_password)
			VALUES(?, '', ?, ?, 'active', ?, ?, ?, 0)
		`, username, role, role, email, info.GivenName, info.FamilyName)
		if err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
	}
	uid, _ := res.LastInsertId()
	SeedAccessForUser(uid, role)

	u := &models.User{}
	if err := db.DB.QueryRow(
		"SELECT "+userSelectCols+" FROM users u WHERE u.id=?", uid,
	).Scan(userScanDests(u)...); err != nil {
		return nil, err
	}
	log.Printf("audit: oidc_user_provisioned user_id=%d email=%q username=%q", uid, email, username)
	return u, nil
}

// sanitiseUsername strips characters PAIMOS would refuse on a manual user
// create. Keeps the result ASCII alphanumeric plus -_. .
func sanitiseUsername(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func ssoError(w http.ResponseWriter, r *http.Request, code string) {
	clearOIDCCookies(w)
	dest := browserPath("/login") + "?sso_error=" + url.QueryEscape(code)
	http.Redirect(w, r, dest, http.StatusFound)
}

// pkceChallengeS256 returns the base64url-no-pad sha256 of verifier — the
// canonical S256 challenge.
func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// mustRandom returns a hex string from n bytes of crypto/rand. Panics on
// rand.Read failure — the system is fundamentally broken at that point.
func mustRandom(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}
