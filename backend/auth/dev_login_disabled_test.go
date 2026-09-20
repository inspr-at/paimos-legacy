// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

//go:build !dev_login

// PAI-267 — pin the production-build invariant: without the
// `dev_login` build tag, auth.DevLoginEnabled() must return false.
// Paired with the dev-build test in dev_login_test.go which pins the
// opposite. If a future refactor accidentally short-circuits the
// build-tag gate, one of these two tests fails.

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
)

func TestDevLoginEnabled_ReturnsFalseOnProdBuild(t *testing.T) {
	if auth.DevLoginEnabled() {
		t.Fatalf("DevLoginEnabled() = true on production build — dev-login route would be exposed in shipping binaries")
	}
}

func TestPasswordLoginDisabledDevLoginStillAbsentOnProdBuild(t *testing.T) {
	t.Setenv("AUTH_PASSWORD_LOGIN", "disabled")
	rec := httptest.NewRecorder()
	auth.DevLoginHandler(rec, httptest.NewRequest(http.MethodPost, "/api/auth/dev-login", nil))
	if auth.DevLoginEnabled() || rec.Code != http.StatusNotFound || len(rec.Result().Cookies()) != 0 {
		t.Fatal("production dev-login path is reachable")
	}
}
