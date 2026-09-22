// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/externalstage"
)

func (s *Service) currentAuthority(ctx context.Context, tx *sql.Tx, actor Actor, projectID int64, write bool) (ProjectAuthority, error) {
	if projectID <= 0 {
		return ProjectAuthority{}, fmt.Errorf("%w: project", ErrInvalid)
	}
	sessionActor := actor.Kind == string(auth.PrincipalSession) && actor.UserID > 0 && actor.SessionCredentialID != "" && !actor.Impersonated && actor.APIKeyID == 0
	apiKeyActor := actor.Kind == string(auth.PrincipalAPIKey) && actor.UserID > 0 && actor.APIKeyID > 0 && actor.SessionCredentialID == "" && !actor.Impersonated
	if write && !sessionActor && !apiKeyActor {
		return ProjectAuthority{}, fmt.Errorf("%w: current editor session or scoped api key required", ErrForbidden)
	}
	if actor.UserID <= 0 {
		return ProjectAuthority{}, fmt.Errorf("%w: authenticated actor required", ErrUnauthorized)
	}
	if actor.Kind == string(auth.PrincipalFlowProjection) {
		if write || actor.SessionCredentialID != "" || actor.Impersonated {
			return ProjectAuthority{}, ErrForbidden
		}
		expected, err := auth.NewFlowProjectionPrincipal(actor.APIKeyID, actor.UserID, projectID)
		if err != nil {
			return ProjectAuthority{}, ErrForbidden
		}
		if _, _, err := auth.ReauthorizePrincipalTx(ctx, tx, expected, time.Now().UTC()); err != nil {
			return ProjectAuthority{}, ErrForbidden
		}
	}
	if sessionActor {
		if err := requireLiveSession(ctx, tx, actor); err != nil {
			return ProjectAuthority{}, err
		}
	}
	if apiKeyActor {
		if err := requireLiveAPIKey(ctx, tx, actor, write); err != nil {
			return ProjectAuthority{}, err
		}
	}
	var status string
	var userStatus, userRole string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM projects WHERE id=?`, projectID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return ProjectAuthority{}, fmt.Errorf("%w: project", ErrNotFound)
		}
		return ProjectAuthority{}, err
	}
	if status == "deleted" {
		return ProjectAuthority{}, fmt.Errorf("%w: project", ErrNotFound)
	}
	if err := tx.QueryRowContext(ctx, `SELECT status,role FROM users WHERE id=?`, actor.UserID).Scan(&userStatus, &userRole); err != nil {
		return ProjectAuthority{}, fmt.Errorf("%w: actor", ErrUnauthorized)
	}
	if userStatus != "active" {
		return ProjectAuthority{}, fmt.Errorf("%w: actor", ErrUnauthorized)
	}
	authz := ProjectAuthority{ProjectID: projectID, Status: status}
	isAdmin := userRole == "admin" || userRole == "super_admin"
	if isAdmin {
		authz.CanView, authz.CanEdit = true, true
	} else {
		var level sql.NullString
		_ = tx.QueryRowContext(ctx, `SELECT access_level FROM project_members WHERE user_id=? AND project_id=?`, actor.UserID, projectID).Scan(&level)
		switch {
		case userRole == "external":
			if level.String == "viewer" {
				authz.CanView = true
			}
			if level.String == "editor" {
				authz.CanView, authz.CanEdit = true, true
			}
		case userRole == "member":
			if !level.Valid || level.String == "editor" {
				authz.CanView, authz.CanEdit = true, true
			} else if level.String == "viewer" {
				authz.CanView = true
			}
		}
	}
	if !authz.CanView {
		return ProjectAuthority{}, fmt.Errorf("%w: project", ErrNotFound)
	}
	if write && !authz.CanEdit {
		return ProjectAuthority{}, fmt.Errorf("%w: edit", ErrForbidden)
	}
	if write && status != "active" {
		return ProjectAuthority{}, fmt.Errorf("%w: project is not accepting new delivery work", ErrStale)
	}
	return authz, nil
}

// principal rebuilds the acting human's session principal for the authorities
// this service composes with. It grants nothing: every one of them
// reauthorizes the credential against the database before acting on it.
func (a Actor) principal() (auth.Principal, error) {
	if err := requireHuman(a); err != nil {
		return auth.Principal{}, err
	}
	p, err := auth.NewSessionPrincipal(a.SessionCredentialID, a.UserID, a.UserID, false)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: current human session required", ErrForbidden)
	}
	return p, nil
}

func (a Actor) externalPrincipal() (externalstage.Principal, error) {
	switch a.Kind {
	case string(auth.PrincipalSession):
		if err := requireHuman(a); err != nil {
			return externalstage.Principal{}, err
		}
		return externalstage.Principal{UserID: a.UserID, Kind: "session", SessionCredentialID: a.SessionCredentialID}, nil
	case string(auth.PrincipalAPIKey):
		if a.UserID <= 0 || a.APIKeyID <= 0 || a.SessionCredentialID != "" || a.Impersonated {
			return externalstage.Principal{}, fmt.Errorf("%w: current scoped api key required", ErrForbidden)
		}
		return externalstage.Principal{UserID: a.UserID, Kind: "api_key", APIKeyID: a.APIKeyID}, nil
	default:
		return externalstage.Principal{}, fmt.Errorf("%w: current editor session or scoped api key required", ErrForbidden)
	}
}

func requireHuman(actor Actor) error {
	if actor.Kind != string(auth.PrincipalSession) || actor.UserID <= 0 || actor.SessionCredentialID == "" || actor.Impersonated || actor.APIKeyID != 0 {
		return fmt.Errorf("%w: current human session required", ErrForbidden)
	}
	return nil
}

func requireLiveSession(ctx context.Context, tx *sql.Tx, actor Actor) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions
		WHERE credential_id=? AND COALESCE(acting_as_user_id,user_id)=? AND expires_at>datetime('now')`,
		actor.SessionCredentialID, actor.UserID).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: current human session required", ErrForbidden)
	}
	return nil
}

func requireLiveAPIKey(ctx context.Context, tx *sql.Tx, actor Actor, write bool) error {
	query := `SELECT COUNT(*) FROM api_keys
		WHERE id=? AND user_id=? AND disabled_at IS NULL
		 AND (expires_at IS NULL OR julianday(expires_at)>julianday('now'))`
	if write {
		query += ` AND (scopes='*' OR (','||replace(scopes,' ','')||',') LIKE '%,agent-controls:write,%')`
	}
	var n int
	err := tx.QueryRowContext(ctx, query, actor.APIKeyID, actor.UserID).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: current scoped api key required", ErrForbidden)
	}
	return nil
}
