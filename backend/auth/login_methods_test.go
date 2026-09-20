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
	"os"
	"strings"
	"testing"
)

// PAI-743 home realm discovery. resolveLoginMethods is a pure function
// of (identifier, ssoEnabled, domain config), so the table below covers
// the entire decision surface the login page can reach.

func domainSet(t *testing.T, raw string) map[string]struct{} {
	t.Helper()
	t.Setenv(envSSODomains, raw)
	return ssoDomains()
}

func TestResolveLoginMethods(t *testing.T) {
	t.Setenv("AUTH_PASSWORD_LOGIN", "")
	cases := []struct {
		name         string
		identifier   string
		ssoEnabled   bool
		domainsRaw   string
		wantPassword bool
		wantSSO      bool
	}{
		// Pre-PAI-743 parity: no IdP at all.
		{"sso off, username", "mba", false, "", true, false},
		{"sso off, email in would-be realm", "mba@agm.ng", false, "agm.ng", true, false},

		// Pre-PAI-743 parity: IdP on, but no realm map configured.
		// Every identifier keeps both methods — the upgrade is a no-op
		// for existing instances (this is the ppm case).
		{"sso on, no domains, username", "mba", true, "", true, true},
		{"sso on, no domains, email", "mba@agm.ng", true, "", true, true},

		// Realm routing.
		{"domain match hides password", "mba@agm.ng", true, "agm.ng", false, true},
		{"domain match is case-insensitive", "MBA@AGM.NG", true, "agm.ng", false, true},
		{"config tolerates @ prefix and spaces", "mba@agm.ng", true, " @agm.ng , other.tld ", false, true},
		{"second domain in list matches", "x@other.tld", true, "agm.ng,other.tld", false, true},
		{"unlisted domain keeps both", "someone@example.com", true, "agm.ng", true, true},

		// A bare username cannot be routed without a lookup — and a
		// lookup is exactly what INV-AUTH-HRD forbids — so it keeps
		// both methods rather than guessing.
		{"bare username keeps both despite domain map", "mba", true, "agm.ng", true, true},

		// Malformed identifiers must not crash or accidentally route.
		{"empty identifier", "", true, "agm.ng", true, true},
		{"trailing at", "mba@", true, "agm.ng", true, true},
		{"lone at", "@", true, "agm.ng", true, true},
		{"subdomain is not the configured domain", "x@mail.agm.ng", true, "agm.ng", true, true},
		{"last at wins for quoted local parts", `"a@b"@agm.ng`, true, "agm.ng", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveLoginMethods(c.identifier, c.ssoEnabled, domainSet(t, c.domainsRaw))
			if got.Password != c.wantPassword || got.SSO != c.wantSSO {
				t.Fatalf("resolveLoginMethods(%q, sso=%v, %q) = (password=%v, sso=%v), want (password=%v, sso=%v)",
					c.identifier, c.ssoEnabled, c.domainsRaw, got.Password, got.SSO, c.wantPassword, c.wantSSO)
			}
			// A method-less answer would lock the user out of the login
			// page entirely; at least one path must always remain.
			if !got.Password && !got.SSO {
				t.Fatal("no login method offered — user would be stranded")
			}
		})
	}
}

func TestLoginMethodsPasswordPolicyAndBypassWithoutDatabase(t *testing.T) {
	setupPasswordPolicyOIDC(t)
	t.Setenv("OIDC_SSO_DOMAINS", "example.test")
	// Deliberately do not initialize a database: HRD must use config only.
	for _, policy := range []string{"", "enabled", "disabled"} {
		t.Run("password-"+policy, func(t *testing.T) {
			t.Setenv("AUTH_PASSWORD_LOGIN", policy)
			for _, method := range []string{"", "password"} {
				var previous string
				for _, identifier := range []string{"admin@example.test", "nonexistent@example.test"} {
					body, _ := json.Marshal(map[string]string{"identifier": identifier, "method": method})
					rec := passwordPolicyPost(LoginMethods, "/api/auth/login/methods", string(body))
					var got loginMethods
					if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if rec.Code != http.StatusOK || got.Password != (policy != "disabled" && method == "password") || !got.SSO {
						t.Fatalf("policy=%q method=%q: unexpected methods %+v", policy, method, got)
					}
					if previous != "" && previous != rec.Body.String() {
						t.Fatal("HRD response differs by account existence")
					}
					previous = rec.Body.String()
				}
			}
		})
	}
	// IdP discovery failure cannot advertise a password fallback when disabled.
	t.Setenv("AUTH_PASSWORD_LOGIN", "disabled")
	for _, identifier := range []string{"admin", "admin@example.test", "nobody@other.test", ""} {
		for _, sso := range []bool{true, false} {
			got := resolveLoginMethods(identifier, sso, ssoDomains())
			if got.Password || got.SSO != sso {
				t.Fatalf("disabled methods %+v", got)
			}
		}
	}
	status := httptest.NewRecorder()
	OIDCStatus(status, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/status", nil))
	if !strings.Contains(status.Body.String(), `"password_disabled":true`) {
		t.Fatal("login page cannot learn disabled password policy")
	}
}

func TestPasswordLoginDefaultPresentationBytes(t *testing.T) {
	setupPasswordPolicyOIDC(t)
	t.Setenv("OIDC_BUTTON_LABEL", "Sign in with SSO")
	t.Setenv("OIDC_SSO_DOMAINS", "")
	for _, policy := range []string{"", "enabled"} {
		t.Setenv("AUTH_PASSWORD_LOGIN", policy)
		methods := passwordPolicyPost(LoginMethods, "/api/auth/login/methods", `{"identifier":"admin"}`)
		if methods.Body.String() != "{\"password\":true,\"sso\":true,\"sso_label\":\"Sign in with SSO\"}\n" {
			t.Fatalf("default login-methods bytes changed: %s", methods.Body.String())
		}
		status := httptest.NewRecorder()
		OIDCStatus(status, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/status", nil))
		if status.Body.String() != "{\"enabled\":true,\"label\":\"Sign in with SSO\"}\n" {
			t.Fatalf("default OIDC-status bytes changed: %s", status.Body.String())
		}
	}
}

// TestResolveLoginMethods_NoEnumeration is the INV-AUTH-HRD regression
// guard: identifiers that differ only in whether the account could
// exist must produce byte-identical answers. resolveLoginMethods takes
// no DB handle, so this holds by construction — the test pins the
// construction so a future "just look up the user" refactor fails here
// instead of shipping an enumeration oracle.
func TestResolveLoginMethods_NoEnumeration(t *testing.T) {
	domains := domainSet(t, "agm.ng")

	// Same realm, wildly different plausibility of existing.
	real := resolveLoginMethods("mba@agm.ng", true, domains)
	fake := resolveLoginMethods("definitely-not-a-user-9f3c@agm.ng", true, domains)
	if real != fake {
		t.Fatalf("realm answers differ by account: %+v vs %+v — enumeration oracle", real, fake)
	}

	// Unrouted realm: same again.
	realOther := resolveLoginMethods("mba@example.com", true, domains)
	fakeOther := resolveLoginMethods("nobody-8a21@example.com", true, domains)
	if realOther != fakeOther {
		t.Fatalf("non-realm answers differ by account: %+v vs %+v — enumeration oracle", realOther, fakeOther)
	}
}

// TestLoginMethodsDoesNotTouchTheDatabase locks the invariant down at
// the source level: no db reference may appear in the handler file.
// Cheap, blunt, and it fails loudly the moment someone adds a lookup.
func TestLoginMethodsDoesNotTouchTheDatabase(t *testing.T) {
	raw, err := os.ReadFile("login_methods.go")
	if err != nil {
		t.Fatalf("read handler source: %v", err)
	}
	src := string(raw)
	for _, forbidden := range []string{"db.DB", "QueryRow", "Query(", "Exec("} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("login_methods.go references %q — INV-AUTH-HRD forbids account lookups on this public endpoint", forbidden)
		}
	}
}
