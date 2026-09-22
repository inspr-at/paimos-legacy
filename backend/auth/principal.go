// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/models"
)

type PrincipalKind string

const (
	PrincipalFlowProjection      PrincipalKind = "flow_projection"
	PrincipalSession             PrincipalKind = "session"
	PrincipalAPIKey              PrincipalKind = "api_key"
	PrincipalMachineNotifier     PrincipalKind = "machine_notifier"
	PrincipalConversationService PrincipalKind = "conversation_service"
)

// Principal is the safe credential identity authenticated for one request.
// Its fields stay private so callers cannot manufacture or mutate an identity
// after authentication. A session credential is the immutable random UUID on
// the session row, never the bearer cookie; API keys use their numeric row ID.
// actorUserID names the real account behind a session, while userID names the
// effective account (they differ only during impersonation).
//
// Scope maps are copied both into and out of context so callers cannot mutate
// the request's authenticated value through a shared map reference.
type Principal struct {
	kind                PrincipalKind
	sessionCredentialID string
	apiKeyID            int64
	actorUserID         int64
	userID              int64
	impersonated        bool
	scopes              ScopeSet
	flowProjectID       int64
}

type principalKeyType struct{}

var principalKey = principalKeyType{}

var (
	ErrCredentialUnavailable = errors.New("authenticated credential is unavailable")
	ErrPrincipalChanged      = errors.New("authenticated principal changed")
)

func cloneScopeSet(scopes ScopeSet) ScopeSet {
	if scopes == nil {
		return nil
	}
	clone := make(ScopeSet, len(scopes))
	for scope := range scopes {
		clone[scope] = struct{}{}
	}
	return clone
}

func clonePrincipal(principal Principal) Principal {
	principal.scopes = cloneScopeSet(principal.scopes)
	return principal
}

func NewSessionPrincipal(credentialID string, actorUserID, userID int64, impersonated bool) (Principal, error) {
	if !validSessionCredentialID(credentialID) || actorUserID <= 0 || userID <= 0 || impersonated != (actorUserID != userID) {
		return Principal{}, ErrCredentialUnavailable
	}
	return Principal{
		kind:                PrincipalSession,
		sessionCredentialID: credentialID,
		actorUserID:         actorUserID,
		userID:              userID,
		impersonated:        impersonated,
		scopes:              ScopeSet{ScopeAll: {}},
	}, nil
}

func NewAPIKeyPrincipal(keyID, userID int64, scopes ScopeSet) (Principal, error) {
	return newAPIKeyPrincipal(PrincipalAPIKey, keyID, userID, scopes)
}

// NewConversationServicePrincipal reconstructs the safe identity of a
// persisted conversation-service credential. It never accepts bearer bytes.
func NewConversationServicePrincipal(keyID, userID int64) (Principal, error) {
	return newAPIKeyPrincipal(PrincipalConversationService, keyID, userID, ScopeSet{})
}

func newAPIKeyPrincipal(kind PrincipalKind, keyID, userID int64, scopes ScopeSet) (Principal, error) {
	if keyID <= 0 || userID <= 0 {
		return Principal{}, ErrCredentialUnavailable
	}
	if kind != PrincipalAPIKey && kind != PrincipalMachineNotifier && kind != PrincipalConversationService {
		return Principal{}, ErrCredentialUnavailable
	}
	return Principal{
		kind:        kind,
		apiKeyID:    keyID,
		actorUserID: userID,
		userID:      userID,
		scopes:      cloneScopeSet(scopes),
	}, nil
}

func (principal Principal) Kind() PrincipalKind { return principal.kind }

func (principal Principal) SessionCredentialID() string { return principal.sessionCredentialID }

func (principal Principal) APIKeyID() int64 { return principal.apiKeyID }

func (principal Principal) ActorUserID() int64 { return principal.actorUserID }

func (principal Principal) UserID() int64 { return principal.userID }

func (principal Principal) Impersonated() bool { return principal.impersonated }

func (principal Principal) Scopes() ScopeSet { return cloneScopeSet(principal.scopes) }

func (principal Principal) HasScope(scope string) bool { return principal.scopes.Has(scope) }

// SafeCredentialID is suitable for audit attribution and is never a bearer
// value. Kind disambiguates the two representations.
func (principal Principal) SafeCredentialID() string {
	if !principal.valid() {
		return ""
	}
	if principal.kind == PrincipalSession {
		return principal.sessionCredentialID
	}
	if principal.kind == PrincipalFlowProjection || principal.kind == PrincipalAPIKey || principal.kind == PrincipalMachineNotifier || principal.kind == PrincipalConversationService {
		return strconv.FormatInt(principal.apiKeyID, 10)
	}
	return ""
}

// WithPrincipal stores an isolated principal value in a context. Middleware
// is the production caller; the exported seam also lets domain tests construct
// an authenticated request without manufacturing bearer credentials.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	if ctx == nil || !principal.valid() {
		return ctx
	}
	return context.WithValue(ctx, principalKey, clonePrincipal(principal))
}

// PrincipalFromContext returns a copy of the authenticated principal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(principalKey).(Principal)
	if !ok || !principal.valid() {
		return Principal{}, false
	}
	return clonePrincipal(principal), true
}

// GetPrincipal is the request-oriented form of PrincipalFromContext.
func GetPrincipal(r *http.Request) (Principal, bool) {
	if r == nil {
		return Principal{}, false
	}
	return PrincipalFromContext(r.Context())
}

// ReauthorizePrincipalTx re-resolves the exact safe credential identity in
// the caller's transaction. It never trusts request access caches. The returned
// scopes are the current API-key scopes, so a scope removed after middleware
// ran cannot authorize a later control boundary.
func ReauthorizePrincipalTx(ctx context.Context, tx *sql.Tx, principal Principal, now time.Time) (*models.User, Principal, error) {
	if tx == nil {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	if !principal.valid() {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	switch principal.kind {
	case PrincipalFlowProjection:
		return reauthorizeFlowProjectionTx(ctx, tx, principal, now)
	case PrincipalSession:
		return reauthorizeSessionPrincipalTx(ctx, tx, principal, now)
	case PrincipalAPIKey, PrincipalMachineNotifier, PrincipalConversationService:
		return reauthorizeAPIKeyPrincipalTx(ctx, tx, principal, now)
	default:
		return nil, Principal{}, ErrCredentialUnavailable
	}
}

// ReauthorizeRequestPrincipalTx combines request-context extraction with exact
// transaction re-resolution.
func ReauthorizeRequestPrincipalTx(ctx context.Context, tx *sql.Tx, r *http.Request, now time.Time) (*models.User, Principal, error) {
	principal, ok := GetPrincipal(r)
	if !ok {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	return ReauthorizePrincipalTx(ctx, tx, principal, now)
}

// ReauthorizeRuntimeReporterTx verifies the current credential behind a
// persisted runtime owner. A future runtime lease is not authority by itself:
// the exact API key must still belong to an active super-admin, retain the
// runner scope, and name an active project in this same transaction.
func ReauthorizeRuntimeReporterTx(ctx context.Context, tx *sql.Tx, userID, apiKeyID, projectID int64, now time.Time) (*models.User, Principal, error) {
	expected, err := NewAPIKeyPrincipal(apiKeyID, userID, ParseScopes("*"))
	if err != nil {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	user, current, err := ReauthorizePrincipalTx(ctx, tx, expected, now)
	if err != nil || user == nil || current.Kind() != PrincipalAPIKey || current.Impersonated() ||
		!IsSuperAdmin(user) || !current.HasScope(ScopeAgentControlsRunner) {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=? AND status='active'`, projectID).Scan(&active); err != nil {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	return user, current, nil
}

func reauthorizeSessionPrincipalTx(ctx context.Context, tx *sql.Tx, expected Principal, now time.Time) (*models.User, Principal, error) {
	if expected.kind != PrincipalSession || !validSessionCredentialID(expected.sessionCredentialID) || expected.apiKeyID != 0 {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	effective := &models.User{}
	actor := &models.User{}
	var impersonating int
	var bearerID string
	var expiresAt, createdAt string
	dests := []any{&bearerID, &impersonating, &expiresAt, &createdAt}
	dests = append(dests, userScanDests(effective)...)
	dests = append(dests, userScanDests(actor)...)
	// #nosec G202 -- both column lists are fixed package constants.
	err := tx.QueryRowContext(ctx, `
		SELECT s.id,CASE WHEN s.acting_as_user_id IS NOT NULL THEN 1 ELSE 0 END,
		       s.expires_at,s.created_at,
		       `+userSelectCols+`, `+userSelectColsFor("actor")+`
		FROM sessions s
		JOIN users actor ON actor.id=COALESCE(s.actor_user_id,s.user_id)
		JOIN users u ON u.id=COALESCE(s.acting_as_user_id,s.user_id)
		WHERE s.credential_id=?
	`, expected.sessionCredentialID).Scan(dests...)
	if err != nil || bearerID == expected.sessionCredentialID || actor.Status != "active" || effective.Status != "active" {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	expires, err := parseCredentialTimestamp(expiresAt)
	if err != nil || !expires.After(now) {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	created, err := parseCredentialTimestamp(createdAt)
	if err != nil || now.Before(created) || now.Sub(created) >= sessionAbsoluteLifetime {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	current, err := NewSessionPrincipal(expected.sessionCredentialID, actor.ID, effective.ID, impersonating != 0)
	if err != nil {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	if current.impersonated && !IsSuperAdmin(actor) {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	if !samePrincipalIdentity(expected, current) {
		return nil, Principal{}, ErrPrincipalChanged
	}
	return effective, clonePrincipal(current), nil
}

func reauthorizeAPIKeyPrincipalTx(ctx context.Context, tx *sql.Tx, expected Principal, now time.Time) (*models.User, Principal, error) {
	if (expected.kind != PrincipalAPIKey && expected.kind != PrincipalMachineNotifier && expected.kind != PrincipalConversationService) || expected.apiKeyID <= 0 || expected.sessionCredentialID != "" {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	user := &models.User{}
	var scopesCSV, credentialKind string
	var disabledAt, expiresAt sql.NullString
	dests := append([]any{&scopesCSV, &credentialKind, &disabledAt, &expiresAt}, userScanDests(user)...)
	// #nosec G202 -- userSelectCols is a fixed package constant.
	err := tx.QueryRowContext(ctx, `
		SELECT ak.scopes,CASE
		 WHEN ak.credential_kind='general' AND EXISTS(
		  SELECT 1 FROM conversation_service_bindings binding WHERE binding.api_key_id=ak.id
		 ) THEN 'conversation_service' ELSE ak.credential_kind END,
		 ak.disabled_at,ak.expires_at,`+userSelectCols+`
		FROM api_keys ak JOIN users u ON u.id=ak.user_id
		WHERE ak.id=?
	`, expected.apiKeyID).Scan(dests...)
	if err != nil || user.Status != "active" || disabledAt.Valid {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	if expiresAt.Valid {
		expires, parseErr := parseCredentialTimestamp(expiresAt.String)
		if parseErr != nil || !expires.After(now) {
			return nil, Principal{}, ErrCredentialUnavailable
		}
	}
	kind, ok := principalKindForCredential(credentialKind)
	if !ok {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	current, err := newAPIKeyPrincipal(kind, expected.apiKeyID, user.ID, ParseScopes(scopesCSV))
	if err != nil {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	if !samePrincipalIdentity(expected, current) {
		return nil, Principal{}, ErrPrincipalChanged
	}
	return user, clonePrincipal(current), nil
}

func samePrincipalIdentity(left, right Principal) bool {
	return left.kind == right.kind &&
		left.sessionCredentialID == right.sessionCredentialID &&
		left.apiKeyID == right.apiKeyID &&
		left.actorUserID == right.actorUserID &&
		left.userID == right.userID &&
		left.impersonated == right.impersonated && left.flowProjectID == right.flowProjectID
}

func (principal Principal) valid() bool {
	switch principal.kind {
	case PrincipalFlowProjection:
		return principal.sessionCredentialID == "" && principal.apiKeyID > 0 && principal.userID > 0 && principal.actorUserID == principal.userID && !principal.impersonated && principal.flowProjectID > 0 && len(principal.scopes) == 0
	case PrincipalSession:
		return validSessionCredentialID(principal.sessionCredentialID) && principal.apiKeyID == 0 &&
			principal.actorUserID > 0 && principal.userID > 0 &&
			(principal.impersonated || principal.actorUserID == principal.userID) &&
			(!principal.impersonated || principal.actorUserID != principal.userID) &&
			principal.scopes.Has(ScopeAll)
	case PrincipalAPIKey, PrincipalMachineNotifier, PrincipalConversationService:
		return principal.sessionCredentialID == "" && principal.apiKeyID > 0 &&
			principal.actorUserID > 0 && principal.userID == principal.actorUserID &&
			!principal.impersonated
	default:
		return false
	}
}

func validSessionCredentialID(value string) bool {
	if len(value) != 36 || strings.ToLower(value) != value ||
		value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' ||
		value[14] != '4' || !strings.ContainsRune("89ab", rune(value[19])) {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func principalForAPIKey(credentialKind string, keyID int64, userID int64, scopes ScopeSet) (Principal, error) {
	kind, ok := principalKindForCredential(credentialKind)
	if !ok {
		return Principal{}, fmt.Errorf("invalid api-key principal")
	}
	principal, err := newAPIKeyPrincipal(kind, keyID, userID, scopes)
	if err != nil {
		return Principal{}, fmt.Errorf("invalid api-key principal")
	}
	return principal, nil
}

func principalKindForCredential(credentialKind string) (PrincipalKind, bool) {
	switch credentialKind {
	case "general":
		return PrincipalAPIKey, true
	case "machine_notifier":
		return PrincipalMachineNotifier, true
	case "conversation_service":
		return PrincipalConversationService, true
	default:
		return "", false
	}
}

func parseCredentialTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid credential timestamp")
}
