// PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/db"
)

func TestFlowProjectionCredentialProductionBoundary(t *testing.T) {
	openSeedTestDB(t)
	router := sessionHomeProductionRouter()
	admin := seedSessionHomeRouterUser(t, "flow-operator", "admin", "active", false)
	adminCookie := seedSessionHomeRouterCookie(t, admin, "000000001991")
	res, err := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,is_reviewer,status)
	 VALUES('flow-reviewer','x','external','external',1,'active')`)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, _ := res.LastInsertId()
	reviewerCookie := seedSessionHomeRouterCookie(t, reviewer, "000000001992")
	project := func(key string) int64 {
		t.Helper()
		result, err := db.DB.Exec(`INSERT INTO projects(name,key,status,inspr_stream_enabled) VALUES(?,?,'active',1)`, key, key)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	sandbox, other := project("FQA"), project("FOTHER")
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'viewer'),(?,?,'viewer')`, reviewer, sandbox, reviewer, other); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, cookie, key string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set(auth.CSRFHeaderName, "csrf")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	enrollment := func(owner, project int64, expiry time.Time) map[string]any {
		return map[string]any{"name": "Janus UXQA projection", "reviewer_user_id": owner, "project_id": project, "expires_at": expiry.UTC().Format(time.RFC3339Nano)}
	}
	endpoint := "/api/auth/flow-projection-credentials"
	body := enrollment(reviewer, sandbox, time.Now().Add(time.Hour))
	for _, cookie := range []string{"", reviewerCookie} {
		got := request("POST", endpoint, cookie, "", body)
		if got.Code != 401 && got.Code != 403 {
			t.Fatalf("non-admin enrollment: %d", got.Code)
		}
	}
	for _, invalid := range []map[string]any{
		enrollment(admin, sandbox, time.Now().Add(time.Hour)),
		enrollment(reviewer, sandbox, time.Now().Add(31*24*time.Hour)),
		enrollment(reviewer, sandbox, time.Now().Add(-time.Hour)),
		enrollment(reviewer, 987654321, time.Now().Add(time.Hour)),
	} {
		if got := request("POST", endpoint, adminCookie, "", invalid); got.Code != 400 {
			t.Fatalf("invalid enrollment: %d", got.Code)
		}
	}
	generalKey := func(name, scopes string) string {
		t.Helper()
		key := "paimos_" + strings.Repeat(name, 64)
		hash := sha256.Sum256([]byte(key))
		if _, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,?,?,?,?)`, admin, name, hex.EncodeToString(hash[:]), "test-prefix", scopes); err != nil {
			t.Fatal(err)
		}
		return key
	}
	wrongScope := generalKey("a", "projects:write")
	if got := request("POST", endpoint, "", wrongScope, body); got.Code != 403 {
		t.Fatalf("wrong scope: %d", got.Code)
	}
	adminKey := generalKey("b", auth.ScopeFlowCredentialsWrite)
	var audit bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&audit)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	created := request("POST", endpoint, "", adminKey, body)
	if created.Code != 201 {
		t.Fatalf("admin key enrollment: %d", created.Code)
	}
	if !strings.Contains(created.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("credential response cacheable")
	}
	var credential struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	if json.Unmarshal(created.Body.Bytes(), &credential) != nil || credential.ID <= 0 || !strings.HasPrefix(credential.Key, auth.FlowProjectionKeyPrefix()) {
		t.Fatal("invalid enrollment response")
	}
	if strings.Contains(audit.String(), credential.Key) {
		t.Fatal("credential leaked to audit")
	}
	hash := sha256.Sum256([]byte(credential.Key))
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE key_hash=?`, hex.EncodeToString(hash[:])).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy general-key reader could resolve Flow credential")
	}
	path := fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", sandbox)
	got := request("GET", path, "", credential.Key, nil)
	if got.Code != 200 {
		t.Fatalf("projection read: %d %s", got.Code, got.Body.String())
	}
	if strings.Contains(got.Body.String(), `"actor_kind":"human"`) {
		t.Fatal("server projection impersonates a browser human")
	}
	for _, route := range []string{
		fmt.Sprintf("/api/projects/%d/baseline-batches/flow-state", other),
		path + "?project_id=" + fmt.Sprint(other), path + "?any=value", path + "/",
		strings.Replace(path, "/baseline-batches/", "%2fbaseline-batches/", 1),
		strings.Replace(path, "/projects/", "/projects/%30", 1),
		"/api/auth/me", "/api/users", "/api/projects", "/api/issues", endpoint, "/api/unknown-future-read",
	} {
		got := request("GET", route, "", credential.Key, nil)
		if got.Code >= 200 && got.Code < 300 {
			t.Errorf("extra route allowed: %s", route)
		}
	}
	for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		if got := request(method, path, "", credential.Key, nil); got.Code != 403 {
			t.Errorf("method %s: %d", method, got.Code)
		}
	}
	if got := request("POST", endpoint, "", credential.Key, body); got.Code != 403 {
		t.Fatalf("credential mint escalation: %d", got.Code)
	}
	// A forged general-row collision never bypasses the dedicated resolver.
	forged := auth.FlowProjectionKeyPrefix() + strings.Repeat("f", 64)
	forgedHash := sha256.Sum256([]byte(forged))
	if _, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,?,?,?, '*')`, admin, "forged", hex.EncodeToString(forgedHash[:]), "test-prefix"); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", path, "", forged, nil); got.Code != 401 {
		t.Fatalf("dedicated prefix fell back to general auth: %d", got.Code)
	}
	// The projection service rechecks the same binding transactionally.
	svc := baselinebatch.NewService(db.DB, nil, nil, nil, nil, nil)
	actor := baselinebatch.Actor{Kind: string(auth.PrincipalFlowProjection), UserID: reviewer, APIKeyID: credential.ID}
	if _, err := svc.Workflow(context.Background(), actor, other); err == nil {
		t.Fatal("service ignored exact credential project")
	}
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='none' WHERE user_id=? AND project_id=?`, reviewer, sandbox); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", path, "", credential.Key, nil); got.Code != 401 {
		t.Fatalf("revoked membership: %d", got.Code)
	}
	if _, err := svc.Workflow(context.Background(), actor, sandbox); err == nil {
		t.Fatal("service retained revoked membership")
	}
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='viewer' WHERE user_id=? AND project_id=?`, reviewer, sandbox); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET status='inactive' WHERE id=?`, reviewer); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", path, "", credential.Key, nil); got.Code != 401 {
		t.Fatalf("inactive owner: %d", got.Code)
	}
	if got := request("POST", endpoint, adminCookie, "", body); got.Code != 400 {
		t.Fatalf("inactive owner enrollment: %d", got.Code)
	}
	if _, err := db.DB.Exec(`UPDATE users SET status='active' WHERE id=?`, reviewer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE projects SET status='archived' WHERE id=?`, sandbox); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", path, "", credential.Key, nil); got.Code != 401 {
		t.Fatalf("inactive project: %d", got.Code)
	}
	if _, err := db.DB.Exec(`UPDATE projects SET status='active' WHERE id=?`, sandbox); err != nil {
		t.Fatal(err)
	}
	if got := request("DELETE", fmt.Sprintf("%s/%d", endpoint, credential.ID), "", wrongScope, nil); got.Code != 403 {
		t.Fatalf("wrong revoke scope: %d", got.Code)
	}
	if got := request("DELETE", fmt.Sprintf("%s/%d", endpoint, credential.ID), adminCookie, "", nil); got.Code != 204 {
		t.Fatalf("revoke: %d", got.Code)
	}
	if got := request("GET", path, "", credential.Key, nil); got.Code != 401 {
		t.Fatalf("revoked credential: %d", got.Code)
	}
	if _, err := db.DB.Exec(`UPDATE flow_projection_credentials SET disabled_at=NULL WHERE id=?`, credential.ID); err == nil {
		t.Fatal("revocation not terminal")
	}
	if _, err := db.DB.Exec(`UPDATE flow_projection_credentials SET project_id=? WHERE id=?`, other, credential.ID); err == nil {
		t.Fatal("binding mutable")
	}
	// Expired credentials cannot authenticate even with otherwise valid access.
	expired := auth.FlowProjectionKeyPrefix() + strings.Repeat("e", 64)
	expiredHash := sha256.Sum256([]byte(expired))
	if _, err := db.DB.Exec(`INSERT INTO flow_projection_credentials(user_id,project_id,name,key_hash,created_at,expires_at)
	 VALUES(?,?,'expired',?,datetime('now','-2 days'),datetime('now','-1 day'))`, reviewer, sandbox, hex.EncodeToString(expiredHash[:])); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", path, "", expired, nil); got.Code != 401 {
		t.Fatalf("expired credential: %d", got.Code)
	}
}
