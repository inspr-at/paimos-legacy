// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/internal/testdb"
	"github.com/inspr-at/paimos/backend/publicbase"
)

type oidcMockIssuer struct {
	server       *httptest.Server
	userinfo     map[string]any
	tokenForms   []url.Values
	userinfoAuth []string
}

func newOIDCMockIssuer(t *testing.T, userinfo map[string]any) *oidcMockIssuer {
	t.Helper()
	m := &oidcMockIssuer{userinfo: userinfo}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]string{
				"authorization_endpoint": m.server.URL + "/authorize",
				"token_endpoint":         m.server.URL + "/token",
				"userinfo_endpoint":      m.server.URL + "/userinfo",
			})
		case "/token":
			if r.Method != http.MethodPost {
				t.Errorf("token method = %s, want POST", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.tokenForms = append(m.tokenForms, r.PostForm)
			writeJSON(t, w, map[string]any{
				"access_token": "mock-access-token",
				"token_type":   "Bearer",
				"expires_in":   300,
			})
		case "/userinfo":
			m.userinfoAuth = append(m.userinfoAuth, r.Header.Get("Authorization"))
			writeJSON(t, w, m.userinfo)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
}

func setupOIDCTest(t *testing.T, issuer *oidcMockIssuer) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("OIDC_ISSUER_URL", issuer.server.URL)
	t.Setenv("OIDC_CLIENT_ID", "paimos-test-client")
	t.Setenv("OIDC_CLIENT_SECRET", "test-client-secret")
	t.Setenv("OIDC_REDIRECT_URL", "https://paimos.example.test/api/auth/oidc/callback")
	t.Setenv("OIDC_PROMPT", "")
	t.Setenv("OIDC_BUTTON_LABEL", "Sign in with Test SSO")
	t.Setenv("OIDC_POST_LOGIN_REDIRECT", "/after-sso")

	resetOIDCTestGlobals()
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
		resetOIDCTestGlobals()
	})
}

func resetOIDCTestGlobals() {
	oidcCfgOnce.Lock()
	oidcCfg = oidcConfig{}
	oidcCfgOnce.Unlock()
	httpClient = &http.Client{Timeout: 15 * time.Second}
}

func seedOIDCUser(t *testing.T, username, email, role, status string) int64 {
	t.Helper()
	res, err := db.DB.Exec(`
		INSERT INTO users(username, password, role, role_key, status, email)
		VALUES(?, ?, ?, ?, ?, ?)
	`, username, "local-hash", role, role, status, email)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func startOIDCLogin(t *testing.T) (*httptest.ResponseRecorder, *url.URL) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil)
	rec := httptest.NewRecorder()
	OIDCLogin(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	return rec, loc
}

func finishOIDCCallback(t *testing.T, loginRec *httptest.ResponseRecorder, state string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=mock-code&state="+url.QueryEscape(state), nil)
	for _, c := range loginRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	OIDCCallback(rec, req)
	return rec
}

func TestOIDCStatusReflectsConfiguration(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{
		"sub":            "sub-1",
		"email":          "person@example.test",
		"email_verified": true,
	})
	setupOIDCTest(t, issuer)
	t.Setenv("OIDC_PROMPT", "select_account")

	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/status", nil)
	rec := httptest.NewRecorder()
	OIDCStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	var got struct {
		Enabled bool   `json:"enabled"`
		Label   string `json:"label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !got.Enabled || got.Label != "Sign in with Test SSO" {
		t.Fatalf("status = %+v, want enabled with configured label", got)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw status: %v", err)
	}
	if _, present := raw["prompt"]; present {
		t.Fatal("public OIDC status exposed the operator prompt setting")
	}
}

func TestOIDCConfigReadsClientSecretFromFile(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{
		"sub":            "sub-1",
		"email":          "person@example.test",
		"email_verified": true,
	})
	setupOIDCTest(t, issuer)
	path := filepath.Join(t.TempDir(), "oidc-client-secret")
	if err := os.WriteFile(path, []byte("file-client-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OIDC_CLIENT_SECRET_FILE", path)

	cfg, err := loadOIDCConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "file-client-secret" {
		t.Fatal("OIDC client secret did not come from OIDC_CLIENT_SECRET_FILE")
	}
}

func TestOIDCLoginBuildsPKCERedirect(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{"sub": "sub-1", "email": "person@example.test", "email_verified": true})
	setupOIDCTest(t, issuer)

	rec, loc := startOIDCLogin(t)
	q := loc.Query()
	if loc.String() == "" || loc.Path != "/authorize" {
		t.Fatalf("redirect location = %q, want issuer authorize endpoint", loc.String())
	}
	for _, key := range []string{"state", "nonce", "code_challenge"} {
		if q.Get(key) == "" {
			t.Fatalf("missing %s in authorize redirect: %s", key, loc.RawQuery)
		}
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	if strings.Contains(loc.RawQuery, "client_secret") {
		t.Fatalf("authorize redirect must not contain client_secret")
	}
	if len(rec.Result().Cookies()) < 3 {
		t.Fatalf("expected OIDC state/verifier/nonce cookies")
	}
	// PAI-743: no hint supplied → the parameter must be absent, not empty.
	if _, present := q["login_hint"]; present {
		t.Fatalf("login_hint sent without a caller-supplied hint: %s", loc.RawQuery)
	}
	if _, present := q["prompt"]; present {
		t.Fatalf("prompt sent without OIDC_PROMPT: %s", loc.RawQuery)
	}
}

func TestOIDCReturnRejectsBackslashQueryAndCookie(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mount            string
		redirectURL      string
		fallbackLocation string
	}{
		{
			name:             "root",
			redirectURL:      "https://paimos.example.test/api/auth/oidc/callback",
			fallbackLocation: "/after-sso",
		},
		{
			name:             "mounted",
			mount:            "/paimos",
			redirectURL:      "https://paimos.example.test/paimos/api/auth/oidc/callback",
			fallbackLocation: "/paimos/after-sso",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer := newOIDCMockIssuer(t, map[string]any{
				"sub": "sub-existing", "email": "person@example.test", "email_verified": true,
			})
			setupOIDCTest(t, issuer)
			prefix, err := publicbase.Parse(tc.mount)
			if err != nil {
				t.Fatal(err)
			}
			publicbase.SetCurrent(prefix)
			t.Cleanup(func() { publicbase.SetCurrent("") })
			t.Setenv("OIDC_REDIRECT_URL", tc.redirectURL)
			seedOIDCUser(t, "person", "person@example.test", "member", "active")

			for _, invalid := range []string{`/\evil.example`, `/\/evil.example`} {
				req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login?redirect="+url.QueryEscape(invalid), nil)
				rec := httptest.NewRecorder()
				OIDCLogin(rec, req)
				if rec.Code != http.StatusFound {
					t.Fatalf("login status = %d, want 302; body=%s", rec.Code, rec.Body.String())
				}
				for _, cookie := range rec.Result().Cookies() {
					if cookie.Name == oidcReturnCookie || cookie.Name == legacyOIDCReturnCookie {
						t.Fatalf("unsafe query %q set return cookie %q=%q", invalid, cookie.Name, cookie.Value)
					}
				}

				loginRec, loc := startOIDCLogin(t)
				callbackReq := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=mock-code&state="+url.QueryEscape(loc.Query().Get("state")), nil)
				for _, cookie := range loginRec.Result().Cookies() {
					callbackReq.AddCookie(cookie)
				}
				cookieValue := strings.ReplaceAll(invalid, `\`, "%5C")
				callbackReq.AddCookie(&http.Cookie{Name: oidcReturnCookie, Value: cookieValue})
				callbackRec := httptest.NewRecorder()
				OIDCCallback(callbackRec, callbackReq)
				if got := callbackRec.Header().Get("Location"); got != tc.fallbackLocation {
					t.Fatalf("unsafe cookie %q redirected to %q, want %q", cookieValue, got, tc.fallbackLocation)
				}
			}
		})
	}
}

func TestOIDCLoginForwardsConfiguredPromptAfterCacheWarmup(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{"sub": "sub-1", "email": "person@example.test", "email_verified": true})
	setupOIDCTest(t, issuer)

	// Warm the lazy config cache with the default, which must omit prompt.
	_, initialLoc := startOIDCLogin(t)
	if _, present := initialLoc.Query()["prompt"]; present {
		t.Fatalf("prompt sent with unset OIDC_PROMPT: %s", initialLoc.RawQuery)
	}

	// Changing only OIDC_PROMPT must invalidate the input cache. Outer
	// whitespace is configuration noise; interior spaces are valid OIDC
	// prompt separators and must survive URL encoding.
	t.Setenv("OIDC_PROMPT", "  select_account consent  ")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login?login_hint=person%40example.test", nil)
	rec := httptest.NewRecorder()
	OIDCLogin(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := loc.Query().Get("prompt"); got != "select_account consent" {
		t.Fatalf("prompt = %q, want trimmed configured value", got)
	}
	if got := loc.Query().Get("login_hint"); got != "person@example.test" {
		t.Fatalf("login_hint = %q, want configured hint alongside prompt", got)
	}
	if !strings.Contains(loc.RawQuery, "prompt=select_account+consent") {
		t.Fatalf("prompt was not safely URL-encoded: %s", loc.RawQuery)
	}

	// Whitespace-only configuration returns to the compatible default.
	t.Setenv("OIDC_PROMPT", " \t ")
	_, blankLoc := startOIDCLogin(t)
	if _, present := blankLoc.Query()["prompt"]; present {
		t.Fatalf("prompt sent for whitespace-only OIDC_PROMPT: %s", blankLoc.RawQuery)
	}
}

// TestOIDCLoginForwardsLoginHint: the identifier-first login already
// asked who the user is (PAI-743), so the IdP shouldn't ask again.
func TestOIDCLoginForwardsLoginHint(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{"sub": "sub-1", "email": "person@example.test", "email_verified": true})
	setupOIDCTest(t, issuer)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login?login_hint=person%40example.test", nil)
	rec := httptest.NewRecorder()
	OIDCLogin(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, want 302", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if got := loc.Query().Get("login_hint"); got != "person@example.test" {
		t.Fatalf("login_hint = %q, want the supplied identifier", got)
	}

	// An oversized hint is dropped rather than forwarded — the authorize
	// URL is attacker-influenced input and stays bounded.
	long := strings.Repeat("a", 400) + "@example.test"
	req = httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login?login_hint="+url.QueryEscape(long), nil)
	rec = httptest.NewRecorder()
	OIDCLogin(rec, req)
	loc, err = url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if _, present := loc.Query()["login_hint"]; present {
		t.Fatalf("oversized login_hint was forwarded: %s", loc.RawQuery)
	}
}

func TestOIDCCallbackInviteOnlyExistingUserCreatesSession(t *testing.T) {
	for _, policy := range []string{"", "enabled", "disabled"} {
		t.Run("password-"+policy, func(t *testing.T) {
			t.Setenv("AUTH_PASSWORD_LOGIN", policy)
			testOIDCCallbackInviteOnlyExistingUserCreatesSession(t)
		})
	}
}

func testOIDCCallbackInviteOnlyExistingUserCreatesSession(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{
		"sub":                "sub-existing",
		"email":              "Person@Example.Test",
		"email_verified":     true,
		"given_name":         "Pat",
		"family_name":        "Example",
		"preferred_username": "pat",
	})
	setupOIDCTest(t, issuer)
	userID := seedOIDCUser(t, "pat-local", "person@example.test", "member", "active")

	loginRec, loc := startOIDCLogin(t)
	callbackRec := finishOIDCCallback(t, loginRec, loc.Query().Get("state"))
	if callbackRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302; body=%s", callbackRec.Code, callbackRec.Body.String())
	}
	if got := callbackRec.Header().Get("Location"); got != "/after-sso" {
		t.Fatalf("callback location = %q, want /after-sso", got)
	}
	var sessionCookieValue string
	for _, c := range callbackRec.Result().Cookies() {
		if c.Name == sessionCookie {
			sessionCookieValue = c.Value
			if time.Until(c.Expires) < sessionDuration {
				t.Fatalf("session cookie expiry = %s, want absolute-cap lifetime beyond sliding window", c.Expires)
			}
		}
	}
	if sessionCookieValue == "" {
		t.Fatalf("missing session cookie")
	}
	var sessionUserID int64
	var createdAt string
	if err := db.DB.QueryRow("SELECT user_id, created_at FROM sessions WHERE id=?", sessionCookieValue).Scan(&sessionUserID, &createdAt); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if sessionUserID != userID || createdAt == "" {
		t.Fatalf("session user_id=%d created_at=%q, want user_id=%d and created_at set", sessionUserID, createdAt, userID)
	}
	if len(issuer.tokenForms) != 1 || issuer.tokenForms[0].Get("code_verifier") == "" {
		t.Fatalf("token exchange did not include PKCE verifier")
	}
	if got := issuer.userinfoAuth[0]; got != "Bearer mock-access-token" {
		t.Fatalf("userinfo Authorization = %q, want bearer access token", got)
	}
	var userCount int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM users WHERE lower(email)='person@example.test'").Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("user count = %d, want existing user only", userCount)
	}
}

func TestOIDCCallbackInviteOnlyRejectsUnknownUser(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{
		"sub":            "sub-new",
		"email":          "new@example.test",
		"email_verified": true,
	})
	setupOIDCTest(t, issuer)

	loginRec, loc := startOIDCLogin(t)
	callbackRec := finishOIDCCallback(t, loginRec, loc.Query().Get("state"))
	if callbackRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callbackRec.Code)
	}
	if got := callbackRec.Header().Get("Location"); got != "/login?sso_error=invite_required" {
		t.Fatalf("callback location = %q, want invite_required", got)
	}
	var count int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM users WHERE lower(email)='new@example.test'").Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("unknown invite-only SSO user was created")
	}
}

func TestOIDCCallbackAutoCreateExternalUser(t *testing.T) {
	issuer := newOIDCMockIssuer(t, map[string]any{
		"sub":                "sub-auto",
		"email":              "auto@example.test",
		"email_verified":     true,
		"given_name":         "Auto",
		"family_name":        "User",
		"preferred_username": "Auto User!",
	})
	setupOIDCTest(t, issuer)
	t.Setenv("OIDC_PROVISION_MODE", "auto-create")
	t.Setenv("OIDC_AUTO_CREATE_ROLE", "external")
	if _, err := db.DB.Exec(`INSERT INTO projects(name, key) VALUES('Private Project', 'PRV')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	loginRec, loc := startOIDCLogin(t)
	callbackRec := finishOIDCCallback(t, loginRec, loc.Query().Get("state"))
	if callbackRec.Code != http.StatusFound || callbackRec.Header().Get("Location") != "/after-sso" {
		t.Fatalf("callback = %d %q, want 302 /after-sso", callbackRec.Code, callbackRec.Header().Get("Location"))
	}
	var username, password, role, roleKey, firstName, lastName string
	var userID int64
	if err := db.DB.QueryRow(`
		SELECT id, username, password, role, role_key, first_name, last_name
		FROM users WHERE lower(email)='auto@example.test'
	`).Scan(&userID, &username, &password, &role, &roleKey, &firstName, &lastName); err != nil {
		t.Fatalf("read auto-created user: %v", err)
	}
	if username != "autouser" || password != "" || role != "external" || roleKey != "external" || firstName != "Auto" || lastName != "User" {
		t.Fatalf("auto-created user = username:%q password:%q role:%q role_key:%q first:%q last:%q",
			username, password, role, roleKey, firstName, lastName)
	}
	var grants int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM project_members WHERE user_id=?", userID).Scan(&grants); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if grants != 0 {
		t.Fatalf("external auto-created user got project grants: %d", grants)
	}
}

func TestOIDCCallbackRequiresExplicitVerifiedEmail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		userinfo map[string]any
	}{
		{
			name: "missing claim",
			userinfo: map[string]any{
				"sub":   "sub-missing",
				"email": "missing@example.test",
			},
		},
		{
			name: "false claim",
			userinfo: map[string]any{
				"sub":            "sub-false",
				"email":          "false@example.test",
				"email_verified": false,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer := newOIDCMockIssuer(t, tc.userinfo)
			setupOIDCTest(t, issuer)
			loginRec, loc := startOIDCLogin(t)
			callbackRec := finishOIDCCallback(t, loginRec, loc.Query().Get("state"))
			if got := callbackRec.Header().Get("Location"); got != "/login?sso_error=email_required" {
				t.Fatalf("callback location = %q, want email_required", got)
			}
		})
	}
}
