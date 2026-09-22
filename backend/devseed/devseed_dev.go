// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

//go:build dev_login

// PAI-267 — dev fixture seeder. Originally minimal (PAI-267 phase 1),
// extended in PAI-269 with richer per-project surface coverage.
//
// Layered structure (each layer is independently idempotent):
//
//   1. Users — 4 dev users with pinned ids 9001–9004, no password.
//   2. Projects — PAI / ACME / BUGZ / LOGS via natural keys.
//   3. Memberships — the per-user × per-project access matrix.
//   4. Phase-1 issues — 5 issues per project covering the status enum;
//      enough to exercise list filters before the rich seed lands.
//   5. PAI-696 — a complete PAI-1 flagship issue for walkthroughs and
//      capture rehearsal, including prose, estimates, and time entries.
//   6. Phase-2 (PAI-269) — rich per-project content: ACME gets sprints
//      and time entries, BUGZ gets ~100 issues with relations and
//      soft-deletes, LOGS gets long markdown bodies with comment
//      threads. Attachments are deferred (need a working MinIO bucket
//      and bytes to upload — out of scope for a pure-DB seeder).
//
// Idempotency rules:
//   - Users use pinned ids + INSERT OR IGNORE.
//   - Projects use unique keys + INSERT OR IGNORE.
//   - Memberships use INSERT OR REPLACE so a tweaked matrix converges.
//   - Phase-1 + each phase-2 seeder gates on the current per-project
//     issue count, skipping when its target threshold is already met.
//     This means re-running on a partially-seeded DB completes the
//     missing layers without ever growing past the target counts.

package devseed

import (
	"database/sql"
	_ "embed"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

const (
	debugAccountsFlagEnv = "PAIMOS_DEBUG_ACCOUNTS"
	debugPasswordMinLen  = 40
	paiHeroLegacyTitle   = "PAI smoke #1: backlog ticket — high priority"
	paiHeroPAITTitle     = "PAIT smoke #1: backlog ticket — high priority"
	paiHeroTitle         = "Dogfood a complete issue-to-agent walkthrough"
	paiHeroDescription   = `A new contributor should be able to open this seeded issue and understand how Paimos carries intent into agent-ready execution without writing setup data first.

This ticket dogfoods that path. It keeps the problem, acceptance boundary, delivery note, estimate, and booked work together so issue detail, search, reporting, and the AI Workbench all have meaningful synthetic evidence to render.`
	paiHeroAcceptance = `- [x] The issue explains the outcome and why it matters.
- [x] Description, acceptance criteria, notes, and report summary contain realistic synthetic content.
- [x] Hour and LP estimates are present with two completed time entries.
- [ ] Refresh public captures only after the seeded screen has been reviewed.`
	paiHeroNotes         = `Demo fixture only — no production or customer data. Use PAI-1 for local walkthroughs and capture rehearsal; reset or edit it freely.`
	paiHeroReportSummary = `The seeded workspace demonstrates a complete work item from intent and acceptance criteria through estimates, tracked effort, and the agent execution boundary.`
	paiHeroCostUnit      = "Paimos product"
	paiHeroRelease       = "Demo walkthrough"
	paiHeroJiraID        = "PAI-DEMO-1"
	devAdminAvatarFile   = "9001.jpg"
)

//go:embed assets/dev-admin-avatar.jpg
var devAdminAvatar []byte

// devUser is one row in the user matrix. ID is pinned so the
// frontend's playwright selectors can refer to fixture users
// stably across machines.
type devUser struct {
	ID        int64
	Username  string
	Role      string // admin | member | external
	FirstName string
	LastName  string
	Nickname  string
}

// devProject is one fixture project.
type devProject struct {
	Key         string
	Name        string
	Description string
}

// memberRow is one cell of the user × project access matrix.
// Level is the access_level string written to project_members
// (none / viewer / editor) — the project_members.access_level
// CHECK constraint forbids anything else. dev_admin has Role=admin
// globally, so it inherits all-access without explicit memberships;
// only the three non-admin users need rows.
type memberRow struct {
	UserID    int64
	ProjectID int64
	Level     string
}

// debugUser is the normal-login counterpart to the token-only dev_ users.
// These accounts exist for interactive browser QA across roles and are only
// seeded when PAIMOS_DEBUG_ACCOUNTS=1 is explicitly set.
type debugUser struct {
	ID          int64
	Username    string
	PublicRole  string
	PasswordEnv string
	FirstName   string
}

var (
	devUsers = []devUser{
		{ID: 9001, Username: "dev_admin", Role: "admin", FirstName: "Mara", LastName: "Ellis", Nickname: "Mara Ellis"},
		{ID: 9002, Username: "dev_editor", Role: "member"},
		{ID: 9003, Username: "dev_viewer", Role: "member"},
		{ID: 9004, Username: "dev_outsider", Role: "external"},
	}

	devProjects = []devProject{
		{Key: "PAI", Name: "PAIMOS", Description: "PAIMOS RBAC sandbox — dev_admin / dev_editor / dev_viewer / dev_outsider exercise the permissions matrix here."},
		{Key: "ACME", Name: "Acme GmbH", Description: "Commercial customer engagement fixture — billing surface, sprint flows, customer detail."},
		{Key: "BUGZ", Name: "Open-source bug tracker", Description: "List virtualisation, search, bulk ops, trash + restore. Phase-1 carries minimal issues; rich fixture variety is a follow-up."},
		{Key: "LOGS", Name: "Personal-OS captain's log", Description: "Issue detail surfaces — comments, attachments, activity timeline. Phase-1 carries minimal issues; rich fixture variety is a follow-up."},
	}

	debugUsers = []debugUser{
		{ID: 9011, Username: "debug-superadmin", PublicRole: auth.RoleSuperAdmin, PasswordEnv: "PAIMOS_DEBUG_SUPERADMIN_PASSWORD", FirstName: "Debug Superadmin"},
		{ID: 9012, Username: "debug-admin", PublicRole: auth.RoleAdmin, PasswordEnv: "PAIMOS_DEBUG_ADMIN_PASSWORD", FirstName: "Debug Admin"},
		{ID: 9013, Username: "debug-user", PublicRole: auth.RoleMember, PasswordEnv: "PAIMOS_DEBUG_USER_PASSWORD", FirstName: "Debug User"},
		{ID: 9014, Username: "debug-customer", PublicRole: auth.RoleExternal, PasswordEnv: "PAIMOS_DEBUG_CUSTOMER_PASSWORD", FirstName: "Debug Customer"},
	}
)

// Run is the dev-seed entrypoint called from main.go's dev-seed
// subcommand. Wraps the whole seed in a single transaction so a
// partial failure doesn't leave the DB in a confused half-state.
func Run() error {
	tx, err := db.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op if committed

	if err := seedUsers(tx); err != nil {
		return fmt.Errorf("seed users: %w", err)
	}
	projectIDs, err := seedProjects(tx)
	if err != nil {
		return fmt.Errorf("seed projects: %w", err)
	}
	if err := seedMemberships(tx, projectIDs); err != nil {
		return fmt.Errorf("seed memberships: %w", err)
	}
	if err := seedDebugAccounts(tx, projectIDs); err != nil {
		return fmt.Errorf("seed debug accounts: %w", err)
	}
	if err := seedIssues(tx, projectIDs); err != nil {
		return fmt.Errorf("seed issues: %w", err)
	}
	if err := seedRichPAIHero(tx, projectIDs["PAI"]); err != nil {
		return fmt.Errorf("seed rich PAI hero: %w", err)
	}
	// PAI-269: rich per-project content. Each helper has its own
	// gate-on-count check, so re-running is safe and partially-seeded
	// DBs converge to the target shape without duplicating rows.
	if err := seedRichACME(tx, projectIDs["ACME"]); err != nil {
		return fmt.Errorf("seed rich ACME: %w", err)
	}
	if err := seedRichBUGZ(tx, projectIDs["BUGZ"]); err != nil {
		return fmt.Errorf("seed rich BUGZ: %w", err)
	}
	if err := seedRichLOGS(tx, projectIDs["LOGS"]); err != nil {
		return fmt.Errorf("seed rich LOGS: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	log.Printf("dev-seed: %d users, %d projects, memberships + phase-1 floor + rich content — re-run-safe",
		len(devUsers), len(devProjects))
	return nil
}

// seedUsers idempotently inserts dev_admin / dev_editor / dev_viewer /
// dev_outsider with pinned ids. password=” so the normal /api/auth/login
// flow's bcrypt compare always fails — the only way in is dev-login.
func seedUsers(tx *sql.Tx) error {
	for _, u := range devUsers {
		superAdminFlag := 0
		if u.Role == auth.RoleSuperAdmin {
			superAdminFlag = 1
		}
		firstName := u.FirstName
		if firstName == "" {
			firstName = u.Username
		}
		lastName := u.LastName
		if lastName == "" {
			lastName = "Dev"
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO users (
				id, username, password, role, role_key, is_super_admin, status,
				first_name, last_name, nickname, must_change_password
			)
			VALUES (?, ?, '', ?, ?, ?, 'active', ?, ?, ?, 0)
		`, u.ID, u.Username, auth.LegacyRoleForPublicRole(u.Role), u.Role, superAdminFlag, firstName, lastName, u.Nickname)
		if err != nil {
			return fmt.Errorf("insert user %s: %w", u.Username, err)
		}
	}

	// PAI-746 established Mara Ellis as the public synthetic fixture after
	// this ticket's older name suggestion. Converge only the exact legacy
	// seed identity, never a developer's locally edited profile.
	if _, err := tx.Exec(`
		UPDATE users
		SET first_name='Mara', last_name='Ellis', nickname='Mara Ellis'
		WHERE username='dev_admin'
		  AND first_name='dev_admin' AND last_name='Dev' AND nickname=''
	`); err != nil {
		return fmt.Errorf("converge dev_admin identity: %w", err)
	}

	return seedDevAdminAvatar(tx)
}

func seedDevAdminAvatar(tx *sql.Tx) error {
	var avatarPath string
	if err := tx.QueryRow(`SELECT avatar_path FROM users WHERE username='dev_admin'`).Scan(&avatarPath); err != nil {
		return fmt.Errorf("read dev_admin avatar: %w", err)
	}
	if avatarPath != "" {
		return nil
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "/app/data"
	}
	avatarsDir := filepath.Join(dataDir, "avatars")
	if err := os.MkdirAll(avatarsDir, 0o750); err != nil {
		return fmt.Errorf("create fixture avatar directory: %w", err)
	}
	destination := filepath.Join(avatarsDir, devAdminAvatarFile)
	if err := os.WriteFile(destination, devAdminAvatar, 0o600); err != nil {
		return fmt.Errorf("write dev_admin fixture avatar: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE users SET avatar_path=? WHERE username='dev_admin' AND avatar_path=''
	`, "/api/avatars/"+devAdminAvatarFile); err != nil {
		return fmt.Errorf("set dev_admin fixture avatar: %w", err)
	}
	return nil
}

func seedDebugAccounts(tx *sql.Tx, projectIDs map[string]int64) error {
	requested, err := debugAccountsRequested()
	if err != nil {
		return err
	}
	if !requested {
		return nil
	}

	passwords := map[string]string{}
	missing := []string{}
	for _, u := range debugUsers {
		password := os.Getenv(u.PasswordEnv)
		if strings.TrimSpace(password) == "" {
			missing = append(missing, u.PasswordEnv)
			continue
		}
		if len(password) < debugPasswordMinLen {
			return fmt.Errorf("%s must be at least %d characters", u.PasswordEnv, debugPasswordMinLen)
		}
		passwords[u.Username] = password
	}
	if len(missing) > 0 {
		return fmt.Errorf("debug accounts requested but missing %s; run scripts/dev-debug-accounts.sh", strings.Join(missing, ", "))
	}

	for _, u := range debugUsers {
		hash, err := auth.HashPassword(passwords[u.Username])
		if err != nil {
			return fmt.Errorf("hash password for %s: %w", u.Username, err)
		}
		superAdminFlag := 0
		if u.PublicRole == auth.RoleSuperAdmin {
			superAdminFlag = 1
		}
		// Update first within the seed transaction: SQLite runs BEFORE INSERT
		// triggers before resolving an UPSERT conflict, so an existing account
		// would otherwise collide with its own normalized email identity.
		result, err := tx.Exec(`UPDATE users SET
			password=?, role=?, role_key=?, is_super_admin=?, status='active',
			first_name=?, last_name='Debug', email=?, must_change_password=0, locale='en'
			WHERE username=?`, hash, auth.LegacyRoleForPublicRole(u.PublicRole), u.PublicRole, superAdminFlag, u.FirstName, u.Username+"@local.invalid", u.Username)
		if err != nil {
			return fmt.Errorf("update debug user %s: %w", u.Username, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count debug user update %s: %w", u.Username, err)
		}
		if updated == 0 {
			_, err = tx.Exec(`INSERT INTO users (
				id, username, password, role, role_key, is_super_admin, status,
				first_name, last_name, email, must_change_password, locale
			) VALUES (?, ?, ?, ?, ?, ?, 'active', ?, 'Debug', ?, 0, 'en')`,
				u.ID, u.Username, hash, auth.LegacyRoleForPublicRole(u.PublicRole), u.PublicRole, superAdminFlag, u.FirstName, u.Username+"@local.invalid")
		}
		if err != nil {
			return fmt.Errorf("upsert debug user %s: %w", u.Username, err)
		}
		var gotID int64
		if err := tx.QueryRow("SELECT id FROM users WHERE username=?", u.Username).Scan(&gotID); err != nil {
			return fmt.Errorf("resolve debug user %s: %w", u.Username, err)
		}
		if gotID != u.ID {
			return fmt.Errorf("debug user %s has id %d, want pinned id %d", u.Username, gotID, u.ID)
		}
	}
	if err := seedDebugMemberships(tx, projectIDs); err != nil {
		return err
	}
	log.Printf("dev-seed: %d debug accounts seeded from local env (passwords not logged)", len(debugUsers))
	return nil
}

func debugAccountsRequested() (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(debugAccountsFlagEnv))) {
	case "":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be one of 1/true/yes/on or 0/false/no/off", debugAccountsFlagEnv)
	}
}

func seedDebugMemberships(tx *sql.Tx, projectIDs map[string]int64) error {
	idByKey := func(k string) int64 { return projectIDs[k] }
	debugUser := debugUserByName("debug-user").ID
	debugCustomer := debugUserByName("debug-customer").ID

	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO project_members (user_id, project_id, access_level)
		SELECT ?, p.id, 'none'
		FROM projects p
		WHERE p.status != 'deleted'
	`, debugUser); err != nil {
		return fmt.Errorf("deny debug-user on existing projects: %w", err)
	}

	rows := []memberRow{
		{UserID: debugUser, ProjectID: idByKey("PAI"), Level: "editor"},
		{UserID: debugUser, ProjectID: idByKey("ACME"), Level: "editor"},
		{UserID: debugUser, ProjectID: idByKey("BUGZ"), Level: "viewer"},
		{UserID: debugCustomer, ProjectID: idByKey("ACME"), Level: "viewer"},
	}
	for _, r := range rows {
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO project_members (user_id, project_id, access_level)
			VALUES (?, ?, ?)
		`, r.UserID, r.ProjectID, r.Level); err != nil {
			return fmt.Errorf("upsert debug membership user=%d project=%d: %w", r.UserID, r.ProjectID, err)
		}
	}
	return nil
}

// seedProjects idempotently inserts the four fixture projects keyed on
// PAI/ACME/BUGZ/LOGS. Returns project_id-by-key so downstream seeders
// can resolve memberships + issues without re-querying.
func seedProjects(tx *sql.Tx) (map[string]int64, error) {
	if err := normalizeLegacyPAITProject(tx); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, p := range devProjects {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO projects (name, key, description, status)
			VALUES (?, ?, ?, 'active')
		`, p.Name, p.Key, p.Description); err != nil {
			return nil, fmt.Errorf("insert project %s: %w", p.Key, err)
		}
		var id int64
		if err := tx.QueryRow("SELECT id FROM projects WHERE key=?", p.Key).Scan(&id); err != nil {
			return nil, fmt.Errorf("resolve project %s: %w", p.Key, err)
		}
		out[p.Key] = id
	}
	return out, nil
}

func normalizeLegacyPAITProject(tx *sql.Tx) error {
	var paiCount int
	if err := tx.QueryRow("SELECT COUNT(*) FROM projects WHERE key='PAI'").Scan(&paiCount); err != nil {
		return fmt.Errorf("count PAI project: %w", err)
	}
	if paiCount > 0 {
		return nil
	}
	if _, err := tx.Exec(`
		UPDATE projects
		SET key='PAI', name='PAIMOS'
		WHERE key='PAIT'
	`); err != nil {
		return fmt.Errorf("normalize legacy PAIT fixture project: %w", err)
	}
	return nil
}

// seedMemberships writes the per-user × per-project access matrix.
// dev_admin has global role=admin so it does not need explicit rows
// (admin inherits all-access); the other three get explicit grants
// (or absent rows = no access for non-admin).
//
// User × project access matrix (per PAI-267 spec):
//
//	dev_editor:   PAI=editor, ACME=editor, BUGZ=viewer, LOGS=(absent)
//	dev_viewer:   PAI=viewer, ACME=(absent), BUGZ=(absent), LOGS=viewer
//	dev_outsider: all absent (and global role=external means no
//	              auto-grants either)
func seedMemberships(tx *sql.Tx, projectIDs map[string]int64) error {
	idByKey := func(k string) int64 { return projectIDs[k] }
	devEditor := devUserByName("dev_editor").ID
	devViewer := devUserByName("dev_viewer").ID

	rows := []memberRow{
		{UserID: devEditor, ProjectID: idByKey("PAI"), Level: "editor"},
		{UserID: devEditor, ProjectID: idByKey("ACME"), Level: "editor"},
		{UserID: devEditor, ProjectID: idByKey("BUGZ"), Level: "viewer"},
		// dev_editor on LOGS: no row (absent = no access since global role=member
		// only gets editor on projects via explicit grants in this seed model)
		{UserID: devViewer, ProjectID: idByKey("PAI"), Level: "viewer"},
		{UserID: devViewer, ProjectID: idByKey("LOGS"), Level: "viewer"},
		// dev_outsider: no rows at all.
	}
	for _, r := range rows {
		// Use INSERT OR REPLACE so re-running with a tweaked matrix
		// converges instead of erroring on the (user_id, project_id)
		// PRIMARY KEY uniqueness.
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO project_members (user_id, project_id, access_level)
			VALUES (?, ?, ?)
		`, r.UserID, r.ProjectID, r.Level); err != nil {
			return fmt.Errorf("upsert membership user=%d project=%d: %w", r.UserID, r.ProjectID, err)
		}
	}
	return nil
}

// seedIssues creates a small set of fixture issues per project — just
// enough surface for an agent to drive a basic UI walkthrough. Skips a
// project entirely if it already has any issues, so re-running this
// seeder is safe and never grows the issue count past the initial set.
func seedIssues(tx *sql.Tx, projectIDs map[string]int64) error {
	devAdmin := devUserByName("dev_admin").ID
	for _, p := range devProjects {
		pid := projectIDs[p.Key]
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM issues WHERE project_id=?", pid).Scan(&count); err != nil {
			return fmt.Errorf("count issues for %s: %w", p.Key, err)
		}
		if count > 0 {
			continue // already seeded — no duplicate fixtures
		}
		// Minimal variety — varies status / priority / type so each
		// project has at least one row in each list-view filter group.
		issues := []struct {
			Title    string
			Status   string
			Priority string
			Type     string
		}{
			// status enum (per db.go M-status check): new | backlog |
			// in-progress | qa | done | delivered | accepted | invoiced |
			// cancelled. Using a spread so each status filter has at
			// least one fixture row.
			{p.Key + " smoke #1: backlog ticket — high priority", "backlog", "high", "ticket"},
			{p.Key + " smoke #2: in-progress task", "in-progress", "medium", "task"},
			{p.Key + " smoke #3: completed work", "done", "low", "ticket"},
			{p.Key + " smoke #4: cancelled scope", "cancelled", "low", "ticket"},
			{p.Key + " smoke #5: epic with no children", "backlog", "medium", "epic"},
		}
		for i, it := range issues {
			if _, err := tx.Exec(`
				INSERT INTO issues (project_id, title, status, priority, type, issue_number, assignee_id)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, pid, it.Title, it.Status, it.Priority, it.Type, i+1, devAdmin); err != nil {
				return fmt.Errorf("insert issue #%d for %s: %w", i+1, p.Key, err)
			}
		}
	}
	return nil
}

// seedRichPAIHero upgrades only the known synthetic PAI-1 fixture into a
// complete issue walkthrough. Blank fields converge on re-run, while local
// edits survive because populated fields are never overwritten. The two time
// entries use stable comments as their idempotency keys.
func seedRichPAIHero(tx *sql.Tx, pid int64) error {
	if pid == 0 {
		return nil
	}

	var issueID int64
	err := tx.QueryRow(`
		SELECT id
		FROM issues
		WHERE project_id=? AND issue_number=1
		  AND title IN (?, ?, ?)
	`, pid, paiHeroLegacyTitle, paiHeroPAITTitle, paiHeroTitle).Scan(&issueID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve PAI-1 hero fixture: %w", err)
	}

	result, err := tx.Exec(`
		UPDATE issues
		SET title = CASE WHEN title IN (?, ?) THEN ? ELSE title END,
		    description = CASE WHEN TRIM(description) = '' THEN ? ELSE description END,
		    acceptance_criteria = CASE WHEN TRIM(acceptance_criteria) = '' THEN ? ELSE acceptance_criteria END,
		    notes = CASE WHEN TRIM(notes) = '' THEN ? ELSE notes END,
		    report_summary = CASE WHEN TRIM(report_summary) = '' THEN ? ELSE report_summary END,
		    estimate_hours = COALESCE(estimate_hours, 6.0),
		    estimate_lp = COALESCE(estimate_lp, 5.0),
		    ar_hours = COALESCE(ar_hours, 3.0),
		    ar_lp = COALESCE(ar_lp, 3.0),
		    jira_id = CASE WHEN TRIM(jira_id) = '' THEN ? ELSE jira_id END
		WHERE id=? AND (
			title IN (?, ?) OR
			TRIM(description) = '' OR
			TRIM(acceptance_criteria) = '' OR
			TRIM(notes) = '' OR
			TRIM(report_summary) = '' OR
			estimate_hours IS NULL OR
			estimate_lp IS NULL OR
			ar_hours IS NULL OR
			ar_lp IS NULL OR
			TRIM(jira_id) = ''
		)
	`,
		paiHeroLegacyTitle, paiHeroPAITTitle, paiHeroTitle,
		paiHeroDescription, paiHeroAcceptance, paiHeroNotes, paiHeroReportSummary,
		paiHeroJiraID, issueID, paiHeroLegacyTitle, paiHeroPAITTitle,
	)
	if err != nil {
		return fmt.Errorf("enrich PAI-1 hero fixture: %w", err)
	}
	updated, _ := result.RowsAffected()

	labelsInserted := 0
	for _, label := range []struct {
		dimension string
		title     string
	}{
		{"cost_unit", paiHeroCostUnit},
		{"release", paiHeroRelease},
	} {
		inserted, err := seedPAIHeroLabel(tx, pid, issueID, label.dimension, label.title)
		if err != nil {
			return err
		}
		labelsInserted += inserted
	}

	type timeEntrySpec struct {
		daysAgo  int
		duration time.Duration
		comment  string
	}
	entries := []timeEntrySpec{
		{5, 75 * time.Minute, "Mapped the seeded issue anatomy and capture boundary"},
		{3, 105 * time.Minute, "Validated the issue detail and AI Workbench walkthrough"},
	}
	inserted := 0
	for _, entry := range entries {
		started := time.Now().UTC().Add(-time.Duration(entry.daysAgo) * 24 * time.Hour).Truncate(time.Second)
		stopped := started.Add(entry.duration)
		res, err := tx.Exec(`
			INSERT INTO time_entries (issue_id, user_id, started_at, stopped_at, comment)
			SELECT ?, ?, ?, ?, ?
			WHERE NOT EXISTS (
				SELECT 1 FROM time_entries WHERE issue_id=? AND comment=?
			)
		`, issueID, devUserByName("dev_admin").ID,
			started.Format("2006-01-02 15:04:05"), stopped.Format("2006-01-02 15:04:05"), entry.comment,
			issueID, entry.comment)
		if err != nil {
			return fmt.Errorf("insert PAI-1 time entry %q: %w", entry.comment, err)
		}
		rows, _ := res.RowsAffected()
		inserted += int(rows)
	}

	if updated > 0 || labelsInserted > 0 || inserted > 0 {
		log.Printf("dev-seed/PAI: enriched PAI-1 hero fixture + %d labels + %d time entries", labelsInserted, inserted)
	}
	return nil
}

// seedPAIHeroLabel gives the synthetic hero one active cost-unit/release edge
// without replacing a label already chosen in a local dev database. A fresh
// seed creates a real container issue because those relations are the current
// source of truth; a re-run resolves the same container and remains a no-op.
func seedPAIHeroLabel(tx *sql.Tx, projectID, issueID int64, dimension, title string) (int, error) {
	var existing int
	err := tx.QueryRow(`
		SELECT 1
		FROM issue_relations edge
		JOIN issues container ON container.id=edge.source_id
		WHERE edge.target_id=? AND edge.type=? AND container.deleted_at IS NULL
		LIMIT 1
	`, issueID, dimension).Scan(&existing)
	if err == nil {
		return 0, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("resolve PAI-1 %s edge: %w", dimension, err)
	}

	// A deleted container does not render a label, but its edge still occupies
	// the one-per-dimension unique slot. Remove only that unusable edge.
	if _, err := tx.Exec(`DELETE FROM issue_relations WHERE target_id=? AND type=?`, issueID, dimension); err != nil {
		return 0, fmt.Errorf("clear inactive PAI-1 %s edge: %w", dimension, err)
	}

	var containerID int64
	err = tx.QueryRow(`
		SELECT id FROM issues
		WHERE project_id=? AND type=? AND title=? AND deleted_at IS NULL
		ORDER BY id LIMIT 1
	`, projectID, dimension, title).Scan(&containerID)
	created := 0
	if err == sql.ErrNoRows {
		number, numberErr := nextIssueNumber(tx, projectID)
		if numberErr != nil {
			return 0, fmt.Errorf("number PAI-1 %s container: %w", dimension, numberErr)
		}
		result, insertErr := tx.Exec(`
			INSERT INTO issues (project_id, issue_number, type, title, status, priority)
			VALUES (?, ?, ?, ?, 'backlog', 'medium')
		`, projectID, number, dimension, title)
		if insertErr != nil {
			return 0, fmt.Errorf("insert PAI-1 %s container: %w", dimension, insertErr)
		}
		containerID, _ = result.LastInsertId()
		created = 1
	} else if err != nil {
		return 0, fmt.Errorf("resolve PAI-1 %s container: %w", dimension, err)
	}

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO issue_relations (source_id, target_id, type)
		VALUES (?, ?, ?)
	`, containerID, issueID, dimension); err != nil {
		return 0, fmt.Errorf("link PAI-1 %s container: %w", dimension, err)
	}

	// Direct fixture inserts bypass the runtime allocator, so keep its counter
	// beyond the new maximum before a developer creates the next real issue.
	if _, err := tx.Exec(`
		INSERT INTO project_issue_counters(project_id, next_number)
		VALUES (?, (SELECT COALESCE(MAX(issue_number), 0) + 1 FROM issues WHERE project_id=?))
		ON CONFLICT(project_id) DO UPDATE SET
			next_number = MAX(project_issue_counters.next_number, excluded.next_number)
	`, projectID, projectID); err != nil {
		return 0, fmt.Errorf("sync PAI issue counter: %w", err)
	}
	return created, nil
}

func devUserByName(name string) devUser {
	for _, u := range devUsers {
		if u.Username == name {
			return u
		}
	}
	panic("devseed: unknown dev user " + name) // build-time invariant
}

func debugUserByName(name string) debugUser {
	for _, u := range debugUsers {
		if u.Username == name {
			return u
		}
	}
	panic("devseed: unknown debug user " + name) // build-time invariant
}

// nextIssueNumber returns the next per-project issue_number to use,
// computed as MAX(issue_number)+1 inside the same transaction. Each
// rich-seed helper computes the base once and increments locally so
// it doesn't have to round-trip to the DB per insert.
func nextIssueNumber(tx *sql.Tx, projectID int64) (int, error) {
	var n sql.NullInt64
	if err := tx.QueryRow("SELECT MAX(issue_number) FROM issues WHERE project_id=?", projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("max issue_number for project=%d: %w", projectID, err)
	}
	if !n.Valid {
		return 1, nil
	}
	return int(n.Int64) + 1, nil
}

// daysAgo formats a timestamp `n` days before now as the SQLite-flavoured
// `YYYY-MM-DD HH:MM:SS`. Used to keep time-entry rows looking realistic
// regardless of when the seed runs (PAI-269 spec: anchor to today-N so
// reports stay populated as the calendar advances).
func daysAgo(n int) string {
	return time.Now().Add(-time.Duration(n) * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
}

// ── PAI-269 / ACME ─────────────────────────────────────────────────
//
// 3 sprints (one done, one active, one planned) + 25 ticket/task
// issues distributed across them, 15 of which carry 2-3 time entries
// from dev_admin and dev_editor with relative timestamps. Five tickets
// have retainer-style billing fields populated.
//
// Idempotency: gates on "any sprint exists for ACME" — sprints are
// the rich-seed's unique signature, so once they're present the
// helper skips. Phase-1's 5 minimal issues co-exist; the rich seed
// is purely additive.
func seedRichACME(tx *sql.Tx, pid int64) error {
	if pid == 0 {
		return nil
	}
	var sprintCount int
	if err := tx.QueryRow("SELECT COUNT(*) FROM issues WHERE project_id=? AND type='sprint'", pid).Scan(&sprintCount); err != nil {
		return fmt.Errorf("count sprints: %w", err)
	}
	if sprintCount > 0 {
		return nil // rich seed already ran
	}

	devAdmin := devUserByName("dev_admin").ID
	devEditor := devUserByName("dev_editor").ID

	num, err := nextIssueNumber(tx, pid)
	if err != nil {
		return err
	}

	// 1. Three sprints. type='sprint' makes them sprint-typed issues
	//    so the issue_relations type='sprint' joins resolve cleanly.
	type sprintSpec struct {
		title  string
		status string
	}
	sprints := []sprintSpec{
		{"Sprint 26S08 — Onboarding wave", "done"},
		{"Sprint 26S09 — Reporting polish", "in-progress"},
		{"Sprint 26S10 — Q3 retainer goals", "backlog"},
	}
	sprintIDs := make([]int64, 0, len(sprints))
	for _, s := range sprints {
		res, err := tx.Exec(`
			INSERT INTO issues (project_id, title, status, priority, type, issue_number, assignee_id)
			VALUES (?, ?, ?, 'medium', 'sprint', ?, ?)
		`, pid, s.title, s.status, num, devAdmin)
		if err != nil {
			return fmt.Errorf("insert sprint %q: %w", s.title, err)
		}
		id, _ := res.LastInsertId()
		sprintIDs = append(sprintIDs, id)
		num++
	}

	// 2. 25 issues distributed across the sprints. Mix of types, statuses
	//    and priorities so the list view filters all have hits.
	type ticketSpec struct {
		title    string
		typ      string
		status   string
		priority string
	}
	tickets := []ticketSpec{
		{"Onboarding flow: welcome email", "ticket", "done", "high"},
		{"Onboarding flow: avatar upload", "ticket", "done", "medium"},
		{"Onboarding flow: SSO disambiguation", "task", "done", "low"},
		{"Onboarding flow: timezone detection", "task", "done", "low"},
		{"Onboarding flow: i18n strings audit", "ticket", "done", "medium"},
		{"Onboarding flow: empty-state copy review", "task", "done", "low"},
		{"Customer dashboard: revenue tile", "ticket", "in-progress", "high"},
		{"Customer dashboard: utilisation chart", "ticket", "in-progress", "high"},
		{"Customer dashboard: filter bar", "task", "in-progress", "medium"},
		{"Customer dashboard: export CSV", "ticket", "in-progress", "medium"},
		{"Customer dashboard: print stylesheet", "task", "qa", "low"},
		{"Customer dashboard: keyboard shortcuts", "task", "qa", "low"},
		{"Reporting: weekly digest email", "ticket", "in-progress", "medium"},
		{"Reporting: ledger export reconciliation", "ticket", "qa", "high"},
		{"Reporting: invoice line-items", "ticket", "qa", "high"},
		{"Q3 retainer: scope confirmation", "ticket", "backlog", "high"},
		{"Q3 retainer: SLA review", "ticket", "backlog", "high"},
		{"Q3 retainer: monthly check-in cadence", "task", "backlog", "medium"},
		{"Q3 retainer: budget tracker", "ticket", "backlog", "medium"},
		{"Q3 retainer: dashboard refresh", "ticket", "backlog", "low"},
		{"Tech-debt: timer panel reuse", "task", "backlog", "low"},
		{"Tech-debt: search debounce tuning", "task", "backlog", "low"},
		{"Tech-debt: stale dependency upgrade", "task", "backlog", "low"},
		{"Bug: assignee picker drops scroll position", "ticket", "backlog", "medium"},
		{"Bug: sprint board overflow on small viewport", "ticket", "backlog", "medium"},
	}

	// Fixed seed for deterministic distribution across sprints +
	// time-entry assignment.
	rng := rand.New(rand.NewSource(int64(pid)))

	ticketIDs := make([]int64, 0, len(tickets))
	billingApplied := 0
	for i, t := range tickets {
		// Optional retainer billing on the first 5 tickets.
		var billingType, totalBudget, rateHourly any = "", nil, nil
		if billingApplied < 5 {
			billingType = "hourly"
			totalBudget = 2000.0
			rateHourly = 120.0
			billingApplied++
		}
		assignee := devAdmin
		if i%3 == 0 {
			assignee = devEditor
		}
		res, err := tx.Exec(`
			INSERT INTO issues (
				project_id, title, status, priority, type, issue_number,
				assignee_id, billing_type, total_budget, rate_hourly
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, pid, t.title, t.status, t.priority, t.typ, num, assignee,
			billingType, totalBudget, rateHourly)
		if err != nil {
			return fmt.Errorf("insert ACME ticket %q: %w", t.title, err)
		}
		id, _ := res.LastInsertId()
		ticketIDs = append(ticketIDs, id)
		num++

		// 3. Sprint membership via issue_relations type='sprint'.
		//    First 6 → done sprint, next 12 → in-progress, last 7 → backlog.
		var sprintID int64
		switch {
		case i < 6:
			sprintID = sprintIDs[0]
		case i < 18:
			sprintID = sprintIDs[1]
		default:
			sprintID = sprintIDs[2]
		}
		// PAI-753: source is the CONTAINER, target the member — the same
		// direction the `parent` edge uses and the only one
		// ListIssuesByRelation reads (`WHERE ir.source_id = <sprint>`).
		// Seeded backwards, every sprint board renders empty.
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO issue_relations (source_id, target_id, type)
			VALUES (?, ?, 'sprint')
		`, sprintID, id); err != nil {
			return fmt.Errorf("link ACME ticket=%d sprint=%d: %w", id, sprintID, err)
		}

		// 4. Time entries on the first 15 tickets — 2-3 entries each
		//    distributed over the past 30 days, alternating between
		//    dev_admin and dev_editor so the report shows multiple users.
		if i < 15 {
			entries := 2 + rng.Intn(2) // 2 or 3 entries
			for e := 0; e < entries; e++ {
				user := devAdmin
				if e%2 == 1 {
					user = devEditor
				}
				start := daysAgo(28 - i - e*3)   // spread out chronologically
				stopOffset := 30 + rng.Intn(150) // 30–180 minutes per entry
				stop := time.Now().Add(-time.Duration(28-i-e*3) * 24 * time.Hour).
					Add(time.Duration(stopOffset) * time.Minute).UTC().
					Format("2006-01-02 15:04:05")
				if _, err := tx.Exec(`
					INSERT INTO time_entries (issue_id, user_id, started_at, stopped_at, comment)
					VALUES (?, ?, ?, ?, ?)
				`, id, user, start, stop, fmt.Sprintf("seed entry %d/%d", e+1, entries)); err != nil {
					return fmt.Errorf("insert time entry for ACME ticket=%d: %w", id, err)
				}
			}
		}
	}
	_ = ticketIDs // reserved for future cross-issue relation seeding
	log.Printf("dev-seed/ACME: 3 sprints + %d tickets + time entries on first 15", len(tickets))
	return nil
}

// ── PAI-269 / BUGZ ─────────────────────────────────────────────────
//
// 100 issues with status / priority / type variety, 5 epics with 3-4
// children each (parent_id chain), 5 soft-deleted rows for trash
// flow testing, and a sprinkling of depends_on + blocks relations
// between random issue pairs so the relations graph has structure.
//
// Idempotency: gates on issue count >= 100. Re-running on a partially
// seeded BUGZ converges to exactly the target.
func seedRichBUGZ(tx *sql.Tx, pid int64) error {
	if pid == 0 {
		return nil
	}
	var existing int
	if err := tx.QueryRow("SELECT COUNT(*) FROM issues WHERE project_id=?", pid).Scan(&existing); err != nil {
		return fmt.Errorf("count BUGZ: %w", err)
	}
	if existing >= 100 {
		return nil
	}

	devAdmin := devUserByName("dev_admin").ID
	devEditor := devUserByName("dev_editor").ID
	devViewer := devUserByName("dev_viewer").ID
	users := []int64{devAdmin, devEditor, devViewer}

	num, err := nextIssueNumber(tx, pid)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(int64(pid)))

	statuses := []string{"new", "backlog", "in-progress", "qa", "done", "cancelled"}
	priorities := []string{"low", "medium", "high"}
	types := []string{"ticket", "task", "ticket", "ticket"} // weighted toward ticket

	// 1. Five epics first so they get low issue_numbers + can be
	//    parents for subsequent children.
	type epicSpec struct {
		title string
	}
	epics := []epicSpec{
		{"epic: search rewrite"},
		{"epic: virtualised list view"},
		{"epic: bulk operations toolkit"},
		{"epic: trash + restore flow"},
		{"epic: relations graph view"},
	}
	epicIDs := make([]int64, 0, len(epics))
	for _, e := range epics {
		res, err := tx.Exec(`
			INSERT INTO issues (project_id, title, status, priority, type, issue_number, assignee_id)
			VALUES (?, ?, 'in-progress', 'high', 'epic', ?, ?)
		`, pid, e.title, num, devAdmin)
		if err != nil {
			return fmt.Errorf("insert BUGZ epic %q: %w", e.title, err)
		}
		id, _ := res.LastInsertId()
		epicIDs = append(epicIDs, id)
		num++
	}

	// 2. Children — 3 to 4 per epic, with parent_id wired up so the
	//    issue tree view has structure.
	for _, eid := range epicIDs {
		childCount := 3 + rng.Intn(2)
		for c := 0; c < childCount; c++ {
			res, err := tx.Exec(`
				INSERT INTO issues (project_id, title, status, priority, type, issue_number, assignee_id)
				VALUES (?, ?, ?, ?, 'ticket', ?, ?)
			`, pid,
				fmt.Sprintf("child of #%d — sub-task %d", eid, c+1),
				statuses[rng.Intn(len(statuses))],
				priorities[rng.Intn(len(priorities))],
				num,
				users[rng.Intn(len(users))])
			if err != nil {
				return fmt.Errorf("insert BUGZ child of epic=%d: %w", eid, err)
			}
			num++
			// PAI-584 P6: hierarchy is the `parent` edge (parent_id dropped).
			childID, _ := res.LastInsertId()
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO issue_relations(source_id, target_id, type) VALUES(?,?,'parent')`,
				eid, childID); err != nil {
				return fmt.Errorf("insert BUGZ child parent edge epic=%d: %w", eid, err)
			}
		}
	}

	// 3. Bulk fill: independent issues until we cross the 100-row
	//    floor. After this loop, BUGZ has at least 100 issues.
	bulkIDs := make([]int64, 0, 100)
	for {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM issues WHERE project_id=?", pid).Scan(&n); err != nil {
			return fmt.Errorf("count BUGZ during bulk fill: %w", err)
		}
		if n >= 100 {
			break
		}
		title := fmt.Sprintf("bug #%d — %s", num, []string{
			"crash on resize",
			"flicker on hover",
			"empty-state copy",
			"date picker off-by-one",
			"validation message overflow",
			"focus trap regression",
			"keyboard shortcut leak",
			"a11y label missing",
			"theme contrast",
			"localisation drift",
		}[rng.Intn(10)])
		res, err := tx.Exec(`
			INSERT INTO issues (project_id, title, status, priority, type, issue_number, assignee_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, pid, title,
			statuses[rng.Intn(len(statuses))],
			priorities[rng.Intn(len(priorities))],
			types[rng.Intn(len(types))],
			num,
			users[rng.Intn(len(users))])
		if err != nil {
			return fmt.Errorf("insert BUGZ bulk: %w", err)
		}
		id, _ := res.LastInsertId()
		bulkIDs = append(bulkIDs, id)
		num++
	}

	// 4. Soft-delete 5 of the bulk issues so the trash + restore flow
	//    has rows. Pre-PAI-269 schema has issues.deleted_at + deleted_by;
	//    setting deleted_at is the soft-delete signal.
	deletedAt := daysAgo(7)
	for i := 0; i < 5 && i < len(bulkIDs); i++ {
		if _, err := tx.Exec(`
			UPDATE issues SET deleted_at=?, deleted_by=? WHERE id=?
		`, deletedAt, devAdmin, bulkIDs[i]); err != nil {
			return fmt.Errorf("soft-delete BUGZ id=%d: %w", bulkIDs[i], err)
		}
	}

	// 5. Cross-issue relations — 8 depends_on + 4 blocks pairs picked
	//    randomly from the bulk pool. Skip self-edges + duplicates
	//    via INSERT OR IGNORE on the (source, target, type) PK.
	pickPair := func() (int64, int64) {
		i := rng.Intn(len(bulkIDs))
		j := rng.Intn(len(bulkIDs))
		for j == i {
			j = rng.Intn(len(bulkIDs))
		}
		return bulkIDs[i], bulkIDs[j]
	}
	for i := 0; i < 8; i++ {
		s, t := pickPair()
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO issue_relations (source_id, target_id, type)
			VALUES (?, ?, 'depends_on')
		`, s, t); err != nil {
			return fmt.Errorf("insert BUGZ depends_on: %w", err)
		}
	}
	for i := 0; i < 4; i++ {
		s, t := pickPair()
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO issue_relations (source_id, target_id, type)
			VALUES (?, ?, 'blocks')
		`, s, t); err != nil {
			return fmt.Errorf("insert BUGZ blocks: %w", err)
		}
	}

	log.Printf("dev-seed/BUGZ: %d epics + children + bulk to 100, 5 soft-deleted, relations seeded", len(epicIDs))
	return nil
}

// ── PAI-269 / LOGS ─────────────────────────────────────────────────
//
// 10 issues with multi-paragraph markdown descriptions and 5 comments
// per issue from dev_admin + dev_viewer alternating. Attachments are
// deferred (need MinIO bytes upload, out of scope for a pure-DB
// seeder); exercising the comments + activity surface is the
// phase-2 goal here.
//
// Idempotency: gates on issue count >= 10.
func seedRichLOGS(tx *sql.Tx, pid int64) error {
	if pid == 0 {
		return nil
	}
	var existing int
	if err := tx.QueryRow("SELECT COUNT(*) FROM issues WHERE project_id=?", pid).Scan(&existing); err != nil {
		return fmt.Errorf("count LOGS: %w", err)
	}
	if existing >= 10 {
		return nil
	}

	devAdmin := devUserByName("dev_admin").ID
	devViewer := devUserByName("dev_viewer").ID

	num, err := nextIssueNumber(tx, pid)
	if err != nil {
		return err
	}

	// Markdown body shared across LOGS issues — keeps the seed
	// compact while exercising headings, lists, code blocks and
	// blockquotes in the issue-detail markdown renderer.
	body := strings.Join([]string{
		"# Daily log entry",
		"",
		"## Context",
		"",
		"Captured during the agent's continuous review pass — see the",
		"upstream tracking for the broader narrative.",
		"",
		"## What happened",
		"",
		"- Observed a regression in the assignee picker (sticky scroll).",
		"- Reproduced via `cmd-K → assignee → arrow-down × 12`.",
		"- Confirmed unrelated to the search-debounce work.",
		"",
		"```ts",
		"// Reproduction snippet",
		"const list = await api.get<User[]>('/users')",
		"console.log(list.length)",
		"```",
		"",
		"> **Working theory:** the virtualised list reuses scroll offsets",
		"> across mounts — see also the earlier list-virtualisation epic.",
		"",
		"## Next",
		"",
		"1. Pin the regression with a screenshot.",
		"2. File a focused ticket once isolated.",
		"3. Land a fix in the next sprint window.",
	}, "\n")

	want := 10 - existing
	for i := 0; i < want; i++ {
		title := fmt.Sprintf("captain's log: entry #%d — daily review", num)
		res, err := tx.Exec(`
			INSERT INTO issues (
				project_id, title, description, status, priority, type, issue_number, assignee_id
			) VALUES (?, ?, ?, ?, ?, 'ticket', ?, ?)
		`, pid, title, body,
			[]string{"backlog", "in-progress", "done"}[i%3],
			[]string{"low", "medium", "high"}[i%3],
			num, devAdmin)
		if err != nil {
			return fmt.Errorf("insert LOGS issue: %w", err)
		}
		id, _ := res.LastInsertId()
		num++

		// 5 comments per issue, alternating authors so threading reads
		// like a real conversation. Created-at offsets keep them in
		// chronological order.
		commentTexts := []string{
			"Initial dump from the daily review pass — pinging here so we have a paper trail.",
			"Quick triage: this looks adjacent to the search-debounce work. Worth tracing the call graph before we file a separate ticket.",
			"Followed up on the working theory — the virtualised list is indeed retaining offsets across mounts. Reproducible.",
			"OK — going to fold this into the next list-virtualisation epic milestone instead of a one-off fix.",
			"Closing the loop. Resolution + screenshot are in the linked sprint review.",
		}
		for c, txt := range commentTexts {
			author := devAdmin
			if c%2 == 1 {
				author = devViewer
			}
			created := daysAgo(7 - c) // newer comments closer to today
			if _, err := tx.Exec(`
				INSERT INTO comments (issue_id, author_id, body, created_at)
				VALUES (?, ?, ?, ?)
			`, id, author, txt, created); err != nil {
				return fmt.Errorf("insert LOGS comment: %w", err)
			}
		}
	}

	log.Printf("dev-seed/LOGS: %d issues with rich markdown + 5-comment threads each", want)
	return nil
}
