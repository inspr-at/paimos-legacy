// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	apiKeyUsageStampTimeout       = 300 * time.Millisecond
	apiKeyUsageStampBusyTimeoutMS = 100
)

// ResolveAPIKey looks up a raw API key (full "paimos_..." string), verifies it
// against the stored hash, updates last_used_at, and returns the owning user
// alongside the key's scope set. PAI-379: the scopes column (M104) is parsed
// here so the auth middleware can stash a ScopeSet on the request context
// without a second query.
func ResolveAPIKey(rawKey string) (*models.User, ScopeSet, error) {
	user, principal, err := ResolveAPIKeyPrincipal(rawKey)
	if err != nil {
		return nil, nil, err
	}
	return user, principal.Scopes(), nil
}

// ResolveAPIKeyPrincipal preserves the safe immutable API-key row identity for
// request attribution. Disabled, expired, deleted, and disabled-owner keys are
// rejected before a principal is created.
func ResolveAPIKeyPrincipal(rawKey string) (*models.User, Principal, error) {
	return resolveAPIKeyPrincipalAt(rawKey, time.Now().UTC())
}

func resolveAPIKeyPrincipalAt(rawKey string, now time.Time) (*models.User, Principal, error) {
	if strings.HasPrefix(rawKey, FlowProjectionKeyPrefix()) {
		return resolveFlowProjection(rawKey, now)
	}
	sum := sha256.Sum256([]byte(rawKey))
	hash := hex.EncodeToString(sum[:])

	var keyID int64
	var scopesCSV, credentialKind string
	var disabledAt, expiresAt, lastUsedAt sql.NullString
	u := &models.User{}
	// Scan order: key id + scopes column + the user-cols list.
	dests := append([]any{&keyID, &scopesCSV, &credentialKind, &disabledAt, &expiresAt, &lastUsedAt}, userScanDests(u)...)
	err := db.DB.QueryRow(`
		SELECT ak.id,ak.scopes,CASE
		 WHEN ak.credential_kind='general' AND EXISTS(
		  SELECT 1 FROM conversation_service_bindings binding WHERE binding.api_key_id=ak.id
		 ) THEN 'conversation_service' ELSE ak.credential_kind END,
		 ak.disabled_at,ak.expires_at,ak.last_used_at,`+userSelectCols+`
		FROM api_keys ak JOIN users u ON u.id = ak.user_id
		WHERE ak.key_hash = ?
	`, hash).Scan(dests...)
	if err != nil {
		return nil, Principal{}, fmt.Errorf("invalid api key")
	}
	if u.Status != "active" || disabledAt.Valid {
		return nil, Principal{}, fmt.Errorf("account disabled")
	}
	if expiresAt.Valid {
		expires, parseErr := parseCredentialTimestamp(expiresAt.String)
		if parseErr != nil || !expires.After(now) {
			return nil, Principal{}, fmt.Errorf("invalid api key")
		}
	}

	// Best-effort, write-throttled usage stamp. Authentication must not inherit
	// SQLite's five-second busy timeout when maintenance owns the writer slot.
	lastUsed, lastUsedErr := parseCredentialTimestamp(lastUsedAt.String)
	if !lastUsedAt.Valid || lastUsedErr != nil || lastUsed.Before(now.Add(-time.Hour)) {
		stampAPIKeyUsage(keyID)
	}

	principal, err := principalForAPIKey(credentialKind, keyID, u.ID, ParseScopes(scopesCSV))
	if err != nil {
		return nil, Principal{}, fmt.Errorf("invalid api key")
	}
	return u, principal, nil
}

func stampAPIKeyUsage(keyID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), apiKeyUsageStampTimeout)
	defer cancel()
	conn, err := db.DB.Conn(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", apiKeyUsageStampBusyTimeoutMS)); err != nil {
		return
	}
	_, usageErr := conn.ExecContext(ctx, `UPDATE api_keys SET last_used_at=datetime('now')
		WHERE id=? AND (last_used_at IS NULL OR last_used_at<datetime('now','-1 hour'))`, keyID)
	// Restore the pool-wide default before returning this connection. PRAGMA
	// busy_timeout does not acquire the database writer lock.
	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), time.Second)
	_, restoreErr := conn.ExecContext(restoreCtx, fmt.Sprintf("PRAGMA busy_timeout=%d", db.DefaultBusyTimeoutMS))
	restoreCancel()
	if restoreErr != nil {
		// Never return a connection with a shortened policy to the shared pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		log.Printf("ResolveAPIKey: restore usage-stamp connection policy: %v", restoreErr)
	}
	if usageErr != nil && ctx.Err() == nil && !strings.Contains(usageErr.Error(), "SQLITE_BUSY") &&
		!strings.Contains(usageErr.Error(), "database is locked") {
		log.Printf("ResolveAPIKey: update last_used_at key_id=%d: %v", keyID, usageErr)
	}
}
