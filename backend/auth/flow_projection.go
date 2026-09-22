// PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only.
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

// FlowProjectionKeyPrefix distinguishes this credential before general-key
// resolution. Its hash never exists in api_keys, including on older binaries.
func FlowProjectionKeyPrefix() string { return brand.Default.APIKeyPrefix + "flow_" }

type flowProjectionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveFlowProjection(raw string, now time.Time) (*models.User, Principal, error) {
	digest := sha256.Sum256([]byte(raw))
	return loadFlowProjection(context.Background(), db.DB, hex.EncodeToString(digest[:]), 0, now)
}

func loadFlowProjection(ctx context.Context, query flowProjectionQuerier, hash string, credentialID int64, now time.Time) (*models.User, Principal, error) {
	user := &models.User{}
	var id, projectID int64
	var expiry string
	destinations := append([]any{&id, &projectID, &expiry}, userScanDests(user)...)
	// The SQL and canonical user projection are fixed; both identifiers are bound.
	err := query.QueryRowContext(ctx, `SELECT credential.id,credential.project_id,credential.expires_at,`+userSelectCols+`
	 FROM flow_projection_credentials credential JOIN users u ON u.id=credential.user_id
	 JOIN projects project ON project.id=credential.project_id
	 JOIN project_members membership ON membership.user_id=u.id AND membership.project_id=project.id
	 WHERE ((?='' AND credential.id=?) OR credential.key_hash=?) AND credential.disabled_at IS NULL AND u.status='active' AND u.is_reviewer=1
	 AND u.role='external' AND u.role_key='external' AND u.is_super_admin=0
	 AND membership.access_level='viewer' AND project.status='active'`, hash, credentialID, hash).Scan(destinations...)
	if err != nil || user.Role != RoleReviewer {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	expires, err := parseCredentialTimestamp(expiry)
	if err != nil || !expires.After(now) {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	principal := Principal{kind: PrincipalFlowProjection, apiKeyID: id, actorUserID: user.ID, userID: user.ID, flowProjectID: projectID}
	return user, principal, nil
}

// NewFlowProjectionPrincipal reconstructs safe identity for transactional
// reauthorization. The caller must still call ReauthorizePrincipalTx.
func NewFlowProjectionPrincipal(id, userID, projectID int64) (Principal, error) {
	principal := Principal{kind: PrincipalFlowProjection, apiKeyID: id, actorUserID: userID, userID: userID, flowProjectID: projectID}
	if !principal.valid() {
		return Principal{}, ErrCredentialUnavailable
	}
	return principal, nil
}

func reauthorizeFlowProjectionTx(ctx context.Context, tx *sql.Tx, expected Principal, now time.Time) (*models.User, Principal, error) {
	user, current, err := loadFlowProjection(ctx, tx, "", expected.APIKeyID(), now)
	if err != nil || !samePrincipalIdentity(expected, current) {
		return nil, Principal{}, ErrCredentialUnavailable
	}
	return user, current, nil
}

func flowProjectionRouteAllowed(r *http.Request, principal Principal) bool {
	return r.Method == http.MethodGet && r.URL.RawQuery == "" &&
		r.URL.EscapedPath() == r.URL.Path &&
		r.URL.Path == "/api/projects/"+strconv.FormatInt(principal.flowProjectID, 10)+"/baseline-batches/flow-state"
}
