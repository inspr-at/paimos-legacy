// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

//go:build dev_login

package devseed_test

import (
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/devseed"
	"github.com/inspr-at/paimos/backend/internal/testdb"
)

func TestRun_SeedsSyntheticDevAdminIdentityAndAvatar(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("PAIMOS_TEST_MODE", "1")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})

	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var firstName, lastName, nickname, avatarPath string
	if err := db.DB.QueryRow(`
		SELECT first_name, last_name, nickname, avatar_path
		FROM users WHERE username='dev_admin'
	`).Scan(&firstName, &lastName, &nickname, &avatarPath); err != nil {
		t.Fatalf("read dev_admin: %v", err)
	}
	if firstName != "Mara" || lastName != "Ellis" || nickname != "Mara Ellis" {
		t.Fatalf("dev_admin identity = %q %q / %q, want Mara Ellis", firstName, lastName, nickname)
	}
	if avatarPath != "/api/avatars/9001.jpg" {
		t.Fatalf("avatar_path = %q, want /api/avatars/9001.jpg", avatarPath)
	}

	avatar, err := os.Open(filepath.Join(dataDir, "avatars", "9001.jpg"))
	if err != nil {
		t.Fatalf("open seeded avatar: %v", err)
	}
	defer avatar.Close()
	config, err := jpeg.DecodeConfig(avatar)
	if err != nil {
		t.Fatalf("decode seeded avatar: %v", err)
	}
	if config.Width != 256 || config.Height != 256 {
		t.Fatalf("seeded avatar = %dx%d, want 256x256", config.Width, config.Height)
	}

	if _, err := db.DB.Exec(`
		UPDATE users
		SET first_name='Local', last_name='Operator', nickname='LO', avatar_path='/api/avatars/custom.jpg'
		WHERE username='dev_admin'
	`); err != nil {
		t.Fatalf("customize dev_admin: %v", err)
	}
	if err := devseed.Run(); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if err := db.DB.QueryRow(`
		SELECT first_name, last_name, nickname, avatar_path
		FROM users WHERE username='dev_admin'
	`).Scan(&firstName, &lastName, &nickname, &avatarPath); err != nil {
		t.Fatalf("read customized dev_admin: %v", err)
	}
	if firstName != "Local" || lastName != "Operator" || nickname != "LO" || avatarPath != "/api/avatars/custom.jpg" {
		t.Fatalf("re-seed replaced local profile: %q %q / %q / %q", firstName, lastName, nickname, avatarPath)
	}
}

// TestRun_Idempotency pins the PAI-267 contract: re-running dev-seed
// is safe and never grows the row counts past the initial set. This
// is the property the `just dev-up` recipe relies on — boot of an
// existing dev environment must not mint duplicate fixtures.
func TestRun_Idempotency(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})

	// First seed
	if err := devseed.Run(); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	users1 := count(t, "SELECT COUNT(*) FROM users WHERE username LIKE 'dev_%'")
	projects1 := count(t, "SELECT COUNT(*) FROM projects WHERE key IN ('PAI','ACME','BUGZ','LOGS')")
	memberships1 := count(t, "SELECT COUNT(*) FROM project_members WHERE user_id IN (9001,9002,9003,9004)")
	issues1 := count(t, "SELECT COUNT(*) FROM issues WHERE project_id IN (SELECT id FROM projects WHERE key IN ('PAI','ACME','BUGZ','LOGS'))")

	if users1 != 4 {
		t.Errorf("first run: users count = %d, want 4", users1)
	}
	if projects1 != 4 {
		t.Errorf("first run: projects count = %d, want 4", projects1)
	}
	// PAI-269: phase-1 + phase-2 totals.
	//   PAI   =   7  (phase-1's 5 + hero cost-unit/release containers)
	//   ACME  =  33  (phase-1's 5 + 3 sprints + 25 rich tickets)
	//   BUGZ  = 100  (phase-2 fills to 100 regardless of phase-1 floor)
	//   LOGS  =  10  (phase-2 fills to 10)
	const wantIssues = 150
	if issues1 != wantIssues {
		t.Errorf("first run: issues count = %d, want %d (PAI 7 + ACME 33 + BUGZ 100 + LOGS 10)", issues1, wantIssues)
	}

	// Second seed — must be a no-op
	if err := devseed.Run(); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	users2 := count(t, "SELECT COUNT(*) FROM users WHERE username LIKE 'dev_%'")
	projects2 := count(t, "SELECT COUNT(*) FROM projects WHERE key IN ('PAI','ACME','BUGZ','LOGS')")
	memberships2 := count(t, "SELECT COUNT(*) FROM project_members WHERE user_id IN (9001,9002,9003,9004)")
	issues2 := count(t, "SELECT COUNT(*) FROM issues WHERE project_id IN (SELECT id FROM projects WHERE key IN ('PAI','ACME','BUGZ','LOGS'))")

	if users2 != users1 {
		t.Errorf("re-run grew users: %d → %d", users1, users2)
	}
	if projects2 != projects1 {
		t.Errorf("re-run grew projects: %d → %d", projects1, projects2)
	}
	if memberships2 != memberships1 {
		t.Errorf("re-run grew memberships: %d → %d", memberships1, memberships2)
	}
	if issues2 != issues1 {
		t.Errorf("re-run grew issues: %d → %d", issues1, issues2)
	}
}

func TestRun_RenamesLegacyPAITFixtureProject(t *testing.T) {
	openDevseedTestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO projects(name, key, description, status) VALUES('Paimos Testing', 'PAIT', 'legacy fixture', 'active')`); err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}
	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := count(t, "SELECT COUNT(*) FROM projects WHERE key='PAIT'"); got != 0 {
		t.Fatalf("legacy PAIT projects = %d, want 0", got)
	}
	if got := count(t, "SELECT COUNT(*) FROM projects WHERE key='PAI'"); got != 1 {
		t.Fatalf("PAI projects = %d, want 1", got)
	}
}

func TestRun_PAIHeroFixture(t *testing.T) {
	openDevseedTestDB(t)
	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var issueID int64
	var title, description, acceptance, notes, reportSummary, jiraID string
	var estimateHours, estimateLP, arHours, arLP float64
	if err := db.DB.QueryRow(`
		SELECT id, title, description, acceptance_criteria, notes,
		       report_summary, estimate_hours, estimate_lp, ar_hours, ar_lp, jira_id
		FROM issues
		WHERE project_id=(SELECT id FROM projects WHERE key='PAI')
		  AND issue_number=1
	`).Scan(
		&issueID, &title, &description, &acceptance, &notes,
		&reportSummary, &estimateHours, &estimateLP, &arHours, &arLP, &jiraID,
	); err != nil {
		t.Fatalf("query PAI-1: %v", err)
	}

	if title != "Dogfood a complete issue-to-agent walkthrough" {
		t.Errorf("PAI-1 title = %q", title)
	}
	for name, value := range map[string]string{
		"description": description,
		"acceptance":  acceptance,
		"notes":       notes,
		"report":      reportSummary,
	} {
		if strings.TrimSpace(value) == "" {
			t.Errorf("PAI-1 %s is blank", name)
		}
	}
	if estimateHours != 6 || estimateLP != 5 {
		t.Errorf("PAI-1 estimates = %.2fh / %.2f LP, want 6h / 5 LP", estimateHours, estimateLP)
	}
	if arHours != 3 || arLP != 3 {
		t.Errorf("PAI-1 AR = %.2fh / %.2f LP, want 3h / 3 LP", arHours, arLP)
	}
	if jiraID != "PAI-DEMO-1" {
		t.Errorf("PAI-1 Jira ID = %q, want PAI-DEMO-1", jiraID)
	}
	for dimension, want := range map[string]string{
		"cost_unit": "Paimos product",
		"release":   "Demo walkthrough",
	} {
		var got string
		if err := db.DB.QueryRow(`
			SELECT container.title
			FROM issue_relations edge
			JOIN issues container ON container.id=edge.source_id
			WHERE edge.target_id=? AND edge.type=?
		`, issueID, dimension).Scan(&got); err != nil {
			t.Errorf("query PAI-1 %s: %v", dimension, err)
		} else if got != want {
			t.Errorf("PAI-1 %s = %q, want %q", dimension, got, want)
		}
	}
	if got := count(t, "SELECT COUNT(*) FROM time_entries WHERE issue_id=?", issueID); got != 2 {
		t.Errorf("PAI-1 time entries = %d, want 2", got)
	}
	if got := count(t, "SELECT COUNT(*) FROM time_entries WHERE issue_id=? AND stopped_at IS NOT NULL AND comment != ''", issueID); got != 2 {
		t.Errorf("PAI-1 complete time entries = %d, want 2", got)
	}

	const localDescription = "Local fixture edit that must survive a seed re-run."
	if _, err := db.DB.Exec("UPDATE issues SET description=? WHERE id=?", localDescription, issueID); err != nil {
		t.Fatalf("customize PAI-1: %v", err)
	}
	if err := devseed.Run(); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	var gotDescription string
	if err := db.DB.QueryRow("SELECT description FROM issues WHERE id=?", issueID).Scan(&gotDescription); err != nil {
		t.Fatalf("query customized PAI-1: %v", err)
	}
	if gotDescription != localDescription {
		t.Errorf("second Run replaced local description: got %q", gotDescription)
	}
	if got := count(t, "SELECT COUNT(*) FROM time_entries WHERE issue_id=?", issueID); got != 2 {
		t.Errorf("second Run grew PAI-1 time entries to %d, want 2", got)
	}
}

// TestRun_PinnedUserIDs pins the playwright contract: dev_admin /
// dev_editor / dev_viewer / dev_outsider get ids 9001–9004 in that
// order so test selectors can refer to them stably across machines.
func TestRun_PinnedUserIDs(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cases := []struct {
		username string
		wantID   int64
		wantRole string
	}{
		{"dev_admin", 9001, "admin"},
		{"dev_editor", 9002, "member"},
		{"dev_viewer", 9003, "member"},
		{"dev_outsider", 9004, "external"},
	}
	for _, c := range cases {
		var id int64
		var role string
		if err := db.DB.QueryRow("SELECT id, role FROM users WHERE username=?", c.username).Scan(&id, &role); err != nil {
			t.Errorf("%s: %v", c.username, err)
			continue
		}
		if id != c.wantID {
			t.Errorf("%s: id = %d, want %d", c.username, id, c.wantID)
		}
		if role != c.wantRole {
			t.Errorf("%s: role = %q, want %q", c.username, role, c.wantRole)
		}
	}
}

// TestRun_PasswordsAreEmpty pins the security invariant: dev users
// MUST have an empty password column so the normal login form's
// bcrypt compare fails. The dev-login route (token-protected) is the
// only valid way in.
func TestRun_PasswordsAreEmpty(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rows, err := db.DB.Query("SELECT username, password FROM users WHERE username LIKE 'dev_%'")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, pw string
		if err := rows.Scan(&name, &pw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if pw != "" {
			t.Errorf("%s has non-empty password (%q) — normal login form must not be able to authenticate dev users", name, pw)
		}
	}
}

func TestRun_DebugAccountsOptIn(t *testing.T) {
	openDevseedTestDB(t)
	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := count(t, "SELECT COUNT(*) FROM users WHERE username LIKE 'debug-%'"); got != 0 {
		t.Fatalf("debug accounts without opt-in = %d, want 0", got)
	}
}

func TestRun_DebugAccountsRequirePasswords(t *testing.T) {
	openDevseedTestDB(t)
	t.Setenv("PAIMOS_DEBUG_ACCOUNTS", "1")
	t.Setenv("PAIMOS_DEBUG_SUPERADMIN_PASSWORD", strings.Repeat("s", 48))

	err := devseed.Run()
	if err == nil {
		t.Fatal("Run returned nil with missing debug account passwords")
	}
	if !strings.Contains(err.Error(), "PAIMOS_DEBUG_ADMIN_PASSWORD") {
		t.Fatalf("Run error = %q, want missing admin password env", err.Error())
	}
}

func TestRun_DebugAccountsSeededFromEnv(t *testing.T) {
	openDevseedTestDB(t)
	passwords := map[string]string{
		"debug-superadmin": "superadmin-local-debug-password-000000000000000000000000000001",
		"debug-admin":      "admin-local-debug-password-000000000000000000000000000000001",
		"debug-user":       "user-local-debug-password-0000000000000000000000000000000001",
		"debug-customer":   "customer-local-debug-password-000000000000000000000000000001",
	}
	t.Setenv("PAIMOS_DEBUG_ACCOUNTS", "1")
	t.Setenv("PAIMOS_DEBUG_SUPERADMIN_PASSWORD", passwords["debug-superadmin"])
	t.Setenv("PAIMOS_DEBUG_ADMIN_PASSWORD", passwords["debug-admin"])
	t.Setenv("PAIMOS_DEBUG_USER_PASSWORD", passwords["debug-user"])
	t.Setenv("PAIMOS_DEBUG_CUSTOMER_PASSWORD", passwords["debug-customer"])

	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO projects(name, key, description, status) VALUES('Extra Project', 'EXTR', '', 'active')`); err != nil {
		t.Fatalf("insert extra project: %v", err)
	}
	if err := devseed.Run(); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	cases := []struct {
		username       string
		wantID         int64
		wantRole       string
		wantRoleKey    string
		wantSuperAdmin int
	}{
		{"debug-superadmin", 9011, "admin", "super_admin", 1},
		{"debug-admin", 9012, "admin", "admin", 0},
		{"debug-user", 9013, "member", "member", 0},
		{"debug-customer", 9014, "external", "external", 0},
	}
	for _, c := range cases {
		var id int64
		var role, roleKey, hash string
		var superAdmin, mustChange int
		if err := db.DB.QueryRow(`
			SELECT id, role, role_key, is_super_admin, password, must_change_password
			FROM users WHERE username=?
		`, c.username).Scan(&id, &role, &roleKey, &superAdmin, &hash, &mustChange); err != nil {
			t.Errorf("%s: %v", c.username, err)
			continue
		}
		if id != c.wantID {
			t.Errorf("%s: id = %d, want %d", c.username, id, c.wantID)
		}
		if role != c.wantRole || roleKey != c.wantRoleKey || superAdmin != c.wantSuperAdmin {
			t.Errorf("%s: role/role_key/is_super_admin = %q/%q/%d, want %q/%q/%d",
				c.username, role, roleKey, superAdmin, c.wantRole, c.wantRoleKey, c.wantSuperAdmin)
		}
		if mustChange != 0 {
			t.Errorf("%s: must_change_password = %d, want 0", c.username, mustChange)
		}
		if !auth.CheckPassword(hash, passwords[c.username]) {
			t.Errorf("%s: stored password hash did not verify", c.username)
		}
	}

	assertMembership(t, "debug-user", "PAI", "editor")
	assertMembership(t, "debug-user", "ACME", "editor")
	assertMembership(t, "debug-user", "BUGZ", "viewer")
	assertMembership(t, "debug-user", "LOGS", "none")
	assertMembership(t, "debug-user", "EXTR", "none")
	assertMembership(t, "debug-customer", "ACME", "viewer")
	assertNoMembership(t, "debug-customer", "PAI")
	assertNoMembership(t, "debug-customer", "EXTR")
}

func TestRun_DebugAccountsRejectOtherEmailOwner(t *testing.T) {
	for _, existingDebugAccount := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_debug_account_%t", existingDebugAccount), func(t *testing.T) {
			openDevseedTestDB(t)
			t.Setenv("PAIMOS_DEBUG_ACCOUNTS", "1")
			for _, name := range []string{"SUPERADMIN", "ADMIN", "USER", "CUSTOMER"} {
				t.Setenv("PAIMOS_DEBUG_"+name+"_PASSWORD", strings.Repeat("test-only-password-", 3))
			}
			if existingDebugAccount {
				if err := devseed.Run(); err != nil {
					t.Fatal(err)
				}
				if _, err := db.DB.Exec(`UPDATE users SET email='previous-debug-owner@local.invalid' WHERE username='debug-superadmin'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.DB.Exec(`INSERT INTO users(username,password,role,email) VALUES('other-email-owner','unchanged','member',' DEBUG-SUPERADMIN@local.invalid ')`); err != nil {
				t.Fatal(err)
			}
			if err := devseed.Run(); err == nil || !strings.Contains(err.Error(), "email must uniquely identify one user") {
				t.Fatalf("seed with another email owner must fail, got %v", err)
			}
			if got := count(t, `SELECT COUNT(*) FROM users WHERE username='other-email-owner' AND password='unchanged' AND role='member'`); got != 1 {
				t.Fatal("failed seed changed the existing email owner")
			}
		})
	}
}

func count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func openDevseedTestDB(t *testing.T) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
}

func assertMembership(t *testing.T, username, projectKey, want string) {
	t.Helper()
	var got string
	if err := db.DB.QueryRow(`
		SELECT pm.access_level
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		JOIN projects p ON p.id = pm.project_id
		WHERE u.username=? AND p.key=?
	`, username, projectKey).Scan(&got); err != nil {
		t.Fatalf("%s/%s membership: %v", username, projectKey, err)
	}
	if got != want {
		t.Fatalf("%s/%s membership = %q, want %q", username, projectKey, got, want)
	}
}

func assertNoMembership(t *testing.T, username, projectKey string) {
	t.Helper()
	var got int
	if err := db.DB.QueryRow(`
		SELECT COUNT(*)
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		JOIN projects p ON p.id = pm.project_id
		WHERE u.username=? AND p.key=?
	`, username, projectKey).Scan(&got); err != nil {
		t.Fatalf("%s/%s membership count: %v", username, projectKey, err)
	}
	if got != 0 {
		t.Fatalf("%s/%s membership count = %d, want 0", username, projectKey, got)
	}
}

// TestRun_RichFixtures pins the PAI-269 phase-2 surface assertions:
// ACME has 3 sprints + time entries; BUGZ has soft-deleted rows +
// depends_on / blocks relations; LOGS has comments. These are the
// signature rows the dev-up walkthrough relies on — without them the
// reporting / trash / relation / comment surfaces have nothing to
// render.
func TestRun_RichFixtures(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	if err := devseed.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ACME — 3 sprint-typed issues.
	acmeSprints := count(t, `
		SELECT COUNT(*) FROM issues
		WHERE type='sprint' AND project_id=(SELECT id FROM projects WHERE key='ACME')
	`)
	if acmeSprints != 3 {
		t.Errorf("ACME sprints: got %d, want 3", acmeSprints)
	}

	// ACME — at least one time entry on a project issue. Reporting +
	// billing surfaces need this to render anything meaningful.
	acmeTimeEntries := count(t, `
		SELECT COUNT(*) FROM time_entries
		WHERE issue_id IN (SELECT id FROM issues WHERE project_id=(SELECT id FROM projects WHERE key='ACME'))
	`)
	if acmeTimeEntries < 15 {
		t.Errorf("ACME time entries: got %d, want at least 15 (2-3 entries × 15 tickets)", acmeTimeEntries)
	}

	// BUGZ — at least 5 soft-deleted issues for the trash + restore flow.
	bugzDeleted := count(t, `
		SELECT COUNT(*) FROM issues
		WHERE deleted_at IS NOT NULL AND project_id=(SELECT id FROM projects WHERE key='BUGZ')
	`)
	if bugzDeleted < 5 {
		t.Errorf("BUGZ soft-deleted: got %d, want at least 5", bugzDeleted)
	}

	// BUGZ — depends_on + blocks relations between project issues.
	bugzRelations := count(t, `
		SELECT COUNT(*) FROM issue_relations
		WHERE type IN ('depends_on','blocks')
		  AND source_id IN (SELECT id FROM issues WHERE project_id=(SELECT id FROM projects WHERE key='BUGZ'))
	`)
	if bugzRelations < 8 {
		t.Errorf("BUGZ depends_on+blocks relations: got %d, want at least 8", bugzRelations)
	}

	// LOGS — at least 5 comments per issue × 5 newly-seeded issues.
	logsComments := count(t, `
		SELECT COUNT(*) FROM comments
		WHERE issue_id IN (SELECT id FROM issues WHERE project_id=(SELECT id FROM projects WHERE key='LOGS'))
	`)
	if logsComments < 25 {
		t.Errorf("LOGS comments: got %d, want at least 25 (5 comments × 5 phase-2 issues)", logsComments)
	}

	// LOGS issues have non-trivial markdown bodies (the seeder feeds a
	// shared multi-paragraph body — at minimum it should be longer
	// than the 5-issue phase-1 floor's empty-string default).
	var bodyMin int
	if err := db.DB.QueryRow(`
		SELECT MIN(LENGTH(description)) FROM issues
		WHERE project_id=(SELECT id FROM projects WHERE key='LOGS')
		  AND type != 'sprint'
		  AND description != ''
	`).Scan(&bodyMin); err == nil && bodyMin < 200 {
		// Phase-1 issues have empty descriptions, so we filter description != ''.
		// Among the rich-seed issues, the shared body is several hundred chars.
		t.Errorf("LOGS rich-seed body: shortest non-empty description is %d chars, want at least 200", bodyMin)
	}
}
