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
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/contracts"
	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/externalstage"
	"github.com/inspr-at/paimos/backend/mailer"
)

const (
	testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSeal   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testArt    = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type stubArtifacts struct {
	id  ArtifactIdentity
	err error
}

func (s stubArtifacts) Load(context.Context, *sql.Tx, int64, int64) (ArtifactIdentity, error) {
	if s.err != nil {
		return ArtifactIdentity{}, s.err
	}
	return s.id, nil
}

type fakeMailer struct {
	mu       sync.Mutex
	messages []mailer.Message
	err      error
}

func (f *fakeMailer) Send(_ context.Context, msg mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, msg)
	return nil
}

type fixture struct {
	t          *testing.T
	svc        *Service
	mail       *fakeMailer
	projectID  int64
	batchID    int64
	admin      Actor
	customer   Actor
	delivery   Actor
	viewer     Actor
	adminID    int64
	customerID int64
	deliveryID int64
	viewerID   int64
}

func restoreEnv(key, prev string) {
	if prev == "" {
		_ = os.Unsetenv(key)
		return
	}
	_ = os.Setenv(key, prev)
}

var (
	schemaSnapshotOnce sync.Once
	schemaSnapshotPath string
	schemaSnapshotErr  error
)

func migratedSchemaSnapshot() (string, error) {
	schemaSnapshotOnce.Do(func() {
		dir, err := os.MkdirTemp("", "paimos-ra-schema-")
		if err != nil {
			schemaSnapshotErr = err
			return
		}
		prevDir := os.Getenv("DATA_DIR")
		prevMode := os.Getenv("PAIMOS_TEST_MODE")
		if schemaSnapshotErr = os.Setenv("DATA_DIR", dir); schemaSnapshotErr != nil {
			return
		}
		if schemaSnapshotErr = os.Setenv("PAIMOS_TEST_MODE", "1"); schemaSnapshotErr != nil {
			restoreEnv("DATA_DIR", prevDir)
			return
		}
		defer func() {
			restoreEnv("DATA_DIR", prevDir)
			restoreEnv("PAIMOS_TEST_MODE", prevMode)
			if appdb.DB != nil {
				_ = appdb.DB.Close()
				appdb.DB = nil
			}
		}()
		if err := appdb.Open(); err != nil {
			schemaSnapshotErr = err
			return
		}
		snap := filepath.Join(dir, "schema-snapshot.db")
		quoted := "'" + strings.ReplaceAll(snap, "'", "''") + "'"
		// #nosec G202 -- snap is a MkdirTemp path owned by this process; not user input.
		if _, err := appdb.DB.Exec(`VACUUM INTO ` + quoted); err != nil {
			schemaSnapshotErr = err
			return
		}
		schemaSnapshotPath = snap
	})
	return schemaSnapshotPath, schemaSnapshotErr
}

func cloneMigratedSchema(destDir string) error {
	src, err := migratedSchemaSnapshot()
	if err != nil {
		return err
	}
	in, err := os.Open(src) // #nosec G304 -- src is the process-local migrated test snapshot.
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(destDir, brand.Default.DBFilename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func openFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("PAIMOS_TEST_MODE", "1")
	prevFrom := brand.Default.EmailFrom
	brand.Default.EmailFrom = "paimos@example.test"
	t.Cleanup(func() { brand.Default.EmailFrom = prevFrom })
	if err := cloneMigratedSchema(dir); err != nil {
		t.Fatal(err)
	}
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if appdb.DB != nil {
			_ = appdb.DB.Close()
			appdb.DB = nil
		}
	})
	f := &fixture{t: t, mail: &fakeMailer{}}
	res, err := appdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('Accept','ACC')`)
	if err != nil {
		t.Fatal(err)
	}
	f.projectID, _ = res.LastInsertId()
	f.adminID, f.admin = f.insertUser("acc-admin", "admin")
	f.customerID, f.customer = f.insertUser("acc-customer", "external")
	f.deliveryID, f.delivery = f.insertUser("acc-delivery", "member")
	f.viewerID, f.viewer = f.insertUser("acc-viewer", "external")
	if _, err := appdb.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'editor')`, f.projectID, f.customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := appdb.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`, f.projectID, f.viewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := appdb.DB.Exec(`INSERT INTO project_cooperation(project_id,report_contract_basis,cooperation_notes) VALUES(?,?,?)`,
		f.projectID, "SOW-9", "old notes are not standing policy"); err != nil {
		t.Fatal(err)
	}
	f.batchID = f.insertBatch()
	art := ArtifactIdentity{
		BatchID: f.batchID, BatchKey: "batch-acc", BaselineRef: "baseline_7",
		ContentDigest: testDigest, RevisionSeal: testSeal, ArtifactDigest: testArt,
		ArtifactCoordinate: "ghcr:inspr-at/demo:acc", VersionScheme: "inspr-calendar-v1",
		ReleaseChannel: "stable", ReleaseSequence: 1, Version: "26.09.08", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	f.svc = NewService(appdb.DB, stubArtifacts{id: art}, f.mail, ClockFunc(func() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) }))
	return f
}

func (f *fixture) insertUser(name, role string) (int64, Actor) {
	f.t.Helper()
	res, err := appdb.DB.Exec(`INSERT INTO users(username,password,role,status,email) VALUES(?,?,?,'active',?)`, name, "x", role, name+"@example.test")
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	cred := uuid.NewString()
	if _, err := appdb.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id) VALUES(?,?,datetime('now','+1 hour'),datetime('now'),?)`,
		"sess-"+name, id, cred); err != nil {
		f.t.Fatal(err)
	}
	return id, Actor{Kind: string(auth.PrincipalSession), UserID: id, SessionCredentialID: cred}
}

func (f *fixture) insertBatch() int64 {
	f.t.Helper()
	issue, err := appdb.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','batch')`, f.projectID)
	if err != nil {
		f.t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	return f.insertBatchForIssue(issueID, nil, nil)
}

func (f *fixture) insertBatchForIssue(issueID int64, deliveryID, attemptID any) int64 {
	f.t.Helper()
	draft, err := appdb.DB.Exec(`INSERT INTO baseline_batch_drafts(
		project_id,revision,status,baseline_ref,baseline_revision,content_digest,revision_seal,stream_ref,
		bounded_content_json,selected_requirement_refs_json,selected_constraint_refs_json,created_by,created_at,updated_at)
		VALUES(?,1,'closed','baseline_7',1,?,?, 'stream_1','{}','[]','[]',?,?,?)`,
		f.projectID, testDigest, testSeal, f.adminID, "2026-09-08T11:00:00Z", "2026-09-08T11:00:00Z")
	if err != nil {
		f.t.Fatal(err)
	}
	draftID, _ := draft.LastInsertId()
	review, err := appdb.DB.Exec(`INSERT INTO baseline_batch_reviews(
		draft_id,draft_revision,binding_hash,execution_mode,worker_json,selected_requirement_refs_json,human_user_id,session_credential_id,created_at)
		VALUES(?,1,'bind','manual','{}','[]',?,?,'2026-09-08T11:00:00Z')`, draftID, f.adminID, f.admin.SessionCredentialID)
	if err != nil {
		f.t.Fatal(err)
	}
	reviewID, _ := review.LastInsertId()
	key := "batch-acc-" + uuid.NewString()
	batch, err := appdb.DB.Exec(`INSERT INTO baseline_batch_batches(
		project_id,batch_key,draft_id,draft_revision,review_id,baseline_ref,content_digest,revision_seal,execution_mode,scope_json,worker_json,
		issue_id,delivery_id,attempt_id,confirmation_json,idempotency_key,started_by,started_at)
		VALUES(?,?,?,1,?,?,?,?,'manual','{}','{}',?,?,?,'{}',?,?,'2026-09-08T11:00:00Z')`,
		f.projectID, key, draftID, reviewID, "baseline_7", testDigest, testSeal, issueID, deliveryID, attemptID, "mint-"+uuid.NewString(), f.adminID)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := batch.LastInsertId()
	return id
}

func (f *fixture) insertProjectEnv(name string) int64 {
	return f.insertProjectEnvDest(name, "", "", "")
}

func (f *fixture) insertProjectEnvDest(name, url, alias, ip string) int64 {
	f.t.Helper()
	res, err := appdb.DB.Exec(`INSERT INTO project_environments(project_id, name, url, host_alias, host_ip, sort_order)
		VALUES(?,?,?,?,?,0)`, f.projectID, name, url, alias, ip)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *fixture) bindProjectEnv(releaseID, envID int64) Acceptance {
	f.t.Helper()
	acc, err := f.svc.BindTarget(context.Background(), f.admin, f.projectID, releaseID, BindTargetRequest{
		Kind: TargetKindProjectEnv, EnvironmentID: envID,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if acc.DeploymentTarget == "" || !strings.HasPrefix(acc.DeploymentTarget, "sha256:") {
		f.t.Fatalf("bind did not store provenance identity: %+v", acc)
	}
	return acc
}

type pharosOwner struct {
	RegistrationID int64
	APIKeyID       int64
	UserID         int64
	DeliveryID     int64
	DeliveryKey    string
	BatchID        int64
}

func (f *fixture) seedPharosOwner(environment string) pharosOwner {
	f.t.Helper()
	var next int64
	if err := appdb.DB.QueryRow(`SELECT COALESCE(MAX(issue_number),0)+1 FROM issues WHERE project_id=?`, f.projectID).Scan(&next); err != nil {
		f.t.Fatal(err)
	}
	issue, err := appdb.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,?,'ticket','pharos-owner')`, f.projectID, next)
	if err != nil {
		f.t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	deliveryKey := fmt.Sprintf("issue:%d", issueID)
	delivery, err := appdb.DB.Exec(`INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, issueID, deliveryKey, f.projectID)
	if err != nil {
		f.t.Fatal(err)
	}
	deliveryID, _ := delivery.LastInsertId()
	username := "pharos-owner-" + uuid.NewString()[:8]
	user, err := appdb.DB.Exec(`INSERT INTO users(username,password,role,status,email) VALUES(?,?,?,'active',?)`,
		username, "x", "member", username+"@example.test")
	if err != nil {
		f.t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'editor')`, f.projectID, userID); err != nil {
		f.t.Fatal(err)
	}
	key, err := appdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,?,?,?,?)`,
		userID, "pharos-owner", strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")[:64], "paimos_pharos", "*")
	if err != nil {
		f.t.Fatal(err)
	}
	apiKeyID, _ := key.LastInsertId()
	stage, err := externalstage.NewService(appdb.DB, externalstage.Options{FixtureDigest: contracts.ExternalStageV1FixtureDigest()})
	if err != nil {
		f.t.Fatal(err)
	}
	reg, err := stage.RegisterReporter(context.Background(), externalstage.Principal{
		UserID: f.adminID, Kind: "session", SessionCredentialID: f.admin.SessionCredentialID,
	}, deliveryKey, "register-pharos-"+uuid.NewString(), externalstage.RegisterReporterRequest{
		APIKeyID: apiKeyID, ReporterClass: externalstage.ReporterClassPharos,
		ReporterRole: externalstage.ReporterRoleOwner, Workflow: "deploy-production", Environment: environment,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return pharosOwner{
		RegistrationID: reg.RegistrationID, APIKeyID: apiKeyID, UserID: userID,
		DeliveryID: deliveryID, DeliveryKey: deliveryKey, BatchID: f.insertBatchForIssue(issueID, deliveryID, nil),
	}
}

func (f *fixture) bindPharos(releaseID, registrationID int64) Acceptance {
	f.t.Helper()
	acc, err := f.svc.BindTarget(context.Background(), f.admin, f.projectID, releaseID, BindTargetRequest{
		Kind: TargetKindPharosOwner, RegistrationID: registrationID,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if acc.DeploymentTarget == "" || !strings.HasPrefix(acc.DeploymentTarget, "sha256:") || acc.DeploymentTargetKind != TargetKindPharosOwner {
		f.t.Fatalf("pharos bind identity=%+v", acc)
	}
	return acc
}

func (f *fixture) mintBatch(batchID int64) Acceptance {
	f.t.Helper()
	acc, err := f.svc.Mint(context.Background(), f.admin, f.projectID, batchID)
	if err != nil {
		f.t.Fatal(err)
	}
	return acc
}

func (f *fixture) revokePharos(deliveryKey string, registrationID int64) {
	f.t.Helper()
	stage, err := externalstage.NewService(appdb.DB, externalstage.Options{FixtureDigest: contracts.ExternalStageV1FixtureDigest()})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := stage.RevokeReporter(context.Background(), externalstage.Principal{
		UserID: f.adminID, Kind: "session", SessionCredentialID: f.admin.SessionCredentialID,
	}, deliveryKey, "revoke-pharos-"+uuid.NewString(), registrationID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) standingPolicy(policyRef, targetRef string, extras ...func(*PolicyRequest)) PolicyRequest {
	req := PolicyRequest{
		PolicyRef:      policyRef,
		ContentDigest:  testDigest,
		RevisionSeal:   testSeal,
		Parties:        []string{"party_customer"},
		AgreementRef:   "SOW-9",
		Gaps:           []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		BoundedUse:     "Same approved baseline implementation updates only.",
		ExpiresAt:      "2026-12-01T00:00:00Z",
		TargetRef:      targetRef,
		ModelRef:       ModeCustomerOperated,
		ReleaseChannel: "stable",
		ArtifactDigest: testArt,
	}
	for _, extra := range extras {
		extra(&req)
	}
	return req
}

func (f *fixture) configure(releaseID int64, mode string) Acceptance {
	f.t.Helper()
	acc, err := f.svc.Configure(context.Background(), f.admin, f.projectID, releaseID, ConfigureRequest{
		OperatingMode:     mode,
		AgreementRef:      "SOW-9",
		DisclosedGaps:     []Gap{{GapRef: "gap_backup", Statement: "Backup restore not proven for this target."}},
		DeliveryPartyRef:  "party_delivery",
		OperatorPartyRef:  "party_customer",
		RequiredPartyRefs: []string{"party_delivery", "party_customer"},
		Parties: []PartyInput{
			{PartyRef: "party_customer", Kind: PartyLinkedUser, UserID: f.customerID, Email: "acc-customer@example.test", DisplayName: "Customer", Roles: []string{"acceptance_party", "operator"}},
			{PartyRef: "party_delivery", Kind: PartyLinkedUser, UserID: f.deliveryID, Email: "acc-delivery@example.test", DisplayName: "Delivery", Roles: []string{"acceptance_party", "delivery_party"}},
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return acc
}

func (f *fixture) mint() Acceptance {
	f.t.Helper()
	acc, err := f.svc.Mint(context.Background(), f.admin, f.projectID, f.batchID)
	if err != nil {
		f.t.Fatal(err)
	}
	return acc
}

func TestFixtureSnapshotClonesStayIsolated(t *testing.T) {
	t.Run("mutate", func(t *testing.T) {
		f := openFixture(t)
		if _, err := appdb.DB.Exec(`UPDATE projects SET name='mutated-acceptance' WHERE id=?`, f.projectID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("fresh", func(t *testing.T) {
		f := openFixture(t)
		var name string
		if err := appdb.DB.QueryRow(`SELECT name FROM projects WHERE id=?`, f.projectID).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "Accept" {
			t.Fatalf("fresh clone reused prior fixture rows: %q", name)
		}
		var versions int
		if err := appdb.DB.QueryRow(`SELECT COUNT(*) FROM schema_versions`).Scan(&versions); err != nil || versions < 180 {
			t.Fatalf("cloned schema versions=%d err=%v", versions, err)
		}
	})
}
