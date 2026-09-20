// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

//go:build dev_login

// PAI-267 — dev-login handler tests, gated on the same build tag as
// the handler itself. Production builds compile without this file.

package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/internal/testdb"
)

// devLoginTestSetup opens an in-memory DB, seeds a single fixture
// user, validates a known PAIMOS_DEV_LOGIN_TOKEN, and returns a
// cleanup that resets state. Avoids pulling in handlers/testhelper
// (which would create a circular dep) — we only need the bare
// minimum for handler-level coverage.
func devLoginTestSetup(t *testing.T, token string) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_DEV_LOGIN_TOKEN", token)
	t.Setenv("PAIMOS_ENV", "development")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	auth.ValidateDevLoginConfig()
	if _, err := db.DB.Exec(`INSERT INTO users(username, password, role, status) VALUES('dev_admin','','admin','active')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func devLoginPost(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/dev-login", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	auth.DevLoginHandler(rec, req)
	return rec
}

func TestDevLogin_HappyPath(t *testing.T) {
	devLoginTestSetup(t, "this-is-a-test-token-of-exactly-32+chars-long")

	rec := devLoginPost(t, map[string]string{
		"username": "dev_admin",
		"token":    "this-is-a-test-token-of-exactly-32+chars-long",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, _ := body["via_dev_login"].(bool); !v {
		t.Errorf("body.via_dev_login should be true, got %v", body["via_dev_login"])
	}
	// Session cookie must be set.
	cookies := rec.Result().Cookies()
	var sid string
	for _, c := range cookies {
		if c.Name == "session" {
			sid = c.Value
		}
	}
	if sid == "" {
		t.Fatalf("no session cookie set; cookies=%v", cookies)
	}
	// Session row must carry via_dev_login=1.
	var via int
	if err := db.DB.QueryRow("SELECT via_dev_login FROM sessions WHERE id=?", sid).Scan(&via); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if via != 1 {
		t.Errorf("session.via_dev_login: got %d, want 1", via)
	}
}

func TestDevLogin_PasswordLoginDisabled(t *testing.T) {
	const token = "this-is-a-test-token-of-exactly-32+chars-long"
	devLoginTestSetup(t, token)
	t.Setenv("AUTH_PASSWORD_LOGIN", "disabled")
	rec := devLoginPost(t, map[string]string{"username": "dev_admin", "token": token})
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != "{\"error\":\"invalid dev-login credentials\"}\n" || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("dev login bypassed policy: status=%d", rec.Code)
	}
}

func TestDevLogin_BadToken(t *testing.T) {
	devLoginTestSetup(t, "this-is-a-test-token-of-exactly-32+chars-long")

	rec := devLoginPost(t, map[string]string{
		"username": "dev_admin",
		"token":    "wrong-token-of-thirty-two+chars-but-not-the-right-one",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", rec.Code)
	}
}

func TestDevLogin_UnknownUser(t *testing.T) {
	devLoginTestSetup(t, "this-is-a-test-token-of-exactly-32+chars-long")

	rec := devLoginPost(t, map[string]string{
		"username": "ghost_user",
		"token":    "this-is-a-test-token-of-exactly-32+chars-long",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (no info-leak distinguishing user vs token)", rec.Code)
	}
}

func TestDevLogin_NonActiveUser(t *testing.T) {
	devLoginTestSetup(t, "this-is-a-test-token-of-exactly-32+chars-long")
	if _, err := db.DB.Exec(`UPDATE users SET status='suspended' WHERE username='dev_admin'`); err != nil {
		t.Fatal(err)
	}
	rec := devLoginPost(t, map[string]string{
		"username": "dev_admin",
		"token":    "this-is-a-test-token-of-exactly-32+chars-long",
	})
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("non-active dev login status=%d cookies=%v", rec.Code, rec.Result().Cookies())
	}
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("non-active dev login minted %d sessions", count)
	}
}

func TestDevLogin_NoTokenConfigured(t *testing.T) {
	// Setup with empty token → ValidateDevLoginConfig leaves devLoginToken
	// empty → handler returns 503.
	devLoginTestSetup(t, "")
	rec := devLoginPost(t, map[string]string{
		"username": "dev_admin",
		"token":    "anything-since-server-isnt-configured",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rec.Code)
	}
}

func TestValidateDevLoginConfig_PanicsOnProductionEnv(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when PAIMOS_ENV=production with dev_login build")
		} else if !strings.Contains(strings.ToLower(toString(r)), "production") {
			t.Errorf("panic message should mention production, got: %v", r)
		}
	}()
	t.Setenv("PAIMOS_ENV", "production")
	auth.ValidateDevLoginConfig()
}

func TestValidateDevLoginConfig_PanicsOnShortToken(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when token is too short")
		} else if !strings.Contains(toString(r), "32 chars") {
			t.Errorf("panic message should mention 32-char minimum, got: %v", r)
		}
	}()
	t.Setenv("PAIMOS_ENV", "development")
	t.Setenv("PAIMOS_DEV_LOGIN_TOKEN", "short")
	auth.ValidateDevLoginConfig()
}

func TestValidateDevLoginConfig_PanicsOnDummyToken(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when token is a placeholder value")
		}
	}()
	t.Setenv("PAIMOS_ENV", "development")
	// 32+ chars but on the blocklist via lowercased trim.
	t.Setenv("PAIMOS_DEV_LOGIN_TOKEN", "                          PASSWORD                          ")
	auth.ValidateDevLoginConfig()
}

// TestDevLoginEnabled_ReturnsTrueOnDevBuild pins the build-tag wiring:
// when this file compiles (because of //go:build dev_login), the
// auth.DevLoginEnabled() helper must report true. The companion
// production-build test (in dev_login_disabled_test.go below) pins
// the opposite — so a future refactor that breaks the gate will
// fail one or the other.
func TestDevLoginEnabled_ReturnsTrueOnDevBuild(t *testing.T) {
	if !auth.DevLoginEnabled() {
		t.Fatalf("DevLoginEnabled() = false on dev build — build-tag wiring broken")
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	// Last-resort: stringify via encoding/json
	b, _ := json.Marshal(v)
	return string(b)
}

// silence unused-import warnings in the rare case some helpers above
// get inlined out under refactors.
var _ = os.Getenv
