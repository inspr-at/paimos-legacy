// PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only.
package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

func flowCredentialOperator(r *http.Request, tx *sql.Tx, now time.Time) bool {
	user, principal, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, now)
	return err == nil && auth.IsAdmin(user) && !principal.Impersonated() &&
		(principal.Kind() == auth.PrincipalSession || principal.Kind() == auth.PrincipalAPIKey) &&
		principal.HasScope(auth.ScopeFlowCredentialsWrite)
}

// CreateFlowProjectionCredential provisions a server-held read credential;
// it does not create a user, grant a project, or change browser permissions.
func CreateFlowProjectionCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	var body struct {
		Name           string `json:"name"`
		ReviewerUserID int64  `json:"reviewer_user_id"`
		ProjectID      int64  `json:"project_id"`
		ExpiresAt      string `json:"expires_at"`
	}
	if err := DecodeControlJSON(w, r, 8192, &body); err != nil {
		jsonError(w, "invalid Flow projection enrollment", http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	now := time.Now().UTC().Truncate(time.Millisecond)
	expires, err := time.Parse(time.RFC3339Nano, body.ExpiresAt)
	if err != nil || body.Name == "" || len([]byte(body.Name)) > 128 || body.ReviewerUserID <= 0 || body.ProjectID <= 0 ||
		!expires.After(now) || expires.After(now.Add(30*24*time.Hour)) {
		jsonError(w, "require an existing reviewer, project and expiry within 30 days", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if !flowCredentialOperator(r, tx, now) {
		jsonError(w, "current administrator with Flow credential scope required", http.StatusForbidden)
		return
	}
	var allowed int
	err = tx.QueryRowContext(r.Context(), `SELECT 1 FROM users u
	 JOIN project_members membership ON membership.user_id=u.id
	 JOIN projects project ON project.id=membership.project_id
	 WHERE u.id=? AND u.is_reviewer=1 AND u.status='active' AND u.role='external' AND u.role_key='external'
	 AND u.is_super_admin=0 AND membership.project_id=? AND membership.access_level='viewer' AND project.status='active'`,
		body.ReviewerUserID, body.ProjectID).Scan(&allowed)
	if err != nil {
		jsonError(w, "reviewer must already have explicit viewer access to this active project", http.StatusBadRequest)
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	full := auth.FlowProjectionKeyPrefix() + hex.EncodeToString(random[:])
	digest := sha256.Sum256([]byte(full))
	result, err := tx.ExecContext(r.Context(), `INSERT INTO flow_projection_credentials(user_id,project_id,name,key_hash,created_at,expires_at)
	 VALUES(?,?,?,?,?,?)`, body.ReviewerUserID, body.ProjectID, body.Name, hex.EncodeToString(digest[:]),
		now.Format(time.RFC3339Nano), expires.UTC().Format(time.RFC3339Nano))
	if err != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	id, err := result.LastInsertId()
	if err != nil || tx.Commit() != nil {
		jsonError(w, "enrollment unavailable", http.StatusInternalServerError)
		return
	}
	log.Printf("audit: flow_projection_created credential_id=%d project_id=%d reviewer_id=%d", id, body.ProjectID, body.ReviewerUserID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	// The only response ever carrying the bearer. It is not retrievable again.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": id, "project_id": body.ProjectID, "expires_at": expires.UTC().Format(time.RFC3339Nano), "key": full,
	})
}

func RevokeFlowProjectionCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid credential identifier", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "revocation unavailable", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if !flowCredentialOperator(r, tx, now) {
		jsonError(w, "current administrator with Flow credential scope required", http.StatusForbidden)
		return
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE flow_projection_credentials SET disabled_at=? WHERE id=? AND disabled_at IS NULL`, now.Format(time.RFC3339Nano), id)
	if err != nil {
		jsonError(w, "revocation unavailable", http.StatusInternalServerError)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		jsonError(w, "active credential not found", http.StatusNotFound)
		return
	}
	if tx.Commit() != nil {
		jsonError(w, "revocation unavailable", http.StatusInternalServerError)
		return
	}
	log.Printf("audit: flow_projection_revoked credential_id=%d", id)
	w.WriteHeader(http.StatusNoContent)
}
