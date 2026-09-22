// PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

func TestReviewerProductionRouterScopesHumanToExplicitViewerProjects(t *testing.T) {
	openSeedTestDB(t)
	router := sessionHomeProductionRouter()
	admin := seedSessionHomeRouterUser(t, "reviewer-operator", "admin", "active", false)
	adminCookie := seedSessionHomeRouterCookie(t, admin, "000000001054")
	request := func(method, path, cookie, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set(auth.CSRFHeaderName, "csrf")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	project := func(name, key string) int64 {
		t.Helper()
		res, err := db.DB.Exec("INSERT INTO projects(name,key) VALUES(?,?)", name, key)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	production := project("Production confidential", "PRIV")
	sandbox := project("UXQA sandbox", "UXQA")
	create := request("POST", "/api/users", adminCookie,
		`{"username":"reviewer","email":"reviewer@example.test","role":"reviewer","password":"synthetic-test-password","must_change_password":false}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var created struct {
		ID    int64  `json:"id"`
		Role  string `json:"role"`
		Admin bool   `json:"is_super_admin"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Role != "reviewer" || created.Admin {
		t.Fatalf("wrong public role: %+v", created)
	}
	cookie := seedSessionHomeRouterCookie(t, created.ID, "000000001055")
	// Both creation and updates must preserve unique reviewer email identity.
	collision := request("POST", "/api/users", adminCookie, `{"username":"reviewer-collision","email":" REVIEWER@example.test ","role":"member","password":"synthetic-test-password"}`)
	if collision.Code != 400 {
		t.Fatalf("create reviewer email collision: %d", collision.Code)
	}
	if _, err := db.DB.Exec("UPDATE users SET email='operator@example.test' WHERE id=?", admin); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		id   int64
		body string
	}{
		{admin, `{"email":"reviewer@example.test"}`},
		{created.ID, `{"email":"operator@example.test"}`},
	} {
		if got := request("PUT", fmt.Sprintf("/api/users/%d", change.id), adminCookie, change.body); got.Code != 400 {
			t.Fatalf("update reviewer email collision: %d", got.Code)
		}
	}
	if _, err := db.DB.Exec("UPDATE users SET email='reviewer@example.test' WHERE id=?", admin); err == nil {
		t.Fatal("database allowed reviewer identity collision")
	}

	var compatibilityRole, compatibilityKey string
	if err := db.DB.QueryRow("SELECT role,role_key FROM users WHERE id=?", created.ID).Scan(&compatibilityRole, &compatibilityKey); err != nil {
		t.Fatal(err)
	}
	if compatibilityRole != "external" || compatibilityKey != "external" {
		t.Fatal("legacy reader must fail closed")
	}
	if got := request("GET", "/api/projects", cookie, ""); got.Code != 200 || strings.TrimSpace(got.Body.String()) != "[]" {
		t.Fatalf("reviewer acquired implicit projects: %d %s", got.Code, got.Body.String())
	}
	grantPath := fmt.Sprintf("/api/users/%d/memberships/%d", created.ID, sandbox)
	if got := request("PUT", grantPath, adminCookie, `{"access_level":"editor"}`); got.Code != 400 {
		t.Fatalf("editor grant: %d", got.Code)
	}
	if got := request("PUT", grantPath, adminCookie, `{"access_level":"viewer"}`); got.Code != 200 {
		t.Fatalf("viewer grant: %d %s", got.Code, got.Body.String())
	}
	issue := func(title string, projectID any) int64 {
		t.Helper()
		res, err := db.DB.Exec("INSERT INTO issues(title,project_id,type,status) VALUES(?,?,'task','backlog')", title, projectID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	privateIssue := issue("Production confidential issue", production)
	if _, err := db.DB.Exec("UPDATE issues SET issue_number=1 WHERE id=?", privateIssue); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", "/api/search?q=PRIV-1%20!!", adminCookie, ""); !strings.Contains(got.Body.String(), "Production confidential issue") {
		t.Fatal("search regression fixture does not exercise key lookup")
	}
	blockedMember := seedSessionHomeRouterUser(t, "search-denied-member", "member", "active", false)
	if _, err := db.DB.Exec("INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')", blockedMember, production); err != nil {
		t.Fatal(err)
	}
	blockedCookie := seedSessionHomeRouterCookie(t, blockedMember, "000000001056")
	for _, searchCookie := range []string{cookie, blockedCookie} {
		got := request("GET", "/api/search?q=PRIV-1%20!!", searchCookie, "")
		if got.Code != 200 || strings.Contains(got.Body.String(), "Production confidential") {
			t.Fatalf("punctuation key search bypassed access: %d", got.Code)
		}
	}

	sharedIssue := issue("UXQA visible issue", sandbox)
	orphan := issue("Production confidential orphan", nil)
	privateTag, err := db.DB.Exec("INSERT INTO tags(name,color) VALUES('Production confidential tag','gray')")
	if err != nil {
		t.Fatal(err)
	}
	tagID, _ := privateTag.LastInsertId()
	if _, err := db.DB.Exec("INSERT INTO issue_tags(issue_id,tag_id) VALUES(?,?)", privateIssue, tagID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec("INSERT INTO views(user_id,title,is_shared) VALUES(?,'Production confidential view',1)", admin); err != nil {
		t.Fatal(err)
	}
	root, err := db.DB.Exec("INSERT INTO project_agents(project_id,name) VALUES(?,'production-root')", production)
	if err != nil {
		t.Fatal(err)
	}
	rootID, _ := root.LastInsertId()
	if _, err := db.DB.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Production confidential root',revision=1,
	 updated_at='2026-09-22T00:00:00.000Z' WHERE singleton_id=1`, rootID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/auth/me", "/api/projects", "/api/issues", "/api/issues/recent", "/api/search?q=Production",
		"/api/users", "/api/tags", "/api/views", "/api/users/me/recent-projects",
		"/api/agent-mode/orchestration/v1", "/api/agent-mode/deliveries", "/api/agent-mode/worker-fleet/v2",
		fmt.Sprintf("/api/projects/%d", sandbox), fmt.Sprintf("/api/issues/%d", sharedIssue),
		fmt.Sprintf("/api/projects/%d/session-home/v1", sandbox),
		fmt.Sprintf("/api/projects/%d/session-home/zoom/v1", sandbox),
		fmt.Sprintf("/api/projects/%d/command-palette/v1?q=Production", sandbox),
	} {
		got := request("GET", path, cookie, "")
		if got.Code != 200 {
			t.Errorf("allowed %s: %d %s", path, got.Code, got.Body.String())
		}
		if strings.Contains(got.Body.String(), "Production confidential") || strings.Contains(got.Body.String(), "reviewer-operator") {
			t.Errorf("cross-project or directory leak at %s", path)
		}
	}
	for _, path := range []string{
		fmt.Sprintf("/api/projects/%d", production), fmt.Sprintf("/api/issues/%d", privateIssue), fmt.Sprintf("/api/issues/%d", orphan),
		fmt.Sprintf("/api/projects/%d/knowledge", production), fmt.Sprintf("/api/projects/%d/session-home/v1", production),
		fmt.Sprintf("/api/agent-mode/projects/%d/orchestration/v1", production),
		"/api/customers", "/api/offers/summary", "/api/instance/memory", "/api/auth/api-keys", "/api/auth/totp/setup",
		"/api/orchestrator/v1/config", "/api/changes", "/api/sprints", "/api/unknown-future-endpoint",
	} {
		got := request("GET", path, cookie, "")
		if got.Code != 403 && got.Code != 404 {
			t.Errorf("denied %s: %d", path, got.Code)
		}
		if strings.Contains(got.Body.String(), "Production confidential") {
			t.Errorf("denial leaks project at %s", path)
		}
	}
	for _, path := range []string{"/api/projects", "/api/users", "/api/auth/api-keys", fmt.Sprintf("/api/projects/%d/issues", sandbox)} {
		if got := request("POST", path, cookie, `{}`); got.Code != 403 {
			t.Errorf("reviewer write %s: %d", path, got.Code)
		}
	}
	for _, path := range []string{fmt.Sprintf("/api/issues?keys=%d", orphan), "/api/search?q=PRIV"} {
		got := request("GET", path, cookie, "")
		if got.Code != 200 || strings.Contains(got.Body.String(), "Production confidential") {
			t.Errorf("lookup isolation %s: %d %s", path, got.Code, got.Body.String())
		}
	}
	future := project("Future production", "FUT")
	auth.SeedAccessForProject(future)
	auth.SeedAccessForUser(created.ID, auth.RoleReviewer)
	var grants int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM project_members WHERE user_id=?", created.ID).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("reviewer seeded access: %d %v", grants, err)
	}
	if _, err := db.DB.Exec("UPDATE project_members SET access_level='editor' WHERE user_id=?", created.ID); err == nil {
		t.Fatal("DB accepted reviewer editor grant")
	}
	if _, err := db.DB.Exec("UPDATE users SET is_super_admin=1 WHERE id=?", created.ID); err == nil {
		t.Fatal("DB accepted reviewer admin flag")
	}
	if got := request("GET", "/api/projects", adminCookie, ""); !strings.Contains(got.Body.String(), "Production confidential") {
		t.Fatal("existing admin behavior changed")
	}
	if _, err := db.DB.Exec("UPDATE projects SET status='deleted' WHERE id=?", sandbox); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", fmt.Sprintf("/api/projects/%d", sandbox), cookie, ""); got.Code != 403 {
		t.Fatalf("deleted project detail: %d", got.Code)
	}
	if got := request("GET", "/api/projects?status=deleted", cookie, ""); strings.Contains(got.Body.String(), "UXQA sandbox") {
		t.Fatal("deleted project list leak")
	}
	if _, err := db.DB.Exec("UPDATE projects SET status='active' WHERE id=?", sandbox); err != nil {
		t.Fatal(err)
	}
	if got := request("PUT", grantPath, adminCookie, `{"access_level":"none"}`); got.Code != 200 {
		t.Fatalf("revoke: %d", got.Code)
	}
	if got := request("GET", fmt.Sprintf("/api/projects/%d", sandbox), cookie, ""); got.Code != 403 {
		t.Fatalf("revocation not enforced: %d", got.Code)
	}
}

// Any editable profile can otherwise poison the verified-email OIDC lookup of
// another principal. Check the real middleware and handlers, not just the SQL.
func TestUserEmailUniquenessAcrossRolesAndStatuses(t *testing.T) {
	openSeedTestDB(t)
	router := sessionHomeProductionRouter()
	admin := seedSessionHomeRouterUser(t, "email-operator", "admin", "active", false)
	adminCookie := seedSessionHomeRouterCookie(t, admin, "000000003001")
	request := func(method, path, cookie, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set(auth.CSRFHeaderName, "csrf")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for index, status := range []string{"active", "inactive", "deleted"} {
		t.Run(status, func(t *testing.T) {
			target := seedSessionHomeRouterUser(t, "email-target-"+status, "admin", status, false)
			email := status + "@example.test"
			if _, err := db.DB.Exec("UPDATE users SET email=? WHERE id=?", email, target); err != nil {
				t.Fatal(err)
			}
			for roleIndex, role := range []string{"member", "external"} {
				t.Run(role, func(t *testing.T) {
					actor := seedSessionHomeRouterUser(t, "email-actor-"+status+"-"+role, role, "active", false)
					cookie := seedSessionHomeRouterCookie(t, actor, fmt.Sprintf("%012d", 3100+index*10+roleIndex))
					if _, err := db.DB.Exec("UPDATE users SET first_name='Original',intake_confidence_threshold=75 WHERE id=?", actor); err != nil {
						t.Fatal(err)
					}
					payload := fmt.Sprintf(`{"email":" %s ","first_name":"Changed","intake_confidence_threshold":0}`, strings.ToUpper(email))
					if got := request("PATCH", "/api/auth/me", cookie, payload); got.Code != 400 {
						t.Fatalf("self-service collision status=%d", got.Code)
					}
					var actualEmail, firstName string
					var confidence int
					if err := db.DB.QueryRow("SELECT email,first_name,intake_confidence_threshold FROM users WHERE id=?", actor).Scan(&actualEmail, &firstName, &confidence); err != nil {
						t.Fatal(err)
					}
					if actualEmail != "" || firstName != "Original" || confidence != 75 {
						t.Fatal("rejected profile update partially changed account")
					}
					if got := request("PUT", fmt.Sprintf("/api/users/%d", actor), adminCookie, payload); got.Code != 400 {
						t.Fatalf("admin update collision status=%d", got.Code)
					}
					if _, err := db.DB.Exec("UPDATE users SET email=? WHERE id=?", " "+strings.ToUpper(email)+" ", actor); err == nil {
						t.Fatal("direct writer bypassed uniqueness")
					}
					unique := fmt.Sprintf(`{"email":"unique-%d@example.test","intake_confidence_threshold":0}`, actor)
					if got := request("PATCH", "/api/auth/me", cookie, unique); got.Code != 200 {
						t.Fatalf("unique self-service email status=%d", got.Code)
					}
					if got := request("PATCH", "/api/auth/me", cookie, unique); got.Code != 200 {
						t.Fatalf("same own email status=%d", got.Code)
					}
				})
			}
			for _, role := range []string{"admin", "member", "external", "reviewer"} {
				payload := fmt.Sprintf(`{"username":"new-%s-%s","email":" %s ","role":"%s","password":"synthetic-test-password"}`, status, role, strings.ToUpper(email), role)
				if got := request("POST", "/api/users", adminCookie, payload); got.Code != 400 {
					t.Fatalf("%s create collision status=%d", role, got.Code)
				}
			}
		})
	}
}
