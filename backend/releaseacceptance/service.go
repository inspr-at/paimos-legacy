// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package releaseacceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/mailer"
)

func (s *Service) now() string {
	return s.Clock.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func (s *Service) Mint(ctx context.Context, actor Actor, projectID, batchID int64) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Acceptance{}, err
	}
	art, err := s.Artifacts.Load(ctx, tx, projectID, batchID)
	if err != nil {
		return Acceptance{}, err
	}
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM release_records WHERE project_id=? AND batch_id=?`, projectID, batchID).Scan(&existing)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Acceptance{}, err
		}
		return s.Get(ctx, actor, projectID, existing)
	}
	if err != sql.ErrNoRows {
		return Acceptance{}, err
	}
	ref := "release_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	res, err := tx.ExecContext(ctx, `INSERT INTO release_records(
		project_id,batch_id,release_ref,batch_key,baseline_ref,content_digest,revision_seal,
		artifact_digest,artifact_coordinate,version_scheme,release_channel,release_sequence,version,commit_sha,
		state,revision,minted_by,minted_session_credential_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, batchID, ref, art.BatchKey, art.BaselineRef, art.ContentDigest, art.RevisionSeal,
		art.ArtifactDigest, art.ArtifactCoordinate, art.VersionScheme, art.ReleaseChannel, art.ReleaseSequence,
		art.Version, art.Commit, StateBuilt, 1, actor.UserID, actor.SessionCredentialID, s.now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			if err := tx.Commit(); err != nil {
				return Acceptance{}, err
			}
			return s.GetByBatch(ctx, actor, projectID, batchID)
		}
		return Acceptance{}, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return s.Get(ctx, actor, projectID, id)
}

func (s *Service) List(ctx context.Context, actor Actor, projectID int64) ([]ReleaseRecord, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM release_records WHERE project_id=? ORDER BY id DESC LIMIT 32`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	out := []ReleaseRecord{}
	for _, id := range ids {
		rec, err := loadRelease(ctx, tx, projectID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, actor Actor, projectID, releaseID int64) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return Acceptance{}, err
	}
	out, err := s.projectAcceptance(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return out, nil
}

func (s *Service) GetByBatch(ctx context.Context, actor Actor, projectID, batchID int64) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return Acceptance{}, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM release_records WHERE project_id=? AND batch_id=?`, projectID, batchID).Scan(&id)
	if err == sql.ErrNoRows {
		return Acceptance{}, fmt.Errorf("%w: release", ErrNotFound)
	}
	if err != nil {
		return Acceptance{}, err
	}
	out, err := s.projectAcceptance(ctx, tx, projectID, id)
	if err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return out, nil
}

type ConfigureRequest struct {
	ExpectedRevision  int64        `json:"expected_revision"`
	OperatingMode     string       `json:"operating_mode"`
	AgreementRef      string       `json:"agreement_ref"`
	DisclosedGaps     []Gap        `json:"disclosed_gaps"`
	Parties           []PartyInput `json:"parties"`
	DeliveryPartyRef  string       `json:"delivery_party_ref"`
	OperatorPartyRef  string       `json:"operator_party_ref"`
	SupportPartyRef   *string      `json:"support_party_ref"`
	RequiredPartyRefs []string     `json:"required_party_refs"`
}

func (s *Service) Configure(ctx context.Context, actor Actor, projectID, releaseID int64, req ConfigureRequest) (Acceptance, error) {
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
	if rel.State == StateAccepted {
		return Acceptance{}, fmt.Errorf("%w: accepted release cannot be reconfigured", ErrConflict)
	}
	if err := validateConfigure(req); err != nil {
		return Acceptance{}, err
	}
	if err := s.validateParties(ctx, tx, projectID, req); err != nil {
		return Acceptance{}, err
	}
	gapsJSON, _ := json.Marshal(canonicalGaps(req.DisclosedGaps))
	reqJSON, _ := json.Marshal(sortedUnique(req.RequiredPartyRefs))
	support := sql.NullString{}
	if req.SupportPartyRef != nil && strings.TrimSpace(*req.SupportPartyRef) != "" {
		support = sql.NullString{String: strings.TrimSpace(*req.SupportPartyRef), Valid: true}
	}
	var accID, revision int64
	var status, mode, agreement, gapsStored, reqStored, delivery, operator string
	var supportStored sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,revision,status,operating_mode,agreement_ref,disclosed_gaps_json,required_party_refs_json,
		delivery_party_ref,operator_party_ref,support_party_ref FROM release_acceptances WHERE release_id=?`, releaseID).
		Scan(&accID, &revision, &status, &mode, &agreement, &gapsStored, &reqStored, &delivery, &operator, &supportStored)
	now := s.now()
	if err == sql.ErrNoRows {
		res, err := tx.ExecContext(ctx, `INSERT INTO release_acceptances(
			release_id,project_id,revision,status,operating_mode,agreement_ref,disclosed_gaps_json,required_party_refs_json,
			delivery_party_ref,operator_party_ref,support_party_ref,configured_by,configured_session_credential_id,configured_at)
			VALUES(?,?,1,'pending',?,?,?,?,?,?,?,?,?,?)`,
			releaseID, projectID, req.OperatingMode, strings.TrimSpace(req.AgreementRef), string(gapsJSON), string(reqJSON),
			req.DeliveryPartyRef, req.OperatorPartyRef, support, actor.UserID, actor.SessionCredentialID, now)
		if err != nil {
			return Acceptance{}, err
		}
		accID, _ = res.LastInsertId()
		revision = 1
	} else if err != nil {
		return Acceptance{}, err
	} else {
		if status == StatusAccepted {
			return Acceptance{}, fmt.Errorf("%w: accepted release cannot be reconfigured", ErrConflict)
		}
		if req.ExpectedRevision != 0 && req.ExpectedRevision != revision {
			return Acceptance{}, fmt.Errorf("%w: acceptance revision", ErrStale)
		}
		same := mode == req.OperatingMode && agreement == strings.TrimSpace(req.AgreementRef) &&
			gapsStored == string(gapsJSON) && reqStored == string(reqJSON) &&
			delivery == req.DeliveryPartyRef && operator == req.OperatorPartyRef &&
			supportStored.String == support.String && supportStored.Valid == support.Valid
		partiesChanged, err := partiesDiffer(ctx, tx, accID, revision, req.Parties)
		if err != nil {
			return Acceptance{}, err
		}
		if !same || partiesChanged {
			revision++
			if _, err := tx.ExecContext(ctx, `UPDATE release_acceptances SET revision=?,operating_mode=?,agreement_ref=?,disclosed_gaps_json=?,
				required_party_refs_json=?,delivery_party_ref=?,operator_party_ref=?,support_party_ref=?,
				preview_subject='',preview_body='',preview_revision=0,configured_by=?,configured_session_credential_id=?,configured_at=?
				WHERE id=?`,
				revision, req.OperatingMode, strings.TrimSpace(req.AgreementRef), string(gapsJSON), string(reqJSON),
				req.DeliveryPartyRef, req.OperatorPartyRef, support, actor.UserID, actor.SessionCredentialID, now, accID); err != nil {
				return Acceptance{}, err
			}
		}
	}
	if err := replaceParties(ctx, tx, accID, revision, req.Parties); err != nil {
		return Acceptance{}, err
	}
	if rel.State == StateBuilt {
		if _, err := tx.ExecContext(ctx, `UPDATE release_records SET state=? WHERE id=?`, StateAcceptancePending, releaseID); err != nil {
			return Acceptance{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return s.Get(ctx, actor, projectID, releaseID)
}

func (s *Service) Confirm(ctx context.Context, actor Actor, projectID, releaseID int64, partyRef, attestation string) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return Acceptance{}, err
	}
	acc, err := s.projectAcceptance(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	if acc.Status == StatusAccepted {
		return Acceptance{}, fmt.Errorf("%w: already accepted", ErrConflict)
	}
	party, ok := partyByRef(acc.Parties, partyRef)
	if !ok {
		return Acceptance{}, fmt.Errorf("%w: unknown party", ErrInvalid)
	}
	if party.Kind != PartyLinkedUser || party.UserID == nil || *party.UserID != actor.UserID {
		return Acceptance{}, fmt.Errorf("%w: party may confirm only their own linked acceptance", ErrForbidden)
	}
	if err := insertConfirmation(ctx, tx, acc, actor, partyRef, SourcePlatform, strings.TrimSpace(attestation), s.now()); err != nil {
		return Acceptance{}, err
	}
	if err := s.maybeFinalize(ctx, tx, acc.Release.ID); err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return s.Get(ctx, actor, projectID, releaseID)
}

type RecordExternalRequest struct {
	RequestKey         string   `json:"request_key"`
	RecipientPartyRefs []string `json:"recipient_party_refs"`
	RawMessage         string   `json:"raw_message"`
	Attestation        string   `json:"attestation"`
	AttestedPartyRefs  []string `json:"attested_party_refs"`
	ConfirmAttest      bool     `json:"confirm_attest"`
}

func (s *Service) RecordExternal(ctx context.Context, actor Actor, projectID, releaseID int64, req RecordExternalRequest) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Acceptance{}, err
	}
	acc, err := s.projectAcceptance(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	if acc.Status == StatusAccepted {
		return Acceptance{}, fmt.Errorf("%w: already accepted", ErrConflict)
	}
	raw := []byte(req.RawMessage)
	if strings.TrimSpace(req.RequestKey) == "" || strings.TrimSpace(req.Attestation) == "" || len(raw) == 0 || len(raw) > maxBodyBytes {
		return Acceptance{}, fmt.Errorf("%w: retained evidence and explicit attestation are required", ErrInvalid)
	}
	if lookslikeHTMLRemote(raw) {
		return Acceptance{}, fmt.Errorf("%w: remote content is not allowed in imported email", ErrInvalid)
	}
	if err := coverKnownParties(acc, req.RecipientPartyRefs); err != nil {
		return Acceptance{}, err
	}
	attested := sortedUnique(req.AttestedPartyRefs)
	if len(attested) > 0 && !req.ConfirmAttest {
		return Acceptance{}, fmt.Errorf("%w: explicit confirm_attest is required to record party acceptance", ErrInvalid)
	}
	if !req.ConfirmAttest {
		attested = nil
	}
	for _, partyRef := range attested {
		if _, ok := partyByRef(acc.Parties, partyRef); !ok {
			return Acceptance{}, fmt.Errorf("%w: attested party", ErrInvalid)
		}
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM acceptance_email_evidence WHERE acceptance_id=? AND request_key=?`, acc.ID, req.RequestKey).Scan(&existing)
	if err == nil {
		var storedHash string
		_ = tx.QueryRowContext(ctx, `SELECT body_sha256 FROM acceptance_email_evidence WHERE id=?`, existing).Scan(&storedHash)
		if storedHash != hash {
			return Acceptance{}, fmt.Errorf("%w: request_key is bound to different evidence", ErrConflict)
		}
		if err := tx.Commit(); err != nil {
			return Acceptance{}, err
		}
		return s.Get(ctx, actor, projectID, releaseID)
	}
	if err != sql.ErrNoRows {
		return Acceptance{}, err
	}
	recip, _ := json.Marshal(sortedUnique(req.RecipientPartyRefs))
	messageRef := "message_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO acceptance_email_evidence(
		acceptance_id,release_id,acceptance_revision,message_ref,request_key,recipient_party_refs_json,state,source,
		recorded_at,sent_at,actor_user_id,session_credential_id,attestation,body_sha256,raw_message)
		VALUES(?,?,?,?,?,?,'sent','external_manual',?,?,?,?,?,?,?)`,
		acc.ID, acc.Release.ID, acc.Revision, messageRef, req.RequestKey, string(recip), now, now,
		actor.UserID, actor.SessionCredentialID, strings.TrimSpace(req.Attestation), hash, raw); err != nil {
		return Acceptance{}, err
	}
	for _, partyRef := range attested {
		if err := insertConfirmation(ctx, tx, acc, actor, partyRef, SourceExternalEmail, strings.TrimSpace(req.Attestation), now); err != nil {
			return Acceptance{}, err
		}
	}
	if err := s.maybeFinalize(ctx, tx, acc.Release.ID); err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return s.Get(ctx, actor, projectID, releaseID)
}

type PreviewRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (s *Service) SavePreview(ctx context.Context, actor Actor, projectID, releaseID int64, req PreviewRequest) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Acceptance{}, err
	}
	acc, err := s.projectAcceptance(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	if acc.Status == StatusAccepted {
		return Acceptance{}, fmt.Errorf("%w: already accepted", ErrConflict)
	}
	subject := strings.TrimSpace(req.Subject)
	body := strings.TrimSpace(req.Body)
	if subject == "" || body == "" || len(body) > maxBodyBytes || !headerSafe(subject) {
		return Acceptance{}, fmt.Errorf("%w: reviewable message is required", ErrInvalid)
	}
	next := acc.PreviewRevision + 1
	if acc.PreviewSubject == subject && acc.PreviewBody == body && acc.PreviewRevision > 0 {
		if err := tx.Commit(); err != nil {
			return Acceptance{}, err
		}
		return acc, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE release_acceptances SET preview_subject=?,preview_body=?,preview_revision=? WHERE id=?`,
		subject, body, next, acc.ID); err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return s.Get(ctx, actor, projectID, releaseID)
}

type AuthorizeSendRequest struct {
	RequestKey         string   `json:"request_key"`
	RecipientPartyRefs []string `json:"recipient_party_refs"`
	PreviewRevision    int64    `json:"preview_revision"`
	ConfirmSend        bool     `json:"confirm_send"`
}

func (s *Service) AuthorizeSend(ctx context.Context, actor Actor, projectID, releaseID int64, req AuthorizeSendRequest) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return Acceptance{}, err
	}
	acc, err := s.projectAcceptance(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	if acc.Status == StatusAccepted {
		return Acceptance{}, fmt.Errorf("%w: already accepted", ErrConflict)
	}
	if !req.ConfirmSend {
		return Acceptance{}, fmt.Errorf("%w: explicit confirm_send is required", ErrInvalid)
	}
	if acc.PreviewRevision == 0 || strings.TrimSpace(acc.PreviewBody) == "" {
		return Acceptance{}, fmt.Errorf("%w: reviewable message is required", ErrInvalid)
	}
	if req.PreviewRevision != acc.PreviewRevision {
		return Acceptance{}, fmt.Errorf("%w: preview revision", ErrStale)
	}
	if strings.TrimSpace(req.RequestKey) == "" {
		return Acceptance{}, fmt.Errorf("%w: request_key", ErrInvalid)
	}
	var existingEvidence int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM acceptance_email_evidence WHERE acceptance_id=? AND request_key=?`, acc.ID, req.RequestKey).Scan(&existingEvidence)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Acceptance{}, err
		}
		return s.Get(ctx, actor, projectID, releaseID)
	}
	if err != sql.ErrNoRows {
		return Acceptance{}, err
	}
	if acc.MailInFlight {
		return Acceptance{}, fmt.Errorf("%w: delivery already in flight or unresolved; do not authorize another send", ErrConflict)
	}
	if err := coverKnownParties(acc, req.RecipientPartyRefs); err != nil {
		return Acceptance{}, err
	}
	from := mailer.SenderAddress(s.Mail)
	if from == "" {
		return Acceptance{}, fmt.Errorf("%w: operator sender is not configured", ErrUnavailable)
	}
	raw := buildRawMessage(acc, req.RecipientPartyRefs, from, s.Clock.Now().UTC(), uuid.NewString())
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	now := s.now()
	recip, _ := json.Marshal(sortedUnique(req.RecipientPartyRefs))
	messageRef := "message_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	res, err := tx.ExecContext(ctx, `INSERT INTO acceptance_email_evidence(
		acceptance_id,release_id,acceptance_revision,message_ref,request_key,recipient_party_refs_json,state,source,
		recorded_at,sent_at,actor_user_id,session_credential_id,attestation,body_sha256,raw_message)
		VALUES(?,?,?,?,?,?,'pending','platform_send',?,NULL,?,?,?,?,?)`,
		acc.ID, acc.Release.ID, acc.Revision, messageRef, req.RequestKey, string(recip), now,
		actor.UserID, actor.SessionCredentialID, "", hash, raw)
	if err != nil {
		return Acceptance{}, err
	}
	evidenceID, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO acceptance_mail_outbox(
		evidence_id,request_key,state,attempt_count,last_error_class,authorized_by,session_credential_id,preview_revision,created_at,updated_at)
		VALUES(?,?,'queued',0,'',?,?,?,?,?)`,
		evidenceID, req.RequestKey, actor.UserID, actor.SessionCredentialID, acc.PreviewRevision, now, now); err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	_ = s.DrainOnce(ctx)
	return s.Get(ctx, actor, projectID, releaseID)
}

func failQueuedMail(ctx context.Context, tx *sql.Tx, outboxID, evidenceID int64, class, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE acceptance_email_evidence SET state='failed' WHERE id=? AND state<>'sent'`, evidenceID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE acceptance_mail_outbox SET state='failed',last_error_class=?,lease_until=NULL,updated_at=? WHERE id=? AND state<>'sent'`,
		class, now, outboxID)
	return err
}

func (s *Service) mailLease() time.Duration {
	if s.Lease > 0 {
		return s.Lease
	}
	return defaultMailLease
}

func (s *Service) mailSendTimeout() time.Duration {
	if s.SendTimeout > 0 {
		return s.SendTimeout
	}
	return defaultSendTimeout
}

func (s *Service) DrainOnce(ctx context.Context) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	if err := s.reconcileExpiredSending(ctx, tx, now); err != nil {
		return err
	}
	var outboxID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM acceptance_mail_outbox WHERE state='queued' ORDER BY id LIMIT 1`).Scan(&outboxID)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	lease := s.Clock.Now().UTC().Add(s.mailLease()).Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `UPDATE acceptance_mail_outbox SET state='sending',attempt_count=attempt_count+1,lease_until=?,updated_at=?
		WHERE id=? AND state='queued'`, lease, now, outboxID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return tx.Commit()
	}
	var evidenceID, previewRev, authorizedBy int64
	var authCred string
	if err := tx.QueryRowContext(ctx, `SELECT evidence_id,preview_revision,authorized_by,session_credential_id
		FROM acceptance_mail_outbox WHERE id=?`, outboxID).Scan(&evidenceID, &previewRev, &authorizedBy, &authCred); err != nil {
		return err
	}
	var raw []byte
	var state string
	var evidenceRev, releaseID, accID int64
	if err := tx.QueryRowContext(ctx, `SELECT raw_message,state,acceptance_revision,release_id,acceptance_id FROM acceptance_email_evidence WHERE id=?`, evidenceID).
		Scan(&raw, &state, &evidenceRev, &releaseID, &accID); err != nil {
		return err
	}
	if state == MailSent {
		if _, err := tx.ExecContext(ctx, `UPDATE acceptance_mail_outbox SET state='sent',updated_at=? WHERE id=?`, now, outboxID); err != nil {
			return err
		}
		return tx.Commit()
	}
	var liveRev, livePreview, projectID int64
	var liveStatus string
	if err := tx.QueryRowContext(ctx, `SELECT a.revision,a.preview_revision,a.status,a.project_id FROM release_acceptances a WHERE a.id=?`, accID).
		Scan(&liveRev, &livePreview, &liveStatus, &projectID); err != nil {
		return err
	}
	if liveStatus == StatusAccepted || liveRev != evidenceRev || livePreview != previewRev {
		if err := failQueuedMail(ctx, tx, outboxID, evidenceID, "smtp_stale", now); err != nil {
			return err
		}
		return tx.Commit()
	}
	authorizer := Actor{Kind: string("session"), UserID: authorizedBy, SessionCredentialID: authCred}
	if _, err := s.currentAuthority(ctx, tx, authorizer, projectID, true); err != nil {
		if err := failQueuedMail(ctx, tx, outboxID, evidenceID, "smtp_revoked", now); err != nil {
			return err
		}
		return tx.Commit()
	}
	to, err := recipientsForEvidence(ctx, tx, evidenceID)
	if err != nil {
		return err
	}
	for _, addr := range to {
		if !validEmail(addr) {
			if err := failQueuedMail(ctx, tx, outboxID, evidenceID, "smtp_rejected", now); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	from := mailer.SenderAddress(s.Mail)
	if from == "" {
		if err := failQueuedMail(ctx, tx, outboxID, evidenceID, "smtp_unconfigured", now); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, s.mailSendTimeout())
	defer cancel()
	sendErr := s.Mail.Send(sendCtx, mailer.Message{From: from, To: to, Raw: raw})
	tx2, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx2.Rollback()
	now = s.now()
	if sendErr == nil {
		if _, err := tx2.ExecContext(ctx, `UPDATE acceptance_email_evidence SET state='sent',sent_at=? WHERE id=?`, now, evidenceID); err != nil {
			return err
		}
		if _, err := tx2.ExecContext(ctx, `UPDATE acceptance_mail_outbox SET state='sent',last_error_class='',lease_until=NULL,updated_at=? WHERE id=?`, now, outboxID); err != nil {
			return err
		}
		if err := s.maybeFinalize(ctx, tx2, releaseID); err != nil {
			return err
		}
		return tx2.Commit()
	}
	class := mailer.ErrorClass(sendErr)
	if err := failQueuedMail(ctx, tx2, outboxID, evidenceID, class, now); err != nil {
		return err
	}
	return tx2.Commit()
}

func (s *Service) reconcileExpiredSending(ctx context.Context, tx *sql.Tx, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,evidence_id FROM acceptance_mail_outbox
		WHERE state='sending' AND (lease_until IS NULL OR lease_until<=?)`, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids [][2]int64
	for rows.Next() {
		var outboxID, evidenceID int64
		if err := rows.Scan(&outboxID, &evidenceID); err != nil {
			return err
		}
		ids = append(ids, [2]int64{outboxID, evidenceID})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := failQueuedMail(ctx, tx, id[0], id[1], "smtp_ambiguous", now); err != nil {
			return err
		}
	}
	return nil
}

type PolicyRequest struct {
	PolicyRef      string   `json:"policy_ref"`
	TargetRef      string   `json:"target_ref"`
	Parties        []string `json:"parties"`
	ModelRef       string   `json:"model_ref"`
	AgreementRef   string   `json:"agreement_ref"`
	Gaps           []Gap    `json:"gaps"`
	ReleaseChannel string   `json:"release_channel"`
	ArtifactDigest string   `json:"artifact_digest"`
	BoundedUse     string   `json:"bounded_use"`
	ExpiresAt      string   `json:"expires_at"`
	ContentDigest  string   `json:"content_digest"`
	RevisionSeal   string   `json:"revision_seal"`
}

func (s *Service) ApprovePolicy(ctx context.Context, actor Actor, projectID int64, req PolicyRequest) (StandingPolicy, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return StandingPolicy{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return StandingPolicy{}, err
	}
	if !validOpaqueRef(req.PolicyRef) || strings.TrimSpace(req.BoundedUse) == "" {
		return StandingPolicy{}, fmt.Errorf("%w: standing policy", ErrInvalid)
	}
	expires, err := parsePolicyExpiry(req.ExpiresAt, s.Clock.Now().UTC())
	if err != nil {
		return StandingPolicy{}, err
	}
	if len(req.ContentDigest) != 71 || len(req.RevisionSeal) != 71 {
		return StandingPolicy{}, fmt.Errorf("%w: baseline binding", ErrInvalid)
	}
	targetRef, modelRef, err := bindStandingPolicyScope(ctx, tx, projectID, req)
	if err != nil {
		return StandingPolicy{}, err
	}
	gapsJSON, _ := json.Marshal(canonicalGaps(req.Gaps))
	partiesJSON, _ := json.Marshal(sortedUnique(req.Parties))
	now := s.now()
	res, err := tx.ExecContext(ctx, `INSERT INTO acceptance_standing_policies(
		project_id,policy_ref,operating_mode,approved_by,session_credential_id,content_digest,revision_seal,target_ref,
		parties_json,model_ref,agreement_ref,gaps_json,release_channel,artifact_digest,bounded_use,expires_at,created_at)
		VALUES(?,?,'customer_operated',?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, req.PolicyRef, actor.UserID, actor.SessionCredentialID, req.ContentDigest, req.RevisionSeal,
		targetRef, string(partiesJSON), modelRef, strings.TrimSpace(req.AgreementRef),
		string(gapsJSON), strings.TrimSpace(req.ReleaseChannel), strings.TrimSpace(req.ArtifactDigest),
		strings.TrimSpace(req.BoundedUse), expires, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return StandingPolicy{}, fmt.Errorf("%w: policy_ref", ErrConflict)
		}
		return StandingPolicy{}, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return StandingPolicy{}, err
	}
	return s.GetPolicy(ctx, actor, projectID, id)
}

func (s *Service) RevokePolicy(ctx context.Context, actor Actor, projectID, policyID int64) (StandingPolicy, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return StandingPolicy{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return StandingPolicy{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE acceptance_standing_policies SET revoked_at=? WHERE id=? AND project_id=? AND revoked_at IS NULL`,
		s.now(), policyID, projectID); err != nil {
		return StandingPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return StandingPolicy{}, err
	}
	return s.GetPolicy(ctx, actor, projectID, policyID)
}

func (s *Service) GetPolicy(ctx context.Context, actor Actor, projectID, policyID int64) (StandingPolicy, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return StandingPolicy{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return StandingPolicy{}, err
	}
	p, err := loadPolicy(ctx, tx, projectID, policyID)
	if err != nil {
		return StandingPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return StandingPolicy{}, err
	}
	return p, nil
}

func (s *Service) ListPolicies(ctx context.Context, actor Actor, projectID int64) ([]StandingPolicy, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM acceptance_standing_policies WHERE project_id=? ORDER BY id DESC LIMIT 32`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	out := []StandingPolicy{}
	for _, id := range ids {
		p, err := loadPolicy(ctx, tx, projectID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) ApplyPolicy(ctx context.Context, actor Actor, projectID, releaseID, policyID int64) (Acceptance, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Acceptance{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, false); err != nil {
		return Acceptance{}, err
	}
	acc, err := s.projectAcceptance(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	if acc.OperatingMode != ModeCustomerOperated {
		return Acceptance{}, fmt.Errorf("%w: standing policy is only customer-operated", ErrForbidden)
	}
	policy, err := loadPolicy(ctx, tx, projectID, policyID)
	if err != nil {
		return Acceptance{}, err
	}
	if err := policyMatches(policy, acc, s.Clock.Now().UTC()); err != nil {
		return Acceptance{}, err
	}
	if policy.RevokedAt != nil {
		return Acceptance{}, fmt.Errorf("%w: policy revoked", ErrForbidden)
	}
	own := ""
	for _, p := range acc.Parties {
		if p.Kind == PartyLinkedUser && p.UserID != nil && *p.UserID == actor.UserID {
			own = p.PartyRef
			break
		}
	}
	if own == "" || !contains(policy.Parties, own) {
		return Acceptance{}, fmt.Errorf("%w: standing policy does not cover this party", ErrForbidden)
	}
	if err := insertConfirmation(ctx, tx, acc, actor, own, SourceStandingPolicy, "standing_policy:"+policy.PolicyRef, s.now()); err != nil {
		return Acceptance{}, err
	}
	if err := s.maybeFinalize(ctx, tx, acc.Release.ID); err != nil {
		return Acceptance{}, err
	}
	if err := tx.Commit(); err != nil {
		return Acceptance{}, err
	}
	return s.Get(ctx, actor, projectID, releaseID)
}

func (s *Service) maybeFinalize(ctx context.Context, tx *sql.Tx, releaseID int64) error {
	acc, err := loadAcceptanceByRelease(ctx, tx, releaseID)
	if err != nil {
		return err
	}
	if acc.Status == StatusAccepted {
		return nil
	}
	missing := computeMissing(acc)
	if len(missing.Confirmations) > 0 || len(missing.EmailCoverage) > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE release_acceptances SET status='accepted' WHERE id=?`, acc.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE release_records SET state='accepted' WHERE id=?`, releaseID); err != nil {
		return err
	}
	return nil
}

func (s *Service) projectAcceptance(ctx context.Context, tx *sql.Tx, projectID, releaseID int64) (Acceptance, error) {
	rel, err := loadRelease(ctx, tx, projectID, releaseID)
	if err != nil {
		return Acceptance{}, err
	}
	acc, err := loadAcceptanceByRelease(ctx, tx, releaseID)
	if err != nil && !isNotFound(err) {
		return Acceptance{}, err
	}
	if isNotFound(err) {
		acc = Acceptance{Release: rel, Status: StatusPending, DisclosedGaps: []Gap{}, Parties: []Party{}, Confirmations: []Confirmation{}, EmailEvidence: []EmailEvidence{}}
	} else {
		acc.Release = rel
	}
	acc.OperatingModeLabel = ModeLabel(acc.OperatingMode)
	acc.OfferDisclaimer = OfferDisclaimer
	acc.Defaults = loadDefaults(ctx, tx, projectID)
	acc.Missing = computeMissing(acc)
	s.annotateDeploymentTarget(ctx, tx, &acc)
	annotateProjection(&acc)
	return acc, nil
}

func annotateProjection(acc *Acceptance) {
	names := map[string]string{}
	for _, p := range acc.Parties {
		label := strings.TrimSpace(p.DisplayName)
		if label == "" {
			label = p.Email
		}
		if label == "" {
			label = p.PartyRef
		}
		names[p.PartyRef] = label
	}
	for i := range acc.Confirmations {
		acc.Confirmations[i].PartyName = names[acc.Confirmations[i].PartyRef]
		acc.Confirmations[i].SourceLabel = sourceLabel(acc.Confirmations[i].Source)
		if acc.Confirmations[i].AcceptanceRevision == 0 {
			acc.Confirmations[i].AcceptanceRevision = acc.Revision
		}
	}
	inFlight := false
	ambiguous := ""
	other := ""
	for i := range acc.EmailEvidence {
		e := &acc.EmailEvidence[i]
		e.DisplayState = displayMailState(e.State, e.OutboxState, e.LastErrorClass)
		e.RecipientNames = nil
		for _, ref := range e.RecipientPartyRefs {
			label := names[ref]
			if label == "" {
				label = partyDisplayName(*acc, ref)
			}
			e.RecipientNames = append(e.RecipientNames, label)
		}
		if e.DisplayState == MailQueued || e.DisplayState == MailSending || e.DisplayState == MailAmbiguous {
			inFlight = true
		}
		text := recoveryText(e.DisplayState, e.LastErrorClass)
		if e.DisplayState == MailAmbiguous || e.LastErrorClass == "smtp_ambiguous" {
			if ambiguous == "" {
				ambiguous = text
			}
			continue
		}
		if other == "" {
			other = text
		}
	}
	if ambiguous != "" {
		acc.MailRecovery = ambiguous
	} else {
		acc.MailRecovery = other
	}
	acc.MailInFlight = inFlight
}

func sourceLabel(source string) string {
	switch source {
	case SourcePlatform:
		return "platform confirmation"
	case SourceExternalEmail:
		return "attested from recorded email"
	case SourceStandingPolicy:
		return "standing policy"
	default:
		return source
	}
}

func displayMailState(state, outbox, class string) string {
	if state == MailSent {
		return MailSent
	}
	if class == "smtp_ambiguous" {
		return MailAmbiguous
	}
	if state == MailFailed {
		return MailFailed
	}
	if outbox == MailSending {
		return MailSending
	}
	if outbox == MailQueued || state == MailPending {
		return MailQueued
	}
	return state
}

func recoveryText(display, class string) string {
	if display == MailAmbiguous || class == "smtp_ambiguous" {
		return "Delivery is uncertain. Do not send this message again until it is reconciled. Automatic retry is not used."
	}
	if class == "smtp_stale" || class == "smtp_revoked" {
		return "This queued send is no longer authorized. Review the current acceptance before any new send."
	}
	if display == MailFailed || class == "smtp_rejected" || class == "smtp_unconfigured" {
		return "Send did not complete before the server accepted the message. After fixing transport, authorize a new send with a new request key."
	}
	return ""
}

func loadDefaults(ctx context.Context, tx *sql.Tx, projectID int64) CooperationDefaults {
	var agreement, notes sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT report_contract_basis,cooperation_notes FROM project_cooperation WHERE project_id=?`, projectID).Scan(&agreement, &notes)
	return CooperationDefaults{AgreementRef: agreement.String, Notes: notes.String}
}

func loadRelease(ctx context.Context, tx *sql.Tx, projectID, id int64) (ReleaseRecord, error) {
	var r ReleaseRecord
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,batch_id,release_ref,batch_key,baseline_ref,content_digest,revision_seal,
		artifact_digest,artifact_coordinate,version_scheme,release_channel,release_sequence,version,commit_sha,state,revision,created_at
		FROM release_records WHERE id=? AND project_id=?`, id, projectID).Scan(
		&r.ID, &r.ProjectID, &r.BatchID, &r.ReleaseRef, &r.BatchKey, &r.BaselineRef, &r.ContentDigest, &r.RevisionSeal,
		&r.ArtifactDigest, &r.ArtifactCoordinate, &r.VersionScheme, &r.ReleaseChannel, &r.ReleaseSequence, &r.Version,
		&r.CommitSHA, &r.State, &r.Revision, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return ReleaseRecord{}, fmt.Errorf("%w: release", ErrNotFound)
	}
	return r, err
}

func loadAcceptanceByRelease(ctx context.Context, tx *sql.Tx, releaseID int64) (Acceptance, error) {
	var a Acceptance
	var support sql.NullString
	var previewSub, previewBody sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,revision,status,operating_mode,agreement_ref,disclosed_gaps_json,required_party_refs_json,
		delivery_party_ref,operator_party_ref,support_party_ref,preview_subject,preview_body,preview_revision
		FROM release_acceptances WHERE release_id=?`, releaseID).Scan(
		&a.ID, &a.Revision, &a.Status, &a.OperatingMode, &a.AgreementRef, newJSONScan(&a.DisclosedGaps), newJSONScan(&a.RequiredPartyRefs),
		&a.DeliveryPartyRef, &a.OperatorPartyRef, &support, &previewSub, &previewBody, &a.PreviewRevision)
	if err == sql.ErrNoRows {
		return Acceptance{}, fmt.Errorf("%w: acceptance", ErrNotFound)
	}
	if err != nil {
		return Acceptance{}, err
	}
	if support.Valid {
		v := support.String
		a.SupportPartyRef = &v
	}
	a.PreviewSubject = previewSub.String
	a.PreviewBody = previewBody.String
	a.Parties, err = loadParties(ctx, tx, a.ID, a.Revision)
	if err != nil {
		return Acceptance{}, err
	}
	a.Confirmations, err = loadConfirmations(ctx, tx, a.ID, a.Revision)
	if err != nil {
		return Acceptance{}, err
	}
	a.EmailEvidence, err = loadEvidence(ctx, tx, a.ID, a.Revision)
	return a, err
}

type jsonScan struct{ dest any }

func newJSONScan(dest any) *jsonScan { return &jsonScan{dest: dest} }

func (j *jsonScan) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	case nil:
		return nil
	default:
		return fmt.Errorf("json")
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, j.dest)
}

func loadParties(ctx context.Context, tx *sql.Tx, accID, rev int64) ([]Party, error) {
	rows, err := tx.QueryContext(ctx, `SELECT party_ref,party_kind,user_id,email,display_name,roles_json
		FROM acceptance_parties WHERE acceptance_id=? AND acceptance_revision=? ORDER BY id`, accID, rev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Party{}
	for rows.Next() {
		var p Party
		var user sql.NullInt64
		if err := rows.Scan(&p.PartyRef, &p.Kind, &user, &p.Email, &p.DisplayName, newJSONScan(&p.Roles)); err != nil {
			return nil, err
		}
		if user.Valid {
			id := user.Int64
			p.UserID = &id
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func loadConfirmations(ctx context.Context, tx *sql.Tx, accID, rev int64) ([]Confirmation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT party_ref,decision,source,actor_user_id,attestation,confirmed_at,acceptance_revision
		FROM acceptance_confirmations WHERE acceptance_id=? AND acceptance_revision=? ORDER BY id`, accID, rev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Confirmation{}
	for rows.Next() {
		var c Confirmation
		if err := rows.Scan(&c.PartyRef, &c.Decision, &c.Source, &c.ActorUserID, &c.Attestation, &c.ConfirmedAt, &c.AcceptanceRevision); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func loadEvidence(ctx context.Context, tx *sql.Tx, accID, rev int64) ([]EmailEvidence, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.message_ref,e.acceptance_revision,e.recipient_party_refs_json,e.state,e.source,e.recorded_at,e.sent_at,e.actor_user_id,e.attestation,e.body_sha256,COALESCE(o.last_error_class,''),COALESCE(o.state,'')
		FROM acceptance_email_evidence e
		LEFT JOIN acceptance_mail_outbox o ON o.evidence_id=e.id
		WHERE e.acceptance_id=? AND e.acceptance_revision=? ORDER BY e.id`, accID, rev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EmailEvidence{}
	for rows.Next() {
		var e EmailEvidence
		var sent sql.NullString
		if err := rows.Scan(&e.MessageRef, &e.ReleaseRevision, newJSONScan(&e.RecipientPartyRefs), &e.State, &e.Source, &e.RecordedAt, &sent, &e.ActorUserID, &e.Attestation, &e.BodySHA256, &e.LastErrorClass, &e.OutboxState); err != nil {
			return nil, err
		}
		if sent.Valid {
			v := sent.String
			e.SentAt = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func loadPolicy(ctx context.Context, tx *sql.Tx, projectID, id int64) (StandingPolicy, error) {
	var p StandingPolicy
	var revoked sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,policy_ref,operating_mode,content_digest,revision_seal,target_ref,parties_json,model_ref,
		agreement_ref,gaps_json,release_channel,artifact_digest,bounded_use,expires_at,revoked_at,created_at
		FROM acceptance_standing_policies WHERE id=? AND project_id=?`, id, projectID).Scan(
		&p.ID, &p.PolicyRef, &p.OperatingMode, &p.ContentDigest, &p.RevisionSeal, &p.TargetRef, newJSONScan(&p.Parties),
		&p.ModelRef, &p.AgreementRef, newJSONScan(&p.Gaps), &p.ReleaseChannel, &p.ArtifactDigest, &p.BoundedUse,
		&p.ExpiresAt, &revoked, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return StandingPolicy{}, fmt.Errorf("%w: policy", ErrNotFound)
	}
	if revoked.Valid {
		v := revoked.String
		p.RevokedAt = &v
	}
	return p, err
}

func partiesDiffer(ctx context.Context, tx *sql.Tx, accID, rev int64, next []PartyInput) (bool, error) {
	current, err := loadParties(ctx, tx, accID, rev)
	if err != nil {
		return false, err
	}
	if len(current) != len(next) {
		return true, nil
	}
	want := map[string]PartyInput{}
	for _, p := range next {
		want[p.PartyRef] = p
	}
	for _, p := range current {
		n, ok := want[p.PartyRef]
		if !ok {
			return true, nil
		}
		user := int64(0)
		if p.UserID != nil {
			user = *p.UserID
		}
		if p.Kind != n.Kind || user != n.UserID ||
			strings.ToLower(strings.TrimSpace(p.Email)) != strings.ToLower(strings.TrimSpace(n.Email)) ||
			strings.TrimSpace(p.DisplayName) != strings.TrimSpace(n.DisplayName) {
			return true, nil
		}
	}
	return false, nil
}

func replaceParties(ctx context.Context, tx *sql.Tx, accID, rev int64, parties []PartyInput) error {
	var n int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM acceptance_parties WHERE acceptance_id=? AND acceptance_revision=?`, accID, rev).Scan(&n)
	if n > 0 {
		return nil
	}
	for _, p := range parties {
		roles, _ := json.Marshal(sortedUnique(p.Roles))
		var user any
		if p.Kind == PartyLinkedUser {
			user = p.UserID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO acceptance_parties(
			acceptance_id,acceptance_revision,party_ref,party_kind,user_id,email,display_name,roles_json)
			VALUES(?,?,?,?,?,?,?,?)`, accID, rev, p.PartyRef, p.Kind, user, strings.ToLower(strings.TrimSpace(p.Email)),
			strings.TrimSpace(p.DisplayName), string(roles)); err != nil {
			return err
		}
	}
	return nil
}

func insertConfirmation(ctx context.Context, tx *sql.Tx, acc Acceptance, actor Actor, partyRef, source, attestation, now string) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT source FROM acceptance_confirmations WHERE acceptance_id=? AND acceptance_revision=? AND party_ref=?`,
		acc.ID, acc.Revision, partyRef).Scan(&existing)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO acceptance_confirmations(
		acceptance_id,release_id,acceptance_revision,party_ref,decision,source,actor_user_id,session_credential_id,attestation,confirmed_at)
		VALUES(?,?,?,?,'accept',?,?,?,?,?)`,
		acc.ID, acc.Release.ID, acc.Revision, partyRef, source, actor.UserID, actor.SessionCredentialID, attestation, now)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil
	}
	return err
}

func (s *Service) validateParties(ctx context.Context, tx *sql.Tx, projectID int64, req ConfigureRequest) error {
	seen := map[string]bool{}
	for _, p := range req.Parties {
		if !validOpaqueRef(p.PartyRef) || seen[p.PartyRef] {
			return fmt.Errorf("%w: party_ref", ErrInvalid)
		}
		if !validEmail(p.Email) {
			return fmt.Errorf("%w: party email", ErrInvalid)
		}
		if !headerSafe(p.DisplayName) {
			return fmt.Errorf("%w: party display name", ErrInvalid)
		}
		if p.Kind == PartyLinkedUser {
			if p.UserID <= 0 {
				return fmt.Errorf("%w: linked user", ErrInvalid)
			}
			var status, role string
			if err := tx.QueryRowContext(ctx, `SELECT status,role FROM users WHERE id=?`, p.UserID).Scan(&status, &role); err != nil {
				return fmt.Errorf("%w: linked user", ErrInvalid)
			}
			if status != "active" {
				return fmt.Errorf("%w: linked user", ErrForbidden)
			}
			if !userCanView(ctx, tx, p.UserID, role, projectID) {
				return fmt.Errorf("%w: linked user cannot view project", ErrForbidden)
			}
		} else if p.Kind != PartyManualEmail {
			return fmt.Errorf("%w: party kind", ErrInvalid)
		}
		seen[p.PartyRef] = true
	}
	for _, ref := range []string{req.DeliveryPartyRef, req.OperatorPartyRef} {
		if !seen[ref] {
			return fmt.Errorf("%w: delivery and operator parties must be listed", ErrInvalid)
		}
	}
	if req.SupportPartyRef != nil && *req.SupportPartyRef != "" && !seen[*req.SupportPartyRef] {
		return fmt.Errorf("%w: support party", ErrInvalid)
	}
	for _, ref := range req.RequiredPartyRefs {
		if !seen[ref] {
			return fmt.Errorf("%w: required party", ErrInvalid)
		}
	}
	if !contains(req.RequiredPartyRefs, req.DeliveryPartyRef) || !contains(req.RequiredPartyRefs, req.OperatorPartyRef) {
		return fmt.Errorf("%w: delivery party and future operator must be required", ErrInvalid)
	}
	return nil
}

func userCanView(ctx context.Context, tx *sql.Tx, userID int64, role string, projectID int64) bool {
	if role == "admin" || role == "super_admin" {
		return true
	}
	var level sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT access_level FROM project_members WHERE user_id=? AND project_id=?`, userID, projectID).Scan(&level)
	if role == "external" {
		return level.String == "viewer" || level.String == "editor"
	}
	if role == "member" {
		return !level.Valid || level.String == "editor" || level.String == "viewer"
	}
	return false
}

func validateConfigure(req ConfigureRequest) error {
	switch req.OperatingMode {
	case ModeCustomerOperated, ModeAgencySupported, ModeAgencyOperated:
	default:
		return fmt.Errorf("%w: operating_mode", ErrInvalid)
	}
	if len(req.Parties) == 0 || len(req.RequiredPartyRefs) == 0 {
		return fmt.Errorf("%w: parties", ErrInvalid)
	}
	for _, g := range req.DisclosedGaps {
		if !validOpaqueRef(g.GapRef) || strings.TrimSpace(g.Statement) == "" {
			return fmt.Errorf("%w: disclosed gap", ErrInvalid)
		}
	}
	return nil
}

func computeMissing(acc Acceptance) Missing {
	required := map[string]bool{}
	for _, r := range acc.RequiredPartyRefs {
		required[r] = true
	}
	coverage := map[string]bool{}
	if acc.DeliveryPartyRef != "" {
		coverage[acc.DeliveryPartyRef] = true
		required[acc.DeliveryPartyRef] = true
	}
	if acc.OperatorPartyRef != "" {
		coverage[acc.OperatorPartyRef] = true
		required[acc.OperatorPartyRef] = true
	}
	if acc.SupportPartyRef != nil && *acc.SupportPartyRef != "" {
		coverage[*acc.SupportPartyRef] = true
	}
	for r := range required {
		coverage[r] = true
	}
	confirmed := map[string]bool{}
	for _, c := range acc.Confirmations {
		confirmed[c.PartyRef] = true
	}
	sent := map[string]bool{}
	for _, e := range acc.EmailEvidence {
		if e.State == MailSent && e.SentAt != nil {
			for _, r := range e.RecipientPartyRefs {
				sent[r] = true
			}
		}
	}
	missing := Missing{Confirmations: []string{}, EmailCoverage: []string{}}
	for r := range required {
		if !confirmed[r] {
			missing.Confirmations = append(missing.Confirmations, r)
		}
	}
	for r := range coverage {
		if !sent[r] {
			missing.EmailCoverage = append(missing.EmailCoverage, r)
		}
	}
	sort.Strings(missing.Confirmations)
	sort.Strings(missing.EmailCoverage)
	missing.Preview = acc.ID != 0 && acc.PreviewRevision == 0
	missing.Send = len(missing.EmailCoverage) > 0
	return missing
}

func policyMatches(policy StandingPolicy, acc Acceptance, now time.Time) error {
	if policy.RevokedAt != nil {
		return fmt.Errorf("%w: policy revoked", ErrForbidden)
	}
	expires, err := time.Parse(time.RFC3339, policy.ExpiresAt)
	if err != nil {
		expires, err = time.Parse(time.RFC3339Nano, policy.ExpiresAt)
	}
	if err != nil || !expires.After(now.UTC()) {
		return fmt.Errorf("%w: policy expired", ErrForbidden)
	}
	if policy.ContentDigest != acc.Release.ContentDigest || policy.RevisionSeal != acc.Release.RevisionSeal {
		return fmt.Errorf("%w: policy baseline drift", ErrStale)
	}
	if policy.AgreementRef != acc.AgreementRef {
		return fmt.Errorf("%w: policy agreement drift", ErrStale)
	}
	if string(mustJSON(canonicalGaps(policy.Gaps))) != string(mustJSON(canonicalGaps(acc.DisclosedGaps))) {
		return fmt.Errorf("%w: policy gap drift", ErrStale)
	}
	if policy.ReleaseChannel != "" && policy.ReleaseChannel != acc.Release.ReleaseChannel {
		return fmt.Errorf("%w: policy channel drift", ErrStale)
	}
	if policy.ArtifactDigest != "" && policy.ArtifactDigest != acc.Release.ArtifactDigest {
		return fmt.Errorf("%w: policy artifact drift", ErrStale)
	}
	if strings.TrimSpace(policy.TargetRef) == "" || strings.TrimSpace(acc.DeploymentTarget) == "" {
		return fmt.Errorf("%w: %s", ErrForbidden, targetUnknownUnbound)
	}
	if policy.TargetRef != acc.DeploymentTarget {
		return fmt.Errorf("%w: policy target drift", ErrStale)
	}
	if policy.ModelRef != acc.OperatingMode {
		return fmt.Errorf("%w: policy model drift", ErrStale)
	}
	for _, p := range policy.Parties {
		if !contains(acc.RequiredPartyRefs, p) && !partyListed(acc.Parties, p) {
			return fmt.Errorf("%w: policy party drift", ErrStale)
		}
	}
	return nil
}

func partyListed(parties []Party, ref string) bool {
	_, ok := partyByRef(parties, ref)
	return ok
}

func coverKnownParties(acc Acceptance, refs []string) error {
	if len(refs) == 0 {
		return fmt.Errorf("%w: recipients", ErrInvalid)
	}
	for _, r := range refs {
		if !partyListed(acc.Parties, r) {
			return fmt.Errorf("%w: recipient party", ErrInvalid)
		}
	}
	return nil
}

func partyByRef(parties []Party, ref string) (Party, bool) {
	for _, p := range parties {
		if p.PartyRef == ref {
			return p, true
		}
	}
	return Party{}, false
}

func recipientsForEvidence(ctx context.Context, tx *sql.Tx, evidenceID int64) ([]string, error) {
	var accID, rev int64
	var recipJSON string
	if err := tx.QueryRowContext(ctx, `SELECT acceptance_id,acceptance_revision,recipient_party_refs_json FROM acceptance_email_evidence WHERE id=?`, evidenceID).
		Scan(&accID, &rev, &recipJSON); err != nil {
		return nil, err
	}
	var refs []string
	_ = json.Unmarshal([]byte(recipJSON), &refs)
	parties, err := loadParties(ctx, tx, accID, rev)
	if err != nil {
		return nil, err
	}
	var to []string
	for _, ref := range refs {
		if p, ok := partyByRef(parties, ref); ok {
			to = append(to, p.Email)
		}
	}
	return to, nil
}

func buildRawMessage(acc Acceptance, recipients []string, from string, now time.Time, id string) []byte {
	var to []string
	for _, ref := range recipients {
		if p, ok := partyByRef(acc.Parties, ref); ok {
			addr, err := mail.ParseAddress(p.Email)
			if err != nil {
				continue
			}
			to = append(to, addr.Address)
		}
	}
	fromAddr := mail.Address{Address: from}
	host := "localhost"
	if parsed, err := mail.ParseAddress(from); err == nil {
		fromAddr = *parsed
		if i := strings.LastIndex(parsed.Address, "@"); i >= 0 {
			host = parsed.Address[i+1:]
		}
	}
	msgid := strings.TrimSpace(id)
	msgid = strings.ReplaceAll(msgid, "<", "")
	msgid = strings.ReplaceAll(msgid, ">", "")
	if msgid == "" {
		msgid = uuid.NewString()
	}
	cte, payload := encodeMailBody(acc.PreviewBody)
	return []byte("From: " + fromAddr.String() + "\r\n" +
		"Date: " + now.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n" +
		"Message-ID: <" + msgid + "@" + host + ">\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", acc.PreviewSubject) + "\r\n" +
		"To: " + strings.Join(to, ", ") + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: " + cte + "\r\n" +
		"\r\n" + payload)
}

func encodeMailBody(body string) (cte, payload string) {
	if isSevenBit(body) {
		if !strings.HasSuffix(body, "\r\n") {
			body += "\r\n"
		}
		return "7bit", body
	}
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	_, _ = w.Write([]byte(body))
	if !strings.HasSuffix(body, "\n") {
		_, _ = w.Write([]byte("\r\n"))
	}
	_ = w.Close()
	return "quoted-printable", buf.String()
}

func isSevenBit(s string) bool {
	if strings.ContainsRune(s, 0) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func partyDisplayName(acc Acceptance, ref string) string {
	if p, ok := partyByRef(acc.Parties, ref); ok {
		if strings.TrimSpace(p.DisplayName) != "" {
			return p.DisplayName
		}
		if strings.TrimSpace(p.Email) != "" {
			return p.Email
		}
	}
	return ref
}

func lookslikeHTMLRemote(raw []byte) bool {
	s := strings.ToLower(string(raw))
	return strings.Contains(s, "<img") && (strings.Contains(s, "src=\"http") || strings.Contains(s, "src='http"))
}

func canonicalGaps(gaps []Gap) []Gap {
	out := append([]Gap{}, gaps...)
	sort.Slice(out, func(i, j int) bool { return out[i].GapRef < out[j].GapRef })
	return out
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func contains(in []string, v string) bool {
	for _, x := range in {
		if x == v {
			return true
		}
	}
	return false
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "release_acceptance_not_found")
}

func StartMailDispatcher(database *sql.DB, mail mailer.Mailer) {
	svc := NewService(database, LiveArtifacts{}, mail, nil)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			_ = svc.DrainOnce(context.Background())
		}
	}()
}
