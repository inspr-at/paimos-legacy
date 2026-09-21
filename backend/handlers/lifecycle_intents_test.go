package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestLifecycleHTTPClosedBodiesAndCredentialSeparation(t *testing.T) {
	openChangesTestDB(t)
	res, e := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin) VALUES('lifecycle-http','disabled','admin','super_admin','active',1)`)
	if e != nil {
		t.Fatal(e)
	}
	user, _ := res.LastInsertId()
	project := seedChangesProject(t, "LIF")
	if _, e = db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, project); e != nil {
		t.Fatal(e)
	}
	credential := uuid.NewString()
	if _, e = db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at) VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'))`, uuid.NewString(), user, credential); e != nil {
		t.Fatal(e)
	}
	human, _ := auth.NewSessionPrincipal(credential, user, user, false)
	res, e = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'fixture','not-a-credential','fixture','*')`, user)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := res.LastInsertId()
	reporter, _ := auth.NewAPIKeyPrincipal(key, user, auth.ParseScopes("*"))
	runtime, e := lifecycleintents.NewService(db.DB).RegisterRuntime(context.Background(), reporter, project, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", lifecycleintents.Registration{
		Generation: uuid.NewString(), Host: "http-fixture", SchemaVersion: lifecycleintents.AccountLifecycleSchemaV4,
		AccountScopes: []lifecycleintents.AccountScope{{
			AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "coordinator", Label: "Coordinator"}},
			Profiles: []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}}, AttachmentRevision: 7,
			AccountAvailability: lifecycleintents.AccountAvailabilityAvailable,
		}},
		Workspaces: []lifecycleintents.Workspace{{Handle: uuid.NewString(), Identity: fmt.Sprintf("%064x", 1)}},
	})
	if e != nil {
		t.Fatal(e)
	}
	router := chi.NewRouter()
	RegisterLifecycleIntentRoutes(router)
	call := func(principal auth.Principal, method, suffix, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, fmt.Sprintf("/projects/%d/lifecycle/v1/%s", project, suffix), strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(auth.WithPrincipal(request.Context(), principal))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	namedStart := fmt.Sprintf(`{"request_key":%q,"operation":"start","runtime_id":%q,"runtime_generation":%q,"account_label":"chatgpt","account_key":"coordinator","attachment_revision":7,"ttl_seconds":60,"workspace_handle":%q,"agent_name":"worker","dispatch_profile_id":"codex-sol-high","dispatch_profile_version":"2","work_shape":"unknown","role":"worker"}`, uuid.NewString(), runtime.ID, runtime.Generation, runtime.Workspaces[0].Handle)
	if response := call(human, "POST", "intents", namedStart); response.Code != 201 || !strings.Contains(response.Body.String(), `"attachment_revision":7`) {
		t.Fatalf("reviewed attachment revision was not durably accepted: %d %s", response.Code, response.Body.String())
	}
	missingRevision := fmt.Sprintf(`{"request_key":%q,"operation":"start","runtime_id":%q,"runtime_generation":%q,"account_label":"chatgpt","account_key":"coordinator","ttl_seconds":60,"workspace_handle":%q,"agent_name":"worker","dispatch_profile_id":"codex-sol-high","dispatch_profile_version":"2","work_shape":"unknown","role":"worker"}`,
		uuid.NewString(), runtime.ID, runtime.Generation, runtime.Workspaces[0].Handle)
	if response := call(human, "POST", "intents", missingRevision); response.Code != 403 {
		t.Fatalf("explicit named start without attachment revision status=%d", response.Code)
	}
	valid := fmt.Sprintf(`{"request_key":%q,"operation":"repair","runtime_id":%q,"runtime_generation":%q,"account_label":"chatgpt","ttl_seconds":60,"repair_layer":"reporter"}`, uuid.NewString(), runtime.ID, runtime.Generation)
	for _, field := range []string{"argv", "shell", "prompt", "credentials", "target_ref", "workspace_path", "instance", "machine_id"} {
		body := strings.TrimSuffix(valid, "}") + fmt.Sprintf(`,%q:"fixture-forbidden"}`, field)
		res := call(human, "POST", "intents", body)
		if res.Code != 400 || strings.Contains(res.Body.String(), "fixture-forbidden") {
			t.Fatalf("field %s status=%d", field, res.Code)
		}
	}
	for _, body := range []string{strings.TrimSuffix(valid, "}") + `,"operation":"start"}`, valid + `{}`, strings.Repeat("x", 8193), "null", strings.TrimSuffix(valid, "}") + `,"agent_name":null}`, strings.Replace(valid, `"ttl_seconds":60`, `"ttl_seconds":60.5`, 1), strings.Replace(valid, `"ttl_seconds":60`, `"ttl_seconds":601`, 1)} {
		if res := call(human, "POST", "intents", body); res.Code != 400 {
			t.Fatalf("malicious body status=%d", res.Code)
		}
	}
	if res := call(reporter, "POST", "intents", valid); res.Code != 403 {
		t.Fatal("API key accepted browser submit")
	}
	if res := call(human, "POST", "runtimes/"+runtime.ID+"/claim", `{}`); res.Code != 403 {
		t.Fatal("session accepted daemon claim")
	}
	response := call(human, "POST", "intents", valid)
	if response.Code != 201 {
		t.Fatalf("submit=%d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("response cacheable")
	}
	if health := call(human, "GET", "runtime-health", ""); health.Code != 200 || !strings.Contains(health.Body.String(), `"status":"unknown"`) || !strings.Contains(health.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("runtime health route omitted unknown evidence or cache policy")
	}
	if health := call(reporter, "GET", "runtime-health", ""); health.Code != 403 {
		t.Fatal("reporter accessed browser health")
	}
	var in lifecycleintents.Intent
	if json.Unmarshal(response.Body.Bytes(), &in) != nil {
		t.Fatal("invalid response")
	}
	response = call(human, "GET", "intents/"+in.ID, "")
	if response.Code != 200 {
		t.Fatal("get failed")
	}
	for _, forbidden := range []string{"lease_digest", "session_credential_id", "api_key_id", "worker_lease", "target_ref"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("private field %s exposed", forbidden)
		}
	}
	response = call(human, "POST", "intents/"+in.ID+"/cancel", `{"expected_revision":1}`)
	if response.Code != 200 {
		t.Fatal("cancel failed")
	}
	response = call(human, "GET", "intents/"+in.ID+"/events", "")
	if response.Code != 200 {
		t.Fatal("events failed")
	}
	if _, e = db.DB.Exec(`UPDATE users SET role_key='admin',is_super_admin=0 WHERE id=?`, user); e != nil {
		t.Fatal(e)
	}
	for _, suffix := range []string{"runtimes", "intents/" + in.ID, "intents/" + uuid.NewString()} {
		response = call(human, "GET", suffix, "")
		if response.Code != 403 || strings.TrimSpace(response.Body.String()) != `{"error":"lifecycle_unavailable"}` {
			t.Fatal("unauthorized existence oracle")
		}
	}
}
