// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package releaseacceptance

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/inspr-at/paimos/backend/externalstage"
	"github.com/inspr-at/paimos/backend/targetidentity"
	"strings"
)

const (
	TargetKindPharosOwner   = "pharos_owner"
	TargetKindProjectEnv    = "project_environment"
	targetUnknownUnbound    = "deployment target is unknown; standing policy cannot apply until an explicit deployment target is bound"
	targetUnknownNoBinding  = "deployment target is unknown until a human selects an exact Pharos owner registration or project environment for this release"
	targetUnknownRevoked    = "bound Pharos owner registration is revoked or missing; standing policy cannot fall back to a project environment"
	targetUnknownGeneration = "bound delivery or attempt generation no longer matches this release"
	targetUnknownChanged    = "bound project environment identity changed or is missing"
	targetUnknownProvenance = "bound deployment target provenance no longer matches"

	livePharosIdentitySQL = `SELECT registration.delivery_id, COALESCE(registration.workflow_symbol,''), COALESCE(registration.environment_symbol,''),
		registration.created_at, registration.api_key_id, registration.user_id, registration.reporter_id, registration.allow_deployment
		FROM external_stage_reporter_registrations registration
		WHERE registration.id=? AND registration.project_id=? AND ` + externalstage.LivePharosOwnerSQL

	livePharosCandidatesSQL = `SELECT registration.id, COALESCE(registration.workflow_symbol,''), COALESCE(registration.environment_symbol,'')
		FROM external_stage_reporter_registrations registration
		WHERE registration.delivery_id=? AND registration.project_id=? AND ` + externalstage.LivePharosOwnerSQL + `
		ORDER BY registration.id`
)

type TargetCandidate struct {
	Kind              string `json:"kind"`
	Label             string `json:"label"`
	RegistrationID    int64  `json:"registration_id,omitempty"`
	EnvironmentID     int64  `json:"environment_id,omitempty"`
	EnvironmentSymbol string `json:"environment_symbol,omitempty"`
	WorkflowSymbol    string `json:"workflow_symbol,omitempty"`
}

type BindTargetRequest struct {
	Kind           string `json:"kind"`
	RegistrationID int64  `json:"registration_id,omitempty"`
	EnvironmentID  int64  `json:"environment_id,omitempty"`
}

type targetIdentity = targetidentity.Identity

type storedBinding struct {
	Kind           string
	TargetRef      string
	RegistrationID sql.NullInt64
	EnvironmentID  sql.NullInt64
}

func targetDigest(id targetIdentity) string {
	return targetidentity.Digest(id)
}

func isTargetDigest(v string) bool {
	return targetidentity.IsDigest(v)
}

func (s *Service) BindTarget(ctx context.Context, actor Actor, projectID, releaseID int64, req BindTargetRequest) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Acceptance{}, err
	}
	rel, err := loadRelease(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	identity, err := selectedTargetIdentity(ctx, tx, rel, req)
	if err != nil {
		return Acceptance{}, err
	}
	digest := targetDigest(identity)
	now := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO acceptance_target_bindings(
		project_id,release_id,kind,target_ref,registration_id,environment_id,delivery_id,attempt_id,
		environment_symbol,workflow_symbol,source_created_at,bound_by,session_credential_id,bound_at,binding_revision)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)
		ON CONFLICT(release_id) DO UPDATE SET
			kind=excluded.kind, target_ref=excluded.target_ref, registration_id=excluded.registration_id,
			environment_id=excluded.environment_id, delivery_id=excluded.delivery_id, attempt_id=excluded.attempt_id,
			environment_symbol=excluded.environment_symbol, workflow_symbol=excluded.workflow_symbol,
			source_created_at=excluded.source_created_at, bound_by=excluded.bound_by,
			session_credential_id=excluded.session_credential_id, bound_at=excluded.bound_at,
			binding_revision=acceptance_target_bindings.binding_revision+1`,
		projectID, releaseID, identity.Kind, digest, nullIfZero(identity.RegistrationID), nullIfZero(identity.EnvironmentID),
		nullIfZero(identity.DeliveryID), nullIfZero(identity.AttemptID), identity.EnvironmentSymbol, identity.WorkflowSymbol,
		identity.CreatedAt, actor.UserID, actor.SessionCredentialID, now); err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return s.Get(ctx, actor, projectID, releaseID)
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func selectedTargetIdentity(ctx context.Context, tx *sql.Tx, rel ReleaseRecord, req BindTargetRequest) (targetIdentity, error) {
	switch strings.TrimSpace(req.Kind) {
	case TargetKindPharosOwner:
		if req.RegistrationID <= 0 {
			return targetIdentity{}, fmt.Errorf("%w: pharos registration_id is required", ErrInvalid)
		}
		return loadPharosIdentity(ctx, tx, rel, req.RegistrationID)
	case TargetKindProjectEnv:
		if req.EnvironmentID <= 0 {
			return targetIdentity{}, fmt.Errorf("%w: project environment_id is required", ErrInvalid)
		}
		return loadProjectEnvIdentity(ctx, tx, rel.ProjectID, req.EnvironmentID)
	default:
		return targetIdentity{}, fmt.Errorf("%w: deployment target kind", ErrInvalid)
	}
}

func loadPharosIdentity(ctx context.Context, tx *sql.Tx, rel ReleaseRecord, registrationID int64) (targetIdentity, error) {
	deliveryID, attemptID, err := batchDelivery(ctx, tx, rel.ProjectID, rel.BatchID)
	if err != nil {
		return targetIdentity{}, err
	}
	if deliveryID == 0 {
		return targetIdentity{}, fmt.Errorf("%w: batch has no delivery for a Pharos target", ErrInvalid)
	}
	var workflow, env, created string
	var regDelivery, apiKeyID, userID, reporterID, allowDeployment int64
	err = tx.QueryRowContext(ctx, livePharosIdentitySQL,
		registrationID, rel.ProjectID).Scan(&regDelivery, &workflow, &env, &created, &apiKeyID, &userID, &reporterID, &allowDeployment)
	if err == sql.ErrNoRows {
		return targetIdentity{}, fmt.Errorf("%w: pharos owner registration", ErrInvalid)
	}
	if err != nil {
		return targetIdentity{}, err
	}
	if regDelivery != deliveryID {
		return targetIdentity{}, fmt.Errorf("%w: pharos registration is not on this release delivery", ErrInvalid)
	}
	if !validOpaqueRef(env) {
		return targetIdentity{}, fmt.Errorf("%w: pharos environment", ErrInvalid)
	}
	return targetIdentity{
		Kind: TargetKindPharosOwner, ProjectID: rel.ProjectID, RegistrationID: registrationID,
		DeliveryID: deliveryID, AttemptID: attemptID, WorkflowSymbol: workflow,
		EnvironmentSymbol: env, CreatedAt: created, APIKeyID: apiKeyID, UserID: userID,
		ReporterID: reporterID, AllowDeployment: allowDeployment,
	}, nil
}

func loadProjectEnvIdentity(ctx context.Context, tx *sql.Tx, projectID, environmentID int64) (targetIdentity, error) {
	identity, err := targetidentity.LoadProjectEnvironment(ctx, tx, projectID, environmentID)
	if err == sql.ErrNoRows {
		return targetIdentity{}, fmt.Errorf("%w: project environment", ErrInvalid)
	}
	if err != nil {
		return targetIdentity{}, fmt.Errorf("%w: project environment identity", ErrInvalid)
	}
	return identity, nil
}

func batchDelivery(ctx context.Context, tx *sql.Tx, projectID, batchID int64) (deliveryID, attemptID int64, err error) {
	var d, a sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT delivery_id, attempt_id FROM baseline_batch_batches WHERE id=? AND project_id=?`,
		batchID, projectID).Scan(&d, &a)
	if err == sql.ErrNoRows {
		return 0, 0, fmt.Errorf("%w: batch", ErrNotFound)
	}
	if err != nil {
		return 0, 0, err
	}
	return d.Int64, a.Int64, nil
}

func (s *Service) annotateDeploymentTarget(ctx context.Context, tx *sql.Tx, acc *Acceptance) {
	acc.TargetCandidates = listTargetCandidates(ctx, tx, acc.Release)
	live, reason := verifyReleaseTarget(ctx, tx, acc.Release)
	acc.DeploymentTarget = live.Ref
	acc.DeploymentTargetKind = live.Kind
	acc.DeploymentTargetLabel = live.Label
	if live.Ref == "" {
		acc.TargetUnknownReason = reason
	}
}

func bindStandingPolicyScope(ctx context.Context, tx *sql.Tx, projectID int64, req PolicyRequest) (targetRef, modelRef string, err error) {
	requestedModel := strings.TrimSpace(req.ModelRef)
	if requestedModel != "" && requestedModel != ModeCustomerOperated {
		return "", "", fmt.Errorf("%w: model_ref must be the customer-operated arrangement", ErrInvalid)
	}
	requestedTarget := strings.TrimSpace(req.TargetRef)
	if !isTargetDigest(requestedTarget) {
		return "", "", fmt.Errorf("%w: explicit bound deployment target is required", ErrInvalid)
	}
	live, err := validBaselineTargetRefs(ctx, tx, projectID, req.ContentDigest, req.RevisionSeal)
	if err != nil {
		return "", "", err
	}
	if !contains(live, requestedTarget) {
		return "", "", fmt.Errorf("%w: target_ref is not a currently bound deployment target for this baseline", ErrInvalid)
	}
	return requestedTarget, ModeCustomerOperated, nil
}

func validBaselineTargetRefs(ctx context.Context, tx *sql.Tx, projectID int64, contentDigest, revisionSeal string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.id, r.project_id, r.batch_id, r.release_ref, r.batch_key, r.baseline_ref,
		r.content_digest, r.revision_seal, r.artifact_digest, r.artifact_coordinate, r.version_scheme, r.release_channel,
		r.release_sequence, r.version, r.commit_sha, r.state, r.revision, r.created_at
		FROM release_records r
		WHERE r.project_id=? AND r.content_digest=? AND r.revision_seal=?`, projectID, contentDigest, revisionSeal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var releases []ReleaseRecord
	for rows.Next() {
		var rel ReleaseRecord
		if err := rows.Scan(&rel.ID, &rel.ProjectID, &rel.BatchID, &rel.ReleaseRef, &rel.BatchKey, &rel.BaselineRef,
			&rel.ContentDigest, &rel.RevisionSeal, &rel.ArtifactDigest, &rel.ArtifactCoordinate, &rel.VersionScheme,
			&rel.ReleaseChannel, &rel.ReleaseSequence, &rel.Version, &rel.CommitSHA, &rel.State, &rel.Revision, &rel.CreatedAt); err != nil {
			return nil, err
		}
		releases = append(releases, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var refs []string
	for _, rel := range releases {
		live, _ := verifyReleaseTarget(ctx, tx, rel)
		if live.Ref != "" {
			refs = append(refs, live.Ref)
		}
	}
	return refs, nil
}

type liveTarget struct {
	Ref   string
	Kind  string
	Label string
}

func verifyReleaseTarget(ctx context.Context, tx *sql.Tx, rel ReleaseRecord) (liveTarget, string) {
	var b storedBinding
	err := tx.QueryRowContext(ctx, `SELECT kind, target_ref, registration_id, environment_id
		FROM acceptance_target_bindings WHERE release_id=? AND project_id=?`, rel.ID, rel.ProjectID).
		Scan(&b.Kind, &b.TargetRef, &b.RegistrationID, &b.EnvironmentID)
	if err == sql.ErrNoRows {
		return liveTarget{}, targetUnknownNoBinding
	}
	if err != nil {
		return liveTarget{}, targetUnknownNoBinding
	}
	switch b.Kind {
	case TargetKindPharosOwner:
		if !b.RegistrationID.Valid {
			return liveTarget{}, targetUnknownRevoked
		}
		got, err := loadPharosIdentity(ctx, tx, rel, b.RegistrationID.Int64)
		if err != nil {
			return liveTarget{}, targetUnknownRevoked
		}
		if targetDigest(got) != b.TargetRef {
			return liveTarget{}, targetUnknownGeneration
		}
		return liveTarget{Ref: b.TargetRef, Kind: got.Kind, Label: pharosLabel(got)}, ""
	case TargetKindProjectEnv:
		if !b.EnvironmentID.Valid {
			return liveTarget{}, targetUnknownChanged
		}
		got, err := loadProjectEnvIdentity(ctx, tx, rel.ProjectID, b.EnvironmentID.Int64)
		if err != nil {
			return liveTarget{}, targetUnknownChanged
		}
		if targetDigest(got) != b.TargetRef {
			return liveTarget{}, targetUnknownChanged
		}
		return liveTarget{Ref: b.TargetRef, Kind: got.Kind, Label: projectEnvLabel(got)}, ""
	default:
		return liveTarget{}, targetUnknownProvenance
	}
}

func pharosLabel(id targetIdentity) string {
	return fmt.Sprintf("Pharos %s / %s (registration %d)", id.WorkflowSymbol, id.EnvironmentSymbol, id.RegistrationID)
}

func projectEnvLabel(id targetIdentity) string {
	return fmt.Sprintf("Project environment %s (#%d)", id.EnvironmentSymbol, id.EnvironmentID)
}

func listTargetCandidates(ctx context.Context, tx *sql.Tx, rel ReleaseRecord) []TargetCandidate {
	out := []TargetCandidate{}
	deliveryID, _, err := batchDelivery(ctx, tx, rel.ProjectID, rel.BatchID)
	if err == nil && deliveryID > 0 {
		rows, qerr := tx.QueryContext(ctx, livePharosCandidatesSQL, deliveryID, rel.ProjectID)
		if qerr == nil {
			for rows.Next() {
				var id int64
				var workflow, env string
				if err := rows.Scan(&id, &workflow, &env); err != nil {
					break
				}
				out = append(out, TargetCandidate{
					Kind: TargetKindPharosOwner, RegistrationID: id, WorkflowSymbol: workflow, EnvironmentSymbol: env,
					Label: fmt.Sprintf("Pharos %s / %s (registration %d)", workflow, env, id),
				})
			}
			_ = rows.Close()
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM project_environments WHERE project_id=? ORDER BY sort_order, id`, rel.ProjectID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return out
		}
		if !validOpaqueRef(strings.TrimSpace(name)) {
			continue
		}
		out = append(out, TargetCandidate{
			Kind: TargetKindProjectEnv, EnvironmentID: id, EnvironmentSymbol: strings.TrimSpace(name),
			Label: fmt.Sprintf("Project environment %s (#%d)", strings.TrimSpace(name), id),
		})
	}
	return out
}
