// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"database/sql"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/db"
)

// PasswordLoginEnabled is instance policy, independent of account roles and
// home realm discovery. Invalid values fail closed; startup rejects them.
// API-key authentication deliberately does not consult this policy.
func PasswordLoginEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTH_PASSWORD_LOGIN"))) {
	case "", "enabled":
		return true
	default:
		return false
	}
}

// ValidatePasswordLoginConfig runs before opening the database or serving HTTP.
// Validate static OIDC configuration only: an IdP outage must not prevent the
// server from starting for API-key break-glass access through the CLI.
func ValidatePasswordLoginConfig() error {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTH_PASSWORD_LOGIN"))) {
	case "", "enabled":
		return nil
	case "disabled":
		cfg, err := oidcConfigFromEnv()
		if err != nil {
			return fmt.Errorf("AUTH_PASSWORD_LOGIN=disabled requires OIDC configuration: %w", err)
		}
		issuer, err := url.Parse(cfg.IssuerURL)
		if err != nil || issuer.Hostname() == "" || (issuer.Scheme != "https" && issuer.Scheme != "http") {
			return fmt.Errorf("AUTH_PASSWORD_LOGIN=disabled requires OIDC_ISSUER_URL to be an absolute http(s) URL")
		}
		if issuer.Scheme == "http" && !strings.EqualFold(issuer.Hostname(), "localhost") && !net.ParseIP(issuer.Hostname()).IsLoopback() {
			return fmt.Errorf("AUTH_PASSWORD_LOGIN=disabled requires HTTPS for OIDC_ISSUER_URL unless the host is loopback")
		}
		return nil
	default:
		return fmt.Errorf("AUTH_PASSWORD_LOGIN must be enabled or disabled")
	}
}

// ValidatePasswordLoginAccess runs after database migration and admin seeding,
// before serving HTTP. Check local recovery prerequisites without contacting the
// IdP: configuration alone cannot make an email-less bootstrap admin usable.
func ValidatePasswordLoginAccess() error {
	if PasswordLoginEnabled() {
		return nil
	}
	rows, err := db.DB.Query("SELECT email FROM users WHERE status='active' AND email IS NOT NULL AND email<>''")
	if err != nil {
		return fmt.Errorf("check OIDC matching users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return fmt.Errorf("check OIDC matching user: %w", err)
		}
		// Match the stored address, not a display name or surrounding spaces:
		// OIDC normalizes the incoming claim but only lowercases the DB value.
		address, err := mail.ParseAddress(email)
		if err == nil && address.Address == email {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check OIDC matching users: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close OIDC matching users: %w", err)
	}

	// Only general keys can provide CLI recovery. Disabled owners, a pending
	// password-rotation gate, and dedicated service credentials cannot do so.
	keys, err := db.DB.Query(`SELECT ak.expires_at FROM api_keys ak JOIN users u ON u.id=ak.user_id
		WHERE u.status='active' AND u.must_change_password=0 AND ak.disabled_at IS NULL
		AND ak.credential_kind='general'
		AND NOT EXISTS (SELECT 1 FROM conversation_service_bindings b WHERE b.api_key_id=ak.id)`)
	if err != nil {
		return fmt.Errorf("check active recovery API keys: %w", err)
	}
	defer keys.Close()
	now := time.Now().UTC()
	for keys.Next() {
		var expiry sql.NullString
		if err := keys.Scan(&expiry); err != nil {
			return fmt.Errorf("check recovery API key: %w", err)
		}
		if !expiry.Valid {
			return nil
		}
		if expires, err := parseCredentialTimestamp(expiry.String); err == nil && expires.After(now) {
			return nil
		}
	}
	if err := keys.Err(); err != nil {
		return fmt.Errorf("check active recovery API keys: %w", err)
	}
	return fmt.Errorf("AUTH_PASSWORD_LOGIN=disabled requires an active user with a usable email for OIDC matching or an active general API key with an unblocked owner; provision access with password login enabled before disabling it")
}
