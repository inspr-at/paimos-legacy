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
	if got := request("PUT", grantPath, adminCookie, `{"access_level":"none"}`); got.Code != 200 {
		t.Fatalf("revoke: %d", got.Code)
	}
	if got := request("GET", fmt.Sprintf("/api/projects/%d", sandbox), cookie, ""); got.Code != 403 {
		t.Fatalf("revocation not enforced: %d", got.Code)
	}
}
