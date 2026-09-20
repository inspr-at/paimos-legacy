// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

type passwordPolicyTransport func(*http.Request) (*http.Response, error)

func (transport passwordPolicyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func unsetPasswordPolicyEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "") // Register restoration before removing the binding.
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

// Exercise discovery and token/userinfo HTTP requests without opening sockets.
func setupPasswordPolicyOIDC(t *testing.T) *int {
	t.Helper()
	t.Setenv("OIDC_ISSUER_URL", "https://issuer.example.test")
	t.Setenv("OIDC_CLIENT_ID", "test-client")
	t.Setenv("OIDC_REDIRECT_URL", "https://paimos.example.test/api/auth/oidc/callback")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	unsetPasswordPolicyEnv(t, "OIDC_CLIENT_SECRET_FILE")
	t.Setenv("OIDC_PROVISION_MODE", "invite-only")
	t.Setenv("OIDC_AUTO_CREATE_ROLE", "member")
	t.Setenv("OIDC_POST_LOGIN_REDIRECT", "/after-sso")
	resetOIDCTestGlobals()
	t.Cleanup(resetOIDCTestGlobals)
	exchanges := 0
	httpClient.Transport = passwordPolicyTransport(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, rec, map[string]string{
				"authorization_endpoint": "https://issuer.example.test/authorize",
				"token_endpoint":         "https://issuer.example.test/token",
				"userinfo_endpoint":      "https://issuer.example.test/userinfo",
			})
		case "/token":
			if err := r.ParseForm(); err != nil || r.Method != http.MethodPost || r.Form.Get("code_verifier") == "" {
				t.Error("OIDC token exchange missing PKCE verifier")
			}
			exchanges++
			writeJSON(t, rec, map[string]string{"access_token": "mock-access-token", "token_type": "Bearer"})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer mock-access-token" {
				t.Error("userinfo missing access token")
			}
			writeJSON(t, rec, map[string]any{"sub": "policy-user", "email": "policy@example.test", "email_verified": true})
		default:
			t.Errorf("unexpected OIDC request path: %s", r.URL.Path)
			rec.WriteHeader(http.StatusNotFound)
		}
		return rec.Result(), nil
	})
	return &exchanges
}

func TestPasswordLoginDisabledPreservesOIDCFlow(t *testing.T) {
	setupPrincipalTestDB(t)
	t.Setenv("AUTH_PASSWORD_LOGIN", "disabled")
	exchanges := setupPasswordPolicyOIDC(t)
	userID := seedOIDCUser(t, "policy-admin", "policy@example.test", "admin", "active")
	if err := ValidatePasswordLoginConfig(); err != nil {
		t.Fatal(err)
	}
	login, location := startOIDCLogin(t)
	if location.Query().Get("code_challenge") == "" || location.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("OIDC login missing PKCE challenge")
	}
	callback := finishOIDCCallback(t, login, location.Query().Get("state"))
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/after-sso" || *exchanges != 1 {
		t.Fatalf("OIDC callback failed: status=%d exchanges=%d", callback.Code, *exchanges)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	for _, cookie := range callback.Result().Cookies() {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.UserID() != userID || !IsViaOIDC(r.Context()) {
			t.Error("OIDC callback session did not authenticate")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OIDC session unusable with password login disabled: status=%d", rec.Code)
	}
}

func TestPasswordLoginConfig(t *testing.T) {
	for _, value := range []string{"", "enabled", " ENABLED "} {
		t.Run("default-"+value, func(t *testing.T) {
			t.Setenv("AUTH_PASSWORD_LOGIN", value)
			if value == "" {
				unsetPasswordPolicyEnv(t, "AUTH_PASSWORD_LOGIN")
			}
			t.Setenv("OIDC_ISSUER_URL", "")
			if !PasswordLoginEnabled() || ValidatePasswordLoginConfig() != nil {
				t.Fatal("default/enabled must preserve local login without OIDC")
			}
		})
	}
	for _, value := range []string{"false", "disable", "0"} {
		t.Run("invalid-"+value, func(t *testing.T) {
			t.Setenv("AUTH_PASSWORD_LOGIN", value)
			if PasswordLoginEnabled() || ValidatePasswordLoginConfig() == nil {
				t.Fatal("invalid policy must fail closed and refuse startup")
			}
		})
	}
	for _, missing := range []string{"OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_REDIRECT_URL", ""} {
		t.Run("disabled-missing-"+missing, func(t *testing.T) {
			t.Setenv("AUTH_PASSWORD_LOGIN", " DISABLED ")
			t.Setenv("OIDC_ISSUER_URL", "https://unreachable.example.invalid")
			t.Setenv("OIDC_CLIENT_ID", "test-client")
			t.Setenv("OIDC_REDIRECT_URL", "https://paimos.example.test/api/auth/oidc/callback")
			t.Setenv("OIDC_CLIENT_SECRET", "")
			unsetPasswordPolicyEnv(t, "OIDC_CLIENT_SECRET_FILE")
			t.Setenv("OIDC_PROVISION_MODE", "invite-only")
			t.Setenv("OIDC_AUTO_CREATE_ROLE", "member")
			if missing != "" {
				t.Setenv(missing, "")
			}
			err := ValidatePasswordLoginConfig()
			if PasswordLoginEnabled() {
				t.Fatal("disabled policy enabled password login")
			}
			if missing == "" && err != nil {
				t.Fatalf("static config must pass even during IdP outage: %v", err)
			}
			if missing != "" && (err == nil || !strings.Contains(err.Error(), "AUTH_PASSWORD_LOGIN=disabled requires OIDC configuration")) {
				t.Fatalf("missing %s: expected clear startup error, got %v", missing, err)
			}
			if missing == "" {
				for key, bad := range map[string]string{"OIDC_ISSUER_URL": "not-a-url", "OIDC_REDIRECT_URL": "https://example.test/wrong", "OIDC_PROVISION_MODE": "wrong"} {
					t.Run(key, func(t *testing.T) {
						t.Setenv(key, bad)
						if ValidatePasswordLoginConfig() == nil {
							t.Fatal("invalid OIDC config accepted")
						}
					})
				}
			}
		})
	}
}

func passwordPolicyPost(handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestPasswordLoginPolicyAllRolesAndQueryBypass(t *testing.T) {
	for _, policy := range []string{"", "enabled", "disabled"} {
		for _, role := range []string{"member", "external", "admin", "super_admin"} {
			t.Run(policy+"/"+role, func(t *testing.T) {
				setupPrincipalTestDB(t)
				t.Setenv("AUTH_PASSWORD_LOGIN", policy)
				hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
				if err != nil {
					t.Fatal(err)
				}
				legacyRole := role
				if role == "super_admin" {
					legacyRole = "admin"
				}
				if _, err := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status) VALUES('local-user',?,?,?,'active')`, string(hash), legacyRole, role); err != nil {
					t.Fatal(err)
				}
				for _, query := range []string{"", "?method=password"} {
					rec := passwordPolicyPost(LoginHandler, "/api/auth/login"+query, `{"username":"local-user","password":"correct-password"}`)
					if policy != "disabled" {
						if rec.Code != http.StatusOK || len(rec.Result().Cookies()) == 0 {
							t.Fatalf("enabled login failed: status=%d", rec.Code)
						}
						continue
					}
					if rec.Code != http.StatusUnauthorized || rec.Body.String() != "{\"error\":\"invalid credentials\"}\n" || len(rec.Result().Cookies()) != 0 {
						t.Fatalf("disabled login not rejected generically: status=%d body=%s", rec.Code, rec.Body.String())
					}
				}
				if policy == "disabled" {
					for _, body := range []string{`{"username":"missing","password":"correct-password"}`, `{"username":"local-user","password":"wrong"}`} {
						rec := passwordPolicyPost(LoginHandler, "/api/auth/login", body)
						if rec.Code != http.StatusUnauthorized || rec.Body.String() != "{\"error\":\"invalid credentials\"}\n" {
							t.Fatal("credential existence changes disabled response")
						}
					}
					var sessions, pending int
					if err := db.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions), (SELECT COUNT(*) FROM totp_pending)`).Scan(&sessions, &pending); err != nil || sessions != 0 || pending != 0 {
						t.Fatalf("disabled login wrote credentials: sessions=%d pending=%d err=%v", sessions, pending, err)
					}
				}
			})
		}
	}
}

func TestPasswordLoginDisabledRejectsPreviouslyIssuedTOTP(t *testing.T) {
	setupPrincipalTestDB(t)
	t.Setenv("AUTH_PASSWORD_LOGIN", "enabled")
	userID := insertPrincipalUser(t, "totp-admin")
	const secret = "JBSWY3DPEHPK3PXP"
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET role='admin',role_key='admin',password=?,totp_secret=?,totp_enabled=1 WHERE id=?`, string(hash), secret, userID); err != nil {
		t.Fatal(err)
	}
	first := passwordPolicyPost(LoginHandler, "/api/auth/login", `{"username":"totp-admin","password":"correct-password"}`)
	var pending struct {
		Token string `json:"totp_token"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &pending); err != nil || pending.Token == "" {
		t.Fatal("enabled password login did not issue TOTP challenge")
	}
	t.Setenv("AUTH_PASSWORD_LOGIN", "disabled")
	blocked := passwordPolicyPost(LoginHandler, "/api/auth/login?method=password", `{"username":"totp-admin","password":"correct-password"}`)
	if blocked.Code != http.StatusUnauthorized || strings.Contains(blocked.Body.String(), "totp_token") {
		t.Fatal("disabled password login issued a fresh TOTP challenge")
	}
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	rec := passwordPolicyPost(TOTPVerify, "/api/auth/totp/verify", fmt.Sprintf(`{"totp_token":%q,"code":%q}`, pending.Token, code))
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("pending password TOTP bypassed disabled policy: status=%d", rec.Code)
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("TOTP created a session: count=%d err=%v", count, err)
	}
	// Re-enabling retains the product's password + TOTP feature.
	t.Setenv("AUTH_PASSWORD_LOGIN", "enabled")
	rec = passwordPolicyPost(TOTPVerify, "/api/auth/totp/verify", fmt.Sprintf(`{"totp_token":%q,"code":%q}`, pending.Token, code))
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled TOTP completion failed: status=%d", rec.Code)
	}
}

func TestPasswordLoginDisabledPreservesAPIKeyBreakGlass(t *testing.T) {
	setupPrincipalTestDB(t)
	t.Setenv("AUTH_PASSWORD_LOGIN", "disabled")
	t.Setenv("OIDC_ISSUER_URL", "") // No dependence on a working IdP.
	userID := insertPrincipalUser(t, "break-glass-admin")
	if _, err := db.DB.Exec(`UPDATE users SET role='admin',role_key='super_admin' WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	const rawKey = "paimos_test_password_policy_break_glass"
	digest := sha256.Sum256([]byte(rawKey))
	if _, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'break-glass',?,'paimos_test','all')`, userID, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.Kind() != PrincipalAPIKey || principal.UserID() != userID {
			t.Error("API-key principal missing")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("API-key break-glass failed or minted a web cookie: status=%d", rec.Code)
	}
}
