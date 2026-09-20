// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"fmt"
	"net/url"
	"os"
	"strings"
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
		if err != nil || issuer.Host == "" || (issuer.Scheme != "https" && issuer.Scheme != "http") {
			return fmt.Errorf("AUTH_PASSWORD_LOGIN=disabled requires OIDC_ISSUER_URL to be an absolute http(s) URL")
		}
		return nil
	default:
		return fmt.Errorf("AUTH_PASSWORD_LOGIN must be enabled or disabled")
	}
}
