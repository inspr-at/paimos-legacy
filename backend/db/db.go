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

package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/cases"
	"modernc.org/sqlite"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/controlcontract"
	"github.com/inspr-at/paimos/backend/safetext"
)

const (
	DefaultBusyTimeoutMS      = 5000
	DefaultMaxOpenConnections = 10
)

var perConnectionPragmas = []string{
	fmt.Sprintf("PRAGMA busy_timeout=%d", DefaultBusyTimeoutMS),
	"PRAGMA foreign_keys=ON",
}

var paimosUnicodeCaseFolder = cases.Fold()

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("paimos_cosine", 2, paimosCosineSQL)
	sqlite.MustRegisterDeterministicScalarFunction("paimos_contains_secret_like", 1, paimosContainsSecretLikeSQL)
	sqlite.MustRegisterDeterministicScalarFunction("paimos_message_body_contains_secret_like", 1, paimosMessageBodyContainsSecretLikeSQL)
	sqlite.MustRegisterDeterministicScalarFunction("paimos_domain_sha256", -1, paimosDomainSHA256SQL)
	sqlite.MustRegisterDeterministicScalarFunction("paimos_casefold", 1, paimosCasefoldSQL)

	// RegisterConnectionHook fires on every new connection in the pool —
	// the right place for genuinely per-connection pragmas. NOT the right
	// place for `journal_mode=WAL`, even though it's effectively idempotent
	// once the DB is already in WAL: each invocation still has to commit
	// the schema change, which briefly takes an exclusive lock and races
	// any concurrent transaction on another pool connection. The symptom
	// was PAI-369 — TestBatchUpdate_AllScalarFields flaking with
	// `pragma "PRAGMA journal_mode=WAL": database is locked (5) (SQLITE_BUSY)`
	// at ~10–15% rate. WAL is now set once at Open(); see below.
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		ctx := context.Background()
		for _, pragma := range perConnectionPragmas {
			if _, err := conn.ExecContext(ctx, pragma, nil); err != nil {
				return fmt.Errorf("pragma %q: %w", pragma, err)
			}
		}
		return nil
	})
}

func paimosCasefoldSQL(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("paimos_casefold requires exactly one text value")
	}
	value, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("paimos_casefold requires a non-null text value")
	}
	return paimosUnicodeCaseFolder.String(value), nil
}

func paimosDomainSHA256SQL(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("paimos_domain_sha256 requires a domain and at least one value")
	}
	hash := sha256.New()
	for index, arg := range args {
		if arg == nil {
			return nil, fmt.Errorf("paimos_domain_sha256 arguments must be non-null text or blobs")
		}
		value, ok := sqliteBlobArg(arg)
		if !ok {
			return nil, fmt.Errorf("paimos_domain_sha256 arguments must be text or blobs")
		}
		if index > 0 {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write(value)
	}
	return hash.Sum(nil), nil
}

func paimosContainsSecretLikeSQL(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 1 || args[0] == nil {
		return int64(0), nil
	}
	var value string
	switch typed := args[0].(type) {
	case string:
		value = typed
	case []byte:
		value = string(typed)
	default:
		return int64(1), nil
	}
	if safetext.ContainsSecretLike(value) {
		return int64(1), nil
	}
	return int64(0), nil
}

func paimosCosineSQL(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 2 {
		return float64(0), nil
	}
	left, ok := sqliteBlobArg(args[0])
	if !ok {
		return float64(0), nil
	}
	right, ok := sqliteBlobArg(args[1])
	if !ok {
		return float64(0), nil
	}
	if len(left) == 0 || len(left) != len(right) || len(left)%4 != 0 {
		return float64(0), nil
	}
	var dot, leftNorm, rightNorm float64
	for i := 0; i < len(left); i += 4 {
		l := float64(math.Float32frombits(binary.LittleEndian.Uint32(left[i : i+4])))
		r := float64(math.Float32frombits(binary.LittleEndian.Uint32(right[i : i+4])))
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm == 0 || rightNorm == 0 {
		return float64(0), nil
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm)), nil
}

func sqliteBlobArg(v driver.Value) ([]byte, bool) {
	switch t := v.(type) {
	case []byte:
		return t, true
	case string:
		return []byte(t), true
	default:
		return nil, false
	}
}

var DB *sql.DB

type migration struct {
	version int
	steps   []string
}

func sqlEnum(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+strings.ReplaceAll(value, "'", "''")+"'")
	}
	return strings.Join(quoted, ",")
}

func sqlUUIDCheck(column string) string {
	return `(typeof(` + column + `)='text' AND length(CAST(` + column + ` AS BLOB))=36 AND ` +
		`length(replace(` + column + `,'-',''))=32 AND ` +
		column + `=lower(` + column + `) AND substr(` + column + `,9,1)='-' AND ` +
		`substr(` + column + `,14,1)='-' AND substr(` + column + `,19,1)='-' AND ` +
		`substr(` + column + `,24,1)='-' AND substr(` + column + `,15,1)='4' AND ` +
		`substr(` + column + `,20,1) IN ('8','9','a','b') AND ` +
		`replace(` + column + `,'-','') NOT GLOB '*[^0-9a-f]*')`
}

func sqlTypedPrincipalCheck(kindColumn, sessionCredentialColumn, apiKeyColumn string) string {
	return `( (` + kindColumn + `='session' AND ` + sqlUUIDCheck(sessionCredentialColumn) + ` AND ` + apiKeyColumn + ` IS NULL) OR ` +
		`(` + kindColumn + `='api_key' AND ` + sessionCredentialColumn + ` IS NULL AND typeof(` + apiKeyColumn + `)='integer' AND ` + apiKeyColumn + `>0) )`
}

func sqlStableKeyCheck(column string, maxBytes int) string {
	return `(length(CAST(` + column + ` AS BLOB)) BETWEEN 1 AND ` + fmt.Sprint(maxBytes) +
		` AND ` + column + ` GLOB '[A-Za-z0-9]*' AND ` + column + ` NOT GLOB '*[^A-Za-z0-9._:/-]*')`
}

// sqlControlTimestampCheck pins supervisory-control instants to one exact
// UTC representation. The julianday round trip rejects offsets, impossible
// dates, and alternate fractional precision that would make equality/CAS
// checks ambiguous.
func sqlControlTimestampCheck(column string) string {
	return `(typeof(` + column + `)='text' AND length(` + column + `)=24 AND ` +
		`julianday(` + column + `) IS NOT NULL AND ` +
		`COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ',julianday(` + column + `))=` + column + `,0)=1)`
}

func sqlNullableControlTimestampCheck(column string) string {
	return `(` + column + ` IS NULL OR ` + sqlControlTimestampCheck(column) + `)`
}

func sqlSafeDeviceIDCheck(column string) string {
	return `(` + sqlStableKeyCheck(column, 128) + ` AND instr(` + column + `,char(0))=0 AND ` +
		`instr(` + column + `,char(10))=0 AND instr(` + column + `,char(13))=0 AND ` +
		`paimos_contains_secret_like(CAST(` + column + ` AS BLOB))=0)`
}

// deliverySecretGuardSQL rebuilds the M144 privacy triggers during M145 so a
// populated database receives the same corpus as a fresh install. The scalar
// owns the shared secret detector, while the SQL-native predicates deliberately
// remain here: SQLite TEXT arguments can be truncated at NUL before reaching a
// scalar, and reporter/policy/evidence references have stricter URL/query
// policies than ordinary display text.
func deliverySecretGuardSQL(trigger, table, values, extra, message string) string {
	return fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON %s WHEN EXISTS (
		 SELECT 1 FROM json_each(json_array(%s)) value
		 WHERE instr(CAST(value.value AS TEXT),char(0))>0
		  OR instr(CAST(value.value AS TEXT),char(10))>0
		  OR instr(CAST(value.value AS TEXT),char(13))>0
		  OR paimos_contains_secret_like(CAST(value.value AS BLOB))=1%s
		) BEGIN SELECT RAISE(ABORT,'%s'); END`, trigger, table, values, extra, message)
}

// The append-only history is authoritative; this pair reconstructs every
// latest pointer without trusting the existing projection. M143 uses the same
// operation when upgrading populated databases, and the regression suite uses
// rebuildAgentRunTelemetryLatest to prove equivalence with incremental writes.
const clearAgentRunTelemetryLatestSQL = `DELETE FROM agent_run_telemetry_latest`

const rebuildAgentRunTelemetryLatestSQL = `INSERT INTO agent_run_telemetry_latest(
	run_id, telemetry_id, sequence, last_heartbeat_at, heartbeat_telemetry_id,
	semantic_telemetry_id, estimate_telemetry_id, latest_event_at,
	latest_semantic_at, latest_estimate_at)
	SELECT newest.run_id, newest.id, newest.sequence,
	 (SELECT h.server_received_at FROM agent_run_telemetry h
	   WHERE h.run_id=newest.run_id AND h.heartbeat=1 ORDER BY h.sequence DESC LIMIT 1),
	 (SELECT h.id FROM agent_run_telemetry h
	   WHERE h.run_id=newest.run_id AND h.heartbeat=1 ORDER BY h.sequence DESC LIMIT 1),
	 (SELECT s.id FROM agent_run_telemetry s
	   WHERE s.run_id=newest.run_id
	     AND (s.kind IN ('phase','needs_input','blocker') OR s.phase<>'unknown' OR
	          s.activity<>'' OR s.needs_input=1 OR s.blocker_state<>'none')
	   ORDER BY s.sequence DESC LIMIT 1),
	 (SELECT e.id FROM agent_run_telemetry e
	   WHERE e.run_id=newest.run_id AND e.estimate_revision IS NOT NULL
	   ORDER BY e.sequence DESC LIMIT 1),
	 newest.server_received_at,
	 (SELECT s.server_received_at FROM agent_run_telemetry s
	   WHERE s.run_id=newest.run_id
	     AND (s.kind IN ('phase','needs_input','blocker') OR s.phase<>'unknown' OR
	          s.activity<>'' OR s.needs_input=1 OR s.blocker_state<>'none')
	   ORDER BY s.sequence DESC LIMIT 1),
	 (SELECT e.server_received_at FROM agent_run_telemetry e
	   WHERE e.run_id=newest.run_id AND e.estimate_revision IS NOT NULL
	   ORDER BY e.sequence DESC LIMIT 1)
	FROM agent_run_telemetry newest
	WHERE newest.sequence=(SELECT MAX(candidate.sequence) FROM agent_run_telemetry candidate WHERE candidate.run_id=newest.run_id)`

// M145 recreates the M144 issue projection trigger with a live-boundary gate:
// the first soft delete and a later restore each invalidate once, while edits
// to an already hidden root cannot create an observable stream oracle.
const agentModeDeliveryIssueUpdateTriggerSQL = `CREATE TRIGGER trg_delivery_issue_update_change
	AFTER UPDATE ON issues WHEN EXISTS(SELECT 1 FROM deliveries WHERE issue_id=NEW.id)
	BEGIN
	 UPDATE deliveries SET project_id_hint=NEW.project_id,
	  updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	  WHERE issue_id=NEW.id AND NEW.project_id IS NOT OLD.project_id;
	 UPDATE deliveries SET spec_revision=spec_revision+1,
	  updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	  WHERE issue_id=NEW.id AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
	   OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
	 INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,kind,
	  reporter_id,reason_code,reason_text,server_received_at)
	 SELECT d.id,COALESCE((SELECT MAX(delivery_revision)+1 FROM delivery_events WHERE delivery_id=d.id),1),
	  'spec-revision:'||d.spec_revision,X'44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
	  'attempt_started',r.id,'spec_changed','Canonical issue specification changed',
	  strftime('%Y-%m-%dT%H:%M:%fZ','now')
	 FROM deliveries d JOIN delivery_reporters r ON r.delivery_id=d.id
	  AND r.reporter_type='system' AND r.opaque_key='paimos'
	 WHERE d.issue_id=NEW.id AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
	  OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
	 INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,previous_attempt_id,
	  start_delivery_event_id,project_id_at_start,reason_code,reason_text,created_at)
	 SELECT d.id,COALESCE((SELECT MAX(attempt_number)+1 FROM delivery_attempts WHERE delivery_id=d.id),1),
	  COALESCE((SELECT MAX(plan_revision)+1 FROM delivery_attempts WHERE delivery_id=d.id),1),
	   (SELECT a.id FROM delivery_attempts a JOIN delivery_attempt_policy_seals seal
	     ON seal.delivery_id=a.delivery_id AND seal.attempt_id=a.id
	    WHERE a.delivery_id=d.id ORDER BY a.attempt_number DESC LIMIT 1),
	  (SELECT id FROM delivery_events WHERE delivery_id=d.id ORDER BY delivery_revision DESC LIMIT 1),
	  NEW.project_id,'spec_changed','Canonical issue specification changed',strftime('%Y-%m-%dT%H:%M:%fZ','now')
	 FROM deliveries d WHERE d.issue_id=NEW.id
	  AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
	   OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
	 INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,applicability,
	  weight,policy_reference,reason_code,reason_text,authorized_by_reporter_id,created_at)
	 SELECT next.delivery_id,next.id,p.stage_key,p.sort_order,p.applicability,p.weight,p.policy_reference,
	  p.reason_code,p.reason_text,p.authorized_by_reporter_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
	 FROM delivery_attempts next JOIN delivery_attempt_stage_policy p ON p.attempt_id=next.previous_attempt_id
	 WHERE next.delivery_id=(SELECT id FROM deliveries WHERE issue_id=NEW.id)
	  AND next.attempt_number=(SELECT MAX(attempt_number) FROM delivery_attempts
	   WHERE delivery_id=next.delivery_id)
	  AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
	   OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
	 INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
	 SELECT next.delivery_id,next.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
	 FROM delivery_attempts next WHERE next.delivery_id=(SELECT id FROM deliveries WHERE issue_id=NEW.id)
	  AND next.attempt_number=(SELECT MAX(attempt_number) FROM delivery_attempts
	   WHERE delivery_id=next.delivery_id)
	  AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
	   OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
	 INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
	  change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
	 SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,NEW.project_id,
	  d.change_sequence_high_water+1,
	  COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
	  'issue','issue',NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
	 FROM deliveries d WHERE d.issue_id=NEW.id AND NEW.deleted_at IS NULL
	  AND NOT (NEW.project_id IS NOT OLD.project_id AND OLD.deleted_at IS NULL)
	  AND (NEW.deleted_at IS NOT OLD.deleted_at OR NEW.issue_number IS NOT OLD.issue_number OR
	   NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description OR
	   NEW.acceptance_criteria IS NOT OLD.acceptance_criteria OR NEW.updated_at IS NOT OLD.updated_at);
	END`

func rebuildAgentRunTelemetryLatest(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, clearAgentRunTelemetryLatestSQL); err != nil {
		return fmt.Errorf("clear agent-run telemetry projection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, rebuildAgentRunTelemetryLatestSQL); err != nil {
		return fmt.Errorf("rebuild agent-run telemetry projection: %w", err)
	}
	return nil
}

func databasePathFromEnvironment() (string, error) {
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		if os.Getenv("PAIMOS_TEST_MODE") == "1" {
			return "", errors.New("PAIMOS_TEST_MODE requires an explicit DATA_DIR for an isolated SQLite database")
		}
		dataDir = "/app/data"
	}
	return filepath.Join(dataDir, brand.Default.DBFilename), nil
}

func Open() error {
	dbPath, err := databasePathFromEnvironment()
	if err != nil {
		return err
	}
	dataDir := filepath.Dir(dbPath)

	// 0o750: the data dir holds the SQLite DB and secret key; only the
	// backend process (and its group) need access.
	// #nosec G703 -- dataDir is the operator-configured DATA_DIR env, not user input.
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// PAI-596: `_txlock=immediate` makes every transaction issue BEGIN IMMEDIATE,
	// acquiring the write lock up front instead of lazily upgrading a deferred
	// read→write transaction. Lazy upgrades fail instantly with SQLITE_BUSY when
	// another connection has written since the read (snapshot conflict) — and
	// busy_timeout CANNOT rescue that (waiting would deadlock). Grabbing the
	// lock up front means busy_timeout actually applies, so concurrent writers
	// queue (up to 5s) instead of erroring. Fixes intermittent 500s on
	// PUT /api/issues and POST .../comments under concurrent writes.
	db, err := sql.Open("sqlite", dbPath+"?_txlock=immediate")
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}

	// WAL mode allows concurrent readers; writers are serialized by SQLite
	// internally. busy_timeout prevents immediate SQLITE_BUSY errors under
	// write contention — connections wait up to 5s before failing
	// (busy_timeout is set per-connection via the hook above).
	db.SetMaxOpenConns(DefaultMaxOpenConnections)
	db.SetMaxIdleConns(5)

	// PAI-369: set WAL once at file open. journal_mode is a database-level
	// pragma persisted in the file header; setting it per-connection (in
	// the hook) raced concurrent transactions and caused intermittent
	// SQLITE_BUSY in CI. One exec here, then every subsequent connection
	// inherits the file's WAL mode without touching it. Test mode below
	// can still flip to MEMORY for speed.
	if err := enableWALMode(db); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	DB = db
	return migrate(db)
}

func enableWALMode(db *sql.DB) error {
	_, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL")
	return err
}

// PromoteSeededAdminSQL is migration M138 (PAI-739): promote the seeded
// bootstrap 'admin' account to super_admin, but only on instances that
// have no super-admin at all — exactly the ones deadlocked by the old
// admin-only seed. Exported so the migration semantics are directly
// testable against planted legacy states.
const PromoteSeededAdminSQL = `UPDATE users
	SET role_key = 'super_admin', is_super_admin = 1
	WHERE username = 'admin'
	  AND role = 'admin'
	  AND is_super_admin = 0
	  AND NOT EXISTS (
		SELECT 1 FROM users WHERE is_super_admin = 1 OR role_key = 'super_admin'
	  )`

func migrate(db *sql.DB) error {
	return migrateThrough(db, math.MaxInt)
}

// migrateThrough is the migration engine with an upper bound used by
// populated-upgrade regression fixtures. Production always calls migrate and
// therefore applies every registered migration.
func migrateThrough(db *sql.DB, maxVersion int) error {
	// In test mode, skip fsync and keep the journal in memory so the ~70
	// migration statements don't each pay a disk-sync cost. Applied here
	// (not after Open) because migrations run inside Open().
	if os.Getenv("PAIMOS_TEST_MODE") == "1" {
		db.Exec("PRAGMA synchronous=OFF")
		db.Exec("PRAGMA journal_mode=MEMORY")
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_versions (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_versions: %w", err)
	}

	migrations := []migration{
		{1, []string{
			`CREATE TABLE IF NOT EXISTS users (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				username   TEXT NOT NULL UNIQUE,
				password   TEXT NOT NULL,
				role       TEXT NOT NULL DEFAULT 'member' CHECK(role IN ('admin','member')),
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE TABLE IF NOT EXISTS sessions (
				id         TEXT PRIMARY KEY,
				user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				expires_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS projects (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				name        TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				status      TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE TABLE IF NOT EXISTS issues (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				title       TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				status      TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','in-progress','done','closed')),
				priority    TEXT NOT NULL DEFAULT 'medium' CHECK(priority IN ('low','medium','high')),
				assignee_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_project ON issues(project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_user  ON sessions(user_id)`,
		}},

		{2, []string{
			`ALTER TABLE projects ADD COLUMN key TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN issue_number INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE issues ADD COLUMN type TEXT NOT NULL DEFAULT 'ticket'`,
			`ALTER TABLE issues ADD COLUMN parent_id INTEGER REFERENCES issues(id) ON DELETE SET NULL`,
			`ALTER TABLE issues ADD COLUMN acceptance_criteria TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN notes TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN cost_unit TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN release TEXT NOT NULL DEFAULT ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number ON issues(project_id, issue_number)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent   ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type     ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_costunit ON issues(cost_unit)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_release  ON issues(release)`,
			`UPDATE issues SET issue_number = (
				SELECT COUNT(*) FROM issues i2
				WHERE i2.project_id = issues.project_id AND i2.id <= issues.id
			) WHERE issue_number = 0`,
		}},

		// Migration 3: global tags, join tables, FTS5 search index with triggers
		{3, []string{
			// Tags table
			`CREATE TABLE IF NOT EXISTS tags (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				name        TEXT NOT NULL UNIQUE,
				color       TEXT NOT NULL DEFAULT 'gray',
				description TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL DEFAULT (datetime('now'))
			)`,

			// Join tables
			`CREATE TABLE IF NOT EXISTS issue_tags (
				issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				tag_id   INTEGER NOT NULL REFERENCES tags(id)   ON DELETE CASCADE,
				PRIMARY KEY (issue_id, tag_id)
			)`,
			`CREATE TABLE IF NOT EXISTS project_tags (
				project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				tag_id     INTEGER NOT NULL REFERENCES tags(id)     ON DELETE CASCADE,
				PRIMARY KEY (project_id, tag_id)
			)`,

			// FTS5 virtual table
			// content: space-separated searchable text for the entity
			`CREATE VIRTUAL TABLE IF NOT EXISTS search_index USING fts5(
				entity_type,
				entity_id UNINDEXED,
				content,
				tokenize='porter ascii'
			)`,

			// ── Project triggers ──────────────────────────────────────────────
			`CREATE TRIGGER IF NOT EXISTS trg_projects_ai
				AFTER INSERT ON projects BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('project', NEW.id, NEW.name || ' ' || NEW.key || ' ' || NEW.description);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_projects_au
				AFTER UPDATE ON projects BEGIN
					DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('project', NEW.id, NEW.name || ' ' || NEW.key || ' ' || NEW.description);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_projects_ad
				AFTER DELETE ON projects BEGIN
					DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
				END`,

			// ── Issue triggers ────────────────────────────────────────────────
			`CREATE TRIGGER IF NOT EXISTS trg_issues_ai
				AFTER INSERT ON issues BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						NEW.title || ' ' || NEW.description || ' ' ||
						NEW.acceptance_criteria || ' ' || NEW.notes || ' ' ||
						NEW.cost_unit || ' ' || NEW.release || ' ' || NEW.type);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issues_au
				AFTER UPDATE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						NEW.title || ' ' || NEW.description || ' ' ||
						NEW.acceptance_criteria || ' ' || NEW.notes || ' ' ||
						NEW.cost_unit || ' ' || NEW.release || ' ' || NEW.type);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issues_ad
				AFTER DELETE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
				END`,

			// ── User triggers ─────────────────────────────────────────────────
			`CREATE TRIGGER IF NOT EXISTS trg_users_ai
				AFTER INSERT ON users BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('user', NEW.id, NEW.username || ' ' || NEW.role);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_users_au
				AFTER UPDATE ON users BEGIN
					DELETE FROM search_index WHERE entity_type='user' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('user', NEW.id, NEW.username || ' ' || NEW.role);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_users_ad
				AFTER DELETE ON users BEGIN
					DELETE FROM search_index WHERE entity_type='user' AND entity_id=OLD.id;
				END`,

			// ── Tag triggers ──────────────────────────────────────────────────
			`CREATE TRIGGER IF NOT EXISTS trg_tags_ai
				AFTER INSERT ON tags BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('tag', NEW.id, NEW.name || ' ' || NEW.description);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_tags_au
				AFTER UPDATE ON tags BEGIN
					DELETE FROM search_index WHERE entity_type='tag' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('tag', NEW.id, NEW.name || ' ' || NEW.description);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_tags_ad
				AFTER DELETE ON tags BEGIN
					DELETE FROM search_index WHERE entity_type='tag' AND entity_id=OLD.id;
				END`,

			// ── Backfill existing data into FTS ───────────────────────────────
			`INSERT INTO search_index(entity_type, entity_id, content)
				SELECT 'project', id, name || ' ' || key || ' ' || description FROM projects`,
			`INSERT INTO search_index(entity_type, entity_id, content)
				SELECT 'issue', id,
					title || ' ' || description || ' ' ||
					acceptance_criteria || ' ' || notes || ' ' ||
					cost_unit || ' ' || release || ' ' || type
				FROM issues`,
			`INSERT INTO search_index(entity_type, entity_id, content)
				SELECT 'user', id, username || ' ' || role FROM users`,
		}},
		// Migration 4: depends_on + impacts (plain-text issue-key references, e.g. "ACME-1, ACME-3")
		{4, []string{
			`ALTER TABLE issues ADD COLUMN depends_on TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN impacts    TEXT NOT NULL DEFAULT ''`,
		}},

		// Migration 6: TOTP 2FA — secret + enabled flag on users, pending token table
		{6, []string{
			`ALTER TABLE users ADD COLUMN totp_secret  TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS totp_pending (
				token      TEXT PRIMARY KEY,
				user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				expires_at TEXT NOT NULL
			)`,
		}},

		// Migration 9: comments — threaded comments on issues
		{9, []string{
			`CREATE TABLE IF NOT EXISTS comments (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
				body       TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id, created_at)`,
		}},

		// Migration 8: integrations — one row per provider, config stored as JSON
		{8, []string{
			`CREATE TABLE IF NOT EXISTS integrations (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				provider   TEXT NOT NULL UNIQUE,
				config     TEXT NOT NULL DEFAULT '{}',
				updated_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
		}},

		// Migration 7: API keys — named long-lived tokens for programmatic access
		{7, []string{
			`CREATE TABLE IF NOT EXISTS api_keys (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name       TEXT NOT NULL,
				key_hash   TEXT NOT NULL UNIQUE,
				key_prefix TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				last_used_at TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id)`,
		}},

		// Migration 10: three-phase soft delete for users and projects.
		//
		// users: add status column (active / inactive / deleted).
		//   active   = normal login
		//   inactive = login blocked, data preserved, shown as "Disabled" in UI
		//   deleted  = login blocked, hidden from UI, restorable via DB
		//
		// projects: the existing status column has CHECK(status IN ('active','archived')).
		// SQLite does not support ALTER TABLE ... MODIFY COLUMN, so we recreate the
		// table without the restrictive CHECK and migrate all data. Application logic
		// enforces valid values (active / frozen / archived / deleted).
		//
		// IMPORTANT: We MUST disable foreign_keys before dropping projects_old,
		// otherwise the ON DELETE CASCADE on issues.project_id would wipe all issues.
		// We re-enable foreign_keys after the migration step is complete.
		{10, []string{
			// ── Users: add status column ──────────────────────────────────────
			`ALTER TABLE users ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`,

			// ── Projects: recreate table to drop the restrictive CHECK ─────────
			// Disable FK enforcement for the duration of the table swap
			`PRAGMA foreign_keys=OFF`,
			// Step 1: rename existing table
			`ALTER TABLE projects RENAME TO projects_old`,
			// Step 2: create new table without CHECK constraint on status
			`CREATE TABLE projects (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				name        TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				status      TEXT NOT NULL DEFAULT 'active',
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
				key         TEXT NOT NULL DEFAULT ''
			)`,
			// Step 3: copy data
			`INSERT INTO projects(id,name,description,status,created_at,updated_at,key)
				SELECT id,name,description,status,created_at,updated_at,key FROM projects_old`,
			// Step 4: drop old table — safe now because FK enforcement is off
			`DROP TABLE projects_old`,
			// Step 5: recreate indexes and triggers
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_key ON projects(key)`,
			`CREATE TRIGGER IF NOT EXISTS trg_projects_ai2
				AFTER INSERT ON projects BEGIN
					DELETE FROM search_index WHERE entity_type='project' AND entity_id=NEW.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('project', NEW.id, NEW.name || ' ' || NEW.key || ' ' || NEW.description);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_projects_au2
				AFTER UPDATE ON projects BEGIN
					DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('project', NEW.id, NEW.name || ' ' || NEW.key || ' ' || NEW.description);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_projects_ad2
				AFTER DELETE ON projects BEGIN
					DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
				END`,
			// Re-enable FK enforcement
			`PRAGMA foreign_keys=ON`,
		}},

		// Migration 11: fix broken FK references caused by migration 10.
		//
		// When migration 10 renamed projects→projects_old and created a new projects table,
		// SQLite internally rewrote the FK references in `issues` and `project_tags` to
		// point to "projects_old". Now projects_old is gone, so any INSERT/UPDATE on those
		// tables fails with "no such table: main.projects_old".
		//
		// Fix: recreate issues and project_tags with correct FK references to `projects`.
		// Full column lists preserved exactly. FK-off pattern required.
		{11, []string{
			`PRAGMA foreign_keys=OFF`,

			// ── Recreate issues ───────────────────────────────────────────────
			`ALTER TABLE issues RENAME TO issues_old`,
			`CREATE TABLE issues (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				title               TEXT NOT NULL,
				description         TEXT NOT NULL DEFAULT '',
				status              TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','in-progress','done','closed')),
				priority            TEXT NOT NULL DEFAULT 'medium' CHECK(priority IN ('low','medium','high')),
				assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at          TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
				issue_number        INTEGER NOT NULL DEFAULT 0,
				type                TEXT NOT NULL DEFAULT 'ticket',
				parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				acceptance_criteria TEXT NOT NULL DEFAULT '',
				notes               TEXT NOT NULL DEFAULT '',
				cost_unit           TEXT NOT NULL DEFAULT '',
				release             TEXT NOT NULL DEFAULT '',
				depends_on          TEXT NOT NULL DEFAULT '',
				impacts             TEXT NOT NULL DEFAULT ''
			)`,
			`INSERT INTO issues SELECT * FROM issues_old`,
			`DROP TABLE issues_old`,

			// ── Restore issue indexes ─────────────────────────────────────────
			`CREATE INDEX IF NOT EXISTS idx_issues_project        ON issues(project_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number ON issues(project_id, issue_number)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent         ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type           ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_costunit       ON issues(cost_unit)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_release        ON issues(release)`,

			// ── Recreate project_tags ─────────────────────────────────────────
			`ALTER TABLE project_tags RENAME TO project_tags_old`,
			`CREATE TABLE project_tags (
				project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				tag_id     INTEGER NOT NULL REFERENCES tags(id)     ON DELETE CASCADE,
				PRIMARY KEY (project_id, tag_id)
			)`,
			`INSERT INTO project_tags SELECT * FROM project_tags_old`,
			`DROP TABLE project_tags_old`,

			`PRAGMA foreign_keys=ON`,
		}},

		// Migration 5: issue change history — full JSON snapshot per save
		{5, []string{
			`CREATE TABLE IF NOT EXISTS issue_history (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
				snapshot   TEXT NOT NULL,
				changed_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_history_issue ON issue_history(issue_id, changed_at)`,
		}},

		// Migration 12: issue_relations — unified M:N relation table replacing
		// parent_id for group→ticket links and free-text depends_on/impacts fields.
		// Relation types: groups | sprint | depends_on | impacts
		{12, []string{
			`CREATE TABLE IF NOT EXISTS issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL,
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source ON issue_relations(source_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target ON issue_relations(target_id)`,
		}},

		// Migration 13: group-level and sprint-level nullable columns on issues.
		// All additive — safe, no data loss.
		{13, []string{
			// Group (epic, cost_unit) fields
			`ALTER TABLE issues ADD COLUMN billing_type  TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN total_budget  REAL`,
			`ALTER TABLE issues ADD COLUMN rate_hourly   REAL`,
			`ALTER TABLE issues ADD COLUMN rate_package  REAL`,
			// Release fields
			`ALTER TABLE issues ADD COLUMN start_date    TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN end_date      TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN group_state   TEXT NOT NULL DEFAULT ''`,
			// Sprint fields
			`ALTER TABLE issues ADD COLUMN sprint_state  TEXT NOT NULL DEFAULT ''`,
			// Jira mapping fields (shared across group types and sprint)
			`ALTER TABLE issues ADD COLUMN jira_id       TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN jira_version  TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE issues ADD COLUMN jira_text     TEXT NOT NULL DEFAULT ''`,
		}},

		// Migration 14: expand issues.type to allow cost_unit, release, sprint.
		// The current CHECK(type IN ('epic','ticket','task')) must be removed.
		// Also rename status values: open→backlog, done→complete, closed→canceled.
		// Requires table recreate with FK-off pattern; data migration for status.
		{14, []string{
			`PRAGMA foreign_keys=OFF`,

			`ALTER TABLE issues RENAME TO issues_old14`,
			`CREATE TABLE issues (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				title               TEXT NOT NULL,
				description         TEXT NOT NULL DEFAULT '',
				status              TEXT NOT NULL DEFAULT 'backlog',
				priority            TEXT NOT NULL DEFAULT 'medium' CHECK(priority IN ('low','medium','high')),
				assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at          TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
				issue_number        INTEGER NOT NULL DEFAULT 0,
				type                TEXT NOT NULL DEFAULT 'ticket',
				parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				acceptance_criteria TEXT NOT NULL DEFAULT '',
				notes               TEXT NOT NULL DEFAULT '',
				cost_unit           TEXT NOT NULL DEFAULT '',
				release             TEXT NOT NULL DEFAULT '',
				depends_on          TEXT NOT NULL DEFAULT '',
				impacts             TEXT NOT NULL DEFAULT '',
				billing_type        TEXT NOT NULL DEFAULT '',
				total_budget        REAL,
				rate_hourly         REAL,
				rate_package        REAL,
				start_date          TEXT NOT NULL DEFAULT '',
				end_date            TEXT NOT NULL DEFAULT '',
				group_state         TEXT NOT NULL DEFAULT '',
				sprint_state        TEXT NOT NULL DEFAULT '',
				jira_id             TEXT NOT NULL DEFAULT '',
				jira_version        TEXT NOT NULL DEFAULT '',
				jira_text           TEXT NOT NULL DEFAULT ''
			)`,
			// Copy data with status rename
			`INSERT INTO issues(id,project_id,title,description,status,priority,
			                    assignee_id,created_at,updated_at,issue_number,type,parent_id,
			                    acceptance_criteria,notes,cost_unit,release,depends_on,impacts,
			                    billing_type,total_budget,rate_hourly,rate_package,
			                    start_date,end_date,group_state,sprint_state,jira_id,jira_version,jira_text)
			SELECT id,project_id,title,description,
			       CASE status
			           WHEN 'open'   THEN 'backlog'
			           WHEN 'done'   THEN 'complete'
			           WHEN 'closed' THEN 'canceled'
			           ELSE status
			       END,
			       priority,assignee_id,created_at,updated_at,issue_number,type,parent_id,
			       acceptance_criteria,notes,cost_unit,release,depends_on,impacts,
			       COALESCE(billing_type,''),total_budget,rate_hourly,rate_package,
			       COALESCE(start_date,''),COALESCE(end_date,''),COALESCE(group_state,''),
			       COALESCE(sprint_state,''),COALESCE(jira_id,''),COALESCE(jira_version,''),COALESCE(jira_text,'')
			FROM issues_old14`,
			`DROP TABLE issues_old14`,

			// Restore indexes
			`CREATE INDEX IF NOT EXISTS idx_issues_project        ON issues(project_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number ON issues(project_id, issue_number)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent         ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type           ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_costunit       ON issues(cost_unit)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_release        ON issues(release)`,

			`PRAGMA foreign_keys=ON`,
		}},

		// Migration 15: add product_owner (FK→users) and customer_id to projects.
		// Additive — safe.
		{15, []string{
			`ALTER TABLE projects ADD COLUMN product_owner INTEGER REFERENCES users(id) ON DELETE SET NULL`,
			`ALTER TABLE projects ADD COLUMN customer_id   TEXT NOT NULL DEFAULT ''`,
		}},

		// Migration 19: fix broken FK in issue_relations.
		// Migration 14 renamed issues→issues_old14, which caused SQLite to rewrite the
		// REFERENCES clause in issue_relations to point at issues_old14. After migration 14
		// dropped issues_old14 and recreated issues, issue_relations was left with a dangling
		// FK reference, making any INSERT fail with "no such table: main.issues_old14".
		// Fix: recreate issue_relations with the correct REFERENCES issues(id).
		// MUST run before migrations 17 and 18 (which INSERT into issue_relations).
		{19, []string{
			`PRAGMA foreign_keys=OFF`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old19`,
			`CREATE TABLE issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL,
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`INSERT OR IGNORE INTO issue_relations SELECT source_id, target_id, type FROM issue_relations_old19`,
			`DROP TABLE issue_relations_old19`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source ON issue_relations(source_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target ON issue_relations(target_id)`,
			`PRAGMA foreign_keys=ON`,
		}},

		// Migration 17: data migration — wire existing epic→ticket parent_id links into
		// issue_relations(type='groups'). After this, parent_id is only used for task→ticket.
		// Safe: additive insert into issue_relations; parent_id column left intact for now.
		{17, []string{
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT parent_id, id, 'groups'
			 FROM issues
			 WHERE type = 'ticket'
			   AND parent_id IS NOT NULL
			   AND EXISTS (SELECT 1 FROM issues p WHERE p.id = issues.parent_id AND p.type = 'epic')`,
		}},

		// Migration 18: data migration — parse free-text depends_on/impacts fields and
		// insert resolved issue_relations rows. Rows that cannot be resolved (bad keys,
		// cross-project references) are silently skipped; we preserve the free-text column.
		// issue_key is not stored; reconstruct as projects.key || '-' || issues.issue_number.
		// NOTE: only handles the first comma-separated token per row (covers ~99% of real data).
		// Multi-value rows are rare; a future cleanup migration can handle them if needed.
		{18, []string{
			// depends_on: resolve first token to issue id via reconstructed issue_key
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT i.id, i2.id, 'depends_on'
			 FROM issues i
			 JOIN issues i2 ON (
			   SELECT p.key || '-' || i2.issue_number FROM projects p WHERE p.id = i2.project_id
			 ) = TRIM(SUBSTR(i.depends_on || ',', 1, INSTR(i.depends_on || ',', ',') - 1))
			 WHERE i.depends_on != ''`,
			// impacts: same pattern
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT i.id, i2.id, 'impacts'
			 FROM issues i
			 JOIN issues i2 ON (
			   SELECT p.key || '-' || i2.issue_number FROM projects p WHERE p.id = i2.project_id
			 ) = TRIM(SUBSTR(i.impacts || ',', 1, INSTR(i.impacts || ',', ',') - 1))
			 WHERE i.impacts != ''`,
		}},

		// Migration 16: time_entries — ticket-level start/stop time tracking.
		// New table — safe.
		{16, []string{
			`CREATE TABLE IF NOT EXISTS time_entries (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				ticket_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				started_at  TEXT NOT NULL,
				stopped_at  TEXT,
				override    REAL,
				comment     TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_ticket ON time_entries(ticket_id)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_user   ON time_entries(user_id)`,
		}},

		// Migration 20: fix broken FK references in issue_tags, comments, issue_history.
		// Prior migrations renamed issues→issues_old, causing SQLite to silently rewrite
		// REFERENCES to point at "issues_old" instead of "issues". With foreign_keys=ON
		// this causes every DML on those tables to fail with "no such table: main.issues_old".
		// Fix: recreate all three tables with correct REFERENCES issues(id).
		{20, []string{
			`PRAGMA foreign_keys=OFF`,

			// issue_tags
			`ALTER TABLE issue_tags RENAME TO issue_tags_old20`,
			`CREATE TABLE issue_tags (
				issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				tag_id   INTEGER NOT NULL REFERENCES tags(id)   ON DELETE CASCADE,
				PRIMARY KEY (issue_id, tag_id)
			)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old20`,
			`DROP TABLE issue_tags_old20`,

			// comments
			`ALTER TABLE comments RENAME TO comments_old20`,
			`CREATE TABLE comments (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
				body       TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO comments SELECT * FROM comments_old20`,
			`DROP TABLE comments_old20`,
			`CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id, created_at)`,

			// issue_history
			`ALTER TABLE issue_history RENAME TO issue_history_old20`,
			`CREATE TABLE issue_history (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
				snapshot   TEXT NOT NULL,
				changed_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO issue_history SELECT * FROM issue_history_old20`,
			`DROP TABLE issue_history_old20`,
			`CREATE INDEX IF NOT EXISTS idx_issue_history_issue ON issue_history(issue_id, changed_at)`,

			`PRAGMA foreign_keys=ON`,
		}},

		// Migration 21: views table — saved column+filter sets per user.
		// is_shared=1 → visible to all users; is_admin_default=1 → appears in "Basics" section.
		{21, []string{
			`CREATE TABLE IF NOT EXISTS views (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				title            TEXT    NOT NULL,
				description      TEXT    NOT NULL DEFAULT '',
				columns_json     TEXT    NOT NULL DEFAULT '[]',
				filters_json     TEXT    NOT NULL DEFAULT '{}',
				is_shared        INTEGER NOT NULL DEFAULT 0,
				is_admin_default INTEGER NOT NULL DEFAULT 0,
				created_at       TEXT    NOT NULL DEFAULT (datetime('now')),
				updated_at       TEXT    NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_views_user ON views(user_id)`,
		}},

		// Migration 22: seed the "Default" admin view.
		// columns_json = hidden keys (cost_unit, release, and all v2 fields).
		// Visible = Key, Type, Title, Status, Priority, Assignee, Tags.
		// Inserts only if no is_admin_default view named "Default" already exists.
		{22, []string{
			`INSERT INTO views (user_id, title, description, columns_json, filters_json, is_shared, is_admin_default)
			 SELECT u.id,
			        'Default',
			        'Standard view: Key, Type, Title, Status, Priority, Assignee, Tags.',
			        '["cost_unit","release","billing_type","total_budget","rate_hourly","rate_package","start_date","end_date","group_state","sprint_state","jira_id","jira_version","jira_text"]',
			        '{}',
			        1, 1
			 FROM users u
			 WHERE u.role = 'admin'
			   AND NOT EXISTS (
			       SELECT 1 FROM views WHERE is_admin_default = 1 AND title = 'Default'
			   )
			 ORDER BY u.id LIMIT 1`,
		}},

		// Migration 23: make project_id nullable on issues to support project-less sprints.
		// Requires table recreate (SQLite can't ALTER NOT NULL → NULL).
		// The (project_id, issue_number) unique index is replaced with a partial one that
		// only applies when project_id IS NOT NULL — orphan sprints get issue_number=0.
		{23, []string{
			`PRAGMA foreign_keys=OFF`,
			`ALTER TABLE issues RENAME TO issues_old23`,
			`CREATE TABLE issues (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
				title               TEXT NOT NULL,
				description         TEXT NOT NULL DEFAULT '',
				status              TEXT NOT NULL DEFAULT 'backlog',
				priority            TEXT NOT NULL DEFAULT 'medium' CHECK(priority IN ('low','medium','high')),
				assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at          TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
				issue_number        INTEGER NOT NULL DEFAULT 0,
				type                TEXT NOT NULL DEFAULT 'ticket',
				parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				acceptance_criteria TEXT NOT NULL DEFAULT '',
				notes               TEXT NOT NULL DEFAULT '',
				cost_unit           TEXT NOT NULL DEFAULT '',
				release             TEXT NOT NULL DEFAULT '',
				depends_on          TEXT NOT NULL DEFAULT '',
				impacts             TEXT NOT NULL DEFAULT '',
				billing_type        TEXT NOT NULL DEFAULT '',
				total_budget        REAL,
				rate_hourly         REAL,
				rate_package        REAL,
				start_date          TEXT NOT NULL DEFAULT '',
				end_date            TEXT NOT NULL DEFAULT '',
				group_state         TEXT NOT NULL DEFAULT '',
				sprint_state        TEXT NOT NULL DEFAULT '',
				jira_id             TEXT NOT NULL DEFAULT '',
				jira_version        TEXT NOT NULL DEFAULT '',
				jira_text           TEXT NOT NULL DEFAULT ''
			)`,
			`INSERT INTO issues SELECT * FROM issues_old23`,
			`DROP TABLE issues_old23`,
			`CREATE INDEX IF NOT EXISTS idx_issues_project        ON issues(project_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number ON issues(project_id, issue_number) WHERE project_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent         ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type           ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_costunit       ON issues(cost_unit)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_release        ON issues(release)`,
			`PRAGMA foreign_keys=ON`,
		}},

		// Migration 24: add archived flag to issues (for sprints) +
		// index on issue_relations(target_id, type) for sprint_ids subquery performance.
		{24, []string{
			`ALTER TABLE issues ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target ON issue_relations(target_id, type)`,
		}},

		// Migration 25: enhanced user profiles — nickname (≤3 chars for avatar badge),
		// first/last name, email, and avatar_path (relative path under STATIC_DIR).
		{25, []string{
			`ALTER TABLE users ADD COLUMN nickname   TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN first_name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN last_name  TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN email      TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN avatar_path TEXT NOT NULL DEFAULT ''`,
		}},

		// Migration 26: rewrite legacy avatar paths from /avatars/{n}.jpg to
		// /api/avatars/{n}.jpg — avatars moved from STATIC_DIR to DATA_DIR
		// (volume-mounted) so they survive container rebuilds.
		{26, []string{
			`UPDATE users SET avatar_path = REPLACE(avatar_path, '/avatars/', '/api/avatars/')
			 WHERE avatar_path LIKE '/avatars/%' AND avatar_path NOT LIKE '/api/%'`,
		}},

		// Migration 27: fix broken FK references caused by migration 23.
		//
		// Migration 23 renamed issues→issues_old23 then recreated issues.
		// SQLite silently rewrote all REFERENCES in child tables (issue_tags,
		// comments, issue_history) to point at issues_old23. After DROP TABLE
		// issues_old23, every DML on those tables failed with:
		//   "no such table: main.issues_old23"
		// This blocked tag attachment and comment creation for all users.
		// Fix: same pattern as migration 20 — recreate all three tables with
		// correct REFERENCES issues(id). FK enforcement off during swap.
		{27, []string{
			`PRAGMA foreign_keys=OFF`,

			// issue_tags
			`ALTER TABLE issue_tags RENAME TO issue_tags_old27`,
			`CREATE TABLE issue_tags (
				issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				tag_id   INTEGER NOT NULL REFERENCES tags(id)   ON DELETE CASCADE,
				PRIMARY KEY (issue_id, tag_id)
			)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old27`,
			`DROP TABLE issue_tags_old27`,

			// comments
			`ALTER TABLE comments RENAME TO comments_old27`,
			`CREATE TABLE comments (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
				body       TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO comments SELECT * FROM comments_old27`,
			`DROP TABLE comments_old27`,
			`CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id, created_at)`,

			// issue_history
			`ALTER TABLE issue_history RENAME TO issue_history_old27`,
			`CREATE TABLE issue_history (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
				snapshot   TEXT NOT NULL,
				changed_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO issue_history SELECT * FROM issue_history_old27`,
			`DROP TABLE issue_history_old27`,
			`CREATE INDEX IF NOT EXISTS idx_issue_history_issue ON issue_history(issue_id, changed_at)`,

			`PRAGMA foreign_keys=ON`,
		}},

		// Migration 28: fix stale FTS5 triggers left by migration 23.
		//
		// When migration 23 renamed issues→issues_old23 and created a new issues table,
		// SQLite automatically remapped the existing FTS triggers (trg_issues_ai/au/ad)
		// to fire on issues_old23. After DROP TABLE issues_old23 those triggers became
		// orphaned. Migration 27 fixes the FK references; this migration fixes the triggers.
		//
		// Fix: drop all stale issue triggers by name, then recreate them on issues.

		{29, []string{
			// Migration 29: editor preferences per user.
			// markdown_default — render long-text fields in Markdown by default.
			// monospace_fields  — use monospace font for long-text fields.
			`ALTER TABLE users ADD COLUMN markdown_default INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN monospace_fields  INTEGER NOT NULL DEFAULT 0`,
		}},

		{28, []string{
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai
				AFTER INSERT ON issues BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						NEW.title || ' ' || NEW.description || ' ' ||
						NEW.acceptance_criteria || ' ' || NEW.notes || ' ' ||
						NEW.cost_unit || ' ' || NEW.release || ' ' || NEW.type);
				END`,
			`CREATE TRIGGER trg_issues_au
				AFTER UPDATE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						NEW.title || ' ' || NEW.description || ' ' ||
						NEW.acceptance_criteria || ' ' || NEW.notes || ' ' ||
						NEW.cost_unit || ' ' || NEW.release || ' ' || NEW.type);
				END`,
			`CREATE TRIGGER trg_issues_ad
				AFTER DELETE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
				END`,
		}},

		// Migration 30: expand issue FTS coverage + add comment FTS entity.
		//
		// Issue triggers (28) only indexed 7 fields. This migration drops and
		// recreates them to also include depends_on, impacts, jira_id,
		// jira_version, jira_text — all added in migrations 4 and 13 but never
		// backfilled into FTS.
		//
		// Also adds a new 'comment' entity type to search_index with
		// INSERT/DELETE triggers on the comments table (UPDATE not needed —
		// comments are immutable after creation in the current UI).
		//
		// Must run AFTER migration 28 (which it supersedes) so the correct
		// triggers are active on first install and on existing DBs.
		// Migration 31: fix broken FK in issue_relations (again).
		// Migration 23 renamed issues→issues_old23, which caused SQLite to
		// silently rewrite REFERENCES in issue_relations to point at issues_old23.
		// Migration 27 fixed issue_tags/comments/issue_history but missed
		// issue_relations. Exact same pattern as migration 19 (issues_old14).
		{31, []string{
			`PRAGMA foreign_keys=OFF`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old31`,
			`CREATE TABLE issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL,
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`INSERT OR IGNORE INTO issue_relations SELECT source_id, target_id, type FROM issue_relations_old31`,
			`DROP TABLE issue_relations_old31`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source ON issue_relations(source_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target ON issue_relations(target_id, type)`,
			`PRAGMA foreign_keys=ON`,
		}},

		{30, []string{
			// Drop old issue triggers (from migration 28)
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,

			// Recreate with expanded content
			`CREATE TRIGGER trg_issues_ai
				AFTER INSERT ON issues BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						COALESCE(NEW.title,'') || ' ' ||
						COALESCE(NEW.description,'') || ' ' ||
						COALESCE(NEW.acceptance_criteria,'') || ' ' ||
						COALESCE(NEW.notes,'') || ' ' ||
						COALESCE(NEW.cost_unit,'') || ' ' ||
						COALESCE(NEW.release,'') || ' ' ||
						COALESCE(NEW.type,'') || ' ' ||
						COALESCE(NEW.depends_on,'') || ' ' ||
						COALESCE(NEW.impacts,'') || ' ' ||
						COALESCE(NEW.jira_id,'') || ' ' ||
						COALESCE(NEW.jira_version,'') || ' ' ||
						COALESCE(NEW.jira_text,''));
				END`,
			`CREATE TRIGGER trg_issues_au
				AFTER UPDATE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						COALESCE(NEW.title,'') || ' ' ||
						COALESCE(NEW.description,'') || ' ' ||
						COALESCE(NEW.acceptance_criteria,'') || ' ' ||
						COALESCE(NEW.notes,'') || ' ' ||
						COALESCE(NEW.cost_unit,'') || ' ' ||
						COALESCE(NEW.release,'') || ' ' ||
						COALESCE(NEW.type,'') || ' ' ||
						COALESCE(NEW.depends_on,'') || ' ' ||
						COALESCE(NEW.impacts,'') || ' ' ||
						COALESCE(NEW.jira_id,'') || ' ' ||
						COALESCE(NEW.jira_version,'') || ' ' ||
						COALESCE(NEW.jira_text,''));
				END`,
			`CREATE TRIGGER trg_issues_ad
				AFTER DELETE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
				END`,

			// Comment triggers (comments are immutable — no UPDATE trigger needed)
			`CREATE TRIGGER IF NOT EXISTS trg_comments_ai
				AFTER INSERT ON comments BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('comment', NEW.id, COALESCE(NEW.body,''));
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_comments_ad
				AFTER DELETE ON comments BEGIN
					DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.id;
				END`,

			// Backfill issues — delete stale FTS rows and re-insert with full content
			`DELETE FROM search_index WHERE entity_type='issue'`,
			`INSERT INTO search_index(entity_type, entity_id, content)
				SELECT 'issue', id,
					COALESCE(title,'') || ' ' ||
					COALESCE(description,'') || ' ' ||
					COALESCE(acceptance_criteria,'') || ' ' ||
					COALESCE(notes,'') || ' ' ||
					COALESCE(cost_unit,'') || ' ' ||
					COALESCE(release,'') || ' ' ||
					COALESCE(type,'') || ' ' ||
					COALESCE(depends_on,'') || ' ' ||
					COALESCE(impacts,'') || ' ' ||
					COALESCE(jira_id,'') || ' ' ||
					COALESCE(jira_version,'') || ' ' ||
					COALESCE(jira_text,'')
				FROM issues`,

			// Backfill comments
			`DELETE FROM search_index WHERE entity_type='comment'`,
			`INSERT INTO search_index(entity_type, entity_id, content)
			SELECT 'comment', id, COALESCE(body,'') FROM comments`,
		}},

		// ── Migration 32: Schema Normalisation ────────────────────────────────────
		//
		// One authoritative migration that eliminates 31 migrations of accumulated
		// scar tissue. No data is destroyed — all existing rows are preserved.
		//
		// Changes (in order):
		//  1. Normalise status enum:  complete→done, canceled→cancelled  (data UPDATE)
		//  2. Flip sprint relations:  source↔target swapped so source=sprint, target=issue
		//     (consistent with groups convention: source=container, target=member)
		//  3. Recreate issues with CHECK constraints + drop legacy depends_on/impacts columns
		//     + rename time_entries.ticket_id→issue_id in the same sweep
		//  4. Recreate all 5 child tables (issue_tags, comments, issue_history,
		//     issue_relations, time_entries) with correct FKs to new issues table
		//  5. Add missing indexes
		//  6. Drop orphaned project triggers (from original M3, orphaned by M10 recreate)
		//  7. Recreate project triggers with clean names (no "2" suffix)
		//  8. Update user FTS triggers to include profile fields (nickname, first_name,
		//     last_name, email) added in M25 but never indexed
		//  9. Backfill FTS — rebuild issues + users from scratch
		{32, []string{
			// ── Step 1: Normalise status values ───────────────────────────────────
			// Map ALL non-canonical values to the 4 canonical ones so the CHECK
			// constraint in step 3 doesn't reject any existing rows.
			`UPDATE issues SET status = 'backlog'     WHERE status IN ('open')`,
			`UPDATE issues SET status = 'done'        WHERE status IN ('complete', 'closed')`,
			`UPDATE issues SET status = 'cancelled'   WHERE status IN ('canceled')`,

			// ── Step 1b: Safety cleanup (idempotent retry guard) ─────────────────
			// If M32 was partially applied before (e.g. step 3 failed), the RENAME
			// may have already created issues_old32. Drop it so the rename succeeds.
			`DROP TABLE IF EXISTS issues_old32`,
			`DROP TABLE IF EXISTS issue_tags_old32`,
			`DROP TABLE IF EXISTS comments_old32`,
			`DROP TABLE IF EXISTS issue_history_old32`,
			`DROP TABLE IF EXISTS issue_relations_old32`,
			`DROP TABLE IF EXISTS time_entries_old32`,

			// ── Step 2: Flip sprint relations (source=sprint, target=issue) ───────
			// Previously: source=issue, target=sprint.  Convention was inconsistent
			// with groups (source=container).  Swap so source is always the container.
			// A temp column approach isn't needed: we swap the pair atomically via CTE.
			`UPDATE issue_relations
			 SET source_id = target_id,
			     target_id = source_id
			 WHERE type = 'sprint'`,

			// ── Step 3 + 4: Recreate tables with corrected schema ────────────────
			`PRAGMA foreign_keys=OFF`,

			// issues — add CHECK on status + type, drop depends_on/impacts columns
			`ALTER TABLE issues RENAME TO issues_old32`,
			`CREATE TABLE issues (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
				title               TEXT NOT NULL,
				description         TEXT NOT NULL DEFAULT '',
				status              TEXT NOT NULL DEFAULT 'backlog'
				                    CHECK(status IN ('backlog','in-progress','done','cancelled')),
				priority            TEXT NOT NULL DEFAULT 'medium'
				                    CHECK(priority IN ('low','medium','high')),
				assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at          TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
				issue_number        INTEGER NOT NULL DEFAULT 0,
				type                TEXT NOT NULL DEFAULT 'ticket'
				                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task')),
				parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				acceptance_criteria TEXT NOT NULL DEFAULT '',
				notes               TEXT NOT NULL DEFAULT '',
				cost_unit           TEXT NOT NULL DEFAULT '',
				release             TEXT NOT NULL DEFAULT '',
				billing_type        TEXT NOT NULL DEFAULT '',
				total_budget        REAL,
				rate_hourly         REAL,
				rate_package        REAL,
				start_date          TEXT NOT NULL DEFAULT '',
				end_date            TEXT NOT NULL DEFAULT '',
				group_state         TEXT NOT NULL DEFAULT '',
				sprint_state        TEXT NOT NULL DEFAULT '',
				jira_id             TEXT NOT NULL DEFAULT '',
				jira_version        TEXT NOT NULL DEFAULT '',
				jira_text           TEXT NOT NULL DEFAULT '',
				archived            INTEGER NOT NULL DEFAULT 0
			)`,
			// Copy all columns except depends_on and impacts (dropped)
			`INSERT INTO issues (
				id, project_id, title, description, status, priority, assignee_id,
				created_at, updated_at, issue_number, type, parent_id,
				acceptance_criteria, notes, cost_unit, release,
				billing_type, total_budget, rate_hourly, rate_package,
				start_date, end_date, group_state, sprint_state,
				jira_id, jira_version, jira_text, archived
			) SELECT
				id, project_id, title, description, status, priority, assignee_id,
				created_at, updated_at, issue_number, type, parent_id,
				acceptance_criteria, notes, cost_unit, release,
				billing_type, total_budget, rate_hourly, rate_package,
				start_date, end_date, group_state, sprint_state,
				jira_id, jira_version, jira_text, archived
			FROM issues_old32`,
			`DROP TABLE issues_old32`,

			// Recreate indexes on issues
			`CREATE INDEX IF NOT EXISTS idx_issues_project        ON issues(project_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number
			 ON issues(project_id, issue_number) WHERE project_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent         ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type           ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_status         ON issues(status)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_assignee       ON issues(assignee_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_updated        ON issues(updated_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_costunit       ON issues(cost_unit)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_release        ON issues(release)`,

			// issue_tags — recreate with correct FK
			`ALTER TABLE issue_tags RENAME TO issue_tags_old32`,
			`CREATE TABLE issue_tags (
				issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				tag_id   INTEGER NOT NULL REFERENCES tags(id)   ON DELETE CASCADE,
				PRIMARY KEY (issue_id, tag_id)
			)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old32`,
			`DROP TABLE issue_tags_old32`,

			// comments — recreate with correct FK
			`ALTER TABLE comments RENAME TO comments_old32`,
			`CREATE TABLE comments (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
				body       TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO comments SELECT * FROM comments_old32`,
			`DROP TABLE comments_old32`,
			`CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id, created_at)`,

			// issue_history — recreate with correct FK
			`ALTER TABLE issue_history RENAME TO issue_history_old32`,
			`CREATE TABLE issue_history (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
				snapshot   TEXT NOT NULL,
				changed_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO issue_history SELECT * FROM issue_history_old32`,
			`DROP TABLE issue_history_old32`,
			`CREATE INDEX IF NOT EXISTS idx_issue_history_issue ON issue_history(issue_id, changed_at)`,

			// issue_relations — recreate with correct FK + CHECK on type
			`ALTER TABLE issue_relations RENAME TO issue_relations_old32`,
			`CREATE TABLE issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL
				          CHECK(type IN ('groups','sprint','depends_on','impacts')),
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`INSERT OR IGNORE INTO issue_relations SELECT source_id, target_id, type
			 FROM issue_relations_old32`,
			`DROP TABLE issue_relations_old32`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source
			 ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target
			 ON issue_relations(target_id, type)`,

			// time_entries — rename ticket_id→issue_id for consistency
			`ALTER TABLE time_entries RENAME TO time_entries_old32`,
			`CREATE TABLE time_entries (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				user_id    INTEGER NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
				started_at TEXT NOT NULL,
				stopped_at TEXT,
				override   REAL,
				comment    TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO time_entries(id, issue_id, user_id, started_at, stopped_at, override, comment, created_at)
			 SELECT id, ticket_id, user_id, started_at, stopped_at, override, comment, created_at
			 FROM time_entries_old32`,
			`DROP TABLE time_entries_old32`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_issue ON time_entries(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_user  ON time_entries(user_id)`,

			// Add missing FK indexes on other tables
			`CREATE INDEX IF NOT EXISTS idx_totp_pending_user   ON totp_pending(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_projects_owner      ON projects(product_owner)`,

			`PRAGMA foreign_keys=ON`,

			// ── Step 5: Add CHECK constraints to projects + users via ALTER TABLE ─
			// SQLite doesn't support ALTER TABLE ADD CONSTRAINT.
			// Instead we enforce via app logic (already done) — document the expected
			// values here via comments in this migration for future reference.
			// projects.status: active | frozen | archived | deleted
			// users.status:    active | inactive | deleted
			// (Full table recreation not worth it — no data enforcement gap in practice)

			// ── Step 6+7: Project FTS triggers (drop orphans, recreate clean) ─────
			// Original trg_projects_ai/au/ad were created in M3 on the pre-M10
			// projects table, then orphaned when M10 dropped that table.
			// M10 created trg_projects_ai2/au2/ad2 on the new table.
			// This migration: drop all, recreate with clean names (no "2" suffix).
			`DROP TRIGGER IF EXISTS trg_projects_ai`,
			`DROP TRIGGER IF EXISTS trg_projects_au`,
			`DROP TRIGGER IF EXISTS trg_projects_ad`,
			`DROP TRIGGER IF EXISTS trg_projects_ai2`,
			`DROP TRIGGER IF EXISTS trg_projects_au2`,
			`DROP TRIGGER IF EXISTS trg_projects_ad2`,
			`CREATE TRIGGER trg_projects_ai
				AFTER INSERT ON projects BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('project', NEW.id,
						COALESCE(NEW.name,'') || ' ' || COALESCE(NEW.key,'') || ' ' ||
						COALESCE(NEW.description,''));
				END`,
			`CREATE TRIGGER trg_projects_au
				AFTER UPDATE ON projects BEGIN
					DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('project', NEW.id,
						COALESCE(NEW.name,'') || ' ' || COALESCE(NEW.key,'') || ' ' ||
						COALESCE(NEW.description,''));
				END`,
			`CREATE TRIGGER trg_projects_ad
				AFTER DELETE ON projects BEGIN
					DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
				END`,

			// ── Step 8: Update user FTS triggers to include profile fields ────────
			// M3 triggers only indexed username + role.
			// M25 added nickname, first_name, last_name, email — never indexed.
			`DROP TRIGGER IF EXISTS trg_users_ai`,
			`DROP TRIGGER IF EXISTS trg_users_au`,
			`DROP TRIGGER IF EXISTS trg_users_ad`,
			`CREATE TRIGGER trg_users_ai
				AFTER INSERT ON users BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('user', NEW.id,
						COALESCE(NEW.username,'') || ' ' ||
						COALESCE(NEW.nickname,'') || ' ' ||
						COALESCE(NEW.first_name,'') || ' ' ||
						COALESCE(NEW.last_name,'') || ' ' ||
						COALESCE(NEW.email,'') || ' ' ||
						COALESCE(NEW.role,''));
				END`,
			`CREATE TRIGGER trg_users_au
				AFTER UPDATE ON users BEGIN
					DELETE FROM search_index WHERE entity_type='user' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('user', NEW.id,
						COALESCE(NEW.username,'') || ' ' ||
						COALESCE(NEW.nickname,'') || ' ' ||
						COALESCE(NEW.first_name,'') || ' ' ||
						COALESCE(NEW.last_name,'') || ' ' ||
						COALESCE(NEW.email,'') || ' ' ||
						COALESCE(NEW.role,''));
				END`,
			`CREATE TRIGGER trg_users_ad
				AFTER DELETE ON users BEGIN
					DELETE FROM search_index WHERE entity_type='user' AND entity_id=OLD.id;
				END`,

			// ── Step 9: Rebuild issue + user FTS (drop_on/impacts removed; profile added) ─
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai
				AFTER INSERT ON issues BEGIN
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						COALESCE(NEW.title,'') || ' ' ||
						COALESCE(NEW.description,'') || ' ' ||
						COALESCE(NEW.acceptance_criteria,'') || ' ' ||
						COALESCE(NEW.notes,'') || ' ' ||
						COALESCE(NEW.cost_unit,'') || ' ' ||
						COALESCE(NEW.release,'') || ' ' ||
						COALESCE(NEW.type,'') || ' ' ||
						COALESCE(NEW.jira_id,'') || ' ' ||
						COALESCE(NEW.jira_version,'') || ' ' ||
						COALESCE(NEW.jira_text,''));
				END`,
			`CREATE TRIGGER trg_issues_au
				AFTER UPDATE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
					INSERT INTO search_index(entity_type, entity_id, content)
					VALUES('issue', NEW.id,
						COALESCE(NEW.title,'') || ' ' ||
						COALESCE(NEW.description,'') || ' ' ||
						COALESCE(NEW.acceptance_criteria,'') || ' ' ||
						COALESCE(NEW.notes,'') || ' ' ||
						COALESCE(NEW.cost_unit,'') || ' ' ||
						COALESCE(NEW.release,'') || ' ' ||
						COALESCE(NEW.type,'') || ' ' ||
						COALESCE(NEW.jira_id,'') || ' ' ||
						COALESCE(NEW.jira_version,'') || ' ' ||
						COALESCE(NEW.jira_text,''));
				END`,
			`CREATE TRIGGER trg_issues_ad
				AFTER DELETE ON issues BEGIN
					DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
				END`,

			// Backfill FTS — issues (without removed columns), users (with profile)
			`DELETE FROM search_index WHERE entity_type='issue'`,
			`INSERT INTO search_index(entity_type, entity_id, content)
				SELECT 'issue', id,
					COALESCE(title,'') || ' ' ||
					COALESCE(description,'') || ' ' ||
					COALESCE(acceptance_criteria,'') || ' ' ||
					COALESCE(notes,'') || ' ' ||
					COALESCE(cost_unit,'') || ' ' ||
					COALESCE(release,'') || ' ' ||
					COALESCE(type,'') || ' ' ||
					COALESCE(jira_id,'') || ' ' ||
					COALESCE(jira_version,'') || ' ' ||
					COALESCE(jira_text,'')
				FROM issues`,
			`DELETE FROM search_index WHERE entity_type='user'`,
			`INSERT INTO search_index(entity_type, entity_id, content)
				SELECT 'user', id,
					COALESCE(username,'') || ' ' ||
					COALESCE(nickname,'') || ' ' ||
					COALESCE(first_name,'') || ' ' ||
					COALESCE(last_name,'') || ' ' ||
					COALESCE(email,'') || ' ' ||
			COALESCE(role,'')
			FROM users`,
		}},

		// ── Migration 33 — estimate + AR fields, rename rate_package→rate_lp,
		//    fix comment FTS triggers orphaned by M32 table recreation ─────────
		{33, []string{
			// ── Step 1: Fix comment FTS triggers (orphaned by M32 comments table recreation)
			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai
			AFTER INSERT ON comments BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('comment', NEW.id, COALESCE(NEW.body,''));
			END`,
			`CREATE TRIGGER trg_comments_ad
			AFTER DELETE ON comments BEGIN
				DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.id;
			END`,

			// Backfill comment FTS (any comments created after M32 are missing)
			`DELETE FROM search_index WHERE entity_type='comment'`,
			`INSERT INTO search_index(entity_type, entity_id, content)
			SELECT 'comment', id, COALESCE(body,'') FROM comments`,

			// ── Step 2: Add new estimate + AR columns (additive)
			`ALTER TABLE issues ADD COLUMN estimate_hours REAL`,
			`ALTER TABLE issues ADD COLUMN estimate_lp    REAL`,
			`ALTER TABLE issues ADD COLUMN ar_hours        REAL`,
			`ALTER TABLE issues ADD COLUMN ar_lp           REAL`,

			// ── Step 3: Rename rate_package → rate_lp via table recreation
			`PRAGMA foreign_keys=OFF`,

			`DROP TABLE IF EXISTS issues_old33`,
			`ALTER TABLE issues RENAME TO issues_old33`,
			`CREATE TABLE issues (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
			issue_number        INTEGER NOT NULL DEFAULT 0,
			type                TEXT NOT NULL DEFAULT 'ticket'
			                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task')),
			parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			title               TEXT NOT NULL,
			description         TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes               TEXT NOT NULL DEFAULT '',
			status              TEXT NOT NULL DEFAULT 'backlog'
			                    CHECK(status IN ('backlog','in-progress','done','cancelled')),
			priority            TEXT NOT NULL DEFAULT 'medium'
			                    CHECK(priority IN ('low','medium','high')),
			cost_unit           TEXT NOT NULL DEFAULT '',
			release             TEXT NOT NULL DEFAULT '',
			billing_type        TEXT NOT NULL DEFAULT '',
			total_budget        REAL,
			rate_hourly         REAL,
			rate_lp             REAL,
			start_date          TEXT NOT NULL DEFAULT '',
			end_date            TEXT NOT NULL DEFAULT '',
			group_state         TEXT NOT NULL DEFAULT '',
			sprint_state        TEXT NOT NULL DEFAULT '',
			jira_id             TEXT NOT NULL DEFAULT '',
			jira_version        TEXT NOT NULL DEFAULT '',
			jira_text           TEXT NOT NULL DEFAULT '',
			estimate_hours      REAL,
			estimate_lp         REAL,
			ar_hours            REAL,
			ar_lp               REAL,
			archived            INTEGER NOT NULL DEFAULT 0,
			assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issues
			SELECT id, project_id, issue_number, type, parent_id,
			       title, description, acceptance_criteria, notes,
			       status, priority, cost_unit, release,
			       billing_type, total_budget, rate_hourly, rate_package,
			       start_date, end_date, group_state, sprint_state,
			       jira_id, jira_version, jira_text,
			       estimate_hours, estimate_lp, ar_hours, ar_lp,
			       archived, assignee_id, created_at, updated_at
			FROM issues_old33`,
			`DROP TABLE issues_old33`,

			// Recreate child tables (SQLite FK rewrite bug — same as M27/M31/M32)
			`DROP TABLE IF EXISTS issue_tags_old33`,
			`ALTER TABLE issue_tags RENAME TO issue_tags_old33`,
			`CREATE TABLE issue_tags (
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, tag_id)
		)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old33`,
			`DROP TABLE issue_tags_old33`,

			`DROP TABLE IF EXISTS comments_old33`,
			`ALTER TABLE comments RENAME TO comments_old33`,
			`CREATE TABLE comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			body       TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO comments SELECT * FROM comments_old33`,
			`DROP TABLE comments_old33`,

			`DROP TABLE IF EXISTS issue_history_old33`,
			`ALTER TABLE issue_history RENAME TO issue_history_old33`,
			`CREATE TABLE issue_history (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
			snapshot   TEXT NOT NULL,
			changed_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO issue_history SELECT * FROM issue_history_old33`,
			`DROP TABLE issue_history_old33`,

			`DROP TABLE IF EXISTS issue_relations_old33`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old33`,
			`CREATE TABLE issue_relations (
			source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			type      TEXT NOT NULL
			          CHECK(type IN ('groups','sprint','depends_on','impacts')),
			PRIMARY KEY (source_id, target_id, type)
		)`,
			`INSERT OR IGNORE INTO issue_relations SELECT * FROM issue_relations_old33`,
			`DROP TABLE issue_relations_old33`,

			`DROP TABLE IF EXISTS time_entries_old33`,
			`ALTER TABLE time_entries RENAME TO time_entries_old33`,
			`CREATE TABLE time_entries (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			stopped_at TEXT,
			override   REAL,
			comment    TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO time_entries SELECT * FROM time_entries_old33`,
			`DROP TABLE time_entries_old33`,

			`PRAGMA foreign_keys=ON`,

			// Recreate indexes (dropped with old tables)
			`CREATE INDEX IF NOT EXISTS idx_issues_project     ON issues(project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent      ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_assignee    ON issues(assignee_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type        ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_status      ON issues(status)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_tags_tag     ON issue_tags(tag_id)`,
			`CREATE INDEX IF NOT EXISTS idx_comments_issue     ON comments(issue_id, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_history_issue ON issue_history(issue_id, changed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target ON issue_relations(target_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_issue ON time_entries(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_user  ON time_entries(user_id)`,

			// Recreate FTS triggers (orphaned by table rename)
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai
			AFTER INSERT ON issues BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' ||
					COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' ||
					COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' ||
					COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.type,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' ||
					COALESCE(NEW.jira_version,'') || ' ' ||
					COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_au
			AFTER UPDATE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' ||
					COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' ||
					COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' ||
					COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.type,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' ||
					COALESCE(NEW.jira_version,'') || ' ' ||
					COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_ad
			AFTER DELETE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
			END`,

			// Recreate comment FTS triggers (orphaned again by comments recreation)
			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai
			AFTER INSERT ON comments BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('comment', NEW.id, COALESCE(NEW.body,''));
			END`,
			`CREATE TRIGGER trg_comments_ad
			AFTER DELETE ON comments BEGIN
				DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.id;
			END`,
		}},

		// ── Migration 34 — epic color field ──────────────────────────────────────
		{34, []string{
			`ALTER TABLE issues ADD COLUMN color TEXT`,
		}},

		// ── Migration 35 — attachments table ─────────────────────────────────────
		{35, []string{
			`CREATE TABLE IF NOT EXISTS attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			object_key   TEXT NOT NULL,
			filename     TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL,
			uploaded_by  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_issue ON attachments(issue_id)`,
		}},

		// ── Migration 36 — seed standard admin-default views ──────────────────────
		// Seeds Issues, Epics, Cost Units, Releases admin-default views if they
		// don't already exist. Each INSERT is independently guarded so existing
		// views (e.g. Epics created manually) are never overwritten.
		{36, []string{
			// Issues — tickets and tasks, hides billing/budget/Jira fields
			`INSERT INTO views (user_id, title, description, columns_json, filters_json, is_shared, is_admin_default)
		 SELECT u.id,
		        'Issues',
		        'Tickets and tasks. Hides billing, budget and Jira fields.',
		        '["billing_type","total_budget","rate_hourly","rate_lp","estimate_hours","estimate_lp","ar_hours","ar_lp","group_state","sprint_state","jira_id","jira_version","jira_text"]',
		        '{"type":["ticket","task"]}',
		        1, 1
		 FROM users u
		 WHERE u.role = 'admin'
		   AND NOT EXISTS (SELECT 1 FROM views WHERE is_admin_default = 1 AND title = 'Issues')
		 ORDER BY u.id LIMIT 1`,
			// Epics — billing and timeline fields visible, sprint/Jira hidden
			`INSERT INTO views (user_id, title, description, columns_json, filters_json, is_shared, is_admin_default)
		 SELECT u.id,
		        'Epics',
		        'Epic planning view with billing and timeline fields.',
		        '["cost_unit","release","sprint","sprint_state","jira_id","jira_version","jira_text"]',
		        '{"type":["epic"]}',
		        1, 1
		 FROM users u
		 WHERE u.role = 'admin'
		   AND NOT EXISTS (SELECT 1 FROM views WHERE is_admin_default = 1 AND title = 'Epics')
		 ORDER BY u.id LIMIT 1`,
			// Cost Units — billing and estimation fields visible, Jira/sprint hidden
			`INSERT INTO views (user_id, title, description, columns_json, filters_json, is_shared, is_admin_default)
		 SELECT u.id,
		        'Cost Units',
		        'Cost unit overview with billing and estimation fields.',
		        '["epic","sprint","sprint_state","jira_id","jira_version","jira_text"]',
		        '{"type":["cost_unit"]}',
		        1, 1
		 FROM users u
		 WHERE u.role = 'admin'
		   AND NOT EXISTS (SELECT 1 FROM views WHERE is_admin_default = 1 AND title = 'Cost Units')
		 ORDER BY u.id LIMIT 1`,
			// Releases — timeline and group state visible, finance/Jira hidden
			`INSERT INTO views (user_id, title, description, columns_json, filters_json, is_shared, is_admin_default)
		 SELECT u.id,
		        'Releases',
		        'Release planning with timeline and group state.',
		        '["billing_type","total_budget","rate_hourly","rate_lp","estimate_hours","estimate_lp","ar_hours","ar_lp","sprint_state","jira_id","jira_version","jira_text"]',
		        '{"type":["release"]}',
		        1, 1
		 FROM users u
		 WHERE u.role = 'admin'
		   AND NOT EXISTS (SELECT 1 FROM views WHERE is_admin_default = 1 AND title = 'Releases')
		 ORDER BY u.id LIMIT 1`,
		}},

		// ── Migration 37 — project logo ───────────────────────────────────────────
		{37, []string{
			`ALTER TABLE projects ADD COLUMN logo_path TEXT NOT NULL DEFAULT ''`,
		}},

		// ── Migration 38 — recent projects per user ───────────────────────────────
		{38, []string{
			`CREATE TABLE IF NOT EXISTS user_recent_projects (
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			visited_at TEXT    NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (user_id, project_id)
		)`,
			`CREATE INDEX IF NOT EXISTS idx_urp_user_visited ON user_recent_projects(user_id, visited_at DESC)`,
			`ALTER TABLE users ADD COLUMN recent_projects_limit INTEGER NOT NULL DEFAULT 3`,
		}},

		// ── Migration 39 — internal hourly rate ───────────────────────────────────
		{39, []string{
			`ALTER TABLE users ADD COLUMN internal_rate_hourly REAL`,
			`ALTER TABLE time_entries ADD COLUMN internal_rate_hourly REAL`,
		}},

		// ── Migration 40 — nullable issue_id on attachments (pending uploads) ──
		{40, []string{
			`PRAGMA foreign_keys=OFF`,
			`ALTER TABLE attachments RENAME TO attachments_old`,
			`CREATE TABLE attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER REFERENCES issues(id) ON DELETE CASCADE,
			object_key   TEXT NOT NULL,
			filename     TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO attachments SELECT * FROM attachments_old`,
			`DROP TABLE attachments_old`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_issue ON attachments(issue_id)`,
			`PRAGMA foreign_keys=ON`,
		}},
		// Migration 44: per-user alt-unit display preferences
		{44, []string{
			`ALTER TABLE users ADD COLUMN show_alt_unit_table INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN show_alt_unit_detail INTEGER NOT NULL DEFAULT 0`,
		}},

		// Migration 43: created_by on issues — tracks who created the issue
		{43, []string{
			`ALTER TABLE issues ADD COLUMN created_by INTEGER REFERENCES users(id) ON DELETE SET NULL`,
			// Backfill from the earliest issue_history entry (the creation snapshot)
			`UPDATE issues SET created_by = (
			SELECT changed_by FROM issue_history
			WHERE issue_id = issues.id
			ORDER BY changed_at ASC, id ASC LIMIT 1
		) WHERE created_by IS NULL`,
		}},

		// Migration 42: View management — sort_order, hidden, user_view_pins
		{42, []string{
			`ALTER TABLE views ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE views ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0`,
			// Backfill sort_order for existing admin-default views (alphabetical by title)
			`UPDATE views SET sort_order = (
			SELECT COUNT(*) FROM views v2
			WHERE v2.is_admin_default = 1 AND v2.title < views.title
		) WHERE is_admin_default = 1`,
			// User view pins table — lazy init (no rows = all defaults shown)
			`CREATE TABLE IF NOT EXISTS user_view_pins (
			user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			view_id  INTEGER NOT NULL REFERENCES views(id) ON DELETE CASCADE,
			pinned   INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (user_id, view_id)
		)`,
		}},

		// Migration 41: Drop porter stemmer from FTS5 — use plain ascii tokenizer.
		// Porter reduces "onboarding" → "onboard", breaking prefix queries like "onboardi*".
		// At <300 issues/project, stemming gain is negligible; plain ascii prefix search is correct.
		{41, []string{
			// Drop and recreate the FTS5 virtual table with ascii-only tokenizer
			`DROP TABLE IF EXISTS search_index`,
			`CREATE VIRTUAL TABLE search_index USING fts5(
			entity_type,
			entity_id UNINDEXED,
			content,
			tokenize='ascii'
		)`,
			// Backfill all entities
			`INSERT INTO search_index(entity_type, entity_id, content)
			SELECT 'project', id,
				COALESCE(name,'') || ' ' || COALESCE(key,'') || ' ' || COALESCE(description,'')
			FROM projects`,
			`INSERT INTO search_index(entity_type, entity_id, content)
			SELECT 'issue', id,
				COALESCE(title,'') || ' ' ||
				COALESCE(description,'') || ' ' ||
				COALESCE(acceptance_criteria,'') || ' ' ||
				COALESCE(notes,'') || ' ' ||
				COALESCE(cost_unit,'') || ' ' ||
				COALESCE(release,'') || ' ' ||
				COALESCE(type,'') || ' ' ||
				COALESCE(jira_id,'') || ' ' ||
				COALESCE(jira_version,'') || ' ' ||
				COALESCE(jira_text,'')
			FROM issues`,
			`INSERT INTO search_index(entity_type, entity_id, content)
			SELECT 'user', id,
				COALESCE(username,'') || ' ' ||
				COALESCE(nickname,'') || ' ' ||
				COALESCE(first_name,'') || ' ' ||
				COALESCE(last_name,'') || ' ' ||
				COALESCE(email,'') || ' ' ||
				COALESCE(role,'')
			FROM users`,
			`INSERT INTO search_index(entity_type, entity_id, content)
			SELECT 'tag', id,
				COALESCE(name,'') || ' ' || COALESCE(description,'')
			FROM tags`,
			`INSERT INTO search_index(entity_type, entity_id, content)
			SELECT 'comment', id, COALESCE(body,'') FROM comments`,
		}},

		// ── Migration 45 — external user role + user_project_access ──
		// Extends users.role CHECK to include 'external'.
		// Creates user_project_access table for per-project visibility.
		// Adds accepted_at/accepted_by columns to issues for customer acceptance.
		// NOTE: Recreated tables include columns from M42-44 (sort_order, hidden, created_by, alt-unit prefs).
		{45, []string{
			`PRAGMA foreign_keys=OFF`,

			// Recreate users with expanded role CHECK + M44 columns
			`DROP TABLE IF EXISTS users_old45`,
			`ALTER TABLE users RENAME TO users_old45`,
			`CREATE TABLE users (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			username              TEXT NOT NULL UNIQUE,
			password              TEXT NOT NULL,
			role                  TEXT NOT NULL DEFAULT 'member'
			                      CHECK(role IN ('admin','member','external')),
			status                TEXT NOT NULL DEFAULT 'active',
			created_at            TEXT NOT NULL DEFAULT (datetime('now')),
			nickname              TEXT NOT NULL DEFAULT '',
			first_name            TEXT NOT NULL DEFAULT '',
			last_name             TEXT NOT NULL DEFAULT '',
			email                 TEXT NOT NULL DEFAULT '',
			avatar_path           TEXT NOT NULL DEFAULT '',
			markdown_default      INTEGER NOT NULL DEFAULT 0,
			monospace_fields      INTEGER NOT NULL DEFAULT 0,
			recent_projects_limit INTEGER NOT NULL DEFAULT 3,
			internal_rate_hourly  REAL,
			show_alt_unit_table   INTEGER NOT NULL DEFAULT 0,
			show_alt_unit_detail  INTEGER NOT NULL DEFAULT 0,
			totp_secret           TEXT NOT NULL DEFAULT '',
			totp_enabled          INTEGER NOT NULL DEFAULT 0
		)`,
			`INSERT INTO users (id,username,password,role,status,created_at,nickname,first_name,last_name,email,avatar_path,markdown_default,monospace_fields,recent_projects_limit,internal_rate_hourly,show_alt_unit_table,show_alt_unit_detail,totp_secret,totp_enabled)
			SELECT id,username,password,role,status,created_at,nickname,first_name,last_name,email,avatar_path,markdown_default,monospace_fields,recent_projects_limit,internal_rate_hourly,show_alt_unit_table,show_alt_unit_detail,totp_secret,totp_enabled FROM users_old45`,
			`DROP TABLE users_old45`,

			// Recreate sessions (FK to users)
			`DROP TABLE IF EXISTS sessions_old45`,
			`ALTER TABLE sessions RENAME TO sessions_old45`,
			`CREATE TABLE sessions (
			id         TEXT PRIMARY KEY,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TEXT NOT NULL
		)`,
			`INSERT OR IGNORE INTO sessions SELECT * FROM sessions_old45`,
			`DROP TABLE sessions_old45`,

			// Recreate api_keys (FK to users)
			`DROP TABLE IF EXISTS api_keys_old45`,
			`ALTER TABLE api_keys RENAME TO api_keys_old45`,
			`CREATE TABLE api_keys (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name         TEXT NOT NULL,
			key_hash     TEXT NOT NULL UNIQUE,
			key_prefix   TEXT NOT NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now')),
			last_used_at TEXT
		)`,
			`INSERT OR IGNORE INTO api_keys SELECT * FROM api_keys_old45`,
			`DROP TABLE api_keys_old45`,

			// Recreate totp_pending (FK to users)
			`DROP TABLE IF EXISTS totp_pending_old45`,
			`ALTER TABLE totp_pending RENAME TO totp_pending_old45`,
			`CREATE TABLE totp_pending (
			token      TEXT PRIMARY KEY,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TEXT NOT NULL
		)`,
			`INSERT OR IGNORE INTO totp_pending SELECT * FROM totp_pending_old45`,
			`DROP TABLE totp_pending_old45`,

			// Recreate user_recent_projects (FK to users)
			`DROP TABLE IF EXISTS user_recent_projects_old45`,
			`ALTER TABLE user_recent_projects RENAME TO user_recent_projects_old45`,
			`CREATE TABLE user_recent_projects (
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			visited_at TEXT    NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (user_id, project_id)
		)`,
			`INSERT OR IGNORE INTO user_recent_projects SELECT * FROM user_recent_projects_old45`,
			`DROP TABLE user_recent_projects_old45`,

			// Recreate projects (FK product_owner -> users)
			`DROP TABLE IF EXISTS projects_old45`,
			`ALTER TABLE projects RENAME TO projects_old45`,
			`CREATE TABLE projects (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			name          TEXT NOT NULL,
			description   TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'active',
			created_at    TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
			key           TEXT NOT NULL DEFAULT '',
			product_owner INTEGER REFERENCES users(id) ON DELETE SET NULL,
			customer_id   TEXT NOT NULL DEFAULT '',
			logo_path     TEXT NOT NULL DEFAULT ''
		)`,
			`INSERT INTO projects SELECT * FROM projects_old45`,
			`DROP TABLE projects_old45`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_key ON projects(key)`,
			`CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects(product_owner)`,
			// Recreate project_tags (FK orphaned by projects rename)
			`DROP TABLE IF EXISTS project_tags_old45`,
			`ALTER TABLE project_tags RENAME TO project_tags_old45`,
			`CREATE TABLE project_tags (
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			tag_id     INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			PRIMARY KEY (project_id, tag_id)
		)`,
			`INSERT OR IGNORE INTO project_tags SELECT * FROM project_tags_old45`,
			`DROP TABLE project_tags_old45`,

			// Recreate views (FK user_id -> users) + M42 columns
			`DROP TABLE IF EXISTS views_old45`,
			`ALTER TABLE views RENAME TO views_old45`,
			`CREATE TABLE views (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title            TEXT    NOT NULL,
			description      TEXT    NOT NULL DEFAULT '',
			columns_json     TEXT    NOT NULL DEFAULT '[]',
			filters_json     TEXT    NOT NULL DEFAULT '{}',
			is_shared        INTEGER NOT NULL DEFAULT 0,
			is_admin_default INTEGER NOT NULL DEFAULT 0,
			sort_order       INTEGER NOT NULL DEFAULT 0,
			hidden           INTEGER NOT NULL DEFAULT 0,
			created_at       TEXT    NOT NULL DEFAULT (datetime('now')),
			updated_at       TEXT    NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO views (id,user_id,title,description,columns_json,filters_json,is_shared,is_admin_default,sort_order,hidden,created_at,updated_at)
			SELECT id,user_id,title,description,columns_json,filters_json,is_shared,is_admin_default,sort_order,hidden,created_at,updated_at FROM views_old45`,
			`DROP TABLE views_old45`,
			`CREATE INDEX IF NOT EXISTS idx_views_user ON views(user_id)`,
			// Recreate user_view_pins (FK to users + views — M42)
			`DROP TABLE IF EXISTS user_view_pins_old45`,
			`ALTER TABLE user_view_pins RENAME TO user_view_pins_old45`,
			`CREATE TABLE user_view_pins (
			user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			view_id  INTEGER NOT NULL REFERENCES views(id) ON DELETE CASCADE,
			pinned   INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (user_id, view_id)
		)`,
			`INSERT OR IGNORE INTO user_view_pins SELECT * FROM user_view_pins_old45`,
			`DROP TABLE user_view_pins_old45`,

			// Recreate issues (FK assignee_id -> users)
			`DROP TABLE IF EXISTS issues_old45`,
			`ALTER TABLE issues RENAME TO issues_old45`,
			`CREATE TABLE issues (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
			issue_number        INTEGER NOT NULL DEFAULT 0,
			type                TEXT NOT NULL DEFAULT 'ticket'
			                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task')),
			parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			title               TEXT NOT NULL,
			description         TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes               TEXT NOT NULL DEFAULT '',
			status              TEXT NOT NULL DEFAULT 'backlog'
			                    CHECK(status IN ('backlog','in-progress','done','cancelled')),
			priority            TEXT NOT NULL DEFAULT 'medium'
			                    CHECK(priority IN ('low','medium','high')),
			cost_unit           TEXT NOT NULL DEFAULT '',
			release             TEXT NOT NULL DEFAULT '',
			billing_type        TEXT NOT NULL DEFAULT '',
			total_budget        REAL,
			rate_hourly         REAL,
			rate_lp             REAL,
			start_date          TEXT NOT NULL DEFAULT '',
			end_date            TEXT NOT NULL DEFAULT '',
			group_state         TEXT NOT NULL DEFAULT '',
			sprint_state        TEXT NOT NULL DEFAULT '',
			jira_id             TEXT NOT NULL DEFAULT '',
			jira_version        TEXT NOT NULL DEFAULT '',
			jira_text           TEXT NOT NULL DEFAULT '',
			estimate_hours      REAL,
			estimate_lp         REAL,
			ar_hours            REAL,
			ar_lp               REAL,
			color               TEXT,
			archived            INTEGER NOT NULL DEFAULT 0,
			assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
			accepted_at         TEXT,
			accepted_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issues (
			id, project_id, issue_number, type, parent_id,
			title, description, acceptance_criteria, notes,
			status, priority, cost_unit, release,
			billing_type, total_budget, rate_hourly, rate_lp,
			start_date, end_date, group_state, sprint_state,
			jira_id, jira_version, jira_text,
			estimate_hours, estimate_lp, ar_hours, ar_lp,
			color, archived, assignee_id, created_by,
			created_at, updated_at
		) SELECT
			id, project_id, issue_number, type, parent_id,
			title, description, acceptance_criteria, notes,
			status, priority, cost_unit, release,
			billing_type, total_budget, rate_hourly, rate_lp,
			start_date, end_date, group_state, sprint_state,
			jira_id, jira_version, jira_text,
			estimate_hours, estimate_lp, ar_hours, ar_lp,
			color, archived, assignee_id, created_by,
			created_at, updated_at
		FROM issues_old45`,
			`DROP TABLE issues_old45`,

			// Recreate child tables (SQLite FK rewrite bug)
			`DROP TABLE IF EXISTS issue_tags_old45`,
			`ALTER TABLE issue_tags RENAME TO issue_tags_old45`,
			`CREATE TABLE issue_tags (
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, tag_id)
		)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old45`,
			`DROP TABLE issue_tags_old45`,

			`DROP TABLE IF EXISTS comments_old45`,
			`ALTER TABLE comments RENAME TO comments_old45`,
			`CREATE TABLE comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			body       TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO comments SELECT * FROM comments_old45`,
			`DROP TABLE comments_old45`,

			`DROP TABLE IF EXISTS issue_history_old45`,
			`ALTER TABLE issue_history RENAME TO issue_history_old45`,
			`CREATE TABLE issue_history (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
			snapshot   TEXT NOT NULL,
			changed_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO issue_history SELECT * FROM issue_history_old45`,
			`DROP TABLE issue_history_old45`,

			`DROP TABLE IF EXISTS issue_relations_old45`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old45`,
			`CREATE TABLE issue_relations (
			source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			type      TEXT NOT NULL
			          CHECK(type IN ('groups','sprint','depends_on','impacts')),
			PRIMARY KEY (source_id, target_id, type)
		)`,
			`INSERT OR IGNORE INTO issue_relations SELECT * FROM issue_relations_old45`,
			`DROP TABLE issue_relations_old45`,

			`DROP TABLE IF EXISTS time_entries_old45`,
			`ALTER TABLE time_entries RENAME TO time_entries_old45`,
			`CREATE TABLE time_entries (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id             INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			started_at           TEXT NOT NULL DEFAULT (datetime('now')),
			stopped_at           TEXT,
			override             REAL,
			comment              TEXT NOT NULL DEFAULT '',
			created_at           TEXT NOT NULL DEFAULT (datetime('now')),
			internal_rate_hourly REAL
		)`,
			`INSERT OR IGNORE INTO time_entries SELECT * FROM time_entries_old45`,
			`DROP TABLE time_entries_old45`,

			`DROP TABLE IF EXISTS attachments_old45`,
			`ALTER TABLE attachments RENAME TO attachments_old45`,
			`CREATE TABLE attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER REFERENCES issues(id) ON DELETE CASCADE,
			object_key   TEXT NOT NULL,
			filename     TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO attachments SELECT * FROM attachments_old45`,
			`DROP TABLE attachments_old45`,

			`PRAGMA foreign_keys=ON`,

			// Recreate all indexes
			`CREATE INDEX IF NOT EXISTS idx_issues_project     ON issues(project_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number
		 ON issues(project_id, issue_number) WHERE project_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent      ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type        ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_status      ON issues(status)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_assignee    ON issues(assignee_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_updated     ON issues(updated_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_costunit    ON issues(cost_unit)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_release     ON issues(release)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_tags_tag     ON issue_tags(tag_id)`,
			`CREATE INDEX IF NOT EXISTS idx_comments_issue     ON comments(issue_id, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_history_issue ON issue_history(issue_id, changed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target ON issue_relations(target_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_issue ON time_entries(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_user  ON time_entries(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_totp_pending_user  ON totp_pending(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_issue  ON attachments(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_urp_user_visited   ON user_recent_projects(user_id, visited_at DESC)`,

			// Recreate FTS triggers (orphaned by table renames)
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai
			AFTER INSERT ON issues BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' ||
					COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' ||
					COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' ||
					COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.type,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' ||
					COALESCE(NEW.jira_version,'') || ' ' ||
					COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_au
			AFTER UPDATE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' ||
					COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' ||
					COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' ||
					COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.type,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' ||
					COALESCE(NEW.jira_version,'') || ' ' ||
					COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_ad
			AFTER DELETE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
			END`,

			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai
			AFTER INSERT ON comments BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('comment', NEW.id, COALESCE(NEW.body,''));
			END`,
			`CREATE TRIGGER trg_comments_ad
			AFTER DELETE ON comments BEGIN
				DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.id;
			END`,

			`DROP TRIGGER IF EXISTS trg_users_ai`,
			`DROP TRIGGER IF EXISTS trg_users_au`,
			`DROP TRIGGER IF EXISTS trg_users_ad`,
			`CREATE TRIGGER trg_users_ai
			AFTER INSERT ON users BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('user', NEW.id,
					COALESCE(NEW.username,'') || ' ' ||
					COALESCE(NEW.nickname,'') || ' ' ||
					COALESCE(NEW.first_name,'') || ' ' ||
					COALESCE(NEW.last_name,'') || ' ' ||
					COALESCE(NEW.email,'') || ' ' ||
					COALESCE(NEW.role,''));
			END`,
			`CREATE TRIGGER trg_users_au
			AFTER UPDATE ON users BEGIN
				DELETE FROM search_index WHERE entity_type='user' AND entity_id=OLD.id;
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('user', NEW.id,
					COALESCE(NEW.username,'') || ' ' ||
					COALESCE(NEW.nickname,'') || ' ' ||
					COALESCE(NEW.first_name,'') || ' ' ||
					COALESCE(NEW.last_name,'') || ' ' ||
					COALESCE(NEW.email,'') || ' ' ||
					COALESCE(NEW.role,''));
			END`,
			`CREATE TRIGGER trg_users_ad
			AFTER DELETE ON users BEGIN
				DELETE FROM search_index WHERE entity_type='user' AND entity_id=OLD.id;
			END`,

			// Recreate project FTS triggers (orphaned by projects table rename)
			`DROP TRIGGER IF EXISTS trg_projects_ai`,
			`DROP TRIGGER IF EXISTS trg_projects_au`,
			`DROP TRIGGER IF EXISTS trg_projects_ad`,
			`CREATE TRIGGER trg_projects_ai
			AFTER INSERT ON projects BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('project', NEW.id,
					COALESCE(NEW.name,'') || ' ' || COALESCE(NEW.key,'') || ' ' ||
					COALESCE(NEW.description,''));
			END`,
			`CREATE TRIGGER trg_projects_au
			AFTER UPDATE ON projects BEGIN
				DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('project', NEW.id,
					COALESCE(NEW.name,'') || ' ' || COALESCE(NEW.key,'') || ' ' ||
					COALESCE(NEW.description,''));
			END`,
			`CREATE TRIGGER trg_projects_ad
			AFTER DELETE ON projects BEGIN
				DELETE FROM search_index WHERE entity_type='project' AND entity_id=OLD.id;
			END`,

			// user_project_access table
			`CREATE TABLE user_project_access (
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			PRIMARY KEY (user_id, project_id)
		)`,
			`CREATE INDEX idx_upa_user ON user_project_access(user_id)`,
		}},

		// ── M46: Add 'new' status to issues ─────────────────────────────────────
		{46, []string{
			`PRAGMA foreign_keys=OFF`,

			// Recreate issues table: add 'new' to CHECK, change DEFAULT to 'new'
			`DROP TABLE IF EXISTS issues_old46`,
			`ALTER TABLE issues RENAME TO issues_old46`,
			`CREATE TABLE issues (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
			issue_number        INTEGER NOT NULL DEFAULT 0,
			type                TEXT NOT NULL DEFAULT 'ticket'
			                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task')),
			parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			title               TEXT NOT NULL,
			description         TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes               TEXT NOT NULL DEFAULT '',
			status              TEXT NOT NULL DEFAULT 'new'
			                    CHECK(status IN ('new','backlog','in-progress','done','cancelled')),
			priority            TEXT NOT NULL DEFAULT 'medium'
			                    CHECK(priority IN ('low','medium','high')),
			cost_unit           TEXT NOT NULL DEFAULT '',
			release             TEXT NOT NULL DEFAULT '',
			billing_type        TEXT NOT NULL DEFAULT '',
			total_budget        REAL,
			rate_hourly         REAL,
			rate_lp             REAL,
			start_date          TEXT NOT NULL DEFAULT '',
			end_date            TEXT NOT NULL DEFAULT '',
			group_state         TEXT NOT NULL DEFAULT '',
			sprint_state        TEXT NOT NULL DEFAULT '',
			jira_id             TEXT NOT NULL DEFAULT '',
			jira_version        TEXT NOT NULL DEFAULT '',
			jira_text           TEXT NOT NULL DEFAULT '',
			estimate_hours      REAL,
			estimate_lp         REAL,
			ar_hours            REAL,
			ar_lp               REAL,
			color               TEXT,
			archived            INTEGER NOT NULL DEFAULT 0,
			assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
			accepted_at         TEXT,
			accepted_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issues (
			id, project_id, issue_number, type, parent_id,
			title, description, acceptance_criteria, notes,
			status, priority, cost_unit, release,
			billing_type, total_budget, rate_hourly, rate_lp,
			start_date, end_date, group_state, sprint_state,
			jira_id, jira_version, jira_text,
			estimate_hours, estimate_lp, ar_hours, ar_lp,
			color, archived, assignee_id, created_by,
			accepted_at, accepted_by,
			created_at, updated_at
		) SELECT
			id, project_id, issue_number, type, parent_id,
			title, description, acceptance_criteria, notes,
			status, priority, cost_unit, release,
			billing_type, total_budget, rate_hourly, rate_lp,
			start_date, end_date, group_state, sprint_state,
			jira_id, jira_version, jira_text,
			estimate_hours, estimate_lp, ar_hours, ar_lp,
			color, archived, assignee_id, created_by,
			accepted_at, accepted_by,
			created_at, updated_at
		FROM issues_old46`,
			`DROP TABLE issues_old46`,

			// Recreate child tables (SQLite FK rewrite bug)
			`DROP TABLE IF EXISTS issue_tags_old46`,
			`ALTER TABLE issue_tags RENAME TO issue_tags_old46`,
			`CREATE TABLE issue_tags (
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, tag_id)
		)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old46`,
			`DROP TABLE issue_tags_old46`,

			`DROP TABLE IF EXISTS comments_old46`,
			`ALTER TABLE comments RENAME TO comments_old46`,
			`CREATE TABLE comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			body       TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO comments SELECT * FROM comments_old46`,
			`DROP TABLE comments_old46`,

			`DROP TABLE IF EXISTS issue_history_old46`,
			`ALTER TABLE issue_history RENAME TO issue_history_old46`,
			`CREATE TABLE issue_history (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
			snapshot   TEXT NOT NULL,
			changed_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO issue_history SELECT * FROM issue_history_old46`,
			`DROP TABLE issue_history_old46`,

			`DROP TABLE IF EXISTS issue_relations_old46`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old46`,
			`CREATE TABLE issue_relations (
			source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			type      TEXT NOT NULL
			          CHECK(type IN ('groups','sprint','depends_on','impacts')),
			PRIMARY KEY (source_id, target_id, type)
		)`,
			`INSERT OR IGNORE INTO issue_relations SELECT * FROM issue_relations_old46`,
			`DROP TABLE issue_relations_old46`,

			`DROP TABLE IF EXISTS time_entries_old46`,
			`ALTER TABLE time_entries RENAME TO time_entries_old46`,
			`CREATE TABLE time_entries (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id             INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			started_at           TEXT NOT NULL DEFAULT (datetime('now')),
			stopped_at           TEXT,
			override             REAL,
			comment              TEXT NOT NULL DEFAULT '',
			created_at           TEXT NOT NULL DEFAULT (datetime('now')),
			internal_rate_hourly REAL
		)`,
			`INSERT OR IGNORE INTO time_entries SELECT * FROM time_entries_old46`,
			`DROP TABLE time_entries_old46`,

			`DROP TABLE IF EXISTS attachments_old46`,
			`ALTER TABLE attachments RENAME TO attachments_old46`,
			`CREATE TABLE attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER REFERENCES issues(id) ON DELETE CASCADE,
			object_key   TEXT NOT NULL,
			filename     TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO attachments SELECT * FROM attachments_old46`,
			`DROP TABLE attachments_old46`,

			`PRAGMA foreign_keys=ON`,

			// Recreate indexes
			`CREATE INDEX IF NOT EXISTS idx_issues_project     ON issues(project_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number
		 ON issues(project_id, issue_number) WHERE project_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent      ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type        ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_status      ON issues(status)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_assignee    ON issues(assignee_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_updated     ON issues(updated_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_costunit    ON issues(cost_unit)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_release     ON issues(release)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_tags_tag     ON issue_tags(tag_id)`,
			`CREATE INDEX IF NOT EXISTS idx_comments_issue     ON comments(issue_id, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_history_issue ON issue_history(issue_id, changed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target ON issue_relations(target_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_issue ON time_entries(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_user  ON time_entries(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_issue  ON attachments(issue_id)`,

			// Recreate FTS triggers (orphaned by table renames)
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai
			AFTER INSERT ON issues BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' ||
					COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' ||
					COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' ||
					COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.type,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' ||
					COALESCE(NEW.jira_version,'') || ' ' ||
					COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_au
			AFTER UPDATE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' ||
					COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' ||
					COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' ||
					COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.type,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' ||
					COALESCE(NEW.jira_version,'') || ' ' ||
					COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_ad
			AFTER DELETE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
			END`,

			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai
			AFTER INSERT ON comments BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('comment', NEW.id, COALESCE(NEW.body,''));
			END`,
			`CREATE TRIGGER trg_comments_ad
			AFTER DELETE ON comments BEGIN
				DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.id;
			END`,
		}},

		// ── M47: Add locale column to users ─────────────────────────────────────
		{47, []string{
			`ALTER TABLE users ADD COLUMN locale TEXT NOT NULL DEFAULT 'en'`,
		}},

		// ── M48: Add time_override to issues ─────────────────────────────────────
		{48, []string{
			`ALTER TABLE issues ADD COLUMN time_override REAL`,
		}},

		// ── M49: Add recent_timers_limit to users ────────────────────────────────
		{49, []string{
			`ALTER TABLE users ADD COLUMN recent_timers_limit INTEGER NOT NULL DEFAULT 5`,
		}},

		// ── M50: Add timezone to users ───────────────────────────────────────────
		{50, []string{
			`ALTER TABLE users ADD COLUMN timezone TEXT NOT NULL DEFAULT 'auto'`,
		}},

		// ── M51: Expand status enum + add invoiced_at/invoice_number ─────────────
		// Adds 'accepted' and 'invoiced' to the status CHECK constraint.
		// Adds invoiced_at TEXT and invoice_number TEXT columns.
		// Must recreate issues + child tables (SQLite FK rewrite bug).
		{51, []string{
			`PRAGMA foreign_keys=OFF`,

			`DROP TABLE IF EXISTS issues_old51`,
			`ALTER TABLE issues RENAME TO issues_old51`,
			`CREATE TABLE issues (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
			issue_number        INTEGER NOT NULL DEFAULT 0,
			type                TEXT NOT NULL DEFAULT 'ticket'
			                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task')),
			parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			title               TEXT NOT NULL,
			description         TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes               TEXT NOT NULL DEFAULT '',
			status              TEXT NOT NULL DEFAULT 'new'
			                    CHECK(status IN ('new','backlog','in-progress','done','accepted','invoiced','cancelled')),
			priority            TEXT NOT NULL DEFAULT 'medium'
			                    CHECK(priority IN ('low','medium','high')),
			cost_unit           TEXT NOT NULL DEFAULT '',
			release             TEXT NOT NULL DEFAULT '',
			billing_type        TEXT NOT NULL DEFAULT '',
			total_budget        REAL,
			rate_hourly         REAL,
			rate_lp             REAL,
			start_date          TEXT NOT NULL DEFAULT '',
			end_date            TEXT NOT NULL DEFAULT '',
			group_state         TEXT NOT NULL DEFAULT '',
			sprint_state        TEXT NOT NULL DEFAULT '',
			jira_id             TEXT NOT NULL DEFAULT '',
			jira_version        TEXT NOT NULL DEFAULT '',
			jira_text           TEXT NOT NULL DEFAULT '',
			estimate_hours      REAL,
			estimate_lp         REAL,
			ar_hours            REAL,
			ar_lp               REAL,
			time_override       REAL,
			color               TEXT,
			archived            INTEGER NOT NULL DEFAULT 0,
			assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
			accepted_at         TEXT,
			accepted_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			invoiced_at         TEXT,
			invoice_number      TEXT NOT NULL DEFAULT '',
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issues (
			id, project_id, issue_number, type, parent_id,
			title, description, acceptance_criteria, notes,
			status, priority, cost_unit, release,
			billing_type, total_budget, rate_hourly, rate_lp,
			start_date, end_date, group_state, sprint_state,
			jira_id, jira_version, jira_text,
			estimate_hours, estimate_lp, ar_hours, ar_lp,
			time_override, color, archived, assignee_id, created_by,
			accepted_at, accepted_by,
			created_at, updated_at
		) SELECT
			id, project_id, issue_number, type, parent_id,
			title, description, acceptance_criteria, notes,
			status, priority, cost_unit, release,
			billing_type, total_budget, rate_hourly, rate_lp,
			start_date, end_date, group_state, sprint_state,
			jira_id, jira_version, jira_text,
			estimate_hours, estimate_lp, ar_hours, ar_lp,
			time_override, color, archived, assignee_id, created_by,
			accepted_at, accepted_by,
			created_at, updated_at
		FROM issues_old51`,
			`DROP TABLE issues_old51`,

			// Recreate child tables (SQLite FK rewrite bug)
			`DROP TABLE IF EXISTS issue_tags_old51`,
			`ALTER TABLE issue_tags RENAME TO issue_tags_old51`,
			`CREATE TABLE issue_tags (
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, tag_id)
		)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old51`,
			`DROP TABLE issue_tags_old51`,

			`DROP TABLE IF EXISTS comments_old51`,
			`ALTER TABLE comments RENAME TO comments_old51`,
			`CREATE TABLE comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			body       TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO comments SELECT * FROM comments_old51`,
			`DROP TABLE comments_old51`,

			`DROP TABLE IF EXISTS issue_history_old51`,
			`ALTER TABLE issue_history RENAME TO issue_history_old51`,
			`CREATE TABLE issue_history (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
			snapshot   TEXT NOT NULL DEFAULT '',
			changed_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issue_history SELECT * FROM issue_history_old51`,
			`DROP TABLE issue_history_old51`,

			// Recreate FTS triggers (point at new issues table)
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai
			AFTER INSERT ON issues BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_au
			AFTER UPDATE ON issues BEGIN
				UPDATE search_index SET content =
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,'')
				WHERE entity_type='issue' AND entity_id=NEW.id;
			END`,
			`CREATE TRIGGER trg_issues_ad
			AFTER DELETE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
			END`,

			// Recreate comment triggers
			`DROP TABLE IF EXISTS issue_relations_old51`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old51`,
			`CREATE TABLE issue_relations (
			source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			type      TEXT NOT NULL
			          CHECK(type IN ('groups','sprint','depends_on','impacts')),
			PRIMARY KEY (source_id, target_id, type)
		)`,
			`INSERT OR IGNORE INTO issue_relations SELECT * FROM issue_relations_old51`,
			`DROP TABLE issue_relations_old51`,

			`DROP TABLE IF EXISTS time_entries_old51`,
			`ALTER TABLE time_entries RENAME TO time_entries_old51`,
			`CREATE TABLE time_entries (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id             INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			started_at           TEXT NOT NULL DEFAULT (datetime('now')),
			stopped_at           TEXT,
			override             REAL,
			comment              TEXT NOT NULL DEFAULT '',
			created_at           TEXT NOT NULL DEFAULT (datetime('now')),
			internal_rate_hourly REAL
		)`,
			`INSERT OR IGNORE INTO time_entries SELECT * FROM time_entries_old51`,
			`DROP TABLE time_entries_old51`,

			`DROP TABLE IF EXISTS attachments_old51`,
			`ALTER TABLE attachments RENAME TO attachments_old51`,
			`CREATE TABLE attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER REFERENCES issues(id) ON DELETE CASCADE,
			object_key   TEXT NOT NULL,
			filename     TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO attachments SELECT * FROM attachments_old51`,
			`DROP TABLE attachments_old51`,

			// Recreate FTS triggers (point at new issues table)
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai
			AFTER INSERT ON issues BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_au
			AFTER UPDATE ON issues BEGIN
				UPDATE search_index SET content =
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,'')
				WHERE entity_type='issue' AND entity_id=NEW.id;
			END`,
			`CREATE TRIGGER trg_issues_ad
			AFTER DELETE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
			END`,

			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai
			AFTER INSERT ON comments BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('comment', NEW.id, COALESCE(NEW.body,''));
			END`,
			`CREATE TRIGGER trg_comments_ad
			AFTER DELETE ON comments BEGIN
				DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.id;
			END`,

			`PRAGMA foreign_keys=ON`,
		}},

		// ── M52: Fix user_recent_projects FK pointing at stale projects_old45 ──────
		// M45 recreated user_recent_projects BEFORE recreating projects, so the FK
		// internally references the renamed (then dropped) projects_old45 table.
		// Recreate the table to fix the FK reference.
		{52, []string{
			`PRAGMA foreign_keys=OFF`,
			`DROP TABLE IF EXISTS user_recent_projects_old52`,
			`ALTER TABLE user_recent_projects RENAME TO user_recent_projects_old52`,
			`CREATE TABLE user_recent_projects (
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			visited_at TEXT    NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (user_id, project_id)
		)`,
			`INSERT OR IGNORE INTO user_recent_projects SELECT * FROM user_recent_projects_old52`,
			`DROP TABLE user_recent_projects_old52`,
			`CREATE INDEX IF NOT EXISTS idx_urp_user_visited ON user_recent_projects(user_id, visited_at DESC)`,
			`PRAGMA foreign_keys=ON`,
		}},

		// ── M53: Add preview_hover_delay to users ──────────────────────────────────
		{53, []string{
			`ALTER TABLE users ADD COLUMN preview_hover_delay INTEGER NOT NULL DEFAULT 1000`,
		}},

		// ── M54: Add last_login_at to users ─────────────────────────────────────────
		{54, []string{
			`ALTER TABLE users ADD COLUMN last_login_at TEXT`,
		}},

		// ── M55: Add 'qa' status to issues CHECK constraint ──────────────────────
		// Recreates issues table to add 'qa' between 'in-progress' and 'done'.
		{55, []string{
			`PRAGMA foreign_keys=OFF`,

			`DROP TABLE IF EXISTS issues_old55`,
			`ALTER TABLE issues RENAME TO issues_old55`,
			`CREATE TABLE issues (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
			issue_number        INTEGER NOT NULL DEFAULT 0,
			type                TEXT NOT NULL DEFAULT 'ticket'
			                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task')),
			parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			title               TEXT NOT NULL,
			description         TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes               TEXT NOT NULL DEFAULT '',
			status              TEXT NOT NULL DEFAULT 'new'
			                    CHECK(status IN ('new','backlog','in-progress','qa','done','accepted','invoiced','cancelled')),
			priority            TEXT NOT NULL DEFAULT 'medium'
			                    CHECK(priority IN ('low','medium','high')),
			cost_unit           TEXT NOT NULL DEFAULT '',
			release             TEXT NOT NULL DEFAULT '',
			billing_type        TEXT NOT NULL DEFAULT '',
			total_budget        REAL,
			rate_hourly         REAL,
			rate_lp             REAL,
			start_date          TEXT NOT NULL DEFAULT '',
			end_date            TEXT NOT NULL DEFAULT '',
			group_state         TEXT NOT NULL DEFAULT '',
			sprint_state        TEXT NOT NULL DEFAULT '',
			jira_id             TEXT NOT NULL DEFAULT '',
			jira_version        TEXT NOT NULL DEFAULT '',
			jira_text           TEXT NOT NULL DEFAULT '',
			estimate_hours      REAL,
			estimate_lp         REAL,
			ar_hours            REAL,
			ar_lp               REAL,
			time_override       REAL,
			color               TEXT,
			archived            INTEGER NOT NULL DEFAULT 0,
			assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
			accepted_at         TEXT,
			accepted_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			invoiced_at         TEXT,
			invoice_number      TEXT NOT NULL DEFAULT '',
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issues SELECT * FROM issues_old55`,
			`DROP TABLE issues_old55`,

			// Recreate child tables with correct FK references
			`DROP TABLE IF EXISTS issue_tags_old55`,
			`ALTER TABLE issue_tags RENAME TO issue_tags_old55`,
			`CREATE TABLE issue_tags (
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, tag_id)
		)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old55`,
			`DROP TABLE issue_tags_old55`,

			`DROP TABLE IF EXISTS comments_old55`,
			`ALTER TABLE comments RENAME TO comments_old55`,
			`CREATE TABLE comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			body       TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO comments SELECT * FROM comments_old55`,
			`DROP TABLE comments_old55`,

			`DROP TABLE IF EXISTS issue_history_old55`,
			`ALTER TABLE issue_history RENAME TO issue_history_old55`,
			`CREATE TABLE issue_history (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
			snapshot   TEXT NOT NULL DEFAULT '',
			changed_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issue_history SELECT * FROM issue_history_old55`,
			`DROP TABLE issue_history_old55`,

			`DROP TABLE IF EXISTS issue_relations_old55`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old55`,
			`CREATE TABLE issue_relations (
			source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			type      TEXT NOT NULL
			          CHECK(type IN ('groups','sprint','depends_on','impacts')),
			PRIMARY KEY (source_id, target_id, type)
		)`,
			`INSERT OR IGNORE INTO issue_relations SELECT * FROM issue_relations_old55`,
			`DROP TABLE issue_relations_old55`,

			`DROP TABLE IF EXISTS time_entries_old55`,
			`ALTER TABLE time_entries RENAME TO time_entries_old55`,
			`CREATE TABLE time_entries (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id             INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			started_at           TEXT NOT NULL DEFAULT (datetime('now')),
			stopped_at           TEXT,
			override             REAL,
			comment              TEXT NOT NULL DEFAULT '',
			created_at           TEXT NOT NULL DEFAULT (datetime('now')),
			internal_rate_hourly REAL
		)`,
			`INSERT OR IGNORE INTO time_entries SELECT * FROM time_entries_old55`,
			`DROP TABLE time_entries_old55`,

			`DROP TABLE IF EXISTS attachments_old55`,
			`ALTER TABLE attachments RENAME TO attachments_old55`,
			`CREATE TABLE attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER REFERENCES issues(id) ON DELETE CASCADE,
			object_key   TEXT NOT NULL,
			filename     TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO attachments SELECT * FROM attachments_old55`,
			`DROP TABLE attachments_old55`,

			// Recreate indexes
			`CREATE INDEX IF NOT EXISTS idx_issues_project  ON issues(project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent   ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_assignee  ON issues(assignee_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_status    ON issues(status)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type      ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_number    ON issues(project_id, issue_number)`,

			// Recreate FTS triggers
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai AFTER INSERT ON issues BEGIN
			INSERT INTO search_index(entity_type, entity_id, content)
			VALUES('issue', NEW.id,
				COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
				COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
				COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
				COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,''));
		END`,
			`CREATE TRIGGER trg_issues_au AFTER UPDATE ON issues BEGIN
			UPDATE search_index SET content =
				COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
				COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
				COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
				COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,'')
			WHERE entity_type='issue' AND entity_id=NEW.id;
		END`,
			`CREATE TRIGGER trg_issues_ad AFTER DELETE ON issues BEGIN
			DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
		END`,

			// Recreate comment FTS triggers
			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai AFTER INSERT ON comments BEGIN
			INSERT INTO search_index(entity_type, entity_id, content) VALUES('comment', NEW.issue_id, NEW.body);
		END`,
			`CREATE TRIGGER trg_comments_ad AFTER DELETE ON comments BEGIN
			DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.issue_id AND content=OLD.body;
		END`,

			`PRAGMA foreign_keys=ON`,
		}},

		// M56 — system tags + rules table + project rate fields
		{56, []string{
			`ALTER TABLE tags ADD COLUMN system INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS system_tag_rules (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			tag_id          INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			condition_type  TEXT NOT NULL DEFAULT 'budget_threshold',
			threshold       REAL NOT NULL DEFAULT 0.8,
			excluded_statuses TEXT NOT NULL DEFAULT 'qa,done,accepted,invoiced,cancelled',
			created_at      TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`ALTER TABLE projects ADD COLUMN rate_hourly REAL`,
			`ALTER TABLE projects ADD COLUMN rate_lp REAL`,
		}},

		// M57 — target_ar field for sprints (stored on issues table since sprints are issues)
		{57, []string{
			`ALTER TABLE issues ADD COLUMN target_ar REAL`,
		}},

		// ── M58: Add 'delivered' status to issues CHECK constraint ───────────────
		// Adds 'delivered' between 'done' and 'accepted' in the status lifecycle.
		// Also updates system_tag_rules default excluded_statuses.
		{58, []string{
			`PRAGMA foreign_keys=OFF`,

			`DROP TABLE IF EXISTS issues_old58`,
			`ALTER TABLE issues RENAME TO issues_old58`,
			`CREATE TABLE issues (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
			issue_number        INTEGER NOT NULL DEFAULT 0,
			type                TEXT NOT NULL DEFAULT 'ticket'
			                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task')),
			parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			title               TEXT NOT NULL,
			description         TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes               TEXT NOT NULL DEFAULT '',
			status              TEXT NOT NULL DEFAULT 'new'
			                    CHECK(status IN ('new','backlog','in-progress','qa','done','delivered','accepted','invoiced','cancelled')),
			priority            TEXT NOT NULL DEFAULT 'medium'
			                    CHECK(priority IN ('low','medium','high')),
			cost_unit           TEXT NOT NULL DEFAULT '',
			release             TEXT NOT NULL DEFAULT '',
			billing_type        TEXT NOT NULL DEFAULT '',
			total_budget        REAL,
			rate_hourly         REAL,
			rate_lp             REAL,
			start_date          TEXT NOT NULL DEFAULT '',
			end_date            TEXT NOT NULL DEFAULT '',
			group_state         TEXT NOT NULL DEFAULT '',
			sprint_state        TEXT NOT NULL DEFAULT '',
			jira_id             TEXT NOT NULL DEFAULT '',
			jira_version        TEXT NOT NULL DEFAULT '',
			jira_text           TEXT NOT NULL DEFAULT '',
			estimate_hours      REAL,
			estimate_lp         REAL,
			ar_hours            REAL,
			ar_lp               REAL,
			time_override       REAL,
			color               TEXT,
			archived            INTEGER NOT NULL DEFAULT 0,
			assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
			accepted_at         TEXT,
			accepted_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			invoiced_at         TEXT,
			invoice_number      TEXT NOT NULL DEFAULT '',
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
			target_ar           REAL
		)`,
			`INSERT INTO issues SELECT * FROM issues_old58`,
			`DROP TABLE issues_old58`,

			// Recreate child tables with correct FK references
			`DROP TABLE IF EXISTS issue_tags_old58`,
			`ALTER TABLE issue_tags RENAME TO issue_tags_old58`,
			`CREATE TABLE issue_tags (
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, tag_id)
		)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old58`,
			`DROP TABLE issue_tags_old58`,

			`DROP TABLE IF EXISTS comments_old58`,
			`ALTER TABLE comments RENAME TO comments_old58`,
			`CREATE TABLE comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			body       TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO comments SELECT * FROM comments_old58`,
			`DROP TABLE comments_old58`,

			`DROP TABLE IF EXISTS issue_history_old58`,
			`ALTER TABLE issue_history RENAME TO issue_history_old58`,
			`CREATE TABLE issue_history (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
			snapshot   TEXT NOT NULL DEFAULT '',
			changed_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT INTO issue_history SELECT * FROM issue_history_old58`,
			`DROP TABLE issue_history_old58`,

			`DROP TABLE IF EXISTS issue_relations_old58`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old58`,
			`CREATE TABLE issue_relations (
			source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			type      TEXT NOT NULL
			          CHECK(type IN ('groups','sprint','depends_on','impacts')),
			PRIMARY KEY (source_id, target_id, type)
		)`,
			`INSERT OR IGNORE INTO issue_relations SELECT * FROM issue_relations_old58`,
			`DROP TABLE issue_relations_old58`,

			`DROP TABLE IF EXISTS time_entries_old58`,
			`ALTER TABLE time_entries RENAME TO time_entries_old58`,
			`CREATE TABLE time_entries (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id             INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			started_at           TEXT NOT NULL DEFAULT (datetime('now')),
			stopped_at           TEXT,
			override             REAL,
			comment              TEXT NOT NULL DEFAULT '',
			created_at           TEXT NOT NULL DEFAULT (datetime('now')),
			internal_rate_hourly REAL
		)`,
			`INSERT OR IGNORE INTO time_entries SELECT * FROM time_entries_old58`,
			`DROP TABLE time_entries_old58`,

			`DROP TABLE IF EXISTS attachments_old58`,
			`ALTER TABLE attachments RENAME TO attachments_old58`,
			`CREATE TABLE attachments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id     INTEGER REFERENCES issues(id) ON DELETE CASCADE,
			object_key   TEXT NOT NULL,
			filename     TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL DEFAULT 0,
			uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO attachments SELECT * FROM attachments_old58`,
			`DROP TABLE attachments_old58`,

			// Recreate indexes
			`CREATE INDEX IF NOT EXISTS idx_issues_project  ON issues(project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent   ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_assignee  ON issues(assignee_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_status    ON issues(status)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type      ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_number    ON issues(project_id, issue_number)`,

			// Recreate FTS triggers
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai AFTER INSERT ON issues BEGIN
			INSERT INTO search_index(entity_type, entity_id, content)
			VALUES('issue', NEW.id,
				COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
				COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
				COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
				COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,''));
		END`,
			`CREATE TRIGGER trg_issues_au AFTER UPDATE ON issues BEGIN
			UPDATE search_index SET content =
				COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
				COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
				COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
				COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,'')
			WHERE entity_type='issue' AND entity_id=NEW.id;
		END`,
			`CREATE TRIGGER trg_issues_ad AFTER DELETE ON issues BEGIN
			DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
		END`,

			// Recreate comment FTS triggers
			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai AFTER INSERT ON comments BEGIN
			INSERT INTO search_index(entity_type, entity_id, content) VALUES('comment', NEW.issue_id, NEW.body);
		END`,
			`CREATE TRIGGER trg_comments_ad AFTER DELETE ON comments BEGIN
			DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.issue_id AND content=OLD.body;
		END`,

			`PRAGMA foreign_keys=ON`,

			// Update system_tag_rules to include 'delivered' in excluded statuses
			`UPDATE system_tag_rules SET excluded_statuses='qa,done,delivered,accepted,invoiced,cancelled' WHERE excluded_statuses='qa,done,accepted,invoiced,cancelled'`,
		}},

		// M59 — add rank column to issue_relations for sprint board ordering
		{59, []string{
			`ALTER TABLE issue_relations ADD COLUMN rank INTEGER NOT NULL DEFAULT 0`,
		}},
		{60, []string{
			`ALTER TABLE time_entries ADD COLUMN mite_id INTEGER`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_mite_id ON time_entries(mite_id)`,
		}},
		// M61: fix mite-imported entries that appear as running timers
		{61, []string{
			`UPDATE time_entries SET stopped_at = started_at WHERE mite_id IS NOT NULL AND stopped_at IS NULL`,
		}},
		// M62: per-user accruals report preferences (admin-only feature)
		{62, []string{
			`ALTER TABLE users ADD COLUMN accruals_stats_enabled INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN accruals_extra_statuses TEXT NOT NULL DEFAULT ''`,
		}},
		// M63: password reset tokens (forgot-password email magic link flow).
		// Tokens are random 32-byte values stored hashed (sha256 — high-entropy input
		// doesn't need bcrypt). used_at=NULL → unused, single-use consume on reset.
		{63, []string{
			`CREATE TABLE IF NOT EXISTS password_reset_tokens (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at    TEXT,
			ip_address TEXT NOT NULL DEFAULT ''
		)`,
			`CREATE INDEX IF NOT EXISTS idx_prt_user ON password_reset_tokens(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_prt_expires ON password_reset_tokens(expires_at)`,
		}},
		// M64: per-project access control (project_members + access_audit).
		// Replaces user_project_access with a richer model that supports three
		// access levels — 'viewer' (read-only), 'editor' (read+write), and
		// 'none' (explicit denial, overrides the default member-has-all-access).
		// Backfills: existing user_project_access rows become 'viewer' grants
		// (they were read-only portal access); all active admin+member users
		// are seeded as 'editor' for every non-deleted project.
		{64, []string{
			`CREATE TABLE IF NOT EXISTS project_members (
			user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			access_level TEXT NOT NULL DEFAULT 'editor'
			             CHECK(access_level IN ('none','viewer','editor')),
			created_at   TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (user_id, project_id)
		)`,
			`CREATE INDEX IF NOT EXISTS idx_project_members_user    ON project_members(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_project_members_project ON project_members(project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_project_members_level   ON project_members(access_level)`,

			`CREATE TABLE IF NOT EXISTS access_audit (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER REFERENCES projects(id) ON DELETE SET NULL,
			user_id    INTEGER REFERENCES users(id)    ON DELETE SET NULL,
			actor_id   INTEGER REFERENCES users(id)    ON DELETE SET NULL,
			action     TEXT NOT NULL,
			old_level  TEXT NOT NULL DEFAULT '',
			new_level  TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_access_audit_project ON access_audit(project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_access_audit_user    ON access_audit(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_access_audit_created ON access_audit(created_at)`,

			// Backfill: existing portal grants become 'viewer' rows.
			`INSERT OR IGNORE INTO project_members(user_id, project_id, access_level)
		 SELECT user_id, project_id, 'viewer' FROM user_project_access`,

			// Seed editor access for every current admin/member on every
			// non-deleted project. External users are NOT auto-seeded — they
			// must be granted per-project access explicitly.
			`INSERT OR IGNORE INTO project_members(user_id, project_id, access_level)
		 SELECT u.id, p.id, 'editor'
		 FROM users u
		 CROSS JOIN projects p
		 WHERE u.role IN ('admin','member')
		   AND u.status = 'active'
		   AND p.status != 'deleted'`,
		}},

		// M65: drop the obsolete user_project_access table. Safety re-insert
		// covers rows added between M64 being applied and this migration
		// running (unlikely in practice — both ship together — but cheap
		// to do before dropping the source table).
		{65, []string{
			`INSERT OR IGNORE INTO project_members(user_id, project_id, access_level)
		 SELECT user_id, project_id, 'viewer' FROM user_project_access`,
			`DROP INDEX IF EXISTS idx_upa_user`,
			`DROP TABLE IF EXISTS user_project_access`,
		}},

		// M66: soft-delete for issues. NULL = live, non-NULL = trashed.
		// deleted_by tracks who moved it to trash; stays as a plain INTEGER
		// (no FK constraint can be added via ALTER TABLE on a populated
		// table in SQLite — a stale user id after a user purge is
		// acceptable, the field is used for display only).
		{66, []string{
			`ALTER TABLE issues ADD COLUMN deleted_at TEXT`,
			`ALTER TABLE issues ADD COLUMN deleted_by INTEGER`,
			`CREATE INDEX IF NOT EXISTS idx_issues_deleted_at ON issues(deleted_at)`,
		}},

		// M67: extend issue_relations.type CHECK constraint with three new
		// directional types — follows_from (spin-off), blocks, related
		// (loose "see also"). Purely additive: existing rows stay valid
		// under the new CHECK. SQLite can't ALTER a CHECK constraint, so
		// the usual rename+recreate+copy dance. See PAI-89.
		{67, []string{
			`ALTER TABLE issue_relations RENAME TO issue_relations_old66`,
			`CREATE TABLE issue_relations (
			source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			type      TEXT NOT NULL
			          CHECK(type IN ('groups','sprint','depends_on','impacts',
			                         'follows_from','blocks','related')),
			rank      INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (source_id, target_id, type)
		)`,
			`INSERT OR IGNORE INTO issue_relations
		 SELECT source_id, target_id, type, rank FROM issue_relations_old66`,
			`DROP TABLE issue_relations_old66`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source
		 ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target
		 ON issue_relations(target_id, type)`,
		}},

		// M68: session-scoped mutation audit (PAI-97). One row per mutation
		// request, tagged with X-PAIMOS-Session-Id. session_id is nullable
		// so requests without the header still get audited (null tag) —
		// catches misbehaving callers that fail to set the header.
		// user_id is also nullable for the same reason.
		{68, []string{
			`CREATE TABLE IF NOT EXISTS session_activity (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id  TEXT,
			user_id     INTEGER,
			method      TEXT NOT NULL,
			path        TEXT NOT NULL,
			status_code INTEGER NOT NULL,
			occurred_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			// (session_id, id) gets us fast keyset pagination by session.
			`CREATE INDEX IF NOT EXISTS idx_session_activity_session
		 ON session_activity(session_id, id)`,
			`CREATE INDEX IF NOT EXISTS idx_session_activity_occurred
		 ON session_activity(occurred_at)`,
		}},

		// M69: customers table (PAI-53). CRM-agnostic by design — provider-side
		// IDs and deep-link URLs live in generic columns (`external_*`) so the
		// schema doesn't bind PAIMOS to any particular CRM. Manual customers
		// are first-class: NULL `external_*` is the no-CRM mode (PAI-28
		// audience #1). FTS5 entry built from name + contact + industry.
		{69, []string{
			`CREATE TABLE IF NOT EXISTS customers (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			name               TEXT NOT NULL,
			external_id        TEXT,
			external_url       TEXT,
			external_provider  TEXT,
			synced_at          TEXT,
			contact_name       TEXT NOT NULL DEFAULT '',
			contact_email      TEXT NOT NULL DEFAULT '',
			address            TEXT NOT NULL DEFAULT '',
			country            TEXT NOT NULL DEFAULT '',
			industry           TEXT NOT NULL DEFAULT '',
			rate_hourly        REAL,
			rate_lp            REAL,
			notes              TEXT NOT NULL DEFAULT '',
			created_at         TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			// Same pair-must-be-set semantic as the API layer: a customer
			// linked to an external CRM has both id and provider; a manual
			// customer has neither. Enforced at the DB so a malformed
			// migration / direct-write can't sneak past.
			`CREATE TRIGGER IF NOT EXISTS trg_customers_external_pair_ai
			BEFORE INSERT ON customers
			WHEN (NEW.external_id IS NULL) <> (NEW.external_provider IS NULL)
			BEGIN
				SELECT RAISE(ABORT, 'external_id and external_provider must be both set or both null');
			END`,
			`CREATE TRIGGER IF NOT EXISTS trg_customers_external_pair_au
			BEFORE UPDATE ON customers
			WHEN (NEW.external_id IS NULL) <> (NEW.external_provider IS NULL)
			BEGIN
				SELECT RAISE(ABORT, 'external_id and external_provider must be both set or both null');
			END`,
			`CREATE INDEX IF NOT EXISTS idx_customers_external
		 ON customers(external_provider, external_id)`,
			// FTS triggers
			`CREATE TRIGGER IF NOT EXISTS trg_customers_ai
			AFTER INSERT ON customers BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('customer', NEW.id,
					NEW.name || ' ' || NEW.contact_name || ' ' ||
					NEW.contact_email || ' ' || NEW.industry || ' ' ||
					NEW.country || ' ' || NEW.notes);
			END`,
			`CREATE TRIGGER IF NOT EXISTS trg_customers_au
			AFTER UPDATE ON customers BEGIN
				DELETE FROM search_index WHERE entity_type='customer' AND entity_id=OLD.id;
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('customer', NEW.id,
					NEW.name || ' ' || NEW.contact_name || ' ' ||
					NEW.contact_email || ' ' || NEW.industry || ' ' ||
					NEW.country || ' ' || NEW.notes);
			END`,
			`CREATE TRIGGER IF NOT EXISTS trg_customers_ad
			AFTER DELETE ON customers BEGIN
				DELETE FROM search_index WHERE entity_type='customer' AND entity_id=OLD.id;
			END`,
		}},

		// M70: projects ↔ customers FK + documents + provider_configs.
		// SQLite can't ALTER an existing column to add a FK on a populated
		// table, and the existing `customer_id` is a freeform TEXT label
		// (legacy-instance era). Rename it to `customer_label` and add a clean
		// `customer_id INTEGER` FK so the rate-cascading + assignment logic
		// (PAI-54) works against the new customers table.
		{70, []string{
			// ── Rename existing customer_id → customer_label, add FK ────
			// SQLite supports RENAME COLUMN since 3.25; this codebase uses
			// modernc.org/sqlite which is well past that.
			`ALTER TABLE projects RENAME COLUMN customer_id TO customer_label`,
			`ALTER TABLE projects ADD COLUMN customer_id INTEGER REFERENCES customers(id)`,
			`CREATE INDEX IF NOT EXISTS idx_projects_customer_id
		 ON projects(customer_id)`,

			// ── documents (PAI-55) ──────────────────────────────────────
			// Metadata-only table for customer- and project-scoped uploads;
			// the file bytes live in MinIO (same bucket as attachments,
			// namespaced under "documents/…"). object_key below is the
			// pointer; handlers/documents.go does all the storage.Put /
			// .Get / .Delete calls.
			//
			// scope is checked so exactly one of customer_id / project_id
			// is set; orphan docs (both NULL) are rejected.
			`CREATE TABLE IF NOT EXISTS documents (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			scope         TEXT NOT NULL CHECK(scope IN ('customer','project')),
			customer_id   INTEGER REFERENCES customers(id) ON DELETE CASCADE,
			project_id    INTEGER REFERENCES projects(id)  ON DELETE CASCADE,
			filename      TEXT NOT NULL,
			mime_type     TEXT NOT NULL,
			size_bytes    INTEGER NOT NULL,
			-- object_key is the path inside the MinIO bucket (same storage
			-- layer as attachments). Documents and attachments share one
			-- bucket; the key namespace separates them ("documents/…" vs
			-- the bare "<issueId>/…" attachments use).
			object_key    TEXT NOT NULL,
			label         TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'active'
			              CHECK(status IN ('draft','active','expired')),
			valid_from    TEXT,
			valid_until   TEXT,
			uploaded_by   INTEGER,
			uploaded_at   TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
			CHECK(
				(scope = 'customer' AND customer_id IS NOT NULL AND project_id IS NULL) OR
				(scope = 'project'  AND project_id  IS NOT NULL AND customer_id IS NULL)
			)
		)`,
			`CREATE INDEX IF NOT EXISTS idx_documents_customer
		 ON documents(customer_id) WHERE customer_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_documents_project
		 ON documents(project_id)  WHERE project_id  IS NOT NULL`,

			// ── provider_configs (PAI-104) ──────────────────────────────
			// Per-provider settings. config_json holds non-secret fields as
			// a plain JSON map; secret fields are encrypted at rest with
			// AES-GCM and stored separately under config_secret_json (so
			// non-secret reads in the API never even touch the ciphertext).
			`CREATE TABLE IF NOT EXISTS provider_configs (
			provider_id           TEXT PRIMARY KEY,
			enabled               INTEGER NOT NULL DEFAULT 0,
			config_json           TEXT NOT NULL DEFAULT '{}',
			config_secret_json    BLOB,
			updated_at            TEXT NOT NULL DEFAULT (datetime('now')),
			updated_by            INTEGER REFERENCES users(id)
		)`,
		}},

		// M71: per-project cooperation metadata (PAI-61). 1:1 with projects.
		// Structured columns for the four dimensions PMs reach for repeatedly
		// (engagement type, code ownership, env responsibility, SLA flags),
		// plus two markdown freeform fields for the long tail. Informational
		// only in v1 — no behavioural effects elsewhere.
		{71, []string{
			`CREATE TABLE IF NOT EXISTS project_cooperation (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id          INTEGER NOT NULL UNIQUE
			                    REFERENCES projects(id) ON DELETE CASCADE,
			engagement_type     TEXT
			                    CHECK(engagement_type IN
			                        ('consultancy','project_delivery','managed_service','retainer')),
			code_ownership      TEXT
			                    CHECK(code_ownership IN
			                        ('client_repo','own_repo','mixed')),
			env_responsibility  TEXT
			                    CHECK(env_responsibility IN
			                        ('dev_staging','dev_staging_prod','full_stack')),
			has_sla             INTEGER NOT NULL DEFAULT 0,
			uptime_sla          TEXT NOT NULL DEFAULT '',
			response_time_sla   TEXT NOT NULL DEFAULT '',
			backup_responsible  INTEGER NOT NULL DEFAULT 0,
			oncall              INTEGER NOT NULL DEFAULT 0,
			sla_details         TEXT NOT NULL DEFAULT '',
			cooperation_notes   TEXT NOT NULL DEFAULT '',
			created_at          TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_project_cooperation_project
		 ON project_cooperation(project_id)`,
		}},

		// M72: per-session CSRF token (PAI-113). Bound to the session so
		// rotation happens automatically on logout/reset. Existing sessions
		// keep an empty token until the next sessionUser() call upgrades them
		// — see auth.Middleware for the lazy-issue path.
		{72, []string{
			`ALTER TABLE sessions ADD COLUMN csrf_token TEXT NOT NULL DEFAULT ''`,
		}},

		// M73: incident_log for first-class operator-recorded security and
		// availability incidents (PAI-116). Intentionally minimal — admins
		// can insert/update/close rows; export endpoints stream the table to
		// JSON or CSV for SIEM ingestion. severity / status are CHECK-bounded
		// so the API layer can rely on them without re-validating.
		{73, []string{
			`CREATE TABLE IF NOT EXISTS incident_log (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			severity        TEXT NOT NULL
			                CHECK(severity IN ('low','medium','high','critical')),
			kind            TEXT NOT NULL DEFAULT 'other',
			title           TEXT NOT NULL,
			summary         TEXT NOT NULL DEFAULT '',
			details         TEXT NOT NULL DEFAULT '',
			reported_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
			status          TEXT NOT NULL DEFAULT 'open'
			                CHECK(status IN ('open','investigating','resolved','closed')),
			detected_at     TEXT NOT NULL DEFAULT (datetime('now')),
			resolved_at     TEXT,
			created_at      TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_incident_log_status ON incident_log(status)`,
			`CREATE INDEX IF NOT EXISTS idx_incident_log_detected_at ON incident_log(detected_at)`,
		}},

		// M74: ai_settings (PAI-149). Singleton row holding the system-wide
		// configuration for the LLM text-optimization feature (PAI-146). One
		// row, id=1, seeded by the handler on first read so the table is safe
		// to query without a "no rows" branch. The api_key column is plaintext
		// in the DB by design — operators who need stronger secrets handling
		// should mount the SQLite volume on encrypted storage. Treating it as
		// "secret" here would imply guarantees we don't actually keep.
		{74, []string{
			`CREATE TABLE IF NOT EXISTS ai_settings (
			id                   INTEGER PRIMARY KEY CHECK(id = 1),
			enabled              INTEGER NOT NULL DEFAULT 0,
			provider             TEXT NOT NULL DEFAULT 'openrouter',
			model                TEXT NOT NULL DEFAULT '',
			api_key              TEXT NOT NULL DEFAULT '',
			optimize_instruction TEXT NOT NULL DEFAULT '',
			updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`INSERT OR IGNORE INTO ai_settings (id) VALUES (1)`,
		}},

		// M75: PAI-29 foundations — project repos, code anchors, and the
		// legacy-instance-hosted project manifest. The manifest is intentionally stored
		// as a validated JSON blob in v1 so the API contract can stabilize
		// before we explode it into many specialised tables.
		{75, []string{
			`CREATE TABLE IF NOT EXISTS project_repos (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			url            TEXT NOT NULL,
			default_branch TEXT NOT NULL DEFAULT 'main',
			label          TEXT NOT NULL DEFAULT '',
			sort_order     INTEGER NOT NULL DEFAULT 0,
			created_at     TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_project_repos_project ON project_repos(project_id, sort_order, id)`,
			`CREATE TABLE IF NOT EXISTS issue_anchors (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			issue_id       INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			repo_id        INTEGER NOT NULL REFERENCES project_repos(id) ON DELETE CASCADE,
			file_path      TEXT NOT NULL,
			line           INTEGER NOT NULL,
			label          TEXT NOT NULL DEFAULT '',
			confidence     TEXT NOT NULL DEFAULT 'declared'
			               CHECK(confidence IN ('declared','derived','suggested')),
			symbol_json    TEXT NOT NULL DEFAULT '',
			schema_version TEXT NOT NULL DEFAULT '',
			repo_revision  TEXT NOT NULL DEFAULT '',
			generated_at   TEXT NOT NULL DEFAULT '',
			hidden         INTEGER NOT NULL DEFAULT 0,
			stale          INTEGER NOT NULL DEFAULT 0,
			created_at     TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_anchors_issue ON issue_anchors(issue_id, repo_id, file_path, line)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_anchors_repo ON issue_anchors(project_id, repo_id, issue_id)`,
			`CREATE TABLE IF NOT EXISTS project_manifests (
			project_id     INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			manifest_json  TEXT NOT NULL DEFAULT '{}',
			updated_at     TEXT NOT NULL DEFAULT (datetime('now')),
			updated_by     INTEGER REFERENCES users(id)
		)`,
		}},

		// M76: PAI-30 foundations — generic entity relations and embeddings.
		// Confidence tiers follow the declared / derived / suggested pattern
		// popularized by code-review-graph. issue_relations remains in place
		// for backward compatibility; the handlers layer can dual-write or
		// bridge incrementally.
		{76, []string{
			`CREATE TABLE IF NOT EXISTS entity_relations (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			source_type   TEXT NOT NULL,
			source_id     INTEGER NOT NULL,
			target_type   TEXT NOT NULL,
			target_id     INTEGER NOT NULL,
			edge_type     TEXT NOT NULL,
			confidence    TEXT NOT NULL CHECK(confidence IN ('declared','derived','suggested')),
			metadata      TEXT NOT NULL DEFAULT '',
			created_at    TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(source_type, source_id, target_type, target_id, edge_type)
		)`,
			`CREATE INDEX IF NOT EXISTS idx_entity_relations_src  ON entity_relations(source_type, source_id)`,
			`CREATE INDEX IF NOT EXISTS idx_entity_relations_tgt  ON entity_relations(target_type, target_id)`,
			`CREATE INDEX IF NOT EXISTS idx_entity_relations_type ON entity_relations(project_id, edge_type)`,
			`CREATE TABLE IF NOT EXISTS entity_embeddings (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			entity_type     TEXT NOT NULL,
			entity_id       INTEGER NOT NULL,
			model           TEXT NOT NULL,
			dim             INTEGER NOT NULL,
			vector          BLOB NOT NULL,
			source_hash     TEXT NOT NULL,
			last_indexed_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(entity_type, entity_id, model)
		)`,
			`CREATE INDEX IF NOT EXISTS idx_entity_embeddings_lookup ON entity_embeddings(project_id, entity_type, entity_id)`,
			`INSERT OR IGNORE INTO entity_relations(project_id, source_type, source_id, target_type, target_id, edge_type, confidence, metadata)
		 SELECT i.project_id, 'issue', ir.source_id, 'issue', ir.target_id, ir.type, 'declared', ''
		 FROM issue_relations ir
		 JOIN issues i ON i.id = ir.source_id
		 WHERE i.project_id IS NOT NULL`,
		}},

		// PAI-161: per-user AI usage tracking and admin-overridable cap.
		// One row per (user, day) — `day` is the YYYY-MM-DD UTC date so
		// rolling-day windows are trivial. Numbers are append-only via
		// ON CONFLICT increment, so a missed mid-call crash leaves the
		// counter slightly low but never wrong by more than one call.
		//
		// users.ai_cap_override_tokens (nullable INT): null means
		// "use the default daily cap" (configurable via env). Setting
		// to 0 explicitly disables AI for that user; a positive integer
		// raises the cap. Mirrors the pattern other per-user opt-in
		// flags follow elsewhere in PAIMOS.
		{77, []string{
			`CREATE TABLE IF NOT EXISTS ai_usage (
			user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			day               TEXT NOT NULL,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			request_count     INTEGER NOT NULL DEFAULT 0,
			updated_at        TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (user_id, day)
		)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_usage_day ON ai_usage(day)`,
			`ALTER TABLE users ADD COLUMN ai_cap_override_tokens INTEGER`,
		}},

		// PAI-175: AI prompt CRUD. Each AI action's prompt template is
		// admin-editable through Settings → AI. Built-in actions are
		// code-defined (label / surface / parent / sub locked) but their
		// prompt text is overridable via a row in this table. Custom
		// actions are also stored here with `is_builtin = 0`.
		//
		// Schema notes:
		//   - `key` is the action key the dispatcher resolves at request
		//     time. Built-in keys mirror the registered actions
		//     (PAI-164–172, PAI-173).
		//   - `prompt_template` is the admin-edited override. Empty
		//     string means "use the code-defined default" — keeps the
		//     reset-to-default path trivial.
		//   - `default_template_hash` is reserved for the change-detection
		//     UI from PAI-176 ("default has shipped a change — review");
		//     populated by handlers when seeding builtins.
		{78, []string{
			`CREATE TABLE IF NOT EXISTS ai_prompts (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			key                   TEXT NOT NULL UNIQUE,
			label                 TEXT NOT NULL,
			surface               TEXT NOT NULL,
			parent_action         TEXT,
			sub_action            TEXT,
			prompt_template       TEXT NOT NULL DEFAULT '',
			enabled               INTEGER NOT NULL DEFAULT 1,
			is_builtin            INTEGER NOT NULL DEFAULT 0,
			default_template_hash TEXT NOT NULL DEFAULT '',
			created_at            TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at            TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_prompts_surface ON ai_prompts(surface)`,
		}},

		// PAI-179: AI action placement.
		//
		// Adds a `placement` column to ai_prompts so each action can be
		// pinned to text-field menus, issue-level menus, or both. The
		// column is admin-overridable through Settings → AI prompts;
		// the registry default applies when the column is empty (which
		// is exactly what we set on backfill so existing rows pick up
		// the defaults the next time the catalogue endpoint runs).
		{79, []string{
			`ALTER TABLE ai_prompts ADD COLUMN placement TEXT NOT NULL DEFAULT ''`,
			// Empty means "use the registry default" — the catalogue
			// endpoint resolves that lazily, so no per-key seed migration
			// is needed. Admins who edit a placement override the default;
			// admins who reset clear back to ''.
		}},
		// PAI-189 / PAI-192: align indexes with real query paths. entity_relations
		// is typically filtered by project + endpoint entity, and ai_prompts
		// prompt resolution is by key + enabled.
		{80, []string{
			`CREATE INDEX IF NOT EXISTS idx_entity_relations_project_src
			ON entity_relations(project_id, source_type, source_id, edge_type)`,
			`CREATE INDEX IF NOT EXISTS idx_entity_relations_project_tgt
			ON entity_relations(project_id, target_type, target_id, edge_type)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_prompts_key_enabled
			ON ai_prompts(key, enabled)`,
		}},

		// M81: project-context lexical retrieval substrate. This extends
		// retrieval beyond raw issues into anchors and manifest-derived
		// context documents (including ADR and NFR entries) without
		// changing the existing global search index.
		{81, []string{
			`CREATE VIRTUAL TABLE IF NOT EXISTS project_context_index USING fts5(
			project_id UNINDEXED,
			entity_type,
			entity_key UNINDEXED,
			title,
			content,
			metadata_json UNINDEXED,
			tokenize='porter ascii'
		)`,
		}},
		// M82 / PAI-208: per-call AI paper trail. Metadata only — never prompt or
		// response bodies. Historical cost is captured at record-time in
		// micro-USD to avoid floating-point drift.
		{82, []string{
			`CREATE TABLE IF NOT EXISTS ai_calls (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id        TEXT NOT NULL,
			user_id           INTEGER REFERENCES users(id) ON DELETE SET NULL,
			action_key        TEXT NOT NULL,
			sub_action        TEXT NOT NULL DEFAULT '',
			surface           TEXT NOT NULL,
			issue_id          INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			project_id        INTEGER REFERENCES projects(id) ON DELETE SET NULL,
			customer_id       INTEGER REFERENCES customers(id) ON DELETE SET NULL,
			cooperation_id    INTEGER REFERENCES project_cooperation(id) ON DELETE SET NULL,
			provider          TEXT NOT NULL,
			model             TEXT NOT NULL,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens      INTEGER NOT NULL DEFAULT 0,
			cost_micro_usd    INTEGER NOT NULL DEFAULT 0,
			outcome           TEXT NOT NULL,
			error_class       TEXT NOT NULL DEFAULT '',
			latency_ms        INTEGER NOT NULL DEFAULT 0,
			created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_user_time
		 ON ai_calls(user_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_time
		 ON ai_calls(created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_action_time
		 ON ai_calls(action_key, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_model_time
		 ON ai_calls(model, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_request
		 ON ai_calls(request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_issue_time
		 ON ai_calls(issue_id, created_at DESC)`,
		}},
		// M83 / PAI-211 foundation: generic mutation log plus a tiny app_settings
		// key/value store so undo stack depth can be tuned without a redeploy.
		{83, []string{
			`CREATE TABLE IF NOT EXISTS app_settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
		)`,
			`INSERT OR IGNORE INTO app_settings(key, value) VALUES('undo_stack_depth', '3')`,
			`CREATE TABLE IF NOT EXISTS mutation_log (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id        TEXT NOT NULL,
			user_id           INTEGER REFERENCES users(id) ON DELETE SET NULL,
			session_id        TEXT,
			mutation_type     TEXT NOT NULL,
			subject_type      TEXT NOT NULL,
			subject_id        INTEGER NOT NULL,
			batch_id          TEXT,
			parent_log_id     INTEGER REFERENCES mutation_log(id) ON DELETE SET NULL,
			inverse_op        TEXT NOT NULL,
			before_state      TEXT NOT NULL,
			before_hash       TEXT NOT NULL,
			after_hash        TEXT NOT NULL,
			undoable          INTEGER NOT NULL DEFAULT 1,
			on_user_stack     INTEGER NOT NULL DEFAULT 1,
			undone_at         TEXT,
			undone_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
			resolution_choice TEXT,
			created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
		)`,
			`CREATE INDEX IF NOT EXISTS idx_mutation_log_user_stack
			ON mutation_log(user_id, created_at DESC)
			WHERE on_user_stack = 1 AND undone_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_mutation_log_subject
			ON mutation_log(subject_type, subject_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_mutation_log_request
			ON mutation_log(request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_mutation_log_batch
			ON mutation_log(batch_id) WHERE batch_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_mutation_log_time
			ON mutation_log(created_at DESC)`,
		}},
		// M84 / PAI-209: store the post-mutation snapshot and explicit redoability.
		{84, []string{
			`ALTER TABLE mutation_log ADD COLUMN after_state TEXT NOT NULL DEFAULT '{}'`,
			`ALTER TABLE mutation_log ADD COLUMN redoable INTEGER NOT NULL DEFAULT 0`,
			`UPDATE mutation_log SET after_state = before_state WHERE after_state = '{}' OR after_state = ''`,
		}},

		// M85: PAI-267 — flag dev-login sessions so /auth/me can surface
		// `via_dev_login: true` to the frontend banner. The column lives on
		// sessions (not users) because the same human can hold both a real
		// and a dev session; the flag belongs to the session that authed
		// the current request.
		{85, []string{
			`ALTER TABLE sessions ADD COLUMN via_dev_login INTEGER NOT NULL DEFAULT 0`,
		}},

		// M86: PAI-261 — encryption-at-rest for ai_settings.api_key. New
		// column api_key_encrypted (BLOB) holds the secretvault-encrypted
		// envelope; the existing plaintext api_key column stays as a
		// transitional read fallback. PutAISettings writes the encrypted
		// column on every save and clears plaintext, so the lazy
		// migration completes the first time an admin re-saves their
		// key after the deploy. Pre-PAI-261 deployments keep working
		// without operator action — the read path falls through to the
		// plaintext column.
		{86, []string{
			`ALTER TABLE ai_settings ADD COLUMN api_key_encrypted BLOB`,
		}},

		// M87: PAI-273 — Customer model expansion. Three things happen
		// in one migration to keep the schema atomic:
		//
		//   A. New `contacts` table (Ansprechpartner entity). One customer
		//      can hold multiple contacts; exactly one is_primary at a
		//      time, enforced at the application layer (the existing
		//      partial-unique-index pattern doesn't cover SQLite cleanly).
		//      external_* columns let HubSpot Contact sync upsert by
		//      (provider, external_id).
		//
		//   B. New customer metadata columns (website / vat / employees /
		//      revenue / phone / billing & visit address quartets). All
		//      nullable / empty-default — no destructive change, no
		//      backfill needed.
		//
		//   C. Data backfill: every customer with non-empty contact_name
		//      OR contact_email gets a contacts row with is_primary=1.
		//      The legacy columns stay populated for one release as a
		//      read-fallback; removal lands in a follow-up after prod
		//      logs show zero fallback hits.
		{87, []string{
			`CREATE TABLE contacts (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				customer_id         INTEGER NOT NULL,
				name                TEXT NOT NULL DEFAULT '',
				email               TEXT NOT NULL DEFAULT '',
				phone               TEXT NOT NULL DEFAULT '',
				role                TEXT NOT NULL DEFAULT '',
				is_primary          INTEGER NOT NULL DEFAULT 0,
				notes               TEXT NOT NULL DEFAULT '',
				external_id         TEXT,
				external_provider   TEXT,
				external_url        TEXT,
				synced_at           TEXT,
				created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now')),
				updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%S','now')),
				FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX idx_contacts_customer ON contacts(customer_id)`,
			`CREATE UNIQUE INDEX idx_contacts_external ON contacts(external_provider, external_id)
				WHERE external_provider IS NOT NULL AND external_id IS NOT NULL`,

			`ALTER TABLE customers ADD COLUMN website                  TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN domain                   TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN vat_id                   TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN employee_count           INTEGER`,
			`ALTER TABLE customers ADD COLUMN annual_revenue_cents     INTEGER`,
			`ALTER TABLE customers ADD COLUMN description              TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN phone                    TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN billing_address_street   TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN billing_address_city     TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN billing_address_zip      TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN billing_address_country  TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN visit_address_street     TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN visit_address_zip        TEXT NOT NULL DEFAULT ''`,

			// Backfill: every customer that has at least one inline
			// contact field gets a primary Contact row mirroring those
			// fields. We deliberately don't NULL the legacy columns —
			// the read-fallback in GetCustomer relies on them.
			`INSERT INTO contacts (customer_id, name, email, is_primary, created_at, updated_at)
			   SELECT c.id, c.contact_name, c.contact_email, 1,
			          COALESCE(c.created_at, strftime('%Y-%m-%d %H:%M:%S','now')),
			          COALESCE(c.updated_at, strftime('%Y-%m-%d %H:%M:%S','now'))
			   FROM customers c
			  WHERE (c.contact_name IS NOT NULL AND c.contact_name <> '')
			     OR (c.contact_email IS NOT NULL AND c.contact_email <> '')`,
		}},

		// M88 / PAI-309: per-user auto-refresh countdown for stale issue
		// lists. Defaults preserve the new behaviour for existing users
		// while allowing users to opt out from Account settings.
		{88, []string{
			`ALTER TABLE users ADD COLUMN issue_auto_refresh_enabled INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE users ADD COLUMN issue_auto_refresh_interval_seconds INTEGER NOT NULL DEFAULT 60`,
		}},

		// M89 / PAI-322: sessions get a created_at column so we can
		// enforce an absolute lifetime cap (90 days) alongside the new
		// 30-day sliding window. SQLite forbids non-constant DEFAULTs
		// on ALTER TABLE, so we add the column with an empty default
		// and UPDATE existing rows in the same migration. Existing
		// sessions effectively reset their absolute cap to migration
		// time, which is acceptable — the user only feels it if they
		// then go 90 days without logging in. Future inserts set the
		// column explicitly in LoginHandler.
		{89, []string{
			`ALTER TABLE sessions ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`,
			`UPDATE sessions SET created_at = datetime('now') WHERE created_at = ''`,
		}},

		// M90 / PAI-320: per-user permissions_epoch counter. Bumped on
		// every change to a user's role, status, or project membership.
		// Middleware emits the current value as `X-Permissions-Epoch`
		// on every authenticated response; the SPA notices a change and
		// re-fetches /auth/me to re-hydrate its access cache. Backend
		// permission checks already read role/status fresh on every
		// request via loadSession, so this column exists purely to
		// invalidate the FRONTEND cache promptly without a hard logout.
		{90, []string{
			`ALTER TABLE users ADD COLUMN permissions_epoch INTEGER NOT NULL DEFAULT 0`,
		}},

		// M91 / PAI-321: per-user must_change_password gate. Set by the
		// admin user-create form (default ON) so a freshly minted
		// account is forced through a password-change screen before it
		// can do anything else. Cleared on successful self-service
		// password change. Existing users default to 0 — the gate
		// applies only to accounts created after this migration.
		{91, []string{
			`ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0`,
		}},

		// M92 / PAI-335: per-user super-admin flag. M105 promotes this
		// into the canonical role_key='super_admin' role; the flag stays
		// as the compatibility marker for older rows and clients.
		{92, []string{
			`ALTER TABLE users ADD COLUMN is_super_admin INTEGER NOT NULL DEFAULT 0`,
			`UPDATE users SET is_super_admin = 1 WHERE username = 'mba'`,
		}},

		// M93 / PAI-324: agent + session attribution on issue_history
		// snapshots. Both columns are nullable TEXT — existing rows
		// stay NULL (no backfill, no synthesis). Write endpoints
		// persist the values from the X-Paimos-Agent-Name and
		// X-Paimos-Session-Id headers when present, otherwise NULL.
		// Length cap is enforced application-side (64 chars each)
		// before the INSERT to avoid surprise blow-ups.
		{93, []string{
			`ALTER TABLE issue_history ADD COLUMN agent_name TEXT`,
			`ALTER TABLE issue_history ADD COLUMN session_id TEXT`,
		}},

		// M94 / PAI-326: declarable agents per project. The "what
		// agents work this project" definition used to live in per-
		// repo local files (e.g. .claude/commands/{ops,dev}.md);
		// moving it to project metadata makes it the single source
		// of truth, queryable, and consistent across instances.
		// Schema is intentionally minimal — PAI-329 will add per-
		// agent `body`, `bootstrap_steps[]`, and
		// `non_negotiable_rules[]` columns when those fields
		// stabilize. `lane_tags` and `metadata` are stored as JSON
		// blobs in TEXT (matching the project_manifests / ai_calls
		// pattern) so the API contract can stabilize before
		// exploding into specialised tables.
		{94, []string{
			`CREATE TABLE IF NOT EXISTS project_agents (
				id                 INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				name               TEXT NOT NULL,
				description        TEXT NOT NULL DEFAULT '',
				slash_command_name TEXT NOT NULL DEFAULT '',
				lane_tags          TEXT NOT NULL DEFAULT '[]',
				metadata           TEXT NOT NULL DEFAULT '{}',
				created_at         TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_agents_project_name ON project_agents(project_id, name)`,
			`CREATE INDEX IF NOT EXISTS idx_project_agents_project ON project_agents(project_id)`,
		}},

		// M95 / PAI-329: extend agent rendering shape + add project-
		// level shared inventories that agent artifacts inherit.
		//
		// Per-agent additive columns on project_agents:
		//   body                  TEXT  — markdown freetext, the bulk of
		//                                  the rendered skill body.
		//   bootstrap_steps       TEXT  — JSON array of {title,
		//                                  command, rationale}; ordered
		//                                  list of "do these once at
		//                                  session start" steps.
		//   non_negotiable_rules  TEXT  — JSON array of {title, body,
		//                                  memory_ref}; the rules that
		//                                  must NEVER be silently broken.
		//                                  memory_ref is just a string
		//                                  here — resolution into an
		//                                  actual memory entry happens
		//                                  at render time (PAI-330).
		//
		// New project-level inventories — separate tables (mirrors
		// project_repos precedent from M75; one row per item, easy
		// CRUD, no JSON-blob editing dance):
		//   project_environments  — {name, url, host_alias, host_ip}
		//                            e.g. staging vs prod.
		//   project_deploy_recipes — {name, command, summary} —
		//                            named deployment shorthand the
		//                            agent body can reference by name.
		//
		// project_repos (existing) is the third leg of project-level
		// inventory and is reused as-is; the canonical agent artifact
		// endpoint inlines all three.
		{95, []string{
			`ALTER TABLE project_agents ADD COLUMN body TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE project_agents ADD COLUMN bootstrap_steps TEXT NOT NULL DEFAULT '[]'`,
			`ALTER TABLE project_agents ADD COLUMN non_negotiable_rules TEXT NOT NULL DEFAULT '[]'`,
			`CREATE TABLE IF NOT EXISTS project_environments (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				name        TEXT NOT NULL,
				url         TEXT NOT NULL DEFAULT '',
				host_alias  TEXT NOT NULL DEFAULT '',
				host_ip     TEXT NOT NULL DEFAULT '',
				sort_order  INTEGER NOT NULL DEFAULT 0,
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_environments_project_name ON project_environments(project_id, name)`,
			`CREATE INDEX IF NOT EXISTS idx_project_environments_project ON project_environments(project_id, sort_order, id)`,
			`CREATE TABLE IF NOT EXISTS project_deploy_recipes (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				name        TEXT NOT NULL,
				command     TEXT NOT NULL DEFAULT '',
				summary     TEXT NOT NULL DEFAULT '',
				sort_order  INTEGER NOT NULL DEFAULT 0,
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_deploy_recipes_project_name ON project_deploy_recipes(project_id, name)`,
			`CREATE INDEX IF NOT EXISTS idx_project_deploy_recipes_project ON project_deploy_recipes(project_id, sort_order, id)`,
		}},

		// M96 / PAI-338 (gated by PAI-346): knowledge plane on issues.
		// Three logical changes recreated as a single issues-rebuild:
		//   1. Extend `type` CHECK to add the five knowledge types:
		//      'memory','runbook','external_system','related_project',
		//      'guideline'. These behave like first-class issues —
		//      reusing history snapshots, comments, tags, FTS, parent-
		//      child, soft-delete and undo for free (PAI-346 §"Why
		//      adopt").
		//   2. Extend `status` CHECK to add 'archived' and 'proposed'.
		//      Knowledge entries live primarily in 'backlog' and
		//      transition to 'archived' on soft-removal; PAI-349 will
		//      use 'proposed' for bot-authored drafts pending review.
		//      Adding both up-front avoids a follow-up recreate.
		//   3. Add nullable `slug TEXT` and `category_metadata TEXT`
		//      columns. Slug is populated only on knowledge types
		//      ([a-z][a-z0-9_-]* pattern, max 64 chars, application-
		//      enforced); UNIQUE INDEX scoped via WHERE slug IS NOT
		//      NULL so non-knowledge issues stay unconstrained.
		//      category_metadata holds per-type tail fields (e.g.
		//      external_system.url) as JSON-as-text.
		// Backwards-compat: existing rows keep their existing `type`
		// and `status`; slug + category_metadata default to NULL.
		// No data backfill — knowledge entries materialize when their
		// CRUD endpoints (PAI-338 handler package) start writing.
		// Pattern follows M51/M55/M58/M82 — same dance:
		// PRAGMA off → rename → recreate → INSERT SELECT → drop old →
		// recreate child tables (SQLite FK rewrite bug) → recreate
		// FTS triggers → PRAGMA on. system_tag_rules left untouched
		// (the new statuses don't change the default exclusion list).
		{96, []string{
			`PRAGMA foreign_keys=OFF`,

			`DROP TABLE IF EXISTS issues_old96`,
			`ALTER TABLE issues RENAME TO issues_old96`,
			`CREATE TABLE issues (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id          INTEGER REFERENCES projects(id) ON DELETE CASCADE,
				issue_number        INTEGER NOT NULL DEFAULT 0,
				type                TEXT NOT NULL DEFAULT 'ticket'
				                    CHECK(type IN ('epic','cost_unit','release','sprint','ticket','task',
				                                   'memory','runbook','external_system','related_project','guideline')),
				parent_id           INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				title               TEXT NOT NULL,
				description         TEXT NOT NULL DEFAULT '',
				acceptance_criteria TEXT NOT NULL DEFAULT '',
				notes               TEXT NOT NULL DEFAULT '',
				status              TEXT NOT NULL DEFAULT 'new'
				                    CHECK(status IN ('new','backlog','in-progress','qa','done','delivered','accepted','invoiced','cancelled','archived','proposed')),
				priority            TEXT NOT NULL DEFAULT 'medium'
				                    CHECK(priority IN ('low','medium','high')),
				cost_unit           TEXT NOT NULL DEFAULT '',
				release             TEXT NOT NULL DEFAULT '',
				billing_type        TEXT NOT NULL DEFAULT '',
				total_budget        REAL,
				rate_hourly         REAL,
				rate_lp             REAL,
				start_date          TEXT NOT NULL DEFAULT '',
				end_date            TEXT NOT NULL DEFAULT '',
				group_state         TEXT NOT NULL DEFAULT '',
				sprint_state        TEXT NOT NULL DEFAULT '',
				jira_id             TEXT NOT NULL DEFAULT '',
				jira_version        TEXT NOT NULL DEFAULT '',
				jira_text           TEXT NOT NULL DEFAULT '',
				estimate_hours      REAL,
				estimate_lp         REAL,
				ar_hours            REAL,
				ar_lp               REAL,
				time_override       REAL,
				color               TEXT,
				archived            INTEGER NOT NULL DEFAULT 0,
				assignee_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_by          INTEGER REFERENCES users(id) ON DELETE SET NULL,
				accepted_at         TEXT,
				accepted_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
				invoiced_at         TEXT,
				invoice_number      TEXT NOT NULL DEFAULT '',
				created_at          TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
				target_ar           REAL,
				deleted_at          TEXT,
				deleted_by          INTEGER,
				slug                TEXT,
				category_metadata   TEXT
			)`,
			// Carry data forward — list every column explicitly so
			// new nullable additions (slug, category_metadata) don't
			// break the SELECT * shape contract. Existing rows pick
			// up NULL for the new columns by virtue of not being in
			// the column list.
			`INSERT INTO issues (
				id, project_id, issue_number, type, parent_id,
				title, description, acceptance_criteria, notes,
				status, priority, cost_unit, release,
				billing_type, total_budget, rate_hourly, rate_lp,
				start_date, end_date, group_state, sprint_state,
				jira_id, jira_version, jira_text,
				estimate_hours, estimate_lp, ar_hours, ar_lp,
				time_override, color, archived, assignee_id, created_by,
				accepted_at, accepted_by, invoiced_at, invoice_number,
				created_at, updated_at, target_ar,
				deleted_at, deleted_by
			) SELECT
				id, project_id, issue_number, type, parent_id,
				title, description, acceptance_criteria, notes,
				status, priority, cost_unit, release,
				billing_type, total_budget, rate_hourly, rate_lp,
				start_date, end_date, group_state, sprint_state,
				jira_id, jira_version, jira_text,
				estimate_hours, estimate_lp, ar_hours, ar_lp,
				time_override, color, archived, assignee_id, created_by,
				accepted_at, accepted_by, invoiced_at, invoice_number,
				created_at, updated_at, target_ar,
				deleted_at, deleted_by
			FROM issues_old96`,
			`DROP TABLE issues_old96`,

			// Recreate child tables (SQLite FK rewrite bug — same
			// dance as M58/M55/M51/M82+). Keep the column shape
			// stable; we're only here for the FK pointer fix.
			`DROP TABLE IF EXISTS issue_tags_old96`,
			`ALTER TABLE issue_tags RENAME TO issue_tags_old96`,
			`CREATE TABLE issue_tags (
				issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
				PRIMARY KEY (issue_id, tag_id)
			)`,
			`INSERT OR IGNORE INTO issue_tags SELECT * FROM issue_tags_old96`,
			`DROP TABLE issue_tags_old96`,

			`DROP TABLE IF EXISTS comments_old96`,
			`ALTER TABLE comments RENAME TO comments_old96`,
			`CREATE TABLE comments (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				author_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
				body       TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT INTO comments SELECT * FROM comments_old96`,
			`DROP TABLE comments_old96`,

			// issue_history carries M93's agent_name + session_id
			// columns — preserve them on the recreate.
			`DROP TABLE IF EXISTS issue_history_old96`,
			`ALTER TABLE issue_history RENAME TO issue_history_old96`,
			`CREATE TABLE issue_history (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				changed_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
				snapshot   TEXT NOT NULL DEFAULT '',
				changed_at TEXT NOT NULL DEFAULT (datetime('now')),
				agent_name TEXT,
				session_id TEXT
			)`,
			`INSERT INTO issue_history (id, issue_id, changed_by, snapshot, changed_at, agent_name, session_id)
				SELECT id, issue_id, changed_by, snapshot, changed_at, agent_name, session_id FROM issue_history_old96`,
			`DROP TABLE issue_history_old96`,

			// issue_relations carries M67's extended type CHECK and
			// M59's rank column — preserve both on recreate.
			`DROP TABLE IF EXISTS issue_relations_old96`,
			`ALTER TABLE issue_relations RENAME TO issue_relations_old96`,
			`CREATE TABLE issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL
				          CHECK(type IN ('groups','sprint','depends_on','impacts',
				                         'follows_from','blocks','related')),
				rank      INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`INSERT OR IGNORE INTO issue_relations SELECT source_id, target_id, type, rank FROM issue_relations_old96`,
			`DROP TABLE issue_relations_old96`,

			`DROP TABLE IF EXISTS time_entries_old96`,
			`ALTER TABLE time_entries RENAME TO time_entries_old96`,
			`CREATE TABLE time_entries (
				id                   INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id             INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				started_at           TEXT NOT NULL DEFAULT (datetime('now')),
				stopped_at           TEXT,
				override             REAL,
				comment              TEXT NOT NULL DEFAULT '',
				created_at           TEXT NOT NULL DEFAULT (datetime('now')),
				internal_rate_hourly REAL,
				mite_id              INTEGER
			)`,
			`INSERT OR IGNORE INTO time_entries SELECT * FROM time_entries_old96`,
			`DROP TABLE time_entries_old96`,

			`DROP TABLE IF EXISTS attachments_old96`,
			`ALTER TABLE attachments RENAME TO attachments_old96`,
			`CREATE TABLE attachments (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id     INTEGER REFERENCES issues(id) ON DELETE CASCADE,
				object_key   TEXT NOT NULL,
				filename     TEXT NOT NULL,
				content_type TEXT NOT NULL,
				size_bytes   INTEGER NOT NULL DEFAULT 0,
				uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at   TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO attachments SELECT * FROM attachments_old96`,
			`DROP TABLE attachments_old96`,

			// issue_anchors (M75) and ai_calls (M82) were created
			// after the last issues recreate (M58), so their FK
			// references inside SQLite still point to the freshly-
			// dropped issues_old96 — same SQLite FK rewrite bug
			// the rest of this migration spends most of its
			// length on. Recreate both with the same column shape
			// and indexes they had at their original migration
			// site, otherwise the next INSERT against either
			// table fails with "no such table: issues_old96".
			`DROP TABLE IF EXISTS issue_anchors_old96`,
			`ALTER TABLE issue_anchors RENAME TO issue_anchors_old96`,
			`CREATE TABLE issue_anchors (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				issue_id       INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				repo_id        INTEGER NOT NULL REFERENCES project_repos(id) ON DELETE CASCADE,
				file_path      TEXT NOT NULL,
				line           INTEGER NOT NULL,
				label          TEXT NOT NULL DEFAULT '',
				confidence     TEXT NOT NULL DEFAULT 'declared'
				               CHECK(confidence IN ('declared','derived','suggested')),
				symbol_json    TEXT NOT NULL DEFAULT '',
				schema_version TEXT NOT NULL DEFAULT '',
				repo_revision  TEXT NOT NULL DEFAULT '',
				generated_at   TEXT NOT NULL DEFAULT '',
				hidden         INTEGER NOT NULL DEFAULT 0,
				stale          INTEGER NOT NULL DEFAULT 0,
				created_at     TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`INSERT OR IGNORE INTO issue_anchors SELECT * FROM issue_anchors_old96`,
			`DROP TABLE issue_anchors_old96`,
			`CREATE INDEX IF NOT EXISTS idx_issue_anchors_issue ON issue_anchors(issue_id, repo_id, file_path, line)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_anchors_repo ON issue_anchors(project_id, repo_id, issue_id)`,

			`DROP TABLE IF EXISTS ai_calls_old96`,
			`ALTER TABLE ai_calls RENAME TO ai_calls_old96`,
			`CREATE TABLE ai_calls (
				id                INTEGER PRIMARY KEY AUTOINCREMENT,
				request_id        TEXT NOT NULL,
				user_id           INTEGER REFERENCES users(id) ON DELETE SET NULL,
				action_key        TEXT NOT NULL,
				sub_action        TEXT NOT NULL DEFAULT '',
				surface           TEXT NOT NULL,
				issue_id          INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				project_id        INTEGER REFERENCES projects(id) ON DELETE SET NULL,
				customer_id       INTEGER REFERENCES customers(id) ON DELETE SET NULL,
				cooperation_id    INTEGER REFERENCES project_cooperation(id) ON DELETE SET NULL,
				provider          TEXT NOT NULL,
				model             TEXT NOT NULL,
				prompt_tokens     INTEGER NOT NULL DEFAULT 0,
				completion_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens      INTEGER NOT NULL DEFAULT 0,
				cost_micro_usd    INTEGER NOT NULL DEFAULT 0,
				outcome           TEXT NOT NULL,
				error_class       TEXT NOT NULL DEFAULT '',
				latency_ms        INTEGER NOT NULL DEFAULT 0,
				created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
			)`,
			`INSERT OR IGNORE INTO ai_calls SELECT * FROM ai_calls_old96`,
			`DROP TABLE ai_calls_old96`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_user_time   ON ai_calls(user_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_time        ON ai_calls(created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_action_time ON ai_calls(action_key, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_model_time  ON ai_calls(model, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_request     ON ai_calls(request_id)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_calls_issue_time  ON ai_calls(issue_id, created_at DESC)`,

			// Recreate the standard issue indexes (covered by M58)
			// plus the soft-delete index from M66.
			`CREATE INDEX IF NOT EXISTS idx_issues_project    ON issues(project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_parent     ON issues(parent_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_assignee   ON issues(assignee_id)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_status     ON issues(status)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_type       ON issues(type)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_number     ON issues(project_id, issue_number)`,
			`CREATE INDEX IF NOT EXISTS idx_issues_deleted_at ON issues(deleted_at)`,
			`CREATE INDEX IF NOT EXISTS idx_time_entries_mite_id ON time_entries(mite_id)`,

			// New: knowledge-plane slug lookup — unique per
			// (type, slug, project_id), but only when slug is
			// non-NULL. SQLite supports partial UNIQUE indexes so
			// non-knowledge issues stay unconstrained on this column.
			// memory_ref resolution (PAI-329 → PAI-330) hits this
			// directly: SELECT * FROM issues WHERE type='memory' AND
			// slug=? AND project_id=?.
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_type_slug_project
				ON issues(type, slug, project_id) WHERE slug IS NOT NULL`,

			// Recreate FTS triggers — same content surface as M58.
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`CREATE TRIGGER trg_issues_ai AFTER INSERT ON issues BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_au AFTER UPDATE ON issues BEGIN
				UPDATE search_index SET content =
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.cost_unit,'') || ' ' || COALESCE(NEW.release,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,'')
				WHERE entity_type='issue' AND entity_id=NEW.id;
			END`,
			`CREATE TRIGGER trg_issues_ad AFTER DELETE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
			END`,

			// Recreate comment FTS triggers (M58 keyed by issue_id;
			// preserve that semantic).
			`DROP TRIGGER IF EXISTS trg_comments_ai`,
			`DROP TRIGGER IF EXISTS trg_comments_ad`,
			`CREATE TRIGGER trg_comments_ai AFTER INSERT ON comments BEGIN
				INSERT INTO search_index(entity_type, entity_id, content) VALUES('comment', NEW.issue_id, NEW.body);
			END`,
			`CREATE TRIGGER trg_comments_ad AFTER DELETE ON comments BEGIN
				DELETE FROM search_index WHERE entity_type='comment' AND entity_id=OLD.issue_id AND content=OLD.body;
			END`,

			`PRAGMA foreign_keys=ON`,
		}},

		// M97 / PAI-342: extend issue_relations.type CHECK with the new
		// 'applies_to_memory' type. Issue → memory links live as a
		// single relation row (source = issue, target = memory entry).
		// The reverse direction (memory → originating tickets) is a
		// query against the same table, no second row needed.
		// SQLite can't ALTER a CHECK constraint, so the usual
		// rename + recreate + copy dance — same pattern as M67.
		{97, []string{
			`ALTER TABLE issue_relations RENAME TO issue_relations_old97`,
			`CREATE TABLE issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL
				          CHECK(type IN ('groups','sprint','depends_on','impacts',
				                         'follows_from','blocks','related',
				                         'applies_to_memory')),
				rank      INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`INSERT OR IGNORE INTO issue_relations
			 SELECT source_id, target_id, type, rank FROM issue_relations_old97`,
			`DROP TABLE issue_relations_old97`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source
			 ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target
			 ON issue_relations(target_id, type)`,
		}},

		// M98 / PAI-331: per-(user, device, project) auto-watch
		// subscriptions for the sync engine. Default OFF — a freshly
		// minted (device, project) tuple does NOT auto-receive SSE
		// updates. The user explicitly opts in via the Settings >
		// Account "auto-watch sync" panel; toggling OFF
		// invalidates the device's active SSE connection server-side.
		//
		// PAI-341 (knowledge-plane sync) reuses this table verbatim:
		// one (user, device, project) row covers ALL kinds for that
		// triple.
		{98, []string{
			`CREATE TABLE IF NOT EXISTS auto_watch_subscriptions (
				user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				device_id   TEXT NOT NULL,
				project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				enabled     INTEGER NOT NULL DEFAULT 0,
				created_at  TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
				PRIMARY KEY (user_id, device_id, project_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_auto_watch_user_project ON auto_watch_subscriptions(user_id, project_id)`,
			`CREATE INDEX IF NOT EXISTS idx_auto_watch_device ON auto_watch_subscriptions(device_id)`,
		}},

		// M99 / PAI-345: cross-scope memory promotion. Adds a nullable
		// `user_id` column on `issues` so knowledge entries (type='memory'
		// for v1; the column itself is type-agnostic) can be owned by a
		// user instead of a project. Existing rows: NULL. Combined with
		// the already-nullable `project_id`, three knowledge scopes fall
		// out by WHERE clause:
		//
		//   project_id NOT NULL, user_id NULL          → project memory
		//   project_id NULL,     user_id NOT NULL      → user memory
		//   project_id NULL,     user_id NULL          → instance memory
		//                                                (admin-only writes)
		//
		// At this historical stage the discriminator was enforced only by the
		// handlers. M162 later adds the database ownership/type firewall while
		// leaving category_metadata.scope freely editable.
		{99, []string{
			`ALTER TABLE issues ADD COLUMN user_id INTEGER REFERENCES users(id) ON DELETE SET NULL`,
			`CREATE INDEX IF NOT EXISTS idx_issues_user_type ON issues(user_id, type) WHERE user_id IS NOT NULL`,
		}},

		// M100 / PAI-347: memory reference-count tracking. Two cheap
		// nullable column additions on the issues table — applied to
		// every row but only meaningful for rows where type='memory'.
		// The counter increments each time a memory is included in a
		// `paimos session start --bundle full` resolve (PAI-340) or
		// surfaces as an auto-suggest candidate (PAI-342); the
		// timestamp is the wall-clock of the most recent reference.
		// Both default to 0 / NULL — existing memory entries pre-date
		// the tracking and are treated as "freshly referenced" by the
		// stale-proposal logic so the day this lands doesn't generate
		// a flood of bogus archive proposals (see /memory/stale handler).
		{100, []string{
			`ALTER TABLE issues ADD COLUMN reference_count INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE issues ADD COLUMN last_referenced_at TEXT`,
		}},

		// M101 / PAI-354: agent + session attribution on mutation_log
		// rows. Mirrors M93's split on issue_history — two nullable
		// TEXT columns, no backfill, existing rows stay NULL. Write
		// endpoints persist X-Paimos-Agent-Name + X-Paimos-Session-Id
		// (the latter already lived on this table as `session_id` from
		// M83, but only via the mutation handler — the new column
		// `agent_name` is the new arrival). Length cap is enforced
		// application-side at 64 chars (handlers.agentAttrCap) before
		// the INSERT; SQLite ALTER TABLE can't add CHECK retroactively.
		// PAI-209 undo/redo continues to work — the new column is
		// purely informational.
		//
		// NOTE: `session_id` already exists on mutation_log from M83 —
		// only `agent_name` is added here.
		{101, []string{
			`ALTER TABLE mutation_log ADD COLUMN agent_name TEXT`,
		}},

		// M102 / PAI-358: drop the legacy `project_manifests` table.
		// PAI-356 moved primary navigation to the footer bar, PAI-357
		// migrated content into the knowledge plane, and this
		// migration deletes the now-unused storage. PAI-29's blob
		// taxonomy (manifest / _guardrails / _glossary / _dev / _ops)
		// is fully superseded by the PAI-338 knowledge plane.
		//
		// Pre-flight: a TEMP TRIGGER fires RAISE(ABORT) if any project
		// still has non-empty `manifest_json` lacking a `_migrated_at`
		// marker. Operators upgrading from v2.9.x with legacy data
		// must run `paimos migrate manifest-to-knowledge --project KEY`
		// against each populated project on v2.9.1 first; the migration
		// then runs cleanly. The trigger uses INSERT-on-marker rather
		// than DDL-time evaluation because SQLite triggers don't fire
		// on DROP — the marker INSERT is what gates the rest of the
		// migration body.
		{102, []string{
			`CREATE TEMPORARY TABLE _pai358_marker(x INTEGER)`,
			`CREATE TEMPORARY TRIGGER _pai358_check
			   BEFORE INSERT ON _pai358_marker
			   WHEN EXISTS (
			     SELECT 1 FROM project_manifests
			     WHERE manifest_json IS NOT NULL
			       AND manifest_json != ''
			       AND manifest_json != '{}'
			       AND json_extract(manifest_json, '$._migrated_at') IS NULL
			   )
			   BEGIN
			     SELECT RAISE(ABORT, 'PAI-358: project_manifests has unmigrated content; on v2.9.1 run paimos migrate manifest-to-knowledge --project KEY for each populated project, then redeploy');
			   END`,
			`INSERT INTO _pai358_marker VALUES (1)`,
			`DROP TRIGGER _pai358_check`,
			`DROP TABLE _pai358_marker`,
			`DROP TABLE project_manifests`,
		}},

		// M103 / PAI-368: per-user search-scope shortcut. Replaces the
		// previously hard-coded Ctrl+^ binding (which was unreachable on
		// some keyboard layouts/OS combos). Stored as a JSON blob with
		// modifier flags + KeyboardEvent.code so matching is layout-stable
		// for the user who recorded it. Empty string = disabled (no
		// shortcut). Default '' rather than the legacy chord because we
		// don't know which keyboard a given user has — the Settings UI
		// guides them to record one.
		{103, []string{
			`ALTER TABLE users ADD COLUMN search_scope_shortcut TEXT NOT NULL DEFAULT ''`,
		}},

		// M104 / PAI-379: api-key scope narrowing. Adds a comma-separated
		// `scopes` column to api_keys. Sentinel `*` means "full owner-role
		// power" (every key created before this migration backfills to `*`
		// so behavior doesn't change). Named scopes like `projects:write`
		// narrow the key — handlers that opt in via `auth.RequireScope`
		// reject api-key callers whose scope set lacks the required entry.
		// Session-cookie auth is unaffected: scopes only attach to keys.
		{104, []string{
			`ALTER TABLE api_keys ADD COLUMN scopes TEXT NOT NULL DEFAULT '*'`,
		}},

		// M105 / PAI-336: promote super-admin from a hidden boolean to a
		// canonical application role, while keeping the legacy columns as
		// compatibility shims.
		//
		// `users.role` still carries the old enum because SQLite cannot
		// widen its CHECK constraint in-place without rebuilding a highly
		// referenced table. New code reads/writes `role_key`; writes also
		// mirror back into `role` (`super_admin` -> `admin`) and
		// `is_super_admin` so older code paths/tests continue to resolve
		// safely during the transition.
		//
		// role_permissions is intentionally small and seeded: PAI-336 does
		// not introduce dynamic custom roles, only a queryable capability
		// registry for privileged actions.
		{105, []string{
			`ALTER TABLE users ADD COLUMN role_key TEXT NOT NULL DEFAULT 'member'
				CHECK(role_key IN ('admin','member','external','super_admin'))`,
			`UPDATE users
			 SET role_key = CASE
			   WHEN is_super_admin = 1 THEN 'super_admin'
			   WHEN role IN ('admin','member','external') THEN role
			   ELSE 'member'
			 END`,
			`CREATE TABLE IF NOT EXISTS role_permissions (
				role       TEXT NOT NULL CHECK(role IN ('admin','member','external','super_admin')),
				capability TEXT NOT NULL,
				PRIMARY KEY(role, capability)
			)`,
			`INSERT OR IGNORE INTO role_permissions(role, capability) VALUES
				('admin',       'security.super_admin_audit.read'),
				('super_admin', 'security.super_admin_audit.read'),
				('super_admin', 'time_entries.write_any_user'),
				('super_admin', 'users.grant_super_admin')`,
			`CREATE TABLE IF NOT EXISTS super_admin_audit (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				actor_user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
				target_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
				capability     TEXT NOT NULL,
				endpoint       TEXT NOT NULL DEFAULT '',
				request_id     TEXT NOT NULL DEFAULT '',
				details_json   TEXT NOT NULL DEFAULT '{}',
				created_at     TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_super_admin_audit_created_at ON super_admin_audit(created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_super_admin_audit_actor ON super_admin_audit(actor_user_id, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_super_admin_audit_target ON super_admin_audit(target_user_id, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_super_admin_audit_capability ON super_admin_audit(capability, created_at)`,
		}},

		// M106 / PAI-389: session-framed super-admin impersonation.
		//
		// sessions.user_id remains the real logged-in account. When a
		// super-admin temporarily acts as another user, actor_user_id
		// preserves the original operator and acting_as_user_id points at
		// the effective user resolved by auth middleware. Audit rows record
		// start, end, and mutating requests while active.
		{106, []string{
			`ALTER TABLE sessions ADD COLUMN actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL`,
			`ALTER TABLE sessions ADD COLUMN acting_as_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_actor_user ON sessions(actor_user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_acting_as_user ON sessions(acting_as_user_id)`,
			`INSERT OR IGNORE INTO role_permissions(role, capability) VALUES
				('super_admin', 'auth.impersonation.start'),
				('super_admin', 'auth.impersonation.end'),
				('super_admin', 'auth.impersonation.action')`,
		}},

		// M107 / PAI-407: Projektbericht snapshots and report-facing
		// project metadata. A snapshot is immutable evidence of the exact
		// issue set shown in a generated report; acceptance later acts on
		// that frozen issue_ids_json, not on a live filter.
		{107, []string{
			`ALTER TABLE project_cooperation ADD COLUMN report_contract_basis TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE project_cooperation ADD COLUMN report_terms_url TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE project_cooperation ADD COLUMN report_objection_period_days INTEGER NOT NULL DEFAULT 30`,
			`ALTER TABLE project_cooperation ADD COLUMN report_customer_responsibilities TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE project_cooperation ADD COLUMN report_contractor_responsibilities TEXT NOT NULL DEFAULT ''`,
			`CREATE TABLE IF NOT EXISTS project_report_permissions (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				person_name    TEXT NOT NULL DEFAULT '',
				company        TEXT NOT NULL DEFAULT '',
				role_label     TEXT NOT NULL DEFAULT '',
				may_approve    INTEGER NOT NULL DEFAULT 0,
				may_deliver    INTEGER NOT NULL DEFAULT 0,
				may_accept     INTEGER NOT NULL DEFAULT 0,
				sort_order     INTEGER NOT NULL DEFAULT 0,
				created_at     TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_project_report_permissions_project
			 ON project_report_permissions(project_id, sort_order, id)`,
			`CREATE TABLE IF NOT EXISTS project_report_snapshots (
				id              INTEGER PRIMARY KEY AUTOINCREMENT,
				project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				code            TEXT NOT NULL UNIQUE,
				report_key      TEXT NOT NULL DEFAULT '',
				report_type     TEXT NOT NULL DEFAULT 'projektbericht',
				lang            TEXT NOT NULL DEFAULT '',
				filter_query     TEXT NOT NULL DEFAULT '',
				issue_ids_json  TEXT NOT NULL DEFAULT '[]',
				total_issues    INTEGER NOT NULL DEFAULT 0,
				pdf_sha256      TEXT NOT NULL DEFAULT '',
				status          TEXT NOT NULL DEFAULT 'generated'
				                CHECK(status IN ('generated','accepted','void')),
				signed_document_id INTEGER REFERENCES documents(id) ON DELETE SET NULL,
				signed_at       TEXT,
				signer_name     TEXT NOT NULL DEFAULT '',
				signer_company  TEXT NOT NULL DEFAULT '',
				signer_role     TEXT NOT NULL DEFAULT '',
				accepted_at     TEXT,
				accepted_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
				accept_summary_json TEXT NOT NULL DEFAULT '{}',
				created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
				created_at      TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_project_report_snapshots_project
			 ON project_report_snapshots(project_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_project_report_snapshots_code
			 ON project_report_snapshots(code)`,
		}},
		// Migration 108: PAI-418/420 — customer-facing report-text
		// field used by Projektbericht export and the portal
		// acceptance page. Single column; the audience style (warm
		// "Apple Notes" copy vs technical executive TL;DR) is picked
		// at AI-generation time, not stored as two parallel fields.
		// NOT NULL DEFAULT '' so existing rows just start blank.
		{108, []string{
			`ALTER TABLE issues ADD COLUMN report_summary TEXT NOT NULL DEFAULT ''`,
		}},

		// Migration 109: PAI-459 — CUSTOMERPORTAL system tag. The single
		// load-bearing marker for what an external customer sees on the
		// portal. System-managed (system=1) so DeleteTag rejects it; UI
		// renders an eye glyph + reserved color by name. The tag-attach
		// API exempts it from the usual system-tag block so internal
		// users can toggle visibility through the standard endpoints
		// (see tags.go isPortalVisibilityTag). Idempotent insert by name.
		{109, []string{
			`INSERT OR IGNORE INTO tags(name, color, description, system)
			 VALUES('CUSTOMERPORTAL', 'blue',
			        'Marks an issue as visible in the customer portal. ' ||
			        'Managed manually via the issue-detail toggle, the ' ||
			        'IssueList bulk action, or auto-attached on portal ' ||
			        'request submission.',
			        1)`,
		}},

		// Migration 110: PAI-462 — one-time backfill. Before this epic,
		// existing terminal-status issues (delivered / done / accepted /
		// invoiced) were visible to portal users. Auto-tag them so the
		// PAI-460 filter doesn't make them silently vanish on rollout.
		//
		// The backfill is staged through a temp table so the same row set
		// drives both the issue_tags inserts and the per-issue audit rows.
		// Re-running the migration is a no-op: the NOT EXISTS gate skips
		// already-tagged issues, the temp table is empty on the second
		// pass, and no duplicate audit rows are written.
		{110, []string{
			`CREATE TEMPORARY TABLE _backfill_m110 AS
			 SELECT i.id AS issue_id, t.id AS tag_id
			 FROM issues i
			 JOIN projects p ON p.id = i.project_id AND p.status = 'active'
			 JOIN tags t ON t.name = 'CUSTOMERPORTAL'
			 WHERE i.deleted_at IS NULL
			   AND i.status IN ('delivered','done','accepted','invoiced')
			   AND NOT EXISTS (
			     SELECT 1 FROM issue_tags it
			     WHERE it.issue_id = i.id AND it.tag_id = t.id
			   )`,

			`INSERT INTO issue_tags(issue_id, tag_id)
			 SELECT issue_id, tag_id FROM _backfill_m110`,

			// Audit each backfilled attach with mutation_type
			// 'issue.tag.migration_backfill' so the PAI-467 admin
			// visibility report renders these distinctly from interactive
			// toggles or portal auto-tags. on_user_stack=0 keeps them off
			// individual users' undo stacks; undoable=0 forbids undo.
			`INSERT INTO mutation_log
			   (request_id, mutation_type, subject_type, subject_id,
			    batch_id, inverse_op, before_state, after_state,
			    before_hash, after_hash, undoable, on_user_stack)
			 SELECT
			   'migration:m110', 'issue.tag.migration_backfill',
			   'issue_tag', issue_id,
			   'm110-customerportal-backfill', '{}',
			   json_object('issue_id', issue_id, 'tag_id', tag_id, 'exists', 0),
			   json_object('issue_id', issue_id, 'tag_id', tag_id, 'exists', 1),
			   '', '', 0, 0
			 FROM _backfill_m110`,

			`DROP TABLE _backfill_m110`,
		}},

		// Migration 111: PAI-475 — comment visibility flag. Every comment
		// is internal (team-only) or external (also visible on the
		// Customer Portal). NEW comments default to internal; existing
		// rows backfill to internal via the DEFAULT clause. This is the
		// safe-by-default choice: the team must explicitly opt-in when
		// they want a comment to land in front of the customer.
		//
		// CHECK is single-column so SQLite accepts it on ADD COLUMN.
		// The portal sidebar (PAI-474) filters on visibility='external';
		// the internal app shows everything with a visibility badge.
		{111, []string{
			`ALTER TABLE comments ADD COLUMN visibility TEXT NOT NULL DEFAULT 'internal'
			 CHECK (visibility IN ('internal','external'))`,
			`CREATE INDEX IF NOT EXISTS idx_comments_visibility ON comments(visibility)`,
		}},

		// Migration 112: PAI-486 — idempotency cache for duplicate-prone
		// create writes. Scoped by user + method + route/path + key so
		// different users cannot collide on a client-generated key.
		{112, []string{
			`CREATE TABLE IF NOT EXISTS idempotency_keys (
				key          TEXT NOT NULL,
				user_id      INTEGER NOT NULL DEFAULT 0,
				route        TEXT NOT NULL,
				method       TEXT NOT NULL,
				request_hash TEXT NOT NULL,
				status_code  INTEGER NOT NULL,
				response     BLOB NOT NULL,
				headers_json TEXT NOT NULL DEFAULT '{}',
				created_at   TEXT NOT NULL,
				expires_at   TEXT NOT NULL,
				PRIMARY KEY (key, user_id, route, method)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_idempotency_expires ON idempotency_keys(expires_at)`,
		}},

		// Migration 113: PAI-554 — project-scoped issue-number
		// allocation moved from racy MAX(issue_number)+1 reads to an
		// atomic per-project counter. The unique index is the database
		// backstop; deployments with pre-existing duplicates must repair
		// them before this migration can apply.
		{113, []string{
			`CREATE TABLE IF NOT EXISTS project_issue_counters (
					project_id   INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
					next_number INTEGER NOT NULL CHECK(next_number >= 1)
				)`,
			`INSERT INTO project_issue_counters(project_id, next_number)
			 SELECT p.id, COALESCE(MAX(i.issue_number), 0) + 1
			 FROM projects p
			 LEFT JOIN issues i ON i.project_id = p.id
			 GROUP BY p.id
			 ON CONFLICT(project_id) DO UPDATE SET
			   next_number = max(project_issue_counters.next_number, excluded.next_number)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number_unique
				 ON issues(project_id, issue_number)
				 WHERE project_id IS NOT NULL`,
		}},

		// Migration 114: PAI-558 — explicit legal identifiers for
		// customer records. tax_id is the report-facing UID/VAT value;
		// company_register_number stores the Firmenbuchnummer / FN.
		{114, []string{
			`ALTER TABLE customers ADD COLUMN tax_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE customers ADD COLUMN company_register_number TEXT NOT NULL DEFAULT ''`,
		}},

		// Migration 115: PAI-577 — issue-list freshness marker.
		// The issue-list conditional-GET ETag (handlers.computeIssueListETag)
		// was keyed only on issues.updated_at + COUNT(*), so it was blind to
		// data the list renders from *other* tables — most notably booked /
		// time_total from time_entries. Booking time never bumped updated_at,
		// so the ETag never changed and clients kept a stale BOOKED column via
		// 304 Not Modified (survived hard reload). content_rev is a per-issue
		// counter bumped by triggers on every table the list derives fields
		// from; the ETag now folds in SUM(content_rev) over the matched set.
		// Triggers are the enforcement layer: any write path (API, mite
		// import, CLI, manual SQL) bumps the marker, so this can't be
		// forgotten. A dedicated column (not updated_at) keeps "recently
		// changed" sort/labels untouched.
		{115, []string{
			`ALTER TABLE issues ADD COLUMN content_rev INTEGER NOT NULL DEFAULT 0`,

			// time_entries → booked_hours / time_logged / time_total
			// (au bumps both OLD and NEW issue in case an entry is moved).
			`CREATE TRIGGER IF NOT EXISTS trg_time_entries_listrev_ai
				AFTER INSERT ON time_entries BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id = NEW.issue_id;
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_time_entries_listrev_au
				AFTER UPDATE ON time_entries BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.issue_id, NEW.issue_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_time_entries_listrev_ad
				AFTER DELETE ON time_entries BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id = OLD.issue_id;
				END`,

			// issue_tags → the TAGS column (tag assignment).
			`CREATE TRIGGER IF NOT EXISTS trg_issue_tags_listrev_ai
				AFTER INSERT ON issue_tags BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id = NEW.issue_id;
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_tags_listrev_au
				AFTER UPDATE ON issue_tags BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.issue_id, NEW.issue_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_tags_listrev_ad
				AFTER DELETE ON issue_tags BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id = OLD.issue_id;
				END`,

			// issue_relations → sprint_ids (and any relation-derived field).
			// Both endpoints are issue ids; bump both regardless of type so
			// no direction/type edge can leave a list stale.
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_ai
				AFTER INSERT ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (NEW.source_id, NEW.target_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_au
				AFTER UPDATE ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.source_id, OLD.target_id, NEW.source_id, NEW.target_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_ad
				AFTER DELETE ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.source_id, OLD.target_id);
				END`,

			// tags → renaming/recoloring a tag changes every chip that shows
			// it. (Insert: not yet on any issue. Delete: cascades to
			// issue_tags, firing trg_issue_tags_listrev_ad.)
			`CREATE TRIGGER IF NOT EXISTS trg_tags_listrev_au
				AFTER UPDATE ON tags BEGIN
					UPDATE issues SET content_rev = content_rev + 1
						WHERE id IN (SELECT issue_id FROM issue_tags WHERE tag_id = NEW.id);
				END`,
		}},

		// PAI-581: per-entry material booking (e.g. AI token cost expressed in
		// Leistungspunkte). Nullable; NULL = no material logged on that entry.
		// Aggregated per window alongside booked hours so Time & Material
		// reports (PAI-580) can show real per-window AR SP. No work_date column
		// is added — the de-facto work date is date(started_at), user-settable
		// via PAI-478 retroactive bookings.
		{116, []string{
			`ALTER TABLE time_entries ADD COLUMN material_lp REAL`,
		}},

		// PAI-222: additive retrieval metadata. The original PAI-30
		// contract already had model, dim, source_hash, vector, and
		// last_indexed_at; these columns make provider/degraded-mode
		// behavior explicit without invalidating existing rows.
		{117, []string{
			`ALTER TABLE entity_embeddings ADD COLUMN provider TEXT NOT NULL DEFAULT 'builtin'`,
			`ALTER TABLE entity_embeddings ADD COLUMN status TEXT NOT NULL DEFAULT 'ready'`,
			`ALTER TABLE entity_embeddings ADD COLUMN error TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_entity_embeddings_model_status ON entity_embeddings(project_id, model, status)`,
		}},

		// M118 / PAI-584 P1: introduce the 'parent' relation edge — the
		// future single source of truth for the issue hierarchy
		// (epic⊃ticket, ticket⊃task), replacing issues.parent_id. This
		// phase is additive + reversible: existing rows are backfilled here,
		// and from now on a pair of DB triggers mirrors every parent_id
		// write into the edge. Reads stay on parent_id until P2/P3.
		// Convention (per models.IssueRelation): source = parent,
		// target = child — at most one parent per child.
		//
		// Why a trigger and not application-level dual-write: the whole
		// point of an SSOT is that NO write path can diverge from it. A
		// trigger can't be bypassed by handlers, batch ops, import,
		// undo/redo replay, devseed, or raw SQL — whereas hand-wired
		// dual-write must be remembered at every call site (and silently
		// rots when a new one appears). This matches the codebase's
		// existing derived-state-via-trigger pattern (content_rev).
		//
		// 'parent' is added to the DB CHECK but intentionally NOT to
		// contracts.RelationTypes — the public relation/MCP API keeps
		// rejecting type=parent until P4.
		//
		// SQLite can't ALTER a CHECK, so the usual rename+recreate+copy
		// dance — same pattern as M67/M97.
		{118, []string{
			`ALTER TABLE issue_relations RENAME TO issue_relations_old117`,
			`CREATE TABLE issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL
				          CHECK(type IN ('parent','groups','sprint','depends_on','impacts',
				                         'follows_from','blocks','related',
				                         'applies_to_memory')),
				rank      INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`INSERT OR IGNORE INTO issue_relations
			 SELECT source_id, target_id, type, rank FROM issue_relations_old117`,
			`DROP TABLE issue_relations_old117`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source
			 ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target
			 ON issue_relations(target_id, type)`,
			// Backfill (a): every issues.parent_id → parent edge (covers
			// epic→ticket AND ticket→task). parent_id is an FK with
			// ON DELETE SET NULL, so it can never dangle; the EXISTS is
			// belt-and-suspenders. The <> guard drops any self-reference so
			// the new table's PK/FK never rejects a degenerate row.
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT i.parent_id, i.id, 'parent'
			 FROM issues i
			 WHERE i.parent_id IS NOT NULL
			   AND i.parent_id <> i.id
			   AND EXISTS (SELECT 1 FROM issues p WHERE p.id = i.parent_id)`,
			// Backfill (b): EPIC-sourced groups relations → parent edge, but
			// ONLY for children that (a) didn't already give a parent.
			//
			// `groups` is polymorphic container membership (epic→ticket,
			// cost_unit→ticket, release→ticket). Only the EPIC→ticket links
			// are part of the WBS tree the `parent` edge owns — cost_unit /
			// release groupings are orthogonal axes (relationized later in
			// P7–P9), so they must NOT be folded into `parent`. Hence the
			// JOIN to src.type='epic'.
			//
			// This catches epic→ticket links created via the relation API
			// alone (no parent_id) — the orphans invisible to reads, the
			// original PAI-584 bug — while guaranteeing one-parent-per-child:
			// parent_id always wins (NOT EXISTS), and multiple divergent epic
			// sources collapse to one (MIN+GROUP BY). Without this a
			// parent_id/groups disagreement would seed two parent edges,
			// fanning out reports and blocking P5's unique index.
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT MIN(g.source_id), g.target_id, 'parent'
			 FROM issue_relations g
			 JOIN issues src ON src.id = g.source_id AND src.type = 'epic'
			 WHERE g.type='groups'
			   AND NOT EXISTS (
			       SELECT 1 FROM issue_relations existing
			       WHERE existing.target_id = g.target_id
			         AND existing.type = 'parent')
			 GROUP BY g.target_id`,
			// Recreate the content_rev list-freshness triggers (added in
			// M115). RENAME-ing then DROP-ing the old table took its triggers
			// with it; without these, relation changes (sprint membership,
			// the new parent edges) would stop bumping issues.content_rev and
			// SSE/list caches would go stale. Identical bodies to M115. Done
			// after the backfill so the one-time edge seeding doesn't churn
			// content_rev for every issue.
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_ai
				AFTER INSERT ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (NEW.source_id, NEW.target_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_au
				AFTER UPDATE ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.source_id, OLD.target_id, NEW.source_id, NEW.target_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_ad
				AFTER DELETE ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.source_id, OLD.target_id);
				END`,
			// Parent-edge mirror triggers — the dual-write bridge. They keep
			// the `parent` edge (source=parent, target=child) in lockstep
			// with issues.parent_id for every write, unbypassably.
			//   ai: a new issue with a parent_id gets its edge.
			//   au: a parent_id change rewrites the edge (clear → no edge).
			// The WHEN guards skip no-op updates and self-references; the FK
			// ON DELETE CASCADE on issue_relations cleans edges when a row is
			// hard-deleted, so no delete trigger is needed. recursive_triggers
			// is OFF (default), so these edge writes do NOT cascade to the
			// content_rev listrev triggers above — matching pre-P1 behavior
			// where a bare parent_id change didn't bump content_rev.
			`CREATE TRIGGER IF NOT EXISTS trg_parent_edge_ai
				AFTER INSERT ON issues
				WHEN NEW.parent_id IS NOT NULL AND NEW.parent_id <> NEW.id
				BEGIN
					INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
						VALUES (NEW.parent_id, NEW.id, 'parent');
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_parent_edge_au
				AFTER UPDATE OF parent_id ON issues
				WHEN OLD.parent_id IS NOT NEW.parent_id
				BEGIN
					DELETE FROM issue_relations WHERE target_id = NEW.id AND type='parent';
					INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
						SELECT NEW.parent_id, NEW.id, 'parent'
						WHERE NEW.parent_id IS NOT NULL AND NEW.parent_id <> NEW.id;
				END`,
		}},

		// M119 / PAI-584 P5: enforce the one-parent-per-child invariant at the
		// DB level — the structural guarantee the dropped parent_id FK used to
		// give for free. A partial UNIQUE index on the `parent` edge's target
		// makes a second parent for any child impossible (the relation API
		// already returns 409; this is the unbypassable backstop and the
		// constraint P6 relies on once the column is gone).
		//
		// Defensive dedup first: M118's backfill already guarantees ≤1 parent
		// per child (parent_id wins; MIN-collapse), and the triggers + API
		// maintain it — but if any stray duplicate slipped in, keep the lowest
		// source_id per child so the unique index can always be created.
		{119, []string{
			`DELETE FROM issue_relations
			 WHERE type='parent'
			   AND (target_id, source_id) NOT IN (
			       SELECT target_id, MIN(source_id) FROM issue_relations
			       WHERE type='parent' GROUP BY target_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_relations_one_parent
			 ON issue_relations(target_id) WHERE type='parent'`,
		}},

		// M120 / PAI-584 P6 (DESTRUCTIVE): drop issues.parent_id — the `parent`
		// edge is now the sole source of truth. All reads (P2/P3), the API
		// payload (P3), every write path (P6 edge-direct), undo/redo, and
		// invariants (P5) are off the column; this removes the vestigial copy.
		//
		// Order matters: SQLite ALTER TABLE DROP COLUMN refuses a column that
		// is indexed or referenced by a trigger, so drop those first. The
		// parent-sync mirror triggers (M118) read NEW.parent_id and are no
		// longer needed — writes go straight to the edge. The self-FK on
		// parent_id goes away with the column.
		//
		// Then retire the now-redundant epic-sourced `groups` rows: M118
		// backfilled them into `parent` edges and the P4 auto-translate stopped
		// creating new ones, so they are pure duplication. cost_unit/release
		// `groups` are left intact (P7–P9).
		{120, []string{
			`DROP TRIGGER IF EXISTS trg_parent_edge_ai`,
			`DROP TRIGGER IF EXISTS trg_parent_edge_au`,
			`DROP INDEX IF EXISTS idx_issues_parent`,
			`ALTER TABLE issues DROP COLUMN parent_id`,
			`DELETE FROM issue_relations
			 WHERE type='groups'
			   AND source_id IN (SELECT id FROM issues WHERE type='epic')`,
		}},

		// M121 / PAI-599 (599-A): add `cost_unit` and `release` relation edge
		// types. cost_unit/release are already first-class container issues
		// (they carry rates and aggregate); the issues.cost_unit/release string
		// columns are a fragile by-title reference to them. These typed edges
		// (source = container issue, target = ticket) become the SSOT, mirroring
		// the `parent` edge. SQLite can't ALTER a CHECK — rename+recreate+copy
		// (pattern M118), recreating every index AND trigger the table carries
		// (source/target indexes, the M119 one-parent partial-unique index, and
		// the M115 content_rev list-freshness triggers).
		//
		// Backfill here is SQL-only and safe: fold existing groups rows whose
		// source is a cost_unit/release issue into the new edge types, and edge
		// tickets to any container that ALREADY matches their label by title.
		// Creating containers for label-only strings (and edging those) is the
		// idempotent Go boot-backfill EnsureCostUnitReleaseEdges — it needs
		// per-project issue numbering, which is unsafe in raw migration SQL.
		{121, []string{
			`ALTER TABLE issue_relations RENAME TO issue_relations_old120`,
			`CREATE TABLE issue_relations (
				source_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				target_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				type      TEXT NOT NULL
				          CHECK(type IN ('parent','cost_unit','release','groups','sprint',
				                         'depends_on','impacts','follows_from','blocks',
				                         'related','applies_to_memory')),
				rank      INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (source_id, target_id, type)
			)`,
			`INSERT OR IGNORE INTO issue_relations
			 SELECT source_id, target_id, type, rank FROM issue_relations_old120`,
			`DROP TABLE issue_relations_old120`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_source
			 ON issue_relations(source_id, type)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_relations_target
			 ON issue_relations(target_id, type)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_relations_one_parent
			 ON issue_relations(target_id) WHERE type='parent'`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_ai
				AFTER INSERT ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (NEW.source_id, NEW.target_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_au
				AFTER UPDATE ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.source_id, OLD.target_id, NEW.source_id, NEW.target_id);
				END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issue_relations_listrev_ad
				AFTER DELETE ON issue_relations BEGIN
					UPDATE issues SET content_rev = content_rev + 1 WHERE id IN (OLD.source_id, OLD.target_id);
				END`,
			// Fold existing groups rows whose source is a cost_unit/release issue.
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT g.source_id, g.target_id, src.type
			 FROM issue_relations g
			 JOIN issues src ON src.id = g.source_id AND src.type IN ('cost_unit','release')
			 WHERE g.type='groups'`,
			// Edge tickets to a container that already matches their label.
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT c.id, i.id, 'cost_unit'
			 FROM issues i
			 JOIN issues c ON c.project_id = i.project_id AND c.type='cost_unit'
			              AND c.title = i.cost_unit AND c.deleted_at IS NULL
			 WHERE i.cost_unit != '' AND i.deleted_at IS NULL`,
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT c.id, i.id, 'release'
			 FROM issues i
			 JOIN issues c ON c.project_id = i.project_id AND c.type='release'
			              AND c.title = i.release AND c.deleted_at IS NULL
			 WHERE i.release != '' AND i.deleted_at IS NULL`,
		}},

		// M122 / PAI-599 (599-B): finish the backfill so EVERY cost_unit/release
		// string has a container + edge BEFORE the column drop (M123). M121
		// edged labels that already had a container; this creates a container
		// for each remaining ("orphan") label and edges its tickets. Done in
		// SQL (not the former Go boot backfill) because migrations run before
		// app boot — a boot backfill would read the already-dropped column.
		//
		// Container issue numbers are assigned MAX(issue_number)+row_number per
		// project (unique, gap-free from the current max); project_issue_counters
		// is then advanced so the API's NextIssueNumber never collides. Finally
		// the one-per-ticket invariant gets partial-unique indexes (mirrors the
		// M119 parent index).
		{122, []string{
			// Orphan containers — cost_unit. Source includes soft-deleted AND
			// project-less issues (project_id IS — NULL-safe) so NO label is
			// lost on the M123 drop or on a later restore. `IS` matches the
			// project-less (orphan-sprint) partition that `=` would skip.
			`INSERT INTO issues(project_id, issue_number, type, title, status, priority)
			 SELECT l.project_id,
			        (SELECT COALESCE(MAX(issue_number),0) FROM issues WHERE issues.project_id IS l.project_id)
			          + ROW_NUMBER() OVER (PARTITION BY l.project_id ORDER BY l.label),
			        'cost_unit', l.label, 'backlog', 'medium'
			 FROM (SELECT DISTINCT project_id, cost_unit AS label FROM issues
			       WHERE cost_unit != '' AND type NOT IN ('cost_unit','release')) l
			 WHERE NOT EXISTS (SELECT 1 FROM issues c
			                   WHERE c.project_id IS l.project_id AND c.type='cost_unit'
			                     AND c.title=l.label AND c.deleted_at IS NULL)`,
			// Orphan containers — release.
			`INSERT INTO issues(project_id, issue_number, type, title, status, priority)
			 SELECT l.project_id,
			        (SELECT COALESCE(MAX(issue_number),0) FROM issues WHERE issues.project_id IS l.project_id)
			          + ROW_NUMBER() OVER (PARTITION BY l.project_id ORDER BY l.label),
			        'release', l.label, 'backlog', 'medium'
			 FROM (SELECT DISTINCT project_id, release AS label FROM issues
			       WHERE release != '' AND type NOT IN ('cost_unit','release')) l
			 WHERE NOT EXISTS (SELECT 1 FROM issues c
			                   WHERE c.project_id IS l.project_id AND c.type='release'
			                     AND c.title=l.label AND c.deleted_at IS NULL)`,
			// Advance per-project counters past the issue numbers just consumed
			// (only real projects have counter rows; project-less containers
			// bypass NextIssueNumber so `=` is correct here).
			`UPDATE project_issue_counters
			 SET next_number = (SELECT MAX(issue_number)+1 FROM issues WHERE issues.project_id = project_issue_counters.project_id)
			 WHERE next_number <= (SELECT COALESCE(MAX(issue_number),0) FROM issues WHERE issues.project_id = project_issue_counters.project_id)`,
			// Edge every labelled issue (incl. soft-deleted + project-less) to
			// its active container. Soft-deleted issues are edged too so a later
			// restore keeps its cost_unit/release (the column is gone after M123).
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT c.id, i.id, 'cost_unit'
			 FROM issues i
			 JOIN issues c ON c.project_id IS i.project_id AND c.type='cost_unit'
			              AND c.title = i.cost_unit AND c.deleted_at IS NULL
			 WHERE i.cost_unit != '' AND i.type NOT IN ('cost_unit','release')`,
			`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type)
			 SELECT c.id, i.id, 'release'
			 FROM issues i
			 JOIN issues c ON c.project_id IS i.project_id AND c.type='release'
			              AND c.title = i.release AND c.deleted_at IS NULL
			 WHERE i.release != '' AND i.type NOT IN ('cost_unit','release')`,
			// Clean up any cost_unit/release edge whose TARGET is itself a
			// container — M121's title-match lacked the type filter and could
			// have produced self/inter-container edges. Targets are tickets only.
			`DELETE FROM issue_relations
			 WHERE type IN ('cost_unit','release')
			   AND target_id IN (SELECT id FROM issues WHERE type IN ('cost_unit','release'))`,
			// Deterministic dedup: keep the edge to the LOWEST-id container per
			// ticket (matches resolveOrCreateLabelContainer's ORDER BY id), so
			// the one-per-ticket invariant holds before the unique indexes and
			// the survivor is stable, not insertion-order dependent.
			`DELETE FROM issue_relations WHERE type='cost_unit' AND rowid NOT IN (
			   SELECT e.rowid FROM issue_relations e WHERE e.type='cost_unit'
			     AND e.source_id = (SELECT MIN(e2.source_id) FROM issue_relations e2
			                        WHERE e2.type='cost_unit' AND e2.target_id=e.target_id))`,
			`DELETE FROM issue_relations WHERE type='release' AND rowid NOT IN (
			   SELECT e.rowid FROM issue_relations e WHERE e.type='release'
			     AND e.source_id = (SELECT MIN(e2.source_id) FROM issue_relations e2
			                        WHERE e2.type='release' AND e2.target_id=e.target_id))`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_relations_one_cost_unit
			 ON issue_relations(target_id) WHERE type='cost_unit'`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_relations_one_release
			 ON issue_relations(target_id) WHERE type='release'`,
		}},

		// M123 / PAI-599 (599-B, DESTRUCTIVE): drop the issues.cost_unit and
		// issues.release columns — the typed edges are the sole SSOT now. All
		// reads (payload {id,label}, filters, sort, label lists, aggregation,
		// reports, CSV, search, rate cascade) and writes (setLabelEdge) are off
		// the columns; M122 guaranteed every label has a container + edge.
		// Drop the column indexes first (DROP COLUMN refuses an indexed column),
		// then retire the now-redundant cost_unit/release `groups` rows (M121
		// folded them into the typed edges; the app no longer writes them).
		{123, []string{
			`DROP INDEX IF EXISTS idx_issues_costunit`,
			`DROP INDEX IF EXISTS idx_issues_release`,
			// The issue FTS triggers reference NEW.cost_unit/NEW.release, which
			// blocks DROP COLUMN — drop them, drop the columns, then recreate
			// the triggers without those fields. (Search by cost_unit/release
			// label still works: the labels are container issue titles, indexed
			// in their own right, and the structured cost_unit/release filter
			// reads the edge.)
			`DROP TRIGGER IF EXISTS trg_issues_ai`,
			`DROP TRIGGER IF EXISTS trg_issues_au`,
			`DROP TRIGGER IF EXISTS trg_issues_ad`,
			`ALTER TABLE issues DROP COLUMN cost_unit`,
			`ALTER TABLE issues DROP COLUMN release`,
			`CREATE TRIGGER trg_issues_ai AFTER INSERT ON issues BEGIN
				INSERT INTO search_index(entity_type, entity_id, content)
				VALUES('issue', NEW.id,
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,''));
			END`,
			`CREATE TRIGGER trg_issues_au AFTER UPDATE ON issues BEGIN
				UPDATE search_index SET content =
					COALESCE(NEW.title,'') || ' ' || COALESCE(NEW.description,'') || ' ' ||
					COALESCE(NEW.acceptance_criteria,'') || ' ' || COALESCE(NEW.notes,'') || ' ' ||
					COALESCE(NEW.jira_id,'') || ' ' || COALESCE(NEW.jira_version,'') || ' ' || COALESCE(NEW.jira_text,'')
				WHERE entity_type='issue' AND entity_id=NEW.id;
			END`,
			`CREATE TRIGGER trg_issues_ad AFTER DELETE ON issues BEGIN
				DELETE FROM search_index WHERE entity_type='issue' AND entity_id=OLD.id;
			END`,
			`DELETE FROM issue_relations
			 WHERE type='groups'
			   AND source_id IN (SELECT id FROM issues WHERE type IN ('cost_unit','release'))`,
		}},

		// M124 / PAI-351 (slice 2): two nullable timestamp columns powering the
		// computed-on-read "needs re-review" signal for memory dependencies.
		// content_revised_at = the parent memory's meaningful-change clock
		// (stamped only when a memory's BODY changes, never on metadata/title/
		// status edits); deps_reviewed_at = the dependent's acknowledge clock
		// (set only by the .../reviewed endpoint). A dependent is flagged iff a
		// depends_on parent's content_revised_at is later than the dependent's
		// COALESCE(deps_reviewed_at, created_at) — derived on read, never
		// stored, so it can't drift (mirrors PAI-347 stale-memory). Nullable,
		// no backfill: existing rows stay NULL so nothing cold-starts a flood.
		{124, []string{
			`ALTER TABLE issues ADD COLUMN content_revised_at TEXT`,
			`ALTER TABLE issues ADD COLUMN deps_reviewed_at TEXT`,
		}},

		// M125 / PAI-606 (epic PAI-605): agent_runs — the run-lifecycle record
		// for the "Implement this" feature. A run is created `queued` when the
		// UI button fires; the developer's local runner then transitions it
		// (running → tests_passed/tests_failed → deployed | failed | cancelled)
		// and posts the structured report (version, tests_summary JSON,
		// deploy_target, log_attachment_id). agent_name/session_id tie the run
		// to the attribution trail once a runner picks it up.
		{125, []string{
			`CREATE TABLE IF NOT EXISTS agent_runs (
				id                INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id          INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				project_id        INTEGER REFERENCES projects(id) ON DELETE SET NULL,
				device_id         TEXT NOT NULL DEFAULT '',
				requested_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
				agent_name        TEXT NOT NULL DEFAULT '',
				session_id        TEXT NOT NULL DEFAULT '',
				status            TEXT NOT NULL DEFAULT 'queued'
					CHECK(status IN ('queued','running','tests_passed','tests_failed','deployed','failed','cancelled')),
				version           TEXT NOT NULL DEFAULT '',
				tests_summary     TEXT,
				deploy_target     TEXT NOT NULL DEFAULT '',
				log_attachment_id INTEGER,
				error             TEXT NOT NULL DEFAULT '',
				created_at        TEXT NOT NULL DEFAULT (datetime('now')),
				started_at        TEXT,
				finished_at       TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_issue ON agent_runs(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_status ON agent_runs(status)`,
		}},

		// M126 / PAI-607: runner capability for the "Implement this" feature.
		// A subscriber advertises can_implement=1 when it connects as an
		// implement-capable runner (CLI `?implement=1`); browser tabs leave it
		// 0. The runner registry intersects this with the broker's live
		// subscribers to surface a project's online runners. updated_at (set on
		// every SSE handshake) doubles as the last-seen clock.
		{126, []string{
			`ALTER TABLE auto_watch_subscriptions ADD COLUMN can_implement INTEGER NOT NULL DEFAULT 0`,
		}},
		// M127 / PAI-605 (audit follow-up): enforce "at most one ACTIVE run per
		// issue" in the DB. ImplementIssue's idempotency was a non-atomic
		// SELECT-then-INSERT; two concurrent "Implement this" clicks could both
		// pass the check and create duplicate queued runs that the runner then
		// executes twice. A partial unique index makes the guarantee atomic.
		// First collapse any pre-existing duplicates (keep the newest) so the
		// index can be created on existing data.
		{127, []string{
			`UPDATE agent_runs SET status='cancelled', finished_at=COALESCE(finished_at, datetime('now'))
				WHERE status IN ('queued','running')
				  AND id NOT IN (
					SELECT MAX(id) FROM agent_runs WHERE status IN ('queued','running') GROUP BY issue_id
				  )`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_active_issue
				ON agent_runs(issue_id) WHERE status IN ('queued','running')`,
		}},
		// M128 / PAI-624: stamp the user who claims a queued Implement-this run.
		// Requester/admin can still manage the run, but after queued->running the
		// reporter path is owned by this claimer rather than every project editor.
		{128, []string{
			`ALTER TABLE agent_runs ADD COLUMN claimed_by INTEGER REFERENCES users(id) ON DELETE SET NULL`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_claimed_by ON agent_runs(claimed_by)`,
		}},
		// M129 / PAI-629: make the requested Implement-this provider/action
		// explicit and auditable. Defaults preserve existing rows and omitted
		// action requests as the current Claude Code local-CLI implementation.
		{129, []string{
			`ALTER TABLE agent_runs ADD COLUMN action_key TEXT NOT NULL DEFAULT 'claude_cli.implement'`,
			`ALTER TABLE agent_runs ADD COLUMN provider_kind TEXT NOT NULL DEFAULT 'local_cli'`,
			`ALTER TABLE agent_runs ADD COLUMN provider_id TEXT NOT NULL DEFAULT 'claude_cli'`,
			`ALTER TABLE agent_runs ADD COLUMN provider_label TEXT NOT NULL DEFAULT 'Claude Code'`,
			`ALTER TABLE agent_runs ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_runs ADD COLUMN run_mode TEXT NOT NULL DEFAULT 'edit'`,
			`ALTER TABLE auto_watch_subscriptions ADD COLUMN actions_json TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_action_key ON agent_runs(action_key)`,
		}},
		// M130 / PAI-649: resolved AI action execution options. Metadata only:
		// profile/model selection, effort, prompt preset reference, and context
		// pack name. Prompt bodies, model response bodies, and secrets are never
		// stored here.
		{130, []string{
			`ALTER TABLE ai_calls ADD COLUMN profile_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_calls ADD COLUMN effort TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_calls ADD COLUMN prompt_preset_ref TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_calls ADD COLUMN context_pack TEXT NOT NULL DEFAULT ''`,
		}},
		// M131 / PAI-657 + PAI-658: draft Implement-this providers.
		// A hosted/local draft provider produces reviewable markdown without
		// shell access, repository mutation, local tests, or deploy authority.
		// The new `drafted` terminal state avoids pretending that a draft has
		// passed tests. Run-option columns mirror ai_calls and store metadata
		// only: no prompt bodies, model output, keys, or local environment.
		{131, []string{
			`ALTER TABLE ai_settings ADD COLUMN base_url TEXT NOT NULL DEFAULT ''`,
			`PRAGMA foreign_keys=OFF`,
			`CREATE TABLE agent_runs_m131 (
				id                   INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id             INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				project_id           INTEGER REFERENCES projects(id) ON DELETE SET NULL,
				device_id            TEXT NOT NULL DEFAULT '',
				requested_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
				agent_name           TEXT NOT NULL DEFAULT '',
				session_id           TEXT NOT NULL DEFAULT '',
				status               TEXT NOT NULL DEFAULT 'queued'
					CHECK(status IN ('queued','running','tests_passed','tests_failed','deployed','failed','cancelled','drafted')),
				version              TEXT NOT NULL DEFAULT '',
				tests_summary        TEXT,
				deploy_target        TEXT NOT NULL DEFAULT '',
				log_attachment_id    INTEGER,
				error                TEXT NOT NULL DEFAULT '',
				created_at           TEXT NOT NULL DEFAULT (datetime('now')),
				started_at           TEXT,
				finished_at          TEXT,
				claimed_by           INTEGER REFERENCES users(id) ON DELETE SET NULL,
				action_key           TEXT NOT NULL DEFAULT 'claude_cli.implement',
				provider_kind        TEXT NOT NULL DEFAULT 'local_cli',
				provider_id          TEXT NOT NULL DEFAULT 'claude_cli',
				provider_label       TEXT NOT NULL DEFAULT 'Claude Code',
				model                TEXT NOT NULL DEFAULT '',
				run_mode             TEXT NOT NULL DEFAULT 'edit',
				profile_id           TEXT NOT NULL DEFAULT '',
				effort               TEXT NOT NULL DEFAULT '',
				prompt_preset_ref    TEXT NOT NULL DEFAULT '',
				context_pack         TEXT NOT NULL DEFAULT '',
				context_truncated    INTEGER NOT NULL DEFAULT 0,
				context_sources_json TEXT NOT NULL DEFAULT '',
				prompt_tokens        INTEGER NOT NULL DEFAULT 0,
				completion_tokens    INTEGER NOT NULL DEFAULT 0,
				finish_reason        TEXT NOT NULL DEFAULT ''
			)`,
			`INSERT INTO agent_runs_m131(
				id, issue_id, project_id, device_id, requested_by, agent_name, session_id,
				status, version, tests_summary, deploy_target, log_attachment_id, error,
				created_at, started_at, finished_at, claimed_by, action_key, provider_kind,
				provider_id, provider_label, model, run_mode
			 )
			 SELECT id, issue_id, project_id, device_id, requested_by, agent_name, session_id,
				status, version, tests_summary, deploy_target, log_attachment_id, error,
				created_at, started_at, finished_at, claimed_by, action_key, provider_kind,
				provider_id, provider_label, model, run_mode
			   FROM agent_runs`,
			`DROP TABLE agent_runs`,
			`ALTER TABLE agent_runs_m131 RENAME TO agent_runs`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_issue ON agent_runs(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_status ON agent_runs(status)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_active_issue
				ON agent_runs(issue_id) WHERE status IN ('queued','running')`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_claimed_by ON agent_runs(claimed_by)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_action_key ON agent_runs(action_key)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_run_mode ON agent_runs(run_mode)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_provider_id ON agent_runs(provider_id)`,
			`PRAGMA foreign_keys=ON`,
		}},
		// M132 / PAI-665 + PAI-666: draft handoff links and project AI
		// defaults/policy. Defaults/policy are JSON metadata only: profile,
		// effort, prompt ref, context pack, provider class, and booleans.
		// They must never carry provider credentials or prompt/context bodies.
		{132, []string{
			`ALTER TABLE agent_runs ADD COLUMN source_draft_run_id INTEGER REFERENCES agent_runs(id) ON DELETE SET NULL`,
			`ALTER TABLE agent_runs ADD COLUMN followup_run_id INTEGER REFERENCES agent_runs(id) ON DELETE SET NULL`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_source_draft ON agent_runs(source_draft_run_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_followup ON agent_runs(followup_run_id)`,
			`ALTER TABLE projects ADD COLUMN ai_defaults_json TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE projects ADD COLUMN ai_policy_json TEXT NOT NULL DEFAULT ''`,
		}},
		// M133 / PAI-690: issue-key aliases for cross-project moves. When an
		// issue is re-homed to another project it takes the target project's
		// prefix + next number, so its former key ("PAI-690") would otherwise
		// 404. An alias row (former project_key, former issue_number) -> issue_id
		// lets ResolveIssueRef fall back to the pre-move key. Former numbers are
		// never reused (project_issue_counters is monotonic), so an alias can
		// never shadow a future live issue in the source project; the live-key
		// lookup is always tried first, so an alias only ever catches a key that
		// no longer resolves directly.
		{133, []string{
			`CREATE TABLE IF NOT EXISTS issue_key_aliases (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				project_key  TEXT    NOT NULL,
				issue_number INTEGER NOT NULL,
				issue_id     INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
				UNIQUE(project_key, issue_number)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_issue_key_aliases_issue ON issue_key_aliases(issue_id)`,
		}},
		// M134 / PAI-704: voice-intake workbench sessions. A session is private
		// to its creator and project-less until detection/pin. intake_events is
		// an append-only per-session log with a gapless seq: it is at once the
		// time-travel history, the SSE replay source, and the scrub timeline.
		// Artifact events (spec/summaries/ticket_preview/...) store full
		// snapshots so scrubbing is O(1); restore appends, never rewrites.
		// Bodies live only here — they must never reach mutation_log, audit
		// lines, or ai_calls (INV-INTAKE-02).
		{134, []string{
			`CREATE TABLE IF NOT EXISTS intake_sessions (
				id                        INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id                   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				status                    TEXT NOT NULL DEFAULT 'active'
				                            CHECK(status IN ('active','completed','abandoned')),
				language                  TEXT NOT NULL DEFAULT 'en' CHECK(language IN ('en','de')),
				detected_project_id       INTEGER REFERENCES projects(id) ON DELETE SET NULL,
				detected_score            INTEGER NOT NULL DEFAULT 0,
				pinned_project_id         INTEGER REFERENCES projects(id) ON DELETE SET NULL,
				created_issue_id          INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				transcript                TEXT NOT NULL DEFAULT '',
				transcript_bytes          INTEGER NOT NULL DEFAULT 0,
				rev                       INTEGER NOT NULL DEFAULT 0,
				session_prompt_tokens     INTEGER NOT NULL DEFAULT 0,
				session_completion_tokens INTEGER NOT NULL DEFAULT 0,
				created_at                TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at                TEXT NOT NULL DEFAULT (datetime('now')),
				completed_at              TEXT
			)`,
			`CREATE INDEX IF NOT EXISTS idx_intake_sessions_user ON intake_sessions(user_id, status)`,
			`CREATE INDEX IF NOT EXISTS idx_intake_sessions_updated ON intake_sessions(updated_at)`,
			`CREATE TABLE IF NOT EXISTS intake_events (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id   INTEGER NOT NULL REFERENCES intake_sessions(id) ON DELETE CASCADE,
				seq          INTEGER NOT NULL,
				kind         TEXT NOT NULL CHECK(kind IN
				               ('transcript_chunk','spec','summaries','ticket_preview',
				                'project_match','impacts','checkpoint','restore',
				                'language','status')),
				source       TEXT NOT NULL DEFAULT 'ai' CHECK(source IN ('ai','user','system')),
				label        TEXT NOT NULL DEFAULT '',
				payload_json TEXT NOT NULL DEFAULT '',
				created_at   TEXT NOT NULL DEFAULT (datetime('now'))
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_intake_events_seq ON intake_events(session_id, seq)`,
			`CREATE INDEX IF NOT EXISTS idx_intake_events_kind ON intake_events(session_id, kind, seq DESC)`,
		}},
		// M135 / PAI-706: per-user override for the voice-intake project
		// auto-switch confidence threshold. NULL = use the instance default
		// (app_settings key intake_confidence_threshold, default 90).
		{135, []string{
			`ALTER TABLE users ADD COLUMN intake_confidence_threshold INTEGER`,
		}},
		// M136 / PAI-710: speech-to-text provider settings for the voice
		// intake workbench, on the M74 singleton row. Key is secretvault-
		// encrypted (domain ai:elevenlabs); voice_base_url exists for the
		// ElevenLabs Enterprise EU residency host, which uses a DIFFERENT
		// hostname AND key than api.elevenlabs.io (START research finding).
		{136, []string{
			`ALTER TABLE ai_settings ADD COLUMN voice_provider TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_settings ADD COLUMN voice_api_key_encrypted BLOB`,
			`ALTER TABLE ai_settings ADD COLUMN voice_base_url TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_settings ADD COLUMN voice_stt_model TEXT NOT NULL DEFAULT ''`,
		}},
		// M137 / PAI-714: text-to-speech settings for the intake
		// understanding check (speak the selected ELI summary). Same
		// provider + key as STT (M136); voice/model are TTS-specific.
		{137, []string{
			`ALTER TABLE ai_settings ADD COLUMN tts_voice_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_settings ADD COLUMN tts_model TEXT NOT NULL DEFAULT ''`,
		}},

		// M138 / PAI-739: break the super-admin bootstrap deadlock on
		// existing installs. The first-run seed used to create the
		// 'admin' user with role admin only, while granting super_admin
		// requires being one — instances bootstrapped that way could
		// never reach the role without DB surgery. Promote exactly the
		// seeded account, and only when the instance has no super-admin
		// at all (instances that already have one are left untouched).
		{138, []string{PromoteSeededAdminSQL}},

		// M139 / PAI-742: record whether a session was minted by the OIDC
		// callback. The dashboard's local-2FA nag reads it — for SSO
		// sessions the second factor is the IdP's policy, not local TOTP,
		// so nagging trains users to ignore security banners.
		{139, []string{
			`ALTER TABLE sessions ADD COLUMN via_oidc INTEGER NOT NULL DEFAULT 0`,
		}},

		// M140 / PAI-702: bind an Implement-this run to the code the local
		// runner observed before and after execution. These are declared
		// references, not server-validated Git objects: the backend remains free
		// of repository credentials and Git execution. Equal base/head values
		// explicitly mean the run produced no commit.
		{140, []string{
			`ALTER TABLE agent_runs ADD COLUMN repo_url TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_runs ADD COLUMN branch_name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_runs ADD COLUMN commit_base_sha TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_runs ADD COLUMN commit_sha TEXT NOT NULL DEFAULT ''`,
		}},

		// M141 / PAI-754: make the project lifecycle creation gate a storage
		// invariant as well as a handler check. The handler provides the normal
		// clear 409 response; these triggers close the race where a project is
		// frozen/archived/deleted between that check and the INSERT, and protect
		// less common issue-producing paths from bypassing lifecycle policy.
		{141, []string{
			`CREATE TRIGGER IF NOT EXISTS trg_issues_reject_frozen_project
				BEFORE INSERT ON issues
				WHEN NEW.project_id IS NOT NULL
				 AND (SELECT status FROM projects WHERE id=NEW.project_id) = 'frozen'
				BEGIN SELECT RAISE(ABORT, 'project is frozen; new issues are disabled'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issues_reject_archived_project
				BEFORE INSERT ON issues
				WHEN NEW.project_id IS NOT NULL
				 AND (SELECT status FROM projects WHERE id=NEW.project_id) = 'archived'
				BEGIN SELECT RAISE(ABORT, 'project is archived; new issues are disabled'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_issues_reject_deleted_project
				BEFORE INSERT ON issues
				WHEN NEW.project_id IS NOT NULL
				 AND (SELECT status FROM projects WHERE id=NEW.project_id) = 'deleted'
				BEGIN SELECT RAISE(ABORT, 'project is deleted; new issues are disabled'); END`,
		}},

		// M142 / PAI-799: provider-neutral, append-only telemetry for one
		// Implement-this run. The event table is the immutable history; the
		// one-row-per-run latest table is only an efficient pointer/snapshot aid.
		// Payloads are deliberately columnar and allowlisted: no provider blobs,
		// prompts, tool arguments, command output, source, or environment data.
		{142, []string{
			`CREATE TABLE IF NOT EXISTS agent_run_telemetry (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id              INTEGER NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
				sequence            INTEGER NOT NULL CHECK(sequence > 0 AND sequence <= 2147483647),
				correlation_id      TEXT NOT NULL CHECK(length(correlation_id) BETWEEN 1 AND 128),
				provider            TEXT NOT NULL CHECK(length(provider) BETWEEN 1 AND 64),
				adapter             TEXT NOT NULL CHECK(length(adapter) BETWEEN 1 AND 64),
				agent_reported_at   TEXT NOT NULL,
				server_received_at  TEXT NOT NULL,
				kind                TEXT NOT NULL CHECK(kind IN ('heartbeat','progress','phase','needs_input','blocker','estimate')),
				heartbeat           INTEGER NOT NULL DEFAULT 0 CHECK(heartbeat IN (0,1)),
				phase               TEXT NOT NULL DEFAULT 'unknown'
				                    CHECK(phase IN ('unknown','starting','planning','implementing','testing','reviewing','deploying','waiting','completed')),
				activity            TEXT NOT NULL DEFAULT '' CHECK(length(CAST(activity AS BLOB)) <= 280),
				needs_input         INTEGER NOT NULL DEFAULT 0 CHECK(needs_input IN (0,1)),
				blocker_state       TEXT NOT NULL DEFAULT 'none'
				                    CHECK(blocker_state IN ('none','input','dependency','permission','environment','external','unknown')),
				estimate_revision   INTEGER CHECK(estimate_revision BETWEEN 1 AND 2147483647),
				progress_percent    REAL CHECK(progress_percent BETWEEN 0 AND 100),
				eta_seconds         INTEGER CHECK(eta_seconds BETWEEN 0 AND 31536000),
				eta_min_seconds     INTEGER CHECK(eta_min_seconds BETWEEN 0 AND 31536000),
				eta_max_seconds     INTEGER CHECK(eta_max_seconds BETWEEN 0 AND 31536000),
				estimate_source     TEXT NOT NULL DEFAULT ''
				                    CHECK(estimate_source IN ('','agent','adapter','provider','tool')),
				estimate_confidence REAL CHECK(estimate_confidence BETWEEN 0 AND 1),
				estimate_basis      TEXT NOT NULL DEFAULT '' CHECK(length(CAST(estimate_basis AS BLOB)) <= 240),
				UNIQUE(run_id, sequence)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_run_telemetry_history
			 ON agent_run_telemetry(run_id, sequence DESC)`,
			`CREATE TABLE IF NOT EXISTS agent_run_telemetry_latest (
				run_id             INTEGER PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
				telemetry_id       INTEGER NOT NULL UNIQUE REFERENCES agent_run_telemetry(id) ON DELETE CASCADE,
				sequence           INTEGER NOT NULL,
				last_heartbeat_at  TEXT
			)`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_run_telemetry_no_update
			 BEFORE UPDATE ON agent_run_telemetry
			 BEGIN SELECT RAISE(ABORT, 'agent run telemetry is append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_run_telemetry_terminal_guard
			 BEFORE INSERT ON agent_run_telemetry
			 WHEN (SELECT status FROM agent_runs WHERE id=NEW.run_id) IN ('tests_passed','tests_failed','deployed','failed','cancelled','drafted')
			 BEGIN SELECT RAISE(ABORT, 'terminal run telemetry is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_run_telemetry_sequence_guard
			 BEFORE INSERT ON agent_run_telemetry
			 WHEN NEW.sequence <= COALESCE((SELECT MAX(sequence) FROM agent_run_telemetry WHERE run_id=NEW.run_id), 0)
			 BEGIN SELECT RAISE(ABORT, 'agent run telemetry sequence is not monotonic'); END`,
		}},

		// M143 / PAI-801: integrate the supervised runner with the telemetry
		// contract. SQLite cannot alter a CHECK constraint, so rebuild agent_runs
		// to add the truthful `completed` terminal status and the durable marker
		// used to distinguish new supervised claims from legacy runners. Preserve
		// every M131/M132/M140 column and recreate every index.
		// Snapshot sqlite_sequence before dropping the old AUTOINCREMENT table;
		// explicit-id copying alone would otherwise lower the next id when rows
		// had previously been deleted.
		//
		// The latest telemetry row is an event pointer, not a state snapshot. Add
		// separately indexed heartbeat/semantic/estimate pointers so a heartbeat
		// cannot erase the last useful activity or ETA fact. Rebuild every pointer
		// from authoritative history, including projection rows that were missing
		// or stale, and add a byte-count trigger for M142 databases whose original
		// SQLite length() checks counted Unicode code points.
		{143, []string{
			`PRAGMA foreign_keys=OFF`,
			`DROP TRIGGER IF EXISTS trg_agent_run_telemetry_terminal_guard`,
			`CREATE TEMP TABLE agent_runs_m143_sequence (seq INTEGER NOT NULL)`,
			`INSERT INTO agent_runs_m143_sequence(seq)
			 SELECT MAX(COALESCE((SELECT seq FROM sqlite_sequence WHERE name='agent_runs'),0), COALESCE((SELECT MAX(id) FROM agent_runs),0))`,
			`CREATE TABLE agent_runs_m143 (
				id                           INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id                     INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
				project_id                   INTEGER REFERENCES projects(id) ON DELETE SET NULL,
				device_id                    TEXT NOT NULL DEFAULT '',
				requested_by                 INTEGER REFERENCES users(id) ON DELETE SET NULL,
				agent_name                   TEXT NOT NULL DEFAULT '',
				session_id                   TEXT NOT NULL DEFAULT '',
				status                       TEXT NOT NULL DEFAULT 'queued'
					CHECK(status IN ('queued','running','completed','tests_passed','tests_failed','deployed','failed','cancelled','drafted')),
				version                      TEXT NOT NULL DEFAULT '',
				tests_summary                TEXT,
				deploy_target                TEXT NOT NULL DEFAULT '',
				log_attachment_id            INTEGER,
				error                        TEXT NOT NULL DEFAULT '',
				created_at                   TEXT NOT NULL DEFAULT (datetime('now')),
				started_at                   TEXT,
				finished_at                  TEXT,
				claimed_by                   INTEGER REFERENCES users(id) ON DELETE SET NULL,
				action_key                   TEXT NOT NULL DEFAULT 'claude_cli.implement',
				provider_kind                TEXT NOT NULL DEFAULT 'local_cli',
				provider_id                  TEXT NOT NULL DEFAULT 'claude_cli',
				provider_label               TEXT NOT NULL DEFAULT 'Claude Code',
				model                        TEXT NOT NULL DEFAULT '',
				run_mode                     TEXT NOT NULL DEFAULT 'edit',
				profile_id                   TEXT NOT NULL DEFAULT '',
				effort                       TEXT NOT NULL DEFAULT '',
				prompt_preset_ref            TEXT NOT NULL DEFAULT '',
				context_pack                 TEXT NOT NULL DEFAULT '',
				context_truncated            INTEGER NOT NULL DEFAULT 0,
				context_sources_json         TEXT NOT NULL DEFAULT '',
				prompt_tokens                INTEGER NOT NULL DEFAULT 0,
				completion_tokens            INTEGER NOT NULL DEFAULT 0,
				finish_reason                TEXT NOT NULL DEFAULT '',
				source_draft_run_id          INTEGER REFERENCES agent_runs(id) ON DELETE SET NULL,
				followup_run_id              INTEGER REFERENCES agent_runs(id) ON DELETE SET NULL,
				repo_url                     TEXT NOT NULL DEFAULT '',
				branch_name                  TEXT NOT NULL DEFAULT '',
				commit_base_sha              TEXT NOT NULL DEFAULT '',
				commit_sha                   TEXT NOT NULL DEFAULT '',
				expects_supervisor_telemetry INTEGER NOT NULL DEFAULT 0 CHECK(expects_supervisor_telemetry IN (0,1))
			)`,
			`INSERT INTO agent_runs_m143(
				id, issue_id, project_id, device_id, requested_by, agent_name, session_id,
				status, version, tests_summary, deploy_target, log_attachment_id, error,
				created_at, started_at, finished_at, claimed_by, action_key, provider_kind,
				provider_id, provider_label, model, run_mode, profile_id, effort,
				prompt_preset_ref, context_pack, context_truncated, context_sources_json,
				prompt_tokens, completion_tokens, finish_reason, source_draft_run_id,
				followup_run_id, repo_url, branch_name, commit_base_sha, commit_sha
			 )
			 SELECT id, issue_id, project_id, device_id, requested_by, agent_name, session_id,
				status, version, tests_summary, deploy_target, log_attachment_id, error,
				created_at, started_at, finished_at, claimed_by, action_key, provider_kind,
				provider_id, provider_label, model, run_mode, profile_id, effort,
				prompt_preset_ref, context_pack, context_truncated, context_sources_json,
				prompt_tokens, completion_tokens, finish_reason, source_draft_run_id,
				followup_run_id, repo_url, branch_name, commit_base_sha, commit_sha
			   FROM agent_runs`,
			`DROP TABLE agent_runs`,
			`ALTER TABLE agent_runs_m143 RENAME TO agent_runs`,
			`INSERT INTO sqlite_sequence(name,seq)
			 SELECT 'agent_runs',seq FROM agent_runs_m143_sequence
			 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='agent_runs')`,
			`UPDATE sqlite_sequence
			 SET seq=MAX(seq,(SELECT seq FROM agent_runs_m143_sequence))
			 WHERE name='agent_runs'`,
			`DROP TABLE agent_runs_m143_sequence`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_issue ON agent_runs(issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_status ON agent_runs(status)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_active_issue ON agent_runs(issue_id) WHERE status IN ('queued','running')`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_claimed_by ON agent_runs(claimed_by)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_action_key ON agent_runs(action_key)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_run_mode ON agent_runs(run_mode)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_provider_id ON agent_runs(provider_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_source_draft ON agent_runs(source_draft_run_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_followup ON agent_runs(followup_run_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_supervisor_active ON agent_runs(status, expects_supervisor_telemetry, started_at) WHERE status IN ('queued','running')`,
			`ALTER TABLE agent_run_telemetry_latest ADD COLUMN heartbeat_telemetry_id INTEGER REFERENCES agent_run_telemetry(id) ON DELETE SET NULL`,
			`ALTER TABLE agent_run_telemetry_latest ADD COLUMN semantic_telemetry_id INTEGER REFERENCES agent_run_telemetry(id) ON DELETE SET NULL`,
			`ALTER TABLE agent_run_telemetry_latest ADD COLUMN estimate_telemetry_id INTEGER REFERENCES agent_run_telemetry(id) ON DELETE SET NULL`,
			`ALTER TABLE agent_run_telemetry_latest ADD COLUMN latest_event_at TEXT`,
			`ALTER TABLE agent_run_telemetry_latest ADD COLUMN latest_semantic_at TEXT`,
			`ALTER TABLE agent_run_telemetry_latest ADD COLUMN latest_estimate_at TEXT`,
			clearAgentRunTelemetryLatestSQL,
			rebuildAgentRunTelemetryLatestSQL,
			`CREATE INDEX IF NOT EXISTS idx_agent_run_telemetry_latest_heartbeat ON agent_run_telemetry_latest(last_heartbeat_at)`,
			`DROP TRIGGER IF EXISTS trg_agent_run_telemetry_terminal_guard`,
			`CREATE TRIGGER trg_agent_run_telemetry_terminal_guard
			 BEFORE INSERT ON agent_run_telemetry
			 WHEN (SELECT status FROM agent_runs WHERE id=NEW.run_id) IN ('completed','tests_passed','tests_failed','deployed','failed','cancelled','drafted')
			 BEGIN SELECT RAISE(ABORT, 'terminal run telemetry is immutable'); END`,
			`DROP TRIGGER IF EXISTS trg_agent_run_telemetry_byte_bounds`,
			`CREATE TRIGGER trg_agent_run_telemetry_byte_bounds
			 BEFORE INSERT ON agent_run_telemetry
			 WHEN length(CAST(NEW.activity AS BLOB)) > 280
			   OR length(CAST(NEW.estimate_basis AS BLOB)) > 240
			 BEGIN SELECT RAISE(ABORT, 'telemetry text exceeds UTF-8 byte bound'); END`,
			`PRAGMA foreign_keys=ON`,
		}},

		// M144 / PAI-802: issue-rooted delivery audit/read model. This is an
		// additive, atomic migration: immutable attempts and stage facts remain
		// the authority, while delivery_stage_latest is a rebuildable pointer
		// projection. The committed change log deliberately has no FK back to the
		// issue graph so a hard-deleted root still leaves a safe resumable
		// tombstone for PAI-804.
		{144, []string{
			`ALTER TABLE agent_runs ADD COLUMN delivery_instrumentation_version INTEGER NOT NULL DEFAULT 0 CHECK(delivery_instrumentation_version IN (0,1))`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_id_issue ON agent_runs(id, issue_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_id_issue ON attachments(id, issue_id)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_runs_delivery_legacy_active
			 ON agent_runs(project_id, id DESC)
			 WHERE delivery_instrumentation_version=0 AND status IN ('queued','running')`,
			`CREATE INDEX IF NOT EXISTS idx_agent_run_telemetry_estimate
			 ON agent_run_telemetry(run_id, estimate_revision DESC, sequence DESC)
			 WHERE estimate_revision IS NOT NULL`,

			`CREATE TABLE IF NOT EXISTS delivery_forbidden_value_patterns (
				pattern TEXT PRIMARY KEY,
				normalize_horizontal_whitespace INTEGER NOT NULL DEFAULT 0 CHECK(normalize_horizontal_whitespace IN (0,1)),
				case_sensitive INTEGER NOT NULL DEFAULT 0 CHECK(case_sensitive IN (0,1)),
				boundary_needle TEXT NOT NULL DEFAULT '',
				require_bearer_whitespace INTEGER NOT NULL DEFAULT 0 CHECK(require_bearer_whitespace IN (0,1))
			) WITHOUT ROWID`,
			`INSERT OR IGNORE INTO delivery_forbidden_value_patterns(
			 pattern,normalize_horizontal_whitespace,case_sensitive,boundary_needle,require_bearer_whitespace) VALUES
				('*api_key[=:]*',1,0,'api_key',0),('*api-key[=:]*',1,0,'api-key',0),
				('*apikey[=:]*',1,0,'apikey',0),('*token[=:]*',1,0,'token',0),
				('*secret[=:]*',1,0,'secret',0),('*password[=:]*',1,0,'password',0),
				('*credential[=:]*',1,0,'credential',0),
				('*api_key[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'api_key',0),
				('*api-key[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'api-key',0),
				('*apikey[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'apikey',0),
				('*token[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'token',0),
				('*secret[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'secret',0),
				('*password[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'password',0),
				('*credential[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'credential',0),
				('*bearer[0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'bearer',1),
				('*sk-live-[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0),
				('*sk_live_[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0),
				('*sk-test-[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0),
				('*sk_test_[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0),
				('*sk-proj-[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0),
				('*sk_proj_[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0),
				('*ghp_[0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z]*',0,0,'ghp_',0),
				('*gho_[0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z]*',0,0,'gho_',0),
				('*ghu_[0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z]*',0,0,'ghu_',0),
				('*ghs_[0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z]*',0,0,'ghs_',0),
				('*ghr_[0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z][0-9a-z]*',0,0,'ghr_',0),
				('*github_pat_[0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_][0-9a-z_]*',0,0,'github_pat_',0),
				('*xoxb-[0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-]*',0,0,'xoxb-',0),
				('*xoxa-[0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-]*',0,0,'xoxa-',0),
				('*xoxp-[0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-]*',0,0,'xoxp-',0),
				('*xoxr-[0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-]*',0,0,'xoxr-',0),
				('*xoxs-[0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-][0-9a-z-]*',0,0,'xoxs-',0),
				('*AKIA[0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z]',0,1,'AKIA',0),
				('*AKIA[0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][0-9A-Z][^0-9A-Za-z_]*',0,1,'AKIA',0),
				('*AIza[0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-]*',0,1,'AIza',0),
				('*eyJ[0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-].[0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-].[0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-][0-9A-Za-z_-]*',0,1,'eyJ',0),
				('*-----BEGIN *PRIVATE KEY-----*',0,1,'',0)`,

			`CREATE TABLE IF NOT EXISTS deliveries (
				id                 INTEGER PRIMARY KEY AUTOINCREMENT,
				issue_id           INTEGER NOT NULL UNIQUE REFERENCES issues(id) ON DELETE CASCADE,
				delivery_key       TEXT NOT NULL UNIQUE CHECK(length(CAST(delivery_key AS BLOB)) BETWEEN 7 AND 80)
				 CHECK(delivery_key GLOB '[A-Za-z0-9]*' AND delivery_key NOT GLOB '*[^A-Za-z0-9._:/-]*'),
				project_id_hint    INTEGER REFERENCES projects(id) ON DELETE SET NULL,
				spec_revision      INTEGER NOT NULL DEFAULT 1 CHECK(spec_revision > 0),
				change_sequence_high_water INTEGER NOT NULL DEFAULT 0 CHECK(change_sequence_high_water >= 0),
				created_at         TEXT NOT NULL,
				updated_at         TEXT NOT NULL,
				UNIQUE(id, issue_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_deliveries_project_issue ON deliveries(project_id_hint, issue_id)`,

			`CREATE TABLE IF NOT EXISTS delivery_reporters (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				delivery_id    INTEGER NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
				reporter_type  TEXT NOT NULL CHECK(reporter_type IN ('user','agent_run','external','system')),
				opaque_key     TEXT NOT NULL CHECK(length(CAST(opaque_key AS BLOB)) BETWEEN 1 AND 128)
				 CHECK(opaque_key GLOB '[A-Za-z0-9]*' AND opaque_key NOT GLOB '*[^A-Za-z0-9._:/-]*'),
				created_at     TEXT NOT NULL,
				UNIQUE(delivery_id, reporter_type, opaque_key),
				UNIQUE(delivery_id, id)
			)`,

			`CREATE TABLE IF NOT EXISTS delivery_events (
				id                 INTEGER PRIMARY KEY AUTOINCREMENT,
				delivery_id        INTEGER NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
				delivery_revision  INTEGER NOT NULL CHECK(delivery_revision > 0),
				idempotency_key    TEXT NOT NULL CHECK(length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 128)
				 CHECK(idempotency_key GLOB '[A-Za-z0-9]*' AND idempotency_key NOT GLOB '*[^A-Za-z0-9._:/-]*'),
				payload_hash       BLOB NOT NULL CHECK(length(payload_hash)=32),
				kind               TEXT NOT NULL CHECK(kind IN (
					'delivery_created','attempt_started','stage_execution_started','stage_reported',
					'handoff','progress_reset_authorized','run_linked','run_normalized',
					'run_lifecycle_observed','project_moved'
				)),
				reporter_id        INTEGER NOT NULL,
				reason_code        TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_code AS BLOB)) <= 64)
				 CHECK(reason_code='' OR (reason_code GLOB '[a-z]*' AND reason_code NOT GLOB '*[^a-z0-9_]*')),
				reason_text        TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_text AS BLOB)) <= 280),
				server_received_at TEXT NOT NULL,
				UNIQUE(delivery_id, delivery_revision),
				UNIQUE(delivery_id, reporter_id, kind, idempotency_key),
				UNIQUE(delivery_id, id),
				UNIQUE(delivery_id, id, reporter_id),
				FOREIGN KEY(delivery_id, reporter_id)
				 REFERENCES delivery_reporters(delivery_id, id) DEFERRABLE INITIALLY DEFERRED
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_events_history ON delivery_events(delivery_id, id DESC)`,

			`CREATE TABLE IF NOT EXISTS delivery_attempts (
				id                    INTEGER PRIMARY KEY AUTOINCREMENT,
				delivery_id           INTEGER NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
				attempt_number        INTEGER NOT NULL CHECK(attempt_number > 0),
				plan_revision         INTEGER NOT NULL CHECK(plan_revision > 0),
				previous_attempt_id   INTEGER,
				start_delivery_event_id INTEGER NOT NULL,
				project_id_at_start   INTEGER,
				reason_code           TEXT NOT NULL CHECK(length(CAST(reason_code AS BLOB)) BETWEEN 1 AND 64)
				 CHECK(reason_code GLOB '[a-z]*' AND reason_code NOT GLOB '*[^a-z0-9_]*'),
				reason_text           TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_text AS BLOB)) <= 280),
				created_at            TEXT NOT NULL,
				UNIQUE(delivery_id, attempt_number),
				UNIQUE(delivery_id, plan_revision),
				UNIQUE(delivery_id, id),
				FOREIGN KEY(delivery_id, previous_attempt_id)
				 REFERENCES delivery_attempts(delivery_id, id) DEFERRABLE INITIALLY DEFERRED,
				FOREIGN KEY(delivery_id, start_delivery_event_id)
				 REFERENCES delivery_events(delivery_id, id) DEFERRABLE INITIALLY DEFERRED
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_attempts_current ON delivery_attempts(delivery_id, attempt_number DESC)`,

			`CREATE TABLE IF NOT EXISTS delivery_attempt_stage_policy (
				delivery_id       INTEGER NOT NULL,
				attempt_id        INTEGER NOT NULL,
				stage_key         TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
				sort_order        INTEGER NOT NULL CHECK(sort_order BETWEEN 1 AND 5),
				applicability     TEXT NOT NULL CHECK(applicability IN ('required','not_applicable')),
				weight            INTEGER NOT NULL CHECK(weight BETWEEN 0 AND 100),
				policy_reference  TEXT NOT NULL DEFAULT '' CHECK(length(CAST(policy_reference AS BLOB)) <= 160)
				 CHECK(policy_reference='' OR (policy_reference GLOB '[A-Za-z0-9]*' AND policy_reference NOT GLOB '*[^A-Za-z0-9._:/@+-]*')),
				reason_code       TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_code AS BLOB)) <= 64)
				 CHECK(reason_code='' OR (reason_code GLOB '[a-z]*' AND reason_code NOT GLOB '*[^a-z0-9_]*')),
				reason_text       TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_text AS BLOB)) <= 280),
				authorized_by_reporter_id INTEGER,
				created_at        TEXT NOT NULL,
				PRIMARY KEY(attempt_id, stage_key),
				UNIQUE(attempt_id, sort_order),
				FOREIGN KEY(delivery_id, attempt_id)
				 REFERENCES delivery_attempts(delivery_id, id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, authorized_by_reporter_id)
				 REFERENCES delivery_reporters(delivery_id, id) DEFERRABLE INITIALLY DEFERRED,
				CHECK(applicability='required' OR
				      (policy_reference<>'' AND reason_code<>'' AND authorized_by_reporter_id IS NOT NULL))
			)`,
			`CREATE TABLE IF NOT EXISTS delivery_attempt_policy_seals (
				delivery_id INTEGER NOT NULL,
				attempt_id  INTEGER PRIMARY KEY,
				sealed_at   TEXT NOT NULL,
				UNIQUE(delivery_id, attempt_id),
				FOREIGN KEY(delivery_id, attempt_id)
				 REFERENCES delivery_attempts(delivery_id, id) ON DELETE CASCADE
			)`,

			`CREATE TABLE IF NOT EXISTS delivery_stage_events (
				id                       INTEGER PRIMARY KEY AUTOINCREMENT,
				delivery_id              INTEGER NOT NULL,
				attempt_id               INTEGER NOT NULL,
				stage_key                TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
				execution_number         INTEGER NOT NULL CHECK(execution_number > 0),
				event_sequence           INTEGER NOT NULL CHECK(event_sequence > 0),
				authority_epoch          INTEGER NOT NULL CHECK(authority_epoch > 0),
				delivery_event_id        INTEGER,
				event_type               TEXT NOT NULL CHECK(event_type IN (
					'execution_started','semantic_report','heartbeat','estimate','handoff',
					'progress_reset_authorized','lifecycle_normalized'
				)),
				reporter_id              INTEGER NOT NULL,
				execution_start_stage_event_id INTEGER,
				previous_stage_event_id  INTEGER,
				based_on_stage_event_id  INTEGER,
				retry_of_stage_event_id  INTEGER,
				handoff_from_reporter_id INTEGER,
				source_sequence          INTEGER CHECK(source_sequence > 0),
				source_idempotency_key   TEXT CHECK(source_idempotency_key IS NULL OR
				 (length(CAST(source_idempotency_key AS BLOB)) BETWEEN 1 AND 128 AND
				  source_idempotency_key GLOB '[A-Za-z0-9]*' AND source_idempotency_key NOT GLOB '*[^A-Za-z0-9._:/-]*')),
				source_payload_hash      BLOB CHECK(source_payload_hash IS NULL OR length(source_payload_hash)=32),
				authority_source_sequence_cutoff INTEGER CHECK(authority_source_sequence_cutoff >= 0),
				semantic_state           TEXT CHECK(semantic_state IN ('pending','active','waiting','succeeded','failed','cancelled','draft_ready','unknown')),
				activity                 TEXT NOT NULL DEFAULT '' CHECK(length(CAST(activity AS BLOB)) <= 280),
				needs_input              INTEGER NOT NULL DEFAULT 0 CHECK(needs_input IN (0,1)),
				declared_blocker_count   INTEGER NOT NULL DEFAULT 0 CHECK(declared_blocker_count BETWEEN 0 AND 16),
				current_blocker_count    INTEGER NOT NULL DEFAULT 0 CHECK(current_blocker_count BETWEEN 0 AND declared_blocker_count),
				declared_evidence_count  INTEGER NOT NULL DEFAULT 0 CHECK(declared_evidence_count BETWEEN 0 AND 16),
				heartbeat                INTEGER NOT NULL DEFAULT 0 CHECK(heartbeat IN (0,1)),
				estimate_revision        INTEGER CHECK(estimate_revision > 0),
				progress_percent         REAL CHECK(progress_percent BETWEEN 0 AND 100),
				eta_seconds              INTEGER CHECK(eta_seconds BETWEEN 0 AND 31536000),
				eta_min_seconds          INTEGER CHECK(eta_min_seconds BETWEEN 0 AND 31536000),
				eta_max_seconds          INTEGER CHECK(eta_max_seconds BETWEEN 0 AND 31536000),
				estimate_source          TEXT NOT NULL DEFAULT '' CHECK(estimate_source IN ('','agent','adapter','provider','tool','external')),
				estimate_confidence      REAL CHECK(estimate_confidence BETWEEN 0 AND 1),
				estimate_basis           TEXT NOT NULL DEFAULT '' CHECK(length(CAST(estimate_basis AS BLOB)) <= 240),
				spec_revision            INTEGER CHECK(spec_revision > 0),
				reset_epoch              INTEGER CHECK(reset_epoch > 0),
				reset_source_cutoff      INTEGER CHECK(reset_source_cutoff >= 0),
				reset_source_kind        TEXT NOT NULL DEFAULT '' CHECK(reset_source_kind IN ('','stage_events','stage_and_agent_run_telemetry')),
				reset_telemetry_run_id   INTEGER,
				reset_telemetry_sequence_cutoff INTEGER CHECK(reset_telemetry_sequence_cutoff >= 0),
				reset_authority_anchor_stage_event_id INTEGER,
				reset_owner_reporter_id  INTEGER,
				reason_code              TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_code AS BLOB)) <= 64)
				 CHECK(reason_code='' OR (reason_code GLOB '[a-z]*' AND reason_code NOT GLOB '*[^a-z0-9_]*')),
				reason_text              TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_text AS BLOB)) <= 280),
				server_received_at       TEXT NOT NULL,
				ended_at                 TEXT,
				UNIQUE(delivery_event_id),
				UNIQUE(delivery_id, source_idempotency_key),
				UNIQUE(delivery_id, id),
				UNIQUE(delivery_id, attempt_id, stage_key, execution_number, id),
				UNIQUE(delivery_id, attempt_id, stage_key, execution_number, authority_epoch, reporter_id, id),
				UNIQUE(attempt_id, stage_key, execution_number, event_sequence),
				FOREIGN KEY(delivery_id, attempt_id)
				 REFERENCES delivery_attempts(delivery_id, id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, delivery_event_id, reporter_id)
				 REFERENCES delivery_events(delivery_id, id, reporter_id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, reporter_id)
				 REFERENCES delivery_reporters(delivery_id, id),
				FOREIGN KEY(delivery_id, previous_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id) DEFERRABLE INITIALLY DEFERRED,
				FOREIGN KEY(delivery_id, based_on_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id) DEFERRABLE INITIALLY DEFERRED,
				FOREIGN KEY(delivery_id, retry_of_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id) DEFERRABLE INITIALLY DEFERRED,
				FOREIGN KEY(delivery_id, handoff_from_reporter_id)
				 REFERENCES delivery_reporters(delivery_id, id) DEFERRABLE INITIALLY DEFERRED,
				FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,reset_telemetry_run_id,reset_owner_reporter_id)
				 REFERENCES delivery_agent_run_links(delivery_id,attempt_id,stage_key,execution_number,agent_run_id,reporter_id)
				 DEFERRABLE INITIALLY DEFERRED,
				FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,reset_owner_reporter_id,
				 reset_authority_anchor_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,reporter_id,id)
				 DEFERRABLE INITIALLY DEFERRED,
				CHECK((event_type='execution_started' AND delivery_event_id IS NOT NULL AND source_idempotency_key IS NULL AND source_payload_hash IS NULL AND
				       semantic_state IS NOT NULL AND semantic_state='active' AND execution_start_stage_event_id IS NULL AND source_sequence IS NULL AND
				       authority_source_sequence_cutoff IS NOT NULL AND authority_source_sequence_cutoff=0 AND handoff_from_reporter_id IS NULL AND
				       activity='' AND needs_input=0 AND declared_blocker_count=0 AND current_blocker_count=0 AND declared_evidence_count=0 AND
				       heartbeat=0 AND estimate_revision IS NULL AND progress_percent IS NULL AND eta_seconds IS NULL AND
				       eta_min_seconds IS NULL AND eta_max_seconds IS NULL AND estimate_source='' AND estimate_confidence IS NULL AND
				       estimate_basis='' AND spec_revision IS NULL AND reset_epoch IS NULL AND reset_source_cutoff IS NULL AND
				       reset_source_kind='' AND reset_telemetry_run_id IS NULL AND reset_telemetry_sequence_cutoff IS NULL AND
				       reset_authority_anchor_stage_event_id IS NULL AND reset_owner_reporter_id IS NULL AND ended_at IS NULL) OR
				      (event_type IN ('semantic_report','lifecycle_normalized') AND delivery_event_id IS NOT NULL AND source_idempotency_key IS NULL AND source_payload_hash IS NULL AND semantic_state IS NOT NULL AND
				       execution_start_stage_event_id IS NOT NULL AND based_on_stage_event_id IS NULL AND retry_of_stage_event_id IS NULL AND
				       handoff_from_reporter_id IS NULL AND authority_source_sequence_cutoff IS NULL AND heartbeat=0 AND estimate_revision IS NULL AND progress_percent IS NULL AND
				       eta_seconds IS NULL AND eta_min_seconds IS NULL AND eta_max_seconds IS NULL AND estimate_source='' AND
				       estimate_confidence IS NULL AND estimate_basis='' AND reset_epoch IS NULL AND reset_source_cutoff IS NULL AND
				       reset_source_kind='' AND reset_telemetry_run_id IS NULL AND reset_telemetry_sequence_cutoff IS NULL AND
				       reset_authority_anchor_stage_event_id IS NULL AND reset_owner_reporter_id IS NULL AND
				       ((stage_key='specification' AND spec_revision IS NOT NULL) OR
				        (stage_key<>'specification' AND spec_revision IS NULL)) AND
				       (event_type='semantic_report' OR (source_sequence IS NULL AND needs_input=0 AND
				        declared_blocker_count=0 AND current_blocker_count=0)) AND
				       ((semantic_state IN ('succeeded','failed','cancelled','draft_ready') AND ended_at IS NOT NULL) OR
				        (semantic_state IN ('pending','active','waiting','unknown') AND ended_at IS NULL))) OR
				      (event_type='heartbeat' AND delivery_event_id IS NULL AND source_idempotency_key IS NOT NULL AND source_payload_hash IS NOT NULL AND
				       source_sequence IS NOT NULL AND heartbeat=1 AND execution_start_stage_event_id IS NOT NULL AND semantic_state IS NULL AND
				       based_on_stage_event_id IS NULL AND retry_of_stage_event_id IS NULL AND handoff_from_reporter_id IS NULL AND authority_source_sequence_cutoff IS NULL AND
				       activity='' AND needs_input=0 AND declared_blocker_count=0 AND current_blocker_count=0 AND declared_evidence_count=0 AND
				       estimate_revision IS NULL AND progress_percent IS NULL AND eta_seconds IS NULL AND eta_min_seconds IS NULL AND
				       eta_max_seconds IS NULL AND estimate_source='' AND estimate_confidence IS NULL AND estimate_basis='' AND
				       spec_revision IS NULL AND reset_epoch IS NULL AND reset_source_cutoff IS NULL AND reset_source_kind='' AND
				       reset_telemetry_run_id IS NULL AND reset_telemetry_sequence_cutoff IS NULL AND
				       reset_authority_anchor_stage_event_id IS NULL AND reset_owner_reporter_id IS NULL AND ended_at IS NULL) OR
				      (event_type='estimate' AND delivery_event_id IS NOT NULL AND source_idempotency_key IS NULL AND source_payload_hash IS NULL AND heartbeat=0 AND source_sequence IS NOT NULL AND estimate_revision IS NOT NULL AND estimate_source<>'' AND
				       execution_start_stage_event_id IS NOT NULL AND semantic_state IS NULL AND based_on_stage_event_id IS NULL AND
				       retry_of_stage_event_id IS NULL AND handoff_from_reporter_id IS NULL AND authority_source_sequence_cutoff IS NULL AND activity='' AND needs_input=0 AND
				       declared_blocker_count=0 AND current_blocker_count=0 AND declared_evidence_count=0 AND
				       estimate_confidence IS NOT NULL AND estimate_confidence>0 AND estimate_basis<>'' AND
				       (progress_percent IS NOT NULL OR eta_min_seconds IS NOT NULL) AND
				       ((eta_seconds IS NULL AND eta_min_seconds IS NULL AND eta_max_seconds IS NULL) OR
				        (eta_min_seconds IS NOT NULL AND eta_max_seconds IS NOT NULL AND eta_min_seconds<=eta_max_seconds AND
				         (eta_seconds IS NULL OR (eta_seconds>=eta_min_seconds AND eta_seconds<=eta_max_seconds)))) AND
				       spec_revision IS NULL AND reset_epoch IS NULL AND reset_source_cutoff IS NULL AND reset_source_kind='' AND
				       reset_telemetry_run_id IS NULL AND reset_telemetry_sequence_cutoff IS NULL AND
				       reset_authority_anchor_stage_event_id IS NULL AND reset_owner_reporter_id IS NULL AND ended_at IS NULL) OR
				      (event_type='handoff' AND delivery_event_id IS NOT NULL AND source_idempotency_key IS NULL AND source_payload_hash IS NULL AND handoff_from_reporter_id IS NOT NULL AND execution_start_stage_event_id IS NOT NULL AND
				       source_sequence IS NULL AND authority_source_sequence_cutoff IS NOT NULL AND semantic_state IS NULL AND based_on_stage_event_id IS NULL AND retry_of_stage_event_id IS NULL AND
				       activity='' AND needs_input=0 AND declared_blocker_count=0 AND current_blocker_count=0 AND declared_evidence_count=0 AND
				       heartbeat=0 AND estimate_revision IS NULL AND progress_percent IS NULL AND eta_seconds IS NULL AND
				       eta_min_seconds IS NULL AND eta_max_seconds IS NULL AND estimate_source='' AND estimate_confidence IS NULL AND
				       estimate_basis='' AND spec_revision IS NULL AND reset_epoch IS NULL AND reset_source_cutoff IS NULL AND
				       reset_source_kind='' AND reset_telemetry_run_id IS NULL AND reset_telemetry_sequence_cutoff IS NULL AND
				       reset_authority_anchor_stage_event_id IS NULL AND reset_owner_reporter_id IS NULL AND reason_code<>'' AND ended_at IS NULL) OR
				      (event_type='progress_reset_authorized' AND delivery_event_id IS NOT NULL AND source_idempotency_key IS NULL AND source_payload_hash IS NULL AND reset_epoch IS NOT NULL AND reset_source_cutoff IS NOT NULL AND
				       reset_source_kind<>'' AND reset_authority_anchor_stage_event_id IS NOT NULL AND reset_owner_reporter_id IS NOT NULL AND
				       ((reset_source_kind='stage_events' AND reset_telemetry_run_id IS NULL AND reset_telemetry_sequence_cutoff IS NULL) OR
				        (reset_source_kind='stage_and_agent_run_telemetry' AND reset_telemetry_run_id IS NOT NULL AND
				         reset_telemetry_sequence_cutoff IS NOT NULL)) AND
				       reason_code<>'' AND execution_start_stage_event_id IS NOT NULL AND source_sequence IS NULL AND authority_source_sequence_cutoff IS NULL AND semantic_state IS NULL AND
				       based_on_stage_event_id IS NULL AND retry_of_stage_event_id IS NULL AND handoff_from_reporter_id IS NULL AND
				       activity='' AND needs_input=0 AND declared_blocker_count=0 AND current_blocker_count=0 AND declared_evidence_count=0 AND
				       heartbeat=0 AND estimate_revision IS NULL AND progress_percent IS NULL AND eta_seconds IS NULL AND
				       eta_min_seconds IS NULL AND eta_max_seconds IS NULL AND estimate_source='' AND estimate_confidence IS NULL AND
				       estimate_basis='' AND spec_revision IS NULL AND ended_at IS NULL))
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_stage_events_history
			 ON delivery_stage_events(attempt_id, stage_key, execution_number DESC, event_sequence DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_stage_events_source
			 ON delivery_stage_events(attempt_id, stage_key, execution_number, authority_epoch, source_sequence)
			 WHERE source_sequence IS NOT NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_delivery_stage_authority_epoch_unique
			 ON delivery_stage_events(attempt_id,stage_key,execution_number,authority_epoch)
			 WHERE event_type IN ('execution_started','handoff')`,

			`CREATE TABLE IF NOT EXISTS delivery_stage_blockers (
				delivery_id       INTEGER NOT NULL,
				stage_event_id    INTEGER NOT NULL,
				ordinal           INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 15),
				blocker_key       TEXT NOT NULL CHECK(length(CAST(blocker_key AS BLOB)) BETWEEN 1 AND 96)
				 CHECK(blocker_key GLOB '[A-Za-z0-9]*' AND blocker_key NOT GLOB '*[^A-Za-z0-9._:/-]*'),
				blocker_class     TEXT NOT NULL CHECK(blocker_class IN ('input','dependency','permission','environment','external','unknown')),
				summary           TEXT NOT NULL DEFAULT '' CHECK(length(CAST(summary AS BLOB)) <= 280),
				is_current        INTEGER NOT NULL CHECK(is_current IN (0,1)),
				is_human_wait     INTEGER NOT NULL CHECK(is_human_wait IN (0,1)),
				interval_started_at TEXT NOT NULL,
				interval_ended_at TEXT NOT NULL,
				PRIMARY KEY(stage_event_id, ordinal),
				UNIQUE(stage_event_id, blocker_key),
				FOREIGN KEY(delivery_id, stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id) ON DELETE CASCADE
			)`,

			`CREATE TABLE IF NOT EXISTS delivery_evidence (
				delivery_id       INTEGER NOT NULL,
				root_issue_id     INTEGER NOT NULL,
				stage_event_id    INTEGER NOT NULL,
				ordinal           INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 15),
				evidence_type     TEXT NOT NULL CHECK(evidence_type IN (
					'spec_acceptance','approval','implementation_result','artifact','test_result',
					'deployment_result','verification_result'
				)),
				outcome           TEXT NOT NULL CHECK(outcome IN ('passed','failed','unknown')),
				reference_kind    TEXT NOT NULL CHECK(reference_kind IN ('digest','commit','attachment','external_ref','none')),
				reference_value   TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reference_value AS BLOB)) <= 192)
				 CHECK(reference_value='' OR (reference_value GLOB '[A-Za-z0-9]*' AND reference_value NOT GLOB '*[^A-Za-z0-9._:/@+-]*')),
				digest_sha256     TEXT NOT NULL DEFAULT '' CHECK(digest_sha256='' OR (length(CAST(digest_sha256 AS BLOB))=64 AND digest_sha256 NOT GLOB '*[^0-9a-f]*')),
				attachment_id     INTEGER,
				created_at        TEXT NOT NULL,
				PRIMARY KEY(stage_event_id, ordinal),
				FOREIGN KEY(delivery_id, stage_event_id)
					 REFERENCES delivery_stage_events(delivery_id, id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, root_issue_id)
					 REFERENCES deliveries(id, issue_id) ON DELETE CASCADE,
				FOREIGN KEY(attachment_id, root_issue_id)
				 REFERENCES attachments(id, issue_id) ON DELETE CASCADE,
				CHECK((reference_kind='attachment' AND attachment_id IS NOT NULL) OR
				      (reference_kind<>'attachment' AND attachment_id IS NULL))
			)`,

			`CREATE TABLE IF NOT EXISTS delivery_agent_run_links (
				agent_run_id             INTEGER PRIMARY KEY,
				root_issue_id            INTEGER NOT NULL,
				delivery_id              INTEGER NOT NULL,
				attempt_id               INTEGER NOT NULL,
				stage_key                TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
				execution_number         INTEGER NOT NULL CHECK(execution_number > 0),
				execution_start_stage_event_id INTEGER NOT NULL,
				reporter_id              INTEGER NOT NULL,
				link_delivery_event_id   INTEGER NOT NULL,
				created_at               TEXT NOT NULL,
				UNIQUE(delivery_id, agent_run_id),
				UNIQUE(attempt_id,stage_key,execution_number),
				UNIQUE(delivery_id,attempt_id,stage_key,execution_number,agent_run_id,reporter_id),
				FOREIGN KEY(agent_run_id, root_issue_id)
				 REFERENCES agent_runs(id, issue_id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, root_issue_id)
				 REFERENCES deliveries(id, issue_id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, attempt_id)
				 REFERENCES delivery_attempts(delivery_id, id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, attempt_id, stage_key, execution_number, execution_start_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, attempt_id, stage_key, execution_number, id),
				FOREIGN KEY(delivery_id, reporter_id)
				 REFERENCES delivery_reporters(delivery_id, id),
				FOREIGN KEY(delivery_id, link_delivery_event_id, reporter_id)
				 REFERENCES delivery_events(delivery_id, id, reporter_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_agent_run_execution
				 ON delivery_agent_run_links(attempt_id, stage_key, execution_number, agent_run_id)`,
			`CREATE TABLE IF NOT EXISTS delivery_agent_run_activations (
				delivery_id              INTEGER NOT NULL,
				attempt_id               INTEGER NOT NULL,
				stage_key                TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
				execution_number         INTEGER NOT NULL CHECK(execution_number > 0),
				authority_epoch          INTEGER NOT NULL CHECK(authority_epoch > 0),
				agent_run_id             INTEGER NOT NULL,
				reporter_id              INTEGER NOT NULL,
				authority_stage_event_id INTEGER NOT NULL,
				telemetry_sequence_cutoff INTEGER NOT NULL CHECK(telemetry_sequence_cutoff >= 0),
				created_at               TEXT NOT NULL,
				PRIMARY KEY(attempt_id,stage_key,execution_number,authority_epoch),
				FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,agent_run_id,reporter_id)
				 REFERENCES delivery_agent_run_links(delivery_id,attempt_id,stage_key,execution_number,agent_run_id,reporter_id)
				 ON DELETE CASCADE,
				FOREIGN KEY(delivery_id,reporter_id)
				 REFERENCES delivery_reporters(delivery_id,id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,reporter_id,authority_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,reporter_id,id)
				 ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_run_activations_run
				 ON delivery_agent_run_activations(agent_run_id,attempt_id,stage_key,execution_number,authority_epoch)`,

			`CREATE TABLE IF NOT EXISTS delivery_stage_latest (
				delivery_id              INTEGER NOT NULL,
				attempt_id               INTEGER NOT NULL,
				stage_key                TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
				execution_number         INTEGER NOT NULL CHECK(execution_number > 0),
				authority_epoch          INTEGER NOT NULL CHECK(authority_epoch > 0),
				current_reporter_id      INTEGER NOT NULL,
				execution_start_stage_event_id INTEGER NOT NULL,
				authority_stage_event_id INTEGER NOT NULL,
				semantic_stage_event_id  INTEGER,
				heartbeat_stage_event_id INTEGER,
				estimate_stage_event_id  INTEGER,
				updated_at               TEXT NOT NULL,
				PRIMARY KEY(attempt_id, stage_key),
				FOREIGN KEY(delivery_id, attempt_id)
				 REFERENCES delivery_attempts(delivery_id, id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, current_reporter_id)
				 REFERENCES delivery_reporters(delivery_id, id),
				FOREIGN KEY(delivery_id, execution_start_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id),
				FOREIGN KEY(delivery_id, authority_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id),
				FOREIGN KEY(delivery_id, semantic_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id),
				FOREIGN KEY(delivery_id, heartbeat_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id),
				FOREIGN KEY(delivery_id, estimate_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_stage_latest_delivery ON delivery_stage_latest(delivery_id, attempt_id, stage_key)`,

			`CREATE TABLE IF NOT EXISTS delivery_stage_durations (
				stage_execution_id       INTEGER PRIMARY KEY,
				terminal_stage_event_id  INTEGER NOT NULL,
				delivery_id              INTEGER NOT NULL,
				root_issue_id            INTEGER NOT NULL,
				attempt_id               INTEGER NOT NULL,
				stage_key                TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
				execution_number         INTEGER NOT NULL CHECK(execution_number > 0),
				project_id_at_completion INTEGER,
				estimator_policy_version INTEGER NOT NULL DEFAULT 1 CHECK(estimator_policy_version > 0),
				full_lead_seconds        INTEGER NOT NULL CHECK(full_lead_seconds >= 0),
				active_seconds           INTEGER NOT NULL CHECK(active_seconds >= 0),
				blocked_seconds          INTEGER NOT NULL CHECK(blocked_seconds >= 0),
				human_wait_seconds       INTEGER NOT NULL CHECK(human_wait_seconds >= 0),
				completed_at             TEXT NOT NULL,
				UNIQUE(delivery_id, attempt_id, stage_key, execution_number),
				FOREIGN KEY(delivery_id, root_issue_id)
				 REFERENCES deliveries(id, issue_id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, attempt_id)
				 REFERENCES delivery_attempts(delivery_id, id) ON DELETE CASCADE,
				FOREIGN KEY(delivery_id, attempt_id, stage_key, execution_number, stage_execution_id)
				 REFERENCES delivery_stage_events(delivery_id, attempt_id, stage_key, execution_number, id),
				FOREIGN KEY(delivery_id, attempt_id, stage_key, execution_number, terminal_stage_event_id)
				 REFERENCES delivery_stage_events(delivery_id, attempt_id, stage_key, execution_number, id),
				CHECK(full_lead_seconds=active_seconds+blocked_seconds+human_wait_seconds)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_stage_duration_history
			 ON delivery_stage_durations(project_id_at_completion, stage_key, estimator_policy_version, completed_at DESC, stage_execution_id DESC)`,

			`CREATE TABLE IF NOT EXISTS delivery_change_retention (
				floor_id        INTEGER PRIMARY KEY CHECK(floor_id >= 0),
				advanced_at     TEXT
			)`,
			`INSERT OR IGNORE INTO delivery_change_retention(floor_id) VALUES(0)`,
			`CREATE TABLE IF NOT EXISTS delivery_change_log (
				id                 INTEGER PRIMARY KEY AUTOINCREMENT,
				cursor_token       TEXT NOT NULL UNIQUE CHECK(length(CAST(cursor_token AS BLOB))=32 AND cursor_token NOT GLOB '*[^0-9a-f]*'),
				delivery_id        INTEGER NOT NULL,
				root_issue_id      INTEGER NOT NULL,
				delivery_key       TEXT NOT NULL CHECK(length(CAST(delivery_key AS BLOB)) BETWEEN 7 AND 80),
				project_id_hint    INTEGER,
				change_sequence    INTEGER NOT NULL CHECK(change_sequence > 0),
				delivery_revision  INTEGER NOT NULL CHECK(delivery_revision >= 0),
				kind               TEXT NOT NULL CHECK(kind IN ('delivery','attempt','stage','run','telemetry','issue','lane','project_move','root_deleted')),
				source_kind        TEXT NOT NULL CHECK(source_kind IN ('delivery_event','stage_event','agent_run','telemetry','issue','relation','system')),
				source_id          INTEGER,
				source_sequence    INTEGER,
				server_received_at TEXT NOT NULL,
				UNIQUE(delivery_id, change_sequence)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_change_delivery ON delivery_change_log(delivery_id, change_sequence DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_change_project_tail ON delivery_change_log(project_id_hint, id)`,

			`CREATE TRIGGER IF NOT EXISTS trg_delivery_events_revision_guard
			 BEFORE INSERT ON delivery_events
			 WHEN NEW.delivery_revision <> COALESCE((SELECT MAX(delivery_revision)+1 FROM delivery_events WHERE delivery_id=NEW.delivery_id), 1)
				 BEGIN SELECT RAISE(ABORT, 'delivery revision is not contiguous'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_runs_delivery_instrumentation_immutable
				 BEFORE UPDATE OF delivery_instrumentation_version ON agent_runs
				 WHEN NEW.delivery_instrumentation_version<>OLD.delivery_instrumentation_version
				 BEGIN SELECT RAISE(ABORT, 'agent run delivery instrumentation marker is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_change_sequence_guard
				 BEFORE INSERT ON delivery_change_log
				 WHEN NEW.change_sequence <> COALESCE((SELECT change_sequence_high_water+1 FROM deliveries WHERE id=NEW.delivery_id),
					COALESCE((SELECT MAX(change_sequence)+1 FROM delivery_change_log WHERE delivery_id=NEW.delivery_id),1))
				 BEGIN SELECT RAISE(ABORT, 'delivery change sequence is not contiguous'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_change_provenance_guard
				 BEFORE INSERT ON delivery_change_log WHEN
				  NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.id=NEW.delivery_id
				   AND d.issue_id=NEW.root_issue_id AND d.delivery_key=NEW.delivery_key
				   AND d.project_id_hint IS NEW.project_id_hint) OR
				  (NEW.kind='root_deleted' AND (EXISTS(SELECT 1 FROM issues i WHERE i.id=NEW.root_issue_id)
				   OR EXISTS(SELECT 1 FROM delivery_change_log prior WHERE prior.delivery_id=NEW.delivery_id
				    AND prior.kind='root_deleted'))) OR
				  (NEW.kind<>'root_deleted' AND NOT EXISTS(SELECT 1 FROM issues i WHERE i.id=NEW.root_issue_id))
				 BEGIN SELECT RAISE(ABORT, 'delivery change provenance does not match its live root'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_change_advance_high_water
				 AFTER INSERT ON delivery_change_log
				 WHEN EXISTS(SELECT 1 FROM deliveries WHERE id=NEW.delivery_id)
				 BEGIN UPDATE deliveries SET change_sequence_high_water=NEW.change_sequence WHERE id=NEW.delivery_id; END`,
			`CREATE TRIGGER IF NOT EXISTS trg_deliveries_change_high_water_guard
				 BEFORE UPDATE OF change_sequence_high_water ON deliveries
				 WHEN NEW.change_sequence_high_water<>OLD.change_sequence_high_water AND NOT (
					NEW.change_sequence_high_water=OLD.change_sequence_high_water+1 AND EXISTS(
					 SELECT 1 FROM delivery_change_log c WHERE c.delivery_id=OLD.id
					 AND c.change_sequence=NEW.change_sequence_high_water)
				 ) BEGIN SELECT RAISE(ABORT, 'delivery change high-water is log-owned'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_latest_children_guard_insert
			 BEFORE INSERT ON delivery_stage_latest
			 WHEN NEW.semantic_stage_event_id IS NOT NULL AND (
				(SELECT declared_blocker_count FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id) <>
				 (SELECT COUNT(*) FROM delivery_stage_blockers WHERE stage_event_id=NEW.semantic_stage_event_id) OR
				(SELECT current_blocker_count FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id) <>
				 (SELECT COUNT(*) FROM delivery_stage_blockers WHERE stage_event_id=NEW.semantic_stage_event_id AND is_current=1) OR
				(SELECT declared_evidence_count FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id) <>
				 (SELECT COUNT(*) FROM delivery_evidence WHERE stage_event_id=NEW.semantic_stage_event_id) OR
				((SELECT needs_input FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id)=1 AND
				 NOT EXISTS(SELECT 1 FROM delivery_stage_blockers WHERE stage_event_id=NEW.semantic_stage_event_id
				  AND is_current=1 AND is_human_wait=1)) OR
				((SELECT semantic_state FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id)='succeeded' AND
				 NOT EXISTS(SELECT 1 FROM delivery_evidence WHERE stage_event_id=NEW.semantic_stage_event_id AND outcome='passed')) OR
				((SELECT semantic_state FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id)<>'succeeded' AND
				 EXISTS(SELECT 1 FROM delivery_evidence WHERE stage_event_id=NEW.semantic_stage_event_id AND outcome='passed'))
			 ) BEGIN SELECT RAISE(ABORT, 'delivery stage children are incomplete'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_latest_children_guard_update
			 BEFORE UPDATE ON delivery_stage_latest
			 WHEN NEW.semantic_stage_event_id IS NOT NULL AND (
				(SELECT declared_blocker_count FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id) <>
				 (SELECT COUNT(*) FROM delivery_stage_blockers WHERE stage_event_id=NEW.semantic_stage_event_id) OR
				(SELECT current_blocker_count FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id) <>
				 (SELECT COUNT(*) FROM delivery_stage_blockers WHERE stage_event_id=NEW.semantic_stage_event_id AND is_current=1) OR
				(SELECT declared_evidence_count FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id) <>
				 (SELECT COUNT(*) FROM delivery_evidence WHERE stage_event_id=NEW.semantic_stage_event_id) OR
				((SELECT needs_input FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id)=1 AND
				 NOT EXISTS(SELECT 1 FROM delivery_stage_blockers WHERE stage_event_id=NEW.semantic_stage_event_id
				  AND is_current=1 AND is_human_wait=1)) OR
				((SELECT semantic_state FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id)='succeeded' AND
				 NOT EXISTS(SELECT 1 FROM delivery_evidence WHERE stage_event_id=NEW.semantic_stage_event_id AND outcome='passed')) OR
				((SELECT semantic_state FROM delivery_stage_events WHERE id=NEW.semantic_stage_event_id)<>'succeeded' AND
				 EXISTS(SELECT 1 FROM delivery_evidence WHERE stage_event_id=NEW.semantic_stage_event_id AND outcome='passed'))
			 ) BEGIN SELECT RAISE(ABORT, 'delivery stage children are incomplete'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_latest_pointer_guard_insert
			 BEFORE INSERT ON delivery_stage_latest
			 WHEN NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se
				WHERE se.id=NEW.execution_start_stage_event_id AND se.delivery_id=NEW.delivery_id
				  AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				  AND se.execution_number=NEW.execution_number AND se.event_type='execution_started'
			 ) OR NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.authority_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND ((se.event_type='progress_reset_authorized' AND se.reset_owner_reporter_id=NEW.current_reporter_id)
				  OR (se.event_type<>'progress_reset_authorized' AND se.reporter_id=NEW.current_reporter_id))
				 AND se.event_type IN ('execution_started','handoff','progress_reset_authorized')
			 ) OR (NEW.semantic_stage_event_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.semantic_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND se.reporter_id=NEW.current_reporter_id AND se.event_type IN ('semantic_report','lifecycle_normalized')
			 )) OR (NEW.heartbeat_stage_event_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.heartbeat_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND se.reporter_id=NEW.current_reporter_id AND se.event_type='heartbeat'
			 )) OR (NEW.estimate_stage_event_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.estimate_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND se.reporter_id=NEW.current_reporter_id AND se.event_type='estimate'
			 )) BEGIN SELECT RAISE(ABORT, 'delivery stage latest has invalid pointer'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_latest_pointer_guard_update
			 BEFORE UPDATE ON delivery_stage_latest
			 WHEN NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se
				WHERE se.id=NEW.execution_start_stage_event_id AND se.delivery_id=NEW.delivery_id
				  AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				  AND se.execution_number=NEW.execution_number AND se.event_type='execution_started'
			 ) OR NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.authority_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND ((se.event_type='progress_reset_authorized' AND se.reset_owner_reporter_id=NEW.current_reporter_id)
				  OR (se.event_type<>'progress_reset_authorized' AND se.reporter_id=NEW.current_reporter_id))
				 AND se.event_type IN ('execution_started','handoff','progress_reset_authorized')
			 ) OR (NEW.semantic_stage_event_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.semantic_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND se.reporter_id=NEW.current_reporter_id AND se.event_type IN ('semantic_report','lifecycle_normalized')
			 )) OR (NEW.heartbeat_stage_event_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.heartbeat_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND se.reporter_id=NEW.current_reporter_id AND se.event_type='heartbeat'
			 )) OR (NEW.estimate_stage_event_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.estimate_stage_event_id
				 AND se.delivery_id=NEW.delivery_id AND se.attempt_id=NEW.attempt_id AND se.stage_key=NEW.stage_key
				 AND se.execution_number=NEW.execution_number AND se.authority_epoch=NEW.authority_epoch
				 AND se.reporter_id=NEW.current_reporter_id AND se.event_type='estimate'
			 )) BEGIN SELECT RAISE(ABORT, 'delivery stage latest has invalid pointer'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_blocker_declared_ordinal
			 BEFORE INSERT ON delivery_stage_blockers
			 WHEN NEW.ordinal >= (SELECT declared_blocker_count FROM delivery_stage_events WHERE id=NEW.stage_event_id)
			 BEGIN SELECT RAISE(ABORT, 'delivery blocker exceeds declared count'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_declared_ordinal
				 BEFORE INSERT ON delivery_evidence
				 WHEN NEW.ordinal >= (SELECT declared_evidence_count FROM delivery_stage_events WHERE id=NEW.stage_event_id)
				 BEGIN SELECT RAISE(ABORT, 'delivery evidence exceeds declared count'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_no_terminal_conflict
				 BEFORE INSERT ON delivery_evidence
				 WHEN NEW.outcome IN ('passed','failed') AND EXISTS (
					SELECT 1 FROM delivery_evidence e
					WHERE e.stage_event_id=NEW.stage_event_id
					  AND e.outcome IN ('passed','failed') AND e.outcome<>NEW.outcome
				 ) BEGIN SELECT RAISE(ABORT, 'delivery terminal evidence is contradictory'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_terminal_state_coherence
				 BEFORE INSERT ON delivery_evidence WHEN EXISTS (
					SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.stage_event_id AND (
					 (NEW.outcome='passed' AND se.semantic_state<>'succeeded') OR
					 (NEW.outcome='failed' AND se.semantic_state='succeeded')
					)
				 ) BEGIN SELECT RAISE(ABORT, 'delivery evidence contradicts terminal state'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_stage_matrix
				 BEFORE INSERT ON delivery_evidence WHEN NOT EXISTS (
					SELECT 1 FROM delivery_stage_events se WHERE se.id=NEW.stage_event_id AND (
					 (se.stage_key='specification' AND NEW.evidence_type IN ('spec_acceptance','approval')) OR
					 (se.stage_key='implementation' AND NEW.evidence_type IN ('implementation_result','artifact')) OR
					 (se.stage_key='qa' AND NEW.evidence_type='test_result') OR
					 (se.stage_key='deployment' AND NEW.evidence_type='deployment_result') OR
					 (se.stage_key='verification' AND NEW.evidence_type='verification_result'))
				 ) BEGIN SELECT RAISE(ABORT, 'delivery evidence type does not match stage'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_proof_bearing
				 BEFORE INSERT ON delivery_evidence WHEN NEW.outcome='passed' AND (
					NEW.reference_kind='none' OR
					(NEW.reference_kind='external_ref' AND NEW.reference_value='') OR
					(NEW.reference_kind='commit' AND (length(CAST(NEW.reference_value AS BLOB)) NOT IN (40,64)
					 OR NEW.reference_value GLOB '*[^0-9a-f]*')) OR
					(NEW.reference_kind='digest' AND NEW.digest_sha256='')
				 ) BEGIN SELECT RAISE(ABORT, 'delivery passed evidence is not proof-bearing'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_forbidden_patterns_no_update
				 BEFORE UPDATE ON delivery_forbidden_value_patterns
				 BEGIN SELECT RAISE(ABORT, 'delivery forbidden patterns are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_forbidden_patterns_no_delete
				 BEFORE DELETE ON delivery_forbidden_value_patterns
				 BEGIN SELECT RAISE(ABORT, 'delivery forbidden patterns are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_forbidden_patterns_no_insert
				 BEFORE INSERT ON delivery_forbidden_value_patterns WHEN NOT EXISTS(
				  SELECT 1 FROM delivery_forbidden_value_patterns existing WHERE existing.pattern=NEW.pattern
				   AND existing.normalize_horizontal_whitespace=NEW.normalize_horizontal_whitespace
				   AND existing.case_sensitive=NEW.case_sensitive AND existing.boundary_needle=NEW.boundary_needle
				   AND existing.require_bearer_whitespace=NEW.require_bearer_whitespace)
				 BEGIN SELECT RAISE(ABORT, 'delivery forbidden patterns are migration-owned'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reporter_secret_guard
				 BEFORE INSERT ON delivery_reporters WHEN EXISTS (
				  SELECT 1 FROM json_each(json_array(NEW.opaque_key)) value
				  WHERE instr(CAST(value.value AS TEXT),char(0))>0 OR instr(CAST(value.value AS TEXT),char(10))>0
				   OR instr(CAST(value.value AS TEXT),char(13))>0 OR EXISTS (
				    SELECT 1 FROM delivery_forbidden_value_patterns forbidden
				    WHERE (CASE WHEN forbidden.case_sensitive=1 THEN
								      CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END
								     ELSE lower(CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END) END) GLOB forbidden.pattern
								     AND (forbidden.boundary_needle='' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB forbidden.boundary_needle||'*' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB
								       '*[^0-9A-Za-z_]'||forbidden.boundary_needle||'*')
								     AND (forbidden.require_bearer_whitespace=0 OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       'bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*' OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       '*[^0-9a-z_]bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*'))
				   OR instr(CAST(value.value AS TEXT),'?')>0
				   OR (instr(CAST(value.value AS TEXT),'://')>0 AND instr(CAST(value.value AS TEXT),'@')>0)
				 ) BEGIN SELECT RAISE(ABORT, 'forbidden delivery reporter value'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_event_secret_guard
				 BEFORE INSERT ON delivery_events WHEN EXISTS (
				  SELECT 1 FROM json_each(json_array(NEW.idempotency_key,NEW.reason_code,NEW.reason_text)) value
				  WHERE instr(CAST(value.value AS TEXT),char(0))>0 OR instr(CAST(value.value AS TEXT),char(10))>0
				   OR instr(CAST(value.value AS TEXT),char(13))>0 OR EXISTS (
				    SELECT 1 FROM delivery_forbidden_value_patterns forbidden
				    WHERE (CASE WHEN forbidden.case_sensitive=1 THEN
								      CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END
								     ELSE lower(CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END) END) GLOB forbidden.pattern
								     AND (forbidden.boundary_needle='' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB forbidden.boundary_needle||'*' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB
								       '*[^0-9A-Za-z_]'||forbidden.boundary_needle||'*')
								     AND (forbidden.require_bearer_whitespace=0 OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       'bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*' OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       '*[^0-9a-z_]bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*'))
				 ) BEGIN SELECT RAISE(ABORT, 'forbidden delivery event value'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_attempt_secret_guard
				 BEFORE INSERT ON delivery_attempts WHEN EXISTS (
				  SELECT 1 FROM json_each(json_array(NEW.reason_code,NEW.reason_text)) value
				  WHERE instr(CAST(value.value AS TEXT),char(0))>0 OR instr(CAST(value.value AS TEXT),char(10))>0
				   OR instr(CAST(value.value AS TEXT),char(13))>0 OR EXISTS (
				    SELECT 1 FROM delivery_forbidden_value_patterns forbidden
				    WHERE (CASE WHEN forbidden.case_sensitive=1 THEN
								      CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END
								     ELSE lower(CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END) END) GLOB forbidden.pattern
								     AND (forbidden.boundary_needle='' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB forbidden.boundary_needle||'*' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB
								       '*[^0-9A-Za-z_]'||forbidden.boundary_needle||'*')
								     AND (forbidden.require_bearer_whitespace=0 OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       'bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*' OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       '*[^0-9a-z_]bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*'))
				 ) BEGIN SELECT RAISE(ABORT, 'forbidden delivery attempt value'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_policy_secret_guard
				 BEFORE INSERT ON delivery_attempt_stage_policy WHEN EXISTS (
				  SELECT 1 FROM json_each(json_array(NEW.policy_reference,NEW.reason_code,NEW.reason_text)) value
				  WHERE instr(CAST(value.value AS TEXT),char(0))>0 OR instr(CAST(value.value AS TEXT),char(10))>0
				   OR instr(CAST(value.value AS TEXT),char(13))>0 OR EXISTS (
				    SELECT 1 FROM delivery_forbidden_value_patterns forbidden
				    WHERE (CASE WHEN forbidden.case_sensitive=1 THEN
								      CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END
								     ELSE lower(CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END) END) GLOB forbidden.pattern
								     AND (forbidden.boundary_needle='' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB forbidden.boundary_needle||'*' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB
								       '*[^0-9A-Za-z_]'||forbidden.boundary_needle||'*')
								     AND (forbidden.require_bearer_whitespace=0 OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       'bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*' OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       '*[^0-9a-z_]bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*'))
				   OR (value.value=NEW.policy_reference AND (instr(CAST(value.value AS TEXT),'?')>0 OR
				    (instr(CAST(value.value AS TEXT),'://')>0 AND instr(CAST(value.value AS TEXT),'@')>0)))
				 ) BEGIN SELECT RAISE(ABORT, 'forbidden delivery policy value'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_secret_guard
				 BEFORE INSERT ON delivery_stage_events WHEN EXISTS (
				  SELECT 1 FROM json_each(json_array(COALESCE(NEW.source_idempotency_key,''),NEW.activity,
				   NEW.estimate_basis,NEW.reason_code,NEW.reason_text)) value
				  WHERE instr(CAST(value.value AS TEXT),char(0))>0 OR instr(CAST(value.value AS TEXT),char(10))>0
				   OR instr(CAST(value.value AS TEXT),char(13))>0 OR EXISTS (
				    SELECT 1 FROM delivery_forbidden_value_patterns forbidden
				    WHERE (CASE WHEN forbidden.case_sensitive=1 THEN
								      CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END
								     ELSE lower(CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END) END) GLOB forbidden.pattern
								     AND (forbidden.boundary_needle='' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB forbidden.boundary_needle||'*' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB
								       '*[^0-9A-Za-z_]'||forbidden.boundary_needle||'*')
								     AND (forbidden.require_bearer_whitespace=0 OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       'bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*' OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       '*[^0-9a-z_]bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*'))
				 ) BEGIN SELECT RAISE(ABORT, 'forbidden delivery stage value'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_blocker_secret_guard
				 BEFORE INSERT ON delivery_stage_blockers WHEN EXISTS (
				  SELECT 1 FROM json_each(json_array(NEW.blocker_key,NEW.summary)) value
				  WHERE instr(CAST(value.value AS TEXT),char(0))>0 OR instr(CAST(value.value AS TEXT),char(10))>0
				   OR instr(CAST(value.value AS TEXT),char(13))>0 OR EXISTS (
				    SELECT 1 FROM delivery_forbidden_value_patterns forbidden
				    WHERE (CASE WHEN forbidden.case_sensitive=1 THEN
								      CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END
								     ELSE lower(CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END) END) GLOB forbidden.pattern
								     AND (forbidden.boundary_needle='' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB forbidden.boundary_needle||'*' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB
								       '*[^0-9A-Za-z_]'||forbidden.boundary_needle||'*')
								     AND (forbidden.require_bearer_whitespace=0 OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       'bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*' OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       '*[^0-9a-z_]bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*'))
				 ) BEGIN SELECT RAISE(ABORT, 'forbidden delivery blocker value'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_secret_guard
				 BEFORE INSERT ON delivery_evidence WHEN EXISTS (
				  SELECT 1 FROM json_each(json_array(NEW.reference_value)) value
				  WHERE instr(CAST(value.value AS TEXT),char(0))>0 OR instr(CAST(value.value AS TEXT),char(10))>0
				   OR instr(CAST(value.value AS TEXT),char(13))>0 OR EXISTS (
				    SELECT 1 FROM delivery_forbidden_value_patterns forbidden
				    WHERE (CASE WHEN forbidden.case_sensitive=1 THEN
								      CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END
								     ELSE lower(CASE WHEN forbidden.normalize_horizontal_whitespace=1
								       THEN replace(replace(replace(replace(replace(replace(replace(replace(replace(replace(CAST(value.value AS TEXT),' ',''),char(9),''),char(12),''),char(11),''),char(10),''),char(13),''),char(160),''),char(8195),''),char(8239),''),char(8203),'')
								       ELSE CAST(value.value AS TEXT) END) END) GLOB forbidden.pattern
								     AND (forbidden.boundary_needle='' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB forbidden.boundary_needle||'*' OR
								      (CASE WHEN forbidden.case_sensitive=1 THEN CAST(value.value AS TEXT)
								       ELSE lower(CAST(value.value AS TEXT)) END) GLOB
								       '*[^0-9A-Za-z_]'||forbidden.boundary_needle||'*')
								     AND (forbidden.require_bearer_whitespace=0 OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       'bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*' OR
								      lower(CAST(value.value AS TEXT)) GLOB
								       '*[^0-9a-z_]bearer['||' '||char(9)||char(12)||char(11)||char(10)||char(13)||char(160)||char(8195)||char(8239)||char(8203)||']*'))
				   OR instr(CAST(value.value AS TEXT),'?')>0
				   OR (instr(CAST(value.value AS TEXT),'://')>0 AND instr(CAST(value.value AS TEXT),'@')>0)
				 ) BEGIN SELECT RAISE(ABORT, 'forbidden delivery evidence value'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_policy_complete
			 BEFORE INSERT ON delivery_stage_events
			 WHEN NEW.event_type='execution_started' AND (
				NOT EXISTS(SELECT 1 FROM delivery_attempt_policy_seals seal
				 WHERE seal.delivery_id=NEW.delivery_id AND seal.attempt_id=NEW.attempt_id)
				 ) BEGIN SELECT RAISE(ABORT, 'delivery attempt policy is incomplete'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_policy_applicable
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='execution_started'
				  AND COALESCE((SELECT policy.applicability FROM delivery_attempt_stage_policy policy
				   WHERE policy.delivery_id=NEW.delivery_id AND policy.attempt_id=NEW.attempt_id
				    AND policy.stage_key=NEW.stage_key),'')<>'required'
				 BEGIN SELECT RAISE(ABORT, 'delivery execution cannot start for an inapplicable stage'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_attempt_envelope_kind
				 BEFORE INSERT ON delivery_attempts WHEN COALESCE((SELECT event.kind FROM delivery_events event
				  WHERE event.delivery_id=NEW.delivery_id AND event.id=NEW.start_delivery_event_id),'')<>'attempt_started'
				 BEGIN SELECT RAISE(ABORT, 'delivery attempt start envelope kind is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_envelope_kind
				 BEFORE INSERT ON delivery_stage_events WHEN
				  (NEW.event_type='heartbeat' AND NEW.delivery_event_id IS NOT NULL) OR
				  (NEW.event_type<>'heartbeat' AND COALESCE((SELECT event.kind FROM delivery_events event
				   WHERE event.delivery_id=NEW.delivery_id AND event.id=NEW.delivery_event_id),'')<>
				   CASE NEW.event_type
				    WHEN 'execution_started' THEN 'stage_execution_started'
				    WHEN 'semantic_report' THEN 'stage_reported'
				    WHEN 'estimate' THEN 'stage_reported'
				    WHEN 'handoff' THEN 'handoff'
				    WHEN 'progress_reset_authorized' THEN 'progress_reset_authorized'
				    WHEN 'lifecycle_normalized' THEN 'run_normalized'
				   END)
				 BEGIN SELECT RAISE(ABORT, 'delivery stage envelope kind is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_run_link_envelope_kind
				 BEFORE INSERT ON delivery_agent_run_links WHEN COALESCE((SELECT event.kind FROM delivery_events event
				  WHERE event.delivery_id=NEW.delivery_id AND event.id=NEW.link_delivery_event_id),'')<>'run_linked'
				 BEGIN SELECT RAISE(ABORT, 'delivery run link envelope kind is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_policy_stage_order
			 BEFORE INSERT ON delivery_attempt_stage_policy WHEN NEW.sort_order<>CASE NEW.stage_key
			  WHEN 'specification' THEN 1 WHEN 'implementation' THEN 2 WHEN 'qa' THEN 3
			  WHEN 'deployment' THEN 4 WHEN 'verification' THEN 5 END
			 BEGIN SELECT RAISE(ABORT, 'delivery policy stage order is not canonical'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_attempt_policy_seal_valid
			 BEFORE INSERT ON delivery_attempt_policy_seals WHEN
			  (SELECT COUNT(*) FROM delivery_attempt_stage_policy p
			   WHERE p.delivery_id=NEW.delivery_id AND p.attempt_id=NEW.attempt_id)<>5 OR
			  (SELECT COUNT(*) FROM delivery_attempt_stage_policy p
			   WHERE p.delivery_id=NEW.delivery_id AND p.attempt_id=NEW.attempt_id
			    AND p.applicability='required')=0 OR
			  (SELECT COALESCE(SUM(p.weight),0) FROM delivery_attempt_stage_policy p
			   WHERE p.delivery_id=NEW.delivery_id AND p.attempt_id=NEW.attempt_id)<>100 OR
			  EXISTS(SELECT 1 FROM delivery_attempt_stage_policy p
			   WHERE p.delivery_id=NEW.delivery_id AND p.attempt_id=NEW.attempt_id
			    AND p.sort_order<>CASE p.stage_key WHEN 'specification' THEN 1 WHEN 'implementation' THEN 2
			     WHEN 'qa' THEN 3 WHEN 'deployment' THEN 4 WHEN 'verification' THEN 5 END)
			 BEGIN SELECT RAISE(ABORT, 'delivery attempt policy cannot be sealed'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_external_stage_source_sequence
				 BEFORE INSERT ON delivery_stage_events
				 WHEN NEW.event_type IN ('semantic_report','heartbeat','estimate')
				  AND NEW.source_sequence IS NULL
				  AND EXISTS(SELECT 1 FROM delivery_reporters r WHERE r.id=NEW.reporter_id
				   AND r.delivery_id=NEW.delivery_id AND r.reporter_type='external')
				 BEGIN SELECT RAISE(ABORT, 'external stage fact requires source sequence'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_external_stage_source_monotone
				 BEFORE INSERT ON delivery_stage_events
				 WHEN NEW.event_type IN ('semantic_report','heartbeat','estimate')
				  AND EXISTS(SELECT 1 FROM delivery_reporters r WHERE r.id=NEW.reporter_id
				   AND r.delivery_id=NEW.delivery_id AND r.reporter_type='external')
				  AND (NEW.source_sequence IS NULL OR NEW.source_sequence<=COALESCE((SELECT MAX(prior.source_sequence)
				   FROM delivery_stage_events prior WHERE prior.attempt_id=NEW.attempt_id
				    AND prior.stage_key=NEW.stage_key AND prior.execution_number=NEW.execution_number
				    AND prior.authority_epoch=NEW.authority_epoch AND prior.reporter_id=NEW.reporter_id),0))
				 BEGIN SELECT RAISE(ABORT, 'external stage source sequence is not increasing'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_estimate_revision_monotone
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='estimate' AND (
				  NEW.estimate_revision IS NULL OR NEW.estimate_revision<COALESCE((SELECT MAX(prior.estimate_revision)
				   FROM delivery_stage_events prior WHERE prior.attempt_id=NEW.attempt_id
				    AND prior.stage_key=NEW.stage_key AND prior.execution_number=NEW.execution_number
				    AND prior.authority_epoch=NEW.authority_epoch AND prior.reporter_id=NEW.reporter_id
				    AND prior.event_type='estimate'),0) OR
				  EXISTS(SELECT 1 FROM delivery_stage_events prior WHERE prior.id=(SELECT latest.id
				   FROM delivery_stage_events latest WHERE latest.attempt_id=NEW.attempt_id
				    AND latest.stage_key=NEW.stage_key AND latest.execution_number=NEW.execution_number
				    AND latest.authority_epoch=NEW.authority_epoch AND latest.reporter_id=NEW.reporter_id
				    AND latest.event_type='estimate' AND latest.estimate_revision=NEW.estimate_revision
				   ORDER BY latest.event_sequence DESC LIMIT 1)
				   AND (prior.progress_percent IS NOT NEW.progress_percent OR prior.eta_seconds IS NOT NEW.eta_seconds
				    OR prior.eta_min_seconds IS NOT NEW.eta_min_seconds OR prior.eta_max_seconds IS NOT NEW.eta_max_seconds
				    OR prior.estimate_source<>NEW.estimate_source OR prior.estimate_confidence IS NOT NEW.estimate_confidence
				    OR prior.estimate_basis<>NEW.estimate_basis))
				 ) BEGIN SELECT RAISE(ABORT, 'delivery estimate revision is stale or changed'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_external_source_activation_cutoff
				 BEFORE INSERT ON delivery_stage_events
				 WHEN NEW.source_sequence IS NOT NULL
				  AND EXISTS(SELECT 1 FROM delivery_reporters r WHERE r.id=NEW.reporter_id
				   AND r.delivery_id=NEW.delivery_id AND r.reporter_type='external')
				  AND NEW.source_sequence<=COALESCE((SELECT owner.authority_source_sequence_cutoff
				   FROM delivery_stage_events owner WHERE owner.attempt_id=NEW.attempt_id
				    AND owner.stage_key=NEW.stage_key AND owner.execution_number=NEW.execution_number
				    AND owner.authority_epoch=NEW.authority_epoch AND owner.reporter_id=NEW.reporter_id
				    AND owner.event_type IN ('execution_started','handoff')
				   ORDER BY owner.event_sequence DESC LIMIT 1),0)
				 BEGIN SELECT RAISE(ABORT, 'external stage fact predates authority activation'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_succeeded_requires_declared_evidence
				 BEFORE INSERT ON delivery_stage_events
				 WHEN NEW.event_type IN ('semantic_report','lifecycle_normalized')
				  AND NEW.semantic_state='succeeded'
				  AND (NEW.declared_evidence_count=0 OR NEW.current_blocker_count<>0 OR NEW.needs_input<>0)
				 BEGIN SELECT RAISE(ABORT, 'successful stage fact requires evidence and no current blocker'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_run_activation_authority_kind
				 BEFORE INSERT ON delivery_agent_run_activations
				 WHEN COALESCE((SELECT event_type FROM delivery_stage_events WHERE id=NEW.authority_stage_event_id),'')
				  NOT IN ('execution_started','handoff')
				 BEGIN SELECT RAISE(ABORT, 'delivery run activation authority is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_run_activation_exact_cutoff
				 BEFORE INSERT ON delivery_agent_run_activations
				 WHEN NEW.telemetry_sequence_cutoff IS NULL OR
				  NEW.telemetry_sequence_cutoff<>COALESCE((SELECT MAX(t.sequence)
				  FROM agent_run_telemetry t WHERE t.run_id=NEW.agent_run_id),0)
				 BEGIN SELECT RAISE(ABORT, 'delivery run activation cutoff is not the ledger high-water'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_external_activation_exact_cutoff
				 BEFORE INSERT ON delivery_stage_events
				 WHEN NEW.event_type IN ('execution_started','handoff')
				  AND EXISTS(SELECT 1 FROM delivery_reporters r WHERE r.id=NEW.reporter_id
				   AND r.delivery_id=NEW.delivery_id AND r.reporter_type='external')
				  AND (NEW.authority_source_sequence_cutoff IS NULL OR
				   NEW.authority_source_sequence_cutoff<>COALESCE((SELECT MAX(prior.source_sequence)
				   FROM delivery_stage_events prior WHERE prior.attempt_id=NEW.attempt_id
				    AND prior.stage_key=NEW.stage_key AND prior.execution_number=NEW.execution_number
				    AND prior.reporter_id=NEW.reporter_id),0))
				 BEGIN SELECT RAISE(ABORT, 'external authority activation cutoff is not the ledger high-water'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_execution_terminal_seal
				 BEFORE INSERT ON delivery_stage_events
				 WHEN EXISTS (
					SELECT 1 FROM delivery_stage_events prior
					WHERE prior.attempt_id=NEW.attempt_id AND prior.stage_key=NEW.stage_key
					  AND prior.execution_number=NEW.execution_number
					  AND prior.event_type IN ('semantic_report','lifecycle_normalized')
					  AND prior.semantic_state IN ('succeeded','failed','cancelled','draft_ready')
				 ) BEGIN SELECT RAISE(ABORT, 'delivery stage execution is terminal'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reset_authority_kind
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='progress_reset_authorized'
				  AND COALESCE((SELECT event_type FROM delivery_stage_events
				   WHERE id=NEW.reset_authority_anchor_stage_event_id),'') NOT IN ('execution_started','handoff')
				 BEGIN SELECT RAISE(ABORT, 'delivery reset authority anchor is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reset_current_authority
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='progress_reset_authorized'
				  AND NOT EXISTS(SELECT 1 FROM delivery_stage_latest latest
				   WHERE latest.delivery_id=NEW.delivery_id AND latest.attempt_id=NEW.attempt_id
				    AND latest.stage_key=NEW.stage_key AND latest.execution_number=NEW.execution_number
				    AND latest.authority_epoch=NEW.authority_epoch
				    AND latest.current_reporter_id=NEW.reset_owner_reporter_id
				    AND latest.execution_start_stage_event_id=NEW.execution_start_stage_event_id
				    AND latest.authority_stage_event_id=NEW.previous_stage_event_id)
				 BEGIN SELECT RAISE(ABORT, 'delivery reset does not target current authority'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reset_exact_epoch
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='progress_reset_authorized'
				  AND (NEW.reset_epoch IS NULL OR NEW.reset_epoch<>COALESCE((SELECT MAX(prior.reset_epoch)+1
				   FROM delivery_stage_events prior WHERE prior.attempt_id=NEW.attempt_id
				    AND prior.stage_key=NEW.stage_key AND prior.execution_number=NEW.execution_number),1))
				 BEGIN SELECT RAISE(ABORT, 'delivery reset epoch is not the next execution epoch'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reset_exact_stage_cutoff
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='progress_reset_authorized'
				  AND (NEW.reset_source_cutoff IS NULL OR NEW.reset_source_cutoff<>COALESCE((SELECT MAX(prior.source_sequence)
				   FROM delivery_stage_events prior WHERE prior.attempt_id=NEW.attempt_id
				    AND prior.stage_key=NEW.stage_key AND prior.execution_number=NEW.execution_number
				    AND prior.authority_epoch=NEW.authority_epoch AND prior.reporter_id=NEW.reset_owner_reporter_id),0))
				 BEGIN SELECT RAISE(ABORT, 'delivery reset stage cutoff is not the ledger high-water'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reset_source_kind
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='progress_reset_authorized'
				  AND ((EXISTS(SELECT 1 FROM delivery_reporters owner WHERE owner.delivery_id=NEW.delivery_id
				    AND owner.id=NEW.reset_owner_reporter_id AND owner.reporter_type='agent_run')
				   AND NEW.reset_source_kind<>'stage_and_agent_run_telemetry') OR
				   (EXISTS(SELECT 1 FROM delivery_reporters owner WHERE owner.delivery_id=NEW.delivery_id
				    AND owner.id=NEW.reset_owner_reporter_id AND owner.reporter_type<>'agent_run')
				   AND NEW.reset_source_kind<>'stage_events'))
				 BEGIN SELECT RAISE(ABORT, 'delivery reset source kind does not match current owner'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reset_exact_telemetry_cutoff
				 BEFORE INSERT ON delivery_stage_events WHEN NEW.event_type='progress_reset_authorized'
				  AND NEW.reset_source_kind='stage_and_agent_run_telemetry'
				  AND (NEW.reset_telemetry_run_id IS NULL OR NEW.reset_telemetry_sequence_cutoff IS NULL OR
				   NOT EXISTS(SELECT 1 FROM delivery_agent_run_activations activation
				    WHERE activation.delivery_id=NEW.delivery_id AND activation.attempt_id=NEW.attempt_id
				     AND activation.stage_key=NEW.stage_key AND activation.execution_number=NEW.execution_number
				     AND activation.authority_epoch=NEW.authority_epoch
				     AND activation.agent_run_id=NEW.reset_telemetry_run_id
				     AND activation.reporter_id=NEW.reset_owner_reporter_id
				     AND activation.authority_stage_event_id=NEW.reset_authority_anchor_stage_event_id) OR
				   NEW.reset_telemetry_sequence_cutoff<>COALESCE((SELECT MAX(t.sequence) FROM agent_run_telemetry t
				    WHERE t.run_id=NEW.reset_telemetry_run_id),0))
				 BEGIN SELECT RAISE(ABORT, 'delivery reset telemetry cutoff is not the ledger high-water'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_duration_exact_success
				 BEFORE INSERT ON delivery_stage_durations WHEN NOT EXISTS(
				  SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id
				  JOIN delivery_attempt_policy_seals seal ON seal.delivery_id=d.id AND seal.attempt_id=NEW.attempt_id
				  JOIN delivery_attempt_stage_policy policy ON policy.delivery_id=d.id
				   AND policy.attempt_id=NEW.attempt_id AND policy.stage_key=NEW.stage_key
				  JOIN delivery_stage_latest latest ON latest.delivery_id=d.id AND latest.attempt_id=NEW.attempt_id
				   AND latest.stage_key=NEW.stage_key
				  JOIN delivery_stage_events start ON start.id=NEW.stage_execution_id
				  JOIN delivery_stage_events terminal ON terminal.id=NEW.terminal_stage_event_id
				  WHERE d.id=NEW.delivery_id AND d.issue_id=NEW.root_issue_id
				   AND i.project_id IS NEW.project_id_at_completion AND policy.applicability='required'
				   AND latest.execution_number=NEW.execution_number
				   AND latest.execution_start_stage_event_id=NEW.stage_execution_id
				   AND latest.semantic_stage_event_id=NEW.terminal_stage_event_id
				   AND start.delivery_id=NEW.delivery_id AND start.attempt_id=NEW.attempt_id
				   AND start.stage_key=NEW.stage_key AND start.execution_number=NEW.execution_number
				   AND start.event_type='execution_started'
				   AND terminal.delivery_id=NEW.delivery_id AND terminal.attempt_id=NEW.attempt_id
				   AND terminal.stage_key=NEW.stage_key AND terminal.execution_number=NEW.execution_number
				   AND terminal.event_type IN ('semantic_report','lifecycle_normalized')
				   AND terminal.semantic_state='succeeded' AND terminal.current_blocker_count=0
				   AND terminal.needs_input=0 AND terminal.authority_epoch=latest.authority_epoch
				   AND terminal.reporter_id=latest.current_reporter_id
				   AND terminal.ended_at=NEW.completed_at AND terminal.server_received_at=NEW.completed_at
				   AND (NEW.stage_key<>'specification' OR terminal.spec_revision=d.spec_revision)
				   AND EXISTS(SELECT 1 FROM delivery_evidence evidence
				    WHERE evidence.stage_event_id=terminal.id AND evidence.outcome='passed')
				   AND NOT EXISTS(SELECT 1 FROM delivery_evidence evidence
				    WHERE evidence.stage_event_id=terminal.id AND evidence.outcome='failed')
				   AND NOT EXISTS(
				    SELECT 1 FROM delivery_attempt_stage_policy chain_policy
				    WHERE chain_policy.attempt_id=NEW.attempt_id AND chain_policy.applicability='required'
				     AND chain_policy.sort_order<=policy.sort_order AND NOT EXISTS(
				      SELECT 1 FROM delivery_stage_latest chain_latest
				      JOIN delivery_stage_events chain_start ON chain_start.id=chain_latest.execution_start_stage_event_id
				      JOIN delivery_stage_events chain_terminal ON chain_terminal.id=chain_latest.semantic_stage_event_id
				      WHERE chain_latest.delivery_id=NEW.delivery_id AND chain_latest.attempt_id=NEW.attempt_id
				       AND chain_latest.stage_key=chain_policy.stage_key
				       AND chain_terminal.semantic_state='succeeded' AND chain_terminal.current_blocker_count=0
				       AND chain_terminal.needs_input=0
				       AND chain_terminal.authority_epoch=chain_latest.authority_epoch
				       AND chain_terminal.reporter_id=chain_latest.current_reporter_id
				       AND (chain_policy.stage_key<>'specification' OR chain_terminal.spec_revision=d.spec_revision)
				       AND chain_start.based_on_stage_event_id IS (
				        SELECT predecessor_terminal.id FROM delivery_attempt_stage_policy predecessor_policy
				        JOIN delivery_stage_latest predecessor_latest ON predecessor_latest.attempt_id=predecessor_policy.attempt_id
				         AND predecessor_latest.stage_key=predecessor_policy.stage_key
				        JOIN delivery_stage_events predecessor_terminal ON predecessor_terminal.id=predecessor_latest.semantic_stage_event_id
				        WHERE predecessor_policy.attempt_id=chain_policy.attempt_id
				         AND predecessor_policy.applicability='required'
				         AND predecessor_policy.sort_order<chain_policy.sort_order
				        ORDER BY predecessor_policy.sort_order DESC LIMIT 1)
				       AND EXISTS(SELECT 1 FROM delivery_evidence evidence
				        WHERE evidence.stage_event_id=chain_terminal.id AND evidence.outcome='passed')
				       AND NOT EXISTS(SELECT 1 FROM delivery_evidence evidence
				        WHERE evidence.stage_event_id=chain_terminal.id AND evidence.outcome='failed')
				     )
				   )
				 ) BEGIN SELECT RAISE(ABORT, 'delivery duration lacks exact eligible terminal lineage'); END`,

			`CREATE TRIGGER IF NOT EXISTS trg_delivery_issue_update_change
				 AFTER UPDATE ON issues WHEN EXISTS(SELECT 1 FROM deliveries WHERE issue_id=NEW.id)
				 BEGIN
				UPDATE deliveries SET project_id_hint=NEW.project_id,
				 updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
				 WHERE issue_id=NEW.id AND NEW.project_id IS NOT OLD.project_id;
				UPDATE deliveries SET spec_revision=spec_revision+1,
				 updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
				 WHERE issue_id=NEW.id AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
				  OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
				INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,payload_hash,kind,
				 reporter_id,reason_code,reason_text,server_received_at)
				SELECT d.id,COALESCE((SELECT MAX(delivery_revision)+1 FROM delivery_events WHERE delivery_id=d.id),1),
				 'spec-revision:'||d.spec_revision,X'44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
				 'attempt_started',r.id,'spec_changed','Canonical issue specification changed',
				 strftime('%Y-%m-%dT%H:%M:%fZ','now')
				FROM deliveries d JOIN delivery_reporters r ON r.delivery_id=d.id
				 AND r.reporter_type='system' AND r.opaque_key='paimos'
				WHERE d.issue_id=NEW.id AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
				 OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
				INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,previous_attempt_id,
				 start_delivery_event_id,project_id_at_start,reason_code,reason_text,created_at)
				SELECT d.id,COALESCE((SELECT MAX(attempt_number)+1 FROM delivery_attempts WHERE delivery_id=d.id),1),
				 COALESCE((SELECT MAX(plan_revision)+1 FROM delivery_attempts WHERE delivery_id=d.id),1),
					 (SELECT a.id FROM delivery_attempts a JOIN delivery_attempt_policy_seals seal
					   ON seal.delivery_id=a.delivery_id AND seal.attempt_id=a.id
					  WHERE a.delivery_id=d.id ORDER BY a.attempt_number DESC LIMIT 1),
				 (SELECT id FROM delivery_events WHERE delivery_id=d.id ORDER BY delivery_revision DESC LIMIT 1),
				 NEW.project_id,'spec_changed','Canonical issue specification changed',strftime('%Y-%m-%dT%H:%M:%fZ','now')
				FROM deliveries d WHERE d.issue_id=NEW.id
				 AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
				  OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
				INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,sort_order,applicability,
				 weight,policy_reference,reason_code,reason_text,authorized_by_reporter_id,created_at)
				SELECT next.delivery_id,next.id,p.stage_key,p.sort_order,p.applicability,p.weight,p.policy_reference,
				 p.reason_code,p.reason_text,p.authorized_by_reporter_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				FROM delivery_attempts next JOIN delivery_attempt_stage_policy p ON p.attempt_id=next.previous_attempt_id
				 WHERE next.delivery_id=(SELECT id FROM deliveries WHERE issue_id=NEW.id)
					 AND next.attempt_number=(SELECT MAX(attempt_number) FROM delivery_attempts
					  WHERE delivery_id=next.delivery_id)
					 AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
					  OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
				INSERT INTO delivery_attempt_policy_seals(delivery_id,attempt_id,sealed_at)
				SELECT next.delivery_id,next.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				FROM delivery_attempts next WHERE next.delivery_id=(SELECT id FROM deliveries WHERE issue_id=NEW.id)
				 AND next.attempt_number=(SELECT MAX(attempt_number) FROM delivery_attempts
				  WHERE delivery_id=next.delivery_id)
				 AND (NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
				  OR NEW.acceptance_criteria IS NOT OLD.acceptance_criteria);
				INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				 change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,NEW.project_id,
				 d.change_sequence_high_water+1,
				 COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				 'issue','issue',NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				FROM deliveries d WHERE d.issue_id=NEW.id;
			 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_relation_insert_change
			 AFTER INSERT ON issue_relations
			 BEGIN
				INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				 change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
				 d.change_sequence_high_water+1,
				 COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				 'lane','relation',NEW.source_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				FROM deliveries d WHERE d.issue_id IN (NEW.source_id,NEW.target_id);
			 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_relation_delete_change
			 AFTER DELETE ON issue_relations
			 BEGIN
				INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				 change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
				 d.change_sequence_high_water+1,
				 COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				 'lane','relation',OLD.source_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				FROM deliveries d WHERE d.issue_id IN (OLD.source_id,OLD.target_id);
			 END`,

			`CREATE TRIGGER IF NOT EXISTS trg_deliveries_delete_tombstone
			 BEFORE DELETE ON deliveries
			 WHEN NOT EXISTS(SELECT 1 FROM issues WHERE id=OLD.issue_id)
			 BEGIN
				INSERT INTO delivery_change_log(
				 cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				 change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				VALUES(lower(hex(randomblob(16))),OLD.id,OLD.issue_id,OLD.delivery_key,OLD.project_id_hint,
				 OLD.change_sequence_high_water+1,
				 COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=OLD.id),0),
				 'root_deleted','issue',OLD.issue_id,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
			 END`,

			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reporters_no_update BEFORE UPDATE ON delivery_reporters BEGIN SELECT RAISE(ABORT,'delivery reporters are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_deliveries_identity_no_update BEFORE UPDATE ON deliveries
				 WHEN NEW.id IS NOT OLD.id OR NEW.issue_id IS NOT OLD.issue_id OR NEW.delivery_key IS NOT OLD.delivery_key
				  OR NEW.created_at IS NOT OLD.created_at
				 BEGIN SELECT RAISE(ABORT,'delivery identity is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_events_no_update BEFORE UPDATE ON delivery_events BEGIN SELECT RAISE(ABORT,'delivery events are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_attempts_no_update BEFORE UPDATE ON delivery_attempts BEGIN SELECT RAISE(ABORT,'delivery attempts are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_policy_no_update BEFORE UPDATE ON delivery_attempt_stage_policy BEGIN SELECT RAISE(ABORT,'delivery attempt policy is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_policy_seals_no_update BEFORE UPDATE ON delivery_attempt_policy_seals BEGIN SELECT RAISE(ABORT,'delivery attempt policy seal is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_events_no_update BEFORE UPDATE ON delivery_stage_events BEGIN SELECT RAISE(ABORT,'delivery stage events are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_blockers_no_update BEFORE UPDATE ON delivery_stage_blockers BEGIN SELECT RAISE(ABORT,'delivery blockers are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_no_update BEFORE UPDATE ON delivery_evidence BEGIN SELECT RAISE(ABORT,'delivery evidence is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_run_links_no_update BEFORE UPDATE ON delivery_agent_run_links BEGIN SELECT RAISE(ABORT,'delivery run links are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_run_activations_no_update BEFORE UPDATE ON delivery_agent_run_activations BEGIN SELECT RAISE(ABORT,'delivery run activations are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_durations_no_update BEFORE UPDATE ON delivery_stage_durations BEGIN SELECT RAISE(ABORT,'delivery durations are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_change_no_update BEFORE UPDATE ON delivery_change_log BEGIN SELECT RAISE(ABORT,'delivery change log is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_retention_no_update BEFORE UPDATE ON delivery_change_retention BEGIN SELECT RAISE(ABORT,'delivery retention history is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_retention_no_delete BEFORE DELETE ON delivery_change_retention BEGIN SELECT RAISE(ABORT,'delivery retention history is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_retention_monotone_insert
				 BEFORE INSERT ON delivery_change_retention WHEN
				  NOT EXISTS(SELECT 1 FROM delivery_change_retention existing WHERE existing.floor_id=NEW.floor_id)
				  AND (NEW.floor_id<=COALESCE((SELECT MAX(floor_id) FROM delivery_change_retention),-1) OR
				   NEW.floor_id>COALESCE((SELECT MAX(id) FROM delivery_change_log),0))
				 BEGIN SELECT RAISE(ABORT,'delivery retention floor is not a valid monotone prefix'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_deliveries_no_direct_delete BEFORE DELETE ON deliveries
			 WHEN EXISTS(SELECT 1 FROM issues WHERE id=OLD.issue_id)
			 BEGIN SELECT RAISE(ABORT,'deliveries cannot be deleted directly'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_reporters_no_direct_delete BEFORE DELETE ON delivery_reporters
			 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
			 BEGIN SELECT RAISE(ABORT,'delivery reporters are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_events_no_direct_delete BEFORE DELETE ON delivery_events
			 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
			 BEGIN SELECT RAISE(ABORT,'delivery events are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_attempts_no_direct_delete BEFORE DELETE ON delivery_attempts
			 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
			 BEGIN SELECT RAISE(ABORT,'delivery attempts are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_policy_no_direct_delete BEFORE DELETE ON delivery_attempt_stage_policy
				 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
				 BEGIN SELECT RAISE(ABORT,'delivery attempt policy is append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_policy_seals_no_direct_delete BEFORE DELETE ON delivery_attempt_policy_seals
				 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
				 BEGIN SELECT RAISE(ABORT,'delivery attempt policy seals are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_stage_events_no_direct_delete BEFORE DELETE ON delivery_stage_events
			 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
			 BEGIN SELECT RAISE(ABORT,'delivery stage events are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_blockers_no_direct_delete BEFORE DELETE ON delivery_stage_blockers
			 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
			 BEGIN SELECT RAISE(ABORT,'delivery blockers are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_evidence_no_direct_delete BEFORE DELETE ON delivery_evidence
			 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
			 BEGIN SELECT RAISE(ABORT,'delivery evidence is append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_run_links_no_direct_delete BEFORE DELETE ON delivery_agent_run_links
				 WHEN EXISTS(SELECT 1 FROM issues WHERE id=OLD.root_issue_id)
				 BEGIN SELECT RAISE(ABORT,'delivery run links are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_run_activations_no_direct_delete BEFORE DELETE ON delivery_agent_run_activations
				 WHEN EXISTS(SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id WHERE d.id=OLD.delivery_id)
				 BEGIN SELECT RAISE(ABORT,'delivery run activations are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_durations_no_direct_delete BEFORE DELETE ON delivery_stage_durations
			 WHEN EXISTS(SELECT 1 FROM issues WHERE id=OLD.root_issue_id)
			 BEGIN SELECT RAISE(ABORT,'delivery durations are append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_change_delete_guard BEFORE DELETE ON delivery_change_log
			 WHEN OLD.id > COALESCE((SELECT MAX(floor_id) FROM delivery_change_retention),-1)
			 BEGIN SELECT RAISE(ABORT,'delivery change retention floor has not advanced'); END`,
		}},

		// M145 / PAI-804: delivery-stream audience and complete lane metadata
		// invalidation. The nullable prior project is intentionally not an FK:
		// move-away and deletion reset rows must survive project retention. No
		// historical source audience is guessed; every pre-M145 row remains NULL.
		{145, []string{
			`ALTER TABLE delivery_change_log ADD COLUMN revoked_project_id INTEGER
				 CHECK(revoked_project_id IS NULL OR revoked_project_id > 0)`,
			`ALTER TABLE deliveries ADD COLUMN pending_revoked_project_id INTEGER
				 CHECK(pending_revoked_project_id IS NULL OR pending_revoked_project_id > 0)`,
			`DROP TRIGGER IF EXISTS trg_delivery_forbidden_patterns_no_insert`,
			`INSERT OR IGNORE INTO delivery_forbidden_value_patterns(
			 pattern,normalize_horizontal_whitespace,case_sensitive,boundary_needle,require_bearer_whitespace) VALUES
			 ('*passwd[=:]*',1,0,'passwd',0),
			 ('*passwd[/_-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-][0-9a-z._~+/=-]*',1,0,'passwd',0),
			 ('*sk-[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0),
			 ('*sk_[0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-][0-9a-z_-]*',0,0,'sk',0)`,
			`CREATE TRIGGER trg_delivery_forbidden_patterns_no_insert
			 BEFORE INSERT ON delivery_forbidden_value_patterns WHEN NOT EXISTS(
			  SELECT 1 FROM delivery_forbidden_value_patterns existing WHERE existing.pattern=NEW.pattern
			   AND existing.normalize_horizontal_whitespace=NEW.normalize_horizontal_whitespace
			   AND existing.case_sensitive=NEW.case_sensitive AND existing.boundary_needle=NEW.boundary_needle
			   AND existing.require_bearer_whitespace=NEW.require_bearer_whitespace)
			 BEGIN SELECT RAISE(ABORT, 'delivery forbidden patterns are migration-owned'); END`,
			`DROP TRIGGER IF EXISTS trg_delivery_reporter_secret_guard`,
			deliverySecretGuardSQL("trg_delivery_reporter_secret_guard", "delivery_reporters",
				"NEW.opaque_key", `
		  OR instr(CAST(value.value AS TEXT),'?')>0
		  OR (instr(CAST(value.value AS TEXT),'://')>0 AND instr(CAST(value.value AS TEXT),'@')>0)`,
				"forbidden delivery reporter value"),
			`DROP TRIGGER IF EXISTS trg_delivery_event_secret_guard`,
			deliverySecretGuardSQL("trg_delivery_event_secret_guard", "delivery_events",
				"NEW.idempotency_key,NEW.reason_code,NEW.reason_text", "", "forbidden delivery event value"),
			`DROP TRIGGER IF EXISTS trg_delivery_attempt_secret_guard`,
			deliverySecretGuardSQL("trg_delivery_attempt_secret_guard", "delivery_attempts",
				"NEW.reason_code,NEW.reason_text", "", "forbidden delivery attempt value"),
			`DROP TRIGGER IF EXISTS trg_delivery_policy_secret_guard`,
			deliverySecretGuardSQL("trg_delivery_policy_secret_guard", "delivery_attempt_stage_policy",
				"NEW.policy_reference,NEW.reason_code,NEW.reason_text", `
		  OR (value.value=NEW.policy_reference AND (instr(CAST(value.value AS TEXT),'?')>0 OR
		   (instr(CAST(value.value AS TEXT),'://')>0 AND instr(CAST(value.value AS TEXT),'@')>0)))`,
				"forbidden delivery policy value"),
			`DROP TRIGGER IF EXISTS trg_delivery_stage_secret_guard`,
			deliverySecretGuardSQL("trg_delivery_stage_secret_guard", "delivery_stage_events",
				"COALESCE(NEW.source_idempotency_key,''),NEW.activity,NEW.estimate_basis,NEW.reason_code,NEW.reason_text",
				"", "forbidden delivery stage value"),
			`DROP TRIGGER IF EXISTS trg_delivery_blocker_secret_guard`,
			deliverySecretGuardSQL("trg_delivery_blocker_secret_guard", "delivery_stage_blockers",
				"NEW.blocker_key,NEW.summary", "", "forbidden delivery blocker value"),
			`DROP TRIGGER IF EXISTS trg_delivery_evidence_secret_guard`,
			deliverySecretGuardSQL("trg_delivery_evidence_secret_guard", "delivery_evidence",
				"NEW.reference_value", `
		  OR instr(CAST(value.value AS TEXT),'?')>0
		  OR (instr(CAST(value.value AS TEXT),'://')>0 AND instr(CAST(value.value AS TEXT),'@')>0)`,
				"forbidden delivery evidence value"),
			`DROP TRIGGER IF EXISTS trg_delivery_issue_update_change`,
			agentModeDeliveryIssueUpdateTriggerSQL,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_delivery_issue_delete
				 BEFORE DELETE ON issues WHEN OLD.deleted_at IS NULL
				  AND EXISTS(SELECT 1 FROM deliveries WHERE issue_id=OLD.id)
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,OLD.project_id,
				   d.change_sequence_high_water+1,
				   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				   'issue','issue',OLD.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM deliveries d WHERE d.issue_id=OLD.id;
				 END`,
			`DROP TRIGGER IF EXISTS trg_deliveries_delete_tombstone`,
			`CREATE TABLE IF NOT EXISTS agent_mode_legacy_roots (
				 issue_id                   INTEGER PRIMARY KEY,
				 synthetic_delivery_id      INTEGER NOT NULL UNIQUE CHECK(synthetic_delivery_id=-issue_id),
				 delivery_key               TEXT NOT NULL UNIQUE CHECK(delivery_key=('issue:'||issue_id)),
				 project_id_hint            INTEGER NOT NULL CHECK(project_id_hint > 0),
				 pending_revoked_project_id INTEGER CHECK(pending_revoked_project_id IS NULL OR pending_revoked_project_id > 0),
				 change_sequence_high_water INTEGER NOT NULL DEFAULT 0 CHECK(change_sequence_high_water >= 0),
				 created_at                 TEXT NOT NULL
			 ) WITHOUT ROWID`,
			`INSERT OR IGNORE INTO agent_mode_legacy_roots(
				 issue_id,synthetic_delivery_id,delivery_key,project_id_hint,created_at)
			 SELECT i.id,-i.id,'issue:'||i.id,i.project_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			 FROM issues i WHERE i.deleted_at IS NULL
			  AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=i.id)
			  AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=i.id
			   AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'))
			  AND NOT EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=i.id
			   AND ar.delivery_instrumentation_version=1)`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_root_insert_guard
				 BEFORE INSERT ON agent_mode_legacy_roots WHEN NEW.issue_id<=0 OR
				  NEW.synthetic_delivery_id<>-NEW.issue_id OR NEW.delivery_key<>('issue:'||NEW.issue_id) OR
				  NEW.pending_revoked_project_id IS NOT NULL OR NEW.change_sequence_high_water<>0 OR
				  julianday(NEW.created_at) IS NULL OR
				  NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ',NEW.created_at) OR
				  NOT EXISTS(SELECT 1 FROM issues issue WHERE issue.id=NEW.issue_id
				   AND issue.deleted_at IS NULL AND issue.project_id=NEW.project_id_hint) OR
				  EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=NEW.issue_id) OR
				  NOT EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=NEW.issue_id
				   AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running')) OR
				  EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=NEW.issue_id
				   AND ar.delivery_instrumentation_version=1)
				 BEGIN SELECT RAISE(ABORT,'agent mode legacy root provenance is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_root_identity_guard
				 BEFORE UPDATE ON agent_mode_legacy_roots WHEN
				  NEW.issue_id IS NOT OLD.issue_id OR NEW.synthetic_delivery_id IS NOT OLD.synthetic_delivery_id OR
				  NEW.delivery_key IS NOT OLD.delivery_key OR NEW.created_at IS NOT OLD.created_at
				 BEGIN SELECT RAISE(ABORT,'agent mode legacy root identity is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_root_project_guard
				 BEFORE UPDATE OF project_id_hint,pending_revoked_project_id ON agent_mode_legacy_roots
				 WHEN NOT (
				  (NEW.project_id_hint IS OLD.project_id_hint AND NEW.pending_revoked_project_id IS OLD.pending_revoked_project_id) OR
				  (OLD.pending_revoked_project_id IS NULL AND NEW.project_id_hint<>OLD.project_id_hint AND
				   NEW.project_id_hint=COALESCE((SELECT project_id FROM issues WHERE id=OLD.issue_id),0) AND
				   NEW.pending_revoked_project_id=OLD.project_id_hint) OR
				  (OLD.pending_revoked_project_id IS NULL AND NEW.project_id_hint<>OLD.project_id_hint AND
				   NEW.project_id_hint=COALESCE((SELECT project_id FROM issues WHERE id=OLD.issue_id),0) AND
				   NEW.pending_revoked_project_id IS NULL AND (EXISTS(
				    SELECT 1 FROM issues issue WHERE issue.id=OLD.issue_id AND issue.deleted_at IS NOT NULL) OR
				    NOT EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=OLD.issue_id
				     AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running')) OR
				    EXISTS(SELECT 1 FROM delivery_change_log change
				     WHERE change.delivery_id=OLD.synthetic_delivery_id
				      AND change.change_sequence=OLD.change_sequence_high_water
				      AND change.kind IN ('issue','project_move')))) OR
				  (NEW.project_id_hint=OLD.project_id_hint AND OLD.pending_revoked_project_id IS NOT NULL AND
				   NEW.pending_revoked_project_id IS NULL AND
				   NEW.change_sequence_high_water=OLD.change_sequence_high_water+1 AND EXISTS(
				    SELECT 1 FROM delivery_change_log change
				    WHERE change.delivery_id=OLD.synthetic_delivery_id
				     AND change.change_sequence=NEW.change_sequence_high_water
				     AND change.kind='project_move'
				     AND change.revoked_project_id=OLD.pending_revoked_project_id))
				 )
				 BEGIN SELECT RAISE(ABORT,'agent mode legacy root project is not current'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_root_no_direct_delete
				 BEFORE DELETE ON agent_mode_legacy_roots WHEN EXISTS(SELECT 1 FROM issues WHERE id=OLD.issue_id)
				  AND NOT EXISTS(SELECT 1 FROM deliveries WHERE issue_id=OLD.issue_id)
				 BEGIN SELECT RAISE(ABORT,'agent mode legacy roots cannot be deleted directly'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_root_retire_on_delivery
				 AFTER INSERT ON deliveries
				 BEGIN DELETE FROM agent_mode_legacy_roots WHERE issue_id=NEW.issue_id; END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_issue_hide_change
				 BEFORE UPDATE OF deleted_at ON issues
				 WHEN OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,OLD.project_id,
				   d.change_sequence_high_water+1,
				   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				   'issue','issue',OLD.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM deliveries d WHERE d.issue_id=OLD.id;
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
				   OLD.project_id,legacy.change_sequence_high_water+1,0,'issue','issue',OLD.id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM agent_mode_legacy_roots legacy
				  WHERE legacy.issue_id=OLD.id
				   AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=OLD.id)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=OLD.id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_project_move_capture
				 BEFORE UPDATE OF project_id ON issues WHEN NEW.project_id IS NOT OLD.project_id
				  AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NULL
				 BEGIN
				  UPDATE deliveries SET pending_revoked_project_id=OLD.project_id WHERE issue_id=OLD.id;
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_deliveries_project_pending_guard
				 BEFORE UPDATE OF pending_revoked_project_id ON deliveries
				 WHEN NEW.pending_revoked_project_id IS NOT OLD.pending_revoked_project_id AND NOT (
				  (OLD.pending_revoked_project_id IS NULL AND NEW.pending_revoked_project_id=OLD.project_id_hint AND
				   EXISTS(SELECT 1 FROM issues issue WHERE issue.id=OLD.issue_id
				    AND issue.project_id=OLD.project_id_hint)) OR
				  (OLD.pending_revoked_project_id IS NOT NULL AND NEW.pending_revoked_project_id IS NULL AND
				   NEW.change_sequence_high_water=OLD.change_sequence_high_water+1 AND EXISTS(
				    SELECT 1 FROM delivery_change_log change WHERE change.delivery_id=OLD.id
				     AND change.change_sequence=NEW.change_sequence_high_water
				     AND change.kind='project_move'
				     AND change.revoked_project_id=OLD.pending_revoked_project_id))
				 )
				 BEGIN SELECT RAISE(ABORT,'delivery project move provenance is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_deliveries_project_hint_guard
				 BEFORE UPDATE OF project_id_hint ON deliveries
				 WHEN NEW.project_id_hint<>OLD.project_id_hint AND NOT EXISTS(
				  SELECT 1 FROM issues issue WHERE issue.id=OLD.issue_id
				   AND issue.project_id=NEW.project_id_hint)
				 BEGIN SELECT RAISE(ABORT,'delivery project hint is not current'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_issue_project_move_change
				 AFTER UPDATE OF project_id ON issues WHEN NEW.project_id IS NOT OLD.project_id
				 BEGIN
				  UPDATE deliveries SET project_id_hint=NEW.project_id WHERE issue_id=NEW.id;
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   revoked_project_id,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,NEW.project_id,OLD.project_id,
				   d.change_sequence_high_water+1,
				   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				   'project_move','issue',NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM deliveries d WHERE d.issue_id=NEW.id
				   AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NULL;
				 END`,
			`DROP TRIGGER IF EXISTS trg_delivery_change_sequence_guard`,
			`CREATE TRIGGER trg_delivery_change_sequence_guard
				 BEFORE INSERT ON delivery_change_log
				 WHEN NEW.change_sequence <> CASE WHEN NEW.delivery_id>0 THEN
				  COALESCE((SELECT change_sequence_high_water+1 FROM deliveries WHERE id=NEW.delivery_id),-1)
				 ELSE COALESCE((SELECT change_sequence_high_water+1 FROM agent_mode_legacy_roots
				  WHERE synthetic_delivery_id=NEW.delivery_id),-1) END
				 BEGIN SELECT RAISE(ABORT,'delivery change sequence is not contiguous'); END`,
			`DROP TRIGGER IF EXISTS trg_delivery_change_provenance_guard`,
			`CREATE TRIGGER trg_delivery_change_provenance_guard
				 BEFORE INSERT ON delivery_change_log WHEN
				  ((NEW.delivery_id>0)<>(EXISTS(SELECT 1 FROM deliveries d WHERE d.id=NEW.delivery_id
				    AND d.issue_id=NEW.root_issue_id AND d.delivery_key=NEW.delivery_key
				    AND d.project_id_hint IS NEW.project_id_hint))) OR
				  ((NEW.delivery_id<0)<>(EXISTS(SELECT 1 FROM agent_mode_legacy_roots legacy
				    WHERE legacy.synthetic_delivery_id=NEW.delivery_id AND legacy.issue_id=NEW.root_issue_id
				     AND legacy.delivery_key=NEW.delivery_key AND legacy.project_id_hint IS NEW.project_id_hint
				     AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)))) OR
				  NEW.delivery_id=0 OR
				  (NEW.delivery_id>0 AND NEW.kind='project_move' AND NEW.revoked_project_id IS NOT (
				   SELECT pending_revoked_project_id FROM deliveries WHERE id=NEW.delivery_id)) OR
				  (NEW.delivery_id<0 AND (NEW.delivery_revision<>0 OR NEW.source_sequence IS NOT NULL OR
				   NEW.source_id IS NULL OR
				   NOT EXISTS(SELECT 1 FROM issues issue WHERE issue.id=NEW.root_issue_id
				    AND issue.deleted_at IS NULL
				    AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=issue.id)
				    AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=issue.id
				     AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'))) OR
				   (NEW.kind='project_move' AND NEW.revoked_project_id IS NOT (
				    SELECT pending_revoked_project_id FROM agent_mode_legacy_roots
				    WHERE synthetic_delivery_id=NEW.delivery_id)) OR NOT (
				   (NEW.kind='run' AND NEW.source_kind='agent_run' AND EXISTS(
				    SELECT 1 FROM agent_runs ar WHERE ar.id=NEW.source_id AND ar.issue_id=NEW.root_issue_id
				     AND ar.delivery_instrumentation_version=0)) OR
				   (NEW.kind IN ('issue','project_move') AND NEW.source_kind='issue'
				    AND NEW.source_id=NEW.root_issue_id) OR
				   (NEW.kind='lane' AND NEW.source_kind='relation' AND NEW.source_id>0) OR
				   (NEW.kind='lane' AND NEW.source_kind='issue' AND NEW.source_id>0) OR
				   (NEW.kind='root_deleted' AND NEW.source_kind='issue' AND NEW.source_id=NEW.root_issue_id)
				  ))) OR
				  (NEW.kind='root_deleted' AND (EXISTS(SELECT 1 FROM issues i WHERE i.id=NEW.root_issue_id)
				   OR EXISTS(SELECT 1 FROM delivery_change_log prior WHERE prior.delivery_id=NEW.delivery_id
				    AND prior.kind='root_deleted'))) OR
				  (NEW.kind<>'root_deleted' AND NOT EXISTS(SELECT 1 FROM issues i
				   WHERE i.id=NEW.root_issue_id AND i.deleted_at IS NULL))
				 BEGIN SELECT RAISE(ABORT,'delivery change provenance does not match its live root'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_change_advance_legacy_high_water
				 AFTER INSERT ON delivery_change_log WHEN NEW.delivery_id<0
				 BEGIN UPDATE agent_mode_legacy_roots SET change_sequence_high_water=NEW.change_sequence,
				  pending_revoked_project_id=CASE WHEN NEW.kind='project_move' THEN NULL
				   ELSE pending_revoked_project_id END
				  WHERE synthetic_delivery_id=NEW.delivery_id; END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_root_high_water_guard
				 BEFORE UPDATE OF change_sequence_high_water ON agent_mode_legacy_roots
				 WHEN NEW.change_sequence_high_water<>OLD.change_sequence_high_water AND NOT (
				  NEW.change_sequence_high_water=OLD.change_sequence_high_water+1 AND EXISTS(
				   SELECT 1 FROM delivery_change_log change WHERE change.delivery_id=OLD.synthetic_delivery_id
				    AND change.change_sequence=NEW.change_sequence_high_water))
				 BEGIN SELECT RAISE(ABORT,'agent mode legacy high-water is log-owned'); END`,
			`DROP TRIGGER IF EXISTS trg_delivery_change_advance_high_water`,
			`CREATE TRIGGER trg_delivery_change_advance_high_water
				 AFTER INSERT ON delivery_change_log WHEN NEW.delivery_id>0
				 BEGIN UPDATE deliveries SET change_sequence_high_water=NEW.change_sequence,
				  pending_revoked_project_id=CASE WHEN NEW.kind='project_move' THEN NULL
				   ELSE pending_revoked_project_id END WHERE id=NEW.delivery_id; END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_runs_creation_lineage_immutable
				 BEFORE UPDATE OF id,issue_id ON agent_runs
				 WHEN NEW.id IS NOT OLD.id OR NEW.issue_id IS NOT OLD.issue_id
				 BEGIN SELECT RAISE(ABORT,'agent run creation lineage is immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_run_insert
				 AFTER INSERT ON agent_runs WHEN NEW.delivery_instrumentation_version=0
				  AND NEW.status IN ('queued','running')
				 BEGIN
				  INSERT OR IGNORE INTO agent_mode_legacy_roots(
				   issue_id,synthetic_delivery_id,delivery_key,project_id_hint,created_at)
				  SELECT issue.id,-issue.id,'issue:'||issue.id,issue.project_id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM issues issue WHERE issue.id=NEW.issue_id
				   AND issue.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=issue.id)
				   AND NOT EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=issue.id
				    AND ar.delivery_instrumentation_version=1);
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),root.delivery_id,NEW.issue_id,root.delivery_key,root.project_id_hint,
				   root.change_sequence+1,root.delivery_revision,'run','agent_run',NEW.id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM (
				    SELECT d.id AS delivery_id,d.delivery_key,d.project_id_hint,d.change_sequence_high_water AS change_sequence,
				     COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0) AS delivery_revision
				    FROM deliveries d WHERE d.issue_id=NEW.issue_id
				     AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=NEW.issue_id AND issue.deleted_at IS NULL)
				    UNION ALL
				    SELECT legacy.synthetic_delivery_id,legacy.delivery_key,legacy.project_id_hint,
				     legacy.change_sequence_high_water,0 FROM agent_mode_legacy_roots legacy
				    WHERE legacy.issue_id=NEW.issue_id AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=NEW.issue_id)
				     AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=NEW.issue_id AND issue.deleted_at IS NULL)
				   ) root;
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_run_deactivate
				 BEFORE UPDATE OF status ON agent_runs WHEN OLD.delivery_instrumentation_version=0
				  AND OLD.status IN ('queued','running') AND NEW.status NOT IN ('queued','running')
				  AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=OLD.issue_id AND issue.deleted_at IS NULL)
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),root.delivery_id,OLD.issue_id,root.delivery_key,root.project_id_hint,
				   root.change_sequence+1,root.delivery_revision,'run','agent_run',OLD.id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM (
				    SELECT d.id AS delivery_id,d.delivery_key,d.project_id_hint,d.change_sequence_high_water AS change_sequence,
				     COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0) AS delivery_revision
				    FROM deliveries d WHERE d.issue_id=OLD.issue_id
				    UNION ALL
				    SELECT legacy.synthetic_delivery_id,legacy.delivery_key,legacy.project_id_hint,
				     legacy.change_sequence_high_water,0 FROM agent_mode_legacy_roots legacy
				    WHERE legacy.issue_id=OLD.issue_id AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=OLD.issue_id)
				   ) root;
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_run_update
				 AFTER UPDATE OF status ON agent_runs WHEN NEW.delivery_instrumentation_version=0
				  AND OLD.status NOT IN ('queued','running') AND NEW.status IN ('queued','running')
				 BEGIN
				  INSERT OR IGNORE INTO agent_mode_legacy_roots(
				   issue_id,synthetic_delivery_id,delivery_key,project_id_hint,created_at)
				  SELECT issue.id,-issue.id,'issue:'||issue.id,issue.project_id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM issues issue WHERE issue.id=NEW.issue_id
				   AND NEW.status IN ('queued','running') AND issue.deleted_at IS NULL
				   AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=issue.id)
				   AND NOT EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=issue.id
				    AND ar.delivery_instrumentation_version=1);
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),root.delivery_id,NEW.issue_id,root.delivery_key,root.project_id_hint,
				   root.change_sequence+1,root.delivery_revision,'run','agent_run',NEW.id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM (
				    SELECT d.id AS delivery_id,d.delivery_key,d.project_id_hint,d.change_sequence_high_water AS change_sequence,
				     COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0) AS delivery_revision
				    FROM deliveries d WHERE d.issue_id=NEW.issue_id
				     AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=NEW.issue_id AND issue.deleted_at IS NULL)
				    UNION ALL
				    SELECT legacy.synthetic_delivery_id,legacy.delivery_key,legacy.project_id_hint,
				     legacy.change_sequence_high_water,0 FROM agent_mode_legacy_roots legacy
				    WHERE legacy.issue_id=NEW.issue_id AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=NEW.issue_id)
				     AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=NEW.issue_id AND issue.deleted_at IS NULL)
				   ) root;
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_run_delete
				 BEFORE DELETE ON agent_runs WHEN OLD.delivery_instrumentation_version=0
				  AND OLD.status IN ('queued','running')
				  AND EXISTS(SELECT 1 FROM issues WHERE id=OLD.issue_id AND deleted_at IS NULL)
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),root.delivery_id,OLD.issue_id,root.delivery_key,root.project_id_hint,
				   root.change_sequence+1,root.delivery_revision,'run','agent_run',OLD.id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM (
				    SELECT d.id AS delivery_id,d.delivery_key,d.project_id_hint,d.change_sequence_high_water AS change_sequence,
				     COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0) AS delivery_revision
				    FROM deliveries d WHERE d.issue_id=OLD.issue_id
				    UNION ALL
				    SELECT legacy.synthetic_delivery_id,legacy.delivery_key,legacy.project_id_hint,
				     legacy.change_sequence_high_water,0 FROM agent_mode_legacy_roots legacy
				    WHERE legacy.issue_id=OLD.issue_id AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=OLD.issue_id)
				   ) root;
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_issue_change
				 AFTER UPDATE OF project_id,issue_number,type,title,status,updated_at,deleted_at ON issues
				 WHEN EXISTS(SELECT 1 FROM agent_mode_legacy_roots WHERE issue_id=NEW.id) OR
				  (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL
				   AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=NEW.id)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=NEW.id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running')))
				 BEGIN
				  INSERT OR IGNORE INTO agent_mode_legacy_roots(
				   issue_id,synthetic_delivery_id,delivery_key,project_id_hint,created_at)
				  SELECT NEW.id,-NEW.id,'issue:'||NEW.id,NEW.project_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  WHERE OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL
				   AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=NEW.id)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=NEW.id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'))
				   AND NOT EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=NEW.id
				    AND ar.delivery_instrumentation_version=1);
				  UPDATE agent_mode_legacy_roots SET pending_revoked_project_id=CASE WHEN EXISTS(
				    SELECT 1 FROM agent_runs ar WHERE ar.issue_id=NEW.id
				     AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'))
				    AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NULL
				   THEN project_id_hint END,
				   project_id_hint=NEW.project_id WHERE issue_id=NEW.id
				   AND NEW.project_id IS NOT OLD.project_id;
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   revoked_project_id,change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
				   legacy.project_id_hint,CASE WHEN NEW.project_id IS NOT OLD.project_id
				    AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NULL THEN OLD.project_id END,
				   legacy.change_sequence_high_water+1,0,
				   CASE WHEN NEW.project_id IS NOT OLD.project_id
				    AND OLD.deleted_at IS NULL AND NEW.deleted_at IS NULL
				    THEN 'project_move' ELSE 'issue' END,
				   'issue',NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM agent_mode_legacy_roots legacy WHERE legacy.issue_id=NEW.id
				   AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'))
				   AND NEW.deleted_at IS NULL;
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_issue_tombstone
				 BEFORE DELETE ON issues WHEN OLD.deleted_at IS NULL
				  AND NOT EXISTS(SELECT 1 FROM deliveries WHERE issue_id=OLD.id)
				  AND EXISTS(SELECT 1 FROM agent_mode_legacy_roots WHERE issue_id=OLD.id)
				  AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=OLD.id
				   AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'))
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
				   OLD.project_id,legacy.change_sequence_high_water+1,0,'issue','issue',OLD.id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM agent_mode_legacy_roots legacy
				  WHERE legacy.issue_id=OLD.id;
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_legacy_issue_delete
				 AFTER DELETE ON issues WHEN EXISTS(SELECT 1 FROM agent_mode_legacy_roots WHERE issue_id=OLD.id)
				 BEGIN DELETE FROM agent_mode_legacy_roots WHERE issue_id=OLD.id; END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_issue_tag_insert_change
				 AFTER INSERT ON issue_tags WHEN EXISTS(SELECT 1 FROM issues WHERE id=NEW.issue_id)
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
				   d.change_sequence_high_water+1,
				   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				   'issue','issue',d.issue_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM deliveries d JOIN issues issue ON issue.id=d.issue_id AND issue.deleted_at IS NULL
				  WHERE d.issue_id=NEW.issue_id;
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
				   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'issue','issue',legacy.issue_id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM agent_mode_legacy_roots legacy
				  WHERE legacy.issue_id=NEW.issue_id AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
				   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_issue_tag_delete_change
				 AFTER DELETE ON issue_tags WHEN EXISTS(SELECT 1 FROM issues WHERE id=OLD.issue_id)
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
				   d.change_sequence_high_water+1,
				   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				   'issue','issue',d.issue_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM deliveries d JOIN issues issue ON issue.id=d.issue_id AND issue.deleted_at IS NULL
				  WHERE d.issue_id=OLD.issue_id;
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
				   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'issue','issue',legacy.issue_id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM agent_mode_legacy_roots legacy
				  WHERE legacy.issue_id=OLD.issue_id AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
				   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_issue_tag_update_change
				 AFTER UPDATE OF issue_id,tag_id ON issue_tags
				 WHEN NEW.issue_id IS NOT OLD.issue_id OR NEW.tag_id IS NOT OLD.tag_id
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
				   d.change_sequence_high_water+1,
				   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				   'issue','issue',d.issue_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM (SELECT OLD.issue_id AS issue_id UNION SELECT NEW.issue_id) changed
				  JOIN deliveries d ON d.issue_id=changed.issue_id
				  JOIN issues issue ON issue.id=d.issue_id AND issue.deleted_at IS NULL;
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
				   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'issue','issue',legacy.issue_id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM (
				    SELECT OLD.issue_id AS issue_id UNION SELECT NEW.issue_id
				   ) changed JOIN agent_mode_legacy_roots legacy ON legacy.issue_id=changed.issue_id
				  WHERE NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
				   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
				 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_mode_tag_name_change
				 AFTER UPDATE OF name ON tags WHEN NEW.name IS NOT OLD.name
				 BEGIN
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
				   d.change_sequence_high_water+1,
				   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
				   'issue','issue',d.issue_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
				  FROM issue_tags assignment JOIN deliveries d ON d.issue_id=assignment.issue_id
				  JOIN issues issue ON issue.id=d.issue_id AND issue.deleted_at IS NULL
				  WHERE assignment.tag_id=NEW.id;
				  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
				   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
				  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
				   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'issue','issue',legacy.issue_id,
				   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM issue_tags assignment
				   JOIN agent_mode_legacy_roots legacy ON legacy.issue_id=assignment.issue_id
				  WHERE assignment.tag_id=NEW.id AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
				   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
				   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
				    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
				 END`,
			`CREATE INDEX IF NOT EXISTS idx_delivery_change_revoked_project_tail
				 ON delivery_change_log(revoked_project_id,id) WHERE revoked_project_id IS NOT NULL`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_change_revoked_audience_guard
				 BEFORE INSERT ON delivery_change_log WHEN
				  (NEW.kind='project_move' AND NEW.revoked_project_id IS NULL) OR
				  (NEW.revoked_project_id IS NOT NULL AND (NEW.kind<>'project_move' OR
				   NEW.project_id_hint IS NULL OR NEW.revoked_project_id=NEW.project_id_hint))
				 BEGIN SELECT RAISE(ABORT,'delivery revoked audience is invalid'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_run_telemetry_delivery_secret_guard
			 BEFORE INSERT ON agent_run_telemetry
			 WHEN instr(CAST(NEW.activity AS TEXT),char(0))>0 OR instr(CAST(NEW.activity AS TEXT),char(10))>0
			  OR instr(CAST(NEW.activity AS TEXT),char(13))>0
			  OR instr(CAST(NEW.estimate_basis AS TEXT),char(0))>0 OR instr(CAST(NEW.estimate_basis AS TEXT),char(10))>0
			  OR instr(CAST(NEW.estimate_basis AS TEXT),char(13))>0
			  OR paimos_contains_secret_like(CAST(NEW.activity AS BLOB))=1
			  OR paimos_contains_secret_like(CAST(NEW.estimate_basis AS BLOB))=1
			 BEGIN SELECT RAISE(ABORT,'forbidden agent run telemetry value'); END`,

			// M144 invalidated only the two relation endpoints. Canonical lanes are
			// inherited through arbitrary parent depth, so a parent edit starts at
			// the child and invalidates every delivery-bearing descendant. UNION is
			// deliberately distinct and therefore cycle-safe without a depth cap.
			`DROP TRIGGER IF EXISTS trg_delivery_relation_insert_change`,
			`DROP TRIGGER IF EXISTS trg_delivery_relation_delete_change`,
			`DROP TRIGGER IF EXISTS trg_delivery_relation_update_change`,
			`CREATE TRIGGER trg_delivery_relation_insert_change
			 AFTER INSERT ON issue_relations WHEN NEW.type='parent'
			 BEGIN
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT NEW.target_id
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id
			    WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
			   d.change_sequence_high_water+1,
			   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
			   'lane','relation',NEW.source_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			  FROM descendants JOIN deliveries d ON d.issue_id=descendants.issue_id
			   JOIN issues live ON live.id=d.issue_id AND live.deleted_at IS NULL;
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT NEW.target_id
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
			   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'lane','relation',NEW.source_id,
			   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM descendants
			   JOIN agent_mode_legacy_roots legacy ON legacy.issue_id=descendants.issue_id
			  WHERE NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
			   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
			   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
			    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
			 END`,
			`CREATE TRIGGER trg_delivery_relation_delete_change
			 AFTER DELETE ON issue_relations WHEN OLD.type='parent'
			 BEGIN
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT OLD.target_id
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id
			    WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
			   d.change_sequence_high_water+1,
			   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
			   'lane','relation',OLD.source_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			  FROM descendants JOIN deliveries d ON d.issue_id=descendants.issue_id
			   JOIN issues live ON live.id=d.issue_id AND live.deleted_at IS NULL;
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT OLD.target_id
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
			   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'lane','relation',OLD.source_id,
			   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM descendants
			   JOIN agent_mode_legacy_roots legacy ON legacy.issue_id=descendants.issue_id
			  WHERE NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
			   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
			   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
			    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
			 END`,
			`CREATE TRIGGER trg_delivery_relation_update_change
			 AFTER UPDATE OF source_id,target_id,type ON issue_relations
			 WHEN NEW.source_id IS NOT OLD.source_id OR NEW.target_id IS NOT OLD.target_id OR NEW.type IS NOT OLD.type
			 BEGIN
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT OLD.target_id WHERE OLD.type='parent'
			   UNION SELECT NEW.target_id WHERE NEW.type='parent'
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
			   d.change_sequence_high_water+1,
			   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
			   'lane','relation',CASE WHEN NEW.type='parent' THEN NEW.source_id ELSE OLD.source_id END,
			   strftime('%Y-%m-%dT%H:%M:%fZ','now')
			  FROM descendants JOIN deliveries d ON d.issue_id=descendants.issue_id
			   JOIN issues live ON live.id=d.issue_id AND live.deleted_at IS NULL;
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT OLD.target_id WHERE OLD.type='parent'
			   UNION SELECT NEW.target_id WHERE NEW.type='parent'
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
			   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'lane','relation',
			   CASE WHEN NEW.type='parent' THEN NEW.source_id ELSE OLD.source_id END,
			   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM descendants
			   JOIN agent_mode_legacy_roots legacy ON legacy.issue_id=descendants.issue_id
			  WHERE NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
			   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
			   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
			    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
			 END`,

			// A mutable ancestor label/type/project/deletion state changes the lane
			// projection of every descendant even when the edge itself is stable.
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_ancestor_issue_change
			 AFTER UPDATE OF type,title,issue_number,deleted_at,project_id ON issues
			 WHEN NEW.type IS NOT OLD.type OR NEW.title IS NOT OLD.title OR
			  NEW.issue_number IS NOT OLD.issue_number OR NEW.deleted_at IS NOT OLD.deleted_at OR
			  NEW.project_id IS NOT OLD.project_id
			 BEGIN
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT relation.target_id FROM issue_relations relation
			    WHERE relation.type='parent' AND relation.source_id=NEW.id
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id
			    WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
			   d.change_sequence_high_water+1,
			   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
			   'lane','issue',NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			  FROM descendants JOIN deliveries d ON d.issue_id=descendants.issue_id
			   JOIN issues live ON live.id=d.issue_id AND live.deleted_at IS NULL
			  WHERE descendants.issue_id<>NEW.id;
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  WITH RECURSIVE descendants(issue_id) AS (
			   SELECT relation.target_id FROM issue_relations relation
			    WHERE relation.type='parent' AND relation.source_id=NEW.id
			   UNION
			   SELECT relation.target_id FROM issue_relations relation
			    JOIN descendants parent ON relation.source_id=parent.issue_id WHERE relation.type='parent'
			  )
			  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
			   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'lane','issue',NEW.id,
			   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM descendants
			   JOIN agent_mode_legacy_roots legacy ON legacy.issue_id=descendants.issue_id
			  WHERE descendants.issue_id<>NEW.id
			   AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
			   AND EXISTS(SELECT 1 FROM issues issue WHERE issue.id=legacy.issue_id AND issue.deleted_at IS NULL)
			   AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
			    AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
			 END`,
			`CREATE TRIGGER IF NOT EXISTS trg_delivery_project_metadata_change
			 AFTER UPDATE OF key,name,status ON projects
			 WHEN NEW.key IS NOT OLD.key OR NEW.name IS NOT OLD.name OR NEW.status IS NOT OLD.status
			 BEGIN
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  SELECT lower(hex(randomblob(16))),d.id,d.issue_id,d.delivery_key,d.project_id_hint,
			   d.change_sequence_high_water+1,
			   COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
			   'lane','issue',NEW.id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			  FROM deliveries d JOIN issues i ON i.id=d.issue_id
			  WHERE i.project_id=NEW.id AND i.deleted_at IS NULL;
			  INSERT INTO delivery_change_log(cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,
			   change_sequence,delivery_revision,kind,source_kind,source_id,server_received_at)
			  SELECT lower(hex(randomblob(16))),legacy.synthetic_delivery_id,legacy.issue_id,legacy.delivery_key,
			   legacy.project_id_hint,legacy.change_sequence_high_water+1,0,'lane','issue',NEW.id,
			   strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM agent_mode_legacy_roots legacy
			   JOIN issues issue ON issue.id=legacy.issue_id WHERE issue.project_id=NEW.id
			    AND issue.deleted_at IS NULL
			    AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.issue_id=legacy.issue_id)
			    AND EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=legacy.issue_id
			     AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'));
			 END`,
		}},

		// M146 / PAI-808: transactionally exact-once internal comments.
		// The client request identity is unique per authenticated author, not
		// per issue/body: reusing one identity for a different target or body
		// must collide so the handler can return an explicit conflict. NULL
		// keeps every pre-M146 and ordinary non-idempotent comment unchanged.
		{146, []string{
			`ALTER TABLE comments ADD COLUMN client_request_id TEXT
			 CHECK(client_request_id IS NULL OR
			  (author_id IS NOT NULL AND visibility='internal' AND
			   length(CAST(client_request_id AS BLOB)) BETWEEN 1 AND 128 AND
			   client_request_id NOT GLOB '*[^A-Za-z0-9._:-]*'))`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_comments_author_client_request
			 ON comments(author_id,client_request_id)
			 WHERE client_request_id IS NOT NULL`,
		}},

		// M147 / PAI-809 Wave 1A: safe principal identity, monotonic issue
		// control revisions, and the inert supervisory-control persistence
		// domain. No route or runner effect is enabled by this migration.
		{147, []string{
			// M89 repaired the rows present at migration time, but legacy TOTP
			// and dev-login insertions continued to inherit its empty default.
			// Repair those post-M89 rows before created_at becomes immutable.
			`UPDATE sessions SET created_at=datetime('now') WHERE created_at=''`,
			`ALTER TABLE sessions ADD COLUMN credential_id TEXT`,
			`UPDATE sessions SET credential_id=
			 lower(hex(randomblob(4)))||'-'||lower(hex(randomblob(2)))||'-4'||
			 substr(lower(hex(randomblob(2))),2,3)||'-'||
			 substr('89ab',(random() & 3)+1,1)||substr(lower(hex(randomblob(2))),2,3)||'-'||
			 lower(hex(randomblob(6)))`,
			// A pre-M147 bearer may itself happen to be a canonical UUID. The
			// durable credential identity must still be distinct from it.
			`UPDATE sessions SET credential_id=substr(credential_id,1,35)||
			 CASE substr(credential_id,36,1) WHEN '0' THEN '1' ELSE '0' END
			 WHERE credential_id=id`,
			`CREATE UNIQUE INDEX idx_sessions_credential_id
			 ON sessions(credential_id) WHERE credential_id IS NOT NULL`,
			`CREATE TRIGGER trg_sessions_credential_insert_guard
			 BEFORE INSERT ON sessions
			 WHEN NEW.credential_id IS NULL OR NEW.credential_id=NEW.id OR NOT ` + sqlUUIDCheck("NEW.credential_id") + `
			 BEGIN SELECT RAISE(ABORT,'invalid session credential identity'); END`,
			`CREATE TRIGGER trg_sessions_identity_update_guard
			 BEFORE UPDATE OF id,credential_id,user_id,created_at,via_dev_login,via_oidc ON sessions
			 WHEN NEW.id IS NOT OLD.id OR NEW.credential_id IS NOT OLD.credential_id OR
			  NEW.user_id IS NOT OLD.user_id OR NEW.created_at IS NOT OLD.created_at OR
			  NEW.via_dev_login IS NOT OLD.via_dev_login OR NEW.via_oidc IS NOT OLD.via_oidc OR
			  NEW.credential_id IS NULL OR NEW.credential_id=NEW.id OR NOT ` + sqlUUIDCheck("NEW.credential_id") + `
			 BEGIN SELECT RAISE(ABORT,'session identity is immutable'); END`,

			`ALTER TABLE api_keys ADD COLUMN disabled_at TEXT
			 CHECK(` + sqlNullableControlTimestampCheck("disabled_at") + `)`,
			`ALTER TABLE api_keys ADD COLUMN expires_at TEXT
			 CHECK(` + sqlNullableControlTimestampCheck("expires_at") + `)`,
			`CREATE INDEX idx_api_keys_enabled_hash ON api_keys(key_hash) WHERE disabled_at IS NULL`,
			`CREATE INDEX idx_api_keys_expiry ON api_keys(expires_at) WHERE expires_at IS NOT NULL`,
			`CREATE TRIGGER trg_api_keys_identity_update_guard
			 BEFORE UPDATE OF id,user_id,key_hash,key_prefix,created_at ON api_keys
			 WHEN NEW.id IS NOT OLD.id OR NEW.user_id IS NOT OLD.user_id OR
			  NEW.key_hash IS NOT OLD.key_hash OR NEW.key_prefix IS NOT OLD.key_prefix OR
			  NEW.created_at IS NOT OLD.created_at
			 BEGIN SELECT RAISE(ABORT,'api key identity is immutable'); END`,
			`CREATE TRIGGER trg_api_keys_disabled_terminal
			 BEFORE UPDATE OF disabled_at ON api_keys
			 WHEN OLD.disabled_at IS NOT NULL AND NEW.disabled_at IS NOT OLD.disabled_at
			 BEGIN SELECT RAISE(ABORT,'api key disablement is terminal'); END`,

			`CREATE TABLE issue_control_revisions (
			 issue_id    INTEGER PRIMARY KEY CHECK(issue_id>0),
			 revision    INTEGER NOT NULL CHECK(revision>0),
			 recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("recorded_at") + `),
			 updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `)
			)`,
			`INSERT INTO issue_control_revisions(issue_id,revision)
			 SELECT id,1 FROM issues`,
			`CREATE TRIGGER trg_issue_control_revision_on_insert
			 AFTER INSERT ON issues
			 BEGIN
			  INSERT INTO issue_control_revisions(issue_id,revision) VALUES(NEW.id,1);
			 END`,
			`CREATE TRIGGER trg_issue_control_revision_on_update
			 AFTER UPDATE ON issues
			 BEGIN
			  UPDATE issue_control_revisions SET revision=revision+1,
			   updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE issue_id=NEW.id;
			  SELECT CASE WHEN changes()<>1 THEN RAISE(ABORT,'missing issue control revision') END;
			 END`,
			`CREATE TRIGGER trg_issue_control_revision_on_delete
			 AFTER DELETE ON issues
			 BEGIN DELETE FROM issue_control_revisions WHERE issue_id=OLD.id; END`,
			`CREATE TRIGGER trg_control_project_status_revisions
			 AFTER UPDATE OF status ON projects
			 WHEN NEW.status IS NOT OLD.status
			 BEGIN
			  UPDATE issue_control_revisions SET revision=revision+1,
			   updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			   WHERE issue_id IN (SELECT id FROM issues WHERE project_id=NEW.id AND deleted_at IS NULL);
			 END`,
			`CREATE TRIGGER trg_issue_control_revision_guard
			 BEFORE UPDATE ON issue_control_revisions
			 WHEN NEW.issue_id IS NOT OLD.issue_id OR NEW.recorded_at IS NOT OLD.recorded_at OR
			      NEW.revision<>OLD.revision+1 OR NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NEW.updated_at<OLD.updated_at
			 BEGIN SELECT RAISE(ABORT,'invalid issue control revision'); END`,
			`CREATE TRIGGER trg_issue_control_revision_no_delete
			 BEFORE DELETE ON issue_control_revisions
			 WHEN EXISTS(SELECT 1 FROM issues WHERE id=OLD.issue_id)
			 BEGIN SELECT RAISE(ABORT,'live issue control revision is required'); END`,

			`CREATE TABLE agent_run_cancellation_facts (
			 run_id             INTEGER PRIMARY KEY CHECK(run_id>0),
			 cancellation_cause TEXT NOT NULL CHECK(cancellation_cause IN (` + sqlEnum(controlcontract.CancellationCauses()) + `)),
			 command_id         TEXT CHECK(command_id IS NULL OR ` + sqlUUIDCheck("command_id") + `),
			 recorded_at        TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("recorded_at") + `),
			 CHECK((cancellation_cause='operator_command' AND command_id IS NOT NULL) OR
			       (cancellation_cause<>'operator_command' AND command_id IS NULL))
			)`,
			`CREATE INDEX idx_agent_run_cancellation_cause
			 ON agent_run_cancellation_facts(cancellation_cause,recorded_at)`,
			`CREATE TRIGGER trg_agent_run_cancellation_facts_no_update
			 BEFORE UPDATE ON agent_run_cancellation_facts
			 BEGIN SELECT RAISE(ABORT,'agent run cancellation facts are immutable'); END`,
			`CREATE TRIGGER trg_agent_run_cancellation_facts_no_delete
			 BEFORE DELETE ON agent_run_cancellation_facts
			 BEGIN SELECT RAISE(ABORT,'agent run cancellation facts are immutable'); END`,

			`CREATE TABLE control_operation_keys (
			 id                INTEGER PRIMARY KEY AUTOINCREMENT,
			 actor_user_id     INTEGER NOT NULL CHECK(actor_user_id>0),
			 user_id           INTEGER NOT NULL CHECK(user_id>0),
			 principal_kind    TEXT NOT NULL CHECK(principal_kind IN ('session','api_key')),
			 actor_session_credential_id TEXT,
			 actor_api_key_id  INTEGER,
			 operation_kind    TEXT NOT NULL CHECK(operation_kind IN (` + sqlEnum(controlcontract.OperationKinds()) + `)),
			 operation_key_digest BLOB NOT NULL CHECK(typeof(operation_key_digest)='blob' AND length(operation_key_digest)=32),
			 request_digest    BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 result_digest     BLOB NOT NULL CHECK(typeof(result_digest)='blob' AND length(result_digest)=32),
			 grant_id          TEXT CHECK(grant_id IS NULL OR ` + sqlUUIDCheck("grant_id") + `),
			 lease_id          TEXT CHECK(lease_id IS NULL OR ` + sqlUUIDCheck("lease_id") + `),
			 input_request_id  TEXT CHECK(input_request_id IS NULL OR ` + sqlUUIDCheck("input_request_id") + `),
			 command_id        TEXT CHECK(command_id IS NULL OR ` + sqlUUIDCheck("command_id") + `),
			 created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 CHECK(actor_user_id=user_id),
			 CHECK(` + sqlTypedPrincipalCheck("principal_kind", "actor_session_credential_id", "actor_api_key_id") + `),
			 CHECK(operation_kind NOT IN ('lease.issue','lease.renew','lease.revoke','input.create','command.claim','command.result') OR principal_kind='api_key'),
			 CHECK((operation_kind IN ('grant.put','grant.revoke') AND grant_id IS NOT NULL AND lease_id IS NULL AND input_request_id IS NULL AND command_id IS NULL) OR
			       (operation_kind IN ('lease.issue','lease.renew','lease.revoke') AND grant_id IS NULL AND lease_id IS NOT NULL AND input_request_id IS NULL AND command_id IS NULL) OR
			       (operation_kind='input.create' AND grant_id IS NULL AND lease_id IS NULL AND input_request_id IS NOT NULL AND command_id IS NULL) OR
			       (operation_kind IN ('command.create','command.confirm','command.withdraw','command.claim','command.result') AND grant_id IS NULL AND lease_id IS NULL AND input_request_id IS NULL AND command_id IS NOT NULL))
			)`,
			`CREATE UNIQUE INDEX idx_control_operation_session_key
			 ON control_operation_keys(actor_session_credential_id,operation_kind,operation_key_digest)
			 WHERE principal_kind='session'`,
			`CREATE UNIQUE INDEX idx_control_operation_api_key
			 ON control_operation_keys(actor_api_key_id,operation_kind,operation_key_digest)
			 WHERE principal_kind='api_key'`,
			`CREATE INDEX idx_control_operation_grant ON control_operation_keys(grant_id) WHERE grant_id IS NOT NULL`,
			`CREATE INDEX idx_control_operation_lease ON control_operation_keys(lease_id) WHERE lease_id IS NOT NULL`,
			`CREATE INDEX idx_control_operation_input ON control_operation_keys(input_request_id) WHERE input_request_id IS NOT NULL`,
			`CREATE INDEX idx_control_operation_command ON control_operation_keys(command_id) WHERE command_id IS NOT NULL`,
			`CREATE TRIGGER trg_control_operation_keys_clock_guard
			 BEFORE INSERT ON control_operation_keys
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now')
			 BEGIN SELECT RAISE(ABORT,'control operation time is server-owned'); END`,
			`CREATE TRIGGER trg_control_operation_keys_no_update
			 BEFORE UPDATE ON control_operation_keys
			 BEGIN SELECT RAISE(ABORT,'control operation keys are append-only'); END`,
			`CREATE TRIGGER trg_control_operation_keys_no_delete
			 BEFORE DELETE ON control_operation_keys
			 BEGIN SELECT RAISE(ABORT,'control operation keys are append-only'); END`,

			`CREATE TABLE control_capability_grants (
			 grant_id          TEXT NOT NULL CHECK(` + sqlUUIDCheck("grant_id") + `),
			 revision          INTEGER NOT NULL CHECK(revision>0),
			 actor_user_id     INTEGER NOT NULL CHECK(actor_user_id>0),
			 user_id           INTEGER NOT NULL CHECK(user_id>0),
			 principal_kind    TEXT NOT NULL CHECK(principal_kind IN ('session','api_key')),
			 actor_session_credential_id TEXT,
			 actor_api_key_id  INTEGER,
			 delivery_id       INTEGER NOT NULL CHECK(delivery_id>0),
			 delivery_key      TEXT NOT NULL CHECK(` + sqlStableKeyCheck("delivery_key", 80) + `),
			 delivery_revision INTEGER NOT NULL CHECK(delivery_revision>0),
			 project_id        INTEGER NOT NULL CHECK(project_id>0),
			 root_issue_id     INTEGER NOT NULL CHECK(root_issue_id>0),
			 issue_revision    INTEGER NOT NULL CHECK(issue_revision>0),
			 issue_etag_digest BLOB NOT NULL CHECK(typeof(issue_etag_digest)='blob' AND length(issue_etag_digest)=32),
			 binding_digest    BLOB NOT NULL CHECK(typeof(binding_digest)='blob' AND length(binding_digest)=32),
			 action_set_digest BLOB NOT NULL CHECK(typeof(action_set_digest)='blob' AND length(action_set_digest)=32),
			 action_count      INTEGER NOT NULL CHECK(action_count BETWEEN 1 AND 6),
			 expires_at        TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("expires_at") + `),
			 revoked_at        TEXT CHECK(` + sqlNullableControlTimestampCheck("revoked_at") + `),
			 created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 PRIMARY KEY(grant_id,revision),
			 CHECK(actor_user_id=user_id),
			 CHECK(` + sqlTypedPrincipalCheck("principal_kind", "actor_session_credential_id", "actor_api_key_id") + `),
			 CHECK(expires_at>created_at),
			 CHECK(revoked_at IS NULL OR revoked_at>=created_at)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_grant_revision_guard
			 BEFORE INSERT ON control_capability_grants
			 WHEN (NOT EXISTS(SELECT 1 FROM control_capability_grants WHERE grant_id=NEW.grant_id) AND
			       (NEW.revision<>1 OR EXISTS(SELECT 1 FROM control_capability_grants prior_subject
			        WHERE prior_subject.user_id=NEW.user_id AND prior_subject.delivery_id=NEW.delivery_id))) OR
			      (EXISTS(SELECT 1 FROM control_capability_grants WHERE grant_id=NEW.grant_id) AND
			       (NEW.revision<>(SELECT MAX(revision)+1 FROM control_capability_grants WHERE grant_id=NEW.grant_id) OR
			        (SELECT revoked_at FROM control_capability_grants WHERE grant_id=NEW.grant_id ORDER BY revision DESC LIMIT 1) IS NULL OR
			        (SELECT revoked_at FROM control_capability_grants WHERE grant_id=NEW.grant_id ORDER BY revision DESC LIMIT 1)>NEW.created_at OR
			        NOT EXISTS(SELECT 1 FROM control_capability_grants lineage WHERE lineage.grant_id=NEW.grant_id
			         AND lineage.revision=1 AND lineage.user_id=NEW.user_id AND lineage.delivery_id=NEW.delivery_id)))
			 BEGIN SELECT RAISE(ABORT,'invalid control grant revision'); END`,
			`CREATE TRIGGER trg_control_grant_current_binding_guard
			 BEFORE INSERT ON control_capability_grants
			 WHEN NEW.revoked_at IS NOT NULL OR NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NEW.updated_at IS NOT NEW.created_at OR NEW.expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NOT EXISTS(
			  SELECT 1 FROM deliveries d JOIN issues i ON i.id=d.issue_id
			  JOIN projects project ON project.id=i.project_id AND project.status IN ('active','frozen','archived')
			  WHERE d.id=NEW.delivery_id AND d.delivery_key=NEW.delivery_key AND d.issue_id=NEW.root_issue_id
			   AND i.project_id=NEW.project_id AND i.deleted_at IS NULL AND
			   (SELECT revision FROM issue_control_revisions WHERE issue_id=i.id)=NEW.issue_revision AND
			   COALESCE((SELECT MAX(de.delivery_revision) FROM delivery_events de WHERE de.delivery_id=d.id),0)=NEW.delivery_revision)
			 BEGIN SELECT RAISE(ABORT,'control grant target is stale'); END`,
			`CREATE INDEX idx_control_grants_subject
			 ON control_capability_grants(delivery_id,user_id,principal_kind,revision DESC)`,
			`CREATE INDEX idx_control_grants_expiry
			 ON control_capability_grants(expires_at) WHERE revoked_at IS NULL`,
			`CREATE UNIQUE INDEX idx_control_grants_current_subject
			 ON control_capability_grants(user_id,delivery_id) WHERE revoked_at IS NULL`,
			`CREATE TABLE control_capability_grant_actions (
			 grant_id TEXT NOT NULL,
			 grant_revision INTEGER NOT NULL CHECK(grant_revision>0),
			 action TEXT NOT NULL CHECK(action IN (` + sqlEnum(controlcontract.Actions()) + `)),
			 PRIMARY KEY(grant_id,grant_revision,action),
			 FOREIGN KEY(grant_id,grant_revision)
			  REFERENCES control_capability_grants(grant_id,revision) ON DELETE CASCADE
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_grant_actions_no_update
			 BEFORE UPDATE ON control_capability_grant_actions
			 BEGIN SELECT RAISE(ABORT,'control grant actions are immutable'); END`,
			`CREATE TRIGGER trg_control_grant_actions_no_delete
			 BEFORE DELETE ON control_capability_grant_actions
			 BEGIN SELECT RAISE(ABORT,'control grant actions are immutable'); END`,
			`CREATE TABLE control_capability_grant_seals (
			 grant_id          TEXT NOT NULL,
			 grant_revision    INTEGER NOT NULL CHECK(grant_revision>0),
			 binding_digest    BLOB NOT NULL CHECK(typeof(binding_digest)='blob' AND length(binding_digest)=32),
			 action_set_digest BLOB NOT NULL CHECK(typeof(action_set_digest)='blob' AND length(action_set_digest)=32),
			 action_count      INTEGER NOT NULL CHECK(action_count BETWEEN 1 AND 6),
			 sealed_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("sealed_at") + `),
			 PRIMARY KEY(grant_id,grant_revision),
			 FOREIGN KEY(grant_id,grant_revision)
			  REFERENCES control_capability_grants(grant_id,revision)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_grant_actions_after_seal
			 BEFORE INSERT ON control_capability_grant_actions
			 WHEN EXISTS(SELECT 1 FROM control_capability_grant_seals
			  WHERE grant_id=NEW.grant_id AND grant_revision=NEW.grant_revision)
			 BEGIN SELECT RAISE(ABORT,'control grant actions are sealed'); END`,
			`CREATE TRIGGER trg_control_grant_seal_complete
			 BEFORE INSERT ON control_capability_grant_seals
			 WHEN NEW.sealed_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      (SELECT COUNT(*) FROM control_capability_grant_actions
			       WHERE grant_id=NEW.grant_id AND grant_revision=NEW.grant_revision)<>NEW.action_count OR
			      NOT EXISTS(SELECT 1 FROM control_capability_grants grant_row
			       WHERE grant_row.grant_id=NEW.grant_id AND grant_row.revision=NEW.grant_revision
			        AND grant_row.binding_digest=NEW.binding_digest AND grant_row.action_set_digest=NEW.action_set_digest
			        AND grant_row.action_count=NEW.action_count) OR
			      NOT EXISTS(
			       SELECT 1 FROM control_capability_grants grant_row
			       JOIN issues issue ON issue.id=grant_row.root_issue_id AND issue.deleted_at IS NULL
			       JOIN projects project ON project.id=grant_row.project_id AND project.id=issue.project_id
			       WHERE grant_row.grant_id=NEW.grant_id AND grant_row.revision=NEW.grant_revision
			        AND (project.status IN ('active','frozen') OR
			             (project.status='archived' AND NOT EXISTS(
			              SELECT 1 FROM control_capability_grant_actions action
			              WHERE action.grant_id=grant_row.grant_id AND action.grant_revision=grant_row.revision
			               AND action.action NOT IN ('run.cancel.queued','run.cancel.running')))))
			 BEGIN SELECT RAISE(ABORT,'control grant seal is incomplete'); END`,
			`CREATE TRIGGER trg_control_grant_seals_no_update
			 BEFORE UPDATE ON control_capability_grant_seals
			 BEGIN SELECT RAISE(ABORT,'control grant seals are immutable'); END`,
			`CREATE TRIGGER trg_control_grant_seals_no_delete
			 BEFORE DELETE ON control_capability_grant_seals
			 BEGIN SELECT RAISE(ABORT,'control grant seals are immutable'); END`,
			`CREATE TRIGGER trg_control_grants_binding_guard
			 BEFORE UPDATE ON control_capability_grants
			 WHEN NOT EXISTS(SELECT 1 FROM control_capability_grant_seals WHERE grant_id=OLD.grant_id AND grant_revision=OLD.revision) OR
			  NEW.grant_id IS NOT OLD.grant_id OR NEW.revision IS NOT OLD.revision OR
			  NEW.actor_user_id IS NOT OLD.actor_user_id OR NEW.user_id IS NOT OLD.user_id OR
			  NEW.principal_kind IS NOT OLD.principal_kind OR
			  NEW.actor_session_credential_id IS NOT OLD.actor_session_credential_id OR NEW.actor_api_key_id IS NOT OLD.actor_api_key_id OR
			  NEW.delivery_id IS NOT OLD.delivery_id OR NEW.delivery_key IS NOT OLD.delivery_key OR
			  NEW.delivery_revision IS NOT OLD.delivery_revision OR NEW.project_id IS NOT OLD.project_id OR
			  NEW.root_issue_id IS NOT OLD.root_issue_id OR NEW.issue_revision IS NOT OLD.issue_revision OR
			  NEW.issue_etag_digest IS NOT OLD.issue_etag_digest OR NEW.binding_digest IS NOT OLD.binding_digest OR
			  NEW.action_set_digest IS NOT OLD.action_set_digest OR NEW.action_count IS NOT OLD.action_count OR NEW.created_at IS NOT OLD.created_at OR
			  NEW.expires_at<OLD.expires_at OR NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.updated_at<OLD.updated_at OR
			  (OLD.revoked_at IS NULL AND NEW.revoked_at IS NULL AND
			   (NEW.expires_at<=OLD.expires_at OR strftime('%Y-%m-%dT%H:%M:%fZ','now')>=OLD.expires_at)) OR
			  (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL AND
			   (NEW.expires_at IS NOT OLD.expires_at OR NEW.revoked_at IS NOT NEW.updated_at OR NEW.revoked_at<OLD.updated_at)) OR
			  (OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS NOT OLD.revoked_at OR NEW.updated_at IS NOT OLD.updated_at)) OR
			  (OLD.revoked_at IS NOT NULL AND NEW.expires_at IS NOT OLD.expires_at)
			 BEGIN SELECT RAISE(ABORT,'control grant binding is immutable'); END`,
			`CREATE TRIGGER trg_control_grants_no_delete
			 BEFORE DELETE ON control_capability_grants
			 BEGIN SELECT RAISE(ABORT,'control grants are retained'); END`,

			`CREATE TABLE control_capability_leases (
			 lease_id           TEXT NOT NULL CHECK(` + sqlUUIDCheck("lease_id") + `),
			 revision           INTEGER NOT NULL CHECK(revision>0),
			 actor_user_id      INTEGER NOT NULL CHECK(actor_user_id>0),
			 user_id            INTEGER NOT NULL CHECK(user_id>0),
			 principal_kind     TEXT NOT NULL CHECK(principal_kind IN ('session','api_key')),
			 actor_session_credential_id TEXT,
			 actor_api_key_id   INTEGER,
			 device_id          TEXT NOT NULL CHECK(` + sqlSafeDeviceIDCheck("device_id") + `),
			 delivery_id        INTEGER NOT NULL CHECK(delivery_id>0),
			 delivery_key       TEXT NOT NULL CHECK(` + sqlStableKeyCheck("delivery_key", 80) + `),
			 delivery_revision  INTEGER NOT NULL CHECK(delivery_revision>0),
			 project_id         INTEGER NOT NULL CHECK(project_id>0),
			 root_issue_id      INTEGER NOT NULL CHECK(root_issue_id>0),
			 issue_revision     INTEGER NOT NULL CHECK(issue_revision>0),
			 attempt_id         INTEGER NOT NULL CHECK(attempt_id>0),
			 attempt_number     INTEGER NOT NULL CHECK(attempt_number>0),
			 plan_revision      INTEGER NOT NULL CHECK(plan_revision>0),
			 stage_key          TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number   INTEGER NOT NULL CHECK(execution_number>0),
			 execution_start_stage_event_id INTEGER NOT NULL CHECK(execution_start_stage_event_id>0),
			 authority_epoch    INTEGER NOT NULL CHECK(authority_epoch>0),
			 authority_stage_event_id INTEGER NOT NULL CHECK(authority_stage_event_id>0),
			 reporter_id        INTEGER NOT NULL CHECK(reporter_id>0),
			 agent_run_id       INTEGER NOT NULL CHECK(agent_run_id>0),
			 binding_digest     BLOB NOT NULL CHECK(typeof(binding_digest)='blob' AND length(binding_digest)=32),
			 action_set_digest  BLOB NOT NULL CHECK(typeof(action_set_digest)='blob' AND length(action_set_digest)=32),
			 action_count       INTEGER NOT NULL CHECK(action_count BETWEEN 1 AND 4),
			 expires_at         TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("expires_at") + `),
			 revoked_at         TEXT CHECK(` + sqlNullableControlTimestampCheck("revoked_at") + `),
			 created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 PRIMARY KEY(lease_id,revision),
			 CHECK(actor_user_id=user_id),
			 CHECK(` + sqlTypedPrincipalCheck("principal_kind", "actor_session_credential_id", "actor_api_key_id") + `),
			 CHECK(principal_kind='api_key'),
			 CHECK(expires_at>created_at),
			 CHECK(revoked_at IS NULL OR revoked_at>=created_at)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_lease_revision_guard
			 BEFORE INSERT ON control_capability_leases
			 WHEN (NOT EXISTS(SELECT 1 FROM control_capability_leases WHERE lease_id=NEW.lease_id) AND
			       (NEW.revision<>1 OR EXISTS(SELECT 1 FROM control_capability_leases prior_subject
			        WHERE prior_subject.delivery_id=NEW.delivery_id AND prior_subject.attempt_id=NEW.attempt_id
			         AND prior_subject.stage_key=NEW.stage_key AND prior_subject.execution_number=NEW.execution_number))) OR
			      (EXISTS(SELECT 1 FROM control_capability_leases WHERE lease_id=NEW.lease_id) AND
			       (NEW.revision<>(SELECT MAX(revision)+1 FROM control_capability_leases WHERE lease_id=NEW.lease_id) OR
			        (SELECT revoked_at FROM control_capability_leases WHERE lease_id=NEW.lease_id ORDER BY revision DESC LIMIT 1) IS NULL OR
			        (SELECT revoked_at FROM control_capability_leases WHERE lease_id=NEW.lease_id ORDER BY revision DESC LIMIT 1)>NEW.created_at OR
			        NOT EXISTS(SELECT 1 FROM control_capability_leases lineage WHERE lineage.lease_id=NEW.lease_id
			         AND lineage.revision=1 AND lineage.delivery_id=NEW.delivery_id AND lineage.attempt_id=NEW.attempt_id
			         AND lineage.stage_key=NEW.stage_key AND lineage.execution_number=NEW.execution_number)))
			 BEGIN SELECT RAISE(ABORT,'invalid control lease revision'); END`,
			`CREATE TRIGGER trg_control_lease_current_binding_guard
			 BEFORE INSERT ON control_capability_leases
			 WHEN NEW.revoked_at IS NOT NULL OR NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NEW.updated_at IS NOT NEW.created_at OR NEW.expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NOT EXISTS(
			  SELECT 1 FROM deliveries d
			  JOIN issues i ON i.id=d.issue_id
			  JOIN projects project ON project.id=i.project_id AND project.status IN ('active','frozen','archived')
			  JOIN delivery_attempts a ON a.delivery_id=d.id AND a.id=NEW.attempt_id
			  JOIN delivery_agent_run_links link ON link.delivery_id=d.id AND link.attempt_id=a.id
			   AND link.stage_key=NEW.stage_key AND link.execution_number=NEW.execution_number
			   AND link.agent_run_id=NEW.agent_run_id AND link.reporter_id=NEW.reporter_id
			   AND link.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			  JOIN agent_runs run ON run.id=NEW.agent_run_id AND run.issue_id=d.issue_id AND run.status='running'
			  JOIN delivery_agent_run_activations activation ON activation.delivery_id=d.id AND activation.attempt_id=a.id
			   AND activation.stage_key=NEW.stage_key AND activation.execution_number=NEW.execution_number
			   AND activation.authority_epoch=NEW.authority_epoch AND activation.agent_run_id=NEW.agent_run_id
			   AND activation.reporter_id=NEW.reporter_id AND activation.authority_stage_event_id=NEW.authority_stage_event_id
			  JOIN delivery_stage_latest latest ON latest.delivery_id=d.id AND latest.attempt_id=a.id
			   AND latest.stage_key=NEW.stage_key AND latest.execution_number=NEW.execution_number
			   AND latest.authority_epoch=NEW.authority_epoch AND latest.current_reporter_id=NEW.reporter_id
			   AND latest.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			   AND latest.authority_stage_event_id=NEW.authority_stage_event_id
			  WHERE d.id=NEW.delivery_id AND d.delivery_key=NEW.delivery_key AND d.issue_id=NEW.root_issue_id
			   AND i.project_id=NEW.project_id AND i.deleted_at IS NULL AND a.attempt_number=NEW.attempt_number AND a.plan_revision=NEW.plan_revision
			   AND (SELECT revision FROM issue_control_revisions WHERE issue_id=i.id)=NEW.issue_revision
			   AND COALESCE((SELECT MAX(de.delivery_revision) FROM delivery_events de WHERE de.delivery_id=d.id),0)=NEW.delivery_revision)
			 BEGIN SELECT RAISE(ABORT,'control lease target is stale'); END`,
			`CREATE INDEX idx_control_leases_run
			 ON control_capability_leases(agent_run_id,revision DESC)`,
			`CREATE INDEX idx_control_leases_binding
			 ON control_capability_leases(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,reporter_id)`,
			`CREATE INDEX idx_control_leases_expiry
			 ON control_capability_leases(expires_at) WHERE revoked_at IS NULL`,
			`CREATE UNIQUE INDEX idx_control_leases_current_activation
			 ON control_capability_leases(delivery_id,attempt_id,stage_key,execution_number)
			 WHERE revoked_at IS NULL`,
			`CREATE TABLE control_capability_lease_actions (
			 lease_id TEXT NOT NULL,
			 lease_revision INTEGER NOT NULL CHECK(lease_revision>0),
			 action TEXT NOT NULL CHECK(action IN (` + sqlEnum(controlcontract.Actions()) + `))
			  CHECK(action IN ('run.cancel.running','input.respond','run.pause','run.resume')),
			 PRIMARY KEY(lease_id,lease_revision,action),
			 FOREIGN KEY(lease_id,lease_revision)
			  REFERENCES control_capability_leases(lease_id,revision) ON DELETE CASCADE
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_lease_actions_no_update
			 BEFORE UPDATE ON control_capability_lease_actions
			 BEGIN SELECT RAISE(ABORT,'control lease actions are immutable'); END`,
			`CREATE TRIGGER trg_control_lease_actions_no_delete
			 BEFORE DELETE ON control_capability_lease_actions
			 BEGIN SELECT RAISE(ABORT,'control lease actions are immutable'); END`,
			`CREATE TABLE control_capability_lease_seals (
			 lease_id          TEXT NOT NULL,
			 lease_revision    INTEGER NOT NULL CHECK(lease_revision>0),
			 binding_digest    BLOB NOT NULL CHECK(typeof(binding_digest)='blob' AND length(binding_digest)=32),
			 action_set_digest BLOB NOT NULL CHECK(typeof(action_set_digest)='blob' AND length(action_set_digest)=32),
			 action_count      INTEGER NOT NULL CHECK(action_count BETWEEN 1 AND 4),
			 sealed_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("sealed_at") + `),
			 PRIMARY KEY(lease_id,lease_revision),
			 FOREIGN KEY(lease_id,lease_revision)
			  REFERENCES control_capability_leases(lease_id,revision)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_lease_actions_after_seal
			 BEFORE INSERT ON control_capability_lease_actions
			 WHEN EXISTS(SELECT 1 FROM control_capability_lease_seals
			  WHERE lease_id=NEW.lease_id AND lease_revision=NEW.lease_revision)
			 BEGIN SELECT RAISE(ABORT,'control lease actions are sealed'); END`,
			`CREATE TRIGGER trg_control_lease_seal_complete
			 BEFORE INSERT ON control_capability_lease_seals
			 WHEN NEW.sealed_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      (SELECT COUNT(*) FROM control_capability_lease_actions
			       WHERE lease_id=NEW.lease_id AND lease_revision=NEW.lease_revision)<>NEW.action_count OR
			      NOT EXISTS(SELECT 1 FROM control_capability_leases lease_row
			       WHERE lease_row.lease_id=NEW.lease_id AND lease_row.revision=NEW.lease_revision
			        AND lease_row.binding_digest=NEW.binding_digest AND lease_row.action_set_digest=NEW.action_set_digest
			        AND lease_row.action_count=NEW.action_count) OR
			      NOT EXISTS(
			       SELECT 1 FROM control_capability_leases lease_row
			       JOIN issues issue ON issue.id=lease_row.root_issue_id AND issue.deleted_at IS NULL
			       JOIN projects project ON project.id=lease_row.project_id AND project.id=issue.project_id
			       WHERE lease_row.lease_id=NEW.lease_id AND lease_row.revision=NEW.lease_revision
			        AND (project.status IN ('active','frozen') OR
			             (project.status='archived' AND NOT EXISTS(
			              SELECT 1 FROM control_capability_lease_actions action
			              WHERE action.lease_id=lease_row.lease_id AND action.lease_revision=lease_row.revision
			               AND action.action<>'run.cancel.running'))))
			 BEGIN SELECT RAISE(ABORT,'control lease seal is incomplete'); END`,
			`CREATE TRIGGER trg_control_lease_seals_no_update
			 BEFORE UPDATE ON control_capability_lease_seals
			 BEGIN SELECT RAISE(ABORT,'control lease seals are immutable'); END`,
			`CREATE TRIGGER trg_control_lease_seals_no_delete
			 BEFORE DELETE ON control_capability_lease_seals
			 BEGIN SELECT RAISE(ABORT,'control lease seals are immutable'); END`,
			`CREATE TRIGGER trg_control_leases_binding_guard
			 BEFORE UPDATE ON control_capability_leases
			 WHEN NOT EXISTS(SELECT 1 FROM control_capability_lease_seals WHERE lease_id=OLD.lease_id AND lease_revision=OLD.revision) OR
			  NEW.lease_id IS NOT OLD.lease_id OR NEW.revision IS NOT OLD.revision OR
			  NEW.actor_user_id IS NOT OLD.actor_user_id OR NEW.user_id IS NOT OLD.user_id OR
			  NEW.principal_kind IS NOT OLD.principal_kind OR
			  NEW.actor_session_credential_id IS NOT OLD.actor_session_credential_id OR NEW.actor_api_key_id IS NOT OLD.actor_api_key_id OR
			  NEW.device_id IS NOT OLD.device_id OR NEW.delivery_id IS NOT OLD.delivery_id OR
			  NEW.delivery_key IS NOT OLD.delivery_key OR NEW.delivery_revision IS NOT OLD.delivery_revision OR
			  NEW.project_id IS NOT OLD.project_id OR NEW.root_issue_id IS NOT OLD.root_issue_id OR NEW.issue_revision IS NOT OLD.issue_revision OR
			  NEW.attempt_id IS NOT OLD.attempt_id OR NEW.attempt_number IS NOT OLD.attempt_number OR
			  NEW.plan_revision IS NOT OLD.plan_revision OR NEW.stage_key IS NOT OLD.stage_key OR
			  NEW.execution_number IS NOT OLD.execution_number OR NEW.execution_start_stage_event_id IS NOT OLD.execution_start_stage_event_id OR NEW.authority_epoch IS NOT OLD.authority_epoch OR
			  NEW.authority_stage_event_id IS NOT OLD.authority_stage_event_id OR NEW.reporter_id IS NOT OLD.reporter_id OR
			  NEW.agent_run_id IS NOT OLD.agent_run_id OR NEW.binding_digest IS NOT OLD.binding_digest OR
			  NEW.action_set_digest IS NOT OLD.action_set_digest OR NEW.action_count IS NOT OLD.action_count OR NEW.created_at IS NOT OLD.created_at OR
			  NEW.expires_at<OLD.expires_at OR NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.updated_at<OLD.updated_at OR
			  (OLD.revoked_at IS NULL AND NEW.revoked_at IS NULL AND
			   (NEW.expires_at<=OLD.expires_at OR strftime('%Y-%m-%dT%H:%M:%fZ','now')>=OLD.expires_at)) OR
			  (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL AND
			   (NEW.expires_at IS NOT OLD.expires_at OR NEW.revoked_at IS NOT NEW.updated_at OR NEW.revoked_at<OLD.updated_at)) OR
			  (OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS NOT OLD.revoked_at OR NEW.updated_at IS NOT OLD.updated_at)) OR
			  (OLD.revoked_at IS NOT NULL AND NEW.expires_at IS NOT OLD.expires_at)
			 BEGIN SELECT RAISE(ABORT,'control lease binding is immutable'); END`,
			`CREATE TRIGGER trg_control_leases_no_delete
			 BEFORE DELETE ON control_capability_leases
			 BEGIN SELECT RAISE(ABORT,'control leases are retained'); END`,

			`CREATE TABLE control_input_requests (
			 request_id         TEXT NOT NULL CHECK(` + sqlUUIDCheck("request_id") + `),
			 revision           INTEGER NOT NULL CHECK(revision>0),
			 lease_id           TEXT NOT NULL,
			 lease_revision     INTEGER NOT NULL CHECK(lease_revision>0),
			 delivery_id        INTEGER NOT NULL CHECK(delivery_id>0),
			 delivery_key       TEXT NOT NULL CHECK(` + sqlStableKeyCheck("delivery_key", 80) + `),
			 delivery_revision  INTEGER NOT NULL CHECK(delivery_revision>0),
			 project_id         INTEGER NOT NULL CHECK(project_id>0),
			 root_issue_id      INTEGER NOT NULL CHECK(root_issue_id>0),
			 issue_revision     INTEGER NOT NULL CHECK(issue_revision>0),
			 attempt_id         INTEGER NOT NULL CHECK(attempt_id>0),
			 attempt_number     INTEGER NOT NULL CHECK(attempt_number>0),
			 plan_revision      INTEGER NOT NULL CHECK(plan_revision>0),
			 stage_key          TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number   INTEGER NOT NULL CHECK(execution_number>0),
			 execution_start_stage_event_id INTEGER NOT NULL CHECK(execution_start_stage_event_id>0),
			 authority_epoch    INTEGER NOT NULL CHECK(authority_epoch>0),
			 authority_stage_event_id INTEGER NOT NULL CHECK(authority_stage_event_id>0),
			 reporter_id        INTEGER NOT NULL CHECK(reporter_id>0),
			 agent_run_id       INTEGER NOT NULL CHECK(agent_run_id>0),
			 request_kind       TEXT NOT NULL CHECK(request_kind IN (` + sqlEnum(controlcontract.InputKinds()) + `)),
			 prompt_template    TEXT NOT NULL CHECK(prompt_template IN (` + sqlEnum(controlcontract.InputPromptTemplates()) + `)),
			 option_count       INTEGER NOT NULL CHECK(option_count BETWEEN 0 AND 8),
			 request_digest     BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 expires_at         TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("expires_at") + `),
			 created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 PRIMARY KEY(request_id,revision),
			 FOREIGN KEY(lease_id,lease_revision)
			  REFERENCES control_capability_leases(lease_id,revision),
			 CHECK((request_kind='approval' AND prompt_template='approval_required' AND option_count=0) OR
			       (request_kind='choice' AND prompt_template='choice_required' AND option_count BETWEEN 1 AND 8)),
			 CHECK(expires_at>created_at)
			) WITHOUT ROWID`,
			`CREATE INDEX idx_control_inputs_run
			 ON control_input_requests(agent_run_id,revision DESC)`,
			`CREATE INDEX idx_control_inputs_expiry ON control_input_requests(expires_at)`,
			`CREATE TRIGGER trg_control_input_current_binding_guard
			 BEFORE INSERT ON control_input_requests
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NEW.expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NOT EXISTS(
			  SELECT 1 FROM control_capability_leases lease
			  JOIN control_capability_lease_seals seal ON seal.lease_id=lease.lease_id AND seal.lease_revision=lease.revision
			  JOIN control_capability_lease_actions lease_action ON lease_action.lease_id=lease.lease_id
			   AND lease_action.lease_revision=lease.revision AND lease_action.action='input.respond'
			  WHERE lease.lease_id=NEW.lease_id AND lease.revision=NEW.lease_revision AND lease.revoked_at IS NULL
			   AND lease.created_at<=NEW.created_at AND lease.expires_at>=NEW.expires_at
			   AND lease.delivery_id=NEW.delivery_id AND lease.delivery_key=NEW.delivery_key
			   AND lease.delivery_revision=NEW.delivery_revision AND lease.project_id=NEW.project_id
			   AND lease.root_issue_id=NEW.root_issue_id AND lease.issue_revision=NEW.issue_revision
			   AND lease.attempt_id=NEW.attempt_id AND lease.attempt_number=NEW.attempt_number AND lease.plan_revision=NEW.plan_revision
			   AND lease.stage_key=NEW.stage_key AND lease.execution_number=NEW.execution_number
			   AND lease.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			   AND lease.authority_epoch=NEW.authority_epoch AND lease.authority_stage_event_id=NEW.authority_stage_event_id
			   AND lease.reporter_id=NEW.reporter_id AND lease.agent_run_id=NEW.agent_run_id)
			 BEGIN SELECT RAISE(ABORT,'control input binding is stale'); END`,
			`CREATE TABLE control_input_request_options (
			 request_id       TEXT NOT NULL,
			 request_revision INTEGER NOT NULL CHECK(request_revision>0),
			 ordinal          INTEGER NOT NULL CHECK(ordinal BETWEEN 1 AND 8),
			 option_code      TEXT NOT NULL CHECK(option_code IN (` + sqlEnum(controlcontract.InputOptionCodes()) + `)),
			 PRIMARY KEY(request_id,request_revision,ordinal),
			 UNIQUE(request_id,request_revision,option_code),
			 FOREIGN KEY(request_id,request_revision)
			  REFERENCES control_input_requests(request_id,revision) ON DELETE CASCADE,
			 CHECK(option_code='choice_'||ordinal)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_input_option_bound
			 BEFORE INSERT ON control_input_request_options
			 WHEN NEW.ordinal>(SELECT option_count FROM control_input_requests
			  WHERE request_id=NEW.request_id AND revision=NEW.request_revision)
			 BEGIN SELECT RAISE(ABORT,'input option exceeds declared count'); END`,
			`CREATE TABLE control_input_request_seals (
			 request_id       TEXT NOT NULL,
			 request_revision INTEGER NOT NULL CHECK(request_revision>0),
			 sealed_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("sealed_at") + `),
			 PRIMARY KEY(request_id,request_revision),
			 FOREIGN KEY(request_id,request_revision)
			  REFERENCES control_input_requests(request_id,revision) ON DELETE CASCADE
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_control_input_options_after_seal
			 BEFORE INSERT ON control_input_request_options
			 WHEN EXISTS(SELECT 1 FROM control_input_request_seals
			  WHERE request_id=NEW.request_id AND request_revision=NEW.request_revision)
			 BEGIN SELECT RAISE(ABORT,'control input options are sealed'); END`,
			`CREATE TRIGGER trg_control_input_seal_complete
			 BEFORE INSERT ON control_input_request_seals
			 WHEN NEW.sealed_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      (SELECT COUNT(*) FROM control_input_request_options
			       WHERE request_id=NEW.request_id AND request_revision=NEW.request_revision)<>
			      (SELECT option_count FROM control_input_requests
			       WHERE request_id=NEW.request_id AND revision=NEW.request_revision)
			 BEGIN SELECT RAISE(ABORT,'input options are incomplete'); END`,
			`CREATE TRIGGER trg_control_input_requests_no_update
			 BEFORE UPDATE ON control_input_requests
			 BEGIN SELECT RAISE(ABORT,'control input requests are immutable'); END`,
			`CREATE TRIGGER trg_control_input_requests_no_delete
			 BEFORE DELETE ON control_input_requests
			 BEGIN SELECT RAISE(ABORT,'control input requests are immutable'); END`,
			`CREATE TRIGGER trg_control_input_options_no_update
			 BEFORE UPDATE ON control_input_request_options
			 BEGIN SELECT RAISE(ABORT,'control input options are immutable'); END`,
			`CREATE TRIGGER trg_control_input_options_no_delete
			 BEFORE DELETE ON control_input_request_options
			 BEGIN SELECT RAISE(ABORT,'control input options are immutable'); END`,
			`CREATE TRIGGER trg_control_input_seals_no_update
			 BEFORE UPDATE ON control_input_request_seals
			 BEGIN SELECT RAISE(ABORT,'control input seals are immutable'); END`,
			`CREATE TRIGGER trg_control_input_seals_no_delete
			 BEFORE DELETE ON control_input_request_seals
			 BEGIN SELECT RAISE(ABORT,'control input seals are immutable'); END`,

			`CREATE TABLE control_input_resolution_events (
			 id               INTEGER PRIMARY KEY AUTOINCREMENT,
			 request_id       TEXT NOT NULL,
			 request_revision INTEGER NOT NULL CHECK(request_revision>0),
			 sequence         INTEGER NOT NULL CHECK(sequence>0),
			 event_kind       TEXT NOT NULL CHECK(event_kind IN (` + sqlEnum(controlcontract.InputTerminalEventKinds()) + `)),
			 choice_ordinal   INTEGER CHECK(choice_ordinal BETWEEN 1 AND 8),
			 choice_code      TEXT CHECK(choice_code IS NULL OR choice_code IN (` + sqlEnum(controlcontract.InputOptionCodes()) + `)),
			 event_digest     BLOB NOT NULL CHECK(typeof(event_digest)='blob' AND length(event_digest)=32),
			 safe_reason      TEXT CHECK(safe_reason IS NULL OR safe_reason IN (` + sqlEnum(controlcontract.SafeReasons()) + `)),
			 command_id       TEXT CHECK(command_id IS NULL OR ` + sqlUUIDCheck("command_id") + `),
			 created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(id,request_id,request_revision),
			 UNIQUE(request_id,request_revision),
			 UNIQUE(request_id,sequence),
			 FOREIGN KEY(request_id,request_revision)
			  REFERENCES control_input_requests(request_id,revision),
			 CHECK(COALESCE(((event_kind='choice' AND choice_ordinal IS NOT NULL AND choice_code='choice_'||choice_ordinal) OR
			       (event_kind<>'choice' AND choice_ordinal IS NULL AND choice_code IS NULL)),0)),
			 CHECK(COALESCE(((event_kind IN ('approve','reject','choice') AND command_id IS NOT NULL AND safe_reason IS NULL) OR
			       (event_kind='superseded' AND command_id IS NULL AND safe_reason='input_superseded') OR
			       (event_kind='expired' AND command_id IS NULL AND safe_reason='input_expired') OR
			       (event_kind='run_terminal' AND command_id IS NULL AND safe_reason='run_terminal') OR
			       (event_kind='cancelled' AND command_id IS NULL AND safe_reason='cancelled')),0))
			)`,
			`CREATE TRIGGER trg_control_input_resolution_kind_guard
			 BEFORE INSERT ON control_input_resolution_events
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NOT EXISTS(SELECT 1 FROM control_input_request_seals
			       WHERE request_id=NEW.request_id AND request_revision=NEW.request_revision) OR
			      NOT EXISTS(SELECT 1 FROM control_input_request_states state
			       WHERE state.request_id=NEW.request_id AND state.current_revision=NEW.request_revision
			        AND state.terminal_event_id IS NULL) OR
			      NEW.sequence<>NEW.request_revision OR
			      (NEW.event_kind IN ('approve','reject') AND
			       (SELECT request_kind FROM control_input_requests WHERE request_id=NEW.request_id AND revision=NEW.request_revision)<>'approval') OR
			      (NEW.event_kind='choice' AND NOT EXISTS(
			       SELECT 1 FROM control_input_request_options option
			       WHERE option.request_id=NEW.request_id AND option.request_revision=NEW.request_revision
			        AND option.ordinal=NEW.choice_ordinal AND option.option_code=NEW.choice_code)) OR
			      (NEW.event_kind IN ('approve','reject','choice') AND NOT EXISTS(
			       SELECT 1 FROM control_commands command
			       JOIN control_input_requests request ON request.request_id=NEW.request_id AND request.revision=NEW.request_revision
			       WHERE command.command_id=NEW.command_id AND command.action='input.respond' AND command.status='applied'
			        AND command.outcome='applied' AND command.result_digest=NEW.event_digest
			        AND command.input_request_id=request.request_id AND command.input_request_revision=request.revision
			        AND command.input_request_expires_at=request.expires_at
			        AND command.input_response_kind=NEW.event_kind
			        AND command.input_choice_ordinal IS NEW.choice_ordinal AND command.input_choice_code IS NEW.choice_code
			        AND command.lease_id=request.lease_id AND command.lease_revision=request.lease_revision
			        AND command.delivery_id=request.delivery_id AND command.delivery_key=request.delivery_key
			        AND command.delivery_revision=request.delivery_revision AND command.project_id=request.project_id
			        AND command.root_issue_id=request.root_issue_id AND command.issue_revision=request.issue_revision
			        AND command.attempt_id=request.attempt_id AND command.attempt_number=request.attempt_number
			        AND command.plan_revision=request.plan_revision AND command.stage_key=request.stage_key
			        AND command.execution_number=request.execution_number
			        AND command.execution_start_stage_event_id=request.execution_start_stage_event_id
			        AND command.authority_epoch=request.authority_epoch
			        AND command.authority_stage_event_id=request.authority_stage_event_id
			        AND command.reporter_id=request.reporter_id AND command.agent_run_id=request.agent_run_id)) OR
			      (NEW.event_kind='expired' AND NEW.created_at<(
			       SELECT expires_at FROM control_input_requests WHERE request_id=NEW.request_id AND revision=NEW.request_revision)) OR
			      (NEW.event_kind='run_terminal' AND NOT EXISTS(
			       SELECT 1 FROM control_input_requests request JOIN agent_runs run ON run.id=request.agent_run_id
			       WHERE request.request_id=NEW.request_id AND request.revision=NEW.request_revision
			        AND run.status NOT IN ('queued','running') AND run.finished_at IS NOT NULL)) OR
			      (NEW.event_kind='cancelled' AND NOT EXISTS(
			       SELECT 1 FROM control_input_requests request JOIN agent_run_cancellation_facts fact ON fact.run_id=request.agent_run_id
			       WHERE request.request_id=NEW.request_id AND request.revision=NEW.request_revision))
			 BEGIN SELECT RAISE(ABORT,'input resolution does not match request'); END`,
			`CREATE TRIGGER trg_control_input_resolutions_no_update
			 BEFORE UPDATE ON control_input_resolution_events
			 BEGIN SELECT RAISE(ABORT,'control input resolutions are append-only'); END`,
			`CREATE TRIGGER trg_control_input_resolutions_no_delete
			 BEFORE DELETE ON control_input_resolution_events
			 BEGIN SELECT RAISE(ABORT,'control input resolutions are append-only'); END`,
			`CREATE TABLE control_input_request_states (
			 request_id        TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("request_id") + `),
			 current_revision  INTEGER NOT NULL CHECK(current_revision>0),
			 state_revision    INTEGER NOT NULL CHECK(state_revision>0),
			 terminal_event_id INTEGER UNIQUE,
			 updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 FOREIGN KEY(request_id,current_revision)
			  REFERENCES control_input_requests(request_id,revision),
			 FOREIGN KEY(terminal_event_id,request_id,current_revision)
			  REFERENCES control_input_resolution_events(id,request_id,request_revision),
			 CHECK((state_revision=1 AND current_revision=1 AND terminal_event_id IS NULL) OR state_revision>1)
			)`,
			`CREATE TRIGGER trg_control_input_request_revision_guard
			 BEFORE INSERT ON control_input_requests
			 WHEN (NOT EXISTS(SELECT 1 FROM control_input_requests WHERE request_id=NEW.request_id) AND NEW.revision<>1) OR
			      (EXISTS(SELECT 1 FROM control_input_requests WHERE request_id=NEW.request_id) AND
			       (NEW.revision<>(SELECT MAX(revision)+1 FROM control_input_requests WHERE request_id=NEW.request_id) OR
			        NOT EXISTS(SELECT 1 FROM control_input_requests lineage
			         WHERE lineage.request_id=NEW.request_id AND lineage.revision=1
			          AND lineage.delivery_id=NEW.delivery_id AND lineage.delivery_key=NEW.delivery_key
			          AND lineage.root_issue_id=NEW.root_issue_id AND lineage.attempt_id=NEW.attempt_id
			          AND lineage.attempt_number=NEW.attempt_number AND lineage.plan_revision=NEW.plan_revision
			          AND lineage.stage_key=NEW.stage_key AND lineage.execution_number=NEW.execution_number
			          AND lineage.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			          AND lineage.agent_run_id=NEW.agent_run_id AND lineage.request_kind=NEW.request_kind
			          AND lineage.prompt_template=NEW.prompt_template) OR
			        NOT EXISTS(SELECT 1 FROM control_input_request_seals seal
			         WHERE seal.request_id=NEW.request_id AND seal.request_revision=(SELECT MAX(revision) FROM control_input_requests WHERE request_id=NEW.request_id)) OR
			        NOT EXISTS(SELECT 1 FROM control_input_request_states state
			         JOIN control_input_resolution_events terminal ON terminal.id=state.terminal_event_id
			          AND terminal.request_id=state.request_id AND terminal.request_revision=state.current_revision
			         WHERE state.request_id=NEW.request_id AND terminal.event_kind='superseded'
			          AND state.current_revision=(SELECT MAX(revision) FROM control_input_requests WHERE request_id=NEW.request_id))))
			 BEGIN SELECT RAISE(ABORT,'invalid control input revision'); END`,
			`CREATE TRIGGER trg_control_input_state_insert_guard
			 BEFORE INSERT ON control_input_request_states
			 WHEN NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NEW.state_revision<>1 OR NEW.current_revision<>1 OR NEW.terminal_event_id IS NOT NULL OR
			      NOT EXISTS(SELECT 1 FROM control_input_request_seals WHERE request_id=NEW.request_id AND request_revision=1)
			 BEGIN SELECT RAISE(ABORT,'invalid control input state'); END`,
			`CREATE TRIGGER trg_control_input_state_transition_guard
			 BEFORE UPDATE ON control_input_request_states
			 WHEN NEW.request_id IS NOT OLD.request_id OR NEW.state_revision<>OLD.state_revision+1 OR
			      NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.updated_at<OLD.updated_at OR
			      NOT ((NEW.current_revision=OLD.current_revision AND OLD.terminal_event_id IS NULL AND NEW.terminal_event_id IS NOT NULL) OR
			           (NEW.current_revision=OLD.current_revision+1 AND OLD.terminal_event_id IS NOT NULL AND NEW.terminal_event_id IS NULL AND
			            EXISTS(SELECT 1 FROM control_input_resolution_events terminal
			             WHERE terminal.id=OLD.terminal_event_id AND terminal.request_id=OLD.request_id
			              AND terminal.request_revision=OLD.current_revision AND terminal.event_kind='superseded') AND
			            EXISTS(SELECT 1 FROM control_input_request_seals WHERE request_id=NEW.request_id AND request_revision=NEW.current_revision)))
			 BEGIN SELECT RAISE(ABORT,'invalid control input state transition'); END`,
			`CREATE TRIGGER trg_control_input_state_no_delete
			 BEFORE DELETE ON control_input_request_states
			 BEGIN SELECT RAISE(ABORT,'control input state is retained'); END`,

			`CREATE TABLE control_runtime_states (
			 agent_run_id       INTEGER PRIMARY KEY CHECK(agent_run_id>0),
			 delivery_id        INTEGER NOT NULL CHECK(delivery_id>0),
			 root_issue_id      INTEGER NOT NULL CHECK(root_issue_id>0),
			 attempt_id         INTEGER NOT NULL CHECK(attempt_id>0),
			 stage_key          TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number   INTEGER NOT NULL CHECK(execution_number>0),
			 execution_start_stage_event_id INTEGER NOT NULL CHECK(execution_start_stage_event_id>0),
			 state              TEXT NOT NULL CHECK(state IN (` + sqlEnum(controlcontract.RuntimeStates()) + `)),
			 revision           INTEGER NOT NULL CHECK(revision>0),
			 last_command_id    TEXT CHECK(last_command_id IS NULL OR ` + sqlUUIDCheck("last_command_id") + `),
			 last_result_digest BLOB CHECK(last_result_digest IS NULL OR
			  (typeof(last_result_digest)='blob' AND length(last_result_digest)=32)),
			 created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 CHECK((revision=1 AND state='running' AND last_command_id IS NULL AND last_result_digest IS NULL) OR
			       (revision>1 AND last_command_id IS NOT NULL AND last_result_digest IS NOT NULL))
			)`,
			`CREATE INDEX idx_control_runtime_binding
			 ON control_runtime_states(delivery_id,attempt_id,stage_key,execution_number,execution_start_stage_event_id)`,
			`CREATE TRIGGER trg_control_runtime_insert_guard
			 BEFORE INSERT ON control_runtime_states
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.updated_at IS NOT NEW.created_at OR
			      NEW.revision<>1 OR NEW.state<>'running' OR NEW.last_command_id IS NOT NULL OR NEW.last_result_digest IS NOT NULL OR
			      NOT EXISTS(
			       SELECT 1 FROM control_capability_leases lease
			       JOIN control_capability_lease_seals seal ON seal.lease_id=lease.lease_id AND seal.lease_revision=lease.revision
			       JOIN agent_runs run ON run.id=lease.agent_run_id AND run.status='running'
			       WHERE lease.revoked_at IS NULL AND lease.expires_at>NEW.created_at AND lease.agent_run_id=NEW.agent_run_id
			        AND lease.delivery_id=NEW.delivery_id AND lease.root_issue_id=NEW.root_issue_id
			        AND lease.attempt_id=NEW.attempt_id AND lease.stage_key=NEW.stage_key
			        AND lease.execution_number=NEW.execution_number
			        AND lease.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			        AND EXISTS(SELECT 1 FROM control_capability_lease_actions action
			         WHERE action.lease_id=lease.lease_id AND action.lease_revision=lease.revision AND action.action='run.pause')
			        AND EXISTS(SELECT 1 FROM control_capability_lease_actions action
			         WHERE action.lease_id=lease.lease_id AND action.lease_revision=lease.revision AND action.action='run.resume'))
			 BEGIN SELECT RAISE(ABORT,'invalid initial runtime control state'); END`,
			`CREATE TRIGGER trg_control_runtime_transition_guard
			 BEFORE UPDATE ON control_runtime_states
			 WHEN NEW.agent_run_id IS NOT OLD.agent_run_id OR NEW.delivery_id IS NOT OLD.delivery_id OR
			  NEW.root_issue_id IS NOT OLD.root_issue_id OR NEW.attempt_id IS NOT OLD.attempt_id OR
			  NEW.stage_key IS NOT OLD.stage_key OR NEW.execution_number IS NOT OLD.execution_number OR
			  NEW.execution_start_stage_event_id IS NOT OLD.execution_start_stage_event_id OR
			  NEW.created_at IS NOT OLD.created_at OR NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  NEW.updated_at<OLD.updated_at OR NEW.revision<>OLD.revision+1 OR NEW.state=OLD.state OR
			  NEW.last_command_id IS NULL OR NEW.last_result_digest IS NULL
			 BEGIN SELECT RAISE(ABORT,'invalid runtime control transition'); END`,
			`CREATE TRIGGER trg_control_runtime_no_delete
			 BEFORE DELETE ON control_runtime_states
			 BEGIN SELECT RAISE(ABORT,'control runtime state is retained'); END`,

			`CREATE TABLE control_commands (
			 command_id          TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("command_id") + `),
			 status_revision     INTEGER NOT NULL DEFAULT 1 CHECK(status_revision>0),
			 actor_user_id       INTEGER NOT NULL CHECK(actor_user_id>0),
			 user_id             INTEGER NOT NULL CHECK(user_id>0),
			 principal_kind      TEXT NOT NULL CHECK(principal_kind IN ('session','api_key')),
			 actor_session_credential_id TEXT,
			 actor_api_key_id    INTEGER,
			 canonical_digest    BLOB NOT NULL CHECK(typeof(canonical_digest)='blob' AND length(canonical_digest)=32),
			 grant_id            TEXT NOT NULL,
			 grant_revision      INTEGER NOT NULL CHECK(grant_revision>0),
			 grant_expires_at    TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("grant_expires_at") + `),
			 grant_binding_digest BLOB NOT NULL CHECK(typeof(grant_binding_digest)='blob' AND length(grant_binding_digest)=32),
			 grant_action_digest BLOB NOT NULL CHECK(typeof(grant_action_digest)='blob' AND length(grant_action_digest)=32),
			 action              TEXT NOT NULL CHECK(action IN (` + sqlEnum(controlcontract.Actions()) + `)),
			 status              TEXT NOT NULL CHECK(status IN (` + sqlEnum(controlcontract.CommandStatuses()) + `)),
			 outcome             TEXT CHECK(outcome IS NULL OR outcome IN (` + sqlEnum(controlcontract.SafeOutcomes()) + `)),
			 safe_reason         TEXT CHECK(safe_reason IS NULL OR safe_reason IN (` + sqlEnum(controlcontract.SafeReasons()) + `)),
			 result_digest       BLOB CHECK(result_digest IS NULL OR (typeof(result_digest)='blob' AND length(result_digest)=32)),
			 challenge_template  TEXT NOT NULL CHECK(challenge_template IN (` + sqlEnum(controlcontract.ChallengeTemplates()) + `)),
			 delivery_id         INTEGER NOT NULL CHECK(delivery_id>0),
			 delivery_key        TEXT NOT NULL CHECK(` + sqlStableKeyCheck("delivery_key", 80) + `),
			 delivery_revision   INTEGER NOT NULL CHECK(delivery_revision>0),
			 project_id          INTEGER NOT NULL CHECK(project_id>0),
			 root_issue_id       INTEGER NOT NULL CHECK(root_issue_id>0),
			 issue_revision      INTEGER NOT NULL CHECK(issue_revision>0),
			 issue_etag_digest   BLOB NOT NULL CHECK(typeof(issue_etag_digest)='blob' AND length(issue_etag_digest)=32),
			 target_snapshot_digest BLOB NOT NULL CHECK(typeof(target_snapshot_digest)='blob' AND length(target_snapshot_digest)=32),
			 attempt_id          INTEGER CHECK(attempt_id>0),
			 attempt_number      INTEGER CHECK(attempt_number>0),
			 plan_revision       INTEGER CHECK(plan_revision>0),
			 stage_key           TEXT CHECK(stage_key IS NULL OR stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number    INTEGER CHECK(execution_number>0),
			 execution_start_stage_event_id INTEGER CHECK(execution_start_stage_event_id>0),
			 authority_epoch     INTEGER CHECK(authority_epoch>0),
			 authority_stage_event_id INTEGER CHECK(authority_stage_event_id>0),
			 reporter_id         INTEGER CHECK(reporter_id>0),
			 agent_run_id        INTEGER CHECK(agent_run_id>0),
			 lease_id            TEXT CHECK(lease_id IS NULL OR ` + sqlUUIDCheck("lease_id") + `),
			 lease_revision      INTEGER CHECK(lease_revision>0),
			 lease_expires_at    TEXT CHECK(` + sqlNullableControlTimestampCheck("lease_expires_at") + `),
			 lease_binding_digest BLOB CHECK(lease_binding_digest IS NULL OR
			  (typeof(lease_binding_digest)='blob' AND length(lease_binding_digest)=32)),
			 lease_action_digest BLOB CHECK(lease_action_digest IS NULL OR
			  (typeof(lease_action_digest)='blob' AND length(lease_action_digest)=32)),
			 input_request_id    TEXT CHECK(input_request_id IS NULL OR ` + sqlUUIDCheck("input_request_id") + `),
			 input_request_revision INTEGER CHECK(input_request_revision>0),
			 input_request_expires_at TEXT CHECK(` + sqlNullableControlTimestampCheck("input_request_expires_at") + `),
			 runtime_revision    INTEGER CHECK(runtime_revision>0),
			 priority_value      TEXT CHECK(priority_value IS NULL OR priority_value IN ('low','medium','high')),
			 input_response_kind TEXT CHECK(input_response_kind IS NULL OR input_response_kind IN ('approve','reject','choice')),
			 input_choice_ordinal INTEGER CHECK(input_choice_ordinal BETWEEN 1 AND 8),
			 input_choice_code   TEXT CHECK(input_choice_code IS NULL OR input_choice_code IN (` + sqlEnum(controlcontract.InputOptionCodes()) + `)),
			 parameter_digest    BLOB NOT NULL CHECK(typeof(parameter_digest)='blob' AND length(parameter_digest)=32),
			 expires_at          TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("expires_at") + ` AND expires_at<=grant_expires_at),
			 accepted_at         TEXT CHECK(` + sqlNullableControlTimestampCheck("accepted_at") + `),
			 terminal_at         TEXT CHECK(` + sqlNullableControlTimestampCheck("terminal_at") + `),
			 created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 FOREIGN KEY(grant_id,grant_revision)
			  REFERENCES control_capability_grants(grant_id,revision),
			 CHECK(actor_user_id=user_id),
			 CHECK(` + sqlTypedPrincipalCheck("principal_kind", "actor_session_credential_id", "actor_api_key_id") + `),
			 CHECK(expires_at>created_at),
			 CHECK(lease_expires_at IS NULL OR expires_at<=lease_expires_at),
			 CHECK(input_request_expires_at IS NULL OR expires_at<=input_request_expires_at),
			 CHECK(updated_at>=created_at),
			 CHECK(COALESCE(((status='pending_confirmation' AND status_revision=1 AND outcome IS NULL AND safe_reason IS NULL AND result_digest IS NULL AND accepted_at IS NULL AND terminal_at IS NULL) OR
			       (status='accepted' AND accepted_at>=created_at AND accepted_at<expires_at AND terminal_at IS NULL AND
			        result_digest IS NULL AND ((outcome IS NULL AND safe_reason IS NULL) OR (outcome='outcome_unknown' AND safe_reason='runner_lost'))) OR
			       (status='applied' AND outcome='applied' AND safe_reason IS NULL AND result_digest IS NOT NULL AND accepted_at>=created_at AND accepted_at<expires_at AND terminal_at>=accepted_at) OR
			       (status='rejected' AND outcome='rejected' AND safe_reason IS NOT NULL
			        AND safe_reason NOT IN ('withdrawn','confirmation_expired','runner_lost')
			        AND result_digest IS NOT NULL AND accepted_at>=created_at AND accepted_at<expires_at AND terminal_at>=accepted_at) OR
			       (status='expired' AND outcome IS NULL AND safe_reason='withdrawn' AND result_digest IS NULL AND accepted_at IS NULL AND terminal_at>=created_at AND terminal_at<expires_at) OR
			       (status='expired' AND outcome IS NULL AND safe_reason='confirmation_expired' AND result_digest IS NULL AND accepted_at IS NULL AND terminal_at>=expires_at)),0)),
			 CHECK(COALESCE(((action='issue.priority.set' AND challenge_template='issue_priority_set' AND priority_value IS NOT NULL AND
			        attempt_id IS NULL AND attempt_number IS NULL AND plan_revision IS NULL AND stage_key IS NULL AND execution_number IS NULL AND
			        execution_start_stage_event_id IS NULL AND authority_epoch IS NULL AND authority_stage_event_id IS NULL AND reporter_id IS NULL AND agent_run_id IS NULL AND
			        lease_id IS NULL AND lease_revision IS NULL AND lease_expires_at IS NULL AND lease_binding_digest IS NULL AND lease_action_digest IS NULL AND
			        input_request_id IS NULL AND input_request_revision IS NULL AND input_request_expires_at IS NULL AND runtime_revision IS NULL AND input_response_kind IS NULL AND input_choice_ordinal IS NULL AND input_choice_code IS NULL) OR
			       (action='run.cancel.queued' AND challenge_template='run_cancel_queued' AND priority_value IS NULL AND
			        attempt_id IS NOT NULL AND attempt_number IS NOT NULL AND plan_revision IS NOT NULL AND stage_key IS NOT NULL AND execution_number IS NOT NULL AND
			        execution_start_stage_event_id IS NOT NULL AND authority_epoch IS NOT NULL AND authority_stage_event_id IS NOT NULL AND reporter_id IS NOT NULL AND agent_run_id IS NOT NULL AND
			        lease_id IS NULL AND lease_revision IS NULL AND lease_expires_at IS NULL AND lease_binding_digest IS NULL AND lease_action_digest IS NULL AND
			        input_request_id IS NULL AND input_request_revision IS NULL AND input_request_expires_at IS NULL AND runtime_revision IS NULL AND input_response_kind IS NULL AND input_choice_ordinal IS NULL AND input_choice_code IS NULL) OR
			       (action='run.cancel.running' AND challenge_template='run_cancel_running' AND priority_value IS NULL AND
			        attempt_id IS NOT NULL AND attempt_number IS NOT NULL AND plan_revision IS NOT NULL AND stage_key IS NOT NULL AND execution_number IS NOT NULL AND
			        execution_start_stage_event_id IS NOT NULL AND authority_epoch IS NOT NULL AND authority_stage_event_id IS NOT NULL AND reporter_id IS NOT NULL AND agent_run_id IS NOT NULL AND
			        lease_id IS NOT NULL AND lease_revision IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_binding_digest IS NOT NULL AND lease_action_digest IS NOT NULL AND
			        input_request_id IS NULL AND input_request_revision IS NULL AND input_request_expires_at IS NULL AND runtime_revision IS NULL AND input_response_kind IS NULL AND input_choice_ordinal IS NULL AND input_choice_code IS NULL) OR
			       (action='input.respond' AND challenge_template IN ('input_approve','input_reject','input_choice') AND priority_value IS NULL AND
			        attempt_id IS NOT NULL AND attempt_number IS NOT NULL AND plan_revision IS NOT NULL AND stage_key IS NOT NULL AND execution_number IS NOT NULL AND
			        execution_start_stage_event_id IS NOT NULL AND authority_epoch IS NOT NULL AND authority_stage_event_id IS NOT NULL AND reporter_id IS NOT NULL AND agent_run_id IS NOT NULL AND
			        lease_id IS NOT NULL AND lease_revision IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_binding_digest IS NOT NULL AND lease_action_digest IS NOT NULL AND
			        input_request_id IS NOT NULL AND input_request_revision IS NOT NULL AND input_request_expires_at IS NOT NULL AND runtime_revision IS NULL AND input_response_kind IS NOT NULL AND
			        ((input_response_kind='approve' AND challenge_template='input_approve' AND input_choice_ordinal IS NULL AND input_choice_code IS NULL) OR
			         (input_response_kind='reject' AND challenge_template='input_reject' AND input_choice_ordinal IS NULL AND input_choice_code IS NULL) OR
			         (input_response_kind='choice' AND challenge_template='input_choice' AND input_choice_ordinal IS NOT NULL AND input_choice_code='choice_'||input_choice_ordinal))) OR
			       (action IN ('run.pause','run.resume') AND challenge_template=CASE action WHEN 'run.pause' THEN 'run_pause' ELSE 'run_resume' END AND priority_value IS NULL AND
			        attempt_id IS NOT NULL AND attempt_number IS NOT NULL AND plan_revision IS NOT NULL AND stage_key IS NOT NULL AND execution_number IS NOT NULL AND
			        execution_start_stage_event_id IS NOT NULL AND authority_epoch IS NOT NULL AND authority_stage_event_id IS NOT NULL AND reporter_id IS NOT NULL AND agent_run_id IS NOT NULL AND
			        lease_id IS NOT NULL AND lease_revision IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_binding_digest IS NOT NULL AND lease_action_digest IS NOT NULL AND
			        input_request_id IS NULL AND input_request_revision IS NULL AND input_request_expires_at IS NULL AND runtime_revision IS NOT NULL AND input_response_kind IS NULL AND input_choice_ordinal IS NULL AND input_choice_code IS NULL)),0))
			)`,
			`CREATE INDEX idx_control_commands_subject ON control_commands(delivery_id,created_at DESC)`,
			`CREATE UNIQUE INDEX idx_control_commands_canonical ON control_commands(canonical_digest)`,
			`CREATE INDEX idx_control_commands_status ON control_commands(status,expires_at)`,
			`CREATE INDEX idx_control_commands_run ON control_commands(agent_run_id,status) WHERE agent_run_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_commands_consumed_input
			 ON control_commands(input_request_id,input_request_revision)
			 WHERE action='input.respond' AND status IN ('accepted','applied')`,
			`CREATE UNIQUE INDEX idx_control_commands_consumed_runtime
			 ON control_commands(agent_run_id,runtime_revision)
			 WHERE action IN ('run.pause','run.resume') AND status IN ('accepted','applied')`,
			`CREATE UNIQUE INDEX idx_control_commands_consumed_running_cancel
			 ON control_commands(delivery_id,attempt_id,stage_key,execution_number,execution_start_stage_event_id,agent_run_id)
			 WHERE action='run.cancel.running' AND status IN ('accepted','applied')`,
			`CREATE UNIQUE INDEX idx_control_commands_consumed_queued_cancel
			 ON control_commands(agent_run_id)
			 WHERE action='run.cancel.queued' AND status IN ('accepted','applied')`,
			`CREATE TRIGGER trg_control_commands_insert_guard
			 BEFORE INSERT ON control_commands
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.updated_at IS NOT NEW.created_at OR
			      NEW.expires_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			      NEW.status<>'pending_confirmation' OR NEW.status_revision<>1 OR NEW.outcome IS NOT NULL OR
			      NEW.safe_reason IS NOT NULL OR NEW.result_digest IS NOT NULL OR NEW.accepted_at IS NOT NULL OR NEW.terminal_at IS NOT NULL OR
			      NOT EXISTS(SELECT 1 FROM control_capability_grant_seals seal
			       WHERE seal.grant_id=NEW.grant_id AND seal.grant_revision=NEW.grant_revision) OR
			      NOT EXISTS(SELECT 1 FROM control_capability_grant_actions granted_action
			       WHERE granted_action.grant_id=NEW.grant_id AND granted_action.grant_revision=NEW.grant_revision
			        AND granted_action.action=NEW.action) OR
			      NOT EXISTS(SELECT 1 FROM control_capability_grants grant_row
			       WHERE grant_row.grant_id=NEW.grant_id AND grant_row.revision=NEW.grant_revision AND grant_row.revoked_at IS NULL
			        AND grant_row.created_at<=NEW.created_at AND grant_row.expires_at>NEW.created_at
			        AND grant_row.actor_user_id=NEW.actor_user_id AND grant_row.user_id=NEW.user_id
			        AND grant_row.principal_kind=NEW.principal_kind
			        AND grant_row.actor_session_credential_id IS NEW.actor_session_credential_id
			        AND grant_row.actor_api_key_id IS NEW.actor_api_key_id
			        AND grant_row.delivery_id=NEW.delivery_id AND grant_row.delivery_key=NEW.delivery_key
			        AND grant_row.delivery_revision=NEW.delivery_revision AND grant_row.project_id=NEW.project_id
			        AND grant_row.root_issue_id=NEW.root_issue_id AND grant_row.issue_revision=NEW.issue_revision
			        AND grant_row.issue_etag_digest=NEW.issue_etag_digest
			        AND grant_row.expires_at=NEW.grant_expires_at AND grant_row.binding_digest=NEW.grant_binding_digest
			        AND grant_row.action_set_digest=NEW.grant_action_digest)
			 BEGIN SELECT RAISE(ABORT,'invalid control command creation'); END`,
			`CREATE TRIGGER trg_control_commands_lease_binding_guard
			 BEFORE INSERT ON control_commands
			 WHEN NEW.action IN ('run.cancel.running','input.respond','run.pause','run.resume') AND NOT EXISTS(
			  SELECT 1 FROM control_capability_leases lease
			  JOIN control_capability_lease_seals seal ON seal.lease_id=lease.lease_id AND seal.lease_revision=lease.revision
			  JOIN control_capability_lease_actions lease_action ON lease_action.lease_id=lease.lease_id
			   AND lease_action.lease_revision=lease.revision AND lease_action.action=NEW.action
			   WHERE lease.lease_id=NEW.lease_id AND lease.revision=NEW.lease_revision AND lease.revoked_at IS NULL
			   AND lease.created_at<=NEW.created_at AND lease.expires_at>NEW.created_at
			   AND lease.expires_at=NEW.lease_expires_at AND lease.binding_digest=NEW.lease_binding_digest
			   AND lease.action_set_digest=NEW.lease_action_digest AND lease.delivery_id=NEW.delivery_id
			   AND lease.delivery_key=NEW.delivery_key AND lease.delivery_revision=NEW.delivery_revision AND lease.project_id=NEW.project_id
			   AND lease.root_issue_id=NEW.root_issue_id AND lease.issue_revision=NEW.issue_revision
			   AND lease.attempt_id=NEW.attempt_id AND lease.attempt_number=NEW.attempt_number AND lease.plan_revision=NEW.plan_revision
			   AND lease.stage_key=NEW.stage_key AND lease.execution_number=NEW.execution_number
			   AND lease.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			   AND lease.authority_epoch=NEW.authority_epoch AND lease.authority_stage_event_id=NEW.authority_stage_event_id
			   AND lease.reporter_id=NEW.reporter_id AND lease.agent_run_id=NEW.agent_run_id)
			 BEGIN SELECT RAISE(ABORT,'control command lease binding is stale'); END`,
			`CREATE TRIGGER trg_control_commands_target_binding_guard
			 BEFORE INSERT ON control_commands
			 WHEN NOT EXISTS(
			  SELECT 1 FROM deliveries delivery JOIN issues issue ON issue.id=delivery.issue_id
			  JOIN projects project ON project.id=issue.project_id
			  WHERE delivery.id=NEW.delivery_id AND delivery.delivery_key=NEW.delivery_key
			   AND delivery.issue_id=NEW.root_issue_id AND issue.project_id=NEW.project_id AND issue.deleted_at IS NULL
			   AND (SELECT revision FROM issue_control_revisions WHERE issue_id=issue.id)=NEW.issue_revision
			   AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			                WHERE event.delivery_id=delivery.id),0)=NEW.delivery_revision
			   AND (project.status IN ('active','frozen') OR
			        (project.status='archived' AND NEW.action IN ('run.cancel.queued','run.cancel.running')
			         AND NOT EXISTS(SELECT 1 FROM control_capability_grant_actions action
			          WHERE action.grant_id=NEW.grant_id AND action.grant_revision=NEW.grant_revision
			           AND action.action NOT IN ('run.cancel.queued','run.cancel.running'))
			         AND (NEW.action='run.cancel.queued' OR NOT EXISTS(
			          SELECT 1 FROM control_capability_lease_actions action
			          WHERE action.lease_id=NEW.lease_id AND action.lease_revision=NEW.lease_revision
			           AND action.action<>'run.cancel.running')))))
			 BEGIN SELECT RAISE(ABORT,'control command target is stale'); END`,
			`CREATE TRIGGER trg_control_commands_run_state_guard
			 BEFORE INSERT ON control_commands
			 WHEN (NEW.action='run.cancel.queued' AND NOT EXISTS(
			        SELECT 1 FROM agent_runs run
			        JOIN delivery_agent_run_links link ON link.agent_run_id=run.id AND link.delivery_id=NEW.delivery_id
			         AND link.attempt_id=NEW.attempt_id AND link.stage_key=NEW.stage_key
			         AND link.execution_number=NEW.execution_number AND link.reporter_id=NEW.reporter_id
			         AND link.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			        JOIN delivery_agent_run_activations activation ON activation.agent_run_id=run.id
			         AND activation.delivery_id=NEW.delivery_id AND activation.attempt_id=NEW.attempt_id
			         AND activation.stage_key=NEW.stage_key AND activation.execution_number=NEW.execution_number
			         AND activation.authority_epoch=NEW.authority_epoch AND activation.reporter_id=NEW.reporter_id
			         AND activation.authority_stage_event_id=NEW.authority_stage_event_id
			        JOIN delivery_stage_latest latest ON latest.delivery_id=NEW.delivery_id AND latest.attempt_id=NEW.attempt_id
			         AND latest.stage_key=NEW.stage_key AND latest.execution_number=NEW.execution_number
			         AND latest.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			         AND latest.authority_epoch=NEW.authority_epoch AND latest.authority_stage_event_id=NEW.authority_stage_event_id
			         AND latest.current_reporter_id=NEW.reporter_id
			        WHERE run.id=NEW.agent_run_id AND run.status='queued' AND run.issue_id=NEW.root_issue_id)) OR
			      (NEW.action IN ('run.cancel.running','input.respond','run.pause','run.resume') AND NOT EXISTS(
			        SELECT 1 FROM agent_runs run WHERE run.id=NEW.agent_run_id AND run.status='running' AND run.issue_id=NEW.root_issue_id))
			 BEGIN SELECT RAISE(ABORT,'control command run state is stale'); END`,
			`CREATE TRIGGER trg_control_commands_input_choice_guard
			 BEFORE INSERT ON control_commands
			 WHEN NEW.action='input.respond' AND NOT EXISTS(
			  SELECT 1 FROM control_input_requests request
			  JOIN control_input_request_seals seal ON seal.request_id=request.request_id AND seal.request_revision=request.revision
			  JOIN control_input_request_states state ON state.request_id=request.request_id
			   AND state.current_revision=request.revision AND state.terminal_event_id IS NULL
			  LEFT JOIN control_input_resolution_events terminal ON terminal.request_id=request.request_id AND terminal.request_revision=request.revision
			  WHERE request.request_id=NEW.input_request_id AND request.revision=NEW.input_request_revision AND terminal.id IS NULL
			   AND request.created_at<=NEW.created_at AND request.expires_at>NEW.created_at
			   AND request.expires_at=NEW.input_request_expires_at AND request.lease_id=NEW.lease_id
			   AND request.lease_revision=NEW.lease_revision AND request.delivery_id=NEW.delivery_id
			   AND request.delivery_key=NEW.delivery_key AND request.delivery_revision=NEW.delivery_revision
			   AND request.project_id=NEW.project_id AND request.root_issue_id=NEW.root_issue_id
			   AND request.issue_revision=NEW.issue_revision AND request.attempt_id=NEW.attempt_id
			   AND request.attempt_number=NEW.attempt_number AND request.plan_revision=NEW.plan_revision
			   AND request.stage_key=NEW.stage_key AND request.execution_number=NEW.execution_number
			   AND request.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			   AND request.authority_epoch=NEW.authority_epoch AND request.authority_stage_event_id=NEW.authority_stage_event_id
			   AND request.reporter_id=NEW.reporter_id AND request.agent_run_id=NEW.agent_run_id
			   AND ((NEW.input_response_kind IN ('approve','reject') AND request.request_kind='approval') OR
			        (NEW.input_response_kind='choice' AND request.request_kind='choice' AND EXISTS(
			         SELECT 1 FROM control_input_request_options option WHERE option.request_id=request.request_id
			          AND option.request_revision=request.revision AND option.ordinal=NEW.input_choice_ordinal
			          AND option.option_code=NEW.input_choice_code))) )
			 BEGIN SELECT RAISE(ABORT,'control command input binding is stale'); END`,
			`CREATE TRIGGER trg_control_commands_runtime_binding_guard
			 BEFORE INSERT ON control_commands
			 WHEN NEW.action IN ('run.pause','run.resume') AND NOT EXISTS(
			  SELECT 1 FROM control_runtime_states runtime
			  WHERE runtime.agent_run_id=NEW.agent_run_id AND runtime.delivery_id=NEW.delivery_id
			   AND runtime.root_issue_id=NEW.root_issue_id AND runtime.attempt_id=NEW.attempt_id
			   AND runtime.stage_key=NEW.stage_key AND runtime.execution_number=NEW.execution_number
			   AND runtime.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			   AND runtime.revision=NEW.runtime_revision
			   AND ((NEW.action='run.pause' AND runtime.state='running') OR
			        (NEW.action='run.resume' AND runtime.state='paused')))
			 BEGIN SELECT RAISE(ABORT,'control command runtime state is stale'); END`,
			`CREATE TRIGGER trg_control_commands_transition_guard
			 BEFORE UPDATE ON control_commands
			 WHEN OLD.status IN ('applied','rejected','expired') OR NEW.status_revision<>OLD.status_revision+1 OR
			  NOT ((OLD.status='pending_confirmation' AND NEW.status IN ('accepted','expired')) OR
			       (OLD.status='accepted' AND NEW.status IN ('accepted','applied','rejected'))) OR
			  (OLD.status='pending_confirmation' AND NEW.status='accepted' AND
			   (NEW.outcome IS NOT NULL OR NEW.safe_reason IS NOT NULL OR NEW.result_digest IS NOT NULL)) OR
			  (OLD.status='accepted' AND NEW.status='accepted' AND
			   (NOT (OLD.outcome IS NULL AND OLD.safe_reason IS NULL AND NEW.outcome='outcome_unknown' AND NEW.safe_reason='runner_lost') OR
			    NEW.action IN ('issue.priority.set','run.cancel.queued') OR NOT EXISTS(
			     SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=OLD.command_id
			      AND outbox.delivery_state='claimed' AND outbox.lease_id=OLD.lease_id
			      AND outbox.lease_revision=OLD.lease_revision AND outbox.effect_digest=OLD.canonical_digest))) OR
			  (OLD.outcome='outcome_unknown' AND NEW.outcome IS NULL) OR
			  (OLD.accepted_at IS NOT NULL AND NEW.accepted_at IS NOT OLD.accepted_at) OR
			  NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  (OLD.status='pending_confirmation' AND NEW.status='accepted' AND
			   (NEW.accepted_at IS NOT NEW.updated_at OR strftime('%Y-%m-%dT%H:%M:%fZ','now')>=OLD.expires_at)) OR
			  (OLD.status='pending_confirmation' AND NEW.status='expired' AND
			   (NEW.terminal_at IS NOT NEW.updated_at OR
			    (NEW.safe_reason='withdrawn' AND strftime('%Y-%m-%dT%H:%M:%fZ','now')>=OLD.expires_at) OR
			    (NEW.safe_reason='confirmation_expired' AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<OLD.expires_at))) OR
			  (OLD.status='accepted' AND NEW.status IN ('applied','rejected') AND NEW.terminal_at IS NOT NEW.updated_at) OR
			  NEW.command_id IS NOT OLD.command_id OR NEW.actor_user_id IS NOT OLD.actor_user_id OR NEW.user_id IS NOT OLD.user_id OR
			  NEW.principal_kind IS NOT OLD.principal_kind OR
			  NEW.actor_session_credential_id IS NOT OLD.actor_session_credential_id OR NEW.actor_api_key_id IS NOT OLD.actor_api_key_id OR
			  NEW.canonical_digest IS NOT OLD.canonical_digest OR
			  NEW.grant_id IS NOT OLD.grant_id OR NEW.grant_revision IS NOT OLD.grant_revision OR
			  NEW.grant_expires_at IS NOT OLD.grant_expires_at OR NEW.grant_binding_digest IS NOT OLD.grant_binding_digest OR
			  NEW.grant_action_digest IS NOT OLD.grant_action_digest OR NEW.action IS NOT OLD.action OR
			  NEW.challenge_template IS NOT OLD.challenge_template OR NEW.delivery_id IS NOT OLD.delivery_id OR
			  NEW.delivery_key IS NOT OLD.delivery_key OR NEW.delivery_revision IS NOT OLD.delivery_revision OR NEW.project_id IS NOT OLD.project_id OR
			  NEW.root_issue_id IS NOT OLD.root_issue_id OR NEW.issue_revision IS NOT OLD.issue_revision OR NEW.issue_etag_digest IS NOT OLD.issue_etag_digest OR
			  NEW.target_snapshot_digest IS NOT OLD.target_snapshot_digest OR NEW.attempt_id IS NOT OLD.attempt_id OR
			  NEW.attempt_number IS NOT OLD.attempt_number OR NEW.plan_revision IS NOT OLD.plan_revision OR
			  NEW.stage_key IS NOT OLD.stage_key OR NEW.execution_number IS NOT OLD.execution_number OR
			  NEW.execution_start_stage_event_id IS NOT OLD.execution_start_stage_event_id OR
			  NEW.authority_epoch IS NOT OLD.authority_epoch OR NEW.authority_stage_event_id IS NOT OLD.authority_stage_event_id OR NEW.reporter_id IS NOT OLD.reporter_id OR
			  NEW.agent_run_id IS NOT OLD.agent_run_id OR NEW.lease_id IS NOT OLD.lease_id OR
			  NEW.lease_revision IS NOT OLD.lease_revision OR NEW.lease_expires_at IS NOT OLD.lease_expires_at OR
			  NEW.lease_binding_digest IS NOT OLD.lease_binding_digest OR NEW.lease_action_digest IS NOT OLD.lease_action_digest OR
			  NEW.input_request_id IS NOT OLD.input_request_id OR NEW.input_request_revision IS NOT OLD.input_request_revision OR
			  NEW.input_request_expires_at IS NOT OLD.input_request_expires_at OR
			  NEW.runtime_revision IS NOT OLD.runtime_revision OR NEW.priority_value IS NOT OLD.priority_value OR
			  NEW.input_response_kind IS NOT OLD.input_response_kind OR NEW.input_choice_ordinal IS NOT OLD.input_choice_ordinal OR
			  NEW.input_choice_code IS NOT OLD.input_choice_code OR NEW.parameter_digest IS NOT OLD.parameter_digest OR
			  NEW.expires_at IS NOT OLD.expires_at OR NEW.created_at IS NOT OLD.created_at OR
			  NEW.updated_at<OLD.updated_at
			 BEGIN SELECT RAISE(ABORT,'invalid control command transition'); END`,
			`CREATE TRIGGER trg_control_commands_accept_grant_target_guard
			 BEFORE UPDATE ON control_commands
			 WHEN OLD.status='pending_confirmation' AND NEW.status='accepted' AND NOT EXISTS(
			  SELECT 1 FROM control_capability_grants grant_row
			  JOIN control_capability_grant_seals grant_seal ON grant_seal.grant_id=grant_row.grant_id
			   AND grant_seal.grant_revision=grant_row.revision
			  JOIN control_capability_grant_actions grant_action ON grant_action.grant_id=grant_row.grant_id
			   AND grant_action.grant_revision=grant_row.revision AND grant_action.action=OLD.action
			  JOIN deliveries delivery ON delivery.id=grant_row.delivery_id AND delivery.delivery_key=grant_row.delivery_key
			   AND delivery.issue_id=grant_row.root_issue_id
			  JOIN issues issue ON issue.id=delivery.issue_id AND issue.project_id=grant_row.project_id AND issue.deleted_at IS NULL
			  JOIN projects project ON project.id=issue.project_id
			  WHERE grant_row.grant_id=OLD.grant_id AND grant_row.revision=OLD.grant_revision
			   AND grant_row.revoked_at IS NULL AND grant_row.expires_at=OLD.grant_expires_at
			   AND grant_row.binding_digest=OLD.grant_binding_digest
			   AND grant_row.action_set_digest=OLD.grant_action_digest
			   AND grant_seal.binding_digest=grant_row.binding_digest
			   AND grant_seal.action_set_digest=grant_row.action_set_digest
			   AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<grant_row.expires_at
			   AND (SELECT revision FROM issue_control_revisions WHERE issue_id=issue.id)=grant_row.issue_revision
			   AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			                WHERE event.delivery_id=delivery.id),0)=grant_row.delivery_revision
			   AND grant_row.delivery_id=OLD.delivery_id AND grant_row.root_issue_id=OLD.root_issue_id
			   AND grant_row.issue_revision=OLD.issue_revision AND grant_row.delivery_revision=OLD.delivery_revision
			   AND (project.status IN ('active','frozen') OR
			        (project.status='archived' AND OLD.action IN ('run.cancel.queued','run.cancel.running')
			         AND NOT EXISTS(SELECT 1 FROM control_capability_grant_actions action
			          WHERE action.grant_id=OLD.grant_id AND action.grant_revision=OLD.grant_revision
			           AND action.action NOT IN ('run.cancel.queued','run.cancel.running'))
			         AND (OLD.action='run.cancel.queued' OR NOT EXISTS(
			          SELECT 1 FROM control_capability_lease_actions action
			          WHERE action.lease_id=OLD.lease_id AND action.lease_revision=OLD.lease_revision
			           AND action.action<>'run.cancel.running')))))
			 BEGIN SELECT RAISE(ABORT,'control command acceptance target is stale'); END`,
			`CREATE TRIGGER trg_control_commands_accept_lease_guard
			 BEFORE UPDATE ON control_commands
			 WHEN OLD.status='pending_confirmation' AND NEW.status='accepted'
			  AND OLD.action IN ('run.cancel.running','input.respond','run.pause','run.resume') AND NOT EXISTS(
			  SELECT 1 FROM control_capability_leases lease
			  JOIN control_capability_lease_seals lease_seal ON lease_seal.lease_id=lease.lease_id
			   AND lease_seal.lease_revision=lease.revision
			  JOIN control_capability_lease_actions lease_action ON lease_action.lease_id=lease.lease_id
			   AND lease_action.lease_revision=lease.revision AND lease_action.action=OLD.action
			  JOIN api_keys runner_key ON runner_key.id=lease.actor_api_key_id AND runner_key.user_id=lease.user_id
			   AND runner_key.disabled_at IS NULL
			   AND (runner_key.expires_at IS NULL OR runner_key.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			  JOIN users runner_user ON runner_user.id=lease.user_id AND runner_user.status='active'
			  JOIN delivery_agent_run_links link ON link.delivery_id=lease.delivery_id AND link.attempt_id=lease.attempt_id
			   AND link.stage_key=lease.stage_key AND link.execution_number=lease.execution_number
			   AND link.execution_start_stage_event_id=lease.execution_start_stage_event_id
			   AND link.agent_run_id=lease.agent_run_id AND link.reporter_id=lease.reporter_id
			  JOIN agent_runs run ON run.id=lease.agent_run_id AND run.issue_id=lease.root_issue_id AND run.status='running'
			  JOIN delivery_agent_run_activations activation ON activation.delivery_id=lease.delivery_id
			   AND activation.attempt_id=lease.attempt_id AND activation.stage_key=lease.stage_key
			   AND activation.execution_number=lease.execution_number AND activation.authority_epoch=lease.authority_epoch
			   AND activation.agent_run_id=lease.agent_run_id AND activation.reporter_id=lease.reporter_id
			   AND activation.authority_stage_event_id=lease.authority_stage_event_id
			  JOIN delivery_stage_latest latest ON latest.delivery_id=lease.delivery_id AND latest.attempt_id=lease.attempt_id
			   AND latest.stage_key=lease.stage_key AND latest.execution_number=lease.execution_number
			   AND latest.execution_start_stage_event_id=lease.execution_start_stage_event_id
			   AND latest.authority_epoch=lease.authority_epoch AND latest.current_reporter_id=lease.reporter_id
			   AND latest.authority_stage_event_id=lease.authority_stage_event_id
			  WHERE lease.lease_id=OLD.lease_id AND lease.revision=OLD.lease_revision
			   AND lease.revoked_at IS NULL AND lease.expires_at=OLD.lease_expires_at
			   AND lease.binding_digest=OLD.lease_binding_digest AND lease.action_set_digest=OLD.lease_action_digest
			   AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<lease.expires_at
			   AND lease.delivery_id=OLD.delivery_id AND lease.root_issue_id=OLD.root_issue_id
			   AND lease.issue_revision=OLD.issue_revision AND lease.delivery_revision=OLD.delivery_revision
			   AND lease.attempt_id=OLD.attempt_id AND lease.stage_key=OLD.stage_key
			   AND lease.execution_number=OLD.execution_number
			   AND lease.execution_start_stage_event_id=OLD.execution_start_stage_event_id
			   AND lease.authority_epoch=OLD.authority_epoch AND lease.authority_stage_event_id=OLD.authority_stage_event_id
			   AND lease.reporter_id=OLD.reporter_id AND lease.agent_run_id=OLD.agent_run_id)
			 BEGIN SELECT RAISE(ABORT,'control command acceptance lease is stale'); END`,
			`CREATE TRIGGER trg_control_commands_accept_action_state_guard
			 BEFORE UPDATE ON control_commands
			 WHEN OLD.status='pending_confirmation' AND NEW.status='accepted' AND
			  ((OLD.action='run.cancel.queued' AND NOT EXISTS(
			    SELECT 1 FROM agent_runs run
			    JOIN delivery_agent_run_links link ON link.agent_run_id=run.id AND link.delivery_id=OLD.delivery_id
			     AND link.attempt_id=OLD.attempt_id AND link.stage_key=OLD.stage_key
			     AND link.execution_number=OLD.execution_number AND link.reporter_id=OLD.reporter_id
			     AND link.execution_start_stage_event_id=OLD.execution_start_stage_event_id
			    JOIN delivery_stage_latest latest ON latest.delivery_id=OLD.delivery_id AND latest.attempt_id=OLD.attempt_id
			     AND latest.stage_key=OLD.stage_key AND latest.execution_number=OLD.execution_number
			     AND latest.execution_start_stage_event_id=OLD.execution_start_stage_event_id
			     AND latest.authority_epoch=OLD.authority_epoch AND latest.current_reporter_id=OLD.reporter_id
			     AND latest.authority_stage_event_id=OLD.authority_stage_event_id
			    WHERE run.id=OLD.agent_run_id AND run.issue_id=OLD.root_issue_id AND run.status='queued')) OR
			   (OLD.action='input.respond' AND NOT EXISTS(
			    SELECT 1 FROM control_input_requests request
			    JOIN control_input_request_seals request_seal ON request_seal.request_id=request.request_id
			     AND request_seal.request_revision=request.revision
			    JOIN control_input_request_states input_state ON input_state.request_id=request.request_id
			     AND input_state.current_revision=request.revision AND input_state.terminal_event_id IS NULL
			    WHERE request.request_id=OLD.input_request_id AND request.revision=OLD.input_request_revision
			     AND request.expires_at=OLD.input_request_expires_at
			     AND NOT EXISTS(SELECT 1 FROM control_input_resolution_events terminal
			      WHERE terminal.request_id=request.request_id AND terminal.request_revision=request.revision)
			     AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<request.expires_at)) OR
			   (OLD.action IN ('run.pause','run.resume') AND NOT EXISTS(
			    SELECT 1 FROM control_runtime_states runtime
			    WHERE runtime.agent_run_id=OLD.agent_run_id AND runtime.revision=OLD.runtime_revision
			     AND runtime.delivery_id=OLD.delivery_id AND runtime.root_issue_id=OLD.root_issue_id
			     AND runtime.attempt_id=OLD.attempt_id AND runtime.stage_key=OLD.stage_key
			     AND runtime.execution_number=OLD.execution_number
			     AND runtime.execution_start_stage_event_id=OLD.execution_start_stage_event_id
			     AND ((OLD.action='run.pause' AND runtime.state='running') OR
			          (OLD.action='run.resume' AND runtime.state='paused')))))
			 BEGIN SELECT RAISE(ABORT,'control command acceptance state is stale'); END`,
			`CREATE TRIGGER trg_control_commands_no_delete
			 BEFORE DELETE ON control_commands
			 BEGIN SELECT RAISE(ABORT,'control commands are retained'); END`,
			`CREATE TRIGGER trg_agent_run_cancellation_command_guard
			 BEFORE INSERT ON agent_run_cancellation_facts
			 WHEN NEW.recorded_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NOT EXISTS(
			  SELECT 1 FROM agent_runs run
			  WHERE run.id=NEW.run_id AND run.status='cancelled' AND run.finished_at IS NOT NULL
			   AND (NEW.cancellation_cause<>'operator_command' OR EXISTS(
			    SELECT 1 FROM control_commands command
			    WHERE command.command_id=NEW.command_id AND command.agent_run_id=NEW.run_id
			     AND command.action IN ('run.cancel.queued','run.cancel.running')
			     AND command.status='applied' AND command.outcome='applied' AND command.result_digest IS NOT NULL)))
			 BEGIN SELECT RAISE(ABORT,'cancellation fact lacks terminal run proof'); END`,
			`CREATE TRIGGER trg_control_runtime_command_proof_guard
			 BEFORE UPDATE ON control_runtime_states
			 WHEN NOT EXISTS(
			  SELECT 1 FROM control_commands command
			  WHERE command.command_id=NEW.last_command_id AND command.status='applied'
			   AND command.result_digest=NEW.last_result_digest AND command.agent_run_id=NEW.agent_run_id
			   AND command.delivery_id=NEW.delivery_id AND command.root_issue_id=NEW.root_issue_id
			   AND command.attempt_id=NEW.attempt_id AND command.stage_key=NEW.stage_key
			   AND command.execution_number=NEW.execution_number
			   AND command.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			   AND command.runtime_revision=OLD.revision AND
			   ((OLD.state='running' AND NEW.state='paused' AND command.action='run.pause') OR
			    (OLD.state='paused' AND NEW.state='running' AND command.action='run.resume')))
			 BEGIN SELECT RAISE(ABORT,'runtime transition lacks verified command proof'); END`,

			`CREATE TABLE control_outbox (
			 id                    INTEGER PRIMARY KEY AUTOINCREMENT CHECK(id>0),
			 command_id            TEXT NOT NULL UNIQUE,
			 lease_id              TEXT NOT NULL,
			 lease_revision        INTEGER NOT NULL CHECK(lease_revision>0),
			 delivery_state        TEXT NOT NULL CHECK(delivery_state IN (` + sqlEnum(controlcontract.OutboxStates()) + `)),
			 effect_sequence       INTEGER NOT NULL DEFAULT 1 CHECK(effect_sequence=1),
			 effect_digest         BLOB NOT NULL CHECK(typeof(effect_digest)='blob' AND length(effect_digest)=32),
			 claim_sequence        INTEGER CHECK(claim_sequence>0),
			 claim_user_id         INTEGER CHECK(claim_user_id>0),
			 claim_principal_kind  TEXT CHECK(claim_principal_kind IS NULL OR claim_principal_kind='api_key'),
			 claim_session_credential_id TEXT,
			 claim_api_key_id      INTEGER CHECK(claim_api_key_id>0),
			 claim_device_id       TEXT CHECK(claim_device_id IS NULL OR ` + sqlSafeDeviceIDCheck("claim_device_id") + `),
			 claimed_at            TEXT CHECK(` + sqlNullableControlTimestampCheck("claimed_at") + `),
			 result_sequence       INTEGER CHECK(result_sequence>0),
			 result_digest         BLOB CHECK(result_digest IS NULL OR (typeof(result_digest)='blob' AND length(result_digest)=32)),
			 result_outcome        TEXT CHECK(result_outcome IS NULL OR result_outcome IN ('applied','rejected')),
			 safe_reason           TEXT CHECK(safe_reason IS NULL OR safe_reason IN (` + sqlEnum(controlcontract.SafeReasons()) + `)),
			 acknowledged_at       TEXT CHECK(` + sqlNullableControlTimestampCheck("acknowledged_at") + `),
			 created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 FOREIGN KEY(command_id) REFERENCES control_commands(command_id),
			 FOREIGN KEY(lease_id,lease_revision)
			  REFERENCES control_capability_leases(lease_id,revision),
			 CHECK(COALESCE((claim_principal_kind IS NULL AND claim_session_credential_id IS NULL AND claim_api_key_id IS NULL AND claim_device_id IS NULL) OR
			       (claim_principal_kind='api_key' AND ` + sqlTypedPrincipalCheck("claim_principal_kind", "claim_session_credential_id", "claim_api_key_id") + `),0)),
			 CHECK(COALESCE((delivery_state='queued' AND claim_sequence IS NULL AND claim_user_id IS NULL AND claim_principal_kind IS NULL AND claim_session_credential_id IS NULL AND claim_api_key_id IS NULL AND claim_device_id IS NULL AND claimed_at IS NULL AND result_sequence IS NULL AND result_digest IS NULL AND result_outcome IS NULL AND safe_reason IS NULL AND acknowledged_at IS NULL) OR
			       (delivery_state='claimed' AND claim_sequence=1 AND claim_user_id IS NOT NULL AND claim_principal_kind='api_key' AND claim_device_id IS NOT NULL AND claimed_at>=created_at AND result_sequence IS NULL AND result_digest IS NULL AND result_outcome IS NULL AND (safe_reason IS NULL OR safe_reason='runner_lost') AND acknowledged_at IS NULL) OR
			       (delivery_state='acknowledged' AND claim_sequence=1 AND claim_user_id IS NOT NULL AND claim_principal_kind='api_key' AND claim_device_id IS NOT NULL AND claimed_at>=created_at AND result_sequence=1 AND result_digest IS NOT NULL AND result_outcome IS NOT NULL AND acknowledged_at>=claimed_at AND
			        ((result_outcome='applied' AND safe_reason IS NULL) OR (result_outcome='rejected' AND safe_reason IS NOT NULL))) OR
			       (delivery_state='abandoned' AND claim_sequence IS NULL AND claim_user_id IS NULL AND claim_principal_kind IS NULL AND claim_session_credential_id IS NULL AND claim_api_key_id IS NULL AND claim_device_id IS NULL AND claimed_at IS NULL AND result_sequence IS NULL AND result_digest IS NULL AND result_outcome IS NULL AND safe_reason IS NOT NULL AND acknowledged_at>=created_at),0)),
			 CHECK(updated_at>=created_at)
			)`,
			`CREATE INDEX idx_control_outbox_delivery ON control_outbox(delivery_state,id)`,
			`CREATE INDEX idx_control_outbox_lease ON control_outbox(lease_id,lease_revision,delivery_state)`,
			`CREATE TRIGGER trg_control_outbox_insert_guard
			 BEFORE INSERT ON control_outbox
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.updated_at IS NOT NEW.created_at OR
			      NEW.delivery_state<>'queued' OR NEW.claim_sequence IS NOT NULL OR NEW.claim_user_id IS NOT NULL OR
			      NEW.claim_principal_kind IS NOT NULL OR NEW.claim_session_credential_id IS NOT NULL OR NEW.claim_api_key_id IS NOT NULL OR NEW.claim_device_id IS NOT NULL OR
			      NEW.claimed_at IS NOT NULL OR NEW.result_sequence IS NOT NULL OR NEW.result_digest IS NOT NULL OR
			      NEW.result_outcome IS NOT NULL OR NEW.safe_reason IS NOT NULL OR NEW.acknowledged_at IS NOT NULL OR
			      NOT EXISTS(SELECT 1 FROM control_commands command
			       WHERE command.command_id=NEW.command_id AND command.status='accepted' AND command.outcome IS NULL
			        AND command.action IN ('run.cancel.running','input.respond','run.pause','run.resume')
			        AND command.lease_id=NEW.lease_id AND command.lease_revision=NEW.lease_revision
			        AND command.canonical_digest=NEW.effect_digest)
			 BEGIN SELECT RAISE(ABORT,'control outbox must start queued'); END`,
			`CREATE TRIGGER trg_control_outbox_transition_guard
			 BEFORE UPDATE ON control_outbox
			 WHEN OLD.delivery_state IN ('acknowledged','abandoned') OR
			 NOT ((OLD.delivery_state='queued' AND NEW.delivery_state IN ('claimed','abandoned')) OR
			      (OLD.delivery_state='claimed' AND NEW.delivery_state='acknowledged') OR
			      (OLD.delivery_state='claimed' AND NEW.delivery_state='claimed' AND OLD.safe_reason IS NULL AND NEW.safe_reason='runner_lost')) OR
			  NEW.id IS NOT OLD.id OR NEW.command_id IS NOT OLD.command_id OR NEW.lease_id IS NOT OLD.lease_id OR
			  NEW.lease_revision IS NOT OLD.lease_revision OR NEW.effect_sequence IS NOT OLD.effect_sequence OR
			 NEW.effect_digest IS NOT OLD.effect_digest OR NEW.created_at IS NOT OLD.created_at OR
			 NEW.updated_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.updated_at<OLD.updated_at OR
			 (OLD.delivery_state='queued' AND NEW.delivery_state='claimed' AND
			  (NEW.claimed_at IS NOT NEW.updated_at OR NEW.safe_reason IS NOT NULL)) OR
			 (OLD.delivery_state='claimed' AND NEW.delivery_state='acknowledged' AND NEW.acknowledged_at IS NOT NEW.updated_at) OR
			 (OLD.delivery_state='queued' AND NEW.delivery_state='abandoned' AND NEW.acknowledged_at IS NOT NEW.updated_at) OR
			 (OLD.delivery_state='claimed' AND (NEW.claim_sequence IS NOT OLD.claim_sequence OR
			  NEW.claim_user_id IS NOT OLD.claim_user_id OR NEW.claim_principal_kind IS NOT OLD.claim_principal_kind OR
			  NEW.claim_session_credential_id IS NOT OLD.claim_session_credential_id OR NEW.claim_api_key_id IS NOT OLD.claim_api_key_id OR
			  NEW.claim_device_id IS NOT OLD.claim_device_id OR NEW.claimed_at IS NOT OLD.claimed_at))
			 BEGIN SELECT RAISE(ABORT,'invalid control outbox transition'); END`,
			`CREATE TRIGGER trg_control_outbox_claim_binding_guard
			 BEFORE UPDATE ON control_outbox
			 WHEN OLD.delivery_state='queued' AND NEW.delivery_state='claimed' AND NOT EXISTS(
			  SELECT 1 FROM control_capability_leases lease
			  JOIN control_commands command ON command.command_id=NEW.command_id
			  JOIN control_capability_grants grant_row ON grant_row.grant_id=command.grant_id
			   AND grant_row.revision=command.grant_revision
			  JOIN control_capability_grant_seals grant_seal ON grant_seal.grant_id=grant_row.grant_id
			   AND grant_seal.grant_revision=grant_row.revision
			  JOIN control_capability_grant_actions grant_action ON grant_action.grant_id=grant_row.grant_id
			   AND grant_action.grant_revision=grant_row.revision AND grant_action.action=command.action
			  JOIN api_keys runner_key ON runner_key.id=lease.actor_api_key_id AND runner_key.user_id=lease.user_id
			   AND runner_key.disabled_at IS NULL
			   AND (runner_key.expires_at IS NULL OR runner_key.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			  JOIN users runner_user ON runner_user.id=lease.user_id AND runner_user.status='active'
			  JOIN deliveries delivery ON delivery.id=lease.delivery_id AND delivery.delivery_key=lease.delivery_key
			   AND delivery.issue_id=lease.root_issue_id
			  JOIN issues issue ON issue.id=delivery.issue_id AND issue.project_id=lease.project_id AND issue.deleted_at IS NULL
			  JOIN projects project ON project.id=issue.project_id AND project.status<>'deleted'
			  JOIN delivery_agent_run_links link ON link.delivery_id=lease.delivery_id AND link.attempt_id=lease.attempt_id
			   AND link.stage_key=lease.stage_key AND link.execution_number=lease.execution_number
			   AND link.execution_start_stage_event_id=lease.execution_start_stage_event_id
			   AND link.agent_run_id=lease.agent_run_id AND link.reporter_id=lease.reporter_id
			  JOIN agent_runs run ON run.id=lease.agent_run_id AND run.issue_id=lease.root_issue_id AND run.status='running'
			  JOIN delivery_agent_run_activations activation ON activation.delivery_id=lease.delivery_id
			   AND activation.attempt_id=lease.attempt_id AND activation.stage_key=lease.stage_key
			   AND activation.execution_number=lease.execution_number AND activation.authority_epoch=lease.authority_epoch
			   AND activation.agent_run_id=lease.agent_run_id AND activation.reporter_id=lease.reporter_id
			   AND activation.authority_stage_event_id=lease.authority_stage_event_id
			  JOIN delivery_stage_latest latest ON latest.delivery_id=lease.delivery_id AND latest.attempt_id=lease.attempt_id
			   AND latest.stage_key=lease.stage_key AND latest.execution_number=lease.execution_number
			   AND latest.execution_start_stage_event_id=lease.execution_start_stage_event_id
			   AND latest.authority_epoch=lease.authority_epoch AND latest.current_reporter_id=lease.reporter_id
			   AND latest.authority_stage_event_id=lease.authority_stage_event_id
			  WHERE lease.lease_id=NEW.lease_id AND lease.revision=NEW.lease_revision
			   AND lease.revoked_at IS NULL AND lease.expires_at=command.lease_expires_at
			   AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<command.lease_expires_at
			   AND grant_row.revoked_at IS NULL AND grant_row.expires_at=command.grant_expires_at
			   AND grant_row.binding_digest=command.grant_binding_digest
			   AND grant_row.action_set_digest=command.grant_action_digest
			   AND grant_seal.binding_digest=grant_row.binding_digest
			   AND grant_seal.action_set_digest=grant_row.action_set_digest
			   AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<command.grant_expires_at
			   AND (SELECT revision FROM issue_control_revisions WHERE issue_id=issue.id)=lease.issue_revision
			   AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			                WHERE event.delivery_id=delivery.id),0)=lease.delivery_revision
			   AND (project.status IN ('active','frozen') OR
			        (project.status='archived' AND command.action='run.cancel.running'
			         AND NOT EXISTS(SELECT 1 FROM control_capability_grant_actions action
			          WHERE action.grant_id=grant_row.grant_id AND action.grant_revision=grant_row.revision
			           AND action.action NOT IN ('run.cancel.queued','run.cancel.running'))
			         AND NOT EXISTS(SELECT 1 FROM control_capability_lease_actions action
			          WHERE action.lease_id=lease.lease_id AND action.lease_revision=lease.revision
			           AND action.action<>'run.cancel.running')))
			   AND lease.user_id=NEW.claim_user_id AND lease.principal_kind='api_key'
			   AND lease.actor_api_key_id=NEW.claim_api_key_id AND lease.device_id=NEW.claim_device_id
			   AND command.status='accepted' AND command.outcome IS NULL
			   AND command.lease_id=lease.lease_id AND command.lease_revision=lease.revision
			   AND command.canonical_digest=NEW.effect_digest
			   AND ((command.action='input.respond' AND EXISTS(
			          SELECT 1 FROM control_input_requests request
			          JOIN control_input_request_seals request_seal ON request_seal.request_id=request.request_id
			           AND request_seal.request_revision=request.revision
			          JOIN control_input_request_states input_state ON input_state.request_id=request.request_id
			           AND input_state.current_revision=request.revision AND input_state.terminal_event_id IS NULL
			          WHERE request.request_id=command.input_request_id
			           AND request.revision=command.input_request_revision
			           AND request.expires_at=command.input_request_expires_at
			           AND NOT EXISTS(SELECT 1 FROM control_input_resolution_events terminal
			            WHERE terminal.request_id=request.request_id AND terminal.request_revision=request.revision)
			           AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<request.expires_at)
			         AND NOT EXISTS(
			          SELECT 1 FROM control_outbox prior_outbox
			          JOIN control_commands prior_command ON prior_command.command_id=prior_outbox.command_id
			          WHERE prior_command.command_id<>command.command_id
			           AND prior_command.input_request_id=command.input_request_id
			           AND prior_command.input_request_revision=command.input_request_revision
			           AND (prior_outbox.delivery_state='claimed' OR
			                (prior_outbox.delivery_state='acknowledged' AND prior_outbox.result_outcome='applied')))) OR
			        (command.action IN ('run.pause','run.resume') AND EXISTS(
			          SELECT 1 FROM control_runtime_states runtime
			          WHERE runtime.agent_run_id=command.agent_run_id AND runtime.revision=command.runtime_revision
			           AND runtime.delivery_id=command.delivery_id AND runtime.root_issue_id=command.root_issue_id
			           AND runtime.attempt_id=command.attempt_id AND runtime.stage_key=command.stage_key
			           AND runtime.execution_number=command.execution_number
			           AND runtime.execution_start_stage_event_id=command.execution_start_stage_event_id
			           AND ((command.action='run.pause' AND runtime.state='running') OR
			                (command.action='run.resume' AND runtime.state='paused')))
			         AND NOT EXISTS(
			          SELECT 1 FROM control_outbox prior_outbox
			          JOIN control_commands prior_command ON prior_command.command_id=prior_outbox.command_id
			          WHERE prior_command.command_id<>command.command_id
			           AND prior_command.agent_run_id=command.agent_run_id
			           AND prior_command.runtime_revision=command.runtime_revision
			           AND prior_command.action=command.action
			           AND (prior_outbox.delivery_state='claimed' OR
			                (prior_outbox.delivery_state='acknowledged' AND prior_outbox.result_outcome='applied')))) OR
			        (command.action='run.cancel.running' AND NOT EXISTS(
			          SELECT 1 FROM control_outbox prior_outbox
			          JOIN control_commands prior_command ON prior_command.command_id=prior_outbox.command_id
			          WHERE prior_command.command_id<>command.command_id
			           AND prior_command.action='run.cancel.running'
			           AND prior_command.delivery_id=command.delivery_id
			           AND prior_command.attempt_id=command.attempt_id
			           AND prior_command.stage_key=command.stage_key
			           AND prior_command.execution_number=command.execution_number
			           AND prior_command.execution_start_stage_event_id=command.execution_start_stage_event_id
			           AND prior_command.agent_run_id=command.agent_run_id
			           AND (prior_outbox.delivery_state='claimed' OR
			                (prior_outbox.delivery_state='acknowledged' AND prior_outbox.result_outcome='applied')))) OR
			        command.action NOT IN ('input.respond','run.pause','run.resume','run.cancel.running')))
			 BEGIN SELECT RAISE(ABORT,'control outbox claimant is stale'); END`,
			`CREATE TRIGGER trg_control_outbox_result_binding_guard
			 BEFORE UPDATE ON control_outbox
			 WHEN OLD.delivery_state='claimed' AND NEW.delivery_state='acknowledged' AND NOT EXISTS(
			  SELECT 1 FROM control_commands command
			  WHERE command.command_id=NEW.command_id AND command.result_digest=NEW.result_digest
			   AND ((NEW.result_outcome='applied' AND command.status='applied' AND command.outcome='applied' AND command.safe_reason IS NULL) OR
			        (NEW.result_outcome='rejected' AND command.status='rejected' AND command.outcome='rejected' AND command.safe_reason=NEW.safe_reason))
			   AND ((command.status_revision=4 AND EXISTS(
			          SELECT 1 FROM control_events unknown_fact
			          WHERE unknown_fact.command_id=command.command_id AND unknown_fact.event_kind='effect_outcome_unknown')) OR
			        (command.status_revision=3 AND
			         (command.status='applied' OR
			          (command.status='rejected' AND
			           (command.safe_reason IN ('effect_rejected','unsupported_platform') OR
			            (command.action='run.cancel.running' AND
			             command.safe_reason IN ('process_termination_failed','natural_exit'))))) AND EXISTS(
			          SELECT 1 FROM control_capability_leases lease
			          JOIN control_capability_grants grant_row ON grant_row.grant_id=command.grant_id
			           AND grant_row.revision=command.grant_revision
			          JOIN control_capability_grant_seals grant_seal ON grant_seal.grant_id=grant_row.grant_id
			           AND grant_seal.grant_revision=grant_row.revision
			          JOIN control_capability_grant_actions grant_action ON grant_action.grant_id=grant_row.grant_id
			           AND grant_action.grant_revision=grant_row.revision AND grant_action.action=command.action
			          JOIN api_keys runner_key ON runner_key.id=lease.actor_api_key_id AND runner_key.user_id=lease.user_id
			           AND runner_key.disabled_at IS NULL
			           AND (runner_key.expires_at IS NULL OR runner_key.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			          JOIN users runner_user ON runner_user.id=lease.user_id AND runner_user.status='active'
			          JOIN deliveries delivery ON delivery.id=lease.delivery_id AND delivery.delivery_key=lease.delivery_key
			           AND delivery.issue_id=lease.root_issue_id
			          JOIN issues issue ON issue.id=delivery.issue_id AND issue.project_id=lease.project_id
			          JOIN projects project ON project.id=issue.project_id
			          JOIN delivery_agent_run_links link ON link.delivery_id=lease.delivery_id AND link.attempt_id=lease.attempt_id
			           AND link.stage_key=lease.stage_key AND link.execution_number=lease.execution_number
			           AND link.execution_start_stage_event_id=lease.execution_start_stage_event_id
			           AND link.agent_run_id=lease.agent_run_id AND link.reporter_id=lease.reporter_id
			          JOIN agent_runs run ON run.id=lease.agent_run_id AND run.issue_id=lease.root_issue_id
			          JOIN delivery_agent_run_activations activation ON activation.delivery_id=lease.delivery_id
			           AND activation.attempt_id=lease.attempt_id AND activation.stage_key=lease.stage_key
			           AND activation.execution_number=lease.execution_number AND activation.authority_epoch=lease.authority_epoch
			           AND activation.agent_run_id=lease.agent_run_id AND activation.reporter_id=lease.reporter_id
			           AND activation.authority_stage_event_id=lease.authority_stage_event_id
			          JOIN delivery_stage_latest latest ON latest.delivery_id=lease.delivery_id AND latest.attempt_id=lease.attempt_id
			           AND latest.stage_key=lease.stage_key AND latest.execution_number=lease.execution_number
			           AND latest.execution_start_stage_event_id=lease.execution_start_stage_event_id
			           AND latest.authority_epoch=lease.authority_epoch AND latest.current_reporter_id=lease.reporter_id
			           AND latest.authority_stage_event_id=lease.authority_stage_event_id
			          WHERE lease.lease_id=NEW.lease_id AND lease.revision=NEW.lease_revision
			           AND lease.revoked_at IS NULL AND lease.expires_at=command.lease_expires_at
			           AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<command.lease_expires_at
			           AND grant_row.revoked_at IS NULL AND grant_row.expires_at=command.grant_expires_at
			           AND grant_row.binding_digest=command.grant_binding_digest
			           AND grant_row.action_set_digest=command.grant_action_digest
			           AND grant_seal.binding_digest=grant_row.binding_digest
			           AND grant_seal.action_set_digest=grant_row.action_set_digest
			           AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<command.grant_expires_at
			           AND lease.user_id=NEW.claim_user_id AND lease.principal_kind='api_key'
			           AND lease.actor_api_key_id=NEW.claim_api_key_id AND lease.device_id=NEW.claim_device_id
			           AND command.lease_id=lease.lease_id AND command.lease_revision=lease.revision
			           AND issue.deleted_at IS NULL
			           AND (SELECT revision FROM issue_control_revisions WHERE issue_id=issue.id)=lease.issue_revision
			           AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			                        WHERE event.delivery_id=delivery.id),0)=lease.delivery_revision
			           AND (project.status IN ('active','frozen') OR
			                (project.status='archived' AND command.action='run.cancel.running'))
			           AND ((run.status='running' AND NOT (
			                 command.action='run.cancel.running' AND NEW.result_outcome='rejected'
			                 AND NEW.safe_reason='natural_exit')) OR
			                (command.action='run.cancel.running' AND command.status='rejected'
			                 AND command.outcome='rejected' AND command.safe_reason='natural_exit'
			                 AND NEW.result_outcome='rejected' AND NEW.safe_reason='natural_exit'
			                 AND run.status IN ('completed','tests_passed','tests_failed','deployed','failed','drafted')
			                 AND run.finished_at IS NOT NULL
			                 AND NOT EXISTS(SELECT 1 FROM agent_run_cancellation_facts cancellation
			                  WHERE cancellation.run_id=run.id)))
			           AND (command.action NOT IN ('input.respond','run.pause','run.resume') OR
			                (command.action='input.respond' AND EXISTS(
			                  SELECT 1 FROM control_input_requests request
			                  JOIN control_input_request_seals request_seal ON request_seal.request_id=request.request_id
			                   AND request_seal.request_revision=request.revision
			                  JOIN control_input_request_states input_state ON input_state.request_id=request.request_id
			                   AND input_state.current_revision=request.revision AND input_state.terminal_event_id IS NULL
			                  WHERE request.request_id=command.input_request_id
			                   AND request.revision=command.input_request_revision
			                   AND request.expires_at=command.input_request_expires_at
			                   AND NOT EXISTS(SELECT 1 FROM control_input_resolution_events terminal
			                    WHERE terminal.request_id=request.request_id AND terminal.request_revision=request.revision)
			                   AND strftime('%Y-%m-%dT%H:%M:%fZ','now')<request.expires_at)) OR
			                (command.action IN ('run.pause','run.resume') AND EXISTS(
			                  SELECT 1 FROM control_runtime_states runtime
			                  WHERE runtime.agent_run_id=command.agent_run_id AND runtime.revision=command.runtime_revision
			                   AND runtime.delivery_id=command.delivery_id AND runtime.root_issue_id=command.root_issue_id
			                   AND runtime.attempt_id=command.attempt_id AND runtime.stage_key=command.stage_key
			                   AND runtime.execution_number=command.execution_number
			                   AND runtime.execution_start_stage_event_id=command.execution_start_stage_event_id
			                   AND ((command.action='run.pause' AND runtime.state='running') OR
			                        (command.action='run.resume' AND runtime.state='paused')))))))))
			 BEGIN SELECT RAISE(ABORT,'control outbox result lacks command proof'); END`,
			`CREATE TRIGGER trg_control_outbox_unknown_binding_guard
			 BEFORE UPDATE ON control_outbox
			 WHEN OLD.delivery_state='claimed' AND NEW.delivery_state='claimed' AND NOT EXISTS(
			  SELECT 1 FROM control_commands command
			  WHERE command.command_id=NEW.command_id AND command.status='accepted'
			   AND command.outcome='outcome_unknown' AND command.safe_reason='runner_lost')
			 BEGIN SELECT RAISE(ABORT,'control outbox unknown outcome lacks command proof'); END`,
			`CREATE TRIGGER trg_control_outbox_abandon_binding_guard
			 BEFORE UPDATE ON control_outbox
			 WHEN OLD.delivery_state='queued' AND NEW.delivery_state='abandoned' AND NOT EXISTS(
			  SELECT 1 FROM control_commands command
			  WHERE command.command_id=NEW.command_id AND command.status='rejected'
			   AND command.outcome='rejected' AND command.result_digest IS NOT NULL
			   AND command.safe_reason=NEW.safe_reason)
			 BEGIN SELECT RAISE(ABORT,'control outbox abandonment lacks command proof'); END`,
			`CREATE TRIGGER trg_control_outbox_no_delete
			 BEFORE DELETE ON control_outbox
			 BEGIN SELECT RAISE(ABORT,'control outbox is retained'); END`,

			`CREATE TABLE control_events (
			 id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			 sequence              INTEGER NOT NULL CHECK(sequence>0),
			 event_kind            TEXT NOT NULL CHECK(event_kind IN (` + sqlEnum(controlcontract.EventKinds()) + `)),
			 grant_id              TEXT CHECK(grant_id IS NULL OR ` + sqlUUIDCheck("grant_id") + `),
			 grant_revision        INTEGER CHECK(grant_revision>0),
			 lease_id              TEXT CHECK(lease_id IS NULL OR ` + sqlUUIDCheck("lease_id") + `),
			 lease_revision        INTEGER CHECK(lease_revision>0),
			 input_request_id      TEXT CHECK(input_request_id IS NULL OR ` + sqlUUIDCheck("input_request_id") + `),
			 input_request_revision INTEGER CHECK(input_request_revision>0),
			 command_id            TEXT CHECK(command_id IS NULL OR ` + sqlUUIDCheck("command_id") + `),
			 command_status_revision INTEGER CHECK(command_status_revision>0),
			 cancellation_run_id    INTEGER CHECK(cancellation_run_id>0),
			 cancellation_command_id TEXT CHECK(cancellation_command_id IS NULL OR ` + sqlUUIDCheck("cancellation_command_id") + `),
			 actor_user_id         INTEGER CHECK(actor_user_id>0),
			 user_id               INTEGER CHECK(user_id>0),
			 principal_kind        TEXT CHECK(principal_kind IS NULL OR principal_kind IN ('session','api_key')),
			 actor_session_credential_id TEXT,
			 actor_api_key_id      INTEGER CHECK(actor_api_key_id>0),
			 executor_user_id      INTEGER CHECK(executor_user_id>0),
			 executor_principal_kind TEXT CHECK(executor_principal_kind IS NULL OR executor_principal_kind IN ('session','api_key')),
			 executor_session_credential_id TEXT,
			 executor_api_key_id   INTEGER CHECK(executor_api_key_id>0),
			 device_id            TEXT CHECK(device_id IS NULL OR ` + sqlSafeDeviceIDCheck("device_id") + `),
			 delivery_id          INTEGER CHECK(delivery_id>0),
			 root_issue_id        INTEGER CHECK(root_issue_id>0),
			 issue_revision       INTEGER CHECK(issue_revision>0),
			 attempt_id           INTEGER CHECK(attempt_id>0),
			 stage_key            TEXT CHECK(stage_key IS NULL OR stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number     INTEGER CHECK(execution_number>0),
			 authority_epoch      INTEGER CHECK(authority_epoch>0),
			 reporter_id          INTEGER CHECK(reporter_id>0),
			 agent_run_id         INTEGER CHECK(agent_run_id>0),
			 action               TEXT CHECK(action IS NULL OR action IN (` + sqlEnum(controlcontract.Actions()) + `)),
			 command_status       TEXT CHECK(command_status IS NULL OR command_status IN (` + sqlEnum(controlcontract.CommandStatuses()) + `)),
			 runtime_state        TEXT CHECK(runtime_state IS NULL OR runtime_state IN (` + sqlEnum(controlcontract.RuntimeStates()) + `)),
			 runtime_revision     INTEGER CHECK(runtime_revision>0),
			 outcome              TEXT CHECK(outcome IS NULL OR outcome IN (` + sqlEnum(controlcontract.SafeOutcomes()) + `)),
			 safe_reason          TEXT CHECK(safe_reason IS NULL OR safe_reason IN (` + sqlEnum(controlcontract.SafeReasons()) + `)),
			 cancellation_cause   TEXT CHECK(cancellation_cause IS NULL OR cancellation_cause IN (` + sqlEnum(controlcontract.CancellationCauses()) + `)),
			 subject_expires_at   TEXT CHECK(` + sqlNullableControlTimestampCheck("subject_expires_at") + `),
			 subject_updated_at   TEXT CHECK(` + sqlNullableControlTimestampCheck("subject_updated_at") + `),
			 parameter_digest     BLOB CHECK(parameter_digest IS NULL OR (typeof(parameter_digest)='blob' AND length(parameter_digest)=32)),
			 binding_digest       BLOB CHECK(binding_digest IS NULL OR (typeof(binding_digest)='blob' AND length(binding_digest)=32)),
			 action_set_digest    BLOB CHECK(action_set_digest IS NULL OR (typeof(action_set_digest)='blob' AND length(action_set_digest)=32)),
			 result_digest        BLOB CHECK(result_digest IS NULL OR (typeof(result_digest)='blob' AND length(result_digest)=32)),
			 correlation_id       TEXT CHECK(correlation_id IS NULL OR ` + sqlUUIDCheck("correlation_id") + `),
			 server_recorded_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("server_recorded_at") + `),
			 CHECK((actor_user_id IS NULL AND user_id IS NULL AND principal_kind IS NULL AND actor_session_credential_id IS NULL AND actor_api_key_id IS NULL) OR
			       (actor_user_id IS NOT NULL AND user_id IS NOT NULL AND principal_kind IS NOT NULL AND actor_user_id=user_id AND
			        ` + sqlTypedPrincipalCheck("principal_kind", "actor_session_credential_id", "actor_api_key_id") + `)),
			 CHECK((executor_user_id IS NULL AND executor_principal_kind IS NULL AND executor_session_credential_id IS NULL AND executor_api_key_id IS NULL) OR
			       (executor_user_id IS NOT NULL AND executor_principal_kind='api_key' AND
			        ` + sqlTypedPrincipalCheck("executor_principal_kind", "executor_session_credential_id", "executor_api_key_id") + `)),
			 CHECK((event_kind='runtime_changed' AND runtime_state IS NOT NULL AND runtime_revision IS NOT NULL) OR
			       (event_kind<>'runtime_changed' AND runtime_state IS NULL AND runtime_revision IS NULL)),
			 CHECK((grant_id IS NULL)=(grant_revision IS NULL)),
			 CHECK((lease_id IS NULL)=(lease_revision IS NULL)),
			 CHECK((input_request_id IS NULL)=(input_request_revision IS NULL)),
			 CHECK((command_id IS NULL)=(command_status_revision IS NULL)),
			 CHECK((event_kind IN ('grant_issued','grant_renewed','grant_revoked','grant_expired','lease_issued','lease_renewed','lease_revoked','lease_expired')
			        AND subject_expires_at IS NOT NULL AND subject_updated_at IS NOT NULL) OR
			       (event_kind NOT IN ('grant_issued','grant_renewed','grant_revoked','grant_expired','lease_issued','lease_renewed','lease_revoked','lease_expired')
			        AND subject_expires_at IS NULL AND subject_updated_at IS NULL)),
			 CHECK((event_kind IN ('grant_issued','grant_renewed','grant_revoked','grant_expired') AND grant_id IS NOT NULL AND grant_revision IS NOT NULL AND lease_id IS NULL AND input_request_id IS NULL AND command_id IS NULL AND cancellation_run_id IS NULL AND cancellation_command_id IS NULL) OR
			       (event_kind IN ('lease_issued','lease_renewed','lease_revoked','lease_expired') AND grant_id IS NULL AND lease_id IS NOT NULL AND lease_revision IS NOT NULL AND input_request_id IS NULL AND command_id IS NULL AND cancellation_run_id IS NULL AND cancellation_command_id IS NULL) OR
			       (event_kind IN ('input_requested','input_resolved','input_superseded','input_expired','input_cancelled','input_run_terminal') AND grant_id IS NULL AND lease_id IS NULL AND input_request_id IS NOT NULL AND input_request_revision IS NOT NULL AND command_id IS NULL AND cancellation_run_id IS NULL AND cancellation_command_id IS NULL) OR
			       (event_kind IN ('runtime_changed','command_created','command_expired','command_withdrawn','command_accepted','command_applied','command_rejected','effect_queued','effect_claimed','effect_outcome_unknown','effect_acknowledged','effect_abandoned','effect_reconciled') AND grant_id IS NULL AND lease_id IS NULL AND input_request_id IS NULL AND command_id IS NOT NULL AND command_status_revision IS NOT NULL AND cancellation_run_id IS NULL AND cancellation_command_id IS NULL) OR
			       (event_kind='cancellation_recorded' AND grant_id IS NULL AND lease_id IS NULL AND input_request_id IS NULL AND command_id IS NULL AND command_status_revision IS NULL AND cancellation_run_id IS NOT NULL AND agent_run_id IS NOT NULL AND agent_run_id=cancellation_run_id AND cancellation_cause IS NOT NULL AND
			        ((cancellation_cause='operator_command' AND cancellation_command_id IS NOT NULL) OR
			         (cancellation_cause<>'operator_command' AND cancellation_command_id IS NULL))))
			)`,
			`CREATE UNIQUE INDEX idx_control_events_grant_sequence
			 ON control_events(grant_id,sequence) WHERE grant_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_events_lease_sequence
			 ON control_events(lease_id,sequence) WHERE lease_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_events_input_sequence
			 ON control_events(input_request_id,sequence) WHERE input_request_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_events_input_requested_revision
			 ON control_events(input_request_id,input_request_revision)
			 WHERE event_kind='input_requested'`,
			`CREATE UNIQUE INDEX idx_control_events_input_terminal_revision
			 ON control_events(input_request_id,input_request_revision)
			 WHERE input_request_id IS NOT NULL AND event_kind<>'input_requested'`,
			`CREATE UNIQUE INDEX idx_control_events_command_kind_revision
			 ON control_events(command_id,event_kind,command_status_revision)
			 WHERE command_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_events_grant_fact
			 ON control_events(grant_id,event_kind,grant_revision,subject_updated_at)
			 WHERE grant_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_events_lease_fact
			 ON control_events(lease_id,event_kind,lease_revision,subject_updated_at)
			 WHERE lease_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_events_command_sequence
			 ON control_events(command_id,sequence) WHERE command_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_control_events_cancellation_sequence
			 ON control_events(cancellation_run_id,sequence) WHERE cancellation_run_id IS NOT NULL`,
			`CREATE INDEX idx_control_events_delivery_tail ON control_events(delivery_id,id)`,
			`CREATE INDEX idx_control_events_run_tail ON control_events(agent_run_id,id)`,
			`CREATE TRIGGER trg_control_events_id_order_guard
			 AFTER INSERT ON control_events
			 WHEN NEW.id<=0 OR NEW.id<>(SELECT MAX(id) FROM control_events)
			 BEGIN SELECT RAISE(ABORT,'control event id is not append ordered'); END`,
			`CREATE TRIGGER trg_control_events_sequence_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.server_recorded_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.sequence<>CASE
			  WHEN NEW.grant_id IS NOT NULL THEN COALESCE((SELECT MAX(sequence)+1 FROM control_events WHERE grant_id=NEW.grant_id),1)
			  WHEN NEW.lease_id IS NOT NULL THEN COALESCE((SELECT MAX(sequence)+1 FROM control_events WHERE lease_id=NEW.lease_id),1)
			  WHEN NEW.input_request_id IS NOT NULL THEN COALESCE((SELECT MAX(sequence)+1 FROM control_events WHERE input_request_id=NEW.input_request_id),1)
			  WHEN NEW.command_id IS NOT NULL THEN COALESCE((SELECT MAX(sequence)+1 FROM control_events WHERE command_id=NEW.command_id),1)
			  ELSE COALESCE((SELECT MAX(sequence)+1 FROM control_events WHERE cancellation_run_id=NEW.cancellation_run_id),1)
			 END
			 BEGIN SELECT RAISE(ABORT,'control event sequence is not contiguous'); END`,
			`CREATE TRIGGER trg_control_events_initial_kind_guard
			 BEFORE INSERT ON control_events
			 WHEN (NEW.sequence=1 AND NOT (
			       (NEW.grant_id IS NOT NULL AND NEW.event_kind='grant_issued') OR
			       (NEW.lease_id IS NOT NULL AND NEW.event_kind='lease_issued') OR
			       (NEW.input_request_id IS NOT NULL AND NEW.event_kind='input_requested') OR
			       (NEW.command_id IS NOT NULL AND NEW.event_kind='command_created') OR
			       (NEW.cancellation_run_id IS NOT NULL AND NEW.event_kind='cancellation_recorded'))) OR
			      (NEW.sequence>1 AND NEW.event_kind IN ('grant_issued','lease_issued','command_created')) OR
			      (NEW.cancellation_run_id IS NOT NULL AND NEW.sequence<>1)
			 BEGIN SELECT RAISE(ABORT,'invalid initial control event'); END`,
			`CREATE TRIGGER trg_control_events_grant_proof_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.grant_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM control_capability_grants grant_row
			  WHERE grant_row.grant_id=NEW.grant_id AND grant_row.revision=NEW.grant_revision
			   AND NEW.actor_user_id=grant_row.actor_user_id AND NEW.user_id=grant_row.user_id
			   AND NEW.principal_kind=grant_row.principal_kind
			   AND NEW.actor_session_credential_id IS grant_row.actor_session_credential_id
			   AND NEW.actor_api_key_id IS grant_row.actor_api_key_id
			   AND NEW.executor_user_id IS NULL AND NEW.executor_principal_kind IS NULL
			   AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id IS NULL AND NEW.device_id IS NULL
			   AND NEW.delivery_id=grant_row.delivery_id AND NEW.root_issue_id=grant_row.root_issue_id
			   AND NEW.issue_revision=grant_row.issue_revision AND NEW.binding_digest=grant_row.binding_digest
			   AND NEW.action_set_digest=grant_row.action_set_digest
			   AND NEW.subject_expires_at=grant_row.expires_at AND NEW.subject_updated_at=grant_row.updated_at
			   AND NEW.attempt_id IS NULL AND NEW.stage_key IS NULL AND NEW.execution_number IS NULL
			   AND NEW.authority_epoch IS NULL AND NEW.reporter_id IS NULL AND NEW.agent_run_id IS NULL
			   AND NEW.action IS NULL AND NEW.command_status IS NULL AND NEW.outcome IS NULL
			   AND NEW.parameter_digest IS NULL AND NEW.result_digest IS NULL AND NEW.cancellation_cause IS NULL
			   AND ((NEW.event_kind IN ('grant_issued','grant_renewed') AND grant_row.revoked_at IS NULL AND NEW.safe_reason IS NULL
			         AND EXISTS(SELECT 1 FROM control_capability_grant_seals seal
			          WHERE seal.grant_id=grant_row.grant_id AND seal.grant_revision=grant_row.revision
			           AND seal.binding_digest=grant_row.binding_digest AND seal.action_set_digest=grant_row.action_set_digest
			           AND seal.action_count=grant_row.action_count)) OR
			        (NEW.event_kind='grant_revoked' AND grant_row.revoked_at IS NOT NULL AND grant_row.revoked_at<grant_row.expires_at
			         AND NEW.server_recorded_at>=grant_row.revoked_at AND NEW.safe_reason IN ('capability_revoked','credential_revoked','authority_changed','stale_target')) OR
			        (NEW.event_kind='grant_expired' AND grant_row.revoked_at IS NOT NULL
			         AND grant_row.revoked_at>=grant_row.expires_at AND NEW.server_recorded_at>=grant_row.revoked_at
			         AND NEW.safe_reason='capability_expired')))
			 BEGIN SELECT RAISE(ABORT,'control grant event lacks exact proof'); END`,
			`CREATE TRIGGER trg_control_events_lease_proof_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.lease_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM control_capability_leases lease
			  WHERE lease.lease_id=NEW.lease_id AND lease.revision=NEW.lease_revision
			   AND NEW.actor_user_id IS NULL AND NEW.user_id IS NULL AND NEW.principal_kind IS NULL
			   AND NEW.actor_session_credential_id IS NULL AND NEW.actor_api_key_id IS NULL
			   AND NEW.executor_user_id=lease.user_id AND NEW.executor_principal_kind='api_key'
			   AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id=lease.actor_api_key_id
			   AND NEW.device_id=lease.device_id AND NEW.delivery_id=lease.delivery_id
			   AND NEW.root_issue_id=lease.root_issue_id AND NEW.issue_revision=lease.issue_revision
			   AND NEW.attempt_id=lease.attempt_id AND NEW.stage_key=lease.stage_key
			   AND NEW.execution_number=lease.execution_number AND NEW.authority_epoch=lease.authority_epoch
			   AND NEW.reporter_id=lease.reporter_id AND NEW.agent_run_id=lease.agent_run_id
			   AND NEW.binding_digest=lease.binding_digest AND NEW.action_set_digest=lease.action_set_digest
			   AND NEW.subject_expires_at=lease.expires_at AND NEW.subject_updated_at=lease.updated_at
			   AND NEW.action IS NULL AND NEW.command_status IS NULL AND NEW.outcome IS NULL
			   AND NEW.parameter_digest IS NULL AND NEW.result_digest IS NULL AND NEW.cancellation_cause IS NULL
			   AND ((NEW.event_kind IN ('lease_issued','lease_renewed') AND lease.revoked_at IS NULL AND NEW.safe_reason IS NULL
			         AND EXISTS(SELECT 1 FROM control_capability_lease_seals seal
			          WHERE seal.lease_id=lease.lease_id AND seal.lease_revision=lease.revision
			           AND seal.binding_digest=lease.binding_digest AND seal.action_set_digest=lease.action_set_digest
			           AND seal.action_count=lease.action_count)) OR
			        (NEW.event_kind='lease_revoked' AND lease.revoked_at IS NOT NULL AND lease.revoked_at<lease.expires_at
			         AND NEW.server_recorded_at>=lease.revoked_at AND NEW.safe_reason IN ('lease_revoked','credential_revoked','authority_changed','stale_target')) OR
			        (NEW.event_kind='lease_expired' AND lease.revoked_at IS NOT NULL
			         AND lease.revoked_at>=lease.expires_at AND NEW.server_recorded_at>=lease.revoked_at
			         AND NEW.safe_reason='lease_expired')))
			 BEGIN SELECT RAISE(ABORT,'control lease event lacks exact proof'); END`,
			`CREATE TRIGGER trg_control_events_capability_graph_guard
			 BEFORE INSERT ON control_events
			 WHEN (NEW.grant_id IS NOT NULL AND NEW.sequence>1 AND NOT (
			        (NEW.event_kind='grant_renewed' AND (
			          (COALESCE((SELECT grant_revision FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1),0)=NEW.grant_revision
			           AND COALESCE((SELECT event_kind FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1),'') IN ('grant_issued','grant_renewed')
			           AND NEW.subject_updated_at>(SELECT subject_updated_at FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1)
			           AND NEW.subject_expires_at>(SELECT subject_expires_at FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1)) OR
			          (COALESCE((SELECT grant_revision FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1),0)=NEW.grant_revision-1
			           AND COALESCE((SELECT event_kind FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1),'') IN ('grant_revoked','grant_expired')))) OR
			        (NEW.event_kind IN ('grant_revoked','grant_expired')
			         AND COALESCE((SELECT grant_revision FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1),0)=NEW.grant_revision
			         AND COALESCE((SELECT event_kind FROM control_events WHERE grant_id=NEW.grant_id ORDER BY sequence DESC LIMIT 1),'') IN ('grant_issued','grant_renewed')))) OR
			      (NEW.lease_id IS NOT NULL AND NEW.sequence>1 AND NOT (
			        (NEW.event_kind='lease_renewed' AND (
			          (COALESCE((SELECT lease_revision FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1),0)=NEW.lease_revision
			           AND COALESCE((SELECT event_kind FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1),'') IN ('lease_issued','lease_renewed')
			           AND NEW.subject_updated_at>(SELECT subject_updated_at FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1)
			           AND NEW.subject_expires_at>(SELECT subject_expires_at FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1)) OR
			          (COALESCE((SELECT lease_revision FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1),0)=NEW.lease_revision-1
			           AND COALESCE((SELECT event_kind FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1),'') IN ('lease_revoked','lease_expired')))) OR
			        (NEW.event_kind IN ('lease_revoked','lease_expired')
			         AND COALESCE((SELECT lease_revision FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1),0)=NEW.lease_revision
			         AND COALESCE((SELECT event_kind FROM control_events WHERE lease_id=NEW.lease_id ORDER BY sequence DESC LIMIT 1),'') IN ('lease_issued','lease_renewed'))))
			 BEGIN SELECT RAISE(ABORT,'invalid capability event transition'); END`,
			`CREATE TRIGGER trg_control_events_input_proof_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.input_request_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM control_input_requests request
			  JOIN control_capability_leases lease ON lease.lease_id=request.lease_id AND lease.revision=request.lease_revision
			  WHERE request.request_id=NEW.input_request_id AND request.revision=NEW.input_request_revision
			   AND (NEW.event_kind='input_requested' OR EXISTS(
			    SELECT 1 FROM control_events requested
			    WHERE requested.input_request_id=request.request_id
			     AND requested.input_request_revision=request.revision
			     AND requested.event_kind='input_requested' AND requested.sequence<NEW.sequence))
			   AND NEW.delivery_id=request.delivery_id AND NEW.root_issue_id=request.root_issue_id
			   AND NEW.issue_revision=request.issue_revision AND NEW.attempt_id=request.attempt_id
			   AND NEW.stage_key=request.stage_key AND NEW.execution_number=request.execution_number
			   AND NEW.authority_epoch=request.authority_epoch AND NEW.reporter_id=request.reporter_id
			   AND NEW.agent_run_id=request.agent_run_id AND NEW.binding_digest=lease.binding_digest
			   AND NEW.action_set_digest IS NULL AND NEW.cancellation_cause IS NULL
			   AND ((NEW.event_kind='input_requested'
			     AND EXISTS(SELECT 1 FROM control_input_request_seals request_seal
			      WHERE request_seal.request_id=request.request_id AND request_seal.request_revision=request.revision)
			     AND EXISTS(SELECT 1 FROM control_input_request_states request_state
			      WHERE request_state.request_id=request.request_id AND request_state.current_revision=request.revision
			       AND request_state.terminal_event_id IS NULL)
			     AND EXISTS(SELECT 1 FROM control_events lease_fact
			      WHERE lease_fact.lease_id=lease.lease_id AND lease_fact.lease_revision=lease.revision
			       AND lease_fact.event_kind IN ('lease_issued','lease_renewed')
			       AND lease_fact.subject_expires_at=lease.expires_at AND lease_fact.subject_updated_at=lease.updated_at)
			     AND ((request.revision=1 AND NEW.sequence=1) OR
			          (request.revision>1 AND NEW.sequence>1
			           AND COALESCE((SELECT input_request_revision FROM control_events
			                         WHERE input_request_id=request.request_id ORDER BY sequence DESC LIMIT 1),0)=request.revision-1
			           AND COALESCE((SELECT event_kind FROM control_events
			                         WHERE input_request_id=request.request_id ORDER BY sequence DESC LIMIT 1),'')='input_superseded'
			           AND EXISTS(
			           SELECT 1 FROM control_input_request_states state WHERE state.request_id=request.request_id
			            AND state.current_revision=request.revision AND state.terminal_event_id IS NULL)))
			     AND NEW.actor_user_id IS NULL AND NEW.user_id IS NULL AND NEW.principal_kind IS NULL
			     AND NEW.actor_session_credential_id IS NULL AND NEW.actor_api_key_id IS NULL
			     AND NEW.executor_user_id=lease.user_id AND NEW.executor_principal_kind='api_key'
			     AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id=lease.actor_api_key_id
			     AND NEW.device_id=lease.device_id
			     AND NEW.action IS NULL AND NEW.command_status IS NULL AND NEW.outcome IS NULL
			     AND NEW.safe_reason IS NULL AND NEW.parameter_digest=request.request_digest AND NEW.result_digest IS NULL) OR
			    (NEW.event_kind='input_resolved' AND EXISTS(
			     SELECT 1 FROM control_input_resolution_events terminal
			     JOIN control_input_request_states state ON state.request_id=terminal.request_id
			      AND state.current_revision=terminal.request_revision AND state.terminal_event_id=terminal.id
			     JOIN control_commands command ON command.command_id=terminal.command_id
			     JOIN control_outbox outbox ON outbox.command_id=command.command_id AND outbox.delivery_state='acknowledged'
			     WHERE terminal.request_id=request.request_id AND terminal.request_revision=request.revision
			      AND terminal.event_digest=NEW.result_digest AND command.status='applied' AND command.action='input.respond'
			      AND EXISTS(SELECT 1 FROM control_events command_fact
			       WHERE command_fact.command_id=command.command_id AND command_fact.event_kind='command_applied')
			      AND EXISTS(SELECT 1 FROM control_events effect_fact
			       WHERE effect_fact.command_id=command.command_id
			        AND effect_fact.event_kind IN ('effect_acknowledged','effect_reconciled'))
			      AND NEW.actor_user_id=command.actor_user_id AND NEW.user_id=command.user_id
			      AND NEW.principal_kind=command.principal_kind
			      AND NEW.actor_session_credential_id IS command.actor_session_credential_id
			      AND NEW.actor_api_key_id IS command.actor_api_key_id
			      AND NEW.executor_user_id=outbox.claim_user_id AND NEW.executor_principal_kind='api_key'
			      AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id=outbox.claim_api_key_id
			      AND NEW.device_id=outbox.claim_device_id AND NEW.action='input.respond'
			      AND NEW.command_status='applied' AND NEW.outcome='applied' AND NEW.safe_reason IS NULL
			      AND NEW.parameter_digest=command.parameter_digest)) OR
			    (NEW.event_kind IN ('input_superseded','input_expired','input_cancelled','input_run_terminal')
			     AND NEW.actor_user_id IS NULL AND NEW.user_id IS NULL AND NEW.principal_kind IS NULL
			     AND NEW.actor_session_credential_id IS NULL AND NEW.actor_api_key_id IS NULL
			     AND NEW.executor_user_id IS NULL AND NEW.executor_principal_kind IS NULL
			     AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id IS NULL AND NEW.device_id IS NULL
			     AND NEW.action IS NULL AND NEW.command_status IS NULL AND NEW.outcome IS NULL
			     AND NEW.parameter_digest IS NULL AND NEW.result_digest IS NOT NULL
			     AND NEW.safe_reason=CASE NEW.event_kind
			      WHEN 'input_superseded' THEN 'input_superseded'
			      WHEN 'input_expired' THEN 'input_expired'
			      WHEN 'input_cancelled' THEN 'cancelled'
			      ELSE 'run_terminal' END
			     AND (NEW.event_kind<>'input_expired' OR NEW.server_recorded_at>=request.expires_at)
			     AND (NEW.event_kind<>'input_run_terminal' OR EXISTS(
			      SELECT 1 FROM agent_runs run WHERE run.id=request.agent_run_id AND run.status NOT IN ('queued','running')))
			     AND (NEW.event_kind<>'input_cancelled' OR EXISTS(
			      SELECT 1 FROM agent_run_cancellation_facts fact WHERE fact.run_id=request.agent_run_id)
			      AND EXISTS(SELECT 1 FROM control_events cancellation_fact
			       WHERE cancellation_fact.event_kind='cancellation_recorded'
			        AND cancellation_fact.cancellation_run_id=request.agent_run_id))
			     AND EXISTS(
			      SELECT 1 FROM control_input_resolution_events terminal
			      JOIN control_input_request_states state ON state.request_id=terminal.request_id
			       AND state.current_revision=terminal.request_revision AND state.terminal_event_id=terminal.id
			      WHERE terminal.request_id=request.request_id AND terminal.request_revision=request.revision
			       AND terminal.event_digest=NEW.result_digest AND terminal.safe_reason=NEW.safe_reason
			       AND terminal.event_kind=CASE NEW.event_kind
			        WHEN 'input_superseded' THEN 'superseded'
			        WHEN 'input_expired' THEN 'expired'
			        WHEN 'input_cancelled' THEN 'cancelled'
			        ELSE 'run_terminal' END))))
			 BEGIN SELECT RAISE(ABORT,'control input event lacks exact proof'); END`,
			`CREATE TRIGGER trg_control_events_command_proof_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.command_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM control_commands command
			  WHERE command.command_id=NEW.command_id AND command.status_revision=NEW.command_status_revision
			   AND NEW.actor_user_id=command.actor_user_id AND NEW.user_id=command.user_id
			   AND NEW.principal_kind=command.principal_kind
			   AND NEW.actor_session_credential_id IS command.actor_session_credential_id
			   AND NEW.actor_api_key_id IS command.actor_api_key_id
			   AND NEW.delivery_id=command.delivery_id AND NEW.root_issue_id=command.root_issue_id
			   AND NEW.issue_revision=command.issue_revision AND NEW.attempt_id IS command.attempt_id
			   AND NEW.stage_key IS command.stage_key AND NEW.execution_number IS command.execution_number
			   AND NEW.authority_epoch IS command.authority_epoch AND NEW.reporter_id IS command.reporter_id
			   AND NEW.agent_run_id IS command.agent_run_id AND NEW.action=command.action
			   AND NEW.command_status=command.status AND NEW.outcome IS command.outcome
			   AND NEW.safe_reason IS command.safe_reason AND NEW.parameter_digest=command.parameter_digest
			   AND NEW.binding_digest=command.target_snapshot_digest AND NEW.action_set_digest=command.grant_action_digest
			   AND NEW.result_digest IS command.result_digest AND NEW.cancellation_cause IS NULL
			   AND ((NEW.event_kind='command_created' AND command.status='pending_confirmation'
			         AND EXISTS(SELECT 1 FROM control_capability_grants grant_row
			          JOIN control_events grant_fact ON grant_fact.grant_id=grant_row.grant_id
			           AND grant_fact.grant_revision=grant_row.revision
			           AND grant_fact.event_kind IN ('grant_issued','grant_renewed')
			          WHERE grant_row.grant_id=command.grant_id AND grant_row.revision=command.grant_revision
			           AND grant_fact.subject_expires_at=command.grant_expires_at
			           AND grant_fact.subject_updated_at=grant_row.updated_at)
			         AND (command.action IN ('issue.priority.set','run.cancel.queued') OR EXISTS(
			          SELECT 1 FROM control_capability_leases lease
			          JOIN control_events lease_fact ON lease_fact.lease_id=lease.lease_id
			           AND lease_fact.lease_revision=lease.revision
			           AND lease_fact.event_kind IN ('lease_issued','lease_renewed')
			          WHERE lease.lease_id=command.lease_id AND lease.revision=command.lease_revision
			           AND lease_fact.subject_expires_at=command.lease_expires_at
			           AND lease_fact.subject_updated_at=lease.updated_at))
			         AND (command.action<>'input.respond' OR EXISTS(
			          SELECT 1 FROM control_events input_fact
			          WHERE input_fact.input_request_id=command.input_request_id
			           AND input_fact.input_request_revision=command.input_request_revision
			           AND input_fact.event_kind='input_requested'))
			         AND (command.action NOT IN ('run.pause','run.resume') OR command.runtime_revision=1 OR EXISTS(
			          SELECT 1 FROM control_events runtime_fact
			          WHERE runtime_fact.agent_run_id=command.agent_run_id
			           AND runtime_fact.event_kind='runtime_changed'
			           AND runtime_fact.runtime_revision=command.runtime_revision))) OR
			        (NEW.event_kind='command_accepted' AND command.status='accepted' AND command.outcome IS NULL) OR
			        (NEW.event_kind='command_expired' AND command.status='expired' AND command.safe_reason='confirmation_expired') OR
			        (NEW.event_kind='command_withdrawn' AND command.status='expired' AND command.safe_reason='withdrawn') OR
			        (NEW.event_kind='command_applied' AND command.status='applied' AND command.outcome='applied') OR
			        (NEW.event_kind='command_rejected' AND command.status='rejected' AND command.outcome='rejected') OR
			        (NEW.event_kind='effect_queued' AND command.status='accepted' AND command.outcome IS NULL AND EXISTS(
			         SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=command.command_id
			          AND outbox.delivery_state='queued' AND outbox.effect_digest=command.canonical_digest)) OR
			        (NEW.event_kind='effect_claimed' AND command.status='accepted' AND command.outcome IS NULL AND EXISTS(
			         SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=command.command_id
			          AND outbox.delivery_state='claimed' AND outbox.safe_reason IS NULL
			          AND NEW.executor_user_id=outbox.claim_user_id AND NEW.executor_principal_kind='api_key'
			          AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id=outbox.claim_api_key_id
			          AND NEW.device_id=outbox.claim_device_id)) OR
			        (NEW.event_kind='effect_outcome_unknown' AND command.status='accepted' AND command.outcome='outcome_unknown'
			         AND command.safe_reason='runner_lost' AND EXISTS(
			          SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=command.command_id
			           AND outbox.delivery_state='claimed' AND outbox.safe_reason='runner_lost'
			           AND NEW.executor_user_id=outbox.claim_user_id AND NEW.executor_principal_kind='api_key'
			           AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id=outbox.claim_api_key_id
			           AND NEW.device_id=outbox.claim_device_id)) OR
			        (NEW.event_kind IN ('effect_acknowledged','effect_reconciled') AND command.status IN ('applied','rejected') AND EXISTS(
			         SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=command.command_id
			          AND outbox.delivery_state='acknowledged' AND outbox.result_digest=command.result_digest
			          AND NEW.executor_user_id=outbox.claim_user_id AND NEW.executor_principal_kind='api_key'
			          AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id=outbox.claim_api_key_id
			          AND NEW.device_id=outbox.claim_device_id)) OR
			        (NEW.event_kind='effect_abandoned' AND command.status='rejected' AND EXISTS(
			         SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=command.command_id
			          AND outbox.delivery_state='abandoned' AND outbox.safe_reason=command.safe_reason)) OR
			        (NEW.event_kind='runtime_changed' AND command.status='applied'
			         AND command.action IN ('run.pause','run.resume') AND EXISTS(
			          SELECT 1 FROM control_runtime_states runtime WHERE runtime.agent_run_id=command.agent_run_id
			           AND runtime.last_command_id=command.command_id AND runtime.last_result_digest=command.result_digest
			           AND runtime.revision=command.runtime_revision+1 AND NEW.runtime_revision=runtime.revision
			           AND runtime.state=CASE command.action WHEN 'run.pause' THEN 'paused' ELSE 'running' END
			           AND NEW.runtime_state=runtime.state)
			         AND EXISTS(SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=command.command_id
			          AND outbox.delivery_state='acknowledged' AND NEW.executor_user_id=outbox.claim_user_id
			          AND NEW.executor_principal_kind='api_key' AND NEW.executor_session_credential_id IS NULL
			          AND NEW.executor_api_key_id=outbox.claim_api_key_id AND NEW.device_id=outbox.claim_device_id))))
			 BEGIN SELECT RAISE(ABORT,'control command event lacks exact proof'); END`,
			`CREATE TRIGGER trg_control_events_command_executor_shape_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.command_id IS NOT NULL AND
			  ((NEW.event_kind IN ('effect_claimed','effect_outcome_unknown','effect_acknowledged','effect_reconciled','runtime_changed') AND
			    (NEW.executor_user_id IS NULL OR NEW.executor_principal_kind<>'api_key' OR NEW.executor_api_key_id IS NULL OR NEW.device_id IS NULL)) OR
			   (NEW.event_kind NOT IN ('effect_claimed','effect_outcome_unknown','effect_acknowledged','effect_reconciled','runtime_changed') AND
			    (NEW.executor_user_id IS NOT NULL OR NEW.executor_principal_kind IS NOT NULL OR
			     NEW.executor_session_credential_id IS NOT NULL OR NEW.executor_api_key_id IS NOT NULL OR NEW.device_id IS NOT NULL)))
			 BEGIN SELECT RAISE(ABORT,'control command event executor shape is invalid'); END`,
			`CREATE TRIGGER trg_control_events_command_graph_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.command_id IS NOT NULL AND NOT (
			  (NEW.event_kind='command_created' AND NEW.sequence=1) OR
			  (NEW.event_kind='command_accepted' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='command_created')
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('command_accepted','command_expired','command_withdrawn'))) OR
			  (NEW.event_kind IN ('command_expired','command_withdrawn') AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='command_created')
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('command_accepted','command_expired','command_withdrawn'))) OR
			  (NEW.event_kind='effect_queued' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='command_accepted')
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('effect_queued','command_applied','command_rejected','command_expired','command_withdrawn'))) OR
			  (NEW.event_kind='effect_claimed' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='effect_queued')
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('effect_claimed','effect_abandoned','effect_acknowledged','effect_reconciled'))) OR
			  (NEW.event_kind='effect_outcome_unknown' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='effect_claimed')
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('effect_outcome_unknown','effect_acknowledged','effect_reconciled'))) OR
			  (NEW.event_kind='effect_acknowledged' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='effect_claimed')
			   AND EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('command_applied','command_rejected'))
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('effect_outcome_unknown','effect_acknowledged','effect_reconciled','effect_abandoned'))) OR
			  (NEW.event_kind='effect_reconciled' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='effect_outcome_unknown')
			   AND EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('command_applied','command_rejected'))
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('effect_acknowledged','effect_reconciled','effect_abandoned'))) OR
			  (NEW.event_kind='effect_abandoned' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='effect_queued')
			   AND EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='command_rejected')
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('effect_claimed','effect_abandoned','effect_acknowledged','effect_reconciled'))) OR
			  (NEW.event_kind IN ('command_applied','command_rejected') AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='command_accepted')
			   AND ((NEW.command_status_revision=3 AND NOT EXISTS(
			          SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			           AND prior.event_kind='effect_outcome_unknown')) OR
			        (NEW.command_status_revision=4 AND EXISTS(
			          SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			           AND prior.event_kind='effect_outcome_unknown')))
			   AND ((NEW.action IN ('issue.priority.set','run.cancel.queued')) OR
			        (NEW.action NOT IN ('issue.priority.set','run.cancel.queued')
			         AND EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='effect_queued')
			         AND ((EXISTS(SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=NEW.command_id AND outbox.delivery_state='acknowledged')
			               AND EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='effect_claimed')) OR
			              (NEW.event_kind='command_rejected' AND EXISTS(
			               SELECT 1 FROM control_outbox outbox WHERE outbox.command_id=NEW.command_id AND outbox.delivery_state='abandoned')))))
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('command_applied','command_rejected'))) OR
			  (NEW.event_kind='runtime_changed' AND EXISTS(
			   SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id
			    AND prior.event_kind IN ('effect_acknowledged','effect_reconciled'))
			   AND NOT EXISTS(SELECT 1 FROM control_events prior WHERE prior.command_id=NEW.command_id AND prior.event_kind='runtime_changed')))
			 BEGIN SELECT RAISE(ABORT,'invalid command event transition'); END`,
			`CREATE TRIGGER trg_control_events_cancellation_proof_guard
			 BEFORE INSERT ON control_events
			 WHEN NEW.cancellation_run_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM agent_run_cancellation_facts fact
			  WHERE fact.run_id=NEW.cancellation_run_id AND fact.cancellation_cause=NEW.cancellation_cause
			   AND NEW.agent_run_id=fact.run_id AND fact.command_id IS NEW.cancellation_command_id AND NEW.server_recorded_at>=fact.recorded_at
			   AND NEW.safe_reason IS NULL
			   AND ((fact.cancellation_cause='operator_command' AND EXISTS(
			    SELECT 1 FROM control_commands command WHERE command.command_id=fact.command_id
			     AND command.agent_run_id=fact.run_id AND command.status='applied' AND command.outcome='applied'
			     AND command.action IN ('run.cancel.queued','run.cancel.running')
			     AND EXISTS(SELECT 1 FROM control_events command_fact
			      WHERE command_fact.command_id=command.command_id AND command_fact.event_kind='command_applied')
			     AND (command.action='run.cancel.queued' OR EXISTS(
			      SELECT 1 FROM control_events effect_fact WHERE effect_fact.command_id=command.command_id
			       AND effect_fact.event_kind IN ('effect_acknowledged','effect_reconciled')))
			     AND NEW.actor_user_id=command.actor_user_id AND NEW.user_id=command.user_id
			     AND NEW.principal_kind=command.principal_kind
			     AND NEW.actor_session_credential_id IS command.actor_session_credential_id
			     AND NEW.actor_api_key_id IS command.actor_api_key_id
			     AND NEW.executor_user_id IS NULL AND NEW.executor_principal_kind IS NULL
			     AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id IS NULL AND NEW.device_id IS NULL
			     AND NEW.delivery_id=command.delivery_id AND NEW.root_issue_id=command.root_issue_id
			     AND NEW.issue_revision=command.issue_revision AND NEW.attempt_id=command.attempt_id
			     AND NEW.stage_key=command.stage_key AND NEW.execution_number=command.execution_number
			     AND NEW.authority_epoch=command.authority_epoch AND NEW.reporter_id=command.reporter_id
			     AND NEW.action=command.action AND NEW.command_status=command.status AND NEW.outcome=command.outcome
			     AND NEW.parameter_digest=command.parameter_digest AND NEW.binding_digest=command.target_snapshot_digest
			     AND NEW.action_set_digest=command.grant_action_digest AND NEW.result_digest=command.result_digest)) OR
			   (fact.cancellation_cause<>'operator_command'
			    AND NEW.actor_user_id IS NULL AND NEW.user_id IS NULL AND NEW.principal_kind IS NULL
			    AND NEW.actor_session_credential_id IS NULL AND NEW.actor_api_key_id IS NULL
			    AND NEW.executor_user_id IS NULL AND NEW.executor_principal_kind IS NULL
			    AND NEW.executor_session_credential_id IS NULL AND NEW.executor_api_key_id IS NULL AND NEW.device_id IS NULL
			    AND NEW.delivery_id IS NULL AND NEW.root_issue_id IS NULL AND NEW.issue_revision IS NULL
			    AND NEW.attempt_id IS NULL AND NEW.stage_key IS NULL AND NEW.execution_number IS NULL
			    AND NEW.authority_epoch IS NULL AND NEW.reporter_id IS NULL
			    AND NEW.action IS NULL AND NEW.command_status IS NULL AND NEW.outcome IS NULL
			    AND NEW.parameter_digest IS NULL AND NEW.binding_digest IS NULL
			    AND NEW.action_set_digest IS NULL AND NEW.result_digest IS NULL)))
			 BEGIN SELECT RAISE(ABORT,'cancellation event lacks exact proof'); END`,
			`CREATE TRIGGER trg_control_events_no_update
			 BEFORE UPDATE ON control_events
			 BEGIN SELECT RAISE(ABORT,'control events are append-only'); END`,
			`CREATE TRIGGER trg_control_events_no_delete
			 BEFORE DELETE ON control_events
			 BEGIN SELECT RAISE(ABORT,'control events are append-only'); END`,
			`CREATE TRIGGER trg_control_grants_audit_precondition
			 BEFORE UPDATE ON control_capability_grants
			 WHEN NOT EXISTS(
			  SELECT 1 FROM control_events fact
			  WHERE fact.id=(SELECT MAX(latest.id) FROM control_events latest WHERE latest.grant_id=OLD.grant_id)
			   AND fact.grant_id=OLD.grant_id AND fact.grant_revision=OLD.revision
			   AND fact.event_kind IN ('grant_issued','grant_renewed')
			   AND fact.subject_expires_at=OLD.expires_at AND fact.subject_updated_at=OLD.updated_at)
			 BEGIN SELECT RAISE(ABORT,'control grant update lacks current audit fact'); END`,
			`CREATE TRIGGER trg_control_grants_renewal_target_guard
			 BEFORE UPDATE ON control_capability_grants
			 WHEN OLD.revoked_at IS NULL AND NEW.revoked_at IS NULL AND NOT EXISTS(
			  SELECT 1 FROM deliveries delivery
			  JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
			  JOIN projects project ON project.id=issue.project_id
			  WHERE delivery.id=OLD.delivery_id AND delivery.delivery_key=OLD.delivery_key
			   AND delivery.issue_id=OLD.root_issue_id AND issue.project_id=OLD.project_id
			   AND (SELECT revision FROM issue_control_revisions WHERE issue_id=issue.id)=OLD.issue_revision
			   AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			                WHERE event.delivery_id=delivery.id),0)=OLD.delivery_revision
			   AND (project.status IN ('active','frozen') OR
			        (project.status='archived' AND NOT EXISTS(
			         SELECT 1 FROM control_capability_grant_actions action
			         WHERE action.grant_id=OLD.grant_id AND action.grant_revision=OLD.revision
			          AND action.action NOT IN ('run.cancel.queued','run.cancel.running')))))
			 BEGIN SELECT RAISE(ABORT,'control grant renewal target is stale'); END`,
			`CREATE TRIGGER trg_control_leases_audit_precondition
			 BEFORE UPDATE ON control_capability_leases
			 WHEN NOT EXISTS(
			  SELECT 1 FROM control_events fact
			  WHERE fact.id=(SELECT MAX(latest.id) FROM control_events latest WHERE latest.lease_id=OLD.lease_id)
			   AND fact.lease_id=OLD.lease_id AND fact.lease_revision=OLD.revision
			   AND fact.event_kind IN ('lease_issued','lease_renewed')
			   AND fact.subject_expires_at=OLD.expires_at AND fact.subject_updated_at=OLD.updated_at)
			 BEGIN SELECT RAISE(ABORT,'control lease update lacks current audit fact'); END`,
			`CREATE TRIGGER trg_control_leases_renewal_target_guard
			 BEFORE UPDATE ON control_capability_leases
			 WHEN OLD.revoked_at IS NULL AND NEW.revoked_at IS NULL AND NOT EXISTS(
			  SELECT 1 FROM api_keys runner_key
			  JOIN users runner_user ON runner_user.id=runner_key.user_id AND runner_user.status='active'
			  JOIN deliveries delivery ON delivery.id=OLD.delivery_id AND delivery.delivery_key=OLD.delivery_key
			   AND delivery.issue_id=OLD.root_issue_id
			  JOIN issues issue ON issue.id=delivery.issue_id AND issue.project_id=OLD.project_id AND issue.deleted_at IS NULL
			  JOIN projects project ON project.id=issue.project_id
			  JOIN delivery_agent_run_links link ON link.delivery_id=OLD.delivery_id AND link.attempt_id=OLD.attempt_id
			   AND link.stage_key=OLD.stage_key AND link.execution_number=OLD.execution_number
			   AND link.execution_start_stage_event_id=OLD.execution_start_stage_event_id
			   AND link.agent_run_id=OLD.agent_run_id AND link.reporter_id=OLD.reporter_id
			  JOIN agent_runs run ON run.id=OLD.agent_run_id AND run.issue_id=OLD.root_issue_id AND run.status='running'
			  JOIN delivery_agent_run_activations activation ON activation.delivery_id=OLD.delivery_id
			   AND activation.attempt_id=OLD.attempt_id AND activation.stage_key=OLD.stage_key
			   AND activation.execution_number=OLD.execution_number AND activation.authority_epoch=OLD.authority_epoch
			   AND activation.agent_run_id=OLD.agent_run_id AND activation.reporter_id=OLD.reporter_id
			   AND activation.authority_stage_event_id=OLD.authority_stage_event_id
			  JOIN delivery_stage_latest latest ON latest.delivery_id=OLD.delivery_id AND latest.attempt_id=OLD.attempt_id
			   AND latest.stage_key=OLD.stage_key AND latest.execution_number=OLD.execution_number
			   AND latest.execution_start_stage_event_id=OLD.execution_start_stage_event_id
			   AND latest.authority_epoch=OLD.authority_epoch AND latest.current_reporter_id=OLD.reporter_id
			   AND latest.authority_stage_event_id=OLD.authority_stage_event_id
			  WHERE runner_key.id=OLD.actor_api_key_id AND runner_key.user_id=OLD.user_id
			   AND runner_key.disabled_at IS NULL
			   AND (runner_key.expires_at IS NULL OR runner_key.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			   AND (SELECT revision FROM issue_control_revisions WHERE issue_id=issue.id)=OLD.issue_revision
			   AND COALESCE((SELECT MAX(event.delivery_revision) FROM delivery_events event
			                WHERE event.delivery_id=delivery.id),0)=OLD.delivery_revision
			   AND (project.status IN ('active','frozen') OR
			        (project.status='archived' AND NOT EXISTS(
			         SELECT 1 FROM control_capability_lease_actions action
			         WHERE action.lease_id=OLD.lease_id AND action.lease_revision=OLD.revision
			          AND action.action<>'run.cancel.running'))))
			 BEGIN SELECT RAISE(ABORT,'control lease renewal target is stale'); END`,
		}},

		// M148 / PAI-810: versioned external-stage handoffs. The schema is
		// deliberately additive: M144's canonical stage enums and M147's
		// control enums remain closed. Owner and dependency reports have
		// independent append-only streams and latest projections, so a Janus
		// dependency fact cannot become a canonical stage fact by construction.
		{148, []string{
			`CREATE VIEW external_stage_user_roles AS
			 SELECT id,status,
			  CASE
			   WHEN is_super_admin=1 THEN 'super_admin'
			   WHEN role_key='member' AND role IN ('admin','external') THEN role
			   WHEN role_key IN ('admin','member','external','super_admin') THEN role_key
			   WHEN role IN ('admin','member','external') THEN role
			   ELSE 'member'
			  END AS effective_role
			 FROM users`,
			`CREATE TABLE external_stage_reporter_registrations (
			 id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			 delivery_id        INTEGER NOT NULL,
			 project_id         INTEGER NOT NULL,
			 user_id            INTEGER NOT NULL,
			 api_key_id         INTEGER NOT NULL,
			 reporter_id        INTEGER NOT NULL,
			 reporter_class     TEXT NOT NULL CHECK(reporter_class IN ('pharos','janus')),
			 reporter_role      TEXT NOT NULL CHECK(reporter_role IN ('owner','dependency')),
			 dependency_key     TEXT CHECK(dependency_key IS NULL OR
			  (length(CAST(dependency_key AS BLOB)) BETWEEN 1 AND 64 AND
			   dependency_key GLOB '[a-z]*' AND dependency_key NOT GLOB '*[^a-z0-9._-]*')),
			 workflow_symbol    TEXT CHECK(workflow_symbol IS NULL OR
			  (length(CAST(workflow_symbol AS BLOB)) BETWEEN 1 AND 64 AND
			   workflow_symbol GLOB '[a-z]*' AND workflow_symbol NOT GLOB '*[^a-z0-9._-]*')),
			 environment_symbol TEXT CHECK(environment_symbol IS NULL OR
			  (length(CAST(environment_symbol AS BLOB)) BETWEEN 1 AND 64 AND
			   environment_symbol GLOB '[a-z]*' AND environment_symbol NOT GLOB '*[^a-z0-9._-]*')),
			 allow_deployment         INTEGER NOT NULL CHECK(allow_deployment IN (0,1)),
			 allow_verification       INTEGER NOT NULL CHECK(allow_verification IN (0,1)),
			 allow_authorization      INTEGER NOT NULL CHECK(allow_authorization IN (0,1)),
			 allow_credential_handoff INTEGER NOT NULL CHECK(allow_credential_handoff IN (0,1)),
			 created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 revoked_at         TEXT,
			 UNIQUE(delivery_id,id),
			 FOREIGN KEY(delivery_id,reporter_id) REFERENCES delivery_reporters(delivery_id,id),
			 FOREIGN KEY(api_key_id) REFERENCES api_keys(id),
			 FOREIGN KEY(user_id) REFERENCES users(id),
			 FOREIGN KEY(project_id) REFERENCES projects(id),
			 CHECK((reporter_class='pharos' AND reporter_role='owner' AND dependency_key IS NULL AND
			        workflow_symbol IS NOT NULL AND environment_symbol IS NOT NULL AND
			        allow_deployment=1 AND allow_verification=1 AND
			        allow_authorization=0 AND allow_credential_handoff=0) OR
			       (reporter_class='janus' AND reporter_role='dependency' AND dependency_key IS NOT NULL AND
			        workflow_symbol IS NULL AND environment_symbol IS NULL AND
			        allow_deployment=0 AND allow_verification=0 AND
			        allow_authorization=1 AND allow_credential_handoff=1)),
			 CHECK(revoked_at IS NULL OR revoked_at>=created_at)
			)`,
			`CREATE INDEX idx_external_stage_registrations_key
			 ON external_stage_reporter_registrations(api_key_id,delivery_id)`,
			`CREATE UNIQUE INDEX idx_external_stage_registration_owner_exact
			 ON external_stage_reporter_registrations(
			  delivery_id,api_key_id,reporter_class,reporter_role,workflow_symbol,environment_symbol)
			 WHERE reporter_role='owner' AND revoked_at IS NULL`,
			`CREATE UNIQUE INDEX idx_external_stage_registration_dependency_exact
			 ON external_stage_reporter_registrations(
			  delivery_id,api_key_id,reporter_class,reporter_role,dependency_key)
			 WHERE reporter_role='dependency' AND revoked_at IS NULL`,
			`CREATE TRIGGER trg_external_stage_registration_insert_guard
			 BEFORE INSERT ON external_stage_reporter_registrations
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.revoked_at IS NOT NULL OR
			  NOT EXISTS(SELECT 1 FROM deliveries delivery
			   JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
			   JOIN projects project ON project.id=issue.project_id
			   JOIN delivery_reporters reporter ON reporter.delivery_id=delivery.id AND reporter.id=NEW.reporter_id
			   JOIN api_keys api_key ON api_key.id=NEW.api_key_id AND api_key.user_id=NEW.user_id
			   JOIN external_stage_user_roles user ON user.id=NEW.user_id AND user.status='active'
			   WHERE delivery.id=NEW.delivery_id AND issue.project_id=NEW.project_id
			    AND reporter.reporter_type='external' AND api_key.disabled_at IS NULL
			    AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
			    AND user.effective_role<>'external'
			    AND (user.effective_role IN ('admin','super_admin') OR
			         EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=user.id
			          AND membership.project_id=NEW.project_id AND membership.access_level IN ('viewer','editor')) OR
			         (user.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			          WHERE membership.user_id=user.id AND membership.project_id=NEW.project_id))))
			 BEGIN SELECT RAISE(ABORT,'external stage registration binding is not current'); END`,
			`CREATE TRIGGER trg_external_stage_registration_update_guard
			 BEFORE UPDATE ON external_stage_reporter_registrations
			 WHEN NEW.id<>OLD.id OR NEW.delivery_id<>OLD.delivery_id OR NEW.project_id<>OLD.project_id OR
			  NEW.user_id<>OLD.user_id OR NEW.api_key_id<>OLD.api_key_id OR NEW.reporter_id<>OLD.reporter_id OR
			  NEW.reporter_class<>OLD.reporter_class OR NEW.reporter_role<>OLD.reporter_role OR
			  NEW.dependency_key IS NOT OLD.dependency_key OR NEW.workflow_symbol IS NOT OLD.workflow_symbol OR
			  NEW.environment_symbol IS NOT OLD.environment_symbol OR NEW.allow_deployment<>OLD.allow_deployment OR
			  NEW.allow_verification<>OLD.allow_verification OR NEW.allow_authorization<>OLD.allow_authorization OR
			  NEW.allow_credential_handoff<>OLD.allow_credential_handoff OR NEW.created_at<>OLD.created_at OR
			  OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
			 BEGIN SELECT RAISE(ABORT,'external stage registration identity is immutable'); END`,
			`CREATE TRIGGER trg_external_stage_registration_no_delete BEFORE DELETE ON external_stage_reporter_registrations
			 BEGIN SELECT RAISE(ABORT,'external stage registrations are append-only'); END`,

			`CREATE TABLE external_stage_prerequisite_sets (
			 delivery_id                   INTEGER NOT NULL,
			 attempt_id                    INTEGER NOT NULL,
			 stage_key                     TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number              INTEGER NOT NULL CHECK(execution_number>0),
			 execution_start_stage_event_id INTEGER NOT NULL,
			 authority_epoch               INTEGER NOT NULL CHECK(authority_epoch>0),
			 authority_stage_event_id      INTEGER NOT NULL,
			 declared_count                INTEGER NOT NULL CHECK(declared_count BETWEEN 0 AND 16),
			 created_at                    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 sealed_at                     TEXT,
			 PRIMARY KEY(attempt_id,stage_key,execution_number,authority_epoch),
			 UNIQUE(delivery_id,attempt_id,stage_key,execution_number,authority_epoch),
			 FOREIGN KEY(delivery_id,attempt_id) REFERENCES delivery_attempts(delivery_id,id),
			 FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,execution_start_stage_event_id)
			  REFERENCES delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,id),
			 FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,authority_stage_event_id)
			  REFERENCES delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,id)
			) WITHOUT ROWID`,
			`CREATE TABLE external_stage_prerequisites (
			 delivery_id      INTEGER NOT NULL,
			 attempt_id       INTEGER NOT NULL,
			 stage_key        TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number INTEGER NOT NULL CHECK(execution_number>0),
			 authority_epoch  INTEGER NOT NULL CHECK(authority_epoch>0),
			 dependency_key   TEXT NOT NULL CHECK(length(CAST(dependency_key AS BLOB)) BETWEEN 1 AND 64 AND
			  dependency_key GLOB '[a-z]*' AND dependency_key NOT GLOB '*[^a-z0-9._-]*'),
			 registration_id  INTEGER NOT NULL,
			 requirement      TEXT NOT NULL CHECK(requirement IN ('required','optional')),
			 ordinal          INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 15),
			 created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 PRIMARY KEY(attempt_id,stage_key,execution_number,authority_epoch,dependency_key),
			 UNIQUE(attempt_id,stage_key,execution_number,authority_epoch,ordinal),
			 FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,authority_epoch)
			  REFERENCES external_stage_prerequisite_sets(delivery_id,attempt_id,stage_key,execution_number,authority_epoch),
			 FOREIGN KEY(delivery_id,registration_id)
			  REFERENCES external_stage_reporter_registrations(delivery_id,id)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_external_stage_prerequisite_set_guard
			 BEFORE INSERT ON external_stage_prerequisite_sets
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.sealed_at IS NOT NULL OR
			  NOT EXISTS(SELECT 1 FROM delivery_stage_latest latest
			   WHERE latest.delivery_id=NEW.delivery_id AND latest.attempt_id=NEW.attempt_id
			    AND latest.stage_key=NEW.stage_key AND latest.execution_number=NEW.execution_number
			    AND latest.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			    AND latest.authority_epoch=NEW.authority_epoch
			    AND latest.authority_stage_event_id=NEW.authority_stage_event_id
			    AND NOT EXISTS(SELECT 1 FROM delivery_stage_events terminal WHERE terminal.id=latest.semantic_stage_event_id
			     AND terminal.semantic_state IN ('succeeded','failed','cancelled','draft_ready')))
			 BEGIN SELECT RAISE(ABORT,'external stage prerequisite set is stale'); END`,
			`CREATE TRIGGER trg_external_stage_prerequisite_guard
			 BEFORE INSERT ON external_stage_prerequisites
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_prerequisite_sets
			  WHERE attempt_id=NEW.attempt_id AND stage_key=NEW.stage_key AND execution_number=NEW.execution_number
			   AND authority_epoch=NEW.authority_epoch AND sealed_at IS NULL) OR
			  NOT EXISTS(SELECT 1 FROM external_stage_reporter_registrations registration
			   WHERE registration.id=NEW.registration_id AND registration.delivery_id=NEW.delivery_id
			    AND registration.reporter_class='janus' AND registration.reporter_role='dependency'
			    AND registration.dependency_key=NEW.dependency_key AND registration.revoked_at IS NULL)
			 BEGIN SELECT RAISE(ABORT,'invalid external stage prerequisite'); END`,
			`CREATE TRIGGER trg_external_stage_prerequisite_sets_update_guard
			 BEFORE UPDATE ON external_stage_prerequisite_sets
			 WHEN OLD.sealed_at IS NOT NULL OR NEW.sealed_at IS NULL OR
			  NEW.delivery_id<>OLD.delivery_id OR NEW.attempt_id<>OLD.attempt_id OR NEW.stage_key<>OLD.stage_key OR
			  NEW.execution_number<>OLD.execution_number OR
			  NEW.execution_start_stage_event_id<>OLD.execution_start_stage_event_id OR
			  NEW.authority_epoch<>OLD.authority_epoch OR NEW.authority_stage_event_id<>OLD.authority_stage_event_id OR
			  NEW.declared_count<>OLD.declared_count OR NEW.created_at<>OLD.created_at OR
			  (SELECT COUNT(*) FROM external_stage_prerequisites prerequisite
			   WHERE prerequisite.attempt_id=OLD.attempt_id AND prerequisite.stage_key=OLD.stage_key
			    AND prerequisite.execution_number=OLD.execution_number
			    AND prerequisite.authority_epoch=OLD.authority_epoch)<>OLD.declared_count
			 BEGIN SELECT RAISE(ABORT,'invalid external stage prerequisite seal'); END`,
			`CREATE TRIGGER trg_external_stage_prerequisite_sets_no_delete BEFORE DELETE ON external_stage_prerequisite_sets
			 BEGIN SELECT RAISE(ABORT,'external stage prerequisite sets are immutable'); END`,
			`CREATE TRIGGER trg_external_stage_prerequisites_no_update BEFORE UPDATE ON external_stage_prerequisites
			 BEGIN SELECT RAISE(ABORT,'external stage prerequisites are immutable'); END`,
			`CREATE TRIGGER trg_external_stage_prerequisites_no_delete BEFORE DELETE ON external_stage_prerequisites
			 BEGIN SELECT RAISE(ABORT,'external stage prerequisites are immutable'); END`,

			`CREATE TABLE external_stage_handoffs (
			 id                             INTEGER PRIMARY KEY AUTOINCREMENT,
			 handoff_id                     TEXT NOT NULL UNIQUE CHECK(length(CAST(handoff_id AS BLOB))=26 AND
			  handoff_id NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'),
			 delivery_id                    INTEGER NOT NULL,
			 delivery_key                   TEXT NOT NULL,
			 root_issue_id                  INTEGER NOT NULL,
			 project_id                     INTEGER NOT NULL,
			 attempt_id                     INTEGER NOT NULL,
			 attempt_number                 INTEGER NOT NULL CHECK(attempt_number>0),
			 plan_revision                  INTEGER NOT NULL CHECK(plan_revision>0),
			 plan_digest                    BLOB NOT NULL CHECK(typeof(plan_digest)='blob' AND length(plan_digest)=32),
			 stage_key                      TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number               INTEGER NOT NULL CHECK(execution_number>0),
			 execution_start_stage_event_id INTEGER NOT NULL,
			 predecessor_digest             BLOB NOT NULL CHECK(typeof(predecessor_digest)='blob' AND length(predecessor_digest)=32),
			 authority_epoch                INTEGER NOT NULL CHECK(authority_epoch>0),
			 authority_stage_event_id       INTEGER NOT NULL,
			 reporter_registration_id       INTEGER NOT NULL,
			 reporter_id                    INTEGER NOT NULL,
			 api_key_id                     INTEGER NOT NULL,
			 reporter_class                 TEXT NOT NULL CHECK(reporter_class IN ('pharos','janus')),
			 reporter_role                  TEXT NOT NULL CHECK(reporter_role IN ('owner','dependency')),
			 dependency_key                 TEXT CHECK(dependency_key IS NULL OR
			  (length(CAST(dependency_key AS BLOB)) BETWEEN 1 AND 64 AND dependency_key GLOB '[a-z]*'
			   AND dependency_key NOT GLOB '*[^a-z0-9._-]*')),
			 workflow_symbol                TEXT,
			 environment_symbol             TEXT,
			 allow_deployment               INTEGER NOT NULL CHECK(allow_deployment IN (0,1)),
			 allow_verification             INTEGER NOT NULL CHECK(allow_verification IN (0,1)),
			 allow_authorization            INTEGER NOT NULL CHECK(allow_authorization IN (0,1)),
			 allow_credential_handoff       INTEGER NOT NULL CHECK(allow_credential_handoff IN (0,1)),
			 contract_major                 INTEGER NOT NULL CHECK(contract_major=1),
			 fixture_digest                 BLOB NOT NULL CHECK(typeof(fixture_digest)='blob' AND
			  fixture_digest=x'0318f4025902c9d5dd790384950cc9daebb16e02e79a4a90ce7dddc673e68bed'),
			 credential_epoch               INTEGER NOT NULL DEFAULT 0 CHECK(credential_epoch>=0),
			 secret_digest                  BLOB CHECK(secret_digest IS NULL OR (typeof(secret_digest)='blob' AND length(secret_digest)=32)),
			 expires_at                     TEXT NOT NULL,
			 context_digest                 BLOB NOT NULL CHECK(typeof(context_digest)='blob' AND length(context_digest)=32),
			 lifecycle_state                TEXT NOT NULL DEFAULT 'issued' CHECK(lifecycle_state IN
			  ('issued','accepted','active','waiting','blocked','succeeded','failed')),
			 last_sequence                  INTEGER NOT NULL DEFAULT 0 CHECK(last_sequence>=0),
			 created_at                     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 accepted_at                    TEXT,
			 terminal_at                    TEXT,
			 revoked_at                     TEXT,
			 UNIQUE(delivery_id,id),
			 FOREIGN KEY(delivery_id,root_issue_id) REFERENCES deliveries(id,issue_id),
			 FOREIGN KEY(delivery_id,attempt_id) REFERENCES delivery_attempts(delivery_id,id),
			 FOREIGN KEY(delivery_id,reporter_registration_id)
			  REFERENCES external_stage_reporter_registrations(delivery_id,id),
			 FOREIGN KEY(delivery_id,reporter_id) REFERENCES delivery_reporters(delivery_id,id),
			 FOREIGN KEY(api_key_id) REFERENCES api_keys(id),
			 FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,execution_start_stage_event_id)
			  REFERENCES delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,id),
			 FOREIGN KEY(delivery_id,attempt_id,stage_key,execution_number,authority_stage_event_id)
			  REFERENCES delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,id),
			 CHECK(julianday(expires_at)>julianday(created_at)),
			 CHECK((credential_epoch=0 AND secret_digest IS NULL) OR (credential_epoch>0 AND secret_digest IS NOT NULL)),
			 CHECK((lifecycle_state='issued' AND accepted_at IS NULL AND terminal_at IS NULL) OR
			       (lifecycle_state IN ('accepted','active','waiting','blocked') AND accepted_at IS NOT NULL AND terminal_at IS NULL) OR
			       (lifecycle_state IN ('succeeded','failed') AND accepted_at IS NOT NULL AND terminal_at IS NOT NULL)),
			 CHECK(revoked_at IS NULL OR revoked_at>=created_at),
			 CHECK((reporter_class='pharos' AND reporter_role='owner' AND dependency_key IS NULL AND
			        workflow_symbol IS NOT NULL AND environment_symbol IS NOT NULL AND
			        allow_deployment=1 AND allow_verification=1 AND allow_authorization=0 AND allow_credential_handoff=0) OR
			       (reporter_class='janus' AND reporter_role='dependency' AND dependency_key IS NOT NULL AND
			        workflow_symbol IS NULL AND environment_symbol IS NULL AND
			        allow_deployment=0 AND allow_verification=0 AND allow_authorization=1 AND allow_credential_handoff=1))
			)`,
			`CREATE UNIQUE INDEX idx_external_stage_owner_handoff
			 ON external_stage_handoffs(attempt_id,stage_key,execution_number,authority_epoch)
			 WHERE reporter_role='owner' AND revoked_at IS NULL`,
			`CREATE UNIQUE INDEX idx_external_stage_dependency_handoff
			 ON external_stage_handoffs(attempt_id,stage_key,execution_number,authority_epoch,dependency_key,reporter_registration_id)
			 WHERE reporter_role='dependency' AND revoked_at IS NULL`,
			`CREATE INDEX idx_external_stage_handoff_api_key ON external_stage_handoffs(api_key_id,handoff_id)`,
			`CREATE TRIGGER trg_external_stage_handoff_insert_guard
			 BEFORE INSERT ON external_stage_handoffs
			 WHEN NEW.created_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR NEW.credential_epoch<>0 OR
			  NEW.secret_digest IS NOT NULL OR NEW.lifecycle_state<>'issued' OR NEW.last_sequence<>0 OR
			  NEW.accepted_at IS NOT NULL OR NEW.terminal_at IS NOT NULL OR NEW.revoked_at IS NOT NULL OR
			  NOT EXISTS(SELECT 1 FROM deliveries delivery
			   JOIN issues issue ON issue.id=delivery.issue_id AND issue.deleted_at IS NULL
			   JOIN delivery_attempts attempt ON attempt.delivery_id=delivery.id AND attempt.id=NEW.attempt_id
			    AND attempt.attempt_number=(SELECT MAX(current_attempt.attempt_number) FROM delivery_attempts current_attempt
			     WHERE current_attempt.delivery_id=delivery.id)
			   JOIN delivery_stage_latest latest ON latest.delivery_id=delivery.id AND latest.attempt_id=attempt.id
			    AND latest.stage_key=NEW.stage_key AND latest.execution_number=NEW.execution_number
			    AND latest.execution_start_stage_event_id=NEW.execution_start_stage_event_id
			    AND latest.authority_epoch=NEW.authority_epoch AND latest.authority_stage_event_id=NEW.authority_stage_event_id
			   JOIN external_stage_reporter_registrations registration
			    ON registration.id=NEW.reporter_registration_id AND registration.delivery_id=delivery.id
			    AND registration.project_id=NEW.project_id
			    AND registration.reporter_id=NEW.reporter_id AND registration.api_key_id=NEW.api_key_id
			    AND registration.reporter_class=NEW.reporter_class AND registration.reporter_role=NEW.reporter_role
			    AND registration.dependency_key IS NEW.dependency_key AND registration.workflow_symbol IS NEW.workflow_symbol
			    AND registration.environment_symbol IS NEW.environment_symbol
			    AND registration.allow_deployment=NEW.allow_deployment
			    AND registration.allow_verification=NEW.allow_verification
			    AND registration.allow_authorization=NEW.allow_authorization
			    AND registration.allow_credential_handoff=NEW.allow_credential_handoff
			    AND registration.revoked_at IS NULL
			    AND EXISTS(SELECT 1 FROM external_stage_setup_events setup
			     WHERE setup.registration_id=registration.id AND setup.event_kind='registration_created'
			      AND setup.delivery_id=registration.delivery_id AND setup.project_id=registration.project_id)
			   JOIN api_keys api_key ON api_key.id=registration.api_key_id AND api_key.user_id=registration.user_id
			    AND api_key.disabled_at IS NULL
			    AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
			   JOIN external_stage_user_roles reporter_user ON reporter_user.id=registration.user_id AND reporter_user.status='active'
			   WHERE delivery.id=NEW.delivery_id AND delivery.delivery_key=NEW.delivery_key
			    AND delivery.issue_id=NEW.root_issue_id AND issue.project_id=NEW.project_id
			    AND attempt.attempt_number=NEW.attempt_number AND attempt.plan_revision=NEW.plan_revision
			    AND NEW.plan_digest=paimos_domain_sha256('paimos.external-stage.plan.v1',
			     printf('%d:%d:%d',attempt.id,attempt.plan_revision,attempt.start_delivery_event_id))
			    AND NEW.predecessor_digest=paimos_domain_sha256('paimos.external-stage.predecessor.v1',
			     printf('%d:%d:%d',latest.execution_start_stage_event_id,latest.authority_stage_event_id,
			      COALESCE(latest.semantic_stage_event_id,0)))
			    AND NEW.context_digest=paimos_domain_sha256('paimos.external-stage.context.v1',delivery.delivery_key,
			     CAST(attempt.id AS TEXT),NEW.stage_key,CAST(NEW.execution_number AS TEXT),
			     CAST(NEW.authority_epoch AS TEXT),CAST(NEW.reporter_registration_id AS TEXT))
			    AND NOT EXISTS(SELECT 1 FROM delivery_stage_events terminal WHERE terminal.id=latest.semantic_stage_event_id
			     AND terminal.semantic_state IN ('succeeded','failed','cancelled','draft_ready'))
			    AND reporter_user.effective_role<>'external'
			    AND (reporter_user.effective_role IN ('admin','super_admin') OR
			         EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=reporter_user.id
			          AND membership.project_id=NEW.project_id AND membership.access_level IN ('viewer','editor')) OR
			         (reporter_user.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			          WHERE membership.user_id=reporter_user.id AND membership.project_id=NEW.project_id)))
			    AND ((NEW.reporter_role='owner' AND NEW.stage_key IN ('deployment','verification')
			          AND latest.current_reporter_id=NEW.reporter_id) OR
			         (NEW.reporter_role='dependency' AND EXISTS(SELECT 1 FROM external_stage_prerequisites prerequisite
			          JOIN external_stage_prerequisite_sets prerequisite_set
			           ON prerequisite_set.attempt_id=prerequisite.attempt_id AND prerequisite_set.stage_key=prerequisite.stage_key
			           AND prerequisite_set.execution_number=prerequisite.execution_number
			           AND prerequisite_set.authority_epoch=prerequisite.authority_epoch AND prerequisite_set.sealed_at IS NOT NULL
			          WHERE prerequisite.attempt_id=NEW.attempt_id AND prerequisite.stage_key=NEW.stage_key
			           AND prerequisite.execution_number=NEW.execution_number AND prerequisite.authority_epoch=NEW.authority_epoch
			           AND prerequisite.dependency_key=NEW.dependency_key
			           AND prerequisite.registration_id=NEW.reporter_registration_id)
			          AND NOT EXISTS(SELECT 1 FROM external_stage_dependency_latest satisfied
			           WHERE satisfied.attempt_id=NEW.attempt_id AND satisfied.stage_key=NEW.stage_key
			            AND satisfied.execution_number=NEW.execution_number AND satisfied.authority_epoch=NEW.authority_epoch
			            AND satisfied.dependency_key=NEW.dependency_key
			            AND satisfied.registration_id=NEW.reporter_registration_id
			            AND satisfied.lifecycle_state='succeeded')))
			  )
			 BEGIN SELECT RAISE(ABORT,'external stage handoff binding is stale'); END`,
			`CREATE TRIGGER trg_external_stage_handoff_binding_guard
			 BEFORE UPDATE ON external_stage_handoffs
			 WHEN NEW.id<>OLD.id OR NEW.handoff_id<>OLD.handoff_id OR NEW.delivery_id<>OLD.delivery_id OR
			  NEW.delivery_key<>OLD.delivery_key OR NEW.root_issue_id<>OLD.root_issue_id OR NEW.project_id<>OLD.project_id OR
			  NEW.attempt_id<>OLD.attempt_id OR NEW.attempt_number<>OLD.attempt_number OR NEW.plan_revision<>OLD.plan_revision OR
			  NEW.plan_digest<>OLD.plan_digest OR NEW.stage_key<>OLD.stage_key OR NEW.execution_number<>OLD.execution_number OR
			  NEW.execution_start_stage_event_id<>OLD.execution_start_stage_event_id OR NEW.predecessor_digest<>OLD.predecessor_digest OR
			  NEW.authority_epoch<>OLD.authority_epoch OR NEW.authority_stage_event_id<>OLD.authority_stage_event_id OR
			  NEW.reporter_registration_id<>OLD.reporter_registration_id OR NEW.reporter_id<>OLD.reporter_id OR
			  NEW.api_key_id<>OLD.api_key_id OR NEW.reporter_class<>OLD.reporter_class OR NEW.reporter_role<>OLD.reporter_role OR
			  NEW.dependency_key IS NOT OLD.dependency_key OR NEW.workflow_symbol IS NOT OLD.workflow_symbol OR
			  NEW.environment_symbol IS NOT OLD.environment_symbol OR NEW.allow_deployment<>OLD.allow_deployment OR
			  NEW.allow_verification<>OLD.allow_verification OR NEW.allow_authorization<>OLD.allow_authorization OR
			  NEW.allow_credential_handoff<>OLD.allow_credential_handoff OR NEW.contract_major<>OLD.contract_major OR
			  NEW.fixture_digest<>OLD.fixture_digest OR NEW.expires_at<>OLD.expires_at OR
			  NEW.context_digest<>OLD.context_digest OR NEW.created_at<>OLD.created_at OR
			  NEW.credential_epoch<OLD.credential_epoch OR NEW.credential_epoch>OLD.credential_epoch+1 OR
			  (NEW.credential_epoch<>OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest) OR
			  NEW.last_sequence<OLD.last_sequence OR NEW.last_sequence>OLD.last_sequence+1 OR
			  (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS NOT OLD.revoked_at) OR
			  (OLD.accepted_at IS NOT NULL AND NEW.accepted_at IS NOT OLD.accepted_at) OR
			  (OLD.terminal_at IS NOT NULL AND NEW.terminal_at IS NOT OLD.terminal_at)
			 BEGIN SELECT RAISE(ABORT,'external stage handoff binding is immutable'); END`,
			`CREATE TRIGGER trg_external_stage_handoff_no_delete BEFORE DELETE ON external_stage_handoffs
			 BEGIN SELECT RAISE(ABORT,'external stage handoffs are append-only'); END`,

			`CREATE TABLE external_stage_operation_events (
			 id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			 handoff_row_id        INTEGER NOT NULL,
			 operation_kind        TEXT NOT NULL CHECK(operation_kind IN ('created','secret_minted','secret_rotated','revoked','accepted')),
			 request_digest        BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 idempotency_digest    BLOB NOT NULL CHECK(typeof(idempotency_digest)='blob' AND length(idempotency_digest)=32),
			 actor_user_id         INTEGER NOT NULL,
			 actor_principal_kind  TEXT NOT NULL CHECK(actor_principal_kind IN ('session','api_key')),
			 actor_session_id      TEXT,
			 actor_api_key_id      INTEGER,
			 credential_epoch      INTEGER NOT NULL CHECK(credential_epoch>=0),
			 sequence              INTEGER CHECK(sequence>0),
			 server_received_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 UNIQUE(handoff_row_id,operation_kind,idempotency_digest),
			 FOREIGN KEY(handoff_row_id) REFERENCES external_stage_handoffs(id),
			 CHECK((actor_principal_kind='session' AND actor_session_id IS NOT NULL AND actor_api_key_id IS NULL) OR
			       (actor_principal_kind='api_key' AND actor_session_id IS NULL AND actor_api_key_id IS NOT NULL)),
			 CHECK((operation_kind='accepted' AND sequence IS NOT NULL) OR (operation_kind<>'accepted' AND sequence IS NULL))
			)`,
			`CREATE UNIQUE INDEX idx_external_stage_operation_causal
			 ON external_stage_operation_events(handoff_row_id,operation_kind,credential_epoch)`,
			`CREATE UNIQUE INDEX idx_external_stage_operation_internal_idempotency
			 ON external_stage_operation_events(actor_user_id,actor_principal_kind,
			  COALESCE(actor_session_id,''),COALESCE(actor_api_key_id,0),operation_kind,idempotency_digest)
			 WHERE operation_kind IN ('created','secret_minted','secret_rotated','revoked')`,
			`CREATE TRIGGER trg_external_stage_operations_no_update BEFORE UPDATE ON external_stage_operation_events
			 BEGIN SELECT RAISE(ABORT,'external stage operations are append-only'); END`,
			`CREATE TRIGGER trg_external_stage_operations_no_delete BEFORE DELETE ON external_stage_operation_events
			 BEGIN SELECT RAISE(ABORT,'external stage operations are append-only'); END`,

			`CREATE TABLE external_stage_report_events (
			 id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			 handoff_row_id        INTEGER NOT NULL,
			 actor_api_key_id      INTEGER NOT NULL REFERENCES api_keys(id),
			 sequence              INTEGER NOT NULL CHECK(sequence>0),
			 credential_epoch      INTEGER NOT NULL CHECK(credential_epoch>0),
			 request_digest        BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 idempotency_digest    BLOB NOT NULL CHECK(typeof(idempotency_digest)='blob' AND length(idempotency_digest)=32),
			 lifecycle_state       TEXT NOT NULL CHECK(lifecycle_state IN ('active','waiting','blocked','succeeded','failed')),
			 observed_at           TEXT NOT NULL,
			 server_received_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 heartbeat              INTEGER NOT NULL CHECK(heartbeat IN (0,1)),
			 declared_blockers     INTEGER NOT NULL CHECK(declared_blockers BETWEEN 0 AND 8),
			 evidence_kind         TEXT CHECK(evidence_kind IN ('deployment','verification','authorization','credential_handoff')),
			 UNIQUE(handoff_row_id,sequence),
			 UNIQUE(handoff_row_id,idempotency_digest),
			 FOREIGN KEY(handoff_row_id) REFERENCES external_stage_handoffs(id),
			 CHECK((heartbeat=1 AND evidence_kind IS NULL AND declared_blockers=0 AND lifecycle_state IN ('active','waiting','blocked')) OR heartbeat=0),
			 CHECK(heartbeat=1 OR lifecycle_state<>'blocked' OR declared_blockers>0),
			 CHECK(lifecycle_state NOT IN ('succeeded','failed') OR declared_blockers=0)
			)`,
			`CREATE INDEX idx_external_stage_report_received ON external_stage_report_events(handoff_row_id,server_received_at DESC)`,
			`CREATE TRIGGER trg_external_stage_reports_no_update BEFORE UPDATE ON external_stage_report_events
			 BEGIN SELECT RAISE(ABORT,'external stage reports are append-only'); END`,
			`CREATE TRIGGER trg_external_stage_reports_no_delete BEFORE DELETE ON external_stage_report_events
			 BEGIN SELECT RAISE(ABORT,'external stage reports are append-only'); END`,
			`CREATE TABLE external_stage_heartbeat_windows (
			 id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			 handoff_row_id     INTEGER NOT NULL REFERENCES external_stage_handoffs(id),
			 actor_api_key_id   INTEGER NOT NULL REFERENCES api_keys(id),
			 credential_epoch   INTEGER NOT NULL CHECK(credential_epoch>0),
			 window_number      INTEGER NOT NULL CHECK(window_number>0),
			 first_sequence     INTEGER NOT NULL CHECK(first_sequence>0),
			 last_sequence      INTEGER NOT NULL CHECK(last_sequence>=first_sequence),
			 heartbeat_count    INTEGER NOT NULL CHECK(heartbeat_count BETWEEN 1 AND 64),
			 lifecycle_state    TEXT NOT NULL CHECK(lifecycle_state IN ('active','waiting','blocked')),
			 replay_json        TEXT NOT NULL CHECK(json_valid(replay_json) AND json_type(replay_json)='array'
			  AND length(CAST(replay_json AS BLOB)) BETWEEN 100 AND 8192),
			 window_started_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 last_observed_at   TEXT NOT NULL,
			 last_received_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 UNIQUE(handoff_row_id,credential_epoch,window_number),
			 CHECK(last_sequence=first_sequence+heartbeat_count-1),
			 CHECK(json_array_length(replay_json)=heartbeat_count)
			)`,
			`CREATE INDEX idx_external_stage_heartbeat_tail
			 ON external_stage_heartbeat_windows(handoff_row_id,credential_epoch,window_number DESC)`,
			`CREATE TRIGGER trg_external_stage_heartbeat_insert_guard
			 BEFORE INSERT ON external_stage_heartbeat_windows
			 WHEN NEW.window_started_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  NEW.last_received_at<>NEW.window_started_at OR NEW.heartbeat_count<>1 OR
			  NEW.first_sequence<>NEW.last_sequence OR
			  EXISTS(SELECT 1 FROM json_each(NEW.replay_json) replay WHERE json_type(replay.value)<>'object' OR
			   (SELECT COUNT(*) FROM json_each(replay.value))<>4 OR
			   EXISTS(SELECT 1 FROM json_each(replay.value) member
			    WHERE member.key NOT IN ('sequence','request_digest','idempotency_digest','server_received_at')) OR
			   json_type(replay.value,'$.sequence')<>'integer' OR
			   json_type(replay.value,'$.request_digest')<>'text' OR
			   json_type(replay.value,'$.idempotency_digest')<>'text' OR
			   json_type(replay.value,'$.server_received_at')<>'text' OR
			   json_extract(replay.value,'$.sequence')<>NEW.last_sequence OR
			   json_extract(replay.value,'$.server_received_at')<>NEW.last_received_at OR
			   length(json_extract(replay.value,'$.request_digest'))<>64 OR
			   json_extract(replay.value,'$.request_digest') GLOB '*[^0-9a-f]*' OR
			   length(json_extract(replay.value,'$.idempotency_digest'))<>64 OR
			   json_extract(replay.value,'$.idempotency_digest') GLOB '*[^0-9a-f]*') OR
			  NOT EXISTS(SELECT 1 FROM external_stage_handoffs handoff
			   JOIN external_stage_reporter_registrations registration ON registration.id=handoff.reporter_registration_id
			    AND registration.delivery_id=handoff.delivery_id AND registration.api_key_id=handoff.api_key_id
			    AND registration.revoked_at IS NULL
			   JOIN api_keys api_key ON api_key.id=handoff.api_key_id AND api_key.user_id=registration.user_id
			    AND api_key.disabled_at IS NULL
			    AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
			   JOIN external_stage_user_roles user ON user.id=registration.user_id AND user.status='active'
			   JOIN issues issue ON issue.id=handoff.root_issue_id AND issue.project_id=handoff.project_id AND issue.deleted_at IS NULL
			   JOIN projects project ON project.id=handoff.project_id AND project.status IN ('active','frozen')
			   JOIN delivery_attempts attempt ON attempt.id=handoff.attempt_id AND attempt.delivery_id=handoff.delivery_id
			    AND attempt.attempt_number=(SELECT MAX(current_attempt.attempt_number) FROM delivery_attempts current_attempt
			     WHERE current_attempt.delivery_id=handoff.delivery_id)
			    AND attempt.attempt_number=handoff.attempt_number AND attempt.plan_revision=handoff.plan_revision
			   JOIN delivery_stage_latest latest ON latest.delivery_id=handoff.delivery_id AND latest.attempt_id=handoff.attempt_id
			    AND latest.stage_key=handoff.stage_key AND latest.execution_number=handoff.execution_number
			    AND latest.execution_start_stage_event_id=handoff.execution_start_stage_event_id
			    AND latest.authority_epoch=handoff.authority_epoch AND latest.authority_stage_event_id=handoff.authority_stage_event_id
			   WHERE handoff.id=NEW.handoff_row_id AND NEW.actor_api_key_id=handoff.api_key_id
			    AND NEW.credential_epoch=handoff.credential_epoch AND handoff.secret_digest IS NOT NULL
			    AND handoff.revoked_at IS NULL AND handoff.terminal_at IS NULL
			    AND julianday(handoff.expires_at)>julianday('now')
			    AND handoff.lifecycle_state=NEW.lifecycle_state AND NEW.last_sequence=handoff.last_sequence+1
			    AND NEW.window_number=COALESCE((SELECT MAX(prior.window_number)+1 FROM external_stage_heartbeat_windows prior
			     WHERE prior.handoff_row_id=handoff.id AND prior.credential_epoch=handoff.credential_epoch),1)
			    AND user.effective_role<>'external'
			    AND (user.effective_role IN ('admin','super_admin') OR
			         EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=user.id
			          AND membership.project_id=handoff.project_id AND membership.access_level IN ('viewer','editor')) OR
			         (user.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			          WHERE membership.user_id=user.id AND membership.project_id=handoff.project_id)))
			    AND ((handoff.reporter_role='owner' AND latest.current_reporter_id=handoff.reporter_id) OR
			         (handoff.reporter_role='dependency' AND EXISTS(SELECT 1 FROM external_stage_prerequisites prerequisite
			          JOIN external_stage_prerequisite_sets prerequisite_set
			           ON prerequisite_set.attempt_id=prerequisite.attempt_id AND prerequisite_set.stage_key=prerequisite.stage_key
			           AND prerequisite_set.execution_number=prerequisite.execution_number
			           AND prerequisite_set.authority_epoch=prerequisite.authority_epoch AND prerequisite_set.sealed_at IS NOT NULL
			          WHERE prerequisite.attempt_id=handoff.attempt_id AND prerequisite.stage_key=handoff.stage_key
			           AND prerequisite.execution_number=handoff.execution_number AND prerequisite.authority_epoch=handoff.authority_epoch
			           AND prerequisite.dependency_key=handoff.dependency_key
			           AND prerequisite.registration_id=handoff.reporter_registration_id))))
			 BEGIN SELECT RAISE(ABORT,'external stage heartbeat window lacks a current exact binding'); END`,
			`CREATE TRIGGER trg_external_stage_heartbeat_update_guard
			 BEFORE UPDATE ON external_stage_heartbeat_windows
			 WHEN NEW.id<>OLD.id OR NEW.handoff_row_id<>OLD.handoff_row_id OR NEW.actor_api_key_id<>OLD.actor_api_key_id OR
			  NEW.credential_epoch<>OLD.credential_epoch OR NEW.window_number<>OLD.window_number OR
			  NEW.first_sequence<>OLD.first_sequence OR NEW.window_started_at<>OLD.window_started_at OR
			  NEW.last_sequence<>OLD.last_sequence+1 OR NEW.heartbeat_count<>OLD.heartbeat_count+1 OR
			  NEW.last_received_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  NEW.heartbeat_count>64 OR julianday(NEW.last_received_at)-julianday(OLD.window_started_at)>30.0/86400.0 OR
			  NEW.lifecycle_state<>OLD.lifecycle_state OR json_array_length(NEW.replay_json)<>NEW.heartbeat_count OR
			  json_remove(NEW.replay_json,'$[#-1]')<>OLD.replay_json OR
			  json_extract(NEW.replay_json,'$[#-1].sequence')<>NEW.last_sequence OR
			  EXISTS(SELECT 1 FROM json_each(NEW.replay_json) replay WHERE json_type(replay.value)<>'object' OR
			   (SELECT COUNT(*) FROM json_each(replay.value))<>4 OR
			   EXISTS(SELECT 1 FROM json_each(replay.value) member
			    WHERE member.key NOT IN ('sequence','request_digest','idempotency_digest','server_received_at')) OR
			   json_type(replay.value,'$.sequence')<>'integer' OR
			   json_type(replay.value,'$.request_digest')<>'text' OR
			   json_type(replay.value,'$.idempotency_digest')<>'text' OR
			   json_type(replay.value,'$.server_received_at')<>'text' OR
			   CAST(json_extract(replay.value,'$.sequence') AS INTEGER)<NEW.first_sequence OR
			   CAST(json_extract(replay.value,'$.sequence') AS INTEGER)>NEW.last_sequence OR
			   length(json_extract(replay.value,'$.request_digest'))<>64 OR
			   json_extract(replay.value,'$.request_digest') GLOB '*[^0-9a-f]*' OR
			   length(json_extract(replay.value,'$.idempotency_digest'))<>64 OR
			   json_extract(replay.value,'$.idempotency_digest') GLOB '*[^0-9a-f]*' OR
			   (json_extract(replay.value,'$.sequence')=NEW.last_sequence AND
			    json_extract(replay.value,'$.server_received_at')<>NEW.last_received_at)) OR
			  NOT EXISTS(SELECT 1 FROM external_stage_handoffs handoff
			   JOIN external_stage_reporter_registrations registration ON registration.id=handoff.reporter_registration_id
			    AND registration.api_key_id=handoff.api_key_id AND registration.revoked_at IS NULL
			   JOIN api_keys api_key ON api_key.id=handoff.api_key_id AND api_key.user_id=registration.user_id
			    AND api_key.disabled_at IS NULL
			   JOIN external_stage_user_roles user ON user.id=registration.user_id AND user.status='active'
			   JOIN delivery_attempts attempt ON attempt.id=handoff.attempt_id AND attempt.delivery_id=handoff.delivery_id
			    AND attempt.attempt_number=(SELECT MAX(current_attempt.attempt_number) FROM delivery_attempts current_attempt
			     WHERE current_attempt.delivery_id=handoff.delivery_id)
			   JOIN delivery_stage_latest latest ON latest.delivery_id=handoff.delivery_id AND latest.attempt_id=handoff.attempt_id
			    AND latest.stage_key=handoff.stage_key AND latest.execution_number=handoff.execution_number
			    AND latest.execution_start_stage_event_id=handoff.execution_start_stage_event_id
			    AND latest.authority_epoch=handoff.authority_epoch AND latest.authority_stage_event_id=handoff.authority_stage_event_id
			   WHERE handoff.id=OLD.handoff_row_id AND handoff.api_key_id=OLD.actor_api_key_id
			    AND handoff.credential_epoch=OLD.credential_epoch AND handoff.lifecycle_state=OLD.lifecycle_state
			    AND handoff.last_sequence=OLD.last_sequence AND handoff.revoked_at IS NULL AND handoff.terminal_at IS NULL
			    AND julianday(handoff.expires_at)>julianday('now')
			    AND ((handoff.reporter_role='owner' AND latest.current_reporter_id=handoff.reporter_id) OR
			         handoff.reporter_role='dependency')
			    AND user.effective_role<>'external'
			    AND (user.effective_role IN ('admin','super_admin') OR
			         EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=user.id
			          AND membership.project_id=handoff.project_id AND membership.access_level IN ('viewer','editor')) OR
			         (user.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			          WHERE membership.user_id=user.id AND membership.project_id=handoff.project_id))))
			 BEGIN SELECT RAISE(ABORT,'invalid heartbeat window coalescing advance'); END`,
			`CREATE TRIGGER trg_external_stage_heartbeat_no_delete BEFORE DELETE ON external_stage_heartbeat_windows
			 BEGIN SELECT RAISE(ABORT,'heartbeat windows are durable'); END`,

			`CREATE TABLE external_stage_pharos_evidence (
			 report_event_id    INTEGER PRIMARY KEY REFERENCES external_stage_report_events(id),
			 evidence_kind      TEXT NOT NULL CHECK(evidence_kind IN ('deployment','verification')),
			 workflow_symbol    TEXT NOT NULL CHECK(length(CAST(workflow_symbol AS BLOB)) BETWEEN 1 AND 64 AND
			  workflow_symbol GLOB '[a-z]*' AND workflow_symbol NOT GLOB '*[^a-z0-9._-]*'),
			 environment_symbol TEXT NOT NULL CHECK(length(CAST(environment_symbol AS BLOB)) BETWEEN 1 AND 64 AND
			  environment_symbol GLOB '[a-z]*' AND environment_symbol NOT GLOB '*[^a-z0-9._-]*'),
			 artifact_version   TEXT NOT NULL CHECK(length(CAST(artifact_version AS BLOB)) BETWEEN 1 AND 64 AND
			  artifact_version GLOB '[A-Za-z0-9]*' AND artifact_version NOT GLOB '*[^A-Za-z0-9._+-]*'),
			 artifact_digest    BLOB NOT NULL CHECK(typeof(artifact_digest)='blob' AND length(artifact_digest)=32),
			 commit_digest      TEXT NOT NULL CHECK(length(CAST(commit_digest AS BLOB)) IN (40,64) AND
			  commit_digest NOT GLOB '*[^0-9a-f]*'),
			 result             TEXT NOT NULL CHECK(result IN ('succeeded','failed')),
			 observed_at        TEXT NOT NULL,
			 server_received_at TEXT NOT NULL,
			 CHECK(evidence_kind<>'verification' OR result IN ('succeeded','failed'))
			) WITHOUT ROWID`,
			`CREATE TABLE external_stage_janus_evidence (
			 report_event_id    INTEGER PRIMARY KEY REFERENCES external_stage_report_events(id),
			 evidence_kind      TEXT NOT NULL CHECK(evidence_kind IN ('authorization','credential_handoff')),
			 result             TEXT NOT NULL CHECK(result IN ('satisfied','blocked')),
			 authorized         INTEGER CHECK(authorized IN (0,1)),
			 credential_ready   INTEGER CHECK(credential_ready IN (0,1)),
			 observed_at        TEXT NOT NULL,
			 server_received_at TEXT NOT NULL,
			 CHECK((evidence_kind='authorization' AND authorized IS NOT NULL AND credential_ready IS NULL AND
			        ((result='satisfied' AND authorized=1) OR (result='blocked' AND authorized=0))) OR
			       (evidence_kind='credential_handoff' AND authorized IS NULL AND credential_ready IS NOT NULL AND
			        ((result='satisfied' AND credential_ready=1) OR (result='blocked' AND credential_ready=0))))
			) WITHOUT ROWID`,
			`CREATE TABLE external_stage_report_blockers (
			 report_event_id INTEGER NOT NULL REFERENCES external_stage_report_events(id),
			 ordinal         INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 7),
			 blocker_code    TEXT NOT NULL CHECK(blocker_code IN
			  ('dependency_pending','dependency_failed','reporter_stale','external_waiting')),
			 PRIMARY KEY(report_event_id,ordinal),
			 UNIQUE(report_event_id,blocker_code)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_external_stage_pharos_evidence_no_update BEFORE UPDATE ON external_stage_pharos_evidence
			 BEGIN SELECT RAISE(ABORT,'external stage evidence is immutable'); END`,
			`CREATE TRIGGER trg_external_stage_pharos_evidence_no_delete BEFORE DELETE ON external_stage_pharos_evidence
			 BEGIN SELECT RAISE(ABORT,'external stage evidence is immutable'); END`,
			`CREATE TRIGGER trg_external_stage_janus_evidence_no_update BEFORE UPDATE ON external_stage_janus_evidence
			 BEGIN SELECT RAISE(ABORT,'external stage evidence is immutable'); END`,
			`CREATE TRIGGER trg_external_stage_janus_evidence_no_delete BEFORE DELETE ON external_stage_janus_evidence
			 BEGIN SELECT RAISE(ABORT,'external stage evidence is immutable'); END`,
			`CREATE TRIGGER trg_external_stage_blockers_no_update BEFORE UPDATE ON external_stage_report_blockers
			 BEGIN SELECT RAISE(ABORT,'external stage blockers are immutable'); END`,
			`CREATE TRIGGER trg_external_stage_blockers_no_delete BEFORE DELETE ON external_stage_report_blockers
			 BEGIN SELECT RAISE(ABORT,'external stage blockers are immutable'); END`,

			`CREATE TABLE external_stage_owner_events (
			 id               INTEGER PRIMARY KEY AUTOINCREMENT,
			 delivery_id      INTEGER NOT NULL,
			 attempt_id       INTEGER NOT NULL,
			 stage_key        TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number INTEGER NOT NULL CHECK(execution_number>0),
			 authority_epoch  INTEGER NOT NULL CHECK(authority_epoch>0),
			 handoff_row_id   INTEGER NOT NULL,
			 report_event_id  INTEGER NOT NULL UNIQUE,
			 sequence         INTEGER NOT NULL CHECK(sequence>0),
			 stream_sequence  INTEGER NOT NULL CHECK(stream_sequence>0),
			 lifecycle_state  TEXT NOT NULL CHECK(lifecycle_state IN ('active','waiting','blocked','succeeded','failed')),
			 server_received_at TEXT NOT NULL,
			 UNIQUE(attempt_id,stage_key,execution_number,authority_epoch,stream_sequence),
			 FOREIGN KEY(delivery_id,handoff_row_id) REFERENCES external_stage_handoffs(delivery_id,id),
			 FOREIGN KEY(report_event_id) REFERENCES external_stage_report_events(id)
			)`,
			`CREATE TABLE external_stage_owner_latest (
			 delivery_id      INTEGER NOT NULL,
			 attempt_id       INTEGER NOT NULL,
			 stage_key        TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number INTEGER NOT NULL CHECK(execution_number>0),
			 authority_epoch  INTEGER NOT NULL CHECK(authority_epoch>0),
			 owner_event_id   INTEGER NOT NULL UNIQUE REFERENCES external_stage_owner_events(id),
			 handoff_row_id   INTEGER NOT NULL,
			 report_event_id  INTEGER NOT NULL,
			 sequence         INTEGER NOT NULL CHECK(sequence>0),
			 stream_sequence  INTEGER NOT NULL CHECK(stream_sequence>0),
			 lifecycle_state  TEXT NOT NULL CHECK(lifecycle_state IN ('active','waiting','blocked','succeeded','failed')),
			 updated_at       TEXT NOT NULL,
			 PRIMARY KEY(attempt_id,stage_key,execution_number,authority_epoch),
			 FOREIGN KEY(delivery_id,handoff_row_id) REFERENCES external_stage_handoffs(delivery_id,id),
			 FOREIGN KEY(report_event_id) REFERENCES external_stage_report_events(id)
			) WITHOUT ROWID`,
			`CREATE TABLE external_stage_dependency_events (
			 id                INTEGER PRIMARY KEY AUTOINCREMENT,
			 delivery_id       INTEGER NOT NULL,
			 attempt_id        INTEGER NOT NULL,
			 stage_key         TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number  INTEGER NOT NULL CHECK(execution_number>0),
			 authority_epoch   INTEGER NOT NULL CHECK(authority_epoch>0),
			 dependency_key    TEXT NOT NULL,
			 registration_id   INTEGER NOT NULL,
			 handoff_row_id    INTEGER NOT NULL,
			 report_event_id   INTEGER NOT NULL UNIQUE,
			 credential_epoch  INTEGER NOT NULL CHECK(credential_epoch>0),
			 sequence          INTEGER NOT NULL CHECK(sequence>0),
			 stream_sequence   INTEGER NOT NULL CHECK(stream_sequence>0),
			 lifecycle_state   TEXT NOT NULL CHECK(lifecycle_state IN ('active','waiting','blocked','succeeded','failed')),
			 server_received_at TEXT NOT NULL,
			 UNIQUE(attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id,stream_sequence),
			 FOREIGN KEY(delivery_id,handoff_row_id) REFERENCES external_stage_handoffs(delivery_id,id),
			 FOREIGN KEY(delivery_id,registration_id) REFERENCES external_stage_reporter_registrations(delivery_id,id),
			 FOREIGN KEY(report_event_id) REFERENCES external_stage_report_events(id)
			)`,
			`CREATE TABLE external_stage_dependency_latest (
			 delivery_id        INTEGER NOT NULL,
			 attempt_id         INTEGER NOT NULL,
			 stage_key          TEXT NOT NULL CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number   INTEGER NOT NULL CHECK(execution_number>0),
			 authority_epoch    INTEGER NOT NULL CHECK(authority_epoch>0),
			 dependency_key     TEXT NOT NULL,
			 registration_id    INTEGER NOT NULL,
			 credential_epoch   INTEGER NOT NULL CHECK(credential_epoch>0),
			 dependency_event_id INTEGER NOT NULL UNIQUE REFERENCES external_stage_dependency_events(id),
			 handoff_row_id     INTEGER NOT NULL,
			 report_event_id    INTEGER NOT NULL,
			 sequence           INTEGER NOT NULL CHECK(sequence>0),
			 stream_sequence    INTEGER NOT NULL CHECK(stream_sequence>0),
			 lifecycle_state    TEXT NOT NULL CHECK(lifecycle_state IN ('active','waiting','blocked','succeeded','failed')),
			 updated_at         TEXT NOT NULL,
			 PRIMARY KEY(attempt_id,stage_key,execution_number,authority_epoch,dependency_key,registration_id),
			 FOREIGN KEY(delivery_id,handoff_row_id) REFERENCES external_stage_handoffs(delivery_id,id),
			 FOREIGN KEY(delivery_id,registration_id) REFERENCES external_stage_reporter_registrations(delivery_id,id),
			 FOREIGN KEY(report_event_id) REFERENCES external_stage_report_events(id)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_external_stage_owner_event_guard BEFORE INSERT ON external_stage_owner_events
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_handoffs handoff
			  JOIN external_stage_report_events report ON report.id=NEW.report_event_id AND report.handoff_row_id=handoff.id
			  WHERE handoff.id=NEW.handoff_row_id AND handoff.delivery_id=NEW.delivery_id AND handoff.attempt_id=NEW.attempt_id
			   AND handoff.stage_key=NEW.stage_key AND handoff.execution_number=NEW.execution_number
			   AND handoff.authority_epoch=NEW.authority_epoch AND handoff.reporter_role='owner'
			   AND report.sequence=NEW.sequence AND report.lifecycle_state=NEW.lifecycle_state
			   AND report.server_received_at=NEW.server_received_at
			   AND NEW.stream_sequence=1+MAX(
			    COALESCE((SELECT authority.authority_source_sequence_cutoff FROM delivery_stage_events authority
			     WHERE authority.id=handoff.authority_stage_event_id),0),
			    COALESCE((SELECT MAX(canonical.source_sequence) FROM delivery_stage_events canonical
			     WHERE canonical.attempt_id=handoff.attempt_id AND canonical.stage_key=handoff.stage_key
			      AND canonical.execution_number=handoff.execution_number AND canonical.authority_epoch=handoff.authority_epoch),0),
			    COALESCE((SELECT MAX(prior.stream_sequence) FROM external_stage_owner_events prior
			     WHERE prior.attempt_id=handoff.attempt_id AND prior.stage_key=handoff.stage_key
			      AND prior.execution_number=handoff.execution_number AND prior.authority_epoch=handoff.authority_epoch),0)))
			 BEGIN SELECT RAISE(ABORT,'owner event lacks exact owner report'); END`,
			`CREATE TRIGGER trg_external_stage_dependency_event_guard BEFORE INSERT ON external_stage_dependency_events
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_handoffs handoff
			  JOIN external_stage_report_events report ON report.id=NEW.report_event_id AND report.handoff_row_id=handoff.id
			  JOIN external_stage_prerequisites prerequisite ON prerequisite.attempt_id=handoff.attempt_id
			   AND prerequisite.stage_key=handoff.stage_key AND prerequisite.execution_number=handoff.execution_number
			   AND prerequisite.authority_epoch=handoff.authority_epoch AND prerequisite.dependency_key=handoff.dependency_key
			   AND prerequisite.registration_id=handoff.reporter_registration_id
			  WHERE handoff.id=NEW.handoff_row_id AND handoff.delivery_id=NEW.delivery_id AND handoff.attempt_id=NEW.attempt_id
			   AND handoff.stage_key=NEW.stage_key AND handoff.execution_number=NEW.execution_number
			   AND handoff.authority_epoch=NEW.authority_epoch AND handoff.reporter_role='dependency'
			   AND handoff.dependency_key=NEW.dependency_key AND handoff.reporter_registration_id=NEW.registration_id
			   AND report.credential_epoch=NEW.credential_epoch AND report.sequence=NEW.sequence
			   AND report.lifecycle_state=NEW.lifecycle_state AND report.server_received_at=NEW.server_received_at
			   AND NEW.stream_sequence=1+COALESCE((SELECT MAX(prior.stream_sequence)
			    FROM external_stage_dependency_events prior WHERE prior.attempt_id=handoff.attempt_id
			     AND prior.stage_key=handoff.stage_key AND prior.execution_number=handoff.execution_number
			     AND prior.authority_epoch=handoff.authority_epoch AND prior.dependency_key=handoff.dependency_key
			     AND prior.registration_id=handoff.reporter_registration_id),0))
			 BEGIN SELECT RAISE(ABORT,'dependency event lacks exact declared dependency report'); END`,
			`CREATE TRIGGER trg_external_stage_owner_events_no_update BEFORE UPDATE ON external_stage_owner_events
			 BEGIN SELECT RAISE(ABORT,'external stage owner events are append-only'); END`,
			`CREATE TRIGGER trg_external_stage_owner_events_no_delete BEFORE DELETE ON external_stage_owner_events
			 BEGIN SELECT RAISE(ABORT,'external stage owner events are append-only'); END`,
			`CREATE TRIGGER trg_external_stage_dependency_events_no_update BEFORE UPDATE ON external_stage_dependency_events
			 BEGIN SELECT RAISE(ABORT,'external stage dependency events are append-only'); END`,
			`CREATE TRIGGER trg_external_stage_dependency_events_no_delete BEFORE DELETE ON external_stage_dependency_events
			 BEGIN SELECT RAISE(ABORT,'external stage dependency events are append-only'); END`,

			`CREATE TABLE external_stage_audit_events (
			 id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			 event_kind         TEXT NOT NULL CHECK(event_kind IN ('created','secret_minted','secret_rotated','revoked','accepted','reported')),
			 handoff_row_id     INTEGER NOT NULL REFERENCES external_stage_handoffs(id),
			 operation_event_id INTEGER REFERENCES external_stage_operation_events(id),
			 report_event_id    INTEGER REFERENCES external_stage_report_events(id),
			 api_key_id         INTEGER,
			 credential_epoch   INTEGER NOT NULL CHECK(credential_epoch>=0),
			 sequence           INTEGER CHECK(sequence>0),
			 outcome            TEXT NOT NULL CHECK(outcome IN ('committed','duplicate')),
			 server_received_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 CHECK((event_kind='reported' AND report_event_id IS NOT NULL AND operation_event_id IS NULL AND sequence IS NOT NULL) OR
			       (event_kind<>'reported' AND operation_event_id IS NOT NULL AND report_event_id IS NULL))
			)`,
			`CREATE INDEX idx_external_stage_audit_handoff ON external_stage_audit_events(handoff_row_id,id DESC)`,
			`CREATE UNIQUE INDEX idx_external_stage_audit_operation
			 ON external_stage_audit_events(operation_event_id) WHERE operation_event_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_external_stage_audit_report
			 ON external_stage_audit_events(report_event_id) WHERE report_event_id IS NOT NULL`,
			`CREATE TABLE external_stage_setup_events (
			 id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			 event_kind         TEXT NOT NULL CHECK(event_kind IN ('registration_created','registration_revoked','prerequisites_sealed')),
			 delivery_id        INTEGER NOT NULL REFERENCES deliveries(id),
			 project_id         INTEGER NOT NULL REFERENCES projects(id),
			 registration_id    INTEGER,
			 attempt_id         INTEGER,
			 stage_key          TEXT CHECK(stage_key IN ('specification','implementation','qa','deployment','verification')),
			 execution_number   INTEGER CHECK(execution_number>0),
			 authority_epoch    INTEGER CHECK(authority_epoch>0),
			 actor_user_id      INTEGER NOT NULL REFERENCES users(id),
			 actor_principal_kind TEXT NOT NULL CHECK(actor_principal_kind IN ('session','api_key')),
			 actor_session_id   TEXT,
			 actor_api_key_id   INTEGER REFERENCES api_keys(id),
			 request_digest     BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 idempotency_digest BLOB NOT NULL CHECK(typeof(idempotency_digest)='blob' AND length(idempotency_digest)=32),
			 server_received_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 CHECK((event_kind IN ('registration_created','registration_revoked') AND registration_id IS NOT NULL AND
			        attempt_id IS NULL AND stage_key IS NULL AND execution_number IS NULL AND authority_epoch IS NULL) OR
			       (event_kind='prerequisites_sealed' AND registration_id IS NULL AND attempt_id IS NOT NULL AND
			        stage_key IS NOT NULL AND execution_number IS NOT NULL AND authority_epoch IS NOT NULL)),
			 CHECK((actor_principal_kind='session' AND actor_session_id IS NOT NULL AND actor_api_key_id IS NULL) OR
			       (actor_principal_kind='api_key' AND actor_session_id IS NULL AND actor_api_key_id IS NOT NULL)),
			 FOREIGN KEY(delivery_id,registration_id) REFERENCES external_stage_reporter_registrations(delivery_id,id),
			 FOREIGN KEY(delivery_id,attempt_id) REFERENCES delivery_attempts(delivery_id,id)
			)`,
			`CREATE UNIQUE INDEX idx_external_stage_setup_registration
			 ON external_stage_setup_events(registration_id,event_kind) WHERE registration_id IS NOT NULL`,
			`CREATE UNIQUE INDEX idx_external_stage_setup_prerequisites
			 ON external_stage_setup_events(attempt_id,stage_key,execution_number,authority_epoch,event_kind)
			 WHERE event_kind='prerequisites_sealed'`,
			`CREATE UNIQUE INDEX idx_external_stage_setup_idempotency
			 ON external_stage_setup_events(actor_user_id,actor_principal_kind,
			  COALESCE(actor_session_id,''),COALESCE(actor_api_key_id,0),event_kind,idempotency_digest)`,
			`CREATE TRIGGER trg_external_stage_setup_insert_guard
			 BEFORE INSERT ON external_stage_setup_events
			 WHEN NEW.server_received_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  NOT EXISTS(SELECT 1 FROM external_stage_user_roles actor WHERE actor.id=NEW.actor_user_id AND actor.status='active'
			   AND actor.effective_role<>'external'
			   AND (actor.effective_role IN ('admin','super_admin') OR
			        EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=actor.id
			         AND membership.project_id=NEW.project_id AND membership.access_level='editor') OR
			        (actor.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			         WHERE membership.user_id=actor.id AND membership.project_id=NEW.project_id)))
			   AND ((NEW.actor_principal_kind='session' AND EXISTS(SELECT 1 FROM sessions session
			         WHERE session.credential_id=NEW.actor_session_id
			          AND COALESCE(session.acting_as_user_id,session.user_id)=actor.id
			          AND julianday(session.expires_at)>julianday('now'))) OR
			        (NEW.actor_principal_kind='api_key' AND EXISTS(SELECT 1 FROM api_keys api_key
			         WHERE api_key.id=NEW.actor_api_key_id AND api_key.user_id=actor.id AND api_key.disabled_at IS NULL
			          AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
			          AND (api_key.scopes='*' OR (','||replace(api_key.scopes,' ','')||',') LIKE '%,agent-controls:write,%'))))) OR
			  NOT ((NEW.event_kind='registration_created' AND EXISTS(SELECT 1 FROM external_stage_reporter_registrations registration
			    WHERE registration.id=NEW.registration_id AND registration.delivery_id=NEW.delivery_id
			     AND registration.project_id=NEW.project_id AND registration.revoked_at IS NULL
			     AND NEW.server_received_at>=registration.created_at
			     AND julianday(NEW.server_received_at)-julianday(registration.created_at)<=1.0/86400.0)) OR
			   (NEW.event_kind='registration_revoked' AND EXISTS(SELECT 1 FROM external_stage_reporter_registrations registration
			    WHERE registration.id=NEW.registration_id AND registration.delivery_id=NEW.delivery_id
			     AND registration.project_id=NEW.project_id AND registration.revoked_at IS NULL)) OR
			   (NEW.event_kind='prerequisites_sealed' AND EXISTS(SELECT 1 FROM external_stage_prerequisite_sets prerequisite_set
			    JOIN deliveries delivery ON delivery.id=prerequisite_set.delivery_id
			    JOIN issues issue ON issue.id=delivery.issue_id AND issue.project_id=NEW.project_id AND issue.deleted_at IS NULL
			    WHERE prerequisite_set.delivery_id=NEW.delivery_id AND prerequisite_set.attempt_id=NEW.attempt_id
			     AND prerequisite_set.stage_key=NEW.stage_key AND prerequisite_set.execution_number=NEW.execution_number
			     AND prerequisite_set.authority_epoch=NEW.authority_epoch AND prerequisite_set.sealed_at IS NULL
			     AND prerequisite_set.declared_count=(SELECT COUNT(*) FROM external_stage_prerequisites prerequisite
			      WHERE prerequisite.attempt_id=NEW.attempt_id AND prerequisite.stage_key=NEW.stage_key
			       AND prerequisite.execution_number=NEW.execution_number AND prerequisite.authority_epoch=NEW.authority_epoch))))
			 BEGIN SELECT RAISE(ABORT,'external stage setup lacks current operator authority'); END`,
			`CREATE TRIGGER trg_external_stage_setup_no_update BEFORE UPDATE ON external_stage_setup_events
			 BEGIN SELECT RAISE(ABORT,'external stage setup audit is append-only'); END`,
			`CREATE TRIGGER trg_external_stage_setup_no_delete BEFORE DELETE ON external_stage_setup_events
			 BEGIN SELECT RAISE(ABORT,'external stage setup audit is append-only'); END`,
			`CREATE TRIGGER trg_external_stage_registration_revoke_causal_guard
			 BEFORE UPDATE OF revoked_at ON external_stage_reporter_registrations
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_setup_events setup
			  WHERE setup.event_kind='registration_revoked' AND setup.registration_id=OLD.id
			   AND setup.delivery_id=OLD.delivery_id AND setup.project_id=OLD.project_id
			   AND NEW.revoked_at=setup.server_received_at)
			 BEGIN SELECT RAISE(ABORT,'registration revoke lacks setup audit'); END`,
			`CREATE TRIGGER trg_external_stage_prerequisite_seal_causal_guard
			 BEFORE UPDATE OF sealed_at ON external_stage_prerequisite_sets
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_setup_events setup
			  WHERE setup.event_kind='prerequisites_sealed' AND setup.delivery_id=OLD.delivery_id
			   AND setup.attempt_id=OLD.attempt_id AND setup.stage_key=OLD.stage_key
			   AND setup.execution_number=OLD.execution_number AND setup.authority_epoch=OLD.authority_epoch
			   AND NEW.sealed_at=setup.server_received_at)
			 BEGIN SELECT RAISE(ABORT,'prerequisite seal lacks setup audit'); END`,

			`CREATE TRIGGER trg_external_stage_operation_insert_guard
			 BEFORE INSERT ON external_stage_operation_events
			 WHEN NEW.server_received_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  NOT EXISTS(SELECT 1 FROM external_stage_handoffs handoff
			   JOIN issues issue ON issue.id=handoff.root_issue_id AND issue.project_id=handoff.project_id AND issue.deleted_at IS NULL
			   JOIN projects project ON project.id=handoff.project_id AND project.status IN ('active','frozen')
			   JOIN external_stage_user_roles actor ON actor.id=NEW.actor_user_id AND actor.status='active'
			   WHERE handoff.id=NEW.handoff_row_id AND
			    ((NEW.operation_kind IN ('created','secret_minted','secret_rotated','revoked') AND actor.effective_role<>'external' AND
			      (actor.effective_role IN ('admin','super_admin') OR
			       EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=NEW.actor_user_id
			        AND membership.project_id=handoff.project_id AND membership.access_level='editor') OR
			       (actor.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			        WHERE membership.user_id=NEW.actor_user_id AND membership.project_id=handoff.project_id))) AND
			      ((NEW.actor_principal_kind='session' AND EXISTS(SELECT 1 FROM sessions session
			        WHERE session.credential_id=NEW.actor_session_id
			         AND COALESCE(session.acting_as_user_id,session.user_id)=NEW.actor_user_id
			         AND julianday(session.expires_at)>julianday('now'))) OR
			       (NEW.actor_principal_kind='api_key' AND EXISTS(SELECT 1 FROM api_keys api_key
			        WHERE api_key.id=NEW.actor_api_key_id AND api_key.user_id=NEW.actor_user_id
			         AND api_key.disabled_at IS NULL
			         AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
			         AND (api_key.scopes='*' OR (','||replace(api_key.scopes,' ','')||',') LIKE '%,agent-controls:write,%')))))
			     OR (NEW.operation_kind='accepted' AND NEW.actor_principal_kind='api_key' AND EXISTS(SELECT 1 FROM api_keys api_key
			       WHERE api_key.id=NEW.actor_api_key_id AND api_key.user_id=NEW.actor_user_id
			        AND api_key.disabled_at IS NULL
			        AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))))) AND
			    ((NEW.operation_kind='created' AND NEW.credential_epoch=0 AND handoff.credential_epoch=0 AND
			       NEW.sequence IS NULL AND handoff.secret_digest IS NULL AND handoff.lifecycle_state='issued' AND
			       handoff.last_sequence=0 AND handoff.accepted_at IS NULL AND handoff.terminal_at IS NULL AND
			       handoff.revoked_at IS NULL AND NEW.server_received_at>=handoff.created_at AND
			       julianday(NEW.server_received_at)-julianday(handoff.created_at)<=1.0/86400.0) OR
			     (NEW.operation_kind='secret_minted' AND NEW.credential_epoch=1 AND handoff.credential_epoch=0 AND
			       handoff.secret_digest IS NULL AND handoff.revoked_at IS NULL AND handoff.terminal_at IS NULL) OR
			     (NEW.operation_kind='secret_rotated' AND NEW.credential_epoch=handoff.credential_epoch+1 AND
			       handoff.secret_digest IS NOT NULL AND handoff.revoked_at IS NULL AND handoff.terminal_at IS NULL) OR
			     (NEW.operation_kind='revoked' AND NEW.credential_epoch=handoff.credential_epoch AND
			       handoff.revoked_at IS NULL) OR
			     (NEW.operation_kind='accepted' AND NEW.actor_principal_kind='api_key' AND
			       NEW.actor_api_key_id=handoff.api_key_id AND NEW.credential_epoch=handoff.credential_epoch AND
			       handoff.credential_epoch>0 AND handoff.secret_digest IS NOT NULL AND handoff.lifecycle_state='issued' AND
			       handoff.revoked_at IS NULL AND julianday(handoff.expires_at)>julianday('now') AND
			       actor.effective_role<>'external' AND
			       (actor.effective_role IN ('admin','super_admin') OR
			        EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=actor.id
			         AND membership.project_id=handoff.project_id AND membership.access_level IN ('viewer','editor')) OR
			        (actor.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			         WHERE membership.user_id=actor.id AND membership.project_id=handoff.project_id))) AND
			       NEW.sequence=handoff.last_sequence+1 AND EXISTS(SELECT 1 FROM external_stage_reporter_registrations registration
			        WHERE registration.id=handoff.reporter_registration_id AND registration.api_key_id=handoff.api_key_id
			         AND registration.user_id=NEW.actor_user_id AND registration.project_id=handoff.project_id
			         AND registration.revoked_at IS NULL))))
			 BEGIN SELECT RAISE(ABORT,'external stage operation lacks a current exact binding'); END`,

			`CREATE TRIGGER trg_external_stage_report_insert_guard
			 BEFORE INSERT ON external_stage_report_events
			 WHEN NEW.heartbeat<>0 OR NEW.server_received_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  NOT EXISTS(SELECT 1 FROM external_stage_handoffs handoff
			   JOIN external_stage_reporter_registrations registration
			    ON registration.id=handoff.reporter_registration_id AND registration.delivery_id=handoff.delivery_id
			    AND registration.api_key_id=handoff.api_key_id AND registration.reporter_id=handoff.reporter_id
			    AND registration.reporter_class=handoff.reporter_class AND registration.reporter_role=handoff.reporter_role
			    AND registration.dependency_key IS handoff.dependency_key AND registration.revoked_at IS NULL
			   JOIN api_keys api_key ON api_key.id=handoff.api_key_id AND api_key.user_id=registration.user_id
			    AND api_key.disabled_at IS NULL
			    AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
			   JOIN external_stage_user_roles user ON user.id=registration.user_id AND user.status='active'
			   JOIN issues issue ON issue.id=handoff.root_issue_id AND issue.project_id=handoff.project_id AND issue.deleted_at IS NULL
			   JOIN projects project ON project.id=handoff.project_id AND project.status IN ('active','frozen')
			   JOIN delivery_attempts attempt ON attempt.id=handoff.attempt_id AND attempt.delivery_id=handoff.delivery_id
			    AND attempt.attempt_number=(SELECT MAX(current_attempt.attempt_number) FROM delivery_attempts current_attempt
			     WHERE current_attempt.delivery_id=handoff.delivery_id)
			    AND attempt.attempt_number=handoff.attempt_number AND attempt.plan_revision=handoff.plan_revision
			   JOIN delivery_stage_latest latest ON latest.delivery_id=handoff.delivery_id AND latest.attempt_id=handoff.attempt_id
			    AND latest.stage_key=handoff.stage_key AND latest.execution_number=handoff.execution_number
			    AND latest.execution_start_stage_event_id=handoff.execution_start_stage_event_id
			    AND latest.authority_epoch=handoff.authority_epoch AND latest.authority_stage_event_id=handoff.authority_stage_event_id
			   WHERE handoff.id=NEW.handoff_row_id AND NEW.actor_api_key_id=handoff.api_key_id
			    AND handoff.credential_epoch=NEW.credential_epoch
			    AND handoff.secret_digest IS NOT NULL AND handoff.revoked_at IS NULL
			    AND julianday(handoff.expires_at)>julianday('now') AND handoff.terminal_at IS NULL
			    AND handoff.lifecycle_state IN ('accepted','active','waiting','blocked')
			    AND NEW.sequence=handoff.last_sequence+1
			    AND (handoff.lifecycle_state='accepted' OR
			     julianday(strftime('%Y-%m-%dT%H:%M:%fZ','now'))-julianday((
			      SELECT MAX(received_at) FROM (
			       SELECT operation.server_received_at AS received_at FROM external_stage_operation_events operation
			        WHERE operation.handoff_row_id=handoff.id AND operation.operation_kind='accepted'
			       UNION ALL SELECT report.server_received_at FROM external_stage_report_events report
			        WHERE report.handoff_row_id=handoff.id
			       UNION ALL SELECT heartbeat.last_received_at FROM external_stage_heartbeat_windows heartbeat
			        WHERE heartbeat.handoff_row_id=handoff.id)))<=120.0/86400.0)
			    AND user.effective_role<>'external'
			    AND (user.effective_role IN ('admin','super_admin') OR
			         EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=user.id
			          AND membership.project_id=handoff.project_id AND membership.access_level IN ('viewer','editor')) OR
			         (user.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			          WHERE membership.user_id=user.id AND membership.project_id=handoff.project_id)))
			    AND ((handoff.reporter_role='owner' AND latest.current_reporter_id=handoff.reporter_id) OR
			         (handoff.reporter_role='dependency' AND EXISTS(SELECT 1 FROM external_stage_prerequisites prerequisite
			          JOIN external_stage_prerequisite_sets prerequisite_set
			           ON prerequisite_set.attempt_id=prerequisite.attempt_id AND prerequisite_set.stage_key=prerequisite.stage_key
			           AND prerequisite_set.execution_number=prerequisite.execution_number
			           AND prerequisite_set.authority_epoch=prerequisite.authority_epoch AND prerequisite_set.sealed_at IS NOT NULL
			          WHERE prerequisite.attempt_id=handoff.attempt_id AND prerequisite.stage_key=handoff.stage_key
			           AND prerequisite.execution_number=handoff.execution_number AND prerequisite.authority_epoch=handoff.authority_epoch
			           AND prerequisite.dependency_key=handoff.dependency_key
			           AND prerequisite.registration_id=handoff.reporter_registration_id)))
			    AND (handoff.reporter_role<>'owner' OR NEW.lifecycle_state<>'succeeded' OR
			         (EXISTS(SELECT 1 FROM external_stage_prerequisite_sets prerequisite_set
			           WHERE prerequisite_set.delivery_id=handoff.delivery_id
			            AND prerequisite_set.attempt_id=handoff.attempt_id AND prerequisite_set.stage_key=handoff.stage_key
			            AND prerequisite_set.execution_number=handoff.execution_number
			            AND prerequisite_set.execution_start_stage_event_id=handoff.execution_start_stage_event_id
			            AND prerequisite_set.authority_epoch=handoff.authority_epoch
			            AND prerequisite_set.authority_stage_event_id=handoff.authority_stage_event_id
			            AND prerequisite_set.sealed_at IS NOT NULL
			            AND prerequisite_set.declared_count=(SELECT COUNT(*) FROM external_stage_prerequisites prerequisite
			             WHERE prerequisite.attempt_id=handoff.attempt_id AND prerequisite.stage_key=handoff.stage_key
			              AND prerequisite.execution_number=handoff.execution_number
			              AND prerequisite.authority_epoch=handoff.authority_epoch))
			          AND NOT EXISTS(SELECT 1 FROM external_stage_prerequisites prerequisite
			           WHERE prerequisite.attempt_id=handoff.attempt_id AND prerequisite.stage_key=handoff.stage_key
			            AND prerequisite.execution_number=handoff.execution_number
			            AND prerequisite.authority_epoch=handoff.authority_epoch
			            AND prerequisite.requirement='required' AND NOT EXISTS(
			             SELECT 1 FROM external_stage_dependency_latest dependency_latest
			             JOIN external_stage_dependency_events dependency_event
			              ON dependency_event.id=dependency_latest.dependency_event_id
			              AND dependency_event.report_event_id=dependency_latest.report_event_id
			              AND dependency_event.handoff_row_id=dependency_latest.handoff_row_id
			              AND dependency_event.stream_sequence=dependency_latest.stream_sequence
			             JOIN external_stage_report_events dependency_report
			              ON dependency_report.id=dependency_event.report_event_id
			              AND dependency_report.sequence=dependency_event.sequence
			              AND dependency_report.lifecycle_state='succeeded'
			             JOIN external_stage_handoffs dependency_handoff
			              ON dependency_handoff.id=dependency_event.handoff_row_id
			              AND dependency_handoff.delivery_id=prerequisite.delivery_id
			              AND dependency_handoff.reporter_registration_id=prerequisite.registration_id
			              AND dependency_handoff.dependency_key=prerequisite.dependency_key
			              AND dependency_handoff.reporter_class='janus' AND dependency_handoff.reporter_role='dependency'
			              AND dependency_handoff.lifecycle_state='succeeded' AND dependency_handoff.terminal_at IS NOT NULL
			             JOIN external_stage_audit_events dependency_audit
			              ON dependency_audit.report_event_id=dependency_report.id
			              AND dependency_audit.event_kind='reported' AND dependency_audit.outcome='committed'
			             JOIN external_stage_janus_evidence dependency_evidence
			              ON dependency_evidence.report_event_id=dependency_report.id
			              AND dependency_evidence.result='satisfied'
			              AND COALESCE(dependency_evidence.authorized,dependency_evidence.credential_ready)=1
			             WHERE dependency_latest.attempt_id=prerequisite.attempt_id
			              AND dependency_latest.stage_key=prerequisite.stage_key
			              AND dependency_latest.execution_number=prerequisite.execution_number
			              AND dependency_latest.authority_epoch=prerequisite.authority_epoch
			              AND dependency_latest.dependency_key=prerequisite.dependency_key
			              AND dependency_latest.registration_id=prerequisite.registration_id
			              AND dependency_latest.lifecycle_state='succeeded'))))
			    AND ((NEW.evidence_kind IS NULL) OR
			         (handoff.reporter_class='pharos' AND handoff.reporter_role='owner' AND
			          ((NEW.evidence_kind='deployment' AND handoff.stage_key='deployment' AND handoff.allow_deployment=1) OR
			           (NEW.evidence_kind='verification' AND handoff.stage_key='verification' AND handoff.allow_verification=1))) OR
			         (handoff.reporter_class='janus' AND handoff.reporter_role='dependency' AND
			          ((NEW.evidence_kind='authorization' AND handoff.allow_authorization=1) OR
			           (NEW.evidence_kind='credential_handoff' AND handoff.allow_credential_handoff=1))))
			    AND (NEW.lifecycle_state NOT IN ('succeeded','failed') OR NEW.evidence_kind IS NOT NULL)
			    AND (handoff.reporter_class<>'janus' OR NEW.lifecycle_state NOT IN ('blocked','succeeded') OR NEW.evidence_kind IS NOT NULL)
			  )
			 BEGIN SELECT RAISE(ABORT,'external stage report lacks a current exact binding'); END`,

			`CREATE TRIGGER trg_external_stage_pharos_evidence_insert_guard
			 BEFORE INSERT ON external_stage_pharos_evidence
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_report_events report
			  JOIN external_stage_handoffs handoff ON handoff.id=report.handoff_row_id
			  WHERE report.id=NEW.report_event_id AND report.evidence_kind=NEW.evidence_kind
			   AND report.observed_at=NEW.observed_at AND report.server_received_at=NEW.server_received_at
			   AND report.heartbeat=0 AND handoff.reporter_class='pharos' AND handoff.reporter_role='owner'
			   AND handoff.workflow_symbol=NEW.workflow_symbol AND handoff.environment_symbol=NEW.environment_symbol
			   AND ((NEW.result='succeeded' AND report.lifecycle_state='succeeded') OR
			        (NEW.result='failed' AND report.lifecycle_state='failed'))
			   AND ((NEW.evidence_kind='deployment' AND handoff.allow_deployment=1) OR
			        (NEW.evidence_kind='verification' AND handoff.allow_verification=1))) OR
			  (NEW.evidence_kind='verification' AND NOT EXISTS(
			   SELECT 1 FROM external_stage_pharos_evidence deployment_evidence
			   JOIN external_stage_report_events deployment_report ON deployment_report.id=deployment_evidence.report_event_id
			   JOIN external_stage_handoffs deployment_handoff ON deployment_handoff.id=deployment_report.handoff_row_id
			   JOIN external_stage_audit_events deployment_audit ON deployment_audit.report_event_id=deployment_report.id
			    AND deployment_audit.event_kind='reported' AND deployment_audit.outcome='committed'
			   JOIN external_stage_owner_events deployment_owner ON deployment_owner.report_event_id=deployment_report.id
			    AND deployment_owner.handoff_row_id=deployment_handoff.id
			   JOIN external_stage_owner_latest deployment_latest ON deployment_latest.owner_event_id=deployment_owner.id
			    AND deployment_latest.report_event_id=deployment_report.id
			   JOIN delivery_stage_latest deployment_canonical_latest
			    ON deployment_canonical_latest.delivery_id=deployment_handoff.delivery_id
			    AND deployment_canonical_latest.attempt_id=deployment_handoff.attempt_id
			    AND deployment_canonical_latest.stage_key='deployment'
			    AND deployment_canonical_latest.execution_number=deployment_handoff.execution_number
			    AND deployment_canonical_latest.authority_epoch=deployment_handoff.authority_epoch
			   JOIN delivery_stage_events deployment_terminal
			    ON deployment_terminal.id=deployment_canonical_latest.semantic_stage_event_id
			    AND deployment_terminal.delivery_id=deployment_handoff.delivery_id
			    AND deployment_terminal.attempt_id=deployment_handoff.attempt_id
			    AND deployment_terminal.stage_key='deployment'
			    AND deployment_terminal.execution_number=deployment_handoff.execution_number
			    AND deployment_terminal.authority_epoch=deployment_handoff.authority_epoch
			    AND deployment_terminal.reporter_id=deployment_handoff.reporter_id
			    AND deployment_terminal.source_sequence=deployment_owner.stream_sequence
			    AND deployment_terminal.semantic_state='succeeded'
			   JOIN external_stage_report_events verification_report ON verification_report.id=NEW.report_event_id
			   JOIN external_stage_handoffs verification_handoff ON verification_handoff.id=verification_report.handoff_row_id
			   JOIN delivery_stage_events verification_start ON verification_start.id=verification_handoff.execution_start_stage_event_id
			    AND verification_start.based_on_stage_event_id=deployment_terminal.id
			   WHERE deployment_handoff.delivery_id=verification_handoff.delivery_id
			    AND deployment_handoff.attempt_id=verification_handoff.attempt_id
			    AND deployment_handoff.stage_key='deployment' AND verification_handoff.stage_key='verification'
			    AND deployment_handoff.lifecycle_state='succeeded' AND deployment_handoff.terminal_at IS NOT NULL
			    AND deployment_evidence.evidence_kind='deployment' AND deployment_evidence.result='succeeded'
			    AND deployment_evidence.environment_symbol=NEW.environment_symbol
			    AND deployment_evidence.artifact_version=NEW.artifact_version
			    AND deployment_evidence.artifact_digest=NEW.artifact_digest
			    AND deployment_evidence.commit_digest=NEW.commit_digest
			    AND julianday(NEW.observed_at)>julianday(deployment_evidence.server_received_at)
			    AND julianday(NEW.server_received_at)>julianday(deployment_evidence.server_received_at)))
			 BEGIN SELECT RAISE(ABORT,'invalid external stage Pharos evidence'); END`,
			`CREATE TRIGGER trg_external_stage_janus_evidence_insert_guard
			 BEFORE INSERT ON external_stage_janus_evidence
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_report_events report
			  JOIN external_stage_handoffs handoff ON handoff.id=report.handoff_row_id
			  WHERE report.id=NEW.report_event_id AND report.evidence_kind=NEW.evidence_kind
			   AND report.observed_at=NEW.observed_at AND report.server_received_at=NEW.server_received_at
			   AND report.heartbeat=0 AND handoff.reporter_class='janus' AND handoff.reporter_role='dependency'
			   AND ((NEW.result='satisfied' AND report.lifecycle_state='succeeded') OR
			        (NEW.result='blocked' AND report.lifecycle_state='blocked'))
			   AND ((NEW.evidence_kind='authorization' AND handoff.allow_authorization=1) OR
			        (NEW.evidence_kind='credential_handoff' AND handoff.allow_credential_handoff=1)))
			 BEGIN SELECT RAISE(ABORT,'invalid external stage Janus evidence'); END`,
			`CREATE TRIGGER trg_external_stage_blocker_insert_guard
			 BEFORE INSERT ON external_stage_report_blockers
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_report_events report WHERE report.id=NEW.report_event_id
			  AND NEW.ordinal<report.declared_blockers AND report.heartbeat=0)
			 BEGIN SELECT RAISE(ABORT,'invalid external stage blocker'); END`,

			`CREATE TRIGGER trg_external_stage_audit_insert_guard
			 BEFORE INSERT ON external_stage_audit_events
			 WHEN NEW.server_received_at<>COALESCE(
			   (SELECT operation.server_received_at FROM external_stage_operation_events operation WHERE operation.id=NEW.operation_event_id),
			   (SELECT report.server_received_at FROM external_stage_report_events report WHERE report.id=NEW.report_event_id)) OR
			  NOT ((NEW.event_kind='reported' AND NEW.outcome='committed' AND EXISTS(
			    SELECT 1 FROM external_stage_report_events report
			    JOIN external_stage_handoffs handoff ON handoff.id=report.handoff_row_id
			    WHERE report.id=NEW.report_event_id AND report.handoff_row_id=NEW.handoff_row_id
			     AND handoff.api_key_id=NEW.api_key_id AND report.actor_api_key_id=handoff.api_key_id
			     AND report.credential_epoch=NEW.credential_epoch
			     AND report.sequence=NEW.sequence AND report.declared_blockers=(SELECT COUNT(*) FROM external_stage_report_blockers blocker
			      WHERE blocker.report_event_id=report.id)
			     AND ((report.evidence_kind IS NULL AND NOT EXISTS(SELECT 1 FROM external_stage_pharos_evidence pharos
			           WHERE pharos.report_event_id=report.id) AND NOT EXISTS(SELECT 1 FROM external_stage_janus_evidence janus
			           WHERE janus.report_event_id=report.id)) OR
			          (handoff.reporter_class='pharos' AND EXISTS(SELECT 1 FROM external_stage_pharos_evidence pharos
			           WHERE pharos.report_event_id=report.id AND pharos.evidence_kind=report.evidence_kind)) OR
			          (handoff.reporter_class='janus' AND EXISTS(SELECT 1 FROM external_stage_janus_evidence janus
			           WHERE janus.report_event_id=report.id AND janus.evidence_kind=report.evidence_kind))))) OR
			   (NEW.event_kind<>'reported' AND NEW.outcome='committed' AND EXISTS(
			    SELECT 1 FROM external_stage_operation_events operation
			    WHERE operation.id=NEW.operation_event_id AND operation.handoff_row_id=NEW.handoff_row_id
			     AND operation.credential_epoch=NEW.credential_epoch AND operation.sequence IS NEW.sequence
			     AND ((NEW.event_kind='created' AND operation.operation_kind='created') OR
			          (NEW.event_kind='secret_minted' AND operation.operation_kind='secret_minted') OR
			          (NEW.event_kind='secret_rotated' AND operation.operation_kind='secret_rotated') OR
			          (NEW.event_kind='revoked' AND operation.operation_kind='revoked') OR
			          (NEW.event_kind='accepted' AND operation.operation_kind='accepted'))
			     AND NEW.api_key_id IS operation.actor_api_key_id)))
			 BEGIN SELECT RAISE(ABORT,'external stage audit lacks an exact complete fact'); END`,

			`CREATE TRIGGER trg_external_stage_owner_latest_insert_guard
			 BEFORE INSERT ON external_stage_owner_latest
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_owner_events owner_event
			  JOIN external_stage_handoffs handoff ON handoff.id=owner_event.handoff_row_id AND handoff.reporter_role='owner'
			  JOIN external_stage_audit_events audit ON audit.report_event_id=owner_event.report_event_id AND audit.event_kind='reported'
			  WHERE owner_event.id=NEW.owner_event_id AND owner_event.delivery_id=NEW.delivery_id
			   AND owner_event.attempt_id=NEW.attempt_id AND owner_event.stage_key=NEW.stage_key
			   AND owner_event.execution_number=NEW.execution_number AND owner_event.authority_epoch=NEW.authority_epoch
			   AND owner_event.handoff_row_id=NEW.handoff_row_id AND owner_event.report_event_id=NEW.report_event_id
			   AND owner_event.sequence=NEW.sequence AND owner_event.stream_sequence=NEW.stream_sequence
			   AND owner_event.lifecycle_state=NEW.lifecycle_state
			   AND owner_event.server_received_at=NEW.updated_at)
			 BEGIN SELECT RAISE(ABORT,'owner latest lacks exact owner event'); END`,
			`CREATE TRIGGER trg_external_stage_owner_latest_update_guard
			 BEFORE UPDATE ON external_stage_owner_latest
			 WHEN NEW.delivery_id<>OLD.delivery_id OR NEW.attempt_id<>OLD.attempt_id OR NEW.stage_key<>OLD.stage_key OR
			  NEW.execution_number<>OLD.execution_number OR NEW.authority_epoch<>OLD.authority_epoch OR
			  NEW.stream_sequence<=OLD.stream_sequence OR NOT EXISTS(SELECT 1 FROM external_stage_owner_events owner_event
			   JOIN external_stage_handoffs handoff ON handoff.id=owner_event.handoff_row_id AND handoff.reporter_role='owner'
			   JOIN external_stage_audit_events audit ON audit.report_event_id=owner_event.report_event_id AND audit.event_kind='reported'
			   WHERE owner_event.id=NEW.owner_event_id AND owner_event.delivery_id=NEW.delivery_id
			    AND owner_event.attempt_id=NEW.attempt_id AND owner_event.stage_key=NEW.stage_key
			    AND owner_event.execution_number=NEW.execution_number AND owner_event.authority_epoch=NEW.authority_epoch
			    AND owner_event.handoff_row_id=NEW.handoff_row_id AND owner_event.report_event_id=NEW.report_event_id
			    AND owner_event.sequence=NEW.sequence AND owner_event.stream_sequence=NEW.stream_sequence
			    AND owner_event.lifecycle_state=NEW.lifecycle_state
			    AND owner_event.server_received_at=NEW.updated_at)
			 BEGIN SELECT RAISE(ABORT,'invalid owner latest advance'); END`,
			`CREATE TRIGGER trg_external_stage_owner_latest_no_delete BEFORE DELETE ON external_stage_owner_latest
			 BEGIN SELECT RAISE(ABORT,'external stage owner latest is durable'); END`,
			`CREATE TRIGGER trg_external_stage_dependency_latest_insert_guard
			 BEFORE INSERT ON external_stage_dependency_latest
			 WHEN NOT EXISTS(SELECT 1 FROM external_stage_dependency_events dependency_event
			  JOIN external_stage_handoffs handoff ON handoff.id=dependency_event.handoff_row_id AND handoff.reporter_role='dependency'
			  JOIN external_stage_audit_events audit ON audit.report_event_id=dependency_event.report_event_id AND audit.event_kind='reported'
			  WHERE dependency_event.id=NEW.dependency_event_id AND dependency_event.delivery_id=NEW.delivery_id
			   AND dependency_event.attempt_id=NEW.attempt_id AND dependency_event.stage_key=NEW.stage_key
			   AND dependency_event.execution_number=NEW.execution_number AND dependency_event.authority_epoch=NEW.authority_epoch
			   AND dependency_event.dependency_key=NEW.dependency_key AND dependency_event.registration_id=NEW.registration_id
			   AND dependency_event.credential_epoch=NEW.credential_epoch AND dependency_event.handoff_row_id=NEW.handoff_row_id
			   AND dependency_event.report_event_id=NEW.report_event_id AND dependency_event.sequence=NEW.sequence
			   AND dependency_event.stream_sequence=NEW.stream_sequence
			   AND dependency_event.lifecycle_state=NEW.lifecycle_state AND dependency_event.server_received_at=NEW.updated_at)
			 BEGIN SELECT RAISE(ABORT,'dependency latest lacks exact dependency event'); END`,
			`CREATE TRIGGER trg_external_stage_dependency_latest_update_guard
			 BEFORE UPDATE ON external_stage_dependency_latest
			 WHEN NEW.delivery_id<>OLD.delivery_id OR NEW.attempt_id<>OLD.attempt_id OR NEW.stage_key<>OLD.stage_key OR
			  NEW.execution_number<>OLD.execution_number OR NEW.authority_epoch<>OLD.authority_epoch OR
			  NEW.dependency_key<>OLD.dependency_key OR NEW.registration_id<>OLD.registration_id OR
			  OLD.lifecycle_state='succeeded' OR NEW.stream_sequence<=OLD.stream_sequence OR
			  NOT EXISTS(SELECT 1 FROM external_stage_dependency_events dependency_event
			   JOIN external_stage_handoffs handoff ON handoff.id=dependency_event.handoff_row_id AND handoff.reporter_role='dependency'
			   JOIN external_stage_audit_events audit ON audit.report_event_id=dependency_event.report_event_id AND audit.event_kind='reported'
			   WHERE dependency_event.id=NEW.dependency_event_id AND dependency_event.delivery_id=NEW.delivery_id
			    AND dependency_event.attempt_id=NEW.attempt_id AND dependency_event.stage_key=NEW.stage_key
			    AND dependency_event.execution_number=NEW.execution_number AND dependency_event.authority_epoch=NEW.authority_epoch
			    AND dependency_event.dependency_key=NEW.dependency_key AND dependency_event.registration_id=NEW.registration_id
			    AND dependency_event.credential_epoch=NEW.credential_epoch AND dependency_event.handoff_row_id=NEW.handoff_row_id
			    AND dependency_event.report_event_id=NEW.report_event_id AND dependency_event.sequence=NEW.sequence
			    AND dependency_event.stream_sequence=NEW.stream_sequence
			    AND dependency_event.lifecycle_state=NEW.lifecycle_state AND dependency_event.server_received_at=NEW.updated_at)
			 BEGIN SELECT RAISE(ABORT,'invalid dependency latest advance'); END`,
			`CREATE TRIGGER trg_external_stage_dependency_latest_no_delete BEFORE DELETE ON external_stage_dependency_latest
			 BEGIN SELECT RAISE(ABORT,'external stage dependency latest is durable'); END`,

			`CREATE TRIGGER trg_external_stage_handoff_causal_update_guard
			 BEFORE UPDATE ON external_stage_handoffs
			 WHEN NOT (
			  (NEW.credential_epoch=OLD.credential_epoch+1 AND NEW.secret_digest IS NOT OLD.secret_digest AND
			   NEW.lifecycle_state=OLD.lifecycle_state AND NEW.last_sequence=OLD.last_sequence AND
			   NEW.accepted_at IS OLD.accepted_at AND NEW.terminal_at IS OLD.terminal_at AND NEW.revoked_at IS OLD.revoked_at AND
			   EXISTS(SELECT 1 FROM external_stage_operation_events operation
			    JOIN external_stage_audit_events audit ON audit.operation_event_id=operation.id
			    WHERE operation.handoff_row_id=OLD.id AND operation.credential_epoch=NEW.credential_epoch
			     AND operation.operation_kind=CASE WHEN OLD.credential_epoch=0 THEN 'secret_minted' ELSE 'secret_rotated' END
			     AND audit.event_kind=CASE WHEN OLD.credential_epoch=0 THEN 'secret_minted' ELSE 'secret_rotated' END
			     AND audit.credential_epoch=NEW.credential_epoch AND audit.outcome='committed')) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   NEW.lifecycle_state=OLD.lifecycle_state AND NEW.last_sequence=OLD.last_sequence AND
			   NEW.accepted_at IS OLD.accepted_at AND NEW.terminal_at IS OLD.terminal_at AND OLD.revoked_at IS NULL AND
			   EXISTS(SELECT 1 FROM external_stage_operation_events operation
			    JOIN external_stage_audit_events audit ON audit.operation_event_id=operation.id
			    WHERE operation.handoff_row_id=OLD.id AND operation.operation_kind='revoked'
			     AND operation.credential_epoch=OLD.credential_epoch AND NEW.revoked_at=operation.server_received_at
			     AND audit.event_kind='revoked' AND audit.credential_epoch=OLD.credential_epoch AND audit.outcome='committed')) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   OLD.lifecycle_state='issued' AND NEW.lifecycle_state='accepted' AND NEW.last_sequence=OLD.last_sequence+1 AND
			   OLD.accepted_at IS NULL AND NEW.terminal_at IS NULL AND OLD.revoked_at IS NULL AND NEW.revoked_at IS NULL AND
			   EXISTS(SELECT 1 FROM external_stage_operation_events operation
			    JOIN external_stage_audit_events audit ON audit.operation_event_id=operation.id
			    WHERE operation.handoff_row_id=OLD.id AND operation.operation_kind='accepted'
			     AND operation.sequence=NEW.last_sequence AND operation.credential_epoch=OLD.credential_epoch
			     AND NEW.accepted_at=operation.server_received_at AND audit.event_kind='accepted'
			     AND audit.sequence=NEW.last_sequence AND audit.outcome='committed')) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   NEW.lifecycle_state=OLD.lifecycle_state AND NEW.last_sequence=OLD.last_sequence+1 AND
			   NEW.accepted_at IS OLD.accepted_at AND NEW.terminal_at IS OLD.terminal_at AND NEW.revoked_at IS OLD.revoked_at AND
			   EXISTS(SELECT 1 FROM external_stage_heartbeat_windows window
			    WHERE window.handoff_row_id=OLD.id AND window.credential_epoch=OLD.credential_epoch
			     AND window.last_sequence=NEW.last_sequence AND window.lifecycle_state=OLD.lifecycle_state)) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   NEW.last_sequence=OLD.last_sequence+1 AND NEW.accepted_at IS OLD.accepted_at AND NEW.revoked_at IS OLD.revoked_at AND
			   ((OLD.lifecycle_state='accepted' AND NEW.lifecycle_state IN ('active','waiting','blocked')) OR
			    (OLD.lifecycle_state IN ('active','waiting','blocked') AND NEW.lifecycle_state IN ('active','waiting','blocked','succeeded','failed'))) AND
			   ((NEW.lifecycle_state IN ('succeeded','failed') AND NEW.terminal_at IS NOT NULL) OR
			    (NEW.lifecycle_state IN ('active','waiting','blocked') AND NEW.terminal_at IS NULL)) AND
			   EXISTS(SELECT 1 FROM external_stage_report_events report
			    JOIN external_stage_audit_events audit ON audit.report_event_id=report.id AND audit.event_kind='reported'
			    WHERE report.handoff_row_id=OLD.id AND report.sequence=NEW.last_sequence
			     AND report.credential_epoch=OLD.credential_epoch AND report.lifecycle_state=NEW.lifecycle_state
			     AND ((NEW.lifecycle_state IN ('succeeded','failed') AND NEW.terminal_at=report.server_received_at) OR
			          (NEW.lifecycle_state IN ('active','waiting','blocked') AND NEW.terminal_at IS NULL))
			     AND audit.sequence=report.sequence AND audit.credential_epoch=report.credential_epoch AND audit.outcome='committed'
			     AND ((OLD.reporter_role='owner' AND EXISTS(SELECT 1 FROM external_stage_owner_latest latest
			          WHERE latest.handoff_row_id=OLD.id AND latest.report_event_id=report.id AND latest.sequence=report.sequence)) OR
			          (OLD.reporter_role='dependency' AND EXISTS(SELECT 1 FROM external_stage_dependency_latest latest
			          WHERE latest.handoff_row_id=OLD.id AND latest.report_event_id=report.id AND latest.sequence=report.sequence
			           AND latest.credential_epoch=OLD.credential_epoch)))) )
			 )
			 BEGIN SELECT RAISE(ABORT,'external stage handoff update lacks an exact causal fact'); END`,
			`CREATE TRIGGER trg_external_stage_audit_no_update BEFORE UPDATE ON external_stage_audit_events
			 BEGIN SELECT RAISE(ABORT,'external stage audit is append-only'); END`,
			`CREATE TRIGGER trg_external_stage_audit_no_delete BEFORE DELETE ON external_stage_audit_events
			 BEGIN SELECT RAISE(ABORT,'external stage audit is append-only'); END`,
		}},

		// M149 / PAI-810: principal-attributed audit for the additive internal
		// owner-activation route. This remains separate from the immutable M148
		// setup table so released v5.11.0 databases upgrade additively.
		{149, []string{
			`CREATE TABLE external_stage_owner_activation_events (
			 id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			 delivery_id          INTEGER NOT NULL,
			 project_id           INTEGER NOT NULL REFERENCES projects(id),
			 registration_id      INTEGER NOT NULL,
			 attempt_id           INTEGER NOT NULL,
			 stage_key            TEXT NOT NULL CHECK(stage_key IN ('deployment','verification')),
			 execution_number     INTEGER NOT NULL CHECK(execution_number>0),
			 authority_epoch      INTEGER NOT NULL CHECK(authority_epoch>0),
			 reporter_id          INTEGER NOT NULL,
			 actor_user_id        INTEGER NOT NULL REFERENCES users(id),
			 actor_principal_kind TEXT NOT NULL CHECK(actor_principal_kind IN ('session','api_key')),
			 actor_session_id     TEXT,
			 actor_api_key_id     INTEGER REFERENCES api_keys(id),
			 request_digest       BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 idempotency_digest   BLOB NOT NULL CHECK(typeof(idempotency_digest)='blob' AND length(idempotency_digest)=32),
			 server_received_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 CHECK((actor_principal_kind='session' AND actor_session_id IS NOT NULL AND actor_api_key_id IS NULL) OR
			       (actor_principal_kind='api_key' AND actor_session_id IS NULL AND actor_api_key_id IS NOT NULL)),
			 FOREIGN KEY(delivery_id,registration_id) REFERENCES external_stage_reporter_registrations(delivery_id,id),
			 FOREIGN KEY(delivery_id,attempt_id) REFERENCES delivery_attempts(delivery_id,id),
			 FOREIGN KEY(delivery_id,reporter_id) REFERENCES delivery_reporters(delivery_id,id)
			)`,
			`CREATE UNIQUE INDEX idx_external_stage_owner_activation_target
			 ON external_stage_owner_activation_events(attempt_id,stage_key,execution_number,authority_epoch)`,
			`CREATE UNIQUE INDEX idx_external_stage_owner_activation_idempotency
			 ON external_stage_owner_activation_events(actor_user_id,actor_principal_kind,
			  COALESCE(actor_session_id,''),COALESCE(actor_api_key_id,0),delivery_id,idempotency_digest)`,
			`CREATE TRIGGER trg_external_stage_owner_activation_insert_guard
			 BEFORE INSERT ON external_stage_owner_activation_events
			 WHEN NEW.server_received_at<>strftime('%Y-%m-%dT%H:%M:%fZ','now') OR
			  NOT EXISTS(SELECT 1 FROM external_stage_user_roles actor WHERE actor.id=NEW.actor_user_id
			   AND actor.status='active' AND actor.effective_role<>'external'
			   AND (actor.effective_role IN ('admin','super_admin') OR
			        EXISTS(SELECT 1 FROM project_members membership WHERE membership.user_id=actor.id
			         AND membership.project_id=NEW.project_id AND membership.access_level='editor') OR
			        (actor.effective_role='member' AND NOT EXISTS(SELECT 1 FROM project_members membership
			         WHERE membership.user_id=actor.id AND membership.project_id=NEW.project_id)))
			   AND ((NEW.actor_principal_kind='session' AND EXISTS(SELECT 1 FROM sessions session
			         WHERE session.credential_id=NEW.actor_session_id
			          AND COALESCE(session.acting_as_user_id,session.user_id)=actor.id
			          AND julianday(session.expires_at)>julianday('now'))) OR
			        (NEW.actor_principal_kind='api_key' AND EXISTS(SELECT 1 FROM api_keys api_key
			         WHERE api_key.id=NEW.actor_api_key_id AND api_key.user_id=actor.id
			          AND api_key.disabled_at IS NULL
			          AND (api_key.expires_at IS NULL OR julianday(api_key.expires_at)>julianday('now'))
			          AND (api_key.scopes='*' OR (','||replace(api_key.scopes,' ','')||',') LIKE '%,agent-controls:write,%'))))) OR
			  NOT EXISTS(SELECT 1 FROM external_stage_reporter_registrations registration
			   JOIN external_stage_setup_events setup ON setup.registration_id=registration.id
			    AND setup.delivery_id=registration.delivery_id AND setup.event_kind='registration_created'
			   JOIN delivery_attempts attempt ON attempt.id=NEW.attempt_id AND attempt.delivery_id=NEW.delivery_id
			   JOIN deliveries delivery ON delivery.id=NEW.delivery_id
			   JOIN issues issue ON issue.id=delivery.issue_id AND issue.project_id=NEW.project_id AND issue.deleted_at IS NULL
			   JOIN delivery_stage_latest latest ON latest.delivery_id=NEW.delivery_id AND latest.attempt_id=NEW.attempt_id
			    AND latest.stage_key=NEW.stage_key AND latest.execution_number=NEW.execution_number
			    AND latest.authority_epoch=NEW.authority_epoch AND latest.current_reporter_id=NEW.reporter_id
			   JOIN delivery_stage_events authority ON authority.id=latest.authority_stage_event_id
			    AND authority.attempt_id=NEW.attempt_id AND authority.stage_key=NEW.stage_key
			    AND authority.execution_number=NEW.execution_number AND authority.authority_epoch=NEW.authority_epoch
			    AND authority.reporter_id=NEW.reporter_id AND authority.event_type='handoff'
			    AND authority.reason_code='external_owner_activation'
			   WHERE registration.id=NEW.registration_id AND registration.delivery_id=NEW.delivery_id
			    AND registration.project_id=NEW.project_id AND registration.reporter_id=NEW.reporter_id
			    AND registration.reporter_class='pharos' AND registration.reporter_role='owner'
			    AND registration.revoked_at IS NULL)
			 BEGIN SELECT RAISE(ABORT,'external owner activation lacks current operator and handoff proof'); END`,
			`CREATE TRIGGER trg_external_stage_owner_activation_no_update
			 BEFORE UPDATE ON external_stage_owner_activation_events
			 BEGIN SELECT RAISE(ABORT,'external owner activation audit is append-only'); END`,
			`CREATE TRIGGER trg_external_stage_owner_activation_no_delete
			 BEFORE DELETE ON external_stage_owner_activation_events
			 BEGIN SELECT RAISE(ABORT,'external owner activation audit is append-only'); END`,
		}},

		// M150 / PAI-810: source-free implementation evidence for the local
		// report-back runner. The runner sends only a domain-separated SHA-256
		// binding for the exact tested worktree; source, paths, diffs, commands,
		// output, and credentials never enter the wire or database.
		{150, []string{
			`ALTER TABLE agent_runs ADD COLUMN implementation_result_digest TEXT NOT NULL DEFAULT ''
			 CHECK(implementation_result_digest='' OR
			  (length(CAST(implementation_result_digest AS BLOB))=64 AND
			   implementation_result_digest NOT GLOB '*[^0-9a-f]*'))`,
			`CREATE TRIGGER trg_agent_runs_implementation_result_digest_guard
			 BEFORE UPDATE OF implementation_result_digest ON agent_runs
			 WHEN NEW.implementation_result_digest<>OLD.implementation_result_digest AND
			  (OLD.delivery_instrumentation_version<>1 OR OLD.implementation_result_digest<>'' OR
			   OLD.status<>'running' OR NEW.status<>'tests_passed' OR
			   length(CAST(NEW.implementation_result_digest AS BLOB))<>64 OR
			   NEW.implementation_result_digest GLOB '*[^0-9a-f]*')
			 BEGIN SELECT RAISE(ABORT,'invalid implementation result digest transition'); END`,
		}},

		// M151 / PAI-817: Untrusted-message security contract for delivered agent messages.
		// Prevents prompt injection by wrapping messages with security framing, enforcing
		// per-receiver allowlists via existing project_agents registry, hop limits, rate
		// limits, size caps, and per-turn bounds. Action-request messages are marked and
		// NEVER delivered as executable; they surface to humans. Message bodies are logged
		// and treated as durable/readable (no secrets allowed).
		{151, []string{
			// agent_message_allowlist: Per-receiver allowlist using existing project_agents
			`CREATE TABLE agent_message_allowlist (
				id                    INTEGER PRIMARY KEY AUTOINCREMENT,
				receiver_agent_id     INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
				sender_agent_id       INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
				created_at            TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(receiver_agent_id, sender_agent_id)
			)`,
			`CREATE INDEX idx_agent_message_allowlist_receiver
				ON agent_message_allowlist(receiver_agent_id)`,
			`CREATE INDEX idx_agent_message_allowlist_sender
				ON agent_message_allowlist(sender_agent_id)`,

			// agent_messages: Message delivery records with security metadata
			// hop_count is end-to-end and system-incremented, not client-supplied
			`CREATE TABLE agent_messages (
				id                INTEGER PRIMARY KEY AUTOINCREMENT,
				from_agent_id     INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
				to_agent_id       INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
				issue_id          INTEGER REFERENCES issues(id) ON DELETE SET NULL,
				parent_message_id INTEGER REFERENCES agent_messages(id) ON DELETE SET NULL,
				hop_count         INTEGER NOT NULL DEFAULT 1 CHECK(hop_count BETWEEN 1 AND 10),
				body              TEXT NOT NULL CHECK(length(CAST(body AS BLOB)) <= 32768),
				is_action_request INTEGER NOT NULL DEFAULT 0 CHECK(is_action_request IN (0,1)),
				delivered         INTEGER NOT NULL DEFAULT 0 CHECK(delivered IN (0,1)),
				held_reason       TEXT NOT NULL DEFAULT '',
				created_at        TEXT NOT NULL DEFAULT (datetime('now')),
				delivered_at      TEXT,
				CHECK(delivered=0 OR delivered_at IS NOT NULL),
				CHECK(delivered=0 OR held_reason=''),
				CHECK(delivered=1 OR delivered_at IS NULL),
				CHECK(is_action_request=0 OR delivered=0),
				CHECK(NOT paimos_contains_secret_like(body))
			)`,
			`CREATE INDEX idx_agent_messages_to
				ON agent_messages(to_agent_id, delivered, created_at)`,
			`CREATE INDEX idx_agent_messages_from
				ON agent_messages(from_agent_id, created_at)`,
			`CREATE INDEX idx_agent_messages_issue
				ON agent_messages(issue_id) WHERE issue_id IS NOT NULL`,
			`CREATE INDEX idx_agent_messages_parent
				ON agent_messages(parent_message_id) WHERE parent_message_id IS NOT NULL`,

			// agent_message_rate_limits: Per-sender rate limiting
			`CREATE TABLE agent_message_rate_limits (
				id                INTEGER PRIMARY KEY AUTOINCREMENT,
				sender_agent_id   INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
				receiver_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
				message_count     INTEGER NOT NULL DEFAULT 0 CHECK(message_count >= 0),
				window_start      TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(sender_agent_id, receiver_agent_id)
			)`,
			`CREATE INDEX idx_agent_message_rate_limits_window
				ON agent_message_rate_limits(window_start)`,
		}},

		// M152 / PAI-815: canonical, project-scoped A2A envelope. M151 remains
		// the security/storage foundation; these additive columns make every
		// row independently addressable and readable without exposing numeric
		// agent IDs as the public contract.
		{152, []string{
			`ALTER TABLE agent_messages ADD COLUMN message_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_messages ADD COLUMN context_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_messages ADD COLUMN task_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_messages ADD COLUMN role TEXT NOT NULL DEFAULT 'agent'`,
			`ALTER TABLE agent_messages ADD COLUMN parts_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(parts_json))`,
			`ALTER TABLE agent_messages ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(metadata_json))`,
			`ALTER TABLE agent_messages ADD COLUMN from_address TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_messages ADD COLUMN to_address TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_messages ADD COLUMN reply_to TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_messages ADD COLUMN thread_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_messages ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`,
			`UPDATE agent_messages SET
				message_id='legacy-'||id,
				context_id=(SELECT p.key FROM project_agents pa JOIN projects p ON p.id=pa.project_id WHERE pa.id=agent_messages.from_agent_id),
				task_id=COALESCE((SELECT p.key||'-'||i.issue_number FROM issues i JOIN projects p ON p.id=i.project_id WHERE i.id=agent_messages.issue_id),''),
				parts_json=json_array(json_object('kind','text','text',body)),
				from_address='paimos:'||(SELECT name FROM project_agents WHERE id=agent_messages.from_agent_id),
				to_address='paimos:'||(SELECT name FROM project_agents WHERE id=agent_messages.to_agent_id),
				reply_to=COALESCE((SELECT message_id FROM agent_messages parent WHERE parent.id=agent_messages.parent_message_id),''),
				thread_id='legacy-'||COALESCE(parent_message_id,id)`,
			`CREATE UNIQUE INDEX idx_agent_messages_message_id ON agent_messages(message_id)`,
			`CREATE INDEX idx_agent_messages_envelope_to ON agent_messages(to_address, id)`,
			`CREATE INDEX idx_agent_messages_thread ON agent_messages(thread_id, id)`,
		}},

		// M153 / PAI-816: durable, monotonic receiver cursors. Acknowledgement
		// belongs to an attributed project agent and one full harness address;
		// read_at records which delivered rows the receiver has durably consumed.
		{153, []string{
			`ALTER TABLE agent_messages ADD COLUMN read_at TEXT`,
			`CREATE TABLE agent_message_cursors (
				project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
				address          TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
				cursor           INTEGER NOT NULL DEFAULT 0 CHECK(cursor >= 0),
				updated_at       TEXT NOT NULL DEFAULT (datetime('now')),
				PRIMARY KEY(project_id, address)
			)`,
			`CREATE INDEX idx_agent_message_cursors_agent ON agent_message_cursors(project_agent_id, address)`,
		}},

		// M154 / PAI-826: durable message-level delivery intent, immutable
		// receiver-owned target versions, and one linked delivery/outbox row.
		// Capability targets are encrypted by the service before insertion; the
		// schema never stores a plaintext vendor session or webhook reference.
		{154, []string{
			`ALTER TABLE agent_messages ADD COLUMN delivery_level TEXT NOT NULL DEFAULT 'simple'
			 CHECK(delivery_level IN ('simple','steer'))`,
			`ALTER TABLE agent_messages ADD COLUMN delivery_fallback TEXT NOT NULL DEFAULT 'simple'
			 CHECK(delivery_fallback='simple')`,
			`ALTER TABLE agent_messages ADD COLUMN delivery_primary_target_id TEXT`,
			`ALTER TABLE agent_messages ADD COLUMN delivery_fallback_target_id TEXT`,
			`CREATE TABLE agent_message_targets (
			 id                TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 64),
			 instance          TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 address           TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 adapter           TEXT NOT NULL CHECK(adapter IN ('codex','grok_bot_routine')),
			 target_kind       TEXT NOT NULL CHECK(target_kind IN ('codex_thread','https_webhook')),
			 target_ref_cipher BLOB NOT NULL CHECK(typeof(target_ref_cipher)='blob' AND length(target_ref_cipher)>28),
			 maximum_level     TEXT NOT NULL DEFAULT 'simple' CHECK(maximum_level IN ('simple','steer')),
			 role              TEXT NOT NULL DEFAULT 'primary' CHECK(role IN ('primary','simple_fallback')),
			 enabled           INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
			 version           INTEGER NOT NULL CHECK(version>0),
			 created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 UNIQUE(instance,project_id,address,role,version),
			 CHECK((adapter='codex' AND target_kind='codex_thread') OR
			       (adapter='grok_bot_routine' AND target_kind='https_webhook'))
			)`,
			`CREATE UNIQUE INDEX idx_agent_message_targets_enabled_role
			 ON agent_message_targets(instance,project_id,address,role) WHERE enabled=1`,
			`CREATE INDEX idx_agent_message_targets_receiver
			 ON agent_message_targets(instance,project_id,address,enabled)`,
			`CREATE TABLE agent_message_deliveries (
			 delivery_id       TEXT PRIMARY KEY CHECK(length(CAST(delivery_id AS BLOB)) BETWEEN 1 AND 64),
			 message_row_id    INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE CASCADE,
			 instance          TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 primary_target_id TEXT REFERENCES agent_message_targets(id),
			 fallback_target_id TEXT REFERENCES agent_message_targets(id),
			 requested_level   TEXT NOT NULL CHECK(requested_level IN ('simple','steer')),
			 effective_level   TEXT CHECK(effective_level IS NULL OR effective_level IN ('simple','steer')),
			 state             TEXT NOT NULL CHECK(state IN ('pending','leased','retry','blocked','handed_off','dead')),
			 fallback_reason   TEXT NOT NULL DEFAULT '' CHECK(fallback_reason IN ('','idle','unsupported','policy_capped','target_missing','not_steerable')),
			 attempt_count     INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count>=0),
			 next_attempt_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 lease_until       TEXT,
			 last_error_code   TEXT NOT NULL DEFAULT '' CHECK(length(CAST(last_error_code AS BLOB))<=64),
			 handed_off_at     TEXT,
			 created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 CHECK((state='handed_off' AND handed_off_at IS NOT NULL AND effective_level IS NOT NULL) OR
			       (state<>'handed_off' AND handed_off_at IS NULL))
			)`,
			`CREATE INDEX idx_agent_message_deliveries_dispatch
			 ON agent_message_deliveries(instance,state,next_attempt_at,message_row_id)`,
			`CREATE INDEX idx_agent_message_deliveries_target
			 ON agent_message_deliveries(primary_target_id,state,message_row_id)`,
			`CREATE TABLE agent_message_idempotency (
			 instance        TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 sender_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 key_digest      BLOB NOT NULL CHECK(typeof(key_digest)='blob' AND length(key_digest)=32),
			 request_digest  BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 message_row_id  INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE CASCADE,
			 created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 PRIMARY KEY(instance,project_id,sender_agent_id,key_digest)
			)`,
		}},

		// M155 / PAI-827: Claude session targets. The Claude simple adapters
		// (`claude_resume` for `claude -p --resume|--cloud`, `claude_channel`
		// for the opt-in MCP channel push) bind a receiver to a Claude session
		// id. SQLite cannot widen a CHECK constraint in place, so the target
		// table is rebuilt under its own name: agent_message_deliveries keeps
		// referencing agent_message_targets(id), and every existing version,
		// ciphertext, and enabled flag is carried over unchanged. Claude has no
		// steer primitive, so a Claude target's maximum_level is fixed to
		// simple at the schema level.
		{155, []string{
			`PRAGMA foreign_keys=OFF`,
			`CREATE TABLE agent_message_targets_m155 (
			 id                TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 64),
			 instance          TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 address           TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 adapter           TEXT NOT NULL CHECK(adapter IN ('codex','grok_bot_routine','claude_resume','claude_channel')),
			 target_kind       TEXT NOT NULL CHECK(target_kind IN ('codex_thread','https_webhook','claude_session')),
			 target_ref_cipher BLOB NOT NULL CHECK(typeof(target_ref_cipher)='blob' AND length(target_ref_cipher)>28),
			 maximum_level     TEXT NOT NULL DEFAULT 'simple' CHECK(maximum_level IN ('simple','steer')),
			 role              TEXT NOT NULL DEFAULT 'primary' CHECK(role IN ('primary','simple_fallback')),
			 enabled           INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
			 version           INTEGER NOT NULL CHECK(version>0),
			 created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 UNIQUE(instance,project_id,address,role,version),
			 CHECK((adapter='codex' AND target_kind='codex_thread') OR
			       (adapter='grok_bot_routine' AND target_kind='https_webhook') OR
			       (adapter IN ('claude_resume','claude_channel') AND target_kind='claude_session')),
			 CHECK(adapter NOT IN ('claude_resume','claude_channel') OR maximum_level='simple')
			)`,
			`INSERT INTO agent_message_targets_m155
			 (id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version,created_at)
			 SELECT id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version,created_at
			   FROM agent_message_targets`,
			`DROP TABLE agent_message_targets`,
			`ALTER TABLE agent_message_targets_m155 RENAME TO agent_message_targets`,
			`CREATE UNIQUE INDEX idx_agent_message_targets_enabled_role
			 ON agent_message_targets(instance,project_id,address,role) WHERE enabled=1`,
			`CREATE INDEX idx_agent_message_targets_receiver
			 ON agent_message_targets(instance,project_id,address,enabled)`,
			`PRAGMA foreign_keys=ON`,
		}},

		// M156 / PAI-829: harness adapters and target kinds are registry plugin
		// keys, not a closed vendor list. Rebuild the target table again so a
		// separately registered plugin can persist its binding without a core
		// migration. Adapter/kind pairing and maximum-level capability remain
		// fail-closed in harness.ValidateBinding.
		{156, []string{
			`PRAGMA foreign_keys=OFF`,
			`CREATE TABLE agent_message_targets_m156 (
			 id                TEXT PRIMARY KEY CHECK(length(CAST(id AS BLOB)) BETWEEN 1 AND 64),
			 instance          TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 address           TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 adapter           TEXT NOT NULL CHECK(length(CAST(adapter AS BLOB)) BETWEEN 1 AND 64 AND length(adapter)=length(CAST(adapter AS BLOB)) AND adapter GLOB '[a-z]*' AND adapter NOT GLOB '*[^a-z0-9_]*'),
			 target_kind       TEXT NOT NULL CHECK(length(CAST(target_kind AS BLOB)) BETWEEN 1 AND 64 AND length(target_kind)=length(CAST(target_kind AS BLOB)) AND target_kind GLOB '[a-z]*' AND target_kind NOT GLOB '*[^a-z0-9_]*'),
			 target_ref_cipher BLOB NOT NULL CHECK(typeof(target_ref_cipher)='blob' AND length(target_ref_cipher)>28),
			 maximum_level     TEXT NOT NULL DEFAULT 'simple' CHECK(maximum_level IN ('simple','steer')),
			 role              TEXT NOT NULL DEFAULT 'primary' CHECK(role IN ('primary','simple_fallback')),
			 enabled           INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
			 version           INTEGER NOT NULL CHECK(version>0),
			 created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 UNIQUE(instance,project_id,address,role,version)
			)`,
			`INSERT INTO agent_message_targets_m156
			 (id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version,created_at)
			 SELECT id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version,created_at
			   FROM agent_message_targets`,
			`DROP TABLE agent_message_targets`,
			`ALTER TABLE agent_message_targets_m156 RENAME TO agent_message_targets`,
			`CREATE UNIQUE INDEX idx_agent_message_targets_enabled_role
			 ON agent_message_targets(instance,project_id,address,role) WHERE enabled=1`,
			`CREATE INDEX idx_agent_message_targets_receiver
			 ON agent_message_targets(instance,project_id,address,enabled)`,
			`PRAGMA foreign_keys=ON`,
		}},

		// M157 / PAI-828: a server-side https_webhook target may carry the
		// receiver-owned sender secret its vendor requires in one request header
		// (Grok Bot routine webhooks authenticate with
		// `Authorization: Bearer <sender key>`). The secret is domain-separated
		// secretvault ciphertext in its own nullable column: adapters without
		// that capability keep NULL, list/status views expose only a boolean,
		// and listen never discloses it. Existing rows are untouched.
		{157, []string{
			`ALTER TABLE agent_message_targets ADD COLUMN target_secret_cipher BLOB
			 CHECK(target_secret_cipher IS NULL OR (typeof(target_secret_cipher)='blob' AND length(target_secret_cipher)>28))`,
		}},

		// M158 / PAI-812: Paimos remains the work record while optionally
		// linking an issue to one opaque Pharos host-action/request id. The
		// constrained scalar cannot hold a URL, prose, or secret; no trigger or
		// integration call is attached to it.
		{158, []string{
			`ALTER TABLE issues ADD COLUMN pharos_request_id TEXT NOT NULL DEFAULT ''
			 CHECK(pharos_request_id='' OR (
			   typeof(pharos_request_id)='text' AND
			   length(CAST(pharos_request_id AS BLOB)) BETWEEN 8 AND 128 AND
			   pharos_request_id NOT GLOB '*[^A-Za-z0-9_-]*' AND
			   paimos_contains_secret_like(CAST(pharos_request_id AS BLOB))=0
			 ))`,
		}},

		// M159 / PAI-840: Codex app-server control-path failures can safely
		// degrade to the exact queue primitive. Widen the durable fallback
		// reason constraint so that truthful handoff result survives restart.
		{159, []string{
			`PRAGMA foreign_keys=OFF`,
			`CREATE TABLE agent_message_deliveries_m159 (
			 delivery_id        TEXT PRIMARY KEY CHECK(length(CAST(delivery_id AS BLOB)) BETWEEN 1 AND 64),
			 message_row_id     INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE CASCADE,
			 instance           TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 primary_target_id  TEXT REFERENCES agent_message_targets(id),
			 fallback_target_id TEXT REFERENCES agent_message_targets(id),
			 requested_level    TEXT NOT NULL CHECK(requested_level IN ('simple','steer')),
			 effective_level    TEXT CHECK(effective_level IS NULL OR effective_level IN ('simple','steer')),
			 state              TEXT NOT NULL CHECK(state IN ('pending','leased','retry','blocked','handed_off','dead')),
			 fallback_reason    TEXT NOT NULL DEFAULT '' CHECK(fallback_reason IN ('','idle','unsupported','policy_capped','target_missing','not_steerable','transport_error')),
			 attempt_count      INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count>=0),
			 next_attempt_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 lease_until        TEXT,
			 last_error_code    TEXT NOT NULL DEFAULT '' CHECK(length(CAST(last_error_code AS BLOB))<=64),
			 handed_off_at      TEXT,
			 created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 CHECK((state='handed_off' AND handed_off_at IS NOT NULL AND effective_level IS NOT NULL) OR
			       (state<>'handed_off' AND handed_off_at IS NULL))
			)`,
			`INSERT INTO agent_message_deliveries_m159
			 (delivery_id,message_row_id,instance,primary_target_id,fallback_target_id,requested_level,effective_level,state,
			  fallback_reason,attempt_count,next_attempt_at,lease_until,last_error_code,handed_off_at,created_at,updated_at)
			 SELECT delivery_id,message_row_id,instance,primary_target_id,fallback_target_id,requested_level,effective_level,state,
			        fallback_reason,attempt_count,next_attempt_at,lease_until,last_error_code,handed_off_at,created_at,updated_at
			   FROM agent_message_deliveries`,
			`DROP TABLE agent_message_deliveries`,
			`ALTER TABLE agent_message_deliveries_m159 RENAME TO agent_message_deliveries`,
			`CREATE INDEX idx_agent_message_deliveries_dispatch
			 ON agent_message_deliveries(instance,state,next_attempt_at,message_row_id)`,
			`CREATE INDEX idx_agent_message_deliveries_target
			 ON agent_message_deliveries(primary_target_id,state,message_row_id)`,
			`PRAGMA foreign_keys=ON`,
		}},

		// M160 / PAI-843: mutation_log is self-referential. SQLite probes the
		// child key for every retained parent deleted by the GDPR sweeper; without
		// this index each probe scans the whole table while holding the WAL writer
		// slot. The partial index contains exactly the rows that can reference a
		// parent and keeps the cold-start retention path bounded.
		{160, []string{
			`CREATE INDEX idx_mutation_log_parent
			 ON mutation_log(parent_log_id) WHERE parent_log_id IS NOT NULL`,
		}},

		// M161 / PAI-848: additive provider-neutral harness-session identity
		// and typed owned-process controls. The private vendor session reference
		// is encrypted in agent_message_targets; only a digest and safe target FK
		// are stored here for replay identity and redacted attribution.
		{161, []string{
			`CREATE TABLE harness_sessions (
			 id                   TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("id") + `),
			 project_id           INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 project_agent_id     INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 agent_name           TEXT NOT NULL CHECK(` + sqlStableKeyCheck("agent_name", 64) + `),
			 harness              TEXT NOT NULL CHECK(` + sqlStableKeyCheck("harness", 64) + ` AND lower(harness)<>'openclaw'),
			 host                 TEXT NOT NULL CHECK(` + sqlStableKeyCheck("host", 128) + `),
			 session_ref_digest   BLOB NOT NULL CHECK(typeof(session_ref_digest)='blob' AND length(session_ref_digest)=32),
			 message_target_id    TEXT REFERENCES agent_message_targets(id),
			 management_mode      TEXT NOT NULL CHECK(management_mode IN ('managed','unmanaged')),
			 role                 TEXT NOT NULL CHECK(role IN ('coordinator','worker')),
			 steer_mode           TEXT NOT NULL CHECK(steer_mode IN ('none','owned','codex_external')),
			 advertised_inbox     INTEGER NOT NULL CHECK(advertised_inbox IN (0,1)),
			 advertised_status    INTEGER NOT NULL CHECK(advertised_status IN (0,1)),
			 advertised_steer     INTEGER NOT NULL CHECK(advertised_steer IN (0,1)),
			 advertised_interrupt INTEGER NOT NULL CHECK(advertised_interrupt IN (0,1)),
			 advertised_stop      INTEGER NOT NULL CHECK(advertised_stop IN (0,1)),
			 phase                TEXT NOT NULL DEFAULT 'starting' CHECK(phase IN ('starting','working','yielded','stopping','stopped')),
			 heartbeat_at         TEXT CHECK(` + sqlNullableControlTimestampCheck("heartbeat_at") + `),
			 yielded_at           TEXT CHECK(` + sqlNullableControlTimestampCheck("yielded_at") + `),
			 yield_sequence       INTEGER NOT NULL DEFAULT 0 CHECK(yield_sequence>=0),
			 revision             INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
			 created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 CHECK((advertised_steer=0 AND steer_mode='none') OR (advertised_steer=1 AND steer_mode<>'none')),
			 CHECK(advertised_steer=0 OR (advertised_inbox=1 AND advertised_status=1)),
			 CHECK(advertised_interrupt=0 OR advertised_status=1),
			 CHECK(advertised_stop=0 OR advertised_status=1),
			 CHECK((management_mode='managed' AND advertised_status=1 AND
			        (advertised_steer=0 OR steer_mode='owned')) OR
			       (management_mode='unmanaged' AND advertised_interrupt=0 AND advertised_stop=0 AND
			        (advertised_steer=0 OR (harness='codex' AND steer_mode='codex_external'))))
			)`,
			`CREATE UNIQUE INDEX idx_harness_sessions_active_address
			 ON harness_sessions(project_id,harness,agent_name) WHERE phase<>'stopped'`,
			`CREATE UNIQUE INDEX idx_harness_sessions_active_identity
			 ON harness_sessions(project_id,harness,host,session_ref_digest) WHERE phase<>'stopped'`,
			`CREATE INDEX idx_harness_sessions_host_phase
			 ON harness_sessions(host,phase,heartbeat_at)`,
			`CREATE TRIGGER trg_harness_sessions_identity_immutable BEFORE UPDATE OF
			 project_id,project_agent_id,agent_name,harness,host,session_ref_digest,management_mode,role,steer_mode,
			 advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop
			 ON harness_sessions BEGIN SELECT RAISE(ABORT,'harness session identity is immutable'); END`,
			`CREATE TRIGGER trg_harness_sessions_agent_insert BEFORE INSERT ON harness_sessions
			 WHEN NOT EXISTS(SELECT 1 FROM project_agents pa WHERE pa.id=NEW.project_agent_id
			  AND pa.project_id=NEW.project_id AND pa.name=NEW.agent_name)
			 BEGIN SELECT RAISE(ABORT,'harness session agent attribution mismatch'); END`,
			`CREATE TRIGGER trg_harness_sessions_target_insert BEFORE INSERT ON harness_sessions
			 WHEN NEW.message_target_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM agent_message_targets t WHERE t.id=NEW.message_target_id AND t.project_id=NEW.project_id
			   AND t.address=lower(NEW.harness)||':'||NEW.agent_name)
			 BEGIN SELECT RAISE(ABORT,'harness session target attribution mismatch'); END`,
			`CREATE TRIGGER trg_harness_sessions_target_update BEFORE UPDATE OF message_target_id ON harness_sessions
			 WHEN NEW.message_target_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM agent_message_targets t WHERE t.id=NEW.message_target_id AND t.project_id=NEW.project_id
			   AND t.address=lower(NEW.harness)||':'||NEW.agent_name)
			 BEGIN SELECT RAISE(ABORT,'harness session target attribution mismatch'); END`,
			`CREATE TABLE harness_session_controls (
			 id                    TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("id") + `),
			 harness_session_id    TEXT NOT NULL REFERENCES harness_sessions(id) ON DELETE CASCADE,
			 sequence              INTEGER NOT NULL CHECK(sequence>0),
			 kind                  TEXT NOT NULL CHECK(kind IN ('interrupt','stop')),
			 state                 TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','claimed','applied','rejected')),
			 reason                TEXT NOT NULL DEFAULT '' CHECK(reason IN ('','applied','not_running','unsupported','ownership_lost','failed')),
			 requested_by_user_id  INTEGER NOT NULL REFERENCES users(id),
			 requested_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("requested_at") + `),
			 claimed_at            TEXT CHECK(` + sqlNullableControlTimestampCheck("claimed_at") + `),
			 completed_at          TEXT CHECK(` + sqlNullableControlTimestampCheck("completed_at") + `),
			 UNIQUE(harness_session_id,sequence),
			 CHECK((state='pending' AND claimed_at IS NULL AND completed_at IS NULL AND reason='') OR
			       (state='claimed' AND claimed_at IS NOT NULL AND completed_at IS NULL AND reason='') OR
			       (state IN ('applied','rejected') AND claimed_at IS NOT NULL AND completed_at IS NOT NULL AND reason<>''))
			)`,
			`CREATE UNIQUE INDEX idx_harness_session_control_active
			 ON harness_session_controls(harness_session_id,kind) WHERE state IN ('pending','claimed')`,
			`CREATE INDEX idx_harness_session_control_drain
			 ON harness_session_controls(harness_session_id,state,sequence)`,
			`CREATE TRIGGER trg_harness_session_control_identity_immutable BEFORE UPDATE OF
			 harness_session_id,sequence,kind,requested_by_user_id,requested_at
			 ON harness_session_controls BEGIN SELECT RAISE(ABORT,'harness session control identity is immutable'); END`,
		}},

		// M162 / PAI-857: SQLite treats NULL values as distinct inside a UNIQUE
		// index, so M96 protected only project-owned knowledge. Give each of the
		// three ownership scopes its actual identity and reject impossible mixed
		// ownership or non-memory user-owned rows at the database boundary.
		{162, []string{
			`DROP INDEX idx_issues_type_slug_project`,
			`CREATE UNIQUE INDEX idx_issues_knowledge_project_identity
			 ON issues(project_id,type,slug)
			 WHERE project_id IS NOT NULL AND slug IS NOT NULL AND deleted_at IS NULL`,
			`CREATE UNIQUE INDEX idx_issues_knowledge_user_identity
			 ON issues(user_id,type,slug)
			 WHERE project_id IS NULL AND user_id IS NOT NULL AND slug IS NOT NULL AND deleted_at IS NULL`,
			`CREATE UNIQUE INDEX idx_issues_knowledge_instance_identity
			 ON issues(type,slug)
			 WHERE project_id IS NULL AND user_id IS NULL AND slug IS NOT NULL AND deleted_at IS NULL`,
			`CREATE TRIGGER trg_issues_scope_owner_insert BEFORE INSERT ON issues
			 WHEN NEW.project_id IS NOT NULL AND NEW.user_id IS NOT NULL
			 BEGIN SELECT RAISE(ABORT,'issue cannot have both project and user ownership'); END`,
			`CREATE TRIGGER trg_issues_scope_owner_update BEFORE UPDATE OF project_id,user_id ON issues
			 WHEN NEW.project_id IS NOT NULL AND NEW.user_id IS NOT NULL
			 BEGIN SELECT RAISE(ABORT,'issue cannot have both project and user ownership'); END`,
			`CREATE TRIGGER trg_issues_user_type_insert BEFORE INSERT ON issues
			 WHEN NEW.user_id IS NOT NULL AND NEW.type<>'memory'
			 BEGIN SELECT RAISE(ABORT,'user-owned issue type must be memory'); END`,
			`CREATE TRIGGER trg_issues_user_type_update BEFORE UPDATE OF project_id,user_id,type ON issues
			 WHEN NEW.user_id IS NOT NULL AND NEW.type<>'memory'
			 BEGIN SELECT RAISE(ABORT,'user-owned issue type must be memory'); END`,
		}},

		// M163 / PAI-859 + PAI-860: product sessions are their own project-
		// scoped resource, not aliases for attribution, harness-control, agent
		// run, delivery, or intake-session identities. The same migration adds
		// the binary project node-depth contract and unbypassable parent-edge
		// guards. The existing issue_relations(type='parent') row remains the
		// hierarchy SSOT; no legacy issue row or type is rewritten.
		{163, []string{
			`ALTER TABLE projects ADD COLUMN node_depth TEXT NOT NULL DEFAULT 'nested'
			 CHECK(node_depth IN ('1','nested'))`,
			`CREATE TABLE node_label_defaults (
			 issue_type TEXT PRIMARY KEY CHECK(issue_type IN ('epic','task','ticket','cost_unit','release','sprint','memory','runbook','external_system','related_project','guideline')),
			 label TEXT NOT NULL CHECK(length(CAST(label AS BLOB)) BETWEEN 1 AND 64 AND label=trim(label)
			  AND instr(label,char(9))=0 AND instr(label,char(10))=0 AND instr(label,char(13))=0)
			)`,
			`INSERT INTO node_label_defaults(issue_type,label) VALUES
			 ('epic','Epic'),('task','Task'),('ticket','Ticket'),('cost_unit','Cost unit'),
			 ('release','Release'),('sprint','Sprint'),('memory','Memory'),('runbook','Runbook'),
			 ('external_system','External system'),('related_project','Related project'),('guideline','Guideline')`,
			`CREATE TRIGGER trg_node_label_defaults_no_delete BEFORE DELETE ON node_label_defaults
			 BEGIN SELECT RAISE(ABORT,'global node label defaults cannot be deleted'); END`,
			`CREATE TABLE project_node_label_overrides (
			 project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 issue_type TEXT NOT NULL REFERENCES node_label_defaults(issue_type) ON DELETE RESTRICT,
			 label TEXT NOT NULL CHECK(length(CAST(label AS BLOB)) BETWEEN 1 AND 64 AND label=trim(label)
			  AND instr(label,char(9))=0 AND instr(label,char(10))=0 AND instr(label,char(13))=0),
			 PRIMARY KEY(project_id,issue_type)
			)`,
			`CREATE TABLE product_sessions (
			 product_session_id       TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("product_session_id") + `),
			 project_id               INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 target_kind              TEXT NOT NULL CHECK(target_kind IN ('paimos','project_agent')),
			 target_project_agent_id  INTEGER REFERENCES project_agents(id) ON DELETE RESTRICT,
			 node_id                  INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			 title                    TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) BETWEEN 1 AND 240),
			 summary                  TEXT NOT NULL DEFAULT '' CHECK(length(CAST(summary AS BLOB))<=4000),
			 revision                 INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
			 created_by_user_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
			 updated_by_user_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
			 created_at               TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at               TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 CHECK((target_kind='paimos' AND target_project_agent_id IS NULL) OR
			       (target_kind='project_agent' AND target_project_agent_id IS NOT NULL))
			)`,
			`CREATE INDEX idx_product_sessions_project_updated
			 ON product_sessions(project_id,updated_at,product_session_id)`,
			`CREATE INDEX idx_product_sessions_node
			 ON product_sessions(node_id,project_id) WHERE node_id IS NOT NULL`,
			`CREATE INDEX idx_product_sessions_target_agent
			 ON product_sessions(project_id,target_project_agent_id) WHERE target_project_agent_id IS NOT NULL`,
			`CREATE TABLE product_session_events (
			 event_id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			 product_session_id        TEXT NOT NULL REFERENCES product_sessions(product_session_id) ON DELETE CASCADE,
			 event_sequence            INTEGER NOT NULL CHECK(event_sequence>0),
			 operation                 TEXT NOT NULL CHECK(operation IN ('create','attach','reattach','detach')),
			 actor_user_id              INTEGER NOT NULL,
			 before_node_id             INTEGER,
			 after_node_id              INTEGER,
			 before_revision            INTEGER NOT NULL CHECK(before_revision>=0),
			 after_revision             INTEGER NOT NULL CHECK(after_revision=before_revision+1),
			 created_at                 TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(product_session_id,event_sequence)
			)`,
			`CREATE INDEX idx_product_session_events_session
			 ON product_session_events(product_session_id,event_sequence)`,
			`CREATE TRIGGER trg_product_sessions_actor_insert BEFORE INSERT ON product_sessions
			 WHEN NEW.created_by_user_id IS NULL OR NEW.updated_by_user_id IS NULL
			 BEGIN SELECT RAISE(ABORT,'product session actor attribution required'); END`,
			`CREATE TRIGGER trg_product_sessions_agent_insert BEFORE INSERT ON product_sessions
			 WHEN NEW.target_kind='project_agent' AND NOT EXISTS(
			  SELECT 1 FROM project_agents pa
			  WHERE pa.id=NEW.target_project_agent_id AND pa.project_id=NEW.project_id)
			 BEGIN SELECT RAISE(ABORT,'product session agent project mismatch'); END`,
			`CREATE TRIGGER trg_project_agents_product_session_move BEFORE UPDATE OF project_id ON project_agents
			 WHEN NEW.project_id IS NOT OLD.project_id AND EXISTS(
			  SELECT 1 FROM product_sessions ps WHERE ps.target_project_agent_id=OLD.id)
			 BEGIN SELECT RAISE(ABORT,'product session target prevents agent project move'); END`,
			`CREATE TRIGGER trg_product_sessions_node_insert BEFORE INSERT ON product_sessions
			 WHEN NEW.node_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM issues i
			  WHERE i.id=NEW.node_id AND i.project_id=NEW.project_id AND i.deleted_at IS NULL)
			 BEGIN SELECT RAISE(ABORT,'product session node project mismatch'); END`,
			`CREATE TRIGGER trg_product_sessions_node_update BEFORE UPDATE OF node_id ON product_sessions
			 WHEN NEW.node_id IS NOT NULL AND NOT EXISTS(
			  SELECT 1 FROM issues i
			  WHERE i.id=NEW.node_id AND i.project_id=NEW.project_id AND i.deleted_at IS NULL)
			 BEGIN SELECT RAISE(ABORT,'product session node project mismatch'); END`,
			`CREATE TRIGGER trg_product_sessions_mutation_guard BEFORE UPDATE ON product_sessions
			 WHEN NEW.node_id IS OLD.node_id OR NEW.revision<>OLD.revision+1 OR NEW.updated_by_user_id IS NULL
			 BEGIN SELECT RAISE(ABORT,'product session mutation requires attributed node revision'); END`,
			`CREATE TRIGGER trg_product_sessions_identity_immutable BEFORE UPDATE OF
			 product_session_id,project_id,target_kind,target_project_agent_id,title,summary,created_by_user_id,created_at
			 ON product_sessions
			 BEGIN SELECT RAISE(ABORT,'product session identity is immutable'); END`,
			`CREATE TRIGGER trg_product_sessions_no_delete BEFORE DELETE ON product_sessions
			 BEGIN SELECT RAISE(ABORT,'product sessions are immutable resources'); END`,
			`CREATE TRIGGER trg_product_sessions_event_create AFTER INSERT ON product_sessions
			 BEGIN INSERT INTO product_session_events(
			  product_session_id,event_sequence,operation,actor_user_id,before_node_id,after_node_id,before_revision,after_revision)
			 VALUES(NEW.product_session_id,1,'create',NEW.created_by_user_id,NULL,NEW.node_id,0,NEW.revision); END`,
			`CREATE TRIGGER trg_product_sessions_event_update AFTER UPDATE ON product_sessions
			 BEGIN INSERT INTO product_session_events(
			  product_session_id,event_sequence,operation,actor_user_id,before_node_id,after_node_id,before_revision,after_revision)
			 VALUES(NEW.product_session_id,NEW.revision,
			  CASE WHEN OLD.node_id IS NULL THEN 'attach' WHEN NEW.node_id IS NULL THEN 'detach' ELSE 'reattach' END,
			  NEW.updated_by_user_id,OLD.node_id,NEW.node_id,OLD.revision,NEW.revision); END`,
			`CREATE TRIGGER trg_product_session_events_immutable_update BEFORE UPDATE ON product_session_events
			 BEGIN SELECT RAISE(ABORT,'product session events are immutable'); END`,
			`CREATE TRIGGER trg_product_session_events_immutable_delete BEFORE DELETE ON product_session_events
			 BEGIN SELECT RAISE(ABORT,'product session events are immutable'); END`,
			`CREATE TRIGGER trg_issues_attached_session_move BEFORE UPDATE OF project_id ON issues
			 WHEN NEW.project_id IS NOT OLD.project_id AND EXISTS(SELECT 1 FROM product_sessions ps WHERE ps.node_id=OLD.id)
			 BEGIN SELECT RAISE(ABORT,'product session attached node must be detached before move'); END`,
			`CREATE TRIGGER trg_issues_attached_session_soft_delete BEFORE UPDATE OF deleted_at ON issues
			 WHEN NEW.deleted_at IS NOT OLD.deleted_at AND NEW.deleted_at IS NOT NULL
			  AND EXISTS(SELECT 1 FROM product_sessions ps WHERE ps.node_id=OLD.id)
			 BEGIN SELECT RAISE(ABORT,'product session attached node must be detached before delete'); END`,
			`CREATE TRIGGER trg_projects_node_depth_guard BEFORE UPDATE OF node_depth ON projects
			 WHEN NEW.node_depth='1' AND EXISTS(
			  SELECT 1 FROM issue_relations edge
			  JOIN issues child ON child.id=edge.target_id
			  WHERE edge.type='parent' AND child.project_id=NEW.id)
			 BEGIN SELECT RAISE(ABORT,'project depth 1 forbids parent edges'); END`,
			`CREATE TRIGGER trg_issue_parent_guard_insert BEFORE INSERT ON issue_relations
			 WHEN NEW.type='parent' AND (
			  NEW.source_id=NEW.target_id OR
			  NOT EXISTS(
			   SELECT 1 FROM issues parent JOIN issues child
			    ON child.id=NEW.target_id
			   WHERE parent.id=NEW.source_id AND parent.project_id IS NOT NULL
			    AND child.project_id=parent.project_id
			  ) OR
			  EXISTS(
			   SELECT 1 FROM issues child JOIN projects project ON project.id=child.project_id
			   WHERE child.id=NEW.target_id AND project.node_depth='1'
			  ) OR
			  EXISTS(
			   WITH RECURSIVE descendants(issue_id) AS (
			    SELECT NEW.target_id
			    UNION
			    SELECT edge.target_id FROM issue_relations edge
			    JOIN descendants parent ON edge.source_id=parent.issue_id
			    WHERE edge.type='parent'
			   ) SELECT 1 FROM descendants WHERE issue_id=NEW.source_id
			  )
			 ) BEGIN SELECT RAISE(ABORT,'invalid project parent edge'); END`,
			`CREATE TRIGGER trg_issue_parent_guard_update BEFORE UPDATE OF source_id,target_id,type ON issue_relations
			 WHEN NEW.type='parent' AND (
			  NEW.source_id=NEW.target_id OR
			  NOT EXISTS(
			   SELECT 1 FROM issues parent JOIN issues child
			    ON child.id=NEW.target_id
			   WHERE parent.id=NEW.source_id AND parent.project_id IS NOT NULL
			    AND child.project_id=parent.project_id
			  ) OR
			  EXISTS(
			   SELECT 1 FROM issues child JOIN projects project ON project.id=child.project_id
			   WHERE child.id=NEW.target_id AND project.node_depth='1'
			  ) OR
			  EXISTS(
			   WITH RECURSIVE descendants(issue_id) AS (
			    SELECT NEW.target_id
			    UNION
			    SELECT edge.target_id FROM issue_relations edge
			    JOIN descendants parent ON edge.source_id=parent.issue_id
			    WHERE edge.type='parent' AND NOT (
			     edge.source_id=OLD.source_id AND edge.target_id=OLD.target_id AND edge.type=OLD.type)
			   ) SELECT 1 FROM descendants WHERE issue_id=NEW.source_id
			  )
			 ) BEGIN SELECT RAISE(ABORT,'invalid project parent edge'); END`,
		}},
		// M164 / PAI-862: human-origin Paimos 6 utterances share the canonical
		// message ledger without impersonating a registered project agent. The
		// rebuild is atomic on one pinned connection and carries every legacy
		// message plus the M154/M159 delivery and idempotency children unchanged.
		{164, []string{
			`PRAGMA foreign_keys=OFF`,
			`DROP INDEX idx_agent_message_deliveries_dispatch`,
			`DROP INDEX idx_agent_message_deliveries_target`,
			`DROP INDEX idx_agent_messages_to`,
			`DROP INDEX idx_agent_messages_from`,
			`DROP INDEX idx_agent_messages_issue`,
			`DROP INDEX idx_agent_messages_parent`,
			`DROP INDEX idx_agent_messages_message_id`,
			`DROP INDEX idx_agent_messages_envelope_to`,
			`DROP INDEX idx_agent_messages_thread`,
			`ALTER TABLE agent_message_deliveries RENAME TO agent_message_deliveries_old164`,
			`ALTER TABLE agent_message_idempotency RENAME TO agent_message_idempotency_old164`,
			`ALTER TABLE agent_messages RENAME TO agent_messages_old164`,
			`CREATE TABLE agent_messages (
			 id                          INTEGER PRIMARY KEY AUTOINCREMENT,
			 from_agent_id               INTEGER REFERENCES project_agents(id) ON DELETE CASCADE,
			 to_agent_id                 INTEGER REFERENCES project_agents(id) ON DELETE CASCADE,
			 issue_id                    INTEGER REFERENCES issues(id) ON DELETE SET NULL,
			 parent_message_id           INTEGER REFERENCES agent_messages(id) ON DELETE SET NULL,
			 hop_count                   INTEGER NOT NULL DEFAULT 1 CHECK(hop_count BETWEEN 1 AND 10),
			 body                        TEXT NOT NULL CHECK(length(CAST(body AS BLOB)) <= 32768),
			 is_action_request           INTEGER NOT NULL DEFAULT 0 CHECK(is_action_request IN (0,1)),
			 delivered                   INTEGER NOT NULL DEFAULT 0 CHECK(delivered IN (0,1)),
			 held_reason                 TEXT NOT NULL DEFAULT '',
			 created_at                  TEXT NOT NULL DEFAULT (datetime('now')),
			 delivered_at                TEXT,
			 message_id                  TEXT NOT NULL DEFAULT '',
			 context_id                  TEXT NOT NULL DEFAULT '',
			 task_id                     TEXT NOT NULL DEFAULT '',
			 role                        TEXT NOT NULL DEFAULT 'agent' CHECK(role IN ('agent','human')),
			 parts_json                  TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(parts_json)),
			 metadata_json               TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(metadata_json)),
			 from_address                TEXT NOT NULL DEFAULT '',
			 to_address                  TEXT NOT NULL DEFAULT '',
			 reply_to                    TEXT NOT NULL DEFAULT '',
			 thread_id                   TEXT NOT NULL DEFAULT '',
			 session_id                  TEXT NOT NULL DEFAULT '',
			 read_at                     TEXT,
			 delivery_level              TEXT NOT NULL DEFAULT 'simple' CHECK(delivery_level IN ('simple','steer')),
			 delivery_fallback           TEXT NOT NULL DEFAULT 'simple' CHECK(delivery_fallback='simple'),
			 delivery_primary_target_id  TEXT,
			 delivery_fallback_target_id TEXT,
			 from_user_id                INTEGER REFERENCES users(id) ON DELETE RESTRICT,
			 product_session_id          TEXT REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
			 CHECK(delivered=0 OR delivered_at IS NOT NULL),
			 CHECK(delivered=0 OR held_reason=''),
			 CHECK(delivered=1 OR delivered_at IS NULL),
			 CHECK(is_action_request=0 OR delivered=0),
			 CHECK(NOT paimos_contains_secret_like(body)),
			 CHECK((role='agent' AND from_agent_id IS NOT NULL AND from_user_id IS NULL) OR
			       (role='human' AND from_agent_id IS NULL AND from_user_id IS NOT NULL AND product_session_id IS NOT NULL)),
			 CHECK(role<>'human' OR
			       (to_agent_id IS NOT NULL AND to_address<>'paimos' AND delivery_primary_target_id IS NOT NULL) OR
			       (to_agent_id IS NULL AND to_address='paimos' AND delivery_primary_target_id IS NULL AND delivery_fallback_target_id IS NULL))
			)`,
			`INSERT INTO agent_messages(
			 id,from_agent_id,to_agent_id,issue_id,parent_message_id,hop_count,body,is_action_request,delivered,
			 held_reason,created_at,delivered_at,message_id,context_id,task_id,role,parts_json,metadata_json,
			 from_address,to_address,reply_to,thread_id,session_id,read_at,delivery_level,delivery_fallback,
			 delivery_primary_target_id,delivery_fallback_target_id,from_user_id,product_session_id)
			 SELECT id,from_agent_id,to_agent_id,issue_id,parent_message_id,hop_count,body,is_action_request,delivered,
			 held_reason,created_at,delivered_at,message_id,context_id,task_id,role,parts_json,metadata_json,
			 from_address,to_address,reply_to,thread_id,session_id,read_at,delivery_level,delivery_fallback,
			 delivery_primary_target_id,delivery_fallback_target_id,NULL,NULL FROM agent_messages_old164`,
			`CREATE UNIQUE INDEX idx_agent_messages_message_id ON agent_messages(message_id)`,
			`CREATE INDEX idx_agent_messages_to ON agent_messages(to_agent_id,delivered,created_at)`,
			`CREATE INDEX idx_agent_messages_from ON agent_messages(from_agent_id,created_at)`,
			`CREATE INDEX idx_agent_messages_user ON agent_messages(from_user_id,created_at) WHERE from_user_id IS NOT NULL`,
			`CREATE INDEX idx_agent_messages_product_session ON agent_messages(product_session_id,id) WHERE product_session_id IS NOT NULL`,
			`CREATE INDEX idx_agent_messages_issue ON agent_messages(issue_id) WHERE issue_id IS NOT NULL`,
			`CREATE INDEX idx_agent_messages_parent ON agent_messages(parent_message_id) WHERE parent_message_id IS NOT NULL`,
			`CREATE INDEX idx_agent_messages_envelope_to ON agent_messages(to_address,id)`,
			`CREATE INDEX idx_agent_messages_thread ON agent_messages(thread_id,id)`,
			`CREATE TABLE agent_message_deliveries (
			 delivery_id        TEXT PRIMARY KEY CHECK(length(CAST(delivery_id AS BLOB)) BETWEEN 1 AND 64),
			 message_row_id     INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE CASCADE,
			 instance           TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 primary_target_id  TEXT REFERENCES agent_message_targets(id),
			 fallback_target_id TEXT REFERENCES agent_message_targets(id),
			 requested_level    TEXT NOT NULL CHECK(requested_level IN ('simple','steer')),
			 effective_level    TEXT CHECK(effective_level IS NULL OR effective_level IN ('simple','steer')),
			 state              TEXT NOT NULL CHECK(state IN ('pending','leased','retry','blocked','handed_off','dead')),
			 fallback_reason    TEXT NOT NULL DEFAULT '' CHECK(fallback_reason IN ('','idle','unsupported','policy_capped','target_missing','not_steerable','transport_error')),
			 attempt_count      INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count>=0),
			 next_attempt_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 lease_until        TEXT,
			 last_error_code    TEXT NOT NULL DEFAULT '' CHECK(length(CAST(last_error_code AS BLOB))<=64),
			 handed_off_at      TEXT,
			 created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 CHECK((state='handed_off' AND handed_off_at IS NOT NULL AND effective_level IS NOT NULL) OR
			       (state<>'handed_off' AND handed_off_at IS NULL))
			)`,
			`INSERT INTO agent_message_deliveries SELECT * FROM agent_message_deliveries_old164`,
			`CREATE INDEX idx_agent_message_deliveries_dispatch ON agent_message_deliveries(instance,state,next_attempt_at,message_row_id)`,
			`CREATE INDEX idx_agent_message_deliveries_target ON agent_message_deliveries(primary_target_id,state,message_row_id)`,
			`CREATE TABLE agent_message_idempotency (
			 instance        TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 sender_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 key_digest      BLOB NOT NULL CHECK(typeof(key_digest)='blob' AND length(key_digest)=32),
			 request_digest  BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 message_row_id  INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE CASCADE,
			 created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			 PRIMARY KEY(instance,project_id,sender_agent_id,key_digest)
			)`,
			`INSERT INTO agent_message_idempotency SELECT * FROM agent_message_idempotency_old164`,
			`DROP TABLE agent_message_deliveries_old164`,
			`DROP TABLE agent_message_idempotency_old164`,
			`DROP TABLE agent_messages_old164`,
			`CREATE TRIGGER trg_agent_messages_human_shape_insert BEFORE INSERT ON agent_messages
			 WHEN NEW.role='human' AND NOT EXISTS(
			  SELECT 1 FROM product_sessions ps
			  LEFT JOIN project_agents receiver ON receiver.id=NEW.to_agent_id
			  WHERE ps.product_session_id=NEW.product_session_id AND (
			   (NEW.to_agent_id IS NULL AND ps.target_kind='paimos' AND NEW.to_address='paimos') OR
			   (NEW.to_agent_id IS NOT NULL AND ps.target_kind='project_agent' AND
			    ps.target_project_agent_id=NEW.to_agent_id AND receiver.project_id=ps.project_id AND
			    EXISTS(SELECT 1 FROM agent_message_targets target
			     WHERE target.id=NEW.delivery_primary_target_id AND target.project_id=ps.project_id AND
			      target.enabled=1 AND target.address=NEW.to_address AND
			      target.address LIKE '%:'||receiver.name))))
			 BEGIN SELECT RAISE(ABORT,'human message session destination mismatch'); END`,
			`CREATE TRIGGER trg_agent_messages_human_shape_update BEFORE UPDATE OF
			 role,from_agent_id,from_user_id,to_agent_id,to_address,product_session_id,
			 delivery_primary_target_id,delivery_fallback_target_id ON agent_messages
			 WHEN NEW.role='human' AND NOT EXISTS(
			  SELECT 1 FROM product_sessions ps
			  LEFT JOIN project_agents receiver ON receiver.id=NEW.to_agent_id
			  WHERE ps.product_session_id=NEW.product_session_id AND (
			   (NEW.to_agent_id IS NULL AND ps.target_kind='paimos' AND NEW.to_address='paimos') OR
			   (NEW.to_agent_id IS NOT NULL AND ps.target_kind='project_agent' AND
			    ps.target_project_agent_id=NEW.to_agent_id AND receiver.project_id=ps.project_id AND
			    EXISTS(SELECT 1 FROM agent_message_targets target
			     WHERE target.id=NEW.delivery_primary_target_id AND target.project_id=ps.project_id AND
			      target.enabled=1 AND target.address=NEW.to_address AND
			      target.address LIKE '%:'||receiver.name))))
			 BEGIN SELECT RAISE(ABORT,'human message session destination mismatch'); END`,
			`CREATE TABLE paimos_conversation_bindings (
			 project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			 product_session_id TEXT NOT NULL UNIQUE REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
			 created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 PRIMARY KEY(project_id,user_id)
			)`,
			`CREATE TRIGGER trg_paimos_conversation_binding_insert BEFORE INSERT ON paimos_conversation_bindings
			 WHEN NOT EXISTS(SELECT 1 FROM product_sessions ps WHERE ps.product_session_id=NEW.product_session_id
			  AND ps.project_id=NEW.project_id AND ps.target_kind='paimos')
			 BEGIN SELECT RAISE(ABORT,'Paimos conversation binding mismatch'); END`,
			`CREATE TRIGGER trg_paimos_conversation_binding_immutable BEFORE UPDATE ON paimos_conversation_bindings
			 BEGIN SELECT RAISE(ABORT,'Paimos conversation binding is immutable'); END`,
			`CREATE TABLE session_utterance_receipts (
			 instance                 TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 project_id               INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 user_id                  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			 utterance_id             TEXT NOT NULL CHECK(length(CAST(utterance_id AS BLOB))=36 AND
			  substr(utterance_id,1,4)='utt_' AND substr(utterance_id,5) NOT GLOB '*[^0-9a-f]*'),
			 request_digest           BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
			 message_row_id           INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE CASCADE,
			 product_session_id       TEXT NOT NULL REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
			 product_session_revision INTEGER NOT NULL CHECK(product_session_revision>0),
			 delivery_id              TEXT,
			 created_at               TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 PRIMARY KEY(instance,project_id,user_id,utterance_id)
			)`,
			`CREATE TRIGGER trg_session_utterance_receipts_shape BEFORE INSERT ON session_utterance_receipts
			 WHEN NOT EXISTS(
			  SELECT 1 FROM agent_messages message
			  JOIN product_sessions session ON session.product_session_id=NEW.product_session_id
			  WHERE message.id=NEW.message_row_id AND message.role='human' AND
			   message.from_user_id=NEW.user_id AND message.product_session_id=NEW.product_session_id AND
			   session.project_id=NEW.project_id AND session.revision=NEW.product_session_revision AND (
			    (message.to_agent_id IS NULL AND message.to_address='paimos' AND NEW.delivery_id IS NULL) OR
			    (message.to_agent_id IS NOT NULL AND NEW.delivery_id IS NOT NULL AND
			     EXISTS(SELECT 1 FROM agent_message_deliveries delivery
			      JOIN agent_message_targets target ON target.id=delivery.primary_target_id
			      WHERE delivery.delivery_id=NEW.delivery_id AND delivery.message_row_id=message.id AND
			       delivery.instance=NEW.instance AND target.instance=NEW.instance AND
			       delivery.primary_target_id=message.delivery_primary_target_id))))
			 BEGIN SELECT RAISE(ABORT,'session utterance receipt mismatch'); END`,
			`CREATE TRIGGER trg_session_utterance_receipts_no_update BEFORE UPDATE ON session_utterance_receipts
			 BEGIN SELECT RAISE(ABORT,'session utterance receipts are immutable'); END`,
			`CREATE TRIGGER trg_session_utterance_receipts_no_delete BEFORE DELETE ON session_utterance_receipts
			 BEGIN SELECT RAISE(ABORT,'session utterance receipts are immutable'); END`,
			`PRAGMA foreign_keys=ON`,
		}},
		// M165 / PAI-865: one explicit, nullable instance orchestrator pin.
		// M164 remains reserved for PAI-862. The singleton starts unset and this
		// migration deliberately performs no inference from existing projects or
		// agents. Historical event snapshots are scalars so later identity changes
		// cannot rewrite the audit record.
		{165, []string{
			`CREATE TABLE instance_orchestrator (
			 singleton_id       INTEGER PRIMARY KEY CHECK(singleton_id=1),
			 project_agent_id   INTEGER REFERENCES project_agents(id) ON DELETE RESTRICT,
			 display_label      TEXT CHECK(display_label IS NULL OR (
			  typeof(display_label)='text' AND
			  length(CAST(display_label AS BLOB)) BETWEEN 1 AND 64 AND
			  display_label=trim(display_label) AND
			  instr(display_label,char(0))=0 AND
			  display_label NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')
			 )),
			 revision            INTEGER NOT NULL DEFAULT 0 CHECK(revision>=0),
			 updated_by_user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
			 updated_at           TEXT CHECK(` + sqlNullableControlTimestampCheck("updated_at") + `),
			 CHECK((project_agent_id IS NULL)=(display_label IS NULL)),
			 CHECK((revision=0 AND project_agent_id IS NULL AND display_label IS NULL AND
			  updated_by_user_id IS NULL AND updated_at IS NULL) OR
			  (revision>0 AND updated_at IS NOT NULL))
			)`,
			`INSERT INTO instance_orchestrator(singleton_id) VALUES(1)`,
			`CREATE TABLE instance_orchestrator_events (
			 event_id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			 operation                TEXT NOT NULL CHECK(operation IN ('set','clear','reassign','display_label_update','target_rename')),
			 actor_user_id            INTEGER NOT NULL CHECK(actor_user_id>0),
			 request_id               TEXT NOT NULL CHECK(
			  typeof(request_id)='text' AND length(CAST(request_id AS BLOB)) BETWEEN 1 AND 128 AND
			  instr(request_id,char(0))=0 AND
			  request_id NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')
			 ),
			 before_revision          INTEGER NOT NULL CHECK(before_revision>=0),
			 after_revision           INTEGER NOT NULL UNIQUE CHECK(after_revision=before_revision+1),
			 before_project_agent_id  INTEGER,
			 before_project_id        INTEGER,
			 before_key               TEXT,
			 before_display_label     TEXT,
			 after_project_agent_id   INTEGER,
			 after_project_id         INTEGER,
			 after_key                TEXT,
			 after_display_label      TEXT,
			 created_at               TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 CHECK((before_project_agent_id IS NULL AND before_project_id IS NULL AND before_key IS NULL AND before_display_label IS NULL) OR
			       (before_project_agent_id>0 AND before_project_id>0 AND
			        typeof(before_key)='text' AND length(CAST(before_key AS BLOB)) BETWEEN 1 AND 32 AND
			        before_key GLOB '[a-z]*' AND before_key NOT GLOB '*[^a-z0-9_-]*' AND
			        typeof(before_display_label)='text' AND length(CAST(before_display_label AS BLOB)) BETWEEN 1 AND 64 AND
			        before_display_label=trim(before_display_label) AND instr(before_display_label,char(0))=0 AND
			        before_display_label NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*'))),
			 CHECK((after_project_agent_id IS NULL AND after_project_id IS NULL AND after_key IS NULL AND after_display_label IS NULL) OR
			       (after_project_agent_id>0 AND after_project_id>0 AND
			        typeof(after_key)='text' AND length(CAST(after_key AS BLOB)) BETWEEN 1 AND 32 AND
			        after_key GLOB '[a-z]*' AND after_key NOT GLOB '*[^a-z0-9_-]*' AND
			        typeof(after_display_label)='text' AND length(CAST(after_display_label AS BLOB)) BETWEEN 1 AND 64 AND
			        after_display_label=trim(after_display_label) AND instr(after_display_label,char(0))=0 AND
			        after_display_label NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*'))),
			 CHECK(
			  (operation='set' AND before_project_agent_id IS NULL AND after_project_agent_id IS NOT NULL) OR
			  (operation='clear' AND before_project_agent_id IS NOT NULL AND after_project_agent_id IS NULL) OR
			  (operation='reassign' AND before_project_agent_id IS NOT NULL AND after_project_agent_id IS NOT NULL AND before_project_agent_id<>after_project_agent_id) OR
			  (operation='display_label_update' AND before_project_agent_id=after_project_agent_id AND before_project_id=after_project_id AND before_key=after_key AND before_display_label<>after_display_label) OR
			  (operation='target_rename' AND before_project_agent_id=after_project_agent_id AND before_project_id=after_project_id AND before_key<>after_key AND before_display_label=after_display_label)
			 )
			)`,
			`CREATE INDEX idx_instance_orchestrator_events_created
			 ON instance_orchestrator_events(created_at DESC,event_id DESC)`,
			`CREATE TRIGGER trg_instance_orchestrator_events_sequence BEFORE INSERT ON instance_orchestrator_events
			 WHEN NEW.before_revision<>COALESCE((SELECT MAX(after_revision) FROM instance_orchestrator_events),0)
			   OR NEW.after_revision<>(SELECT revision FROM instance_orchestrator WHERE singleton_id=1)
			 BEGIN SELECT RAISE(ABORT,'instance orchestrator audit revision mismatch'); END`,
			`CREATE TRIGGER trg_instance_orchestrator_events_no_update BEFORE UPDATE ON instance_orchestrator_events
			 BEGIN SELECT RAISE(ABORT,'instance orchestrator audit is append-only'); END`,
			`CREATE TRIGGER trg_instance_orchestrator_events_no_delete BEFORE DELETE ON instance_orchestrator_events
			 BEGIN SELECT RAISE(ABORT,'instance orchestrator audit is append-only'); END`,
			`CREATE TRIGGER trg_instance_orchestrator_project_no_soft_delete BEFORE UPDATE OF status ON projects
			 WHEN NEW.status='deleted' AND OLD.status<>'deleted' AND EXISTS(
			  SELECT 1 FROM instance_orchestrator io JOIN project_agents pa ON pa.id=io.project_agent_id
			  WHERE io.singleton_id=1 AND pa.project_id=OLD.id
			 ) BEGIN SELECT RAISE(ABORT,'assigned orchestrator project'); END`,
			`CREATE TRIGGER trg_instance_orchestrator_no_active_harness_rename BEFORE UPDATE OF name ON project_agents
			 WHEN NEW.name<>OLD.name
			  AND EXISTS(SELECT 1 FROM instance_orchestrator io WHERE io.singleton_id=1 AND io.project_agent_id=OLD.id)
			  AND EXISTS(SELECT 1 FROM harness_sessions hs WHERE hs.project_agent_id=OLD.id AND hs.phase<>'stopped')
				 BEGIN SELECT RAISE(ABORT,'assigned orchestrator has active harness'); END`,
		}},

		// M166 / PAI-866: nullable per-user Paimos 6 command-palette
		// shortcut. NULL inherits the instance app_settings override and then
		// the safe default. M164 deliberately remains reserved/absent.
		{166, []string{
			`ALTER TABLE users ADD COLUMN command_palette_shortcut TEXT
			 CHECK(command_palette_shortcut IS NULL OR (
			  typeof(command_palette_shortcut)='text' AND
			  length(CAST(command_palette_shortcut AS BLOB)) BETWEEN 3 AND 128 AND
			  command_palette_shortcut=trim(command_palette_shortcut) AND
			  instr(command_palette_shortcut,char(0))=0 AND
			  command_palette_shortcut NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')
			 ))`,
		}},

		// M167 / PAI-863: reviewed structured-knowledge Compact, durable
		// entries, canonical links, proposals, and atomic promotion evidence.
		{167, migration167StructuredKnowledgeSteps(structuredKnowledgeShortBodyLimitBytes)},

		// M168 / PAI-870: a public harness-session UUID and caller-supplied
		// agent attribution never authorize worker mutations. Store only the
		// generation-bound digest of the agentd-minted worker lease.
		{168, []string{
			`ALTER TABLE harness_sessions ADD COLUMN worker_lease_digest BLOB
			 CHECK(worker_lease_digest IS NULL OR (typeof(worker_lease_digest)='blob' AND length(worker_lease_digest)=32))`,
			`UPDATE harness_session_controls
			 SET state='rejected',reason='ownership_lost',
			     claimed_at=COALESCE(claimed_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			     completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			 WHERE state IN ('pending','claimed') AND harness_session_id IN (
			  SELECT id FROM harness_sessions WHERE worker_lease_digest IS NULL
			 )`,
			`UPDATE harness_sessions
			 SET phase='stopped',heartbeat_at=COALESCE(heartbeat_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			     updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1
			 WHERE worker_lease_digest IS NULL AND phase<>'stopped'`,
			`CREATE TRIGGER trg_harness_sessions_worker_lease_required BEFORE INSERT ON harness_sessions
			 WHEN NEW.worker_lease_digest IS NULL
			 BEGIN SELECT RAISE(ABORT,'harness worker lease is required'); END`,
			`CREATE TRIGGER trg_harness_sessions_inbox_target_required BEFORE INSERT ON harness_sessions
			 WHEN NEW.advertised_inbox=1 AND NEW.message_target_id IS NULL
			 BEGIN SELECT RAISE(ABORT,'harness inbox target is required'); END`,
			`CREATE TRIGGER trg_harness_sessions_inbox_target_update BEFORE UPDATE OF advertised_inbox,message_target_id ON harness_sessions
			 WHEN NEW.advertised_inbox=1 AND NEW.message_target_id IS NULL
			 BEGIN SELECT RAISE(ABORT,'harness inbox target is required'); END`,
			`CREATE TRIGGER trg_harness_sessions_worker_lease_immutable BEFORE UPDATE OF worker_lease_digest ON harness_sessions
			 BEGIN SELECT RAISE(ABORT,'harness worker lease is immutable'); END`,
		}},

		// M169 / PAI-901: authenticated, content-free worker activity is a
		// separate truth from harness phase and control leasing. Semantic state
		// transitions append the resulting projection to an immutable event
		// stream; replay-equivalent heartbeat and yield mutations do not.
		{169, []string{
			`ALTER TABLE harness_sessions ADD COLUMN activity_state TEXT NOT NULL DEFAULT 'unknown'
			 CHECK(activity_state IN ('busy','idle','unknown','dead'))`,
			`ALTER TABLE harness_sessions ADD COLUMN activity_reason TEXT NOT NULL DEFAULT 'unreported'
			 CHECK(activity_reason IN ('unreported','adapter_activity','turn_completed','heartbeat_stale','stale_evidence','malformed_evidence','unmanaged_evidence','process_exited','process_failed','ownership_lost','stopped'))`,
			`ALTER TABLE harness_sessions ADD COLUMN activity_event_kind TEXT NOT NULL DEFAULT ''
			 CHECK(activity_event_kind IN ('','session_started','turn_started','tool_started','control_applied','turn_completed'))`,
			`ALTER TABLE harness_sessions ADD COLUMN activity_at TEXT
			 CHECK(` + sqlNullableControlTimestampCheck("activity_at") + `)`,
			`ALTER TABLE harness_sessions ADD COLUMN activity_sequence INTEGER NOT NULL DEFAULT 0
			 CHECK(activity_sequence>=0)`,
			`ALTER TABLE harness_sessions ADD COLUMN closed_reason TEXT NOT NULL DEFAULT ''
			 CHECK(closed_reason IN ('','stopped','process_exited','process_failed','ownership_lost'))`,
			`UPDATE harness_sessions SET activity_state='dead',
			 activity_reason=CASE WHEN worker_lease_digest IS NULL THEN 'ownership_lost' ELSE 'stopped' END,
			 closed_reason=CASE WHEN worker_lease_digest IS NULL THEN 'ownership_lost' ELSE 'stopped' END
			 WHERE phase='stopped'`,
			`UPDATE harness_sessions SET activity_state='unknown',activity_reason='unmanaged_evidence'
			 WHERE phase<>'stopped' AND management_mode='unmanaged'`,
			`CREATE TRIGGER trg_harness_sessions_activity_shape_insert BEFORE INSERT ON harness_sessions
			 WHEN NOT (
			  (NEW.activity_state='dead' AND NEW.phase='stopped' AND NEW.closed_reason<>'' AND NEW.activity_reason IN ('stopped','process_exited','process_failed','ownership_lost')) OR
			  (NEW.management_mode='managed' AND NEW.activity_state='idle' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason='turn_completed' AND NEW.activity_event_kind='turn_completed') OR
			  (NEW.management_mode='managed' AND NEW.activity_state='busy' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason='adapter_activity' AND NEW.activity_event_kind IN ('turn_started','tool_started','control_applied')) OR
			  (NEW.management_mode='managed' AND NEW.activity_state='unknown' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason IN ('unreported','heartbeat_stale','stale_evidence','malformed_evidence')) OR
			  (NEW.management_mode='unmanaged' AND NEW.activity_state='unknown' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason='unmanaged_evidence')
			 ) BEGIN SELECT RAISE(ABORT,'harness activity state is inconsistent'); END`,
			`CREATE TRIGGER trg_harness_sessions_activity_shape_update BEFORE UPDATE OF phase,activity_state,activity_reason,activity_event_kind,closed_reason ON harness_sessions
			 WHEN NOT (
			  (NEW.activity_state='dead' AND NEW.phase='stopped' AND NEW.closed_reason<>'' AND NEW.activity_reason IN ('stopped','process_exited','process_failed','ownership_lost')) OR
			  (NEW.management_mode='managed' AND NEW.activity_state='idle' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason='turn_completed' AND NEW.activity_event_kind='turn_completed') OR
			  (NEW.management_mode='managed' AND NEW.activity_state='busy' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason='adapter_activity' AND NEW.activity_event_kind IN ('turn_started','tool_started','control_applied')) OR
			  (NEW.management_mode='managed' AND NEW.activity_state='unknown' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason IN ('unreported','heartbeat_stale','stale_evidence','malformed_evidence')) OR
			  (NEW.management_mode='unmanaged' AND NEW.activity_state='unknown' AND NEW.phase<>'stopped' AND NEW.closed_reason='' AND NEW.activity_reason='unmanaged_evidence')
			 ) BEGIN SELECT RAISE(ABORT,'harness activity state is inconsistent'); END`,
			`CREATE TABLE harness_session_events (
			 id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			 harness_session_id   TEXT NOT NULL REFERENCES harness_sessions(id) ON DELETE CASCADE,
			 event_sequence       INTEGER NOT NULL CHECK(event_sequence>0),
			 operation            TEXT NOT NULL CHECK(operation IN ('register','heartbeat','yield','control_completed','stop','activity_timeout')),
			 phase                TEXT NOT NULL CHECK(phase IN ('starting','working','yielded','stopping','stopped')),
			 activity_state       TEXT NOT NULL CHECK(activity_state IN ('busy','idle','unknown','dead')),
			 activity_reason      TEXT NOT NULL CHECK(activity_reason IN ('unreported','adapter_activity','turn_completed','heartbeat_stale','stale_evidence','malformed_evidence','unmanaged_evidence','process_exited','process_failed','ownership_lost','stopped')),
			 activity_event_kind  TEXT NOT NULL DEFAULT '' CHECK(activity_event_kind IN ('','session_started','turn_started','tool_started','control_applied','turn_completed')),
			 activity_sequence    INTEGER NOT NULL CHECK(activity_sequence>=0),
			 closed_reason        TEXT NOT NULL DEFAULT '' CHECK(closed_reason IN ('','stopped','process_exited','process_failed','ownership_lost')),
			 created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(harness_session_id,event_sequence),
			 CHECK(
			  (activity_state='dead' AND phase='stopped' AND closed_reason<>'' AND activity_reason IN ('stopped','process_exited','process_failed','ownership_lost')) OR
			  (activity_state='idle' AND phase<>'stopped' AND closed_reason='' AND activity_reason='turn_completed' AND activity_event_kind='turn_completed') OR
			  (activity_state='busy' AND phase<>'stopped' AND closed_reason='' AND activity_reason='adapter_activity' AND activity_event_kind IN ('turn_started','tool_started','control_applied')) OR
			  (activity_state='unknown' AND phase<>'stopped' AND closed_reason='' AND activity_reason IN ('unreported','heartbeat_stale','stale_evidence','malformed_evidence','unmanaged_evidence'))
			 )
			)`,
			`CREATE INDEX idx_harness_session_events_session
			 ON harness_session_events(harness_session_id,event_sequence)`,
			`CREATE INDEX idx_harness_sessions_activity
			 ON harness_sessions(activity_state,heartbeat_at) WHERE phase<>'stopped'`,
			`CREATE TRIGGER trg_harness_session_events_no_update BEFORE UPDATE ON harness_session_events
			 BEGIN SELECT RAISE(ABORT,'harness session events are immutable'); END`,
			`CREATE TRIGGER trg_harness_session_events_no_delete BEFORE DELETE ON harness_session_events
			 WHEN EXISTS(SELECT 1 FROM harness_sessions WHERE id=OLD.harness_session_id)
			 BEGIN SELECT RAISE(ABORT,'harness session events are immutable'); END`,
		}},

		// M170 / PAI-903: explicit, project-scoped worker hierarchy and ticket
		// bindings. Bindings are current state on harness_sessions; their changes
		// extend M169's immutable event stream rather than creating a second log.
		{170, []string{
			`PRAGMA foreign_keys=OFF`,
			`ALTER TABLE harness_sessions ADD COLUMN parent_harness_session_id TEXT
			 REFERENCES harness_sessions(id) ON DELETE RESTRICT`,
			`ALTER TABLE harness_sessions ADD COLUMN ticket_id INTEGER
			 REFERENCES issues(id) ON DELETE RESTRICT`,
			`DROP TRIGGER trg_harness_session_events_no_update`,
			`DROP TRIGGER trg_harness_session_events_no_delete`,
			`DROP INDEX idx_harness_session_events_session`,
			`ALTER TABLE harness_session_events RENAME TO harness_session_events_m169`,
			`CREATE TABLE harness_session_events (
			 id                         INTEGER PRIMARY KEY AUTOINCREMENT,
			 harness_session_id         TEXT NOT NULL REFERENCES harness_sessions(id) ON DELETE CASCADE,
			 event_sequence             INTEGER NOT NULL CHECK(event_sequence>0),
			 operation                  TEXT NOT NULL CHECK(operation IN ('register','heartbeat','yield','control_completed','stop','activity_timeout','binding_changed')),
			 phase                      TEXT NOT NULL CHECK(phase IN ('starting','working','yielded','stopping','stopped')),
			 activity_state             TEXT NOT NULL CHECK(activity_state IN ('busy','idle','unknown','dead')),
			 activity_reason            TEXT NOT NULL CHECK(activity_reason IN ('unreported','adapter_activity','turn_completed','heartbeat_stale','stale_evidence','malformed_evidence','unmanaged_evidence','process_exited','process_failed','ownership_lost','stopped')),
			 activity_event_kind        TEXT NOT NULL DEFAULT '' CHECK(activity_event_kind IN ('','session_started','turn_started','tool_started','control_applied','turn_completed')),
			 activity_sequence          INTEGER NOT NULL CHECK(activity_sequence>=0),
			 closed_reason              TEXT NOT NULL DEFAULT '' CHECK(closed_reason IN ('','stopped','process_exited','process_failed','ownership_lost')),
			 before_parent_harness_session_id TEXT,
			 after_parent_harness_session_id  TEXT,
			 before_ticket_id           INTEGER,
			 after_ticket_id            INTEGER,
			 created_at                 TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(harness_session_id,event_sequence),
			 CHECK(
			  (activity_state='dead' AND phase='stopped' AND closed_reason<>'' AND activity_reason IN ('stopped','process_exited','process_failed','ownership_lost')) OR
			  (activity_state='idle' AND phase<>'stopped' AND closed_reason='' AND activity_reason='turn_completed' AND activity_event_kind='turn_completed') OR
			  (activity_state='busy' AND phase<>'stopped' AND closed_reason='' AND activity_reason='adapter_activity' AND activity_event_kind IN ('turn_started','tool_started','control_applied')) OR
			  (activity_state='unknown' AND phase<>'stopped' AND closed_reason='' AND activity_reason IN ('unreported','heartbeat_stale','stale_evidence','malformed_evidence','unmanaged_evidence'))
			 ),
			 CHECK(operation<>'binding_changed' OR
			  before_parent_harness_session_id IS NOT after_parent_harness_session_id OR before_ticket_id IS NOT after_ticket_id)
			)`,
			`INSERT INTO harness_session_events(
			 id,harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,
			 activity_sequence,closed_reason,created_at)
			 SELECT id,harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,
			        activity_sequence,closed_reason,created_at FROM harness_session_events_m169`,
			`DROP TABLE harness_session_events_m169`,
			`CREATE INDEX idx_harness_session_events_session
			 ON harness_session_events(harness_session_id,event_sequence)`,
			`CREATE INDEX idx_harness_sessions_parent
			 ON harness_sessions(project_id,parent_harness_session_id) WHERE parent_harness_session_id IS NOT NULL`,
			`CREATE INDEX idx_harness_sessions_parent_ref
			 ON harness_sessions(parent_harness_session_id) WHERE parent_harness_session_id IS NOT NULL`,
			`CREATE INDEX idx_harness_sessions_ticket
			 ON harness_sessions(project_id,ticket_id) WHERE ticket_id IS NOT NULL`,
			`CREATE INDEX idx_harness_sessions_ticket_ref
			 ON harness_sessions(ticket_id) WHERE ticket_id IS NOT NULL`,
			`CREATE TRIGGER trg_harness_sessions_binding_insert BEFORE INSERT ON harness_sessions
			 WHEN
			  (NEW.parent_harness_session_id IS NOT NULL AND (
			   NEW.parent_harness_session_id=NEW.id OR
			   NOT EXISTS(SELECT 1 FROM harness_sessions parent WHERE parent.id=NEW.parent_harness_session_id
			    AND parent.project_id=NEW.project_id AND parent.phase<>'stopped') OR
			   EXISTS(WITH RECURSIVE lineage(id,parent_id,depth) AS (
			    SELECT parent.id,parent.parent_harness_session_id,1 FROM harness_sessions parent
			     WHERE parent.id=NEW.parent_harness_session_id AND parent.project_id=NEW.project_id
			    UNION ALL
			    SELECT parent.id,parent.parent_harness_session_id,lineage.depth+1
			     FROM harness_sessions parent JOIN lineage ON parent.id=lineage.parent_id
			     WHERE parent.project_id=NEW.project_id AND lineage.depth<17
			   ) SELECT 1 FROM lineage WHERE id=NEW.id OR (depth>=16 AND parent_id IS NOT NULL))
			  )) OR
			  (NEW.ticket_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM issues ticket WHERE ticket.id=NEW.ticket_id
			   AND ticket.project_id=NEW.project_id AND ticket.deleted_at IS NULL AND ticket.type IN ('ticket','task')))
			 BEGIN SELECT RAISE(ABORT,'harness session binding is invalid'); END`,
			`CREATE TRIGGER trg_harness_sessions_binding_update BEFORE UPDATE OF parent_harness_session_id,ticket_id ON harness_sessions
			 WHEN
			  (NEW.parent_harness_session_id IS NOT OLD.parent_harness_session_id AND NEW.parent_harness_session_id IS NOT NULL AND (
			   NEW.parent_harness_session_id=NEW.id OR
			   NOT EXISTS(SELECT 1 FROM harness_sessions parent WHERE parent.id=NEW.parent_harness_session_id
			    AND parent.project_id=NEW.project_id AND parent.phase<>'stopped') OR
			   EXISTS(WITH RECURSIVE lineage(id,parent_id,depth) AS (
			    SELECT parent.id,parent.parent_harness_session_id,1 FROM harness_sessions parent
			     WHERE parent.id=NEW.parent_harness_session_id AND parent.project_id=NEW.project_id
			    UNION ALL
			    SELECT parent.id,parent.parent_harness_session_id,lineage.depth+1
			     FROM harness_sessions parent JOIN lineage ON parent.id=lineage.parent_id
			     WHERE parent.project_id=NEW.project_id AND lineage.depth<17
			   ) SELECT 1 FROM lineage WHERE id=NEW.id OR (depth>=16 AND parent_id IS NOT NULL)) OR
			   (WITH RECURSIVE
			    lineage(id,parent_id,depth) AS (
			     SELECT parent.id,parent.parent_harness_session_id,1 FROM harness_sessions parent
			      WHERE parent.id=NEW.parent_harness_session_id AND parent.project_id=NEW.project_id
			     UNION ALL
			     SELECT parent.id,parent.parent_harness_session_id,lineage.depth+1
			      FROM harness_sessions parent JOIN lineage ON parent.id=lineage.parent_id
			      WHERE parent.project_id=NEW.project_id AND lineage.depth<17
			    ),
			    subtree(id,depth) AS (
			     SELECT OLD.id,0
			     UNION ALL
			     SELECT child.id,subtree.depth+1 FROM harness_sessions child
			      JOIN subtree ON child.parent_harness_session_id=subtree.id
			      WHERE child.project_id=NEW.project_id AND subtree.depth<17
			    )
			    SELECT COALESCE((SELECT MAX(depth) FROM lineage),0)+COALESCE((SELECT MAX(depth) FROM subtree),0))>16
			  )) OR
			  (NEW.ticket_id IS NOT OLD.ticket_id AND NEW.ticket_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM issues ticket WHERE ticket.id=NEW.ticket_id
			   AND ticket.project_id=NEW.project_id AND ticket.deleted_at IS NULL AND ticket.type IN ('ticket','task')))
			 BEGIN SELECT RAISE(ABORT,'harness session binding is invalid'); END`,
			`CREATE TRIGGER trg_harness_sessions_binding_revision BEFORE UPDATE OF parent_harness_session_id,ticket_id ON harness_sessions
			 WHEN (NEW.parent_harness_session_id IS NOT OLD.parent_harness_session_id OR NEW.ticket_id IS NOT OLD.ticket_id)
			  AND NEW.revision<>OLD.revision+1
			 BEGIN SELECT RAISE(ABORT,'harness session binding requires revision'); END`,
			`CREATE TRIGGER trg_harness_sessions_binding_event AFTER UPDATE OF parent_harness_session_id,ticket_id ON harness_sessions
			 WHEN NEW.parent_harness_session_id IS NOT OLD.parent_harness_session_id OR NEW.ticket_id IS NOT OLD.ticket_id
			 BEGIN INSERT INTO harness_session_events(
			  harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,
			  activity_sequence,closed_reason,before_parent_harness_session_id,after_parent_harness_session_id,before_ticket_id,after_ticket_id)
			 VALUES(NEW.id,NEW.revision,'binding_changed',NEW.phase,NEW.activity_state,NEW.activity_reason,NEW.activity_event_kind,
			  NEW.activity_sequence,NEW.closed_reason,OLD.parent_harness_session_id,NEW.parent_harness_session_id,OLD.ticket_id,NEW.ticket_id); END`,
			`CREATE TRIGGER trg_harness_session_events_no_update BEFORE UPDATE ON harness_session_events
			 BEGIN SELECT RAISE(ABORT,'harness session events are immutable'); END`,
			`CREATE TRIGGER trg_harness_session_events_no_delete BEFORE DELETE ON harness_session_events
			 WHEN EXISTS(SELECT 1 FROM harness_sessions WHERE id=OLD.harness_session_id)
			 BEGIN SELECT RAISE(ABORT,'harness session events are immutable'); END`,
			`CREATE TRIGGER trg_issues_bound_harness_move BEFORE UPDATE OF project_id,deleted_at,type ON issues
			 WHEN EXISTS(SELECT 1 FROM harness_sessions session WHERE session.ticket_id=OLD.id
			  AND (NEW.project_id IS NOT OLD.project_id OR (session.phase<>'stopped'
			   AND (NEW.deleted_at IS NOT OLD.deleted_at OR NEW.type NOT IN ('ticket','task')))))
			 BEGIN SELECT RAISE(ABORT,'bound harness ticket must be explicitly detached'); END`,
			`PRAGMA foreign_keys=ON`,
		}},

		// M171 / PAI-902: actionable attention is a derived, content-free
		// projection over authoritative ledgers. Per-orchestrator cursors and durable
		// batches make a coalesced simple wake recoverable across listener
		// crashes without creating a second worker-state truth.
		{171, []string{
			`ALTER TABLE harness_session_events ADD COLUMN assignment_present INTEGER
			 CHECK(assignment_present IN (0,1))`,
			`CREATE TABLE agent_attention_items (
			 id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			 receiver_project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 receiver_project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 address                   TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 source_project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 source_kind               TEXT NOT NULL CHECK(source_kind IN ('harness_session_event','agent_message_delivery','held_agent_message','harness_control')),
			 source_id                 TEXT NOT NULL CHECK(length(CAST(source_id AS BLOB)) BETWEEN 1 AND 64 AND source_id=trim(source_id)),
			 source_sequence           INTEGER NOT NULL CHECK(source_sequence>=0),
			 attention_kind            TEXT NOT NULL CHECK(attention_kind IN ('worker_unknown','worker_dead','assignment_turn_ended','delivery_failed','held_action','control_rejected')),
			 reason_code               TEXT NOT NULL CHECK(reason_code IN ('heartbeat_stale','stale_evidence','malformed_evidence','process_exited','process_failed','ownership_lost','stopped','turn_completed_open_assignment','target_blocked','delivery_dead','action_request_held','control_rejected')),
			 occurred_at               TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("occurred_at") + `),
			 created_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(receiver_project_agent_id,source_kind,source_id,source_sequence,attention_kind,reason_code)
			)`,
			`CREATE INDEX idx_agent_attention_items_receiver
			 ON agent_attention_items(receiver_project_id,receiver_project_agent_id,id)`,
			`CREATE TRIGGER trg_agent_attention_items_no_update BEFORE UPDATE ON agent_attention_items
			 BEGIN SELECT RAISE(ABORT,'agent attention items are immutable'); END`,
			`CREATE TRIGGER trg_agent_attention_items_no_delete BEFORE DELETE ON agent_attention_items
			 WHEN EXISTS(SELECT 1 FROM projects WHERE id=OLD.receiver_project_id)
			  AND EXISTS(SELECT 1 FROM project_agents WHERE id=OLD.receiver_project_agent_id)
			  AND EXISTS(SELECT 1 FROM projects WHERE id=OLD.source_project_id)
			 BEGIN SELECT RAISE(ABORT,'agent attention items are immutable'); END`,
			`CREATE TABLE agent_attention_projection_cursors (
			 receiver_project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 receiver_project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 source_kind               TEXT NOT NULL CHECK(source_kind IN ('harness_session_event','held_agent_message','agent_message_delivery','harness_control')),
			 source_row_id             INTEGER NOT NULL DEFAULT 0 CHECK(source_row_id>=0),
			 source_updated_at         TEXT NOT NULL DEFAULT '1970-01-01T00:00:00.000Z' CHECK(` + sqlControlTimestampCheck("source_updated_at") + `),
			 updated_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 PRIMARY KEY(receiver_project_agent_id,source_kind)
			)`,
			`CREATE INDEX idx_agent_message_deliveries_attention
			 ON agent_message_deliveries(instance,state,updated_at)`,
			`CREATE INDEX idx_harness_session_controls_attention
			 ON harness_session_controls(state,completed_at,harness_session_id)`,
			`CREATE TABLE agent_attention_cursors (
			 receiver_project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 receiver_project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 address                   TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 cursor                    INTEGER NOT NULL DEFAULT 0 CHECK(cursor>=0),
			 updated_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 PRIMARY KEY(receiver_project_agent_id)
			)`,
			`CREATE TABLE agent_attention_batches (
			 batch_id                  TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("batch_id") + `),
			 receiver_project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 receiver_project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 address                   TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 from_cursor               INTEGER NOT NULL CHECK(from_cursor>=0),
			 to_cursor                 INTEGER NOT NULL CHECK(to_cursor>from_cursor),
			 item_count                INTEGER NOT NULL CHECK(item_count BETWEEN 1 AND 32),
			 state                     TEXT NOT NULL CHECK(state IN ('pending','leased','blocked','handed_off')),
			 target_id                 TEXT REFERENCES agent_message_targets(id),
			 worker_adapter            TEXT NOT NULL DEFAULT '' CHECK(length(CAST(worker_adapter AS BLOB))<=64),
			 blocked_reason            TEXT NOT NULL DEFAULT '' CHECK(blocked_reason IN ('','target_missing','capability_missing')),
			 lease_until               TEXT CHECK(` + sqlNullableControlTimestampCheck("lease_until") + `),
			 handed_off_at             TEXT CHECK(` + sqlNullableControlTimestampCheck("handed_off_at") + `),
			 created_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 CHECK((state='blocked')=(blocked_reason<>'')),
			 CHECK((state='handed_off')=(handed_off_at IS NOT NULL)),
			 CHECK(state<>'leased' OR (target_id IS NOT NULL AND worker_adapter<>'' AND lease_until IS NOT NULL))
			)`,
			`CREATE UNIQUE INDEX idx_agent_attention_batches_open
			 ON agent_attention_batches(receiver_project_agent_id) WHERE state IN ('pending','leased','blocked')`,
			`CREATE INDEX idx_agent_attention_batches_cursor
			 ON agent_attention_batches(receiver_project_id,receiver_project_agent_id,to_cursor)`,
		}},

		// M172 / PAI-906: immutable owned-workspace provenance and the exact
		// versioned dispatch profile resolved by the authenticated reporter.
		// Empty/unknown defaults keep pre-profile harness registrations readable.
		{172, []string{
			`ALTER TABLE harness_sessions ADD COLUMN workspace_path TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN git_top_level TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN git_branch TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN workspace_identity TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN workspace_kind TEXT NOT NULL DEFAULT 'unknown'`,
			`ALTER TABLE harness_sessions ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'unknown'`,
			`ALTER TABLE harness_sessions ADD COLUMN dispatch_profile_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN dispatch_profile_version TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN dispatch_model TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN dispatch_effort TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE harness_sessions ADD COLUMN account_label TEXT NOT NULL DEFAULT 'unknown'`,
			`CREATE INDEX idx_harness_sessions_active_workspace
			 ON harness_sessions(host,workspace_identity,workspace_mode) WHERE phase<>'stopped' AND workspace_identity<>''`,
			`CREATE TRIGGER trg_harness_sessions_provenance_shape_insert BEFORE INSERT ON harness_sessions
			 WHEN NOT (
			  (NEW.workspace_identity='' AND NEW.workspace_path='' AND NEW.git_top_level='' AND NEW.git_branch=''
			   AND NEW.workspace_kind='unknown' AND NEW.workspace_mode='unknown' AND NEW.dispatch_profile_id=''
			   AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='' AND NEW.account_label='unknown')
			  OR
			  (length(NEW.workspace_identity)=64 AND NEW.workspace_identity NOT GLOB '*[^0-9a-f]*'
			   AND NEW.workspace_path<>'' AND instr(NEW.workspace_path,char(0))=0
			   AND instr(NEW.workspace_path,char(10))=0 AND instr(NEW.workspace_path,char(13))=0
			   AND NEW.workspace_kind IN ('directory','git_primary','git_worktree') AND NEW.workspace_mode IN ('exclusive','shared')
			   AND NEW.account_label IN ('unknown','chatgpt','api_key','claude_ai_max','claude_ai_pro','claude_ai_team','claude_ai_enterprise','console')
			   AND ((NEW.workspace_kind='directory' AND NEW.git_top_level='' AND NEW.git_branch='')
			    OR (NEW.workspace_kind IN ('git_primary','git_worktree') AND NEW.git_top_level<>'' AND NEW.git_branch<>''))
			   AND ((NEW.dispatch_profile_id='' AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='')
			    OR (NEW.dispatch_profile_id<>'' AND NEW.dispatch_profile_version<>'' AND NEW.dispatch_model<>'' AND NEW.dispatch_effort<>'')))
			 ) BEGIN SELECT RAISE(ABORT,'harness workspace provenance is invalid'); END`,
			`CREATE TRIGGER trg_harness_sessions_provenance_immutable BEFORE UPDATE OF
			 workspace_path,git_top_level,git_branch,workspace_identity,workspace_kind,workspace_mode,
			 dispatch_profile_id,dispatch_profile_version,dispatch_model,dispatch_effort,account_label
			 ON harness_sessions BEGIN SELECT RAISE(ABORT,'harness workspace provenance is immutable'); END`,
			`CREATE TRIGGER trg_harness_sessions_workspace_collision_insert BEFORE INSERT ON harness_sessions
			 WHEN NEW.phase<>'stopped' AND NEW.workspace_identity<>'' AND EXISTS(
			  SELECT 1 FROM harness_sessions active WHERE active.host=NEW.host
			   AND active.workspace_identity=NEW.workspace_identity AND active.phase<>'stopped'
			   AND (active.workspace_mode='exclusive' OR NEW.workspace_mode='exclusive'))
			 BEGIN SELECT RAISE(ABORT,'active harness workspace ownership conflicts'); END`,
			`CREATE TRIGGER trg_harness_sessions_workspace_collision_activate BEFORE UPDATE OF phase ON harness_sessions
			 WHEN OLD.phase='stopped' AND NEW.phase<>'stopped' AND NEW.workspace_identity<>'' AND EXISTS(
			  SELECT 1 FROM harness_sessions active WHERE active.id<>NEW.id AND active.host=NEW.host
			   AND active.workspace_identity=NEW.workspace_identity AND active.phase<>'stopped'
			   AND (active.workspace_mode='exclusive' OR NEW.workspace_mode='exclusive'))
			 BEGIN SELECT RAISE(ABORT,'active harness workspace ownership conflicts'); END`,
		}},

		// M173 / PAI-905: explicit reply obligations keep one mutable current
		// state beside append-only lifecycle facts. Human decisions on held action
		// requests are separate immutable facts and never release the held message.
		{173, []string{
			`ALTER TABLE agent_messages ADD COLUMN expects_reply INTEGER NOT NULL DEFAULT 0
			 CHECK(expects_reply IN (0,1))`,
			`CREATE TRIGGER trg_agent_messages_expects_reply_immutable BEFORE UPDATE OF expects_reply ON agent_messages
			 BEGIN SELECT RAISE(ABORT,'message reply expectation is immutable'); END`,
			`CREATE TABLE agent_reply_obligations (
			 message_row_id            INTEGER PRIMARY KEY REFERENCES agent_messages(id) ON DELETE CASCADE,
			 project_id                INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 sender_agent_id           INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 state                     TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','closed')),
			 closing_message_row_id    INTEGER UNIQUE REFERENCES agent_messages(id),
			 resurface_count           INTEGER NOT NULL DEFAULT 0 CHECK(resurface_count BETWEEN 0 AND 6),
			 next_attention_at         TEXT CHECK(` + sqlNullableControlTimestampCheck("next_attention_at") + `),
			 opened_at                 TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("opened_at") + `),
			 closed_at                 TEXT CHECK(` + sqlNullableControlTimestampCheck("closed_at") + `),
			 CHECK((state='open' AND closing_message_row_id IS NULL AND closed_at IS NULL) OR
			       (state='closed' AND closing_message_row_id IS NOT NULL AND closed_at IS NOT NULL AND next_attention_at IS NULL))
			)`,
			`CREATE INDEX idx_agent_reply_obligations_due
			 ON agent_reply_obligations(state,next_attention_at,message_row_id)`,
			`CREATE TRIGGER trg_agent_reply_obligation_insert_guard BEFORE INSERT ON agent_reply_obligations
			 WHEN NEW.state<>'open' OR NEW.resurface_count<>0 OR NEW.next_attention_at IS NULL OR
			  NOT EXISTS(SELECT 1 FROM agent_messages message JOIN project_agents sender ON sender.id=message.from_agent_id
			  WHERE message.id=NEW.message_row_id AND message.expects_reply=1 AND sender.project_id=NEW.project_id
			   AND sender.id=NEW.sender_agent_id AND message.delivered=1 AND message.is_action_request=0)
			 BEGIN SELECT RAISE(ABORT,'reply obligation does not match its message'); END`,
			`CREATE TRIGGER trg_agent_reply_obligation_update_guard BEFORE UPDATE ON agent_reply_obligations
			 WHEN OLD.state='closed' OR NEW.message_row_id<>OLD.message_row_id OR NEW.project_id<>OLD.project_id
			  OR NEW.sender_agent_id<>OLD.sender_agent_id OR NEW.opened_at<>OLD.opened_at
			  OR (NEW.state='open' AND (OLD.resurface_count=6
			      OR NEW.resurface_count NOT IN (OLD.resurface_count,OLD.resurface_count+1)
			      OR (NEW.resurface_count=OLD.resurface_count AND NEW.next_attention_at IS NULL)
			      OR (NEW.resurface_count=OLD.resurface_count+1 AND NEW.resurface_count<6 AND NEW.next_attention_at IS NULL)
			      OR (NEW.resurface_count=OLD.resurface_count+1 AND NEW.resurface_count=6 AND NEW.next_attention_at IS NOT NULL)))
			  OR (NEW.state='closed' AND (NEW.resurface_count<>OLD.resurface_count OR NOT EXISTS(
			      SELECT 1 FROM agent_messages original JOIN agent_messages reply ON reply.id=NEW.closing_message_row_id
			      WHERE original.id=OLD.message_row_id AND reply.parent_message_id=original.id
			       AND reply.reply_to=original.message_id AND reply.delivered=1 AND reply.is_action_request=0
			       AND reply.from_agent_id=original.to_agent_id AND reply.to_agent_id=original.from_agent_id)))
			 BEGIN SELECT RAISE(ABORT,'reply obligation transition is invalid'); END`,
			`CREATE TABLE agent_reply_obligation_events (
			 id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			 message_row_id         INTEGER NOT NULL REFERENCES agent_messages(id) ON DELETE CASCADE,
			 event_sequence         INTEGER NOT NULL CHECK(event_sequence>=0),
			 event_kind             TEXT NOT NULL CHECK(event_kind IN ('opened','resurfaced','closed')),
			 related_message_row_id INTEGER REFERENCES agent_messages(id),
			 occurred_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("occurred_at") + `),
			 UNIQUE(message_row_id,event_sequence),
			 CHECK((event_kind='closed')=(related_message_row_id IS NOT NULL))
			)`,
			`CREATE TRIGGER trg_agent_reply_obligation_events_no_update BEFORE UPDATE ON agent_reply_obligation_events
			 BEGIN SELECT RAISE(ABORT,'reply obligation events are immutable'); END`,
			`CREATE TRIGGER trg_agent_reply_obligation_events_no_delete BEFORE DELETE ON agent_reply_obligation_events
			 WHEN EXISTS(SELECT 1 FROM agent_messages WHERE id=OLD.message_row_id)
			 BEGIN SELECT RAISE(ABORT,'reply obligation events are immutable'); END`,
			`CREATE TRIGGER trg_agent_reply_obligation_open_event AFTER INSERT ON agent_reply_obligations
			 BEGIN INSERT INTO agent_reply_obligation_events(message_row_id,event_sequence,event_kind)
			 VALUES(NEW.message_row_id,0,'opened'); END`,
			`CREATE TRIGGER trg_agent_reply_obligation_close_event AFTER UPDATE OF state ON agent_reply_obligations
			 WHEN OLD.state='open' AND NEW.state='closed'
			 BEGIN INSERT INTO agent_reply_obligation_events(message_row_id,event_sequence,event_kind,related_message_row_id,occurred_at)
			 VALUES(NEW.message_row_id,NEW.resurface_count+1,'closed',NEW.closing_message_row_id,NEW.closed_at); END`,
			`CREATE TABLE agent_message_human_resolutions (
			 resolution_id          TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("resolution_id") + `),
			 message_row_id         INTEGER NOT NULL UNIQUE REFERENCES agent_messages(id) ON DELETE CASCADE,
			 project_id             INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 outcome                TEXT NOT NULL CHECK(outcome IN ('resolved','dismissed')),
			 actor_user_id          INTEGER NOT NULL REFERENCES users(id),
			 actor_session_id       TEXT NOT NULL CHECK(length(CAST(actor_session_id AS BLOB)) BETWEEN 1 AND 64
			  AND instr(actor_session_id,char(0))=0 AND instr(actor_session_id,char(10))=0 AND instr(actor_session_id,char(13))=0),
			 instance               TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
			 idempotency_key_digest BLOB NOT NULL CHECK(length(idempotency_key_digest)=32),
			 request_digest         BLOB NOT NULL CHECK(length(request_digest)=32),
			 created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(instance,project_id,actor_user_id,idempotency_key_digest)
			)`,
			`CREATE INDEX idx_agent_message_human_resolutions_project
			 ON agent_message_human_resolutions(project_id,created_at,message_row_id)`,
			`CREATE TRIGGER trg_agent_message_human_resolution_insert_guard BEFORE INSERT ON agent_message_human_resolutions
			 WHEN NOT EXISTS(SELECT 1 FROM agent_messages message JOIN project_agents receiver ON receiver.id=message.to_agent_id
			  WHERE message.id=NEW.message_row_id AND receiver.project_id=NEW.project_id
			   AND message.is_action_request=1 AND message.delivered=0)
			 BEGIN SELECT RAISE(ABORT,'human resolution requires a held action request'); END`,
			`CREATE TRIGGER trg_agent_message_human_resolutions_no_update BEFORE UPDATE ON agent_message_human_resolutions
			 BEGIN SELECT RAISE(ABORT,'human message resolutions are immutable'); END`,
			`CREATE TRIGGER trg_agent_message_human_resolutions_no_delete BEFORE DELETE ON agent_message_human_resolutions
			 WHEN EXISTS(SELECT 1 FROM agent_messages WHERE id=OLD.message_row_id)
			 BEGIN SELECT RAISE(ABORT,'human message resolutions are immutable'); END`,
			`PRAGMA foreign_keys=OFF`,
			`DROP TRIGGER trg_agent_attention_items_no_update`,
			`DROP TRIGGER trg_agent_attention_items_no_delete`,
			`DROP INDEX idx_agent_attention_items_receiver`,
			// Keep the AUTOINCREMENT high-water mark across the table rebuild. A
			// durable attention cursor may already exceed the largest surviving row
			// after parent cascades removed historical items.
			`CREATE TEMP TABLE agent_attention_items_m173_sequence (seq INTEGER NOT NULL)`,
			`INSERT INTO agent_attention_items_m173_sequence(seq)
			 SELECT MAX(
			  COALESCE((SELECT seq FROM sqlite_sequence WHERE name='agent_attention_items'),0),
			  COALESCE((SELECT MAX(id) FROM agent_attention_items),0)
			 )`,
			`ALTER TABLE agent_attention_items RENAME TO agent_attention_items_m171`,
			`CREATE TABLE agent_attention_items (
			 id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			 receiver_project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 receiver_project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 address                   TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 source_project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 source_kind               TEXT NOT NULL CHECK(source_kind IN ('harness_session_event','agent_message_delivery','held_agent_message','harness_control','reply_obligation')),
			 source_id                 TEXT NOT NULL CHECK(length(CAST(source_id AS BLOB)) BETWEEN 1 AND 64 AND source_id=trim(source_id)),
			 source_sequence           INTEGER NOT NULL CHECK(source_sequence>=0),
			 attention_kind            TEXT NOT NULL CHECK(attention_kind IN ('worker_unknown','worker_dead','assignment_turn_ended','delivery_failed','held_action','control_rejected','reply_overdue')),
			 reason_code               TEXT NOT NULL CHECK(reason_code IN ('heartbeat_stale','stale_evidence','malformed_evidence','process_exited','process_failed','ownership_lost','stopped','turn_completed_open_assignment','target_blocked','delivery_dead','action_request_held','control_rejected','reply_expected')),
			 occurred_at               TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("occurred_at") + `),
			 created_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(receiver_project_agent_id,source_kind,source_id,source_sequence,attention_kind,reason_code)
			)`,
			`INSERT INTO agent_attention_items(id,receiver_project_id,receiver_project_agent_id,address,source_project_id,
			 source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at,created_at)
			 SELECT id,receiver_project_id,receiver_project_agent_id,address,source_project_id,
			 source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at,created_at
			 FROM agent_attention_items_m171`,
			`DROP TABLE agent_attention_items_m171`,
			`INSERT INTO sqlite_sequence(name,seq)
			 SELECT 'agent_attention_items',seq FROM agent_attention_items_m173_sequence
			 WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='agent_attention_items')`,
			`UPDATE sqlite_sequence
			 SET seq=MAX(seq,(SELECT seq FROM agent_attention_items_m173_sequence))
			 WHERE name='agent_attention_items'`,
			`DROP TABLE agent_attention_items_m173_sequence`,
			`CREATE INDEX idx_agent_attention_items_receiver
			 ON agent_attention_items(receiver_project_id,receiver_project_agent_id,id)`,
			`CREATE TRIGGER trg_agent_attention_items_no_update BEFORE UPDATE ON agent_attention_items
			 BEGIN SELECT RAISE(ABORT,'agent attention items are immutable'); END`,
			`CREATE TRIGGER trg_agent_attention_items_no_delete BEFORE DELETE ON agent_attention_items
			 WHEN EXISTS(SELECT 1 FROM projects WHERE id=OLD.receiver_project_id)
			  AND EXISTS(SELECT 1 FROM project_agents WHERE id=OLD.receiver_project_agent_id)
			  AND EXISTS(SELECT 1 FROM projects WHERE id=OLD.source_project_id)
			 BEGIN SELECT RAISE(ABORT,'agent attention items are immutable'); END`,
			`DROP INDEX idx_agent_attention_batches_open`,
			`DROP INDEX idx_agent_attention_batches_cursor`,
			`ALTER TABLE agent_attention_batches RENAME TO agent_attention_batches_m171`,
			`CREATE TABLE agent_attention_batches (
			 batch_id                  TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("batch_id") + `),
			 receiver_project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 receiver_project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 address                   TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 from_cursor               INTEGER NOT NULL CHECK(from_cursor>=0),
			 to_cursor                 INTEGER NOT NULL CHECK(to_cursor>from_cursor),
			 item_count                INTEGER NOT NULL CHECK(item_count BETWEEN 1 AND 32),
			 state                     TEXT NOT NULL CHECK(state IN ('pending','leased','blocked','handed_off','superseded')),
			 target_id                 TEXT REFERENCES agent_message_targets(id),
			 worker_adapter            TEXT NOT NULL DEFAULT '' CHECK(length(CAST(worker_adapter AS BLOB))<=64),
			 blocked_reason            TEXT NOT NULL DEFAULT '' CHECK(blocked_reason IN ('','target_missing','capability_missing')),
			 lease_until               TEXT CHECK(` + sqlNullableControlTimestampCheck("lease_until") + `),
			 handed_off_at             TEXT CHECK(` + sqlNullableControlTimestampCheck("handed_off_at") + `),
			 superseded_at             TEXT CHECK(` + sqlNullableControlTimestampCheck("superseded_at") + `),
			 created_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 updated_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("updated_at") + `),
			 CHECK((state='blocked')=(blocked_reason<>'')),
			 CHECK((state='handed_off')=(handed_off_at IS NOT NULL)),
			 CHECK((state='superseded')=(superseded_at IS NOT NULL)),
			 CHECK(state<>'leased' OR (target_id IS NOT NULL AND worker_adapter<>'' AND lease_until IS NOT NULL))
			)`,
			`INSERT INTO agent_attention_batches(batch_id,receiver_project_id,receiver_project_agent_id,address,
			 from_cursor,to_cursor,item_count,state,target_id,worker_adapter,blocked_reason,lease_until,handed_off_at,
			 created_at,updated_at)
			 SELECT batch_id,receiver_project_id,receiver_project_agent_id,address,from_cursor,to_cursor,item_count,state,
			 target_id,worker_adapter,blocked_reason,lease_until,handed_off_at,created_at,updated_at
			 FROM agent_attention_batches_m171`,
			`DROP TABLE agent_attention_batches_m171`,
			`CREATE UNIQUE INDEX idx_agent_attention_batches_open
			 ON agent_attention_batches(receiver_project_agent_id) WHERE state IN ('pending','leased','blocked')`,
			`CREATE INDEX idx_agent_attention_batches_cursor
			 ON agent_attention_batches(receiver_project_id,receiver_project_agent_id,to_cursor)`,
			`PRAGMA foreign_keys=ON`,
		}},

		// M174 / PAI-907: nullable storage keeps pre-shape bindings unknown while
		// every classified value is the closed ship/scout enum. Binding changes
		// remain revisioned and append exact before/after shape evidence.
		{174, []string{
			`ALTER TABLE harness_sessions ADD COLUMN work_shape TEXT CHECK(work_shape IN ('ship','scout'))`,
			`DROP TRIGGER trg_harness_sessions_binding_revision`,
			`DROP TRIGGER trg_harness_sessions_binding_event`,
			`DROP TRIGGER trg_harness_session_events_no_update`,
			`DROP TRIGGER trg_harness_session_events_no_delete`,
			`DROP INDEX idx_harness_session_events_session`,
			`CREATE TEMP TABLE harness_session_events_m174_sequence (seq INTEGER NOT NULL)`,
			`INSERT INTO harness_session_events_m174_sequence(seq)
			 SELECT MAX(
			  COALESCE((SELECT seq FROM sqlite_sequence WHERE name='harness_session_events'),0),
			  COALESCE((SELECT MAX(id) FROM harness_session_events),0)
			 )`,
			`ALTER TABLE harness_session_events RENAME TO harness_session_events_m173`,
			`CREATE TABLE harness_session_events (
			 id                         INTEGER PRIMARY KEY AUTOINCREMENT,
			 harness_session_id         TEXT NOT NULL REFERENCES harness_sessions(id) ON DELETE CASCADE,
			 event_sequence             INTEGER NOT NULL CHECK(event_sequence>0),
			 operation                  TEXT NOT NULL CHECK(operation IN ('register','heartbeat','yield','control_completed','stop','activity_timeout','binding_changed')),
			 phase                      TEXT NOT NULL CHECK(phase IN ('starting','working','yielded','stopping','stopped')),
			 activity_state             TEXT NOT NULL CHECK(activity_state IN ('busy','idle','unknown','dead')),
			 activity_reason            TEXT NOT NULL CHECK(activity_reason IN ('unreported','adapter_activity','turn_completed','heartbeat_stale','stale_evidence','malformed_evidence','unmanaged_evidence','process_exited','process_failed','ownership_lost','stopped')),
			 activity_event_kind        TEXT NOT NULL DEFAULT '' CHECK(activity_event_kind IN ('','session_started','turn_started','tool_started','control_applied','turn_completed')),
			 activity_sequence          INTEGER NOT NULL CHECK(activity_sequence>=0),
			 closed_reason              TEXT NOT NULL DEFAULT '' CHECK(closed_reason IN ('','stopped','process_exited','process_failed','ownership_lost')),
			 before_parent_harness_session_id TEXT,
			 after_parent_harness_session_id  TEXT,
			 before_ticket_id           INTEGER,
			 after_ticket_id            INTEGER,
			 assignment_present         INTEGER CHECK(assignment_present IN (0,1)),
			 before_work_shape          TEXT CHECK(before_work_shape IN ('ship','scout')),
			 after_work_shape           TEXT CHECK(after_work_shape IN ('ship','scout')),
			 created_at                 TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(harness_session_id,event_sequence),
			 CHECK(
			  (activity_state='dead' AND phase='stopped' AND closed_reason<>'' AND activity_reason IN ('stopped','process_exited','process_failed','ownership_lost')) OR
			  (activity_state='idle' AND phase<>'stopped' AND closed_reason='' AND activity_reason='turn_completed' AND activity_event_kind='turn_completed') OR
			  (activity_state='busy' AND phase<>'stopped' AND closed_reason='' AND activity_reason='adapter_activity' AND activity_event_kind IN ('turn_started','tool_started','control_applied')) OR
			  (activity_state='unknown' AND phase<>'stopped' AND closed_reason='' AND activity_reason IN ('unreported','heartbeat_stale','stale_evidence','malformed_evidence','unmanaged_evidence'))
			 ),
			 CHECK(operation<>'binding_changed' OR
			  before_parent_harness_session_id IS NOT after_parent_harness_session_id OR
			  before_ticket_id IS NOT after_ticket_id OR before_work_shape IS NOT after_work_shape)
			)`,
			`INSERT INTO harness_session_events(
			 id,harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,
			 activity_sequence,closed_reason,before_parent_harness_session_id,after_parent_harness_session_id,
			 before_ticket_id,after_ticket_id,assignment_present,created_at)
			 SELECT id,harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,
			 activity_sequence,closed_reason,before_parent_harness_session_id,after_parent_harness_session_id,
			 before_ticket_id,after_ticket_id,assignment_present,created_at FROM harness_session_events_m173`,
			`DROP TABLE harness_session_events_m173`,
			`INSERT INTO sqlite_sequence(name,seq)
			 SELECT 'harness_session_events',seq FROM harness_session_events_m174_sequence
			 WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='harness_session_events')`,
			`UPDATE sqlite_sequence
			 SET seq=MAX(seq,(SELECT seq FROM harness_session_events_m174_sequence))
			 WHERE name='harness_session_events'`,
			`DROP TABLE harness_session_events_m174_sequence`,
			`CREATE INDEX idx_harness_session_events_session ON harness_session_events(harness_session_id,event_sequence)`,
			`CREATE TRIGGER trg_harness_sessions_work_shape_insert BEFORE INSERT ON harness_sessions
			 WHEN (NEW.ticket_id IS NULL)<>(NEW.work_shape IS NULL)
			 BEGIN SELECT RAISE(ABORT,'harness ticket binding and work shape must be assigned together'); END`,
			`CREATE TRIGGER trg_harness_sessions_work_shape_update BEFORE UPDATE OF ticket_id,work_shape ON harness_sessions
			 WHEN (NEW.ticket_id IS NULL)<>(NEW.work_shape IS NULL)
			  AND (NEW.ticket_id IS NOT OLD.ticket_id OR NEW.work_shape IS NOT OLD.work_shape)
			 BEGIN SELECT RAISE(ABORT,'harness ticket binding and work shape must be assigned together'); END`,
			`CREATE TRIGGER trg_harness_sessions_binding_revision BEFORE UPDATE OF parent_harness_session_id,ticket_id,work_shape ON harness_sessions
			 WHEN (NEW.parent_harness_session_id IS NOT OLD.parent_harness_session_id OR NEW.ticket_id IS NOT OLD.ticket_id OR NEW.work_shape IS NOT OLD.work_shape)
			  AND NEW.revision<>OLD.revision+1
			 BEGIN SELECT RAISE(ABORT,'harness session binding requires revision'); END`,
			`CREATE TRIGGER trg_harness_sessions_binding_event AFTER UPDATE OF parent_harness_session_id,ticket_id,work_shape ON harness_sessions
			 WHEN NEW.parent_harness_session_id IS NOT OLD.parent_harness_session_id OR NEW.ticket_id IS NOT OLD.ticket_id OR NEW.work_shape IS NOT OLD.work_shape
			 BEGIN INSERT INTO harness_session_events(
			  harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,
			  activity_sequence,closed_reason,before_parent_harness_session_id,after_parent_harness_session_id,before_ticket_id,after_ticket_id,before_work_shape,after_work_shape)
			 VALUES(NEW.id,NEW.revision,'binding_changed',NEW.phase,NEW.activity_state,NEW.activity_reason,NEW.activity_event_kind,
			  NEW.activity_sequence,NEW.closed_reason,OLD.parent_harness_session_id,NEW.parent_harness_session_id,OLD.ticket_id,NEW.ticket_id,OLD.work_shape,NEW.work_shape); END`,
			`CREATE TRIGGER trg_harness_session_events_no_update BEFORE UPDATE ON harness_session_events
			 BEGIN SELECT RAISE(ABORT,'harness session events are immutable'); END`,
			`CREATE TRIGGER trg_harness_session_events_no_delete BEFORE DELETE ON harness_session_events
			 WHEN EXISTS(SELECT 1 FROM harness_sessions WHERE id=OLD.harness_session_id)
			 BEGIN SELECT RAISE(ABORT,'harness session events are immutable'); END`,
		}},

		// M175 / PAI-876: additive scheme-aware identity for Pharos external-
		// stage evidence. The M148 v1 evidence table and its guards remain
		// unchanged; v2 rows extend an exact committed v1-compatible fact and
		// verification names the exact prior v2 deployment fact it matches.
		{175, []string{
			`CREATE TABLE external_stage_pharos_evidence_v2 (
			 report_event_id                 INTEGER PRIMARY KEY REFERENCES external_stage_pharos_evidence(report_event_id),
			 version_scheme                  TEXT NOT NULL CHECK(version_scheme IN ('legacy','inspr-calendar-v1')),
			 release_channel                 TEXT NOT NULL CHECK(length(CAST(release_channel AS BLOB)) BETWEEN 1 AND 64 AND
			  release_channel GLOB '[a-z]*' AND release_channel NOT GLOB '*[^a-z0-9._-]*'),
			 release_sequence                INTEGER NOT NULL CHECK(release_sequence>=0),
			 release_manifest_coordinate     TEXT NOT NULL CHECK(length(CAST(release_manifest_coordinate AS BLOB)) BETWEEN 3 AND 255 AND
			  instr(release_manifest_coordinate,':') BETWEEN 2 AND 65 AND
			  substr(release_manifest_coordinate,1,1) GLOB '[a-z]' AND
			  substr(release_manifest_coordinate,1,instr(release_manifest_coordinate,':')-1) NOT GLOB '*[^a-z0-9._-]*' AND
			  substr(release_manifest_coordinate,instr(release_manifest_coordinate,':')+1,1) GLOB '[A-Za-z0-9]' AND
			  substr(release_manifest_coordinate,instr(release_manifest_coordinate,':')+1) NOT GLOB '*[^A-Za-z0-9._/@:+-]*'),
			 release_manifest_digest         BLOB NOT NULL CHECK(typeof(release_manifest_digest)='blob' AND length(release_manifest_digest)=32),
			 bound_deployment_report_event_id INTEGER REFERENCES external_stage_pharos_evidence_v2(report_event_id)
			) WITHOUT ROWID`,
			`CREATE TRIGGER trg_external_stage_pharos_evidence_v2_insert_guard
			 BEFORE INSERT ON external_stage_pharos_evidence_v2
			 WHEN NOT EXISTS(
			  SELECT 1 FROM external_stage_pharos_evidence evidence
			  JOIN external_stage_report_events report ON report.id=evidence.report_event_id
			  JOIN external_stage_handoffs handoff ON handoff.id=report.handoff_row_id
			  WHERE evidence.report_event_id=NEW.report_event_id AND handoff.reporter_class='pharos'
			   AND ((evidence.evidence_kind='deployment' AND NEW.bound_deployment_report_event_id IS NULL) OR
			        (evidence.evidence_kind='verification' AND NEW.bound_deployment_report_event_id IS NOT NULL AND EXISTS(
			          SELECT 1 FROM external_stage_pharos_evidence deployment
			          JOIN external_stage_pharos_evidence_v2 deployment_v2 ON deployment_v2.report_event_id=deployment.report_event_id
			          JOIN external_stage_report_events deployment_report ON deployment_report.id=deployment.report_event_id
			          JOIN external_stage_handoffs deployment_handoff ON deployment_handoff.id=deployment_report.handoff_row_id
			          WHERE deployment.report_event_id=NEW.bound_deployment_report_event_id
			           AND deployment.evidence_kind='deployment' AND deployment.result='succeeded'
			           AND deployment_handoff.delivery_id=handoff.delivery_id AND deployment_handoff.attempt_id=handoff.attempt_id
			           AND deployment_handoff.stage_key='deployment' AND deployment_handoff.lifecycle_state='succeeded'
			           AND deployment.environment_symbol=evidence.environment_symbol
			           AND deployment.artifact_version=evidence.artifact_version
			           AND deployment.artifact_digest=evidence.artifact_digest
			           AND deployment.commit_digest=evidence.commit_digest
			           AND deployment_v2.version_scheme=NEW.version_scheme
			           AND deployment_v2.release_channel=NEW.release_channel
			           AND deployment_v2.release_sequence=NEW.release_sequence
			           AND deployment_v2.release_manifest_coordinate=NEW.release_manifest_coordinate
			           AND deployment_v2.release_manifest_digest=NEW.release_manifest_digest))))
			 BEGIN SELECT RAISE(ABORT,'invalid external stage Pharos v2 evidence binding'); END`,
			`CREATE TRIGGER trg_external_stage_pharos_evidence_v2_no_update BEFORE UPDATE ON external_stage_pharos_evidence_v2
			 BEGIN SELECT RAISE(ABORT,'external stage v2 evidence is immutable'); END`,
			`CREATE TRIGGER trg_external_stage_pharos_evidence_v2_no_delete BEFORE DELETE ON external_stage_pharos_evidence_v2
			 BEGIN SELECT RAISE(ABORT,'external stage v2 evidence is immutable'); END`,
		}},

		// M176 / PAI-924: immutable typed requests and one audited outcome per
		// intent. Reporter leases never appear in browser projections.
		{176, []string{
			`CREATE TABLE IF NOT EXISTS lifecycle_runtimes (
			 id TEXT PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id),
			 generation TEXT NOT NULL, machine_id TEXT NOT NULL,
			 user_id INTEGER NOT NULL REFERENCES users(id), api_key_id INTEGER NOT NULL CHECK(api_key_id>0),
			 lease_digest BLOB NOT NULL CHECK(length(lease_digest)=32),
			 registration_json TEXT NOT NULL CHECK(json_valid(registration_json) AND length(registration_json)<=8192),
			 expires_at TEXT NOT NULL, created_at TEXT NOT NULL,
			 UNIQUE(project_id,generation))`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_runtime_no_delete BEFORE DELETE ON lifecycle_runtimes
			 BEGIN SELECT RAISE(ABORT,'immutable runtime history'); END`,
			`CREATE INDEX IF NOT EXISTS idx_lifecycle_runtime_project ON lifecycle_runtimes(project_id,expires_at)`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_runtime_identity BEFORE UPDATE OF id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,created_at ON lifecycle_runtimes
			 BEGIN SELECT RAISE(ABORT,'immutable runtime identity'); END`,
			`CREATE TABLE IF NOT EXISTS lifecycle_runtime_sessions (
			 session_id TEXT PRIMARY KEY,
			 runtime_id TEXT NOT NULL REFERENCES lifecycle_runtimes(id),
			 generation TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL)`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_runtime_session_immutable BEFORE UPDATE ON lifecycle_runtime_sessions
			 BEGIN SELECT RAISE(ABORT,'immutable runtime session generation'); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_runtime_session_no_delete BEFORE DELETE ON lifecycle_runtime_sessions
			 BEGIN SELECT RAISE(ABORT,'immutable runtime session generation'); END`,
			`CREATE TABLE IF NOT EXISTS lifecycle_intents (
			 id TEXT PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id),
			 runtime_id TEXT NOT NULL REFERENCES lifecycle_runtimes(id),
			 user_id INTEGER NOT NULL REFERENCES users(id), session_credential_id TEXT NOT NULL,
			 request_key TEXT NOT NULL, request_json TEXT NOT NULL CHECK(json_valid(request_json) AND length(request_json)<=8192),
			 new_generation TEXT NOT NULL DEFAULT '', result_session_id TEXT NOT NULL DEFAULT '',
			 state TEXT NOT NULL CHECK(state IN ('requested','claimed','executing','completed','failed','expired','cancelled')),
			 reason TEXT NOT NULL DEFAULT '' CHECK(reason IN ('','applied','failed','unsupported','ownership_lost','outcome_unknown','authority_revoked','expired','cancelled')),
			 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 5),
			 created_at TEXT NOT NULL, expires_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			 actor_kind TEXT NOT NULL CHECK(actor_kind IN ('session','api_key')), actor_user_id INTEGER NOT NULL, actor_credential_id TEXT NOT NULL,
			 UNIQUE(project_id,user_id,request_key),
			 CHECK((state IN ('requested','claimed','executing') AND reason='' AND result_session_id='') OR
			 (state='completed' AND reason='applied') OR
			 (state='failed' AND reason IN ('failed','unsupported','ownership_lost','outcome_unknown','authority_revoked') AND result_session_id='') OR
			 (state='expired' AND reason IN ('expired','outcome_unknown') AND result_session_id='') OR
			 (state='cancelled' AND reason='cancelled' AND result_session_id='')))`,
			`CREATE INDEX IF NOT EXISTS idx_lifecycle_intent_queue ON lifecycle_intents(runtime_id,state,created_at,id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_lifecycle_intent_inflight ON lifecycle_intents(runtime_id) WHERE state IN ('claimed','executing')`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_lifecycle_intent_agent_reservation
             ON lifecycle_intents(project_id,json_extract(request_json,'$.agent_name'))
             WHERE state IN ('claimed','executing') AND json_extract(request_json,'$.operation') IN ('start','restart')`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_intent_identity BEFORE UPDATE OF id,project_id,runtime_id,user_id,session_credential_id,request_key,request_json,new_generation,created_at,expires_at ON lifecycle_intents
			 BEGIN SELECT RAISE(ABORT,'immutable lifecycle intent'); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_intent_transition BEFORE UPDATE ON lifecycle_intents
			 WHEN NEW.revision<>OLD.revision+1 OR NOT (
			 (OLD.state='requested' AND NEW.state IN ('claimed','failed','expired','cancelled')) OR
			 (OLD.state='claimed' AND NEW.state IN ('executing','failed','expired','cancelled')) OR
			 (OLD.state='executing' AND NEW.state IN ('completed','failed','expired')))
			 BEGIN SELECT RAISE(ABORT,'invalid lifecycle transition'); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_intent_initial BEFORE INSERT ON lifecycle_intents
			 WHEN NEW.state<>'requested' OR NEW.revision<>1 OR NEW.reason<>'' OR NEW.result_session_id<>''
			 BEGIN SELECT RAISE(ABORT,'invalid lifecycle initial state'); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_intent_no_delete BEFORE DELETE ON lifecycle_intents
			 BEGIN SELECT RAISE(ABORT,'immutable lifecycle history'); END`,
			`CREATE TABLE IF NOT EXISTS lifecycle_intent_events (
			 intent_id TEXT NOT NULL REFERENCES lifecycle_intents(id), revision INTEGER NOT NULL,
			 state TEXT NOT NULL, reason TEXT NOT NULL, created_at TEXT NOT NULL,
			 actor_kind TEXT NOT NULL, actor_user_id INTEGER NOT NULL, actor_credential_id TEXT NOT NULL,
			 PRIMARY KEY(intent_id,revision)) WITHOUT ROWID`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_intent_insert_event AFTER INSERT ON lifecycle_intents
			 BEGIN INSERT INTO lifecycle_intent_events VALUES(NEW.id,NEW.revision,NEW.state,NEW.reason,NEW.updated_at,NEW.actor_kind,NEW.actor_user_id,NEW.actor_credential_id); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_intent_update_event AFTER UPDATE ON lifecycle_intents
			 BEGIN INSERT INTO lifecycle_intent_events VALUES(NEW.id,NEW.revision,NEW.state,NEW.reason,NEW.updated_at,NEW.actor_kind,NEW.actor_user_id,NEW.actor_credential_id); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_event_insert_guard BEFORE INSERT ON lifecycle_intent_events
			 WHEN NOT EXISTS(SELECT 1 FROM lifecycle_intents i WHERE i.id=NEW.intent_id AND i.revision=NEW.revision AND i.state=NEW.state AND i.reason=NEW.reason AND i.updated_at=NEW.created_at AND i.actor_kind=NEW.actor_kind AND i.actor_user_id=NEW.actor_user_id AND i.actor_credential_id=NEW.actor_credential_id)
			 BEGIN SELECT RAISE(ABORT,'invalid lifecycle event'); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_event_no_update BEFORE UPDATE ON lifecycle_intent_events
			 BEGIN SELECT RAISE(ABORT,'immutable lifecycle event'); END`,
			`CREATE TRIGGER IF NOT EXISTS lifecycle_event_no_delete BEFORE DELETE ON lifecycle_intent_events
			 BEGIN SELECT RAISE(ABORT,'immutable lifecycle event'); END`,
		}},

		// M177 / PAI-923: sticky generation-fenced consumer ownership and typed runtime health.
		{177, []string{
			`CREATE TABLE IF NOT EXISTS agent_consumer_streams (
 id TEXT PRIMARY KEY, project_id INTEGER NOT NULL, agent_id INTEGER NOT NULL, address TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('fallback','attention')), revision INTEGER NOT NULL CHECK(revision>0),
 generation TEXT NOT NULL, registration_json TEXT NOT NULL CHECK(json_valid(registration_json)),
 runtime_id TEXT NOT NULL, runtime_generation TEXT NOT NULL, session_id TEXT NOT NULL, session_generation TEXT NOT NULL,
 user_id INTEGER NOT NULL, api_key_id INTEGER NOT NULL, target_id TEXT NOT NULL, target_version INTEGER NOT NULL,
 lease_digest BLOB NOT NULL CHECK(length(lease_digest)=32), expires_at TEXT NOT NULL,
 UNIQUE(project_id,agent_id,kind))`,
			`CREATE TABLE IF NOT EXISTS agent_consumer_attempts (
 id TEXT PRIMARY KEY,stream_id TEXT NOT NULL REFERENCES agent_consumer_streams(id),stream_revision INTEGER NOT NULL,
 request_key TEXT NOT NULL, nonce_digest BLOB NOT NULL CHECK(length(nonce_digest)=32),
 owner_json TEXT NOT NULL CHECK(json_valid(owner_json)), consumer_digest BLOB NOT NULL CHECK(length(consumer_digest)=32),
 resource_id TEXT NOT NULL, cursor INTEGER NOT NULL CHECK(cursor>0),
 state TEXT NOT NULL CHECK(state IN ('claimed','executing','completed','released','outcome_unknown')),
 expires_at TEXT NOT NULL, result_json TEXT NOT NULL DEFAULT '',
 UNIQUE(stream_id,stream_revision,request_key),UNIQUE(stream_id,nonce_digest))`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_consumer_attempt_open ON agent_consumer_attempts(stream_id) WHERE state IN ('claimed','executing','outcome_unknown')`,
			`CREATE TRIGGER IF NOT EXISTS consumer_attempt_identity BEFORE UPDATE OF id,stream_id,stream_revision,request_key,nonce_digest,owner_json,consumer_digest,resource_id,cursor,expires_at ON agent_consumer_attempts
 BEGIN SELECT RAISE(ABORT,'immutable consumer attempt'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_attempt_terminal BEFORE UPDATE ON agent_consumer_attempts
 WHEN OLD.state IN ('completed','released','outcome_unknown') OR NOT ((OLD.state='claimed' AND NEW.state IN ('executing','released')) OR (OLD.state='executing' AND NEW.state IN ('completed','outcome_unknown')))
 BEGIN SELECT RAISE(ABORT,'invalid consumer transition'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_attempt_no_delete BEFORE DELETE ON agent_consumer_attempts BEGIN SELECT RAISE(ABORT,'immutable consumer attempt'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_stream_no_delete BEFORE DELETE ON agent_consumer_streams BEGIN SELECT RAISE(ABORT,'sticky consumer ownership'); END`,
			`ALTER TABLE agent_message_deliveries ADD COLUMN consumer_fence INTEGER NOT NULL DEFAULT 0 CHECK(consumer_fence>=0)`,
			`ALTER TABLE agent_attention_batches ADD COLUMN consumer_fence INTEGER NOT NULL DEFAULT 0 CHECK(consumer_fence>=0)`,
			`ALTER TABLE agent_message_cursors ADD COLUMN consumer_fence INTEGER NOT NULL DEFAULT 0 CHECK(consumer_fence>=0)`,
			`ALTER TABLE agent_attention_cursors ADD COLUMN consumer_fence INTEGER NOT NULL DEFAULT 0 CHECK(consumer_fence>=0)`,
			`CREATE TRIGGER IF NOT EXISTS consumer_delivery_fence BEFORE UPDATE ON agent_message_deliveries WHEN EXISTS(SELECT 1 FROM agent_consumer_streams s JOIN agent_messages m ON m.to_agent_id=s.agent_id AND m.to_address=s.address WHERE s.kind='fallback' AND m.id=OLD.message_row_id AND (
 EXISTS(SELECT 1 FROM agent_message_targets t WHERE t.id=(CASE
	WHEN OLD.last_error_code='managed_target_unavailable' AND OLD.fallback_target_id IS NOT NULL
	THEN OLD.fallback_target_id
	WHEN OLD.requested_level='simple' AND OLD.primary_target_id IS NOT NULL AND OLD.fallback_target_id IS NOT NULL
	 AND (SELECT adapter FROM agent_message_targets policy_target WHERE policy_target.id=OLD.primary_target_id) IN ('agentd_codex','agentd_claude','agentd_pi')
	THEN OLD.fallback_target_id
	WHEN OLD.requested_level='steer' AND OLD.primary_target_id IS NOT NULL AND OLD.fallback_target_id IS NOT NULL
	 AND (SELECT maximum_level FROM agent_message_targets policy_target WHERE policy_target.id=OLD.primary_target_id)='simple'
	THEN OLD.fallback_target_id ELSE COALESCE(OLD.primary_target_id,OLD.fallback_target_id) END) AND t.adapter='codex')
 OR EXISTS(SELECT 1 FROM agent_consumer_attempts a WHERE a.stream_id=s.id AND a.resource_id=OLD.delivery_id))) AND NEW.consumer_fence<>OLD.consumer_fence+1 BEGIN SELECT RAISE(ABORT,'consumer ownership required'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_attention_insert BEFORE INSERT ON agent_attention_batches WHEN NEW.state='leased' AND EXISTS(SELECT 1 FROM agent_consumer_streams s WHERE s.kind='attention' AND s.project_id=NEW.receiver_project_id AND s.agent_id=NEW.receiver_project_agent_id) AND NEW.consumer_fence<=0 BEGIN SELECT RAISE(ABORT,'consumer ownership required'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_attention_fence BEFORE UPDATE ON agent_attention_batches WHEN EXISTS(SELECT 1 FROM agent_consumer_streams s WHERE s.kind='attention' AND s.project_id=OLD.receiver_project_id AND s.agent_id=OLD.receiver_project_agent_id) AND NEW.consumer_fence<>OLD.consumer_fence+1 BEGIN SELECT RAISE(ABORT,'consumer ownership required'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_fallback_cursor_insert BEFORE INSERT ON agent_message_cursors WHEN EXISTS(SELECT 1 FROM agent_consumer_streams s WHERE s.kind='fallback' AND s.project_id=NEW.project_id AND s.agent_id=NEW.project_agent_id) AND (NEW.consumer_fence<=0) BEGIN SELECT RAISE(ABORT,'consumer cursor ownership required'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_fallback_cursor_update BEFORE UPDATE ON agent_message_cursors WHEN EXISTS(SELECT 1 FROM agent_consumer_streams s WHERE s.kind='fallback' AND s.project_id=NEW.project_id AND s.agent_id=NEW.project_agent_id) AND (NEW.cursor>OLD.cursor AND NEW.consumer_fence<>OLD.consumer_fence+1) BEGIN SELECT RAISE(ABORT,'consumer cursor ownership required'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_attention_cursor_insert BEFORE INSERT ON agent_attention_cursors WHEN EXISTS(SELECT 1 FROM agent_consumer_streams s WHERE s.kind='attention' AND s.project_id=NEW.receiver_project_id AND s.agent_id=NEW.receiver_project_agent_id) AND (NEW.consumer_fence<=0) BEGIN SELECT RAISE(ABORT,'consumer cursor ownership required'); END`,
			`CREATE TRIGGER IF NOT EXISTS consumer_attention_cursor_update BEFORE UPDATE ON agent_attention_cursors WHEN EXISTS(SELECT 1 FROM agent_consumer_streams s WHERE s.kind='attention' AND s.project_id=NEW.receiver_project_id AND s.agent_id=NEW.receiver_project_agent_id) AND (NEW.cursor>OLD.cursor AND NEW.consumer_fence<>OLD.consumer_fence+1) BEGIN SELECT RAISE(ABORT,'consumer cursor ownership required'); END`,
			`CREATE TABLE IF NOT EXISTS agent_runtime_health (
 id TEXT PRIMARY KEY,runtime_id TEXT NOT NULL,project_id INTEGER NOT NULL,layer TEXT NOT NULL CHECK(layer IN ('reporter','primary','fallback','attention')),
 sequence INTEGER NOT NULL CHECK(sequence>0),state TEXT NOT NULL CHECK(state IN ('healthy','unhealthy')),
 reason TEXT NOT NULL CHECK(reason IN ('recovered','consumer_crash_loop','consumer_authority_unavailable','consumer_transport_failed')),
 failure_count INTEGER NOT NULL CHECK(failure_count BETWEEN 0 AND 10),episode INTEGER NOT NULL CHECK(episode BETWEEN 0 AND 10000),published INTEGER NOT NULL DEFAULT 0 CHECK(published IN (0,1)),published_at TEXT NOT NULL,updated_at TEXT NOT NULL,
 UNIQUE(runtime_id,layer))`,
			`DROP TRIGGER IF EXISTS trg_agent_attention_items_no_update`,
			`DROP TRIGGER IF EXISTS trg_agent_attention_items_no_delete`,
			`DROP INDEX IF EXISTS idx_agent_attention_items_receiver`,
			`CREATE TEMP TABLE consumer_attention_sequence AS SELECT seq FROM sqlite_sequence WHERE name='agent_attention_items'`,
			`ALTER TABLE agent_attention_items RENAME TO agent_attention_items_m176`,
			`CREATE TABLE IF NOT EXISTS agent_attention_items (
			 id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			 receiver_project_id       INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 receiver_project_agent_id INTEGER NOT NULL REFERENCES project_agents(id) ON DELETE CASCADE,
			 address                   TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
			 source_project_id         INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			 source_kind               TEXT NOT NULL CHECK(source_kind IN ('harness_session_event','agent_message_delivery','held_agent_message','harness_control','reply_obligation','runtime_health')),
			 source_id                 TEXT NOT NULL CHECK(length(CAST(source_id AS BLOB)) BETWEEN 1 AND 64 AND source_id=trim(source_id)),
			 source_sequence           INTEGER NOT NULL CHECK(source_sequence>=0),
			 attention_kind            TEXT NOT NULL CHECK(attention_kind IN ('worker_unknown','worker_dead','assignment_turn_ended','delivery_failed','held_action','control_rejected','reply_overdue','runtime_unhealthy')),
			 reason_code               TEXT NOT NULL CHECK(reason_code IN ('heartbeat_stale','stale_evidence','malformed_evidence','process_exited','process_failed','ownership_lost','stopped','turn_completed_open_assignment','target_blocked','delivery_dead','action_request_held','control_rejected','reply_expected','consumer_crash_loop','consumer_authority_unavailable','consumer_transport_failed')),
			 occurred_at               TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("occurred_at") + `),
			 created_at                TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
			 UNIQUE(receiver_project_agent_id,source_kind,source_id,source_sequence,attention_kind,reason_code)
			)`,
			`INSERT INTO agent_attention_items SELECT * FROM agent_attention_items_m176`,
			`DROP TABLE agent_attention_items_m176`,
			`UPDATE sqlite_sequence SET seq=MAX(seq,COALESCE((SELECT seq FROM consumer_attention_sequence),0)) WHERE name='agent_attention_items'`,
			`INSERT INTO sqlite_sequence(name,seq) SELECT 'agent_attention_items',seq FROM consumer_attention_sequence WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='agent_attention_items')`,
			`DROP TABLE consumer_attention_sequence`,
			`CREATE INDEX IF NOT EXISTS idx_agent_attention_items_receiver ON agent_attention_items(receiver_project_id,receiver_project_agent_id,id)`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_attention_items_no_update BEFORE UPDATE ON agent_attention_items BEGIN SELECT RAISE(ABORT,'agent attention items are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS trg_agent_attention_items_no_delete BEFORE DELETE ON agent_attention_items WHEN EXISTS(SELECT 1 FROM projects WHERE id=OLD.receiver_project_id) AND EXISTS(SELECT 1 FROM project_agents WHERE id=OLD.receiver_project_agent_id) AND EXISTS(SELECT 1 FROM projects WHERE id=OLD.source_project_id) BEGIN SELECT RAISE(ABORT,'agent attention items are immutable'); END`,
		}},
	}

	// M178 changes only the message-body guard, preserving multiline bytes and
	// all existing ledger references through an atomic table rebuild.
	migrations = append(migrations, migration{version: 178})

	// M179 / PAI-923: explicit recovery of never-claimed delivery work from a
	// publicly closed managed generation. Canonical message and delivery target
	// snapshots remain immutable; this bounded ledger records the separately
	// selected effective binding.
	migrations = append(migrations, migration{version: 179, steps: []string{
		`CREATE TABLE agent_message_delivery_recoveries (
		 delivery_id              TEXT NOT NULL REFERENCES agent_message_deliveries(delivery_id) ON DELETE CASCADE,
		 sequence                 INTEGER NOT NULL CHECK(sequence BETWEEN 1 AND 8),
		 project_id               INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		 old_target_id            TEXT NOT NULL REFERENCES agent_message_targets(id),
		 old_target_version       INTEGER NOT NULL CHECK(old_target_version>0),
		 old_harness_session_id   TEXT NOT NULL REFERENCES harness_sessions(id),
		 old_runtime_id           TEXT NOT NULL REFERENCES lifecycle_runtimes(id),
		 old_session_generation   TEXT NOT NULL,
		 new_target_id            TEXT NOT NULL REFERENCES agent_message_targets(id),
		 new_target_version       INTEGER NOT NULL CHECK(new_target_version>0),
		 new_harness_session_id   TEXT NOT NULL REFERENCES harness_sessions(id),
		 new_runtime_id           TEXT NOT NULL REFERENCES lifecycle_runtimes(id),
		 new_runtime_generation   TEXT NOT NULL,
		 new_session_generation   TEXT NOT NULL,
		 actor_user_id            INTEGER NOT NULL REFERENCES users(id),
		 created_at               TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 PRIMARY KEY(delivery_id,sequence),
		 UNIQUE(delivery_id,new_target_id),
		 CHECK(old_target_id<>new_target_id),
		 CHECK(old_harness_session_id<>new_harness_session_id)
		)`,
		`CREATE INDEX idx_agent_message_delivery_recoveries_project
		 ON agent_message_delivery_recoveries(project_id,created_at,delivery_id)`,
		`CREATE TRIGGER trg_agent_message_delivery_recovery_guard BEFORE INSERT ON agent_message_delivery_recoveries
		 WHEN NOT EXISTS(
		  SELECT 1 FROM agent_message_deliveries delivery
		  JOIN agent_messages message ON message.id=delivery.message_row_id
		  JOIN project_agents receiver ON receiver.id=message.to_agent_id
		  JOIN agent_message_targets old_target ON old_target.id=NEW.old_target_id
		  JOIN harness_sessions old_session ON old_session.id=NEW.old_harness_session_id
		  JOIN agent_message_targets new_target ON new_target.id=NEW.new_target_id
		  JOIN harness_sessions new_session ON new_session.id=NEW.new_harness_session_id
		  WHERE delivery.delivery_id=NEW.delivery_id AND receiver.project_id=NEW.project_id
		   AND delivery.instance=old_target.instance AND old_target.instance=new_target.instance
		   AND old_target.project_id=NEW.project_id AND new_target.project_id=NEW.project_id
		   AND old_target.address=message.to_address AND new_target.address=message.to_address
		   AND old_target.adapter='managed_harness' AND new_target.adapter='managed_harness'
		   AND old_target.target_kind='harness_session' AND new_target.target_kind='harness_session'
		   AND old_target.version=NEW.old_target_version AND new_target.version=NEW.new_target_version
		   AND old_session.project_id=NEW.project_id AND old_session.message_target_id=NEW.old_target_id
		   AND old_session.phase='stopped'
		   AND EXISTS(SELECT 1 FROM lifecycle_runtime_sessions old_binding
		              WHERE old_binding.session_id=NEW.old_harness_session_id
		               AND old_binding.runtime_id=NEW.old_runtime_id
		               AND old_binding.generation=NEW.old_session_generation)
		   AND new_session.project_id=NEW.project_id AND new_session.message_target_id=NEW.new_target_id
		   AND new_session.phase<>'stopped' AND new_session.management_mode='managed'
		   AND new_session.steer_mode='owned' AND new_session.advertised_inbox=1 AND new_session.advertised_status=1
		   AND EXISTS(SELECT 1 FROM lifecycle_runtime_sessions new_binding
		              JOIN lifecycle_runtimes runtime ON runtime.id=new_binding.runtime_id
		              WHERE new_binding.session_id=NEW.new_harness_session_id
		               AND new_binding.runtime_id=NEW.new_runtime_id
		               AND new_binding.generation=NEW.new_session_generation
		               AND runtime.generation=NEW.new_runtime_generation
		               AND runtime.project_id=NEW.project_id AND runtime.machine_id=new_session.host
		               AND runtime.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		   AND delivery.state='pending' AND delivery.attempt_count=0 AND delivery.consumer_fence=0
		   AND delivery.lease_until IS NULL AND delivery.handed_off_at IS NULL AND delivery.effective_level IS NULL
		   AND NOT EXISTS(SELECT 1 FROM agent_consumer_attempts attempt WHERE attempt.resource_id=delivery.delivery_id)
		   AND NEW.sequence=COALESCE((SELECT MAX(previous.sequence)+1 FROM agent_message_delivery_recoveries previous
		                              WHERE previous.delivery_id=NEW.delivery_id),1)
		   AND NEW.old_target_id=COALESCE((SELECT previous.new_target_id FROM agent_message_delivery_recoveries previous
		                                  WHERE previous.delivery_id=NEW.delivery_id ORDER BY previous.sequence DESC LIMIT 1),
		                                 delivery.primary_target_id)
		 ) BEGIN SELECT RAISE(ABORT,'invalid closed-target delivery recovery'); END`,
		`CREATE TRIGGER trg_agent_message_delivery_recovery_no_update BEFORE UPDATE ON agent_message_delivery_recoveries
		 BEGIN SELECT RAISE(ABORT,'delivery recovery history is immutable'); END`,
		`CREATE TRIGGER trg_agent_message_delivery_recovery_no_delete BEFORE DELETE ON agent_message_delivery_recoveries
		 WHEN EXISTS(SELECT 1 FROM agent_message_deliveries WHERE delivery_id=OLD.delivery_id)
		 BEGIN SELECT RAISE(ABORT,'delivery recovery history is immutable'); END`,
	}})
	// M180 / PAI-926: authenticated human messages address one exact
	// runtime-owned harness generation and retain immutable replay evidence.
	migrations = append(migrations, migration{version: 180})
	// M181 / PAI-952: opaque non-secret Codex account selection, distinct from
	// the closed chatgpt/api_key class label. Empty keeps legacy starts readable.
	migrations = append(migrations, migration{version: 181, steps: []string{
		`ALTER TABLE harness_sessions ADD COLUMN account_key TEXT NOT NULL DEFAULT ''`,
		`DROP TRIGGER IF EXISTS trg_harness_sessions_provenance_shape_insert`,
		`DROP TRIGGER IF EXISTS trg_harness_sessions_provenance_immutable`,
		`CREATE TRIGGER trg_harness_sessions_provenance_shape_insert BEFORE INSERT ON harness_sessions
			 WHEN NOT (
			  (NEW.workspace_identity='' AND NEW.workspace_path='' AND NEW.git_top_level='' AND NEW.git_branch=''
			   AND NEW.workspace_kind='unknown' AND NEW.workspace_mode='unknown' AND NEW.dispatch_profile_id=''
			   AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='' AND NEW.account_label='unknown'
			   AND NEW.account_key='')
			  OR
			  (length(NEW.workspace_identity)=64 AND NEW.workspace_identity NOT GLOB '*[^0-9a-f]*'
			   AND NEW.workspace_path<>'' AND instr(NEW.workspace_path,char(0))=0
			   AND instr(NEW.workspace_path,char(10))=0 AND instr(NEW.workspace_path,char(13))=0
			   AND NEW.workspace_kind IN ('directory','git_primary','git_worktree') AND NEW.workspace_mode IN ('exclusive','shared')
			   AND NEW.account_label IN ('unknown','chatgpt','api_key','claude_ai_max','claude_ai_pro','claude_ai_team','claude_ai_enterprise','console')
			   AND (NEW.account_key='' OR (length(NEW.account_key) BETWEEN 1 AND 128
			    AND NEW.account_key GLOB '[A-Za-z0-9]*' AND NEW.account_key NOT GLOB '*[^A-Za-z0-9._:-]*'
			    AND instr(NEW.account_key,'/')=0 AND instr(NEW.account_key,char(0))=0
			    AND instr(NEW.account_key,char(10))=0 AND instr(NEW.account_key,char(13))=0
			    AND NEW.account_key NOT IN ('unknown','chatgpt','api_key','claude_ai_max','claude_ai_pro','claude_ai_team','claude_ai_enterprise','console','local_probe')))
			   AND ((NEW.workspace_kind='directory' AND NEW.git_top_level='' AND NEW.git_branch='')
			    OR (NEW.workspace_kind IN ('git_primary','git_worktree') AND NEW.git_top_level<>'' AND NEW.git_branch<>''))
			   AND ((NEW.dispatch_profile_id='' AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='')
			    OR (NEW.dispatch_profile_id<>'' AND NEW.dispatch_profile_version<>'' AND NEW.dispatch_model<>'' AND NEW.dispatch_effort<>'')))
			 ) BEGIN SELECT RAISE(ABORT,'harness workspace provenance is invalid'); END`,
		`CREATE TRIGGER trg_harness_sessions_provenance_immutable BEFORE UPDATE OF
			 workspace_path,git_top_level,git_branch,workspace_identity,workspace_kind,workspace_mode,
			 dispatch_profile_id,dispatch_profile_version,dispatch_model,dispatch_effort,account_label,account_key
			 ON harness_sessions BEGIN SELECT RAISE(ABORT,'harness workspace provenance is immutable'); END`,
	}})
	// M182 / PAI-956: project-bound Aithema handover drafts and immutable
	// baseline batches mapped onto existing delivery attempts, lifecycle
	// intents and the owned readiness observations that authorize them.
	migrations = append(migrations, migration{version: 182, steps: []string{
		`CREATE TABLE baseline_batch_drafts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			revision INTEGER NOT NULL DEFAULT 1,
			status TEXT NOT NULL CHECK (status IN ('open','reviewing','closed')),
			baseline_ref TEXT NOT NULL,
			baseline_revision INTEGER NOT NULL,
			content_digest TEXT NOT NULL,
			revision_seal TEXT NOT NULL,
			stream_ref TEXT NOT NULL,
			imported_claimed_approved_by TEXT NOT NULL DEFAULT '',
			imported_claimed_approved_at TEXT NOT NULL DEFAULT '',
			imported_authenticity TEXT NOT NULL DEFAULT 'untrusted_imported_claim',
			bounded_content_json TEXT NOT NULL,
			selected_requirement_refs_json TEXT NOT NULL,
			selected_constraint_refs_json TEXT NOT NULL,
			execution_mode TEXT NOT NULL DEFAULT '' CHECK (execution_mode IN ('','manual','assisted','automatic')),
			worker_json TEXT NOT NULL DEFAULT '{}',
			created_by INTEGER NOT NULL REFERENCES users(id),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK (length(CAST(baseline_ref AS BLOB)) BETWEEN 1 AND 128),
			CHECK (length(CAST(content_digest AS BLOB)) = 71),
			CHECK (length(CAST(revision_seal AS BLOB)) = 71),
			CHECK (imported_authenticity = 'untrusted_imported_claim')
		)`,
		`CREATE UNIQUE INDEX idx_baseline_batch_open_draft ON baseline_batch_drafts(project_id) WHERE status IN ('open','reviewing')`,
		`CREATE TABLE baseline_batch_reviews (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			draft_id INTEGER NOT NULL REFERENCES baseline_batch_drafts(id),
			draft_revision INTEGER NOT NULL,
			binding_hash TEXT NOT NULL,
			execution_mode TEXT NOT NULL CHECK (execution_mode IN ('manual','assisted','automatic')),
			worker_json TEXT NOT NULL,
			selected_requirement_refs_json TEXT NOT NULL,
			human_user_id INTEGER NOT NULL REFERENCES users(id),
			session_credential_id TEXT NOT NULL,
			confirmed INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			invalidated_at TEXT,
			invalidate_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX idx_baseline_batch_reviews_draft ON baseline_batch_reviews(draft_id, draft_revision)`,
		`CREATE TABLE baseline_batch_batches (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			batch_key TEXT NOT NULL UNIQUE,
			draft_id INTEGER NOT NULL REFERENCES baseline_batch_drafts(id),
			draft_revision INTEGER NOT NULL,
			review_id INTEGER NOT NULL REFERENCES baseline_batch_reviews(id),
			baseline_ref TEXT NOT NULL,
			content_digest TEXT NOT NULL,
			revision_seal TEXT NOT NULL,
			execution_mode TEXT NOT NULL CHECK (execution_mode IN ('manual','assisted','automatic')),
			scope_json TEXT NOT NULL,
			worker_json TEXT NOT NULL,
			issue_id INTEGER NOT NULL REFERENCES issues(id),
			delivery_id INTEGER,
			attempt_id INTEGER,
			lifecycle_intent_id TEXT NOT NULL DEFAULT '',
			readiness_intent_id TEXT NOT NULL DEFAULT '',
			control_state TEXT NOT NULL DEFAULT 'started' CHECK (control_state IN ('started','paused','cancelled')),
			control_reason TEXT NOT NULL DEFAULT '',
			confirmation_json TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			imported_claimed_approved_by TEXT NOT NULL DEFAULT '',
			imported_claimed_approved_at TEXT NOT NULL DEFAULT '',
			imported_authenticity TEXT NOT NULL DEFAULT 'untrusted_imported_claim',
			stream_ref TEXT NOT NULL DEFAULT '',
			started_by INTEGER NOT NULL REFERENCES users(id),
			started_at TEXT NOT NULL,
			CHECK (imported_authenticity = 'untrusted_imported_claim'),
			UNIQUE(project_id, idempotency_key)
		)`,
		`CREATE INDEX idx_baseline_batch_batches_project ON baseline_batch_batches(project_id, control_state)`,
		// Human control actions on a started batch. The durable owned effect is a
		// lifecycle intent revision change or a harness session control row; this
		// ledger records which human asked for it, what it actually reached, and
		// makes a repeated request idempotent.
		`CREATE TABLE baseline_batch_controls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			batch_id INTEGER NOT NULL REFERENCES baseline_batch_batches(id),
			request_key TEXT NOT NULL,
			action TEXT NOT NULL CHECK (action IN ('pause','resume','cancel')),
			effect TEXT NOT NULL CHECK (effect IN ('harness_control','lifecycle_cancel','lifecycle_resubmit','manual_hold','manual_release','manual_cancel')),
			effect_ref TEXT NOT NULL DEFAULT '',
			actor_user_id INTEGER NOT NULL REFERENCES users(id),
			actor_credential_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(batch_id, request_key)
		)`,
		// Owned readiness evidence. One row per completed readiness intent; the
		// server binds project, runtime generation, account, profile, workspace and
		// baseline, and clamps the deadline to the ownership that produced it.
		`CREATE TABLE lifecycle_readiness_observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			intent_id TEXT NOT NULL UNIQUE REFERENCES lifecycle_intents(id),
			runtime_id TEXT NOT NULL,
			runtime_generation TEXT NOT NULL,
			account_label TEXT NOT NULL,
			account_key TEXT NOT NULL DEFAULT '',
			dispatch_profile_id TEXT NOT NULL,
			dispatch_profile_version TEXT NOT NULL,
			workspace_handle TEXT NOT NULL,
			workspace_identity TEXT NOT NULL,
			baseline_digest TEXT NOT NULL,
			contract_version TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('ready','needs_setup','unavailable')),
			next_action TEXT NOT NULL DEFAULT 'none',
			host_kind TEXT NOT NULL,
			checks_json TEXT NOT NULL,
			observed_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			recorded_at TEXT NOT NULL,
			CHECK (contract_version = 'inspr.readiness.v1'),
			CHECK (length(workspace_identity) = 64 AND workspace_identity NOT GLOB '*[^0-9a-f]*'),
			CHECK (length(CAST(baseline_digest AS BLOB)) = 71),
			CHECK (observed_at < expires_at)
		)`,
		`CREATE INDEX idx_lifecycle_readiness_current ON lifecycle_readiness_observations(
			project_id, baseline_digest, runtime_id, runtime_generation, expires_at)`,
		`CREATE TRIGGER trg_lifecycle_readiness_immutable BEFORE UPDATE ON lifecycle_readiness_observations
			BEGIN SELECT RAISE(ABORT,'readiness observation is immutable'); END`,
		`ALTER TABLE projects ADD COLUMN inspr_stream_enabled INTEGER NOT NULL DEFAULT 0
			CHECK (inspr_stream_enabled IN (0,1))`,
		`CREATE TABLE baseline_batch_forecasts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			batch_id INTEGER NOT NULL REFERENCES baseline_batch_batches(id),
			subject TEXT NOT NULL,
			percent REAL NOT NULL CHECK (percent >= 0 AND percent <= 100),
			eta_seconds INTEGER,
			kind TEXT NOT NULL CHECK (kind IN ('measured','worker_estimate','educated_guess')),
			basis TEXT NOT NULL,
			as_of TEXT NOT NULL,
			UNIQUE(batch_id, subject)
		)`,
		`CREATE TRIGGER trg_baseline_batch_batches_immutable_identity BEFORE UPDATE OF
			batch_key,draft_id,draft_revision,review_id,baseline_ref,content_digest,revision_seal,execution_mode,
			scope_json,worker_json,issue_id,delivery_id,attempt_id,idempotency_key,started_by,readiness_intent_id
			ON baseline_batch_batches BEGIN SELECT RAISE(ABORT,'baseline batch identity is immutable'); END`,
	}})
	// M183 / PAI-957: closed Pi selected-context account class, distinct from
	// Codex/Claude provider identity labels and from opaque account keys.
	migrations = append(migrations, migration{version: 183, steps: []string{
		`DROP TRIGGER IF EXISTS trg_harness_sessions_provenance_shape_insert`,
		`CREATE TRIGGER trg_harness_sessions_provenance_shape_insert BEFORE INSERT ON harness_sessions
			 WHEN NOT (
			  (NEW.workspace_identity='' AND NEW.workspace_path='' AND NEW.git_top_level='' AND NEW.git_branch=''
			   AND NEW.workspace_kind='unknown' AND NEW.workspace_mode='unknown' AND NEW.dispatch_profile_id=''
			   AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='' AND NEW.account_label='unknown'
			   AND NEW.account_key='')
			  OR
			  (length(NEW.workspace_identity)=64 AND NEW.workspace_identity NOT GLOB '*[^0-9a-f]*'
			   AND NEW.workspace_path<>'' AND instr(NEW.workspace_path,char(0))=0
			   AND instr(NEW.workspace_path,char(10))=0 AND instr(NEW.workspace_path,char(13))=0
			   AND NEW.workspace_kind IN ('directory','git_primary','git_worktree') AND NEW.workspace_mode IN ('exclusive','shared')
			   AND NEW.account_label IN ('unknown','chatgpt','api_key','claude_ai_max','claude_ai_pro','claude_ai_team','claude_ai_enterprise','console','pi_context')
			   AND (NEW.account_key='' OR (length(NEW.account_key) BETWEEN 1 AND 128
			    AND NEW.account_key GLOB '[A-Za-z0-9]*' AND NEW.account_key NOT GLOB '*[^A-Za-z0-9._:-]*'
			    AND instr(NEW.account_key,'/')=0 AND instr(NEW.account_key,char(0))=0
			    AND instr(NEW.account_key,char(10))=0 AND instr(NEW.account_key,char(13))=0
			    AND NEW.account_key NOT IN ('unknown','chatgpt','api_key','claude_ai_max','claude_ai_pro','claude_ai_team','claude_ai_enterprise','console','local_probe','pi_context')))
			   AND ((NEW.workspace_kind='directory' AND NEW.git_top_level='' AND NEW.git_branch='')
			    OR (NEW.workspace_kind IN ('git_primary','git_worktree') AND NEW.git_top_level<>'' AND NEW.git_branch<>''))
			   AND ((NEW.dispatch_profile_id='' AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='')
			    OR (NEW.dispatch_profile_id<>'' AND NEW.dispatch_profile_version<>'' AND NEW.dispatch_model<>'' AND NEW.dispatch_effort<>'')))
			 ) BEGIN SELECT RAISE(ABORT,'harness workspace provenance is invalid'); END`,
	}})
	// M184 / PAI-963: closed Cursor selected-identity account class, and live
	// consumer fence membership for owned Cursor targets. Historical M177/M183
	// SQL stays unchanged so existing v183 databases receive this upgrade.
	migrations = append(migrations, migration{version: 184, steps: []string{
		`DROP TRIGGER IF EXISTS trg_harness_sessions_provenance_shape_insert`,
		`CREATE TRIGGER trg_harness_sessions_provenance_shape_insert BEFORE INSERT ON harness_sessions
			 WHEN NOT (
			  (NEW.workspace_identity='' AND NEW.workspace_path='' AND NEW.git_top_level='' AND NEW.git_branch=''
			   AND NEW.workspace_kind='unknown' AND NEW.workspace_mode='unknown' AND NEW.dispatch_profile_id=''
			   AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='' AND NEW.account_label='unknown'
			   AND NEW.account_key='')
			  OR
			  (length(NEW.workspace_identity)=64 AND NEW.workspace_identity NOT GLOB '*[^0-9a-f]*'
			   AND NEW.workspace_path<>'' AND instr(NEW.workspace_path,char(0))=0
			   AND instr(NEW.workspace_path,char(10))=0 AND instr(NEW.workspace_path,char(13))=0
			   AND NEW.workspace_kind IN ('directory','git_primary','git_worktree') AND NEW.workspace_mode IN ('exclusive','shared')
			   AND NEW.account_label IN ('unknown','chatgpt','api_key','claude_ai_max','claude_ai_pro','claude_ai_team','claude_ai_enterprise','console','pi_context','cursor_context')
			   AND (NEW.account_key='' OR (length(NEW.account_key) BETWEEN 1 AND 128
			    AND NEW.account_key GLOB '[A-Za-z0-9]*' AND NEW.account_key NOT GLOB '*[^A-Za-z0-9._:-]*'
			    AND instr(NEW.account_key,'/')=0 AND instr(NEW.account_key,char(0))=0
			    AND instr(NEW.account_key,char(10))=0 AND instr(NEW.account_key,char(13))=0
			    AND NEW.account_key NOT IN ('unknown','chatgpt','api_key','claude_ai_max','claude_ai_pro','claude_ai_team','claude_ai_enterprise','console','local_probe','pi_context','cursor_context')))
			   AND ((NEW.workspace_kind='directory' AND NEW.git_top_level='' AND NEW.git_branch='')
			    OR (NEW.workspace_kind IN ('git_primary','git_worktree') AND NEW.git_top_level<>'' AND NEW.git_branch<>''))
			   AND ((NEW.dispatch_profile_id='' AND NEW.dispatch_profile_version='' AND NEW.dispatch_model='' AND NEW.dispatch_effort='')
			    OR (NEW.dispatch_profile_id<>'' AND NEW.dispatch_profile_version<>'' AND NEW.dispatch_model<>'' AND NEW.dispatch_effort<>'')))
			 ) BEGIN SELECT RAISE(ABORT,'harness workspace provenance is invalid'); END`,
		`DROP TRIGGER IF EXISTS consumer_delivery_fence`,
		`CREATE TRIGGER consumer_delivery_fence BEFORE UPDATE ON agent_message_deliveries WHEN EXISTS(SELECT 1 FROM agent_consumer_streams s JOIN agent_messages m ON m.to_agent_id=s.agent_id AND m.to_address=s.address WHERE s.kind='fallback' AND m.id=OLD.message_row_id AND (
 EXISTS(SELECT 1 FROM agent_message_targets t WHERE t.id=(CASE
	WHEN OLD.last_error_code='managed_target_unavailable' AND OLD.fallback_target_id IS NOT NULL
	THEN OLD.fallback_target_id
	WHEN OLD.requested_level='simple' AND OLD.primary_target_id IS NOT NULL AND OLD.fallback_target_id IS NOT NULL
	 AND (SELECT adapter FROM agent_message_targets policy_target WHERE policy_target.id=OLD.primary_target_id) IN ('agentd_codex','agentd_claude','agentd_pi','agentd_cursor')
	THEN OLD.fallback_target_id
	WHEN OLD.requested_level='steer' AND OLD.primary_target_id IS NOT NULL AND OLD.fallback_target_id IS NOT NULL
	 AND (SELECT maximum_level FROM agent_message_targets policy_target WHERE policy_target.id=OLD.primary_target_id)='simple'
	THEN OLD.fallback_target_id ELSE COALESCE(OLD.primary_target_id,OLD.fallback_target_id) END) AND t.adapter='codex')
 OR EXISTS(SELECT 1 FROM agent_consumer_attempts a WHERE a.stream_id=s.id AND a.resource_id=OLD.delivery_id))) AND NEW.consumer_fence<>OLD.consumer_fence+1 BEGIN SELECT RAISE(ABORT,'consumer ownership required'); END`,
	}})
	// M185 / PAI-968: release-specific go-live acceptance and email evidence.
	// Deployment completion stays distinct from acceptance. Cooperation
	// metadata is not copied here as authorization.
	migrations = append(migrations, migration{version: 185, steps: []string{
		`CREATE TABLE release_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			batch_id INTEGER NOT NULL REFERENCES baseline_batch_batches(id),
			release_ref TEXT NOT NULL UNIQUE,
			batch_key TEXT NOT NULL,
			baseline_ref TEXT NOT NULL,
			content_digest TEXT NOT NULL,
			revision_seal TEXT NOT NULL,
			artifact_digest TEXT NOT NULL,
			artifact_coordinate TEXT NOT NULL,
			version_scheme TEXT NOT NULL,
			release_channel TEXT NOT NULL,
			release_sequence INTEGER NOT NULL,
			version TEXT NOT NULL,
			commit_sha TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('built','acceptance_pending','accepted')),
			revision INTEGER NOT NULL DEFAULT 1,
			minted_by INTEGER NOT NULL REFERENCES users(id),
			minted_session_credential_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			CHECK (length(CAST(content_digest AS BLOB)) = 71),
			CHECK (length(CAST(revision_seal AS BLOB)) = 71),
			CHECK (length(CAST(artifact_digest AS BLOB)) = 71),
			CHECK (length(CAST(release_ref AS BLOB)) BETWEEN 1 AND 160),
			UNIQUE(project_id, batch_id)
		)`,
		`CREATE INDEX idx_release_records_project ON release_records(project_id, id DESC)`,
		`CREATE TRIGGER trg_release_records_immutable_identity BEFORE UPDATE OF
			project_id,batch_id,release_ref,batch_key,baseline_ref,content_digest,revision_seal,
			artifact_digest,artifact_coordinate,version_scheme,release_channel,release_sequence,
			version,commit_sha,minted_by,minted_session_credential_id,created_at
			ON release_records BEGIN SELECT RAISE(ABORT,'release identity is immutable'); END`,
		`CREATE TRIGGER trg_release_records_state_guard BEFORE UPDATE OF state ON release_records
			WHEN NOT (
				(OLD.state='built' AND NEW.state IN ('built','acceptance_pending','accepted')) OR
				(OLD.state='acceptance_pending' AND NEW.state IN ('acceptance_pending','accepted')) OR
				(OLD.state='accepted' AND NEW.state='accepted')
			)
			BEGIN SELECT RAISE(ABORT,'invalid release state transition'); END`,
		`CREATE TABLE release_acceptances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			release_id INTEGER NOT NULL REFERENCES release_records(id),
			project_id INTEGER NOT NULL REFERENCES projects(id),
			revision INTEGER NOT NULL DEFAULT 1,
			status TEXT NOT NULL CHECK (status IN ('pending','accepted')),
			operating_mode TEXT NOT NULL CHECK (operating_mode IN ('customer_operated','agency_supported','agency_operated')),
			agreement_ref TEXT NOT NULL DEFAULT '',
			disclosed_gaps_json TEXT NOT NULL DEFAULT '[]',
			required_party_refs_json TEXT NOT NULL DEFAULT '[]',
			delivery_party_ref TEXT NOT NULL DEFAULT '',
			operator_party_ref TEXT NOT NULL DEFAULT '',
			support_party_ref TEXT,
			preview_subject TEXT NOT NULL DEFAULT '',
			preview_body TEXT NOT NULL DEFAULT '',
			preview_revision INTEGER NOT NULL DEFAULT 0,
			configured_by INTEGER REFERENCES users(id),
			configured_session_credential_id TEXT NOT NULL DEFAULT '',
			configured_at TEXT,
			UNIQUE(release_id)
		)`,
		`CREATE TRIGGER trg_release_acceptances_no_unaccept BEFORE UPDATE OF status ON release_acceptances
			WHEN OLD.status='accepted' AND NEW.status<>'accepted'
			BEGIN SELECT RAISE(ABORT,'acceptance cannot be un-finalized'); END`,
		`CREATE TABLE acceptance_parties (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			acceptance_id INTEGER NOT NULL REFERENCES release_acceptances(id),
			acceptance_revision INTEGER NOT NULL,
			party_ref TEXT NOT NULL,
			party_kind TEXT NOT NULL CHECK (party_kind IN ('linked_user','manual_email')),
			user_id INTEGER REFERENCES users(id),
			email TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			roles_json TEXT NOT NULL,
			UNIQUE(acceptance_id, acceptance_revision, party_ref),
			CHECK (length(CAST(party_ref AS BLOB)) BETWEEN 1 AND 160),
			CHECK ((party_kind='linked_user' AND user_id IS NOT NULL) OR (party_kind='manual_email' AND user_id IS NULL))
		)`,
		`CREATE INDEX idx_acceptance_parties_user ON acceptance_parties(user_id, acceptance_id)`,
		`CREATE TABLE acceptance_confirmations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			acceptance_id INTEGER NOT NULL REFERENCES release_acceptances(id),
			release_id INTEGER NOT NULL REFERENCES release_records(id),
			acceptance_revision INTEGER NOT NULL,
			party_ref TEXT NOT NULL,
			decision TEXT NOT NULL CHECK (decision='accept'),
			source TEXT NOT NULL CHECK (source IN ('platform','external_email','standing_policy')),
			actor_user_id INTEGER NOT NULL REFERENCES users(id),
			session_credential_id TEXT NOT NULL,
			attestation TEXT NOT NULL DEFAULT '',
			confirmed_at TEXT NOT NULL,
			UNIQUE(acceptance_id, acceptance_revision, party_ref)
		)`,
		`CREATE TRIGGER trg_acceptance_confirmations_immutable BEFORE UPDATE ON acceptance_confirmations
			BEGIN SELECT RAISE(ABORT,'acceptance confirmation is immutable'); END`,
		`CREATE TABLE acceptance_email_evidence (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			acceptance_id INTEGER NOT NULL REFERENCES release_acceptances(id),
			release_id INTEGER NOT NULL REFERENCES release_records(id),
			acceptance_revision INTEGER NOT NULL,
			message_ref TEXT NOT NULL UNIQUE,
			request_key TEXT NOT NULL,
			recipient_party_refs_json TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('pending','sent','failed')),
			source TEXT NOT NULL CHECK (source IN ('platform_send','external_manual')),
			recorded_at TEXT NOT NULL,
			sent_at TEXT,
			actor_user_id INTEGER NOT NULL REFERENCES users(id),
			session_credential_id TEXT NOT NULL,
			attestation TEXT NOT NULL DEFAULT '',
			body_sha256 TEXT NOT NULL,
			raw_message BLOB,
			UNIQUE(acceptance_id, request_key)
		)`,
		`CREATE INDEX idx_acceptance_email_evidence_acceptance ON acceptance_email_evidence(acceptance_id, acceptance_revision)`,
		`CREATE TABLE acceptance_mail_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			evidence_id INTEGER NOT NULL UNIQUE REFERENCES acceptance_email_evidence(id),
			request_key TEXT NOT NULL UNIQUE,
			state TEXT NOT NULL CHECK (state IN ('queued','sending','sent','failed')),
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_error_class TEXT NOT NULL DEFAULT '',
			authorized_by INTEGER NOT NULL REFERENCES users(id),
			session_credential_id TEXT NOT NULL,
			preview_revision INTEGER NOT NULL,
			lease_until TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX idx_acceptance_mail_outbox_dispatch ON acceptance_mail_outbox(state, id)`,
		`CREATE TABLE acceptance_standing_policies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			policy_ref TEXT NOT NULL UNIQUE,
			operating_mode TEXT NOT NULL CHECK (operating_mode='customer_operated'),
			approved_by INTEGER NOT NULL REFERENCES users(id),
			session_credential_id TEXT NOT NULL,
			content_digest TEXT NOT NULL,
			revision_seal TEXT NOT NULL,
			target_ref TEXT NOT NULL DEFAULT '',
			parties_json TEXT NOT NULL,
			model_ref TEXT NOT NULL DEFAULT '',
			agreement_ref TEXT NOT NULL DEFAULT '',
			gaps_json TEXT NOT NULL DEFAULT '[]',
			release_channel TEXT NOT NULL DEFAULT '',
			artifact_digest TEXT NOT NULL DEFAULT '',
			bounded_use TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			revoked_at TEXT,
			created_at TEXT NOT NULL,
			CHECK (length(CAST(content_digest AS BLOB)) = 71),
			CHECK (length(CAST(revision_seal AS BLOB)) = 71)
		)`,
		`CREATE INDEX idx_acceptance_standing_policies_project ON acceptance_standing_policies(project_id, id DESC)`,
	}})
	// M186 / PAI-968: explicit human deployment-target binding for standing
	// policy. Existence of an environment or Pharos owner registration is
	// not selection. Identity is a provenance digest, not a display name.
	migrations = append(migrations, migration{version: 186, steps: []string{
		`CREATE TABLE acceptance_target_bindings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			release_id INTEGER NOT NULL UNIQUE REFERENCES release_records(id),
			kind TEXT NOT NULL CHECK (kind IN ('pharos_owner','project_environment')),
			target_ref TEXT NOT NULL CHECK (length(CAST(target_ref AS BLOB))=71 AND substr(target_ref,1,7)='sha256:'),
			registration_id INTEGER,
			environment_id INTEGER,
			delivery_id INTEGER,
			attempt_id INTEGER,
			environment_symbol TEXT NOT NULL DEFAULT '',
			workflow_symbol TEXT NOT NULL DEFAULT '',
			source_created_at TEXT NOT NULL DEFAULT '',
			bound_by INTEGER NOT NULL REFERENCES users(id),
			session_credential_id TEXT NOT NULL,
			bound_at TEXT NOT NULL,
			binding_revision INTEGER NOT NULL DEFAULT 1 CHECK (binding_revision>=1),
			CHECK ((kind='pharos_owner' AND registration_id IS NOT NULL AND registration_id>0 AND environment_id IS NULL) OR
			       (kind='project_environment' AND environment_id IS NOT NULL AND environment_id>0 AND registration_id IS NULL))
		)`,
		`CREATE INDEX idx_acceptance_target_bindings_project ON acceptance_target_bindings(project_id, release_id)`,
	}})
	// M191 / PAI-978: deliberate human one-shot launch grants and the closed
	// additive Pharos candidate/admission/consume sidecar. Existing external
	// stage v1/v2 tables and bytes are unchanged.
	launchAuthorityMigration := migration{version: 191, steps: []string{
		`ALTER TABLE external_stage_reporter_registrations ADD COLUMN target_ref TEXT NOT NULL DEFAULT ''
			CHECK(target_ref='' OR (length(CAST(target_ref AS BLOB))=71 AND substr(target_ref,1,7)='sha256:'))`,
		`CREATE TRIGGER trg_external_stage_registration_target_immutable BEFORE UPDATE OF target_ref
			ON external_stage_reporter_registrations BEGIN SELECT RAISE(ABORT,'external stage registration target is immutable'); END`,
		`ALTER TABLE baseline_batch_drafts ADD COLUMN delegated_launch_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE baseline_batch_reviews ADD COLUMN delegated_launch_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE baseline_batch_batches ADD COLUMN delegated_launch_json TEXT NOT NULL DEFAULT ''`,
		`CREATE TRIGGER trg_baseline_batch_closed_draft_launch_immutable BEFORE UPDATE OF delegated_launch_json
			ON baseline_batch_drafts WHEN OLD.status='closed' OR NEW.status='closed'
			BEGIN SELECT RAISE(ABORT,'closed draft launch selection is immutable'); END`,
		`CREATE TRIGGER trg_baseline_batch_review_launch_immutable BEFORE UPDATE OF delegated_launch_json
			ON baseline_batch_reviews BEGIN SELECT RAISE(ABORT,'reviewed launch selection is immutable'); END`,
		`CREATE TRIGGER trg_baseline_batch_launch_selection_immutable BEFORE UPDATE OF delegated_launch_json
			ON baseline_batch_batches BEGIN SELECT RAISE(ABORT,'batch launch selection is immutable'); END`,
		`CREATE TABLE baseline_batch_launch_grants (
			grant_id TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("grant_id") + `),
			revision INTEGER NOT NULL CHECK(revision=1),
			grant_digest BLOB NOT NULL UNIQUE CHECK(length(grant_digest)=32),
			project_id INTEGER NOT NULL REFERENCES projects(id),
			draft_id INTEGER NOT NULL REFERENCES baseline_batch_drafts(id),
			draft_revision INTEGER NOT NULL,
			review_id INTEGER NOT NULL REFERENCES baseline_batch_reviews(id),
			batch_id INTEGER NOT NULL UNIQUE REFERENCES baseline_batch_batches(id),
			delivery_id INTEGER NOT NULL REFERENCES deliveries(id),
			attempt_id INTEGER NOT NULL REFERENCES delivery_attempts(id),
			attempt_number INTEGER NOT NULL CHECK(attempt_number>0),
			plan_revision INTEGER NOT NULL CHECK(plan_revision>0),
			baseline_ref TEXT NOT NULL,
			content_digest TEXT NOT NULL CHECK(length(CAST(content_digest AS BLOB))=71),
			revision_seal TEXT NOT NULL CHECK(length(CAST(revision_seal AS BLOB))=71),
			scope_digest BLOB NOT NULL CHECK(length(scope_digest)=32),
			worker_digest BLOB NOT NULL CHECK(length(worker_digest)=32),
			target_ref TEXT NOT NULL CHECK(length(CAST(target_ref AS BLOB))=71 AND substr(target_ref,1,7)='sha256:'),
			workflow_symbol TEXT NOT NULL CHECK(workflow_symbol='deploy-production'),
			environment_symbol TEXT NOT NULL,
			max_launches INTEGER NOT NULL CHECK(max_launches=1),
			human_user_id INTEGER NOT NULL REFERENCES users(id),
			human_session_credential_id TEXT NOT NULL,
			issued_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			revoked_at TEXT,
			revoked_by INTEGER REFERENCES users(id),
			revoked_session_credential_id TEXT,
			CHECK(issued_at<expires_at),
			CHECK((revoked_at IS NULL AND revoked_by IS NULL AND revoked_session_credential_id IS NULL) OR
			      (revoked_at IS NOT NULL AND revoked_by IS NOT NULL AND revoked_session_credential_id IS NOT NULL))
		)`,
		`CREATE INDEX idx_baseline_batch_launch_grants_project ON baseline_batch_launch_grants(project_id,batch_id)`,
		`CREATE TRIGGER trg_baseline_batch_launch_grants_identity_immutable BEFORE UPDATE OF
			grant_id,revision,grant_digest,project_id,draft_id,draft_revision,review_id,batch_id,delivery_id,attempt_id,
			attempt_number,plan_revision,baseline_ref,content_digest,revision_seal,scope_digest,worker_digest,target_ref,
			workflow_symbol,environment_symbol,max_launches,human_user_id,human_session_credential_id,issued_at,expires_at
			ON baseline_batch_launch_grants BEGIN SELECT RAISE(ABORT,'launch grant identity is immutable'); END`,
		`CREATE TRIGGER trg_baseline_batch_launch_grants_revoke_guard BEFORE UPDATE OF revoked_at,revoked_by,revoked_session_credential_id
			ON baseline_batch_launch_grants WHEN OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
			BEGIN SELECT RAISE(ABORT,'launch grant revocation is immutable'); END`,
		`CREATE TABLE baseline_batch_launch_grant_revocations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			grant_id TEXT NOT NULL UNIQUE REFERENCES baseline_batch_launch_grants(grant_id),
			batch_id INTEGER NOT NULL UNIQUE REFERENCES baseline_batch_batches(id),
			actor_user_id INTEGER NOT NULL REFERENCES users(id),
			actor_session_credential_id TEXT NOT NULL,
			idempotency_digest BLOB NOT NULL CHECK(length(idempotency_digest)=32),
			revoked_at TEXT NOT NULL
		)`,
		`CREATE TRIGGER trg_baseline_batch_launch_grant_revocations_no_update BEFORE UPDATE ON baseline_batch_launch_grant_revocations
			BEGIN SELECT RAISE(ABORT,'launch grant revocation is immutable'); END`,
		`CREATE TABLE external_stage_launch_admissions (
			admission_id TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("admission_id") + `),
			admission_digest BLOB NOT NULL UNIQUE CHECK(length(admission_digest)=32),
			grant_id TEXT NOT NULL UNIQUE REFERENCES baseline_batch_launch_grants(grant_id),
			handoff_row_id INTEGER NOT NULL UNIQUE REFERENCES external_stage_handoffs(id),
			handoff_id TEXT NOT NULL UNIQUE,
			credential_epoch INTEGER NOT NULL CHECK(credential_epoch>0),
			candidate_digest BLOB NOT NULL CHECK(length(candidate_digest)=32),
			candidate_idempotency_digest BLOB NOT NULL CHECK(length(candidate_idempotency_digest)=32),
			target_ref TEXT NOT NULL,
			workflow_symbol TEXT NOT NULL CHECK(workflow_symbol='deploy-production'),
			environment_symbol TEXT NOT NULL,
			artifact_json TEXT NOT NULL,
			reviewed_plan_digest TEXT NOT NULL,
			operation_binding_digest TEXT NOT NULL,
			stage_key TEXT NOT NULL CHECK(stage_key='deployment'),
			attempt_number INTEGER NOT NULL CHECK(attempt_number>0),
			plan_revision INTEGER NOT NULL CHECK(plan_revision>0),
			execution_number INTEGER NOT NULL CHECK(execution_number>0),
			authority_epoch INTEGER NOT NULL CHECK(authority_epoch>0),
			plan_digest TEXT NOT NULL,
			predecessor_digest TEXT NOT NULL,
			context_digest TEXT NOT NULL,
			max_launches INTEGER NOT NULL CHECK(max_launches=1),
			issued_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('issued','consumed')),
			response_bytes BLOB NOT NULL,
			consume_request_digest BLOB,
			consume_idempotency_digest BLOB,
			receipt_bytes BLOB,
			consumed_at TEXT,
			CHECK(issued_at<expires_at),
			CHECK((state='issued' AND consume_request_digest IS NULL AND consume_idempotency_digest IS NULL AND receipt_bytes IS NULL AND consumed_at IS NULL) OR
			      (state='consumed' AND length(consume_request_digest)=32 AND length(consume_idempotency_digest)=32 AND receipt_bytes IS NOT NULL AND consumed_at IS NOT NULL))
		)`,
		`CREATE TRIGGER trg_external_stage_launch_admissions_identity_immutable BEFORE UPDATE OF
			admission_id,admission_digest,grant_id,handoff_row_id,handoff_id,credential_epoch,candidate_digest,
			candidate_idempotency_digest,target_ref,workflow_symbol,environment_symbol,artifact_json,
			reviewed_plan_digest,operation_binding_digest,stage_key,attempt_number,plan_revision,execution_number,
			authority_epoch,plan_digest,predecessor_digest,context_digest,max_launches,issued_at,expires_at,response_bytes
			ON external_stage_launch_admissions BEGIN SELECT RAISE(ABORT,'launch admission identity is immutable'); END`,
		`CREATE TRIGGER trg_external_stage_launch_admissions_consume_guard BEFORE UPDATE OF
			state,consume_request_digest,consume_idempotency_digest,receipt_bytes,consumed_at ON external_stage_launch_admissions
			WHEN OLD.state<>'issued' OR NEW.state<>'consumed'
			BEGIN SELECT RAISE(ABORT,'launch admission consume is one-shot'); END`,
	}}

	// M187 / PAI-979: admit the INSPR calendar v2 scheme (`inspr-calendar-v2`,
	// UTC `YYMMDDhhmmss.0.0`) in stored Pharos v2 evidence. SQLite cannot widen a
	// CHECK in place, so the M175 table is rebuilt byte-for-byte with the
	// extended constraint; rows, the self-referencing deployment binding, the
	// insert guard and the immutability triggers are preserved unchanged.
	migrations = append(migrations, migration{version: 187, steps: []string{
		`PRAGMA foreign_keys=OFF`,
		`ALTER TABLE external_stage_pharos_evidence_v2 RENAME TO external_stage_pharos_evidence_v2_old187`,
		`DROP TRIGGER IF EXISTS trg_external_stage_pharos_evidence_v2_insert_guard`,
		`DROP TRIGGER IF EXISTS trg_external_stage_pharos_evidence_v2_no_update`,
		`DROP TRIGGER IF EXISTS trg_external_stage_pharos_evidence_v2_no_delete`,
		`CREATE TABLE external_stage_pharos_evidence_v2 (
		 report_event_id                 INTEGER PRIMARY KEY REFERENCES external_stage_pharos_evidence(report_event_id),
		 version_scheme                  TEXT NOT NULL CHECK(version_scheme IN ('legacy','inspr-calendar-v1','inspr-calendar-v2')),
		 release_channel                 TEXT NOT NULL CHECK(length(CAST(release_channel AS BLOB)) BETWEEN 1 AND 64 AND
		  release_channel GLOB '[a-z]*' AND release_channel NOT GLOB '*[^a-z0-9._-]*'),
		 release_sequence                INTEGER NOT NULL CHECK(release_sequence>=0),
		 release_manifest_coordinate     TEXT NOT NULL CHECK(length(CAST(release_manifest_coordinate AS BLOB)) BETWEEN 3 AND 255 AND
		  instr(release_manifest_coordinate,':') BETWEEN 2 AND 65 AND
		  substr(release_manifest_coordinate,1,1) GLOB '[a-z]' AND
		  substr(release_manifest_coordinate,1,instr(release_manifest_coordinate,':')-1) NOT GLOB '*[^a-z0-9._-]*' AND
		  substr(release_manifest_coordinate,instr(release_manifest_coordinate,':')+1,1) GLOB '[A-Za-z0-9]' AND
		  substr(release_manifest_coordinate,instr(release_manifest_coordinate,':')+1) NOT GLOB '*[^A-Za-z0-9._/@:+-]*'),
		 release_manifest_digest         BLOB NOT NULL CHECK(typeof(release_manifest_digest)='blob' AND length(release_manifest_digest)=32),
		 bound_deployment_report_event_id INTEGER REFERENCES external_stage_pharos_evidence_v2(report_event_id)
		) WITHOUT ROWID`,
		`INSERT INTO external_stage_pharos_evidence_v2(report_event_id,version_scheme,release_channel,release_sequence,
		  release_manifest_coordinate,release_manifest_digest,bound_deployment_report_event_id)
		 SELECT report_event_id,version_scheme,release_channel,release_sequence,
		  release_manifest_coordinate,release_manifest_digest,bound_deployment_report_event_id
		 FROM external_stage_pharos_evidence_v2_old187 ORDER BY report_event_id`,
		`DROP TABLE external_stage_pharos_evidence_v2_old187`,
		`CREATE TRIGGER trg_external_stage_pharos_evidence_v2_insert_guard
		 BEFORE INSERT ON external_stage_pharos_evidence_v2
		 WHEN NOT EXISTS(
		  SELECT 1 FROM external_stage_pharos_evidence evidence
		  JOIN external_stage_report_events report ON report.id=evidence.report_event_id
		  JOIN external_stage_handoffs handoff ON handoff.id=report.handoff_row_id
		  WHERE evidence.report_event_id=NEW.report_event_id AND handoff.reporter_class='pharos'
		   AND ((evidence.evidence_kind='deployment' AND NEW.bound_deployment_report_event_id IS NULL) OR
		        (evidence.evidence_kind='verification' AND NEW.bound_deployment_report_event_id IS NOT NULL AND EXISTS(
		          SELECT 1 FROM external_stage_pharos_evidence deployment
		          JOIN external_stage_pharos_evidence_v2 deployment_v2 ON deployment_v2.report_event_id=deployment.report_event_id
		          JOIN external_stage_report_events deployment_report ON deployment_report.id=deployment.report_event_id
		          JOIN external_stage_handoffs deployment_handoff ON deployment_handoff.id=deployment_report.handoff_row_id
		          WHERE deployment.report_event_id=NEW.bound_deployment_report_event_id
		           AND deployment.evidence_kind='deployment' AND deployment.result='succeeded'
		           AND deployment_handoff.delivery_id=handoff.delivery_id AND deployment_handoff.attempt_id=handoff.attempt_id
		           AND deployment_handoff.stage_key='deployment' AND deployment_handoff.lifecycle_state='succeeded'
		           AND deployment.environment_symbol=evidence.environment_symbol
		           AND deployment.artifact_version=evidence.artifact_version
		           AND deployment.artifact_digest=evidence.artifact_digest
		           AND deployment.commit_digest=evidence.commit_digest
		           AND deployment_v2.version_scheme=NEW.version_scheme
		           AND deployment_v2.release_channel=NEW.release_channel
		           AND deployment_v2.release_sequence=NEW.release_sequence
		           AND deployment_v2.release_manifest_coordinate=NEW.release_manifest_coordinate
		           AND deployment_v2.release_manifest_digest=NEW.release_manifest_digest))))
		 BEGIN SELECT RAISE(ABORT,'invalid external stage Pharos v2 evidence binding'); END`,
		`CREATE TRIGGER trg_external_stage_pharos_evidence_v2_no_update BEFORE UPDATE ON external_stage_pharos_evidence_v2
		 BEGIN SELECT RAISE(ABORT,'external stage v2 evidence is immutable'); END`,
		`CREATE TRIGGER trg_external_stage_pharos_evidence_v2_no_delete BEFORE DELETE ON external_stage_pharos_evidence_v2
		 BEGIN SELECT RAISE(ABORT,'external stage v2 evidence is immutable'); END`,
		`PRAGMA foreign_keys=ON`,
	}})
	// PAI-991: atomic offer documents, immutable numbers and optimistic revisions.
	migrations = append(migrations, migration{version: 188, steps: []string{
		`ALTER TABLE customers ADD COLUMN customer_no TEXT`,
		`CREATE UNIQUE INDEX idx_customers_customer_no ON customers(customer_no) WHERE customer_no IS NOT NULL`,
		`CREATE TABLE offer_sequences(key TEXT PRIMARY KEY,value INTEGER NOT NULL CHECK(value>0))`,
		`CREATE TABLE offers(id INTEGER PRIMARY KEY AUTOINCREMENT,offer_no TEXT NOT NULL UNIQUE,customer_id INTEGER NOT NULL REFERENCES customers(id),status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','sent','accepted','declined','expired')),revision INTEGER NOT NULL DEFAULT 1,document TEXT NOT NULL CHECK(json_valid(document)),created_by INTEGER REFERENCES users(id),created_at TEXT NOT NULL DEFAULT (datetime('now')),updated_at TEXT NOT NULL DEFAULT (datetime('now')),sent_at TEXT)`,
		`CREATE INDEX idx_offers_customer ON offers(customer_id,id)`,
		`CREATE TRIGGER offers_immutable_number BEFORE UPDATE OF offer_no,customer_id ON offers BEGIN SELECT RAISE(ABORT,'offer identity is immutable'); END`,
		`CREATE TRIGGER offers_frozen_document BEFORE UPDATE OF document ON offers WHEN OLD.status!='draft' BEGIN SELECT RAISE(ABORT,'finalized offer is immutable'); END`,
		`CREATE TRIGGER customer_no_immutable BEFORE UPDATE OF customer_no ON customers WHEN OLD.customer_no IS NOT NULL AND NEW.customer_no IS NOT OLD.customer_no BEGIN SELECT RAISE(ABORT,'customer number is immutable'); END`,
	}})
	// PAI-991: capability links and an atomic, immutable acceptance receipt.
	migrations = append(migrations, migration{version: 189, steps: []string{
		`ALTER TABLE offers ADD COLUMN public_token TEXT`,
		`CREATE UNIQUE INDEX idx_offers_public_token ON offers(public_token) WHERE public_token IS NOT NULL`,
		`ALTER TABLE offers ADD COLUMN accepted_at TEXT`,
		`ALTER TABLE offers ADD COLUMN accepted_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE offers ADD COLUMN accepted_company TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE offers ADD COLUMN accepted_note TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE offer_acceptance_audit(offer_id INTEGER PRIMARY KEY REFERENCES offers(id),accepted_at TEXT NOT NULL,accepted_name TEXT NOT NULL,accepted_company TEXT NOT NULL,accepted_note TEXT NOT NULL,ip TEXT NOT NULL,user_agent TEXT NOT NULL,document_sha256 TEXT NOT NULL,offer_revision INTEGER NOT NULL)`,
		`CREATE TRIGGER offer_acceptance_audit_no_update BEFORE UPDATE ON offer_acceptance_audit BEGIN SELECT RAISE(ABORT,'acceptance audit is immutable'); END`,
		`CREATE TRIGGER offer_acceptance_audit_no_delete BEFORE DELETE ON offer_acceptance_audit BEGIN SELECT RAISE(ABORT,'acceptance audit is immutable'); END`,
		`CREATE TRIGGER offers_acceptance_immutable BEFORE UPDATE ON offers WHEN OLD.status='accepted' BEGIN SELECT RAISE(ABORT,'accepted offer is immutable'); END`,
	}})
	// PAI-991: permit one explicit legacy-to-monthly number conversion while
	// every related offer is still a draft. No existing number is auto-rewritten.
	migrations = append(migrations, migration{version: 190, steps: []string{
		`DROP TRIGGER customer_no_immutable`,
		`CREATE TRIGGER customer_no_immutable BEFORE UPDATE OF customer_no ON customers
		 WHEN OLD.customer_no IS NOT NULL AND NEW.customer_no IS NOT OLD.customer_no
		 AND (NEW.customer_no IS NULL OR NOT (
		   OLD.customer_no GLOB 'K[0-9][0-9]-[0-9][0-9][0-9]*'
		   AND substr(OLD.customer_no,5) NOT GLOB '*[^0-9]*'
		   AND length(NEW.customer_no)>=6 AND substr(NEW.customer_no,1,1)='K'
		   AND substr(NEW.customer_no,2) NOT GLOB '*[^0-9]*'
		   AND substr(NEW.customer_no,4,2) BETWEEN '01' AND '12'
		   AND substr(NEW.customer_no,6,1) BETWEEN '1' AND '9'
		   AND NOT EXISTS(SELECT 1 FROM offers WHERE customer_id=OLD.id AND status!='draft')
		 )) BEGIN SELECT RAISE(ABORT,'customer number is immutable'); END`,
	}})
	// M192 / PAI-986: durable finish-current-work retirement and admission fence.
	// Both held migrations remain additive after published main migrations 187–190.
	migrations = append(migrations, launchAuthorityMigration, migration{version: 192})
	// PAI-1012: one durable confirmation per acceptance; legacy offers are not queued.
	migrations = append(migrations, migration{version: 193, steps: []string{
		`CREATE TABLE offer_confirmations (
		 offer_id INTEGER PRIMARY KEY REFERENCES offer_acceptance_audit(offer_id),
		 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','rendering','sending','sent','failed','uncertain')),
		 attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT NOT NULL,
		 updated_at TEXT NOT NULL, sent_at TEXT, error_class TEXT NOT NULL DEFAULT '',
		 public_url TEXT NOT NULL, message_id TEXT NOT NULL UNIQUE, pdf BLOB,
		 CHECK(pdf IS NULL OR length(pdf)<=20971520))`,
		`CREATE INDEX idx_offer_confirmations_due ON offer_confirmations(state,next_attempt_at)`,
		`CREATE TABLE offer_visibility(offer_id INTEGER PRIMARY KEY REFERENCES offers(id),deleted_at TEXT,deleted_by INTEGER REFERENCES users(id))`,
	}})
	// M194 / PAI-1015: preserve the Aithema baseline revision in the immutable
	// batch snapshot; recover historical values only from seal-proven snapshots.
	migrations = append(migrations, migration{version: 194})
	// M195 / PAI-1018: one narrowly bound machine-notifier credential. The
	// credential remains an API key for hashing, expiry, usage accounting, and
	// revocation, while its kind and immutable binding prevent it from inheriting
	// the owning administrator's ordinary routes.
	migrations = append(migrations, migration{version: 195, steps: []string{
		`ALTER TABLE api_keys ADD COLUMN credential_kind TEXT NOT NULL DEFAULT 'general'
		 CHECK(credential_kind IN ('general','machine_notifier'))`,
		`CREATE TRIGGER trg_api_keys_credential_kind_immutable BEFORE UPDATE OF credential_kind ON api_keys
		 BEGIN SELECT RAISE(ABORT,'api key credential kind is immutable'); END`,
		`CREATE TABLE machine_notifier_bindings (
		 api_key_id       INTEGER PRIMARY KEY REFERENCES api_keys(id),
		 instance         TEXT NOT NULL CHECK(length(CAST(instance AS BLOB)) BETWEEN 1 AND 64),
		 project_id       INTEGER NOT NULL REFERENCES projects(id),
		 sender_agent_id  INTEGER NOT NULL REFERENCES project_agents(id),
		 receiver_agent_id INTEGER NOT NULL REFERENCES project_agents(id),
		 address          TEXT NOT NULL CHECK(length(CAST(address AS BLOB)) BETWEEN 3 AND 129),
		 target_id        TEXT NOT NULL REFERENCES agent_message_targets(id),
		 target_version   INTEGER NOT NULL CHECK(target_version>0),
		 created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		 CHECK(sender_agent_id<>receiver_agent_id)
		)`,
		`CREATE INDEX idx_machine_notifier_project ON machine_notifier_bindings(project_id,api_key_id)`,
		`CREATE TRIGGER trg_machine_notifier_binding_guard BEFORE INSERT ON machine_notifier_bindings
		 WHEN NOT EXISTS(
		  SELECT 1 FROM api_keys ak JOIN users u ON u.id=ak.user_id
		  JOIN projects p ON p.id=NEW.project_id
		  JOIN project_agents sender ON sender.id=NEW.sender_agent_id AND sender.project_id=NEW.project_id
		  JOIN project_agents receiver ON receiver.id=NEW.receiver_agent_id AND receiver.project_id=NEW.project_id
		  JOIN agent_message_allowlist allow ON allow.sender_agent_id=sender.id AND allow.receiver_agent_id=receiver.id
		  JOIN agent_message_targets target ON target.id=NEW.target_id
		  WHERE ak.id=NEW.api_key_id AND ak.credential_kind='machine_notifier' AND ak.disabled_at IS NULL
		   AND (ak.expires_at IS NULL OR datetime(ak.expires_at)>datetime('now')) AND u.status='active' AND p.status='active'
		   AND target.instance=NEW.instance AND target.project_id=NEW.project_id AND target.address=NEW.address
		   AND target.version=NEW.target_version AND target.role='primary' AND target.enabled=1
		   AND target.target_kind='https_webhook' AND target.maximum_level='simple'
		 ) BEGIN SELECT RAISE(ABORT,'invalid machine notifier binding'); END`,
		`CREATE TRIGGER trg_machine_notifier_binding_no_update BEFORE UPDATE ON machine_notifier_bindings
		 BEGIN SELECT RAISE(ABORT,'machine notifier binding is immutable'); END`,
		`ALTER TABLE agent_messages ADD COLUMN machine_notifier_api_key_id INTEGER REFERENCES api_keys(id)`,
		`CREATE INDEX idx_agent_messages_machine_notifier
		 ON agent_messages(machine_notifier_api_key_id,message_id) WHERE machine_notifier_api_key_id IS NOT NULL`,
		`CREATE TRIGGER trg_machine_notifier_no_target_recovery
		 BEFORE INSERT ON agent_message_delivery_recoveries
		 WHEN EXISTS(SELECT 1 FROM agent_message_deliveries d JOIN agent_messages m ON m.id=d.message_row_id
		             WHERE d.delivery_id=NEW.delivery_id AND m.machine_notifier_api_key_id IS NOT NULL)
		 BEGIN SELECT RAISE(ABORT,'machine notifier delivery target is immutable'); END`,
	}})

	// M196 / PAI-1022: align the durable handoff guard with the frozen v1
	// fixtures. A reporter may accept a handoff and immediately commit its
	// already-observed terminal evidence; all report, audit, latest-projection,
	// sequence, epoch, and timestamp causal checks remain mandatory.
	migrations = append(migrations, migration{version: 196, steps: []string{
		`DROP TRIGGER trg_external_stage_handoff_causal_update_guard`,
		`CREATE TRIGGER trg_external_stage_handoff_causal_update_guard
			 BEFORE UPDATE ON external_stage_handoffs
			 WHEN NOT (
			  (NEW.credential_epoch=OLD.credential_epoch+1 AND NEW.secret_digest IS NOT OLD.secret_digest AND
			   NEW.lifecycle_state=OLD.lifecycle_state AND NEW.last_sequence=OLD.last_sequence AND
			   NEW.accepted_at IS OLD.accepted_at AND NEW.terminal_at IS OLD.terminal_at AND NEW.revoked_at IS OLD.revoked_at AND
			   EXISTS(SELECT 1 FROM external_stage_operation_events operation
			    JOIN external_stage_audit_events audit ON audit.operation_event_id=operation.id
			    WHERE operation.handoff_row_id=OLD.id AND operation.credential_epoch=NEW.credential_epoch
			     AND operation.operation_kind=CASE WHEN OLD.credential_epoch=0 THEN 'secret_minted' ELSE 'secret_rotated' END
			     AND audit.event_kind=CASE WHEN OLD.credential_epoch=0 THEN 'secret_minted' ELSE 'secret_rotated' END
			     AND audit.credential_epoch=NEW.credential_epoch AND audit.outcome='committed')) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   NEW.lifecycle_state=OLD.lifecycle_state AND NEW.last_sequence=OLD.last_sequence AND
			   NEW.accepted_at IS OLD.accepted_at AND NEW.terminal_at IS OLD.terminal_at AND OLD.revoked_at IS NULL AND
			   EXISTS(SELECT 1 FROM external_stage_operation_events operation
			    JOIN external_stage_audit_events audit ON audit.operation_event_id=operation.id
			    WHERE operation.handoff_row_id=OLD.id AND operation.operation_kind='revoked'
			     AND operation.credential_epoch=OLD.credential_epoch AND NEW.revoked_at=operation.server_received_at
			     AND audit.event_kind='revoked' AND audit.credential_epoch=OLD.credential_epoch AND audit.outcome='committed')) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   OLD.lifecycle_state='issued' AND NEW.lifecycle_state='accepted' AND NEW.last_sequence=OLD.last_sequence+1 AND
			   OLD.accepted_at IS NULL AND NEW.terminal_at IS NULL AND OLD.revoked_at IS NULL AND NEW.revoked_at IS NULL AND
			   EXISTS(SELECT 1 FROM external_stage_operation_events operation
			    JOIN external_stage_audit_events audit ON audit.operation_event_id=operation.id
			    WHERE operation.handoff_row_id=OLD.id AND operation.operation_kind='accepted'
			     AND operation.sequence=NEW.last_sequence AND operation.credential_epoch=OLD.credential_epoch
			     AND NEW.accepted_at=operation.server_received_at AND audit.event_kind='accepted'
			     AND audit.sequence=NEW.last_sequence AND audit.outcome='committed')) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   NEW.lifecycle_state=OLD.lifecycle_state AND NEW.last_sequence=OLD.last_sequence+1 AND
			   NEW.accepted_at IS OLD.accepted_at AND NEW.terminal_at IS OLD.terminal_at AND NEW.revoked_at IS OLD.revoked_at AND
			   EXISTS(SELECT 1 FROM external_stage_heartbeat_windows window
			    WHERE window.handoff_row_id=OLD.id AND window.credential_epoch=OLD.credential_epoch
			     AND window.last_sequence=NEW.last_sequence AND window.lifecycle_state=OLD.lifecycle_state)) OR
			  (NEW.credential_epoch=OLD.credential_epoch AND NEW.secret_digest IS OLD.secret_digest AND
			   NEW.last_sequence=OLD.last_sequence+1 AND NEW.accepted_at IS OLD.accepted_at AND NEW.revoked_at IS OLD.revoked_at AND
			   ((OLD.lifecycle_state='accepted' AND NEW.lifecycle_state IN ('active','waiting','blocked','succeeded','failed')) OR
			    (OLD.lifecycle_state IN ('active','waiting','blocked') AND NEW.lifecycle_state IN ('active','waiting','blocked','succeeded','failed'))) AND
			   ((NEW.lifecycle_state IN ('succeeded','failed') AND NEW.terminal_at IS NOT NULL) OR
			    (NEW.lifecycle_state IN ('active','waiting','blocked') AND NEW.terminal_at IS NULL)) AND
			   EXISTS(SELECT 1 FROM external_stage_report_events report
			    JOIN external_stage_audit_events audit ON audit.report_event_id=report.id AND audit.event_kind='reported'
			    WHERE report.handoff_row_id=OLD.id AND report.sequence=NEW.last_sequence
			     AND report.credential_epoch=OLD.credential_epoch AND report.lifecycle_state=NEW.lifecycle_state
			     AND ((NEW.lifecycle_state IN ('succeeded','failed') AND NEW.terminal_at=report.server_received_at) OR
			          (NEW.lifecycle_state IN ('active','waiting','blocked') AND NEW.terminal_at IS NULL))
			     AND audit.sequence=report.sequence AND audit.credential_epoch=report.credential_epoch AND audit.outcome='committed'
			     AND ((OLD.reporter_role='owner' AND EXISTS(SELECT 1 FROM external_stage_owner_latest latest
			          WHERE latest.handoff_row_id=OLD.id AND latest.report_event_id=report.id AND latest.sequence=report.sequence)) OR
			          (OLD.reporter_role='dependency' AND EXISTS(SELECT 1 FROM external_stage_dependency_latest latest
			          WHERE latest.handoff_row_id=OLD.id AND latest.report_event_id=report.id AND latest.sequence=report.sequence
			           AND latest.credential_epoch=OLD.credential_epoch)))) )
			 )
			 BEGIN SELECT RAISE(ABORT,'external stage handoff update lacks an exact causal fact'); END`,
	}})

	// M197 / PAI-1027: durable, authenticated Aithema conversation turns.
	// A conversation-service credential remains in api_keys for the existing
	// hash/expiry/revocation custody, but its immutable one-to-one binding is
	// the dedicated kind discriminator used by authentication.
	migrations = append(migrations, migration{version: 197, steps: []string{
		`CREATE TABLE conversation_service_bindings (
		 binding_id              TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("binding_id") + `),
		 revision                INTEGER NOT NULL CHECK(revision=1),
		 api_key_id               INTEGER NOT NULL UNIQUE REFERENCES api_keys(id),
		 project_id               INTEGER NOT NULL REFERENCES projects(id),
		 host_id                  TEXT NOT NULL CHECK(length(CAST(host_id AS BLOB)) BETWEEN 1 AND 128),
		 project_ref              TEXT NOT NULL CHECK(length(CAST(project_ref AS BLOB)) BETWEEN 1 AND 128),
		 runtime_id               TEXT NOT NULL CHECK(` + sqlUUIDCheck("runtime_id") + `),
		 runtime_generation       TEXT NOT NULL CHECK(` + sqlUUIDCheck("runtime_generation") + `),
		 account_label            TEXT NOT NULL CHECK(length(CAST(account_label AS BLOB)) BETWEEN 1 AND 64),
		 account_key              TEXT NOT NULL CHECK(length(CAST(account_key AS BLOB)) BETWEEN 1 AND 128),
		 attachment_revision      INTEGER NOT NULL CHECK(attachment_revision>0),
		 dispatch_profile_id      TEXT NOT NULL CHECK(length(CAST(dispatch_profile_id AS BLOB)) BETWEEN 1 AND 128),
		 dispatch_profile_version TEXT NOT NULL CHECK(length(CAST(dispatch_profile_version AS BLOB)) BETWEEN 1 AND 128),
		 execution_policy_id      TEXT NOT NULL CHECK(execution_policy_id='aithema-conversation-v1'),
		 max_input_bytes          INTEGER NOT NULL CHECK(max_input_bytes BETWEEN 1 AND 131072),
		 max_messages             INTEGER NOT NULL CHECK(max_messages BETWEEN 1 AND 128),
		 max_output_bytes         INTEGER NOT NULL CHECK(max_output_bytes BETWEEN 1 AND 262144),
		 max_event_bytes          INTEGER NOT NULL CHECK(max_event_bytes BETWEEN 1 AND 8192),
		 max_events               INTEGER NOT NULL CHECK(max_events BETWEEN 2 AND 512),
		 max_timeout_ms           INTEGER NOT NULL CHECK(max_timeout_ms BETWEEN 1 AND 180000),
		 created_by               INTEGER NOT NULL REFERENCES users(id),
		 session_credential_id    TEXT NOT NULL,
		 created_at               TEXT NOT NULL,
		 CHECK(host_id NOT GLOB '*[^ -~]*' AND project_ref NOT GLOB '*[^ -~]*' AND
		       account_label NOT GLOB '*[^A-Za-z0-9._:-]*' AND account_key NOT GLOB '*[^A-Za-z0-9._:-]*' AND
		       dispatch_profile_id NOT GLOB '*[^A-Za-z0-9._:-]*' AND dispatch_profile_version NOT GLOB '*[^A-Za-z0-9._:-]*')
		)`,
		`CREATE INDEX idx_conversation_service_binding_runtime
		 ON conversation_service_bindings(project_id,runtime_id,runtime_generation,account_key)`,
		`CREATE TRIGGER trg_conversation_service_binding_guard BEFORE INSERT ON conversation_service_bindings
		 WHEN NOT EXISTS(SELECT 1 FROM api_keys key JOIN users owner ON owner.id=key.user_id
		                 JOIN projects project ON project.id=NEW.project_id
		                 WHERE key.id=NEW.api_key_id AND key.credential_kind='general' AND key.scopes=''
		                  AND key.disabled_at IS NULL AND (key.expires_at IS NULL OR julianday(key.expires_at)>julianday('now'))
		                  AND owner.status='active' AND project.status='active')
		 BEGIN SELECT RAISE(ABORT,'invalid conversation service credential binding'); END`,
		`CREATE TRIGGER trg_conversation_service_binding_no_update BEFORE UPDATE ON conversation_service_bindings
		 BEGIN SELECT RAISE(ABORT,'conversation service binding is immutable'); END`,
		`CREATE TRIGGER trg_conversation_service_binding_no_delete BEFORE DELETE ON conversation_service_bindings
		 BEGIN SELECT RAISE(ABORT,'conversation service binding is durable'); END`,
		`CREATE TABLE conversation_service_actors (
		 binding_id TEXT NOT NULL REFERENCES conversation_service_bindings(binding_id),
		 issuer     TEXT NOT NULL CHECK(length(CAST(issuer AS BLOB)) BETWEEN 1 AND 256 AND issuer NOT GLOB '*[^ -~]*'),
		 subject    TEXT NOT NULL CHECK(length(CAST(subject AS BLOB)) BETWEEN 1 AND 256 AND subject NOT GLOB '*[^ -~]*'),
		 user_id    INTEGER NOT NULL REFERENCES users(id),
		 created_at TEXT NOT NULL,
		 PRIMARY KEY(binding_id,issuer,subject)
		) WITHOUT ROWID`,
		`CREATE INDEX idx_conversation_service_actor_user ON conversation_service_actors(binding_id,user_id)`,
		`CREATE TRIGGER trg_conversation_service_actor_no_update BEFORE UPDATE ON conversation_service_actors
		 BEGIN SELECT RAISE(ABORT,'conversation service actor mapping is immutable'); END`,
		`CREATE TRIGGER trg_conversation_service_actor_no_delete BEFORE DELETE ON conversation_service_actors
		 BEGIN SELECT RAISE(ABORT,'conversation service actor mapping is durable'); END`,
		`CREATE TABLE conversation_calls (
		 call_id                  TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("call_id") + `),
		 binding_id               TEXT NOT NULL REFERENCES conversation_service_bindings(binding_id),
		 binding_revision         INTEGER NOT NULL CHECK(binding_revision>0),
		 api_key_id               INTEGER NOT NULL REFERENCES api_keys(id),
		 project_id               INTEGER NOT NULL REFERENCES projects(id),
		 request_id               TEXT NOT NULL CHECK(length(CAST(request_id AS BLOB)) BETWEEN 1 AND 128),
		 request_digest           BLOB NOT NULL CHECK(typeof(request_digest)='blob' AND length(request_digest)=32),
		 request_json             TEXT NOT NULL CHECK(json_valid(request_json) AND length(CAST(request_json AS BLOB))<=819200),
		 actor_issuer             TEXT NOT NULL,
		 actor_subject            TEXT NOT NULL,
		 actor_user_id            INTEGER NOT NULL REFERENCES users(id),
		 project_ref              TEXT NOT NULL,
		 conversation_id          TEXT NOT NULL CHECK(length(CAST(conversation_id AS BLOB)) BETWEEN 1 AND 128),
		 turn_id                  TEXT NOT NULL CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 128),
		 purpose                  TEXT NOT NULL CHECK(purpose IN ('chat','understand','interpret')),
		 state                    TEXT NOT NULL CHECK(state IN ('queued','claimed','running','cancel_requested','completed','failed','cancelled')),
		 deadline_at              TEXT NOT NULL,
		 last_sequence            INTEGER NOT NULL DEFAULT 0 CHECK(last_sequence BETWEEN 0 AND 512),
		 execution_generation     TEXT CHECK(execution_generation IS NULL OR ` + sqlUUIDCheck("execution_generation") + `),
		 runtime_id               TEXT NOT NULL,
		 runtime_generation       TEXT NOT NULL,
		 account_key              TEXT NOT NULL,
		 attachment_revision      INTEGER NOT NULL CHECK(attachment_revision>0),
		 dispatch_profile_id      TEXT NOT NULL,
		 dispatch_profile_version TEXT NOT NULL,
		 execution_policy_id      TEXT NOT NULL CHECK(execution_policy_id='aithema-conversation-v1'),
		 native_thread_id         TEXT NOT NULL DEFAULT '' CHECK(length(CAST(native_thread_id AS BLOB))<=256),
		 native_turn_id           TEXT NOT NULL DEFAULT '' CHECK(length(CAST(native_turn_id AS BLOB))<=256),
		 assembled_text           TEXT NOT NULL DEFAULT '' CHECK(length(CAST(assembled_text AS BLOB))<=262144),
		 output_text              TEXT NOT NULL DEFAULT '' CHECK(length(CAST(output_text AS BLOB))<=262144),
		 output_sha256            TEXT NOT NULL DEFAULT '' CHECK(output_sha256='' OR (length(output_sha256)=64 AND output_sha256 NOT GLOB '*[^0-9a-f]*')),
		 error_code               TEXT NOT NULL DEFAULT '' CHECK(length(error_code)<=64),
		 created_at               TEXT NOT NULL,
		 updated_at               TEXT NOT NULL,
		 UNIQUE(api_key_id,project_id,request_id),
		 UNIQUE(binding_id,call_id)
		)`,
		`CREATE UNIQUE INDEX idx_conversation_calls_active_conversation
		 ON conversation_calls(project_id,project_ref,conversation_id)
		 WHERE state IN ('queued','claimed','running','cancel_requested')`,
		`CREATE INDEX idx_conversation_calls_claim
		 ON conversation_calls(project_id,runtime_id,runtime_generation,state,created_at,call_id)`,
		`CREATE TRIGGER trg_conversation_call_identity_immutable BEFORE UPDATE OF
		 call_id,binding_id,binding_revision,api_key_id,project_id,request_id,request_digest,request_json,
		 actor_issuer,actor_subject,actor_user_id,project_ref,conversation_id,turn_id,purpose,deadline_at,
		 runtime_id,runtime_generation,account_key,attachment_revision,dispatch_profile_id,
		 dispatch_profile_version,execution_policy_id,created_at ON conversation_calls
		 BEGIN SELECT RAISE(ABORT,'conversation call identity is immutable'); END`,
		`CREATE TRIGGER trg_conversation_call_no_delete BEFORE DELETE ON conversation_calls
		 BEGIN SELECT RAISE(ABORT,'conversation call is durable'); END`,
		`CREATE TABLE conversation_account_slots (
		 call_id            TEXT PRIMARY KEY REFERENCES conversation_calls(call_id),
		 project_id         INTEGER NOT NULL REFERENCES projects(id),
		 host_id            TEXT NOT NULL,
		 runtime_id         TEXT NOT NULL,
		 runtime_generation TEXT NOT NULL,
		 account_label      TEXT NOT NULL,
		 account_key        TEXT NOT NULL,
		 reserved_at        TEXT NOT NULL,
		 released_at        TEXT
		)`,
		`CREATE UNIQUE INDEX idx_conversation_account_slot_active
		 ON conversation_account_slots(project_id,host_id,account_label,account_key)
		 WHERE released_at IS NULL`,
		`CREATE TRIGGER trg_conversation_account_slot_identity_immutable BEFORE UPDATE OF
		 call_id,project_id,host_id,runtime_id,runtime_generation,account_label,account_key,reserved_at ON conversation_account_slots
		 BEGIN SELECT RAISE(ABORT,'conversation account reservation identity is immutable'); END`,
		`CREATE TRIGGER trg_conversation_account_slot_release_terminal BEFORE UPDATE OF released_at ON conversation_account_slots
		 WHEN OLD.released_at IS NOT NULL OR NEW.released_at IS NULL
		 BEGIN SELECT RAISE(ABORT,'conversation account reservation release is terminal'); END`,
		`CREATE TRIGGER trg_conversation_account_slot_no_delete BEFORE DELETE ON conversation_account_slots
		 BEGIN SELECT RAISE(ABORT,'conversation account reservation is durable'); END`,
		`CREATE TABLE conversation_call_events (
		 call_id              TEXT NOT NULL REFERENCES conversation_calls(call_id),
		 sequence             INTEGER NOT NULL CHECK(sequence BETWEEN 1 AND 512),
		 execution_generation TEXT,
		 kind                 TEXT NOT NULL CHECK(kind IN ('started','assistant_delta','completed','failed','cancelled')),
		 text                 TEXT NOT NULL DEFAULT '' CHECK(length(CAST(text AS BLOB))<=8192),
		 thread_id            TEXT NOT NULL DEFAULT '' CHECK(length(CAST(thread_id AS BLOB))<=256),
		 turn_id              TEXT NOT NULL DEFAULT '' CHECK(length(CAST(turn_id AS BLOB))<=256),
		 output_sha256        TEXT NOT NULL DEFAULT '' CHECK(output_sha256='' OR (length(output_sha256)=64 AND output_sha256 NOT GLOB '*[^0-9a-f]*')),
		 error_code           TEXT NOT NULL DEFAULT '' CHECK(length(error_code)<=64),
		 event_digest         BLOB NOT NULL CHECK(typeof(event_digest)='blob' AND length(event_digest)=32),
		 created_at           TEXT NOT NULL,
		 PRIMARY KEY(call_id,sequence)
		) WITHOUT ROWID`,
		`CREATE TRIGGER trg_conversation_call_event_no_update BEFORE UPDATE ON conversation_call_events
		 BEGIN SELECT RAISE(ABORT,'conversation event is immutable'); END`,
		`CREATE TRIGGER trg_conversation_call_event_no_delete BEFORE DELETE ON conversation_call_events
		 BEGIN SELECT RAISE(ABORT,'conversation event is durable'); END`,
	}})

	// M198 / PAI-1054: scoped internal human reviewer. The compatibility roles
	// stay external so old binaries cannot reinterpret the identity as a member.
	migrations = append(migrations, migration{version: 198, steps: []string{
		`ALTER TABLE users ADD COLUMN is_reviewer INTEGER NOT NULL DEFAULT 0 CHECK(is_reviewer IN (0,1))`,
		`CREATE TRIGGER reviewer_identity_insert BEFORE INSERT ON users
		 WHEN NEW.is_reviewer=1 AND (NEW.role!='external' OR NEW.role_key!='external' OR NEW.is_super_admin!=0)
		 BEGIN SELECT RAISE(ABORT,'reviewer requires external compatibility roles'); END`,
		`CREATE TRIGGER reviewer_identity_update BEFORE UPDATE ON users
		 WHEN NEW.is_reviewer=1 AND (NEW.role!='external' OR NEW.role_key!='external' OR NEW.is_super_admin!=0 OR
		 EXISTS(SELECT 1 FROM project_members WHERE user_id=NEW.id AND access_level='editor'))
		 BEGIN SELECT RAISE(ABORT,'reviewer requires explicit viewer-only access'); END`,
		`CREATE TRIGGER reviewer_membership_insert BEFORE INSERT ON project_members
		 WHEN NEW.access_level='editor' AND EXISTS(SELECT 1 FROM users WHERE id=NEW.user_id AND is_reviewer=1)
		 BEGIN SELECT RAISE(ABORT,'reviewer cannot receive editor access'); END`,
		`CREATE TRIGGER reviewer_membership_update BEFORE UPDATE ON project_members
		 WHEN NEW.access_level='editor' AND EXISTS(SELECT 1 FROM users WHERE id=NEW.user_id AND is_reviewer=1)
		 BEGIN SELECT RAISE(ABORT,'reviewer cannot receive editor access'); END`,
	}})

	// M199 / PAI-1054: prevent new ambiguous email identities for every role.
	// Existing rows remain untouched; deployments audit duplicates beforehand.
	// A Flow projection credential is never a general API key.
	// Older readers cannot find its hash in api_keys and therefore fail closed.
	migrations = append(migrations, migration{version: 199, steps: []string{
		`CREATE TRIGGER user_email_insert BEFORE INSERT ON users
   WHEN trim(COALESCE(NEW.email,''))!='' AND EXISTS(SELECT 1 FROM users existing WHERE lower(trim(existing.email))=lower(trim(NEW.email)))
   BEGIN SELECT RAISE(ABORT,'email must uniquely identify one user'); END`,
		`CREATE TRIGGER user_email_update BEFORE UPDATE OF email ON users
   WHEN trim(COALESCE(NEW.email,''))!='' AND lower(trim(COALESCE(NEW.email,'')))!=lower(trim(COALESCE(OLD.email,''))) AND EXISTS(SELECT 1 FROM users existing WHERE existing.id!=NEW.id AND lower(trim(existing.email))=lower(trim(NEW.email)))
   BEGIN SELECT RAISE(ABORT,'email must uniquely identify one user'); END`,
		`CREATE TABLE flow_projection_credentials (
		 id INTEGER PRIMARY KEY AUTOINCREMENT,
		 user_id INTEGER NOT NULL REFERENCES users(id),
		 project_id INTEGER NOT NULL REFERENCES projects(id),
		 name TEXT NOT NULL CHECK(length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
		 key_hash TEXT NOT NULL UNIQUE CHECK(length(key_hash)=64 AND key_hash NOT GLOB '*[^0-9a-f]*'),
		 created_at TEXT NOT NULL,
		 expires_at TEXT NOT NULL CHECK(julianday(expires_at) IS NOT NULL AND julianday(created_at) IS NOT NULL AND julianday(expires_at)>julianday(created_at) AND julianday(expires_at)<=julianday(created_at)+30),
		 disabled_at TEXT
		)`,
		`CREATE TRIGGER flow_projection_identity_immutable BEFORE UPDATE OF id,user_id,project_id,name,key_hash,created_at,expires_at ON flow_projection_credentials
		 BEGIN SELECT RAISE(ABORT,'Flow projection credential identity is immutable'); END`,
		`CREATE TRIGGER flow_projection_revocation_terminal BEFORE UPDATE OF disabled_at ON flow_projection_credentials
		 WHEN OLD.disabled_at IS NOT NULL OR NEW.disabled_at IS NULL
		 BEGIN SELECT RAISE(ABORT,'Flow projection revocation is terminal'); END`,
		`CREATE TRIGGER flow_projection_no_delete BEFORE DELETE ON flow_projection_credentials
		 BEGIN SELECT RAISE(ABORT,'Flow projection credential audit is durable'); END`,
	}})

	for _, m := range migrations {
		if m.version > maxVersion {
			continue
		}
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_versions WHERE version=?", m.version).Scan(&count); err != nil {
			return fmt.Errorf("check migration %d: %w", m.version, err)
		}
		if count > 0 {
			continue
		}
		conn, err := db.Conn(context.Background())
		if err != nil {
			return fmt.Errorf("migration %d: get conn: %w", m.version, err)
		}
		if check := migrationPreconditions[m.version]; check != nil {
			if err := check(context.Background(), conn); err != nil {
				conn.Close()
				return fmt.Errorf("migration %d precondition failed: %w", m.version, err)
			}
		}
		migErr := applyMigration(context.Background(), conn, m)
		conn.Close()
		if migErr != nil {
			return migErr
		}
		fmt.Printf("db: applied migration %d\n", m.version)
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	if m.version == 178 {
		return applyMessageBodyMigration178(ctx, conn)
	}
	if m.version == 180 {
		return applyHarnessMessagesMigration180(ctx, conn)
	}
	if m.version == 192 {
		return applyHarnessRetirementMigration192(ctx, conn)
	}
	if m.version == 194 {
		return applyBaselineRevisionMigration194(ctx, conn)
	}
	if migrationUsesForeignKeyPragma(m) {
		return applyForeignKeyRebuildMigration(ctx, conn, m)
	}
	return applyMigrationAtomic(ctx, conn, m)
}

func applyMigrationAtomic(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %d: begin tx: %w", m.version, err)
	}
	for _, step := range m.steps {
		if _, err := tx.ExecContext(ctx, step); err != nil {
			_ = tx.Rollback()
			return migrationStepError(m.version, step, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_versions(version) VALUES(?)", m.version); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %d: commit: %w", m.version, err)
	}
	return nil
}

func applyForeignKeyRebuildMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	// SQLite ignores PRAGMA foreign_keys=OFF inside an open transaction. Disable
	// it first on this pinned connection, then transact the complete rebuild and
	// schema-version write. A failed or interrupted rebuild therefore rolls back
	// as one unit instead of leaving a half-renamed table behind.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("migration %d: disable foreign keys: %w", m.version, err)
	}
	restoreForeignKeys := func() error {
		_, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = restoreForeignKeys()
		return fmt.Errorf("migration %d: begin rebuild tx: %w", m.version, err)
	}
	for _, step := range m.steps {
		normalized := strings.ToUpper(strings.Join(strings.Fields(step), " "))
		if normalized == "PRAGMA FOREIGN_KEYS=OFF" || normalized == "PRAGMA FOREIGN_KEYS=ON" {
			continue
		}
		if _, err := tx.ExecContext(ctx, step); err != nil {
			_ = tx.Rollback()
			_ = restoreForeignKeys()
			return migrationStepError(m.version, step, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_versions(version) VALUES(?)", m.version); err != nil {
		_ = tx.Rollback()
		_ = restoreForeignKeys()
		return fmt.Errorf("record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		_ = restoreForeignKeys()
		return fmt.Errorf("migration %d: commit rebuild: %w", m.version, err)
	}
	if err := restoreForeignKeys(); err != nil {
		return fmt.Errorf("migration %d: restore foreign keys: %w", m.version, err)
	}
	return nil
}

func migrationUsesForeignKeyPragma(m migration) bool {
	for _, step := range m.steps {
		if strings.Contains(strings.ToUpper(step), "PRAGMA FOREIGN_KEYS") {
			return true
		}
	}
	return false
}

// migrationPreconditions maps a migration version to a data-shape check that
// must pass before that migration is applied. Constraint-adding migrations
// (unique indexes, NOT NULL columns) can brick an upgrade if pre-existing data
// already violates the new constraint: the step fails mid-transaction with an
// opaque SQLite error, Open() returns it, the process exits, and the container
// restart policy replays the same doomed migration forever. A precondition
// turns that into a fail-fast error that names the offending rows so an
// operator can repair the data. Checks run only when the migration is pending
// (zero cost for already-upgraded instances) on the same pinned connection.
var migrationPreconditions = map[int]func(context.Context, *sql.Conn) error{
	194: checkM194SchemaIsUnapplied,
	195: checkM195SchemaIsUnapplied,
	197: checkM197SchemaIsUnapplied,
	// PAI-576: migration 113 adds a UNIQUE index on (project_id, issue_number).
	113: checkNoDuplicateIssueNumbers,
	// PAI-799/801: M142's original SQLite length() checks counted Unicode
	// code points. HTTP already enforced bytes, but refuse to carry any row
	// written by a direct legacy DB client across the byte-bound correction.
	143: checkAgentRunTelemetryByteBounds,
	// PAI-809: M147 is deliberately non-idempotent. A partial or locally
	// modified control schema must fail before the first ALTER rather than be
	// silently accepted behind object-existence clauses.
	147: checkM147SchemaIsUnapplied,
	// PAI-810: M148 is also a pure additive, non-idempotent migration. Refuse
	// partial local copies so sealed-stream invariants cannot be skipped behind
	// CREATE IF NOT EXISTS behavior.
	148: checkM148SchemaIsUnapplied,
	149: checkM149SchemaIsUnapplied,
	150: checkM150SchemaIsUnapplied,
	// PAI-817: M151 is a pure additive, non-idempotent migration for agent message
	// security. Refuse partial local copies so the security contract is not bypassed.
	151: checkM151SchemaIsUnapplied,
	152: checkM152SchemaIsUnapplied,
	153: checkM153SchemaIsUnapplied,
	154: checkM154SchemaIsUnapplied,
	161: func(ctx context.Context, conn *sql.Conn) error {
		return checkSchemaObjectsAbsent(ctx, conn, 161, []string{
			"harness_sessions", "idx_harness_sessions_active_address", "idx_harness_sessions_active_identity",
			"idx_harness_sessions_host_phase",
			"trg_harness_sessions_identity_immutable", "trg_harness_sessions_agent_insert", "trg_harness_sessions_target_insert",
			"trg_harness_sessions_target_update", "harness_session_controls",
			"idx_harness_session_control_active", "idx_harness_session_control_drain",
			"trg_harness_session_control_identity_immutable",
		})
	},
	162: checkKnowledgeScopeIdentities,
	163: checkProductSessionNodeFoundation,
	164: checkSessionUtteranceFoundation,
	165: func(ctx context.Context, conn *sql.Conn) error {
		return checkSchemaObjectsAbsent(ctx, conn, 165, []string{
			"instance_orchestrator", "instance_orchestrator_events", "idx_instance_orchestrator_events_created",
			"trg_instance_orchestrator_events_sequence", "trg_instance_orchestrator_events_no_update", "trg_instance_orchestrator_events_no_delete",
			"trg_instance_orchestrator_project_no_soft_delete", "trg_instance_orchestrator_no_active_harness_rename",
		})
	},
	166: func(ctx context.Context, conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='command_palette_shortcut'`).Scan(&count); err != nil {
			return fmt.Errorf("inspect M166 users ownership: %w", err)
		}
		if count != 0 {
			return fmt.Errorf("M166 schema is partially present or locally incompatible: column:users.command_palette_shortcut")
		}
		return nil
	},
	167: checkM167SchemaIsUnapplied,
	169: func(ctx context.Context, conn *sql.Conn) error {
		var columns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_sessions')
			WHERE name IN ('activity_state','activity_reason','activity_event_kind','activity_at','activity_sequence','closed_reason')`).Scan(&columns); err != nil {
			return fmt.Errorf("inspect M169 harness activity columns: %w", err)
		}
		if columns != 0 {
			return fmt.Errorf("M169 schema is partially present or locally incompatible: harness activity columns=%d", columns)
		}
		return checkSchemaObjectsAbsent(ctx, conn, 169, []string{
			"harness_session_events", "idx_harness_session_events_session", "idx_harness_sessions_activity",
			"trg_harness_sessions_activity_shape_insert", "trg_harness_sessions_activity_shape_update",
			"trg_harness_session_events_no_update", "trg_harness_session_events_no_delete",
		})
	},
	170: func(ctx context.Context, conn *sql.Conn) error {
		var columns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_sessions')
			WHERE name IN ('parent_harness_session_id','ticket_id')`).Scan(&columns); err != nil {
			return fmt.Errorf("inspect M170 harness binding columns: %w", err)
		}
		if columns != 0 {
			return fmt.Errorf("M170 schema is partially present or locally incompatible: harness binding columns=%d", columns)
		}
		var eventColumns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_session_events')
			WHERE name IN ('before_parent_harness_session_id','after_parent_harness_session_id','before_ticket_id','after_ticket_id')`).Scan(&eventColumns); err != nil {
			return fmt.Errorf("inspect M170 harness event columns: %w", err)
		}
		if eventColumns != 0 {
			return fmt.Errorf("M170 schema is partially present or locally incompatible: harness event columns=%d", eventColumns)
		}
		return checkSchemaObjectsAbsent(ctx, conn, 170, []string{
			"idx_harness_sessions_parent", "idx_harness_sessions_parent_ref",
			"idx_harness_sessions_ticket", "idx_harness_sessions_ticket_ref",
			"trg_harness_sessions_binding_insert", "trg_harness_sessions_binding_update",
			"trg_harness_sessions_binding_revision", "trg_harness_sessions_binding_event",
			"trg_issues_bound_harness_move",
		})
	},
	171: func(ctx context.Context, conn *sql.Conn) error {
		var columns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_session_events')
			WHERE name='assignment_present'`).Scan(&columns); err != nil {
			return fmt.Errorf("inspect M171 harness event assignment snapshot: %w", err)
		}
		if columns != 0 {
			return fmt.Errorf("M171 schema is partially present or locally incompatible: harness event assignment columns=%d", columns)
		}
		return checkSchemaObjectsAbsent(ctx, conn, 171, []string{
			"agent_attention_items", "idx_agent_attention_items_receiver",
			"trg_agent_attention_items_no_update", "trg_agent_attention_items_no_delete",
			"agent_attention_projection_cursors", "idx_harness_session_controls_attention",
			"agent_attention_cursors", "agent_attention_batches",
			"idx_agent_attention_batches_open", "idx_agent_attention_batches_cursor",
		})
	},
	172: func(ctx context.Context, conn *sql.Conn) error {
		var columns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_sessions')
			WHERE name IN ('workspace_path','git_top_level','git_branch','workspace_identity','workspace_kind','workspace_mode',
			'dispatch_profile_id','dispatch_profile_version','dispatch_model','dispatch_effort','account_label')`).Scan(&columns); err != nil {
			return fmt.Errorf("inspect M172 harness provenance columns: %w", err)
		}
		if columns != 0 {
			return fmt.Errorf("M172 schema is partially present or locally incompatible: harness provenance columns=%d", columns)
		}
		return checkSchemaObjectsAbsent(ctx, conn, 172, []string{
			"idx_harness_sessions_active_workspace", "trg_harness_sessions_provenance_shape_insert",
			"trg_harness_sessions_provenance_immutable", "trg_harness_sessions_workspace_collision_insert",
			"trg_harness_sessions_workspace_collision_activate",
		})
	},
	173: func(ctx context.Context, conn *sql.Conn) error {
		var columns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('agent_messages')
			WHERE name='expects_reply'`).Scan(&columns); err != nil {
			return fmt.Errorf("inspect M173 message reply expectation: %w", err)
		}
		if columns != 0 {
			return fmt.Errorf("M173 schema is partially present or locally incompatible: agent_messages.expects_reply")
		}
		return checkSchemaObjectsAbsent(ctx, conn, 173, []string{
			"trg_agent_messages_expects_reply_immutable", "agent_reply_obligations", "idx_agent_reply_obligations_due",
			"trg_agent_reply_obligation_insert_guard", "trg_agent_reply_obligation_update_guard",
			"agent_reply_obligation_events", "trg_agent_reply_obligation_events_no_update",
			"trg_agent_reply_obligation_events_no_delete", "trg_agent_reply_obligation_open_event",
			"trg_agent_reply_obligation_close_event", "agent_message_human_resolutions",
			"idx_agent_message_human_resolutions_project", "trg_agent_message_human_resolution_insert_guard",
			"trg_agent_message_human_resolutions_no_update", "trg_agent_message_human_resolutions_no_delete",
		})
	},
	174: func(ctx context.Context, conn *sql.Conn) error {
		var columns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_sessions') WHERE name='work_shape'`).Scan(&columns); err != nil {
			return fmt.Errorf("inspect M174 harness work shape: %w", err)
		}
		if columns != 0 {
			return fmt.Errorf("M174 schema is partially present or locally incompatible: harness work-shape columns=%d", columns)
		}
		var eventColumns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_session_events')
			WHERE name IN ('before_work_shape','after_work_shape')`).Scan(&eventColumns); err != nil {
			return fmt.Errorf("inspect M174 harness assignment event columns: %w", err)
		}
		if eventColumns != 0 {
			return fmt.Errorf("M174 schema is partially present or locally incompatible: harness assignment event columns=%d", eventColumns)
		}
		return checkSchemaObjectsAbsent(ctx, conn, 174, []string{
			"trg_harness_sessions_work_shape_insert", "trg_harness_sessions_work_shape_update",
		})
	},
	175: func(ctx context.Context, conn *sql.Conn) error {
		return checkSchemaObjectsAbsent(ctx, conn, 175, []string{
			"external_stage_pharos_evidence_v2", "trg_external_stage_pharos_evidence_v2_insert_guard",
			"trg_external_stage_pharos_evidence_v2_no_update", "trg_external_stage_pharos_evidence_v2_no_delete",
		})
	},
	181: func(ctx context.Context, conn *sql.Conn) error {
		var columns int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('harness_sessions') WHERE name='account_key'`).Scan(&columns); err != nil {
			return fmt.Errorf("inspect M181 harness account key: %w", err)
		}
		if columns != 0 {
			return fmt.Errorf("M181 schema is partially present or locally incompatible: harness account_key columns=%d", columns)
		}
		return nil
	},
	192: func(ctx context.Context, conn *sql.Conn) error {
		return checkSchemaObjectsAbsent(ctx, conn, 192, []string{
			"harness_session_retirements", "idx_harness_session_retirements_runtime", "idx_harness_session_retirements_active",
			"trg_harness_session_retirement_identity", "trg_harness_session_retirement_transition",
			"trg_harness_session_retirement_no_delete", "trg_harness_retirement_binding_admission",
			"trg_harness_retirement_delivery_admission", "trg_harness_retirement_consumer_admission",
			"trg_harness_retirement_control_admission",
		})
	},
}

func checkM197SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	return checkSchemaObjectsAbsent(ctx, conn, 197, []string{
		"conversation_service_bindings", "idx_conversation_service_binding_runtime",
		"trg_conversation_service_binding_guard", "trg_conversation_service_binding_no_update",
		"trg_conversation_service_binding_no_delete", "conversation_service_actors",
		"idx_conversation_service_actor_user", "trg_conversation_service_actor_no_update",
		"trg_conversation_service_actor_no_delete", "conversation_calls",
		"idx_conversation_calls_active_conversation", "idx_conversation_calls_claim",
		"trg_conversation_call_identity_immutable", "trg_conversation_call_no_delete",
		"conversation_account_slots", "idx_conversation_account_slot_active",
		"trg_conversation_account_slot_identity_immutable", "trg_conversation_account_slot_release_terminal",
		"trg_conversation_account_slot_no_delete", "conversation_call_events",
		"trg_conversation_call_event_no_update", "trg_conversation_call_event_no_delete",
	})
}

func checkSessionUtteranceFoundation(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT 'column:agent_messages.'||name FROM pragma_table_info('agent_messages')
		WHERE name IN ('from_user_id','product_session_id')
		UNION ALL
		SELECT type||':'||name FROM sqlite_master WHERE name IN (
		 'idx_agent_messages_user','idx_agent_messages_product_session',
		 'trg_agent_messages_human_shape_insert','trg_agent_messages_human_shape_update',
		 'paimos_conversation_bindings','trg_paimos_conversation_binding_insert',
		 'trg_paimos_conversation_binding_immutable','session_utterance_receipts',
		 'trg_session_utterance_receipts_shape',
		 'trg_session_utterance_receipts_no_update','trg_session_utterance_receipts_no_delete')
		ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M164 schema ownership: %w", err)
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M164 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M164 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M164 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	var invalid int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages am
		LEFT JOIN project_agents sender ON sender.id=am.from_agent_id
		LEFT JOIN project_agents receiver ON receiver.id=am.to_agent_id
		WHERE am.role<>'agent' OR am.from_agent_id IS NULL OR am.to_agent_id IS NULL OR
		 sender.id IS NULL OR receiver.id IS NULL OR sender.project_id<>receiver.project_id`).Scan(&invalid); err != nil {
		return fmt.Errorf("inspect M164 legacy message shape: %w", err)
	}
	if invalid != 0 {
		return fmt.Errorf("invalid legacy agent message rows block M164: count=%d", invalid)
	}
	return nil
}

func checkProductSessionNodeFoundation(ctx context.Context, conn *sql.Conn) error {
	var nodeDepthColumn int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name='node_depth'`).Scan(&nodeDepthColumn); err != nil {
		return fmt.Errorf("inspect M163 schema ownership: %w", err)
	}
	if nodeDepthColumn != 0 {
		return fmt.Errorf("M163 schema is partially present or locally incompatible: column:projects.node_depth")
	}
	if err := checkSchemaObjectsAbsent(ctx, conn, 163, []string{
		"node_label_defaults", "trg_node_label_defaults_no_delete", "project_node_label_overrides",
		"product_sessions", "idx_product_sessions_project_updated", "idx_product_sessions_node",
		"idx_product_sessions_target_agent", "product_session_events", "idx_product_session_events_session",
		"trg_product_sessions_actor_insert",
		"trg_product_sessions_agent_insert", "trg_project_agents_product_session_move",
		"trg_product_sessions_node_insert",
		"trg_product_sessions_node_update", "trg_product_sessions_mutation_guard",
		"trg_product_sessions_identity_immutable", "trg_product_sessions_no_delete",
		"trg_product_sessions_event_create", "trg_product_sessions_event_update",
		"trg_product_session_events_immutable_update", "trg_product_session_events_immutable_delete",
		"trg_issues_attached_session_move", "trg_issues_attached_session_soft_delete",
		"trg_projects_node_depth_guard", "trg_issue_parent_guard_insert", "trg_issue_parent_guard_update",
	}); err != nil {
		return err
	}

	rows, err := conn.QueryContext(ctx, `
		SELECT edge.source_id,edge.target_id
		FROM issue_relations edge
		JOIN issues parent ON parent.id=edge.source_id
		JOIN issues child ON child.id=edge.target_id
		WHERE edge.type='parent' AND (
		 parent.project_id IS NULL OR child.project_id IS NULL OR parent.project_id<>child.project_id)
		ORDER BY edge.source_id,edge.target_id LIMIT 10`)
	if err != nil {
		return fmt.Errorf("inspect M163 parent project ownership: %w", err)
	}
	var invalid []string
	for rows.Next() {
		var sourceID, targetID int64
		if err := rows.Scan(&sourceID, &targetID); err != nil {
			rows.Close()
			return fmt.Errorf("scan M163 parent project ownership: %w", err)
		}
		invalid = append(invalid, fmt.Sprintf("%d->%d", sourceID, targetID))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate M163 parent project ownership: %w", err)
	}
	rows.Close()
	if len(invalid) > 0 {
		return fmt.Errorf("invalid cross-project parent edges block M163; repair these rows before upgrading: %s", strings.Join(invalid, ", "))
	}

	cycleRows, err := conn.QueryContext(ctx, `
		WITH RECURSIVE walk(root_id,current_id) AS (
		 SELECT source_id,target_id FROM issue_relations WHERE type='parent'
		 UNION
		 SELECT walk.root_id,edge.target_id
		 FROM walk JOIN issue_relations edge ON edge.source_id=walk.current_id
		 WHERE edge.type='parent'
		)
		SELECT DISTINCT root_id FROM walk WHERE current_id=root_id ORDER BY root_id LIMIT 10`)
	if err != nil {
		return fmt.Errorf("inspect M163 parent cycles: %w", err)
	}
	var cycleRoots []string
	for cycleRows.Next() {
		var rootID int64
		if err := cycleRows.Scan(&rootID); err != nil {
			cycleRows.Close()
			return fmt.Errorf("scan M163 parent cycles: %w", err)
		}
		cycleRoots = append(cycleRoots, fmt.Sprint(rootID))
	}
	if err := cycleRows.Err(); err != nil {
		cycleRows.Close()
		return fmt.Errorf("iterate M163 parent cycles: %w", err)
	}
	cycleRows.Close()
	if len(cycleRoots) > 0 {
		return fmt.Errorf("parent cycles block M163; repair cycles containing issue ids: %s", strings.Join(cycleRoots, ", "))
	}
	return nil
}

func checkKnowledgeScopeIdentities(ctx context.Context, conn *sql.Conn) error {
	var legacyKind string
	err := conn.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name='idx_issues_type_slug_project'`).Scan(&legacyKind)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("M162 prerequisite is missing: index:idx_issues_type_slug_project; restore the M96 index before upgrading")
	}
	if err != nil {
		return fmt.Errorf("inspect M162 prerequisite index: %w", err)
	}
	if legacyKind != "index" {
		return fmt.Errorf("M162 prerequisite is locally incompatible: %s:idx_issues_type_slug_project", legacyKind)
	}

	if err := checkSchemaObjectsAbsent(ctx, conn, 162, []string{
		"idx_issues_knowledge_project_identity", "idx_issues_knowledge_user_identity",
		"idx_issues_knowledge_instance_identity", "trg_issues_scope_owner_insert",
		"trg_issues_scope_owner_update", "trg_issues_user_type_insert", "trg_issues_user_type_update",
	}); err != nil {
		return err
	}

	rows, err := conn.QueryContext(ctx, `
		SELECT id,project_id,user_id,type
		FROM issues
		WHERE (project_id IS NOT NULL AND user_id IS NOT NULL)
		   OR (user_id IS NOT NULL AND type<>'memory')
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("inspect knowledge ownership rows: %w", err)
	}
	var invalid []string
	for rows.Next() {
		var id int64
		var projectID, userID sql.NullInt64
		var issueType string
		if err := rows.Scan(&id, &projectID, &userID, &issueType); err != nil {
			rows.Close()
			return fmt.Errorf("scan invalid knowledge ownership: %w", err)
		}
		invalid = append(invalid, fmt.Sprintf("id=%d project_id=%v user_id=%v type=%q", id, nullableIntLabel(projectID), nullableIntLabel(userID), issueType))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate invalid knowledge ownership: %w", err)
	}
	rows.Close()
	if len(invalid) > 0 {
		return fmt.Errorf("invalid knowledge ownership blocks M162; repair these rows before upgrading: %s", strings.Join(invalid, "; "))
	}

	rows, err = conn.QueryContext(ctx, `
		SELECT CASE WHEN project_id IS NOT NULL THEN 'project'
		            WHEN user_id IS NOT NULL THEN 'user' ELSE 'instance' END,
		       COALESCE(project_id,user_id,0),type,slug,id
		FROM issues
		WHERE slug IS NOT NULL AND deleted_at IS NULL
		ORDER BY 1,2,type,slug,id`)
	if err != nil {
		return fmt.Errorf("inspect knowledge scope identities: %w", err)
	}
	type identityGroup struct {
		scope string
		owner int64
		type_ string
		slug  string
		ids   []int64
	}
	var current identityGroup
	var collisions []string
	flush := func() {
		if len(current.ids) > 1 {
			collisions = append(collisions, fmt.Sprintf("scope=%s:%d type=%q slug=%q ids=%v", current.scope, current.owner, current.type_, current.slug, current.ids))
		}
	}
	for rows.Next() {
		var scope, issueType, slug string
		var owner, id int64
		if err := rows.Scan(&scope, &owner, &issueType, &slug, &id); err != nil {
			rows.Close()
			return fmt.Errorf("scan knowledge scope identity: %w", err)
		}
		if len(current.ids) > 0 && (scope != current.scope || owner != current.owner || issueType != current.type_ || slug != current.slug) {
			flush()
			current = identityGroup{}
		}
		if len(current.ids) == 0 {
			current.scope, current.owner, current.type_, current.slug = scope, owner, issueType, slug
		}
		current.ids = append(current.ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate knowledge scope identities: %w", err)
	}
	rows.Close()
	flush()
	if len(collisions) > 0 {
		return fmt.Errorf("active knowledge scope identity collisions block M162; rename or merge the listed rows before upgrading: %s", strings.Join(collisions, "; "))
	}
	return nil
}

func nullableIntLabel(value sql.NullInt64) string {
	if !value.Valid {
		return "NULL"
	}
	return fmt.Sprint(value.Int64)
}

func checkM154SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT 'column:agent_messages.'||name FROM pragma_table_info('agent_messages')
		WHERE name IN ('delivery_level','delivery_fallback','delivery_primary_target_id','delivery_fallback_target_id')
		UNION ALL
		SELECT type||':'||name FROM sqlite_master
		WHERE name IN ('agent_message_targets','idx_agent_message_targets_enabled_role','idx_agent_message_targets_receiver',
		 'agent_message_deliveries','idx_agent_message_deliveries_dispatch','idx_agent_message_deliveries_target',
		 'agent_message_idempotency') ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M154 schema ownership: %w", err)
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M154 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M154 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M154 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	return nil
}

func checkM149SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	return checkSchemaObjectsAbsent(ctx, conn, 149, []string{
		"external_stage_owner_activation_events", "idx_external_stage_owner_activation_target",
		"idx_external_stage_owner_activation_idempotency", "trg_external_stage_owner_activation_insert_guard",
		"trg_external_stage_owner_activation_no_update", "trg_external_stage_owner_activation_no_delete",
	})
}

func checkM150SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	var collisions []string
	rows, err := conn.QueryContext(ctx, `
		SELECT 'agent_runs.'||name FROM pragma_table_info('agent_runs') WHERE name='implementation_result_digest'
		UNION ALL
		SELECT type||':'||name FROM sqlite_master WHERE name='trg_agent_runs_implementation_result_digest_guard'
		ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M150 schema ownership: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M150 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M150 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M150 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	return nil
}

func checkM151SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	return checkSchemaObjectsAbsent(ctx, conn, 151, []string{
		"agent_message_allowlist",
		"idx_agent_message_allowlist_receiver",
		"idx_agent_message_allowlist_sender",
		"agent_messages",
		"idx_agent_messages_to",
		"idx_agent_messages_from",
		"idx_agent_messages_issue",
		"idx_agent_messages_parent",
		"agent_message_rate_limits",
		"idx_agent_message_rate_limits_window",
	})
}

func checkM152SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT 'column:agent_messages.'||name FROM pragma_table_info('agent_messages')
		WHERE name IN ('message_id','context_id','task_id','role','parts_json','metadata_json',
		 'from_address','to_address','reply_to','thread_id','session_id')
		UNION ALL
		SELECT type||':'||name FROM sqlite_master
		WHERE name IN ('idx_agent_messages_message_id','idx_agent_messages_envelope_to','idx_agent_messages_thread')
		ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M152 schema ownership: %w", err)
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M152 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M152 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M152 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	return nil
}

func checkM153SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT 'column:agent_messages.'||name FROM pragma_table_info('agent_messages') WHERE name='read_at'
		UNION ALL
		SELECT type||':'||name FROM sqlite_master
		WHERE name IN ('agent_message_cursors','idx_agent_message_cursors_agent')
		ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M153 schema ownership: %w", err)
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M153 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M153 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M153 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	return nil
}

func checkSchemaObjectsAbsent(ctx context.Context, conn *sql.Conn, version int, names []string) error {
	for _, name := range names {
		var kind string
		err := conn.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name=?`, name).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect M%d schema ownership: %w", version, err)
		}
		return fmt.Errorf("M%d schema is partially present or locally incompatible: %s:%s", version, kind, name)
	}
	return nil
}

func checkM148SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT type||':'||name FROM sqlite_master
		WHERE name GLOB 'trg_external_stage_*' OR name GLOB 'idx_external_stage_*' OR name IN (
		 'external_stage_reporter_registrations','external_stage_prerequisite_sets','external_stage_prerequisites',
		 'external_stage_handoffs','external_stage_operation_events','external_stage_report_events',
		 'external_stage_heartbeat_windows','external_stage_user_roles',
		 'external_stage_pharos_evidence','external_stage_janus_evidence','external_stage_report_blockers',
		 'external_stage_owner_events','external_stage_owner_latest','external_stage_dependency_events',
		 'external_stage_dependency_latest','external_stage_audit_events','external_stage_setup_events')
		ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M148 schema ownership: %w", err)
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M148 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M148 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M148 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	return nil
}

func checkM147SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT 'sessions.'||name FROM pragma_table_info('sessions') WHERE name='credential_id'
		UNION ALL
		SELECT 'api_keys.'||name FROM pragma_table_info('api_keys') WHERE name IN ('disabled_at','expires_at')
		UNION ALL
		SELECT type||':'||name FROM sqlite_master
		WHERE name GLOB 'trg_control_*' OR name GLOB 'idx_control_*' OR name IN (
		 'issue_control_revisions','agent_run_cancellation_facts','control_operation_keys',
		 'control_capability_grants','control_capability_grant_actions','control_capability_grant_seals',
		 'control_capability_leases','control_capability_lease_actions','control_capability_lease_seals',
		 'control_input_requests','control_input_request_options','control_input_request_seals',
		 'control_input_resolution_events','control_input_request_states','control_runtime_states',
		 'control_commands','control_outbox','control_events',
		 'idx_sessions_credential_id','trg_sessions_credential_insert_guard','trg_sessions_identity_update_guard',
		 'idx_api_keys_enabled_hash','idx_api_keys_expiry','trg_api_keys_identity_update_guard','trg_api_keys_disabled_terminal',
		 'trg_issue_control_revision_on_insert','trg_issue_control_revision_on_update','trg_issue_control_revision_on_delete',
		 'trg_issue_control_revision_guard','trg_issue_control_revision_no_delete',
		 'idx_agent_run_cancellation_cause','trg_agent_run_cancellation_facts_no_update',
		 'trg_agent_run_cancellation_facts_no_delete','trg_agent_run_cancellation_command_guard'
		)
		ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M147 schema ownership: %w", err)
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M147 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M147 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M147 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	return nil
}

func checkAgentRunTelemetryByteBounds(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT id,length(CAST(activity AS BLOB)),length(CAST(estimate_basis AS BLOB))
		FROM agent_run_telemetry
		WHERE length(CAST(activity AS BLOB))>280 OR length(CAST(estimate_basis AS BLOB))>240
		ORDER BY id LIMIT 10`)
	if err != nil {
		return fmt.Errorf("scan telemetry UTF-8 byte bounds: %w", err)
	}
	defer rows.Close()
	var violations []string
	for rows.Next() {
		var id int64
		var activityBytes, basisBytes int
		if err := rows.Scan(&id, &activityBytes, &basisBytes); err != nil {
			return fmt.Errorf("scan telemetry UTF-8 byte-bound row: %w", err)
		}
		violations = append(violations, fmt.Sprintf("id=%d activity_bytes=%d estimate_basis_bytes=%d", id, activityBytes, basisBytes))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate telemetry UTF-8 byte-bound rows: %w", err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("legacy telemetry exceeds the M143 UTF-8 byte bounds; repair the listed rows before upgrading: %s", strings.Join(violations, ", "))
	}
	return nil
}

// checkNoDuplicateIssueNumbers refuses migration 113 if the issues table holds
// any duplicate non-NULL (project_id, issue_number) pair, which would violate
// the unique index the migration creates. NULL project_id rows are excluded:
// the partial index is WHERE project_id IS NOT NULL, and SQLite treats NULLs as
// distinct, so sprint-marker rows (project_id NULL, issue_number 0) are not
// collisions even though a naive GROUP BY would flag them.
func checkNoDuplicateIssueNumbers(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT project_id, issue_number, GROUP_CONCAT(id) AS ids
		FROM issues
		WHERE project_id IS NOT NULL
		GROUP BY project_id, issue_number
		HAVING COUNT(*) > 1
		ORDER BY project_id, issue_number`)
	if err != nil {
		return fmt.Errorf("scan for duplicate issue numbers: %w", err)
	}
	defer rows.Close()

	var dups []string
	for rows.Next() {
		var projectID, issueNumber int
		var ids string
		if err := rows.Scan(&projectID, &issueNumber, &ids); err != nil {
			return fmt.Errorf("scan duplicate row: %w", err)
		}
		dups = append(dups, fmt.Sprintf("project_id=%d issue_number=%d ids=[%s]", projectID, issueNumber, ids))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate duplicate rows: %w", err)
	}
	if len(dups) > 0 {
		return fmt.Errorf("found %d duplicate (project_id, issue_number) group(s) that violate the unique index; "+
			"renumber the offending issues before upgrading:\n  %s",
			len(dups), strings.Join(dups, "\n  "))
	}
	return nil
}

func migrationStepError(version int, step string, err error) error {
	label := step
	if len(label) > 60 {
		label = label[:60]
	}
	return fmt.Errorf("run migration %d step %q: %w", version, label, err)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
