package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func schemaNames(t *testing.T, database *sql.DB, query string) []string {
	t.Helper()
	rows, err := database.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const latestSchemaVersion = 199

func TestMigration198PreservesExistingUsersAndGrants(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m198-upgrade.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 197); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(username,password,role,role_key,status) VALUES
	 ('m198-member','x','member','member','active'),('m198-admin','x','admin','admin','active'),('m198-external','x','external','external','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M198 existing','M198');
	 INSERT INTO project_members(user_id,project_id,access_level) SELECT users.id,projects.id,'editor' FROM users CROSS JOIN projects WHERE users.role='member'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 198); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 198); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	var users, reviewers, grants int
	if err := database.QueryRow("SELECT COUNT(*),SUM(is_reviewer) FROM users").Scan(&users, &reviewers); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM project_members WHERE access_level='editor'").Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if users != 3 || reviewers != 0 || grants != 1 {
		t.Fatalf("existing identity or grants changed: users=%d reviewers=%d grants=%d", users, reviewers, grants)
	}
	var mismatched int
	if err := database.QueryRow("SELECT COUNT(*) FROM users WHERE role != role_key OR status != 'active'").Scan(&mismatched); err != nil || mismatched != 0 {
		t.Fatalf("legacy roles changed: %d %v", mismatched, err)
	}
}

func TestMigration197KeepsRevokedConversationCredentialDedicated(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m197-upgrade.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := migrateThrough(database, 196); err != nil {
		t.Fatalf("migrate through M196: %v", err)
	}
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m197-owner','x','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	project, err := database.Exec(`INSERT INTO projects(name,key,status) VALUES('M197 conversation','M197','active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	key, err := database.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,credential_kind)
		VALUES(?,'m197-conversation',lower(hex(randomblob(32))),'paimos_m197','','general')`, userID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := key.LastInsertId()

	if err := migrateThrough(database, 197); err != nil {
		t.Fatalf("apply M197: %v", err)
	}
	if version, err := CurrentSchemaVersion(database); err != nil || version != 197 {
		t.Fatalf("post-upgrade schema version=%d err=%v", version, err)
	}
	const bindingID = "11111111-1111-4111-8111-111111111111"
	_, err = database.Exec(`INSERT INTO conversation_service_bindings(
		binding_id,revision,api_key_id,project_id,host_id,project_ref,runtime_id,runtime_generation,
		account_label,account_key,attachment_revision,dispatch_profile_id,dispatch_profile_version,
		execution_policy_id,max_input_bytes,max_messages,max_output_bytes,max_event_bytes,max_events,
		max_timeout_ms,created_by,session_credential_id,created_at)
		VALUES(?,1,?,?,'m197-host','m197-project','22222222-2222-4222-8222-222222222222',
		'33333333-3333-4333-8333-333333333333','chatgpt','acct-main',7,'codex-sol-high','1',
		'aithema-conversation-v1',131072,128,262144,8192,512,180000,?,'m197-session',
		'2026-09-14T12:00:00.000Z')`, bindingID, keyID, projectID, userID)
	if err != nil {
		t.Fatalf("insert M197 dedicated binding: %v", err)
	}
	if _, err := database.Exec(`UPDATE api_keys SET disabled_at='2026-09-14T12:01:00.000Z' WHERE id=?`, keyID); err != nil {
		t.Fatalf("revoke dedicated credential: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM conversation_service_bindings WHERE binding_id=?`, bindingID); err == nil {
		t.Fatal("revocation allowed the dedicated binding to be removed")
	}
	var effectiveKind string
	err = database.QueryRow(`SELECT CASE
		WHEN key.credential_kind='general' AND EXISTS(
		 SELECT 1 FROM conversation_service_bindings binding WHERE binding.api_key_id=key.id
		) THEN 'conversation_service' ELSE key.credential_kind END
		FROM api_keys key WHERE key.id=?`, keyID).Scan(&effectiveKind)
	if err != nil || effectiveKind != "conversation_service" {
		t.Fatalf("revoked credential effective kind=%q err=%v", effectiveKind, err)
	}
}

func TestMigration196PreservesM195AndReplacesExternalStageCausalGuard(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m196-upgrade.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := migrateThrough(database, 195); err != nil {
		t.Fatalf("migrate through M195: %v", err)
	}
	if version, err := CurrentSchemaVersion(database); err != nil || version != 195 {
		t.Fatalf("pre-upgrade schema version=%d err=%v", version, err)
	}
	if !tableExists(t, database, "machine_notifier_bindings") {
		t.Fatal("M195 machine notifier binding disappeared before M196")
	}

	if err := migrateThrough(database, 196); err != nil {
		t.Fatalf("apply M196: %v", err)
	}
	if version, err := CurrentSchemaVersion(database); err != nil || version != 196 {
		t.Fatalf("post-upgrade schema version=%d err=%v", version, err)
	}
	var triggerCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type='trigger' AND name='trg_external_stage_handoff_causal_update_guard'`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 1 || !tableExists(t, database, "machine_notifier_bindings") {
		t.Fatalf("M196 trigger count=%d preserved_m195=%t", triggerCount,
			tableExists(t, database, "machine_notifier_bindings"))
	}
}

func TestMigration191AddsClosedOneShotLaunchAuthority(t *testing.T) {
	database := openTestDB(t)
	for _, table := range []string{
		"baseline_batch_launch_grants",
		"baseline_batch_launch_grant_revocations",
		"external_stage_launch_admissions",
	} {
		if !tableExists(t, database, table) {
			t.Fatalf("M191 table %s missing", table)
		}
	}
	if !columnExists(t, database, "external_stage_reporter_registrations", "target_ref") ||
		!columnExists(t, database, "baseline_batch_drafts", "delegated_launch_json") ||
		!columnExists(t, database, "baseline_batch_reviews", "delegated_launch_json") ||
		!columnExists(t, database, "baseline_batch_batches", "delegated_launch_json") {
		t.Fatal("M191 additive target or delegated launch columns missing")
	}
	for _, trigger := range []string{
		"trg_external_stage_registration_target_immutable",
		"trg_baseline_batch_closed_draft_launch_immutable",
		"trg_baseline_batch_review_launch_immutable",
		"trg_baseline_batch_launch_selection_immutable",
		"trg_baseline_batch_launch_grants_identity_immutable",
		"trg_baseline_batch_launch_grants_revoke_guard",
		"trg_baseline_batch_launch_grant_revocations_no_update",
		"trg_external_stage_launch_admissions_identity_immutable",
		"trg_external_stage_launch_admissions_consume_guard",
	} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("M191 trigger %s count=%d err=%v", trigger, count, err)
		}
	}
}

func TestHeldMigrationsFollowPublishedMain190(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "main190-upgrade.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := migrateThrough(database, 190); err != nil {
		t.Fatalf("migrate published main through M190: %v", err)
	}
	if version, err := CurrentSchemaVersion(database); err != nil || version != 190 {
		t.Fatalf("published main schema version=%d err=%v, want 190", version, err)
	}
	if !tableExists(t, database, "offers") || !columnExists(t, database, "external_stage_pharos_evidence_v2", "version_scheme") {
		t.Fatal("published main M187-M190 schema was not preserved")
	}

	if err := migrateThrough(database, latestSchemaVersion); err != nil {
		t.Fatalf("apply held migrations after published main: %v", err)
	}
	for _, table := range []string{"baseline_batch_launch_grants", "external_stage_launch_admissions", "harness_session_retirements"} {
		if !tableExists(t, database, table) {
			t.Fatalf("held additive table %s missing after M190 upgrade", table)
		}
	}
	for version := 187; version <= latestSchemaVersion; version++ {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=?`, version).Scan(&count); err != nil || count != 1 {
			t.Fatalf("schema version %d count=%d err=%v", version, count, err)
		}
	}
}

func TestMigration177PreservesAttentionLedgerAndSequence(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m177.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = migrateThrough(database, 176); err != nil {
		t.Fatal(err)
	}
	res, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Consumer fixture','CFX')`)
	if err != nil {
		t.Fatal(err)
	}
	project, _ := res.LastInsertId()
	res, err = database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'receiver')`, project)
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := res.LastInsertId()
	_, err = database.Exec(`INSERT INTO agent_attention_items(id,receiver_project_id,receiver_project_agent_id,address,source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at) VALUES(42,?,?,'codex:receiver',?,'harness_session_event','fixture-event',1,'worker_unknown','heartbeat_stale',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, project, agent, project)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`UPDATE sqlite_sequence SET seq=99 WHERE name='agent_attention_items'`)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateThrough(database, 177); err != nil {
		t.Fatal(err)
	}
	if err = migrateThrough(database, 177); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	var id, sequence int64
	var source string
	if err = database.QueryRow(`SELECT id,source_id FROM agent_attention_items`).Scan(&id, &source); err != nil || id != 42 || source != "fixture-event" {
		t.Fatal("immutable attention ledger changed")
	}
	if err = database.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='agent_attention_items'`).Scan(&sequence); err != nil || sequence != 99 {
		t.Fatalf("attention sequence=%d error=%v", sequence, err)
	}
	if _, err = database.Exec(`UPDATE agent_attention_items SET reason_code='stopped'`); err == nil {
		t.Fatal("attention immutability lost")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	prevDataDir := os.Getenv("DATA_DIR")
	prevTestMode := os.Getenv("PAIMOS_TEST_MODE")
	t.Cleanup(func() {
		_ = DB.Close()
		DB = nil
		_ = os.Setenv("DATA_DIR", prevDataDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", prevTestMode)
	})

	dataDir := t.TempDir()
	if err := os.Setenv("DATA_DIR", dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		t.Fatal(err)
	}
	if err := Open(); err != nil {
		t.Fatalf("open db: %v", err)
	}
	return DB
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	found, err := SchemaHasTable(db, name)
	if err != nil {
		t.Fatalf("schema_has_table %s: %v", name, err)
	}
	return found
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	found, err := SchemaHasColumn(db, table, column)
	if err != nil {
		t.Fatalf("schema_has_column %s.%s: %v", table, column, err)
	}
	return found
}

func TestSchemaMigrationsReachLatestVersion(t *testing.T) {
	db := openTestDB(t)
	maxVersion, err := CurrentSchemaVersion(db)
	if err != nil {
		t.Fatalf("max schema version: %v", err)
	}
	if maxVersion != latestSchemaVersion {
		t.Fatalf("max schema version=%d want %d", maxVersion, latestSchemaVersion)
	}
	var reservedM164 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_versions WHERE version=164`).Scan(&reservedM164); err != nil {
		t.Fatalf("query reserved M164: %v", err)
	}
	if reservedM164 != 1 {
		t.Fatalf("M164 application count=%d want 1", reservedM164)
	}
}

func TestMigration161AddsDistinctEncryptedReferenceHarnessControlPlane(t *testing.T) {
	database := openTestDB(t)
	for _, table := range []string{"harness_sessions", "harness_session_controls"} {
		if !tableExists(t, database, table) {
			t.Fatalf("M161 table %s missing", table)
		}
	}
	if tableExists(t, database, "managed_harness_sessions") {
		t.Fatal("M161 must use the additive harness_sessions resource, not a reused managed-control table")
	}
	var plaintextColumn int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('harness_sessions') WHERE name='harness_session_id'`).Scan(&plaintextColumn); err != nil || plaintextColumn != 0 {
		t.Fatalf("plaintext harness_session_id column count=%d err=%v", plaintextColumn, err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Managed harnesses','MHS')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	const targetID = "target-managed-harness-worker-v1"
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
	 VALUES(?,'default',?,'codex:worker','managed_harness','harness_session',randomblob(29),'steer','primary',1,1)`, targetID, projectID); err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,message_target_id,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES('11111111-1111-4111-8111-111111111111',?,?,?,?,?,zeroblob(32),zeroblob(32),?,?,?, ?,1,1,1,1,1,'working')`,
		projectID, agentID, "worker", "codex", "mbp0", targetID, "managed", "worker", "owned")
	if err != nil {
		t.Fatalf("valid managed session rejected: %v", err)
	}
	if _, err := database.Exec(`UPDATE harness_sessions
		SET phase='stopped',activity_state='dead',activity_reason='stopped',closed_reason='stopped'
		WHERE id='11111111-1111-4111-8111-111111111111'`); err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,message_target_id,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES('55555555-5555-4555-8555-555555555555',?,?,?,?,?,zeroblob(32),zeroblob(32),?,?,?, ?,1,1,1,1,1,'working')`,
		projectID, agentID, "worker", "codex", "mbp0", targetID, "managed", "worker", "owned")
	if err != nil {
		t.Fatalf("replacement generation rejected: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker2')`, projectID); err != nil {
		t.Fatal(err)
	}
	var worker2ID int64
	if err := database.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='worker2'`, projectID).Scan(&worker2ID); err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES('66666666-6666-4666-8666-666666666666',?,?,?,?,?,zeroblob(32),zeroblob(32),?,?,?,0,1,0,0,0,'working')`,
		projectID, worker2ID, "worker2", "codex", "mbp0", "managed", "worker", "none")
	if err == nil {
		t.Fatal("second active generation with the same stable identity was accepted")
	}
	otherProject, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Other harnesses','OHS')`)
	if err != nil {
		t.Fatal(err)
	}
	otherProjectID, _ := otherProject.LastInsertId()
	otherAgent, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, otherProjectID)
	if err != nil {
		t.Fatal(err)
	}
	otherAgentID, _ := otherAgent.LastInsertId()
	_, err = database.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES('44444444-4444-4444-8444-444444444444',?,?,?,?,?,randomblob(32),randomblob(32),'managed','worker','none',0,1,0,0,0,'working')`,
		projectID, otherAgentID, "worker", "other", "mbp-x")
	if err == nil {
		t.Fatal("cross-project project_agent attribution accepted")
	}
	_, err = database.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES('22222222-2222-4222-8222-222222222222',?,?,?,?,?,randomblob(32),randomblob(32),?,?,?,1,1,1,0,0,'working')`,
		projectID, agentID, "worker", "claude", "mbp1", "unmanaged", "coordinator", "codex_external")
	if err == nil {
		t.Fatal("unmanaged Claude session claimed steer")
	}
	_, err = database.Exec(`INSERT INTO harness_sessions(
		id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES('33333333-3333-4333-8333-333333333333',?,?,?,?,?,randomblob(32),randomblob(32),?,?,?,1,1,0,1,0,'working')`,
		projectID, agentID, "worker", "codex", "mbp2", "unmanaged", "worker", "none")
	if err == nil {
		t.Fatal("unmanaged session claimed owned interrupt")
	}
}

func TestMigration152BackfillsCanonicalAgentMessageEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m152-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 151); err != nil {
		t.Fatalf("create exact M151 fixture: %v", err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Messages','MSG')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	sender, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?, 'sender')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?, 'receiver')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,delivered,delivered_at) VALUES(?,?,'hello',1,datetime('now'))`, senderID, receiverID); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("M151 to M152: %v", err)
	}
	var messageID, contextID, from, to, parts string
	if err := database.QueryRow(`SELECT message_id,context_id,from_address,to_address,parts_json FROM agent_messages`).Scan(&messageID, &contextID, &from, &to, &parts); err != nil {
		t.Fatal(err)
	}
	if messageID != "legacy-1" || contextID != "MSG" || from != "paimos:sender" || to != "paimos:receiver" || parts != `[{"kind":"text","text":"hello"}]` {
		t.Fatalf("unexpected envelope backfill: %q %q %q %q %s", messageID, contextID, from, to, parts)
	}
}

func TestMigration153AddsDurableMessageCursorsWithoutRewritingLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m153-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 152); err != nil {
		t.Fatalf("create exact M152 fixture: %v", err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Cursors','CUR')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	sender, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID)
	receiver, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'receiver')`, projectID)
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,parts_json,from_address,to_address,thread_id)
		VALUES(?,?,'hello',1,datetime('now'),'msg-1','CUR','[{"kind":"text","text":"hello"}]','paimos:sender','codex:receiver','msg-1')`, senderID, receiverID); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("M152 to M153: %v", err)
	}
	if !columnExists(t, database, "agent_messages", "read_at") || !tableExists(t, database, "agent_message_cursors") {
		t.Fatal("M153 cursor schema missing")
	}
	var body string
	if err := database.QueryRow(`SELECT body FROM agent_messages WHERE message_id='msg-1'`).Scan(&body); err != nil || body != "hello" {
		t.Fatalf("ledger row changed: body=%q err=%v", body, err)
	}
}

func TestMigration154BackfillsSimpleIntentAndAddsAgentBusState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m154-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 153); err != nil {
		t.Fatalf("create exact M153 fixture: %v", err)
	}
	project, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Bus migration','BUS')`)
	projectID, _ := project.LastInsertId()
	sender, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID)
	receiver, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'receiver')`, projectID)
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,parts_json,from_address,to_address,thread_id)
		VALUES(?,?,'legacy intent',1,datetime('now'),'legacy-bus','BUS','[{"kind":"text","text":"legacy intent"}]','paimos:sender','codex:receiver','legacy-bus')`, senderID, receiverID); err != nil {
		t.Fatal(err)
	}
	if err := migrate(database); err != nil {
		t.Fatalf("M153 to M154: %v", err)
	}
	for _, table := range []string{"agent_message_targets", "agent_message_deliveries", "agent_message_idempotency"} {
		if !tableExists(t, database, table) {
			t.Fatalf("M154 table %s missing", table)
		}
	}
	var level, fallback string
	if err := database.QueryRow(`SELECT delivery_level,delivery_fallback FROM agent_messages WHERE message_id='legacy-bus'`).Scan(&level, &fallback); err != nil {
		t.Fatal(err)
	}
	if level != "simple" || fallback != "simple" {
		t.Fatalf("legacy intent level=%q fallback=%q", level, fallback)
	}
}

func TestMigration150PreservesRunsAndPinsSourceFreeImplementationDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m150-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 149); err != nil {
		t.Fatalf("create exact M149 fixture: %v", err)
	}
	user, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m150-runner','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M150','M150')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,1,'M150 issue')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'running',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()

	if err := migrate(database); err != nil {
		t.Fatalf("M149 to M150: %v", err)
	}
	var status, digest string
	if err := database.QueryRow(`SELECT status,implementation_result_digest FROM agent_runs WHERE id=?`, runID).
		Scan(&status, &digest); err != nil {
		t.Fatal(err)
	}
	if status != "running" || digest != "" {
		t.Fatalf("M150 changed legacy run: status=%q digest=%q", status, digest)
	}
	valid := strings.Repeat("a", 64)
	if _, err := database.Exec(`UPDATE agent_runs SET status='tests_passed',implementation_result_digest=? WHERE id=?`, valid, runID); err != nil {
		t.Fatalf("valid digest transition rejected: %v", err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET implementation_result_digest=? WHERE id=?`, strings.Repeat("b", 64), runID); err == nil || !strings.Contains(err.Error(), "invalid implementation result digest transition") {
		t.Fatalf("digest rewrite error=%v", err)
	}
	legacy, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'running',0)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	legacyID, _ := legacy.LastInsertId()
	if _, err := database.Exec(`UPDATE agent_runs SET status='tests_passed',implementation_result_digest=? WHERE id=?`, valid, legacyID); err == nil || !strings.Contains(err.Error(), "invalid implementation result digest transition") {
		t.Fatalf("legacy digest transition error=%v", err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='failed' WHERE id=?`, legacyID); err != nil {
		t.Fatal(err)
	}
	for name, invalid := range map[string]string{
		"short": strings.Repeat("a", 63), "uppercase": strings.Repeat("A", 64), "nonhex": strings.Repeat("g", 64),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
				VALUES(?,?,?,'running',1)`, issueID, projectID, userID)
			if err != nil {
				t.Fatal(err)
			}
			candidateID, _ := result.LastInsertId()
			if _, err := database.Exec(`UPDATE agent_runs SET status='tests_passed',implementation_result_digest=? WHERE id=?`, invalid, candidateID); err == nil {
				t.Fatalf("malformed digest %q persisted", invalid)
			}
			if _, err := database.Exec(`UPDATE agent_runs SET status='failed' WHERE id=?`, candidateID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSchemaIntakeTables(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"status", "language", "transcript", "rev", "pinned_project_id"} {
		if !columnExists(t, db, "intake_sessions", col) {
			t.Fatalf("expected intake_sessions.%s to exist (PAI-704 / M134)", col)
		}
	}
	for _, col := range []string{"session_id", "seq", "kind", "source", "payload_json"} {
		if !columnExists(t, db, "intake_events", col) {
			t.Fatalf("expected intake_events.%s to exist (PAI-704 / M134)", col)
		}
	}
}

func TestSchemaAgentRunsClaimedByColumn(t *testing.T) {
	db := openTestDB(t)
	if !columnExists(t, db, "agent_runs", "claimed_by") {
		t.Fatal("expected agent_runs.claimed_by to exist (PAI-624 / M128)")
	}
}

func TestSchemaAgentRunsCommitEvidenceColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"repo_url", "branch_name", "commit_base_sha", "commit_sha"} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-702 / M140)", col)
		}
	}
}

func TestSchemaAgentRunTelemetryTables(t *testing.T) {
	database := openTestDB(t)
	for _, table := range []string{"agent_run_telemetry", "agent_run_telemetry_latest"} {
		if !tableExists(t, database, table) {
			t.Fatalf("expected %s to exist (PAI-799 / M142)", table)
		}
	}
	for _, col := range []string{"sequence", "correlation_id", "provider", "adapter", "server_received_at", "progress_percent", "estimate_confidence"} {
		if !columnExists(t, database, "agent_run_telemetry", col) {
			t.Fatalf("expected agent_run_telemetry.%s to exist (PAI-799 / M142)", col)
		}
	}
	for _, col := range []string{"heartbeat_telemetry_id", "semantic_telemetry_id", "estimate_telemetry_id", "latest_event_at", "latest_semantic_at", "latest_estimate_at"} {
		if !columnExists(t, database, "agent_run_telemetry_latest", col) {
			t.Fatalf("expected agent_run_telemetry_latest.%s to exist (PAI-801 / M143)", col)
		}
	}
	if !columnExists(t, database, "agent_runs", "expects_supervisor_telemetry") {
		t.Fatal("expected agent_runs.expects_supervisor_telemetry to exist (PAI-801 / M143)")
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Completed','CMP')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','Completed')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'completed')`, issueID, projectID); err != nil {
		t.Fatalf("completed must satisfy the M143 status CHECK: %v", err)
	}
	var fkTable string
	if err := database.QueryRow(`SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&fkTable); err != sql.ErrNoRows {
		t.Fatalf("M143 foreign-key check found table=%q err=%v", fkTable, err)
	}
}

func TestMigration143UpgradesPopulatedM142WithoutLosingGraphOrTelemetry(t *testing.T) {
	database := openTestDB(t)
	admin, err := database.Exec(`INSERT INTO users(username,password,role,status) VALUES('m143-admin','hash','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	adminID, _ := admin.LastInsertId()
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('M143 fixture','MFX')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,1,'ticket','upgrade fixture','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	draft, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,claimed_by,status,agent_name,session_id,version,tests_summary,deploy_target,error,action_key,provider_kind,provider_id,provider_label,model,run_mode,profile_id,effort,prompt_preset_ref,context_pack,context_truncated,context_sources_json,prompt_tokens,completion_tokens,finish_reason,repo_url,branch_name,commit_base_sha,commit_sha)
		VALUES(?,?,?,?, 'drafted','planner','session-draft','1.2.3','draft only','','','openrouter.draft','hosted','openrouter','OpenRouter','model-a','draft','profile-a','high','preset-a','full',1,'[{"source":"issue"}]',11,22,'stop','https://example.test/repo','draft-branch',?,?)`,
		issueID, projectID, adminID, adminID, strings.Repeat("a", 40), strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	draftID, _ := draft.LastInsertId()
	followup, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,claimed_by,status,agent_name,session_id,action_key,provider_kind,provider_id,provider_label,run_mode,source_draft_run_id,repo_url,branch_name,commit_base_sha,commit_sha)
		VALUES(?,?,?,?,'running','implementer','session-run','claude_cli.implement','local_cli','claude_cli','Claude Code','edit',?,'https://example.test/repo','feature/m143',?,?)`,
		issueID, projectID, adminID, adminID, draftID, strings.Repeat("b", 40), strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	followupID, _ := followup.LastInsertId()
	if _, err := database.Exec(`UPDATE agent_runs SET followup_run_id=? WHERE id=?`, followupID, draftID); err != nil {
		t.Fatal(err)
	}
	telemetry, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat,phase)
		VALUES(?,1,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:00Z','2026-08-20T10:00:01Z','heartbeat',1,'implementing')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	telemetryID, _ := telemetry.LastInsertId()
	semantic, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,activity)
		VALUES(?,2,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:02Z','2026-08-20T10:00:02Z','phase','implementing','editing')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	semanticID, _ := semantic.LastInsertId()
	estimate, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,activity,estimate_revision,progress_percent,eta_seconds,eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis)
		VALUES(?,3,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:03Z','2026-08-20T10:00:03Z','progress','testing','testing',1,50,300,240,360,'adapter',0.8,'half the checks passed')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	estimateID, _ := estimate.LastInsertId()
	latestHeartbeat, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat,phase)
		VALUES(?,4,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:04Z','2026-08-20T10:00:04Z','heartbeat',1,'testing')`, followupID)
	if err != nil {
		t.Fatal(err)
	}
	latestHeartbeatID, _ := latestHeartbeat.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry_latest(run_id,telemetry_id,sequence,last_heartbeat_at,heartbeat_telemetry_id,latest_event_at)
		VALUES(?,?,1,'2026-08-20T10:00:01Z',?,'2026-08-20T10:00:01Z')`, followupID, telemetryID, telemetryID); err != nil {
		t.Fatal(err)
	}

	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture rewinds only the agent-run tables. Drop every later trigger
	// whose SQL references agent_runs before the table rebuild; otherwise an
	// M145 cross-table guard makes SQLite validate a deliberately absent column
	// midway through the exact M142 reconstruction.
	for _, trigger := range schemaNames(t, database, `SELECT name FROM sqlite_master
		WHERE type='trigger' AND sql LIKE '%agent_runs%'`) {
		quoted := strings.ReplaceAll(trigger, `"`, `""`)
		if _, err := tx.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS "`+quoted+`"`); err != nil {
			_ = tx.Rollback()
			t.Fatalf("drop post-M142 trigger %q: %v", trigger, err)
		}
	}
	steps := []string{
		`DROP TRIGGER IF EXISTS trg_agent_run_telemetry_terminal_guard`,
		`DROP INDEX IF EXISTS idx_agent_runs_supervisor_active`,
		`DROP INDEX IF EXISTS idx_agent_run_telemetry_latest_heartbeat`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN latest_estimate_at`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN latest_semantic_at`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN latest_event_at`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN estimate_telemetry_id`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN semantic_telemetry_id`,
		`ALTER TABLE agent_run_telemetry_latest DROP COLUMN heartbeat_telemetry_id`,
		`CREATE TABLE agent_runs_m142_fixture (
			id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			project_id INTEGER REFERENCES projects(id) ON DELETE SET NULL, device_id TEXT NOT NULL DEFAULT '',
			requested_by INTEGER REFERENCES users(id) ON DELETE SET NULL, agent_name TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','tests_passed','tests_failed','deployed','failed','cancelled','drafted')),
			version TEXT NOT NULL DEFAULT '', tests_summary TEXT, deploy_target TEXT NOT NULL DEFAULT '', log_attachment_id INTEGER,
			error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (datetime('now')), started_at TEXT, finished_at TEXT,
			claimed_by INTEGER REFERENCES users(id) ON DELETE SET NULL, action_key TEXT NOT NULL DEFAULT 'claude_cli.implement',
			provider_kind TEXT NOT NULL DEFAULT 'local_cli', provider_id TEXT NOT NULL DEFAULT 'claude_cli', provider_label TEXT NOT NULL DEFAULT 'Claude Code',
			model TEXT NOT NULL DEFAULT '', run_mode TEXT NOT NULL DEFAULT 'edit', profile_id TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL DEFAULT '',
			prompt_preset_ref TEXT NOT NULL DEFAULT '', context_pack TEXT NOT NULL DEFAULT '', context_truncated INTEGER NOT NULL DEFAULT 0,
			context_sources_json TEXT NOT NULL DEFAULT '', prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
			finish_reason TEXT NOT NULL DEFAULT '', source_draft_run_id INTEGER REFERENCES agent_runs_m142_fixture(id) ON DELETE SET NULL,
			followup_run_id INTEGER REFERENCES agent_runs_m142_fixture(id) ON DELETE SET NULL, repo_url TEXT NOT NULL DEFAULT '', branch_name TEXT NOT NULL DEFAULT '',
			commit_base_sha TEXT NOT NULL DEFAULT '', commit_sha TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO agent_runs_m142_fixture SELECT id,issue_id,project_id,device_id,requested_by,agent_name,session_id,status,version,tests_summary,deploy_target,log_attachment_id,error,created_at,started_at,finished_at,claimed_by,action_key,provider_kind,provider_id,provider_label,model,run_mode,profile_id,effort,prompt_preset_ref,context_pack,context_truncated,context_sources_json,prompt_tokens,completion_tokens,finish_reason,source_draft_run_id,followup_run_id,repo_url,branch_name,commit_base_sha,commit_sha FROM agent_runs`,
		`DROP TABLE agent_runs`,
		`ALTER TABLE agent_runs_m142_fixture RENAME TO agent_runs`,
		`CREATE INDEX idx_agent_runs_issue ON agent_runs(issue_id)`,
		`CREATE INDEX idx_agent_runs_status ON agent_runs(status)`,
		`CREATE UNIQUE INDEX idx_agent_runs_active_issue ON agent_runs(issue_id) WHERE status IN ('queued','running')`,
		`CREATE INDEX idx_agent_runs_claimed_by ON agent_runs(claimed_by)`,
		`CREATE INDEX idx_agent_runs_action_key ON agent_runs(action_key)`,
		`CREATE INDEX idx_agent_runs_run_mode ON agent_runs(run_mode)`,
		`CREATE INDEX idx_agent_runs_provider_id ON agent_runs(provider_id)`,
		`CREATE INDEX idx_agent_runs_source_draft ON agent_runs(source_draft_run_id)`,
		`CREATE INDEX idx_agent_runs_followup ON agent_runs(followup_run_id)`,
		`CREATE TRIGGER trg_agent_run_telemetry_terminal_guard BEFORE INSERT ON agent_run_telemetry
		 WHEN (SELECT status FROM agent_runs WHERE id=NEW.run_id) IN ('tests_passed','tests_failed','deployed','failed','cancelled','drafted')
		 BEGIN SELECT RAISE(ABORT, 'terminal run telemetry is immutable'); END`,
		`UPDATE sqlite_sequence SET seq=50 WHERE name='agent_runs'`,
		`DELETE FROM schema_versions WHERE version IN (143,144)`,
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(context.Background(), step); err != nil {
			_ = tx.Rollback()
			t.Fatalf("prepare M142 fixture step %q: %v", step, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	m142Columns := schemaNames(t, database, `SELECT name FROM pragma_table_info('agent_runs')`)
	m142Indexes := schemaNames(t, database, `SELECT name FROM pragma_index_list('agent_runs') WHERE origin='c'`)
	exactM142Columns := []string{
		"id", "issue_id", "project_id", "device_id", "requested_by", "agent_name", "session_id", "status", "version", "tests_summary",
		"deploy_target", "log_attachment_id", "error", "created_at", "started_at", "finished_at", "claimed_by", "action_key", "provider_kind",
		"provider_id", "provider_label", "model", "run_mode", "profile_id", "effort", "prompt_preset_ref", "context_pack", "context_truncated",
		"context_sources_json", "prompt_tokens", "completion_tokens", "finish_reason", "source_draft_run_id", "followup_run_id", "repo_url",
		"branch_name", "commit_base_sha", "commit_sha",
	}
	exactM142Indexes := []string{
		"idx_agent_runs_issue", "idx_agent_runs_status", "idx_agent_runs_active_issue", "idx_agent_runs_claimed_by", "idx_agent_runs_action_key",
		"idx_agent_runs_run_mode", "idx_agent_runs_provider_id", "idx_agent_runs_source_draft", "idx_agent_runs_followup",
	}
	sort.Strings(exactM142Columns)
	sort.Strings(exactM142Indexes)
	if strings.Join(m142Columns, "\x00") != strings.Join(exactM142Columns, "\x00") || strings.Join(m142Indexes, "\x00") != strings.Join(exactM142Indexes, "\x00") {
		t.Fatalf("fixture is not exact M142 schema: columns=%v indexes=%v", m142Columns, m142Indexes)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	DB = nil
	if err := Open(); err != nil {
		t.Fatalf("restart applying M143→M144: %v", err)
	}
	database = DB

	wantColumns := append(append([]string(nil), m142Columns...), "expects_supervisor_telemetry", "delivery_instrumentation_version")
	sort.Strings(wantColumns)
	if got := schemaNames(t, database, `SELECT name FROM pragma_table_info('agent_runs')`); strings.Join(got, "\x00") != strings.Join(wantColumns, "\x00") {
		t.Fatalf("M143→M144 columns=%v want M142+intentional=%v", got, wantColumns)
	}
	wantIndexes := append(append([]string(nil), m142Indexes...), "idx_agent_runs_supervisor_active", "idx_agent_runs_id_issue", "idx_agent_runs_delivery_legacy_active")
	sort.Strings(wantIndexes)
	if got := schemaNames(t, database, `SELECT name FROM pragma_index_list('agent_runs') WHERE origin='c'`); strings.Join(got, "\x00") != strings.Join(wantIndexes, "\x00") {
		t.Fatalf("M143→M144 indexes=%v want M142+intentional=%v", got, wantIndexes)
	}
	var gotFollowup, gotSource sql.NullInt64
	var contextJSON, repoURL, branch, baseSHA, headSHA string
	if err := database.QueryRow(`SELECT followup_run_id,context_sources_json,repo_url,branch_name,commit_base_sha,commit_sha FROM agent_runs WHERE id=?`, draftID).
		Scan(&gotFollowup, &contextJSON, &repoURL, &branch, &baseSHA, &headSHA); err != nil {
		t.Fatal(err)
	}
	if !gotFollowup.Valid || gotFollowup.Int64 != followupID || contextJSON != `[{"source":"issue"}]` || repoURL != "https://example.test/repo" || branch != "draft-branch" || baseSHA != strings.Repeat("a", 40) || headSHA != strings.Repeat("b", 40) {
		t.Fatalf("draft row not preserved: followup=%v context=%q repo=%q branch=%q base=%q head=%q", gotFollowup, contextJSON, repoURL, branch, baseSHA, headSHA)
	}
	if err := database.QueryRow(`SELECT source_draft_run_id FROM agent_runs WHERE id=?`, followupID).Scan(&gotSource); err != nil || !gotSource.Valid || gotSource.Int64 != draftID {
		t.Fatalf("followup source=%v err=%v", gotSource, err)
	}
	var childRun, latestTelemetry, heartbeatTelemetry, semanticTelemetry, estimateTelemetry int64
	if err := database.QueryRow(`SELECT t.run_id,l.telemetry_id,l.heartbeat_telemetry_id,l.semantic_telemetry_id,l.estimate_telemetry_id FROM agent_run_telemetry t JOIN agent_run_telemetry_latest l ON l.run_id=t.run_id WHERE t.id=?`, telemetryID).
		Scan(&childRun, &latestTelemetry, &heartbeatTelemetry, &semanticTelemetry, &estimateTelemetry); err != nil || childRun != followupID || latestTelemetry != latestHeartbeatID || heartbeatTelemetry != latestHeartbeatID || semanticTelemetry != latestHeartbeatID || estimateTelemetry != estimateID {
		t.Fatalf("rebuilt telemetry pointers run=%d latest=%d heartbeat=%d semantic=%d estimate=%d err=%v (seed semantic=%d)", childRun, latestTelemetry, heartbeatTelemetry, semanticTelemetry, estimateTelemetry, err, semanticID)
	}
	var fkTable string
	if err := database.QueryRow(`SELECT "table" FROM pragma_foreign_key_check LIMIT 1`).Scan(&fkTable); err != sql.ErrNoRows {
		t.Fatalf("foreign_key_check table=%q err=%v", fkTable, err)
	}
	newRun, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'completed')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	newID, _ := newRun.LastInsertId()
	if newID <= 50 {
		t.Fatalf("sqlite_sequence regressed: next id=%d", newID)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='tests_passed',tests_summary='fixture tests passed' WHERE id=?`, followupID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind)
		VALUES(?,2,'fixture-correlation','anthropic','claude-code','2026-08-20T10:00:02Z','2026-08-20T10:00:02Z','phase')`, followupID); err == nil || !strings.Contains(err.Error(), "terminal run telemetry") {
		t.Fatalf("M143 terminal guard error=%v", err)
	}
}

func TestSchemaAgentRunTelemetryAppendOnlyAndTerminalGuards(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name, key) VALUES('Telemetry', 'TEL')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, 1, 'ticket', 'Telemetry')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id, project_id, status) VALUES(?, ?, 'running')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	_, err = database.Exec(`INSERT INTO agent_run_telemetry(
		run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind, heartbeat)
		VALUES(?, 1, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:00Z', '2026-08-20T10:00:01Z', 'heartbeat', 1)`, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_run_telemetry SET phase='testing' WHERE run_id=?`, runID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("append-only update error=%v", err)
	}
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
		run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind)
		VALUES(?, 1, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:02Z', '2026-08-20T10:00:02Z', 'progress')`, runID); err == nil || !strings.Contains(err.Error(), "sequence is not monotonic") {
		t.Fatalf("monotonic insert error=%v", err)
	}
	terminalStatuses := []string{"completed", "tests_passed", "tests_failed", "deployed", "failed", "cancelled", "drafted"}
	for i, status := range terminalStatuses {
		terminalRunID := runID
		if i > 0 {
			issue, err := database.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, ?, 'ticket', ?)`, projectID, i+1, "Telemetry "+status)
			if err != nil {
				t.Fatal(err)
			}
			terminalIssueID, _ := issue.LastInsertId()
			run, err := database.Exec(`INSERT INTO agent_runs(issue_id, project_id, status) VALUES(?, ?, 'running')`, terminalIssueID, projectID)
			if err != nil {
				t.Fatal(err)
			}
			terminalRunID, _ = run.LastInsertId()
			if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
				run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind, heartbeat)
				VALUES(?, 1, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:00Z', '2026-08-20T10:00:01Z', 'heartbeat', 1)`, terminalRunID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.Exec(`UPDATE agent_runs SET status=? WHERE id=?`, status, terminalRunID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
			run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind)
			VALUES(?, 2, 'run-1', 'anthropic', 'claude-code', '2026-08-20T10:00:03Z', '2026-08-20T10:00:03Z', 'progress')`, terminalRunID); err == nil || !strings.Contains(err.Error(), "terminal run telemetry") {
			t.Fatalf("status %s terminal insert error=%v", status, err)
		}
	}
}

func TestSchemaAgentRunTelemetryUTF8ByteBounds(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Telemetry bytes','TBY')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','UTF-8 bounds')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'running')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	insert := func(sequence int, activity, basis string) error {
		_, err := database.Exec(`INSERT INTO agent_run_telemetry(
			run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,phase,activity,
			estimate_revision,progress_percent,eta_seconds,eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis)
			VALUES(?,?,'bytes-1','paimos','test','2026-08-20T10:00:00Z','2026-08-20T10:00:00Z','progress','testing',?, ?,50,300,240,360,'adapter',0.8,?)`,
			runID, sequence, activity, sequence, basis)
		return err
	}
	if err := insert(1, strings.Repeat("é", 140), strings.Repeat("é", 120)); err != nil {
		t.Fatalf("exact UTF-8 byte bounds rejected: %v", err)
	}
	if err := insert(2, strings.Repeat("é", 141), "valid basis"); err == nil {
		t.Fatal("281-byte activity passed the storage boundary")
	}
	if err := insert(2, "valid activity", strings.Repeat("é", 121)); err == nil {
		t.Fatal("242-byte estimate basis passed the storage boundary")
	}
	var tableSQL, triggerSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_run_telemetry'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_agent_run_telemetry_byte_bounds'`).Scan(&triggerSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "length(CAST(activity AS BLOB))") || !strings.Contains(tableSQL, "length(CAST(estimate_basis AS BLOB))") || !strings.Contains(triggerSQL, "CAST(NEW.activity AS BLOB)") {
		t.Fatalf("byte-bound schema missing: table=%q trigger=%q", tableSQL, triggerSQL)
	}
}

func TestMigration143PreconditionRejectsLegacyCodePointBoundRows(t *testing.T) {
	legacy, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	legacy.SetMaxOpenConns(1)
	if _, err := legacy.Exec(`CREATE TABLE agent_run_telemetry(
		id INTEGER PRIMARY KEY, activity TEXT NOT NULL CHECK(length(activity)<=280),
		estimate_basis TEXT NOT NULL CHECK(length(estimate_basis)<=240))`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO agent_run_telemetry(id,activity,estimate_basis) VALUES(1,?,'')`, strings.Repeat("é", 141)); err != nil {
		t.Fatalf("legacy code-point constraint should admit the fixture: %v", err)
	}
	conn, err := legacy.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := checkAgentRunTelemetryByteBounds(context.Background(), conn); err == nil || !strings.Contains(err.Error(), "activity_bytes=282") {
		t.Fatalf("M143 precondition error=%v", err)
	}
}

func TestRebuildAgentRunTelemetryLatestMatchesIncrementalProjection(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Projection rebuild','PRB')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	type projectionRow struct {
		runID, telemetryID, sequence               int64
		lastHeartbeatAt                            sql.NullString
		heartbeatID, semanticID, estimateID        sql.NullInt64
		latestEventAt, latestSemanticAt, latestETA sql.NullString
	}
	readProjection := func() []projectionRow {
		rows, err := database.Query(`SELECT run_id,telemetry_id,sequence,last_heartbeat_at,heartbeat_telemetry_id,
			semantic_telemetry_id,estimate_telemetry_id,latest_event_at,latest_semantic_at,latest_estimate_at
			FROM agent_run_telemetry_latest ORDER BY run_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []projectionRow
		for rows.Next() {
			var row projectionRow
			if err := rows.Scan(&row.runID, &row.telemetryID, &row.sequence, &row.lastHeartbeatAt,
				&row.heartbeatID, &row.semanticID, &row.estimateID, &row.latestEventAt,
				&row.latestSemanticAt, &row.latestETA); err != nil {
				t.Fatal(err)
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	type eventSpec struct {
		sequence         int64
		kind, phase      string
		heartbeat        bool
		activity         string
		estimateRevision *int64
	}
	var runIDs []int64
	estimateRevision := int64(1)
	for issueNumber, events := range [][]eventSpec{
		{
			{sequence: 1, kind: "heartbeat", phase: "starting", heartbeat: true},
			{sequence: 2, kind: "phase", phase: "implementing", activity: "editing"},
			{sequence: 3, kind: "progress", phase: "testing", activity: "testing", estimateRevision: &estimateRevision},
			{sequence: 4, kind: "heartbeat", phase: "testing", heartbeat: true},
		},
		{{sequence: 1, kind: "phase", phase: "planning", activity: "planning"}},
	} {
		issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,?,'ticket','Projection')`, projectID, issueNumber+1)
		if err != nil {
			t.Fatal(err)
		}
		issueID, _ := issue.LastInsertId()
		run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'running')`, issueID, projectID)
		if err != nil {
			t.Fatal(err)
		}
		runID, _ := run.LastInsertId()
		runIDs = append(runIDs, runID)
		for _, event := range events {
			receivedAt := "2026-08-20T10:00:0" + strconv.FormatInt(event.sequence, 10) + "Z"
			res, err := database.Exec(`INSERT INTO agent_run_telemetry(
				run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat,phase,activity,
				estimate_revision,progress_percent,eta_seconds,eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis)
				VALUES(?,?,'projection-1','paimos','test',?,?,?, ?,?,?, ?,?,?,?,?,?,?,?)`,
				runID, event.sequence, receivedAt, receivedAt, event.kind, event.heartbeat, event.phase, event.activity,
				event.estimateRevision, nullableEstimateFloat(event.estimateRevision, 50), nullableEstimateInt(event.estimateRevision, 300),
				nullableEstimateInt(event.estimateRevision, 240), nullableEstimateInt(event.estimateRevision, 360),
				nullableEstimateString(event.estimateRevision, "adapter"), nullableEstimateFloat(event.estimateRevision, .8), nullableEstimateString(event.estimateRevision, "half"))
			if err != nil {
				t.Fatal(err)
			}
			eventID, _ := res.LastInsertId()
			var heartbeatAt, heartbeatID, semanticID, semanticAt, estimateID, estimateAt any
			if event.heartbeat {
				heartbeatAt, heartbeatID = receivedAt, eventID
			}
			if event.kind == "phase" || event.kind == "needs_input" || event.kind == "blocker" ||
				event.phase != "unknown" || event.activity != "" {
				semanticID, semanticAt = eventID, receivedAt
			}
			if event.estimateRevision != nil {
				estimateID, estimateAt = eventID, receivedAt
			}
			if _, err := database.Exec(`INSERT INTO agent_run_telemetry_latest(
				run_id,telemetry_id,sequence,last_heartbeat_at,heartbeat_telemetry_id,semantic_telemetry_id,estimate_telemetry_id,
				latest_event_at,latest_semantic_at,latest_estimate_at) VALUES(?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(run_id) DO UPDATE SET telemetry_id=excluded.telemetry_id,sequence=excluded.sequence,
				last_heartbeat_at=COALESCE(excluded.last_heartbeat_at,agent_run_telemetry_latest.last_heartbeat_at),
				heartbeat_telemetry_id=COALESCE(excluded.heartbeat_telemetry_id,agent_run_telemetry_latest.heartbeat_telemetry_id),
				semantic_telemetry_id=COALESCE(excluded.semantic_telemetry_id,agent_run_telemetry_latest.semantic_telemetry_id),
				estimate_telemetry_id=COALESCE(excluded.estimate_telemetry_id,agent_run_telemetry_latest.estimate_telemetry_id),
				latest_event_at=excluded.latest_event_at,
				latest_semantic_at=COALESCE(excluded.latest_semantic_at,agent_run_telemetry_latest.latest_semantic_at),
				latest_estimate_at=COALESCE(excluded.latest_estimate_at,agent_run_telemetry_latest.latest_estimate_at)`,
				runID, eventID, event.sequence, heartbeatAt, heartbeatID, semanticID, estimateID, receivedAt, semanticAt, estimateAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := readProjection()
	if _, err := database.Exec(`DELETE FROM agent_run_telemetry_latest WHERE run_id=?`, runIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_run_telemetry_latest SET
		telemetry_id=(SELECT MIN(id) FROM agent_run_telemetry WHERE run_id=?),sequence=1,
		last_heartbeat_at=NULL,heartbeat_telemetry_id=NULL,semantic_telemetry_id=NULL,estimate_telemetry_id=NULL,
		latest_event_at='stale',latest_semantic_at=NULL,latest_estimate_at=NULL WHERE run_id=?`, runIDs[0], runIDs[0]); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildAgentRunTelemetryLatest(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := readProjection(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt projection=%+v want incremental=%+v", got, want)
	}
}

func nullableEstimateInt(revision *int64, value int64) any {
	if revision == nil {
		return nil
	}
	return value
}

func nullableEstimateFloat(revision *int64, value float64) any {
	if revision == nil {
		return nil
	}
	return value
}

func nullableEstimateString(revision *int64, value string) any {
	if revision == nil {
		return ""
	}
	return value
}

func TestSchemaAgentRunTelemetryTerminalWriteRace(t *testing.T) {
	database := openTestDB(t)
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Telemetry Race','TRC')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','Race')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	run, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,status) VALUES(?,?,'running')`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := run.LastInsertId()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var terminalErr, telemetryErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, terminalErr = database.Exec(`UPDATE agent_runs SET status='completed',finished_at=datetime('now') WHERE id=? AND status='running'`, runID)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, telemetryErr = database.Exec(`INSERT INTO agent_run_telemetry(
			run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind,heartbeat)
			VALUES(?,1,'race-1','paimos','run-agent','2026-08-20T10:00:00Z','2026-08-20T10:00:00Z','heartbeat',1)`, runID)
	}()
	close(start)
	wg.Wait()
	if terminalErr != nil {
		t.Fatalf("terminal writer: %v", terminalErr)
	}
	if telemetryErr != nil && !strings.Contains(telemetryErr.Error(), "terminal run telemetry") {
		t.Fatalf("telemetry writer: %v", telemetryErr)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if _, err := database.Exec(`INSERT INTO agent_run_telemetry(
		run_id,sequence,correlation_id,provider,adapter,agent_reported_at,server_received_at,kind)
		VALUES(?,2,'race-1','paimos','run-agent','2026-08-20T10:00:01Z','2026-08-20T10:00:01Z','phase')`, runID); err == nil || !strings.Contains(err.Error(), "terminal run telemetry") {
		t.Fatalf("late telemetry error=%v", err)
	}
}

func TestProjectLifecycleTriggersRejectNewIssues(t *testing.T) {
	db := openTestDB(t)
	res, err := db.Exec(`INSERT INTO projects(name, key, status) VALUES('Lifecycle', 'LIFE', 'active')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, 1, 'ticket', 'active is writable')`, projectID); err != nil {
		t.Fatalf("active project insert: %v", err)
	}

	for number, status := range []string{"frozen", "archived", "deleted"} {
		if _, err := db.Exec(`UPDATE projects SET status=? WHERE id=?`, status, projectID); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(?, ?, 'ticket', 'blocked')`, projectID, number+2)
		if err == nil || !strings.Contains(err.Error(), "project is "+status+"; new issues are disabled") {
			t.Fatalf("status %s insert error = %v", status, err)
		}
	}
}

func TestSchemaAgentRunsProviderColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"action_key", "provider_kind", "provider_id", "provider_label", "model", "run_mode"} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-629 / M129)", col)
		}
	}
	if !columnExists(t, db, "auto_watch_subscriptions", "actions_json") {
		t.Fatal("expected auto_watch_subscriptions.actions_json to exist (PAI-629 / M129)")
	}
}

func TestSchemaAICallsExecutionOptionColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"profile_id", "effort", "prompt_preset_ref", "context_pack"} {
		if !columnExists(t, db, "ai_calls", col) {
			t.Fatalf("expected ai_calls.%s to exist (PAI-649 / M130)", col)
		}
	}
}

func TestSchemaAgentRunsDraftProviderColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{
		"profile_id", "effort", "prompt_preset_ref", "context_pack",
		"context_truncated", "context_sources_json", "prompt_tokens",
		"completion_tokens", "finish_reason",
	} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-657 / M131)", col)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects(name, key) VALUES('Draft Status Project', 'DSP')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	res, err := db.Exec(`INSERT INTO issues(project_id, issue_number, type, title) VALUES(1, 1, 'ticket', 'Draft status')`)
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	issueID, _ := res.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO agent_runs(issue_id, status, run_mode, action_key, provider_kind, provider_id, provider_label)
		VALUES(?, 'drafted', 'draft', 'openrouter_draft.implement', 'hosted_model', 'openrouter', 'OpenRouter Draft')
	`, issueID); err != nil {
		t.Fatalf("expected drafted to be an allowed agent_runs.status: %v", err)
	}
	if !columnExists(t, db, "ai_settings", "base_url") {
		t.Fatal("expected ai_settings.base_url to exist (PAI-658 / M131)")
	}
}

func TestSchemaAIDefaultsAndDraftHandoffColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"source_draft_run_id", "followup_run_id"} {
		if !columnExists(t, db, "agent_runs", col) {
			t.Fatalf("expected agent_runs.%s to exist (PAI-665 / M132)", col)
		}
	}
	for _, col := range []string{"ai_defaults_json", "ai_policy_json"} {
		if !columnExists(t, db, "projects", col) {
			t.Fatalf("expected projects.%s to exist (PAI-666 / M132)", col)
		}
	}
}

func TestPerConnectionPragmasDoNotTouchJournalMode(t *testing.T) {
	for _, pragma := range perConnectionPragmas {
		if strings.Contains(strings.ToLower(pragma), "journal_mode") {
			t.Fatalf("per-connection pragma %q touches journal_mode; WAL must be set once during Open", pragma)
		}
	}
}

func TestEnableWALModePersistsFileJournalMode(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "wal-mode.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := enableWALMode(db); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=%q, want wal", mode)
	}
}

func TestSchemaContainsCurrentProjectContextAndAIRelationsTables(t *testing.T) {
	db := openTestDB(t)
	for _, table := range []string{
		"project_repos",
		"issue_anchors",
		// PAI-358: project_manifests dropped in M102 — the manifest
		// editor surface was retired with the v3.0 footer bar redesign.
		"project_context_index",
		"entity_relations",
		"entity_embeddings",
		"ai_prompts",
		"ai_calls",
		"mutation_log",
		"app_settings",
		"project_members",
		"project_agents",
		"project_environments",
		"project_deploy_recipes",
		"project_report_permissions",
		"project_report_snapshots",
		"role_permissions",
		"super_admin_audit",
		"project_issue_counters",
		"issue_key_aliases",
	} {
		if !tableExists(t, db, table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}
	if tableExists(t, db, "user_project_access") {
		t.Fatal("user_project_access should be removed after migration 65")
	}
	if tableExists(t, db, "project_manifests") {
		t.Fatal("project_manifests should be removed after migration 102 (PAI-358)")
	}
	if !columnExists(t, db, "ai_prompts", "placement") {
		t.Fatal("expected ai_prompts.placement to exist")
	}
	if !columnExists(t, db, "mutation_log", "after_state") {
		t.Fatal("expected mutation_log.after_state to exist")
	}
	if !columnExists(t, db, "mutation_log", "redoable") {
		t.Fatal("expected mutation_log.redoable to exist")
	}
	if !columnExists(t, db, "sessions", "csrf_token") {
		t.Fatal("expected sessions.csrf_token to exist")
	}
	if !columnExists(t, db, "sessions", "actor_user_id") {
		t.Fatal("expected sessions.actor_user_id to exist (PAI-389 / M106)")
	}
	if !columnExists(t, db, "sessions", "acting_as_user_id") {
		t.Fatal("expected sessions.acting_as_user_id to exist (PAI-389 / M106)")
	}
	if !columnExists(t, db, "users", "issue_auto_refresh_enabled") {
		t.Fatal("expected users.issue_auto_refresh_enabled to exist")
	}
	if !columnExists(t, db, "users", "issue_auto_refresh_interval_seconds") {
		t.Fatal("expected users.issue_auto_refresh_interval_seconds to exist")
	}
	if !columnExists(t, db, "users", "role_key") {
		t.Fatal("expected users.role_key to exist (PAI-336 / M105)")
	}
	if !columnExists(t, db, "project_cooperation", "report_contract_basis") {
		t.Fatal("expected project_cooperation.report_contract_basis to exist (PAI-407 / M107)")
	}
	if !columnExists(t, db, "project_cooperation", "report_customer_responsibilities") {
		t.Fatal("expected project_cooperation.report_customer_responsibilities to exist (PAI-407 / M107)")
	}
	if !columnExists(t, db, "customers", "tax_id") {
		t.Fatal("expected customers.tax_id to exist (PAI-558 / M114)")
	}
	if !columnExists(t, db, "customers", "company_register_number") {
		t.Fatal("expected customers.company_register_number to exist (PAI-558 / M114)")
	}
	// PAI-324 / M93 — agent + session attribution on history snapshots.
	if !columnExists(t, db, "issue_history", "agent_name") {
		t.Fatal("expected issue_history.agent_name to exist")
	}
	if !columnExists(t, db, "issue_history", "session_id") {
		t.Fatal("expected issue_history.session_id to exist")
	}
	// PAI-329 / M95 — agent rendering shape extensions.
	if !columnExists(t, db, "project_agents", "body") {
		t.Fatal("expected project_agents.body to exist")
	}
	if !columnExists(t, db, "project_agents", "bootstrap_steps") {
		t.Fatal("expected project_agents.bootstrap_steps to exist")
	}
	if !columnExists(t, db, "project_agents", "non_negotiable_rules") {
		t.Fatal("expected project_agents.non_negotiable_rules to exist")
	}
	// PAI-338 / M96 — knowledge-plane columns on issues.
	if !columnExists(t, db, "issues", "slug") {
		t.Fatal("expected issues.slug to exist (PAI-338 / M96)")
	}
	if !columnExists(t, db, "issues", "category_metadata") {
		t.Fatal("expected issues.category_metadata to exist (PAI-338 / M96)")
	}
	// PAI-345 / M99 — user_id column for cross-scope memory.
	if !columnExists(t, db, "issues", "user_id") {
		t.Fatal("expected issues.user_id to exist (PAI-345 / M99)")
	}
	// PAI-347 / M100 — memory reference-count tracking.
	if !columnExists(t, db, "issues", "reference_count") {
		t.Fatal("expected issues.reference_count to exist (PAI-347 / M100)")
	}
	if !columnExists(t, db, "issues", "last_referenced_at") {
		t.Fatal("expected issues.last_referenced_at to exist (PAI-347 / M100)")
	}
	// PAI-577 / M115 — issue-list freshness marker for the conditional-GET ETag.
	if !columnExists(t, db, "issues", "content_rev") {
		t.Fatal("expected issues.content_rev to exist (PAI-577 / M115)")
	}
	// PAI-354 / M101 — agent attribution on mutation_log rows.
	// session_id has lived here since M83; agent_name is the new arrival.
	if !columnExists(t, db, "mutation_log", "agent_name") {
		t.Fatal("expected mutation_log.agent_name to exist (PAI-354 / M101)")
	}
	if !columnExists(t, db, "mutation_log", "session_id") {
		t.Fatal("expected mutation_log.session_id to exist (PAI-354 / M101)")
	}
}

func TestSchemaCreatesDatabaseFileInConfiguredDataDir(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		t.Fatal("db is nil")
	}
	dbPath := filepath.Join(os.Getenv("DATA_DIR"), "paimos.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected sqlite file at %s: %v", dbPath, err)
	}
}

func TestTestDatabasePathRequiresExplicitIsolation(t *testing.T) {
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("DATA_DIR", "")
	if _, err := databasePathFromEnvironment(); err == nil || !strings.Contains(err.Error(), "explicit DATA_DIR") {
		t.Fatalf("implicit test database path error=%v, want explicit DATA_DIR rejection", err)
	}

	firstDir := t.TempDir()
	t.Setenv("DATA_DIR", firstDir)
	first, err := databasePathFromEnvironment()
	if err != nil {
		t.Fatalf("first isolated database path: %v", err)
	}
	secondDir := t.TempDir()
	t.Setenv("DATA_DIR", secondDir)
	second, err := databasePathFromEnvironment()
	if err != nil {
		t.Fatalf("second isolated database path: %v", err)
	}
	if first == second || first != filepath.Join(firstDir, "paimos.db") || second != filepath.Join(secondDir, "paimos.db") {
		t.Fatalf("isolated database paths first=%q second=%q", first, second)
	}
}

func TestSchemaEnablesForeignKeysAndPassesIntegrityCheck(t *testing.T) {
	db := openTestDB(t)
	enabled, err := ForeignKeysEnabled(db)
	if err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if !enabled {
		t.Fatal("expected PRAGMA foreign_keys=ON")
	}
	ok, err := IntegrityCheckOK(db)
	if err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if !ok {
		t.Fatal("expected integrity_check=ok")
	}
}

func TestSchemaContainsCriticalIndexes(t *testing.T) {
	db := openTestDB(t)
	for _, index := range []string{
		"idx_issues_number",
		"idx_issues_deleted_at",
		"idx_project_members_project",
		"idx_project_repos_project",
		"idx_issue_anchors_issue",
		"idx_entity_relations_project_src",
		"idx_entity_relations_project_tgt",
		"idx_ai_prompts_key_enabled",
		"idx_ai_calls_time",
		"idx_ai_calls_issue_time",
		"idx_mutation_log_user_stack",
		"idx_mutation_log_request",
		"idx_mutation_log_parent",
		"idx_product_sessions_project_updated",
		"idx_product_sessions_node",
		"idx_product_sessions_target_agent",
		"idx_documents_project",
		"idx_time_entries_mite_id",
		// PAI-857 / M162 — scope-correct knowledge identities.
		"idx_issues_knowledge_project_identity",
		"idx_issues_knowledge_user_identity",
		"idx_issues_knowledge_instance_identity",
		// PAI-345 / M99 — user-scoped knowledge lookups.
		"idx_issues_user_type",
		// PAI-336 / M105 — queryable privileged-action audit feed.
		"idx_super_admin_audit_created_at",
		"idx_super_admin_audit_actor",
		"idx_super_admin_audit_target",
		"idx_super_admin_audit_capability",
		"idx_sessions_actor_user",
		"idx_sessions_acting_as_user",
		"idx_project_report_permissions_project",
		"idx_project_report_snapshots_project",
		"idx_project_report_snapshots_code",
		"idx_issues_project_number_unique",
	} {
		found, err := SchemaHasIndex(db, index)
		if err != nil {
			t.Fatalf("schema_has_index %s: %v", index, err)
		}
		if !found {
			t.Fatalf("expected index %s to exist", index)
		}
	}
}

func TestSchemaSeedsSuperAdminCapabilities(t *testing.T) {
	db := openTestDB(t)
	for _, row := range []struct {
		role       string
		capability string
	}{
		{"admin", "security.super_admin_audit.read"},
		{"super_admin", "security.super_admin_audit.read"},
		{"super_admin", "time_entries.write_any_user"},
		{"super_admin", "users.grant_super_admin"},
		{"super_admin", "auth.impersonation.start"},
		{"super_admin", "auth.impersonation.end"},
		{"super_admin", "auth.impersonation.action"},
	} {
		var found int
		if err := db.QueryRow(
			"SELECT 1 FROM role_permissions WHERE role=? AND capability=?",
			row.role, row.capability,
		).Scan(&found); err != nil {
			t.Fatalf("expected role_permissions row (%s, %s): %v", row.role, row.capability, err)
		}
	}
}

func TestMigration155WidensTargetAdaptersForClaudeSessionsAndKeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m155-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 154); err != nil {
		t.Fatalf("create exact M154 fixture: %v", err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	project, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Bus migration','BUS')`)
	projectID, _ := project.LastInsertId()
	sender, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID)
	receiver, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'receiver')`, projectID)
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	cipher := []byte("opaque-ciphertext-longer-than-twenty-eight-bytes")
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-codex','ppm',?,'codex:receiver','codex','codex_thread',?,'steer','primary',1,1)`, projectID, cipher); err != nil {
		t.Fatal(err)
	}
	message, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,parts_json,from_address,to_address,thread_id,delivery_primary_target_id)
		VALUES(?,?,'bus intent',1,datetime('now'),'bus-155','BUS','[{"kind":"text","text":"bus intent"}]','paimos:sender','codex:receiver','bus-155','t-codex')`, senderID, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	messageRowID, _ := message.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries(delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
		VALUES('d-155',?,'ppm','t-codex','steer','pending')`, messageRowID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-claude','ppm',?,'claude:receiver','claude_resume','claude_session',?,'simple','primary',1,1)`, projectID, cipher); err == nil {
		t.Fatal("M154 must still reject Claude adapters before the migration")
	}

	if err := migrateThrough(database, 155); err != nil {
		t.Fatalf("M154 to M155: %v", err)
	}

	// Every pre-existing target version, its ciphertext, and the delivery row
	// that references it survive the rebuild under the original table name.
	var adapter, kind, level, role string
	var enabled, version int
	var keptCipher []byte
	if err := database.QueryRow(`SELECT adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version FROM agent_message_targets WHERE id='t-codex'`).Scan(
		&adapter, &kind, &keptCipher, &level, &role, &enabled, &version); err != nil {
		t.Fatalf("codex target lost in rebuild: %v", err)
	}
	if adapter != "codex" || kind != "codex_thread" || string(keptCipher) != string(cipher) || level != "steer" || role != "primary" || enabled != 1 || version != 1 {
		t.Fatalf("codex target changed: adapter=%q kind=%q level=%q role=%q enabled=%d version=%d", adapter, kind, level, role, enabled, version)
	}
	var referenced string
	if err := database.QueryRow(`SELECT t.id FROM agent_message_deliveries d JOIN agent_message_targets t ON t.id=d.primary_target_id WHERE d.delivery_id='d-155'`).Scan(&referenced); err != nil || referenced != "t-codex" {
		t.Fatalf("delivery no longer joins its target: id=%q err=%v", referenced, err)
	}
	violations, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer violations.Close()
	if violations.Next() {
		t.Fatal("foreign_key_check returned a violation after the M155 rebuild")
	}
	for _, index := range []string{"idx_agent_message_targets_enabled_role", "idx_agent_message_targets_receiver"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='agent_message_targets'`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s missing after rebuild: count=%d err=%v", index, count, err)
		}
	}
	for _, name := range []string{"agent_message_targets_m155", "agent_message_targets_old155"} {
		if tableExists(t, database, name) {
			t.Fatalf("rebuild residue %s left behind", name)
		}
	}

	// Claude session targets are accepted for both Claude adapters, only with
	// target_kind claude_session and maximum_level simple.
	for _, tc := range []struct {
		name, id, adapter, kind, level string
		ok                             bool
	}{
		{"claude_resume session", "t-resume", "claude_resume", "claude_session", "simple", true},
		{"claude_channel session", "t-channel", "claude_channel", "claude_session", "simple", true},
		{"claude_resume with codex kind", "t-bad-kind", "claude_resume", "codex_thread", "simple", false},
		{"claude_channel with webhook kind", "t-bad-kind2", "claude_channel", "https_webhook", "simple", false},
		{"codex with claude kind", "t-bad-kind3", "codex", "claude_session", "simple", false},
		{"claude_resume steer policy", "t-bad-level", "claude_resume", "claude_session", "steer", false},
		{"unknown adapter", "t-bad-adapter", "claude", "claude_session", "simple", false},
	} {
		_, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
			VALUES(?,'ppm',?,?,?,?,?,?,'primary',0,1)`, tc.id, projectID, "claude:"+tc.id, tc.adapter, tc.kind, cipher, tc.level)
		if tc.ok && err != nil {
			t.Fatalf("%s: rejected: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
	}
	// Existing adapter pairs remain valid after the rebuild.
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-webhook','ppm',?,'grok_bot:amy','grok_bot_routine','https_webhook',?,'simple','primary',1,1)`, projectID, cipher); err != nil {
		t.Fatalf("grok_bot_routine target rejected after rebuild: %v", err)
	}
}

func TestMigration156OpensHarnessPluginKeysAndKeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m156-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 155); err != nil {
		t.Fatalf("create exact M155 fixture: %v", err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	project, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Plugin migration','PLG')`)
	projectID, _ := project.LastInsertId()
	sender, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID)
	receiver, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'receiver')`, projectID)
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	cipher := []byte("opaque-ciphertext-longer-than-twenty-eight-bytes")
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-known','ppm',?,'codex:receiver','codex','codex_thread',?,'steer','primary',1,1)`, projectID, cipher); err != nil {
		t.Fatal(err)
	}
	message, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,parts_json,from_address,to_address,thread_id,delivery_primary_target_id)
		VALUES(?,?,'plugin intent',1,datetime('now'),'bus-156','PLG','[{"kind":"text","text":"plugin intent"}]','paimos:sender','codex:receiver','bus-156','t-known')`, senderID, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	messageRowID, _ := message.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries(delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
		VALUES('d-156',?,'ppm','t-known','steer','pending')`, messageRowID); err != nil {
		t.Fatal(err)
	}

	if err := migrateThrough(database, 156); err != nil {
		t.Fatalf("M155 to M156: %v", err)
	}

	var adapter, kind, level, role, referenced string
	var enabled, version int
	var keptCipher []byte
	if err := database.QueryRow(`SELECT adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version FROM agent_message_targets WHERE id='t-known'`).Scan(
		&adapter, &kind, &keptCipher, &level, &role, &enabled, &version); err != nil {
		t.Fatalf("known target lost in rebuild: %v", err)
	}
	if adapter != "codex" || kind != "codex_thread" || string(keptCipher) != string(cipher) || level != "steer" || role != "primary" || enabled != 1 || version != 1 {
		t.Fatalf("known target changed: adapter=%q kind=%q level=%q role=%q enabled=%d version=%d", adapter, kind, level, role, enabled, version)
	}
	if err := database.QueryRow(`SELECT t.id FROM agent_message_deliveries d JOIN agent_message_targets t ON t.id=d.primary_target_id WHERE d.delivery_id='d-156'`).Scan(&referenced); err != nil || referenced != "t-known" {
		t.Fatalf("delivery no longer joins its target: id=%q err=%v", referenced, err)
	}

	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-third','ppm',?,'third:receiver','third_adapter','third_ref',?,'steer','primary',0,1)`, projectID, cipher); err != nil {
		t.Fatalf("third-party plugin keys rejected: %v", err)
	}
	for _, tc := range []struct {
		name, id, adapter, kind string
	}{
		{"uppercase adapter", "t-upper-adapter", "Third_adapter", "third_ref"},
		{"empty adapter", "t-empty-adapter", "", "third_ref"},
		{"punctuated adapter", "t-punct-adapter", "third-adapter", "third_ref"},
		{"digit-leading adapter", "t-digit-adapter", "3third", "third_ref"},
		{"underscore-leading adapter", "t-underscore-adapter", "_third", "third_ref"},
		{"long adapter", "t-long-adapter", strings.Repeat("a", 65), "third_ref"},
		{"uppercase kind", "t-upper-kind", "third_adapter", "Third_ref"},
		{"empty kind", "t-empty-kind", "third_adapter", ""},
		{"punctuated kind", "t-punct-kind", "third_adapter", "third-ref"},
		{"digit-leading kind", "t-digit-kind", "third_adapter", "3third"},
		{"underscore-leading kind", "t-underscore-kind", "third_adapter", "_third"},
		{"long kind", "t-long-kind", "third_adapter", strings.Repeat("a", 65)},
	} {
		if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
			VALUES(?,'ppm',?,?,?,?,?,'simple','primary',0,1)`, tc.id, projectID, "third:"+tc.id, tc.adapter, tc.kind, cipher); err == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
	}

	violations, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer violations.Close()
	if violations.Next() {
		t.Fatal("foreign_key_check returned a violation after the M156 rebuild")
	}
	for _, index := range []string{"idx_agent_message_targets_enabled_role", "idx_agent_message_targets_receiver"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='agent_message_targets'`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s missing after rebuild: count=%d err=%v", index, count, err)
		}
	}
	for _, name := range []string{"agent_message_targets_m156", "agent_message_targets_old156"} {
		if tableExists(t, database, name) {
			t.Fatalf("rebuild residue %s left behind", name)
		}
	}
}

func TestMigration157AddsNullableTargetSecretCipher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m157-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 156); err != nil {
		t.Fatalf("create exact M156 fixture: %v", err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	project, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Sender secret migration','SSM')`)
	projectID, _ := project.LastInsertId()
	sender, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID)
	receiver, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'amy')`, projectID)
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	cipher := []byte("opaque-ciphertext-longer-than-twenty-eight-bytes")
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-legacy','ppm',?,'grok_bot:amy','grok_bot_routine','https_webhook',?,'simple','primary',1,1)`, projectID, cipher); err != nil {
		t.Fatal(err)
	}
	message, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,parts_json,from_address,to_address,thread_id,delivery_primary_target_id)
		VALUES(?,?,'wake intent',1,datetime('now'),'bus-157','SSM','[{"kind":"text","text":"wake intent"}]','paimos:sender','grok_bot:amy','bus-157','t-legacy')`, senderID, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	messageRowID, _ := message.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries(delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
		VALUES('d-157',?,'ppm','t-legacy','simple','pending')`, messageRowID); err != nil {
		t.Fatal(err)
	}

	if err := migrateThrough(database, 157); err != nil {
		t.Fatalf("M156 to M157: %v", err)
	}

	var keptCipher []byte
	var hasSecret int
	if err := database.QueryRow(`SELECT target_ref_cipher,target_secret_cipher IS NOT NULL FROM agent_message_targets WHERE id='t-legacy'`).Scan(&keptCipher, &hasSecret); err != nil {
		t.Fatalf("legacy target lost: %v", err)
	}
	if string(keptCipher) != string(cipher) || hasSecret != 0 {
		t.Fatalf("legacy target changed: cipher kept=%v has_secret=%d", string(keptCipher) == string(cipher), hasSecret)
	}
	var referenced string
	if err := database.QueryRow(`SELECT t.id FROM agent_message_deliveries d JOIN agent_message_targets t ON t.id=d.primary_target_id WHERE d.delivery_id='d-157'`).Scan(&referenced); err != nil || referenced != "t-legacy" {
		t.Fatalf("delivery no longer joins its target: id=%q err=%v", referenced, err)
	}
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,target_secret_cipher,maximum_level,role,enabled,version)
		VALUES('t-short-secret','ppm',?,'grok_bot:amy','grok_bot_routine','https_webhook',?,X'00','simple','primary',0,2)`, projectID, cipher); err == nil {
		t.Fatal("a secret ciphertext shorter than the AEAD envelope was accepted")
	}
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,target_secret_cipher,maximum_level,role,enabled,version)
		VALUES('t-with-secret','ppm',?,'grok_bot:amy','grok_bot_routine','https_webhook',?,?,'simple','primary',0,3)`, projectID, cipher, []byte("opaque-secret-ciphertext-longer-than-28-bytes")); err != nil {
		t.Fatalf("encrypted sender secret rejected: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-codex-no-secret','ppm',?,'codex:sender','codex','codex_thread',?,'steer','primary',1,1)`, projectID, cipher); err != nil {
		t.Fatalf("secret-free target rejected after M157: %v", err)
	}
	violations, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer violations.Close()
	if violations.Next() {
		t.Fatal("foreign_key_check returned a violation after M157")
	}
	for _, index := range []string{"idx_agent_message_targets_enabled_role", "idx_agent_message_targets_receiver"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='agent_message_targets'`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s missing after M157: count=%d err=%v", index, count, err)
		}
	}
}

func TestMigration158AddsConstrainedPharosRequestLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m158-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 157); err != nil {
		t.Fatalf("create exact M157 fixture: %v", err)
	}
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Pharos Link','PHL')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	issue, err := database.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,1,'ticket','Need a host')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()

	if err := migrateThrough(database, 158); err != nil {
		t.Fatalf("M157 to M158: %v", err)
	}
	var title, requestID string
	if err := database.QueryRow(`SELECT title,pharos_request_id FROM issues WHERE id=?`, issueID).Scan(&title, &requestID); err != nil {
		t.Fatal(err)
	}
	if title != "Need a host" || requestID != "" {
		t.Fatalf("legacy issue changed: title=%q request=%q", title, requestID)
	}
	if _, err := database.Exec(`UPDATE issues SET pharos_request_id='pharos-create-csb1-1787912345000-1' WHERE id=?`, issueID); err != nil {
		t.Fatalf("valid request id rejected: %v", err)
	}
	for _, invalid := range []string{"short", "https://pharos.invalid/request/1", "Bearer sk-secret-value", "sk_test_abcdefghijklmnopqrstuvwxyz", strings.Repeat("a", 129)} {
		if _, err := database.Exec(`UPDATE issues SET pharos_request_id=? WHERE id=?`, invalid, issueID); err == nil {
			t.Errorf("invalid request id %q bypassed database constraint", invalid)
		}
	}
}

func TestMigration159AddsTransportFallbackReasonWithoutChangingDeliveries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m159-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 158); err != nil {
		t.Fatalf("create exact M158 fixture: %v", err)
	}
	project, _ := database.Exec(`INSERT INTO projects(name,key) VALUES('Transport fallback','TRN')`)
	projectID, _ := project.LastInsertId()
	sender, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID)
	receiver, _ := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID)
	senderID, _ := sender.LastInsertId()
	receiverID, _ := receiver.LastInsertId()
	cipher := []byte("opaque-ciphertext-longer-than-twenty-eight-bytes")
	if _, err := database.Exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('t-m159','ppm',?,'codex:codex','codex','codex_thread',?,'steer','primary',1,1)`, projectID, cipher); err != nil {
		t.Fatal(err)
	}
	message, err := database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,context_id,parts_json,from_address,to_address,thread_id,delivery_level,delivery_primary_target_id)
		VALUES(?,?,'fallback',1,datetime('now'),'bus-159','TRN','[{"kind":"text","text":"fallback"}]','paimos:sender','codex:codex','bus-159','steer','t-m159')`, senderID, receiverID)
	if err != nil {
		t.Fatal(err)
	}
	messageRowID, _ := message.LastInsertId()
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries
		(delivery_id,message_row_id,instance,primary_target_id,requested_level,effective_level,state,fallback_reason,attempt_count,last_error_code,handed_off_at)
		VALUES('d-m159',?,'ppm','t-m159','steer','simple','handed_off','not_steerable',3,'',datetime('now'))`, messageRowID); err != nil {
		t.Fatal(err)
	}

	if err := migrateThrough(database, 159); err != nil {
		t.Fatalf("M158 to M159: %v", err)
	}
	var state, effective, reason, targetID string
	var attempts int
	if err := database.QueryRow(`SELECT d.state,d.effective_level,d.fallback_reason,d.attempt_count,t.id
		FROM agent_message_deliveries d JOIN agent_message_targets t ON t.id=d.primary_target_id
		WHERE d.delivery_id='d-m159'`).Scan(&state, &effective, &reason, &attempts, &targetID); err != nil {
		t.Fatalf("legacy delivery lost: %v", err)
	}
	if state != "handed_off" || effective != "simple" || reason != "not_steerable" || attempts != 3 || targetID != "t-m159" {
		t.Fatalf("state=%q effective=%q reason=%q attempts=%d target=%q", state, effective, reason, attempts, targetID)
	}
	if _, err := database.Exec(`UPDATE agent_message_deliveries SET fallback_reason='transport_error' WHERE delivery_id='d-m159'`); err != nil {
		t.Fatalf("transport_error rejected after M159: %v", err)
	}
	if _, err := database.Exec(`UPDATE agent_message_deliveries SET fallback_reason='invented' WHERE delivery_id='d-m159'`); err == nil {
		t.Fatal("unknown fallback reason bypassed M159 constraint")
	}
	violations, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer violations.Close()
	if violations.Next() {
		t.Fatal("foreign_key_check returned a violation after M159")
	}
	for _, index := range []string{"idx_agent_message_deliveries_dispatch", "idx_agent_message_deliveries_target"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='agent_message_deliveries'`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s missing after M159: count=%d err=%v", index, count, err)
		}
	}
}

func TestMigration160IndexesMutationLogParentWithoutChangingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m160-populated.db")
	database, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 159); err != nil {
		t.Fatalf("create exact M159 fixture: %v", err)
	}
	parent, err := database.Exec(`INSERT INTO mutation_log
		(request_id,mutation_type,subject_type,subject_id,inverse_op,before_state,before_hash,after_hash,after_state)
		VALUES('m160-parent','update','issue',1,'restore','{}','before','after','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	parentID, _ := parent.LastInsertId()
	if _, err := database.Exec(`INSERT INTO mutation_log
		(request_id,mutation_type,subject_type,subject_id,parent_log_id,inverse_op,before_state,before_hash,after_hash,after_state)
		VALUES('m160-child','undo','issue',1,?,'restore','{}','before','after','{}')`, parentID); err != nil {
		t.Fatal(err)
	}

	if err := migrateThrough(database, 160); err != nil {
		t.Fatalf("M159 to M160: %v", err)
	}
	var rows, linked int
	if err := database.QueryRow(`SELECT COUNT(*),COUNT(parent_log_id) FROM mutation_log`).Scan(&rows, &linked); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || linked != 1 {
		t.Fatalf("mutation rows changed: rows=%d linked=%d", rows, linked)
	}
	var indexSQL string
	if err := database.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_mutation_log_parent'`).Scan(&indexSQL); err != nil {
		t.Fatalf("parent index missing: %v", err)
	}
	if !strings.Contains(indexSQL, "parent_log_id") || !strings.Contains(indexSQL, "IS NOT NULL") {
		t.Fatalf("unexpected parent index: %q", indexSQL)
	}
	planRows, err := database.Query(`EXPLAIN QUERY PLAN DELETE FROM mutation_log WHERE id=?`, parentID)
	if err != nil {
		t.Fatal(err)
	}
	defer planRows.Close()
	var plan strings.Builder
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
	}
	if !strings.Contains(plan.String(), "idx_mutation_log_parent") {
		t.Fatalf("delete FK enforcement did not use M160 child index: %q", plan.String())
	}
	var violations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign-key violations=%d err=%v", violations, err)
	}
}

func TestMigration185ReleaseAcceptanceTables(t *testing.T) {
	database := openTestDB(t)
	for _, table := range []string{
		"release_records",
		"release_acceptances",
		"acceptance_parties",
		"acceptance_confirmations",
		"acceptance_email_evidence",
		"acceptance_mail_outbox",
		"acceptance_standing_policies",
	} {
		if !tableExists(t, database, table) {
			t.Fatalf("M185 table %s missing", table)
		}
	}
}

func TestMigration186AcceptanceTargetBindings(t *testing.T) {
	database := openTestDB(t)
	if !tableExists(t, database, "acceptance_target_bindings") {
		t.Fatal("M186 table acceptance_target_bindings missing")
	}
	if !columnExists(t, database, "acceptance_target_bindings", "target_ref") {
		t.Fatal("M186 target_ref missing")
	}
	if !columnExists(t, database, "acceptance_target_bindings", "registration_id") {
		t.Fatal("M186 registration_id missing")
	}
}

func TestMigration199PreservesLegacyIdentitiesAndRejectsReviewerCollisions(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m199-upgrade.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateThrough(database, 198); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(username,password,role,role_key,email,status) VALUES
 ('old-admin','x','admin','admin','same@example.test','active'),('old-member','x','member','member','same@example.test','active')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 199); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 199); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM users WHERE email='same@example.test'").Scan(&count); err != nil || count != 2 {
		t.Fatal("migration rewrote existing identities")
	}
	if _, err := database.Exec(`INSERT INTO users(username,password,role,role_key,email,is_reviewer,status) VALUES('reviewer-collision','x','external','external','same@example.test',1,'active')`); err == nil {
		t.Fatal("reviewer collision accepted")
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM flow_projection_credentials").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration created a credential")
	}
}
