// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package flowhost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
)

const (
	HostID = "paimos"

	IdentityContractVersion = "inspr.flow-identity/0.1-draft"
	AuthorityDisclaimer     = "Schema validity is not authentication. Host must issue this context from a verified principal and revalidate on every consequential intent."

	ContextTTL = 8 * time.Hour

	overviewBaselinePath = "/projects/%d?tab=overview#baseline-batch"
)

type ActorKind string

const (
	ActorHuman ActorKind = "human"
	ActorAgent ActorKind = "agent"
)

type IdentityContext struct {
	ContractVersion     string          `json:"contract_version"`
	EvaluatedAt         string          `json:"evaluated_at"`
	HostID              string          `json:"host_id"`
	PrincipalKind       string          `json:"principal_kind"`
	PrincipalRef        string          `json:"principal_ref"`
	BindingRef          string          `json:"binding_ref"`
	OrganizationRef     *string         `json:"organization_ref"`
	ProjectRef          string          `json:"project_ref"`
	ActorKind           string          `json:"actor_kind"`
	IssuedAt            string          `json:"issued_at"`
	ExpiresAt           string          `json:"expires_at"`
	FreshUntil          string          `json:"fresh_until"`
	ContextRevision     string          `json:"context_revision"`
	AuthorityDisclaimer string          `json:"authority_disclaimer"`
	Display             IdentityDisplay `json:"display"`
}

type IdentityDisplay struct {
	UserLabel    string `json:"user_label"`
	UserInitials string `json:"user_initials"`
	ProjectLabel string `json:"project_label"`
	FixtureLabel string `json:"fixture_label"`
}

func OpaqueRef(kind string, parts ...string) string {
	sum := sha256.New()
	sum.Write([]byte(HostID + ":" + kind + ":"))
	for _, part := range parts {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return HostID + ":" + kind + "-" + hex.EncodeToString(sum.Sum(nil)[:16])
}

func NextTenMinuteBoundary(now time.Time) time.Time {
	utc := now.UTC()
	elapsed := utc.Unix() % 600
	if elapsed == 0 {
		return utc.Add(10 * time.Minute)
	}
	return utc.Add(time.Duration(600-elapsed) * time.Second)
}

func iso(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func initialsFromRef(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	hexRef := hex.EncodeToString(sum[:])
	letters := make([]byte, 0, 2)
	for i := 0; i < len(hexRef) && len(letters) < 2; i++ {
		c := hexRef[i]
		if c >= 'a' && c <= 'f' {
			letters = append(letters, c)
		}
	}
	if len(letters) < 2 {
		letters = []byte(hexRef[:2])
	}
	return strings.ToUpper(string(letters))
}

func actorKindOf(principal auth.Principal) ActorKind {
	if principal.Kind() != auth.PrincipalSession {
		return ActorAgent
	}
	return ActorHuman
}

func principalMaterial(principal auth.Principal) string {
	if principal.Kind() == auth.PrincipalFlowProjection {
		return fmt.Sprintf("flow_projection:%d", principal.APIKeyID())
	}
	if principal.Kind() == auth.PrincipalAPIKey {
		return fmt.Sprintf("api:%d", principal.APIKeyID())
	}
	return "sess:" + principal.SessionCredentialID()
}

func projectLabel(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Project"
	}
	runes := []rune(name)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return name
}

func canStartAsHuman(principal auth.Principal) bool {
	return principal.Kind() == auth.PrincipalSession && !principal.Impersonated()
}
