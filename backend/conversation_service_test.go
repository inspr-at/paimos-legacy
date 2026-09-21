// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/conversationturns"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/internal/testdb"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

const conversationTestLease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type conversationRouteFixture struct {
	router                        http.Handler
	projectID, foreignProjectID   int64
	adminID, actorID, runnerKeyID int64
	sessionID, csrf, runnerKey    string
	runtime                       lifecycleintents.Runtime
	bindingID, serviceKey         string
	actor                         conversationturns.Actor
}

func openConversationRouteFixture(t *testing.T) *conversationRouteFixture {
	return openConversationRouteFixtureConfigured(t, true, true)
}

func openConversationRouteFixtureConfigured(t *testing.T, advertiseConversation, enrollConversation bool) *conversationRouteFixture {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	testdb.Prepare(t)
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = db.DB.Close()
		db.DB = nil
	})
	f := &conversationRouteFixture{sessionID: "conversation-browser-session", csrf: "conversation-csrf",
		runnerKey: brand.Default.APIKeyPrefix + strings.Repeat("1", 64),
		actor:     conversationturns.Actor{Issuer: "https://identity.aithema.test", Subject: "actor-1"}}
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin)
		VALUES('conversation-admin','x','admin','super_admin','active',1)`)
	if err != nil {
		t.Fatal(err)
	}
	f.adminID, _ = result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO users(username,password,role,role_key,status)
		VALUES('conversation-actor','x','member','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	f.actorID, _ = result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Conversation','CVS','active')`)
	if err != nil {
		t.Fatal(err)
	}
	f.projectID, _ = result.LastInsertId()
	result, err = db.DB.Exec(`INSERT INTO projects(name,key,status) VALUES('Foreign conversation','CVF','active')`)
	if err != nil {
		t.Fatal(err)
	}
	f.foreignProjectID, _ = result.LastInsertId()
	credentialID := uuid.NewString()
	if _, err := db.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at,csrf_token)
		VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'),?)`, f.sessionID, f.adminID, credentialID, f.csrf); err != nil {
		t.Fatal(err)
	}
	runnerDigest := sha256.Sum256([]byte(f.runnerKey))
	result, err = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes,credential_kind)
		VALUES(?,'conversation-runner',?,?,?,'general')`, f.adminID, hex.EncodeToString(runnerDigest[:]),
		f.runnerKey[:len(brand.Default.APIKeyPrefix)+8], auth.ScopeAgentControlsRunner)
	if err != nil {
		t.Fatal(err)
	}
	f.runnerKeyID, _ = result.LastInsertId()
	runner, err := auth.NewAPIKeyPrincipal(f.runnerKeyID, f.adminID, auth.ParseScopes(auth.ScopeAgentControlsRunner))
	if err != nil {
		t.Fatal(err)
	}
	registration := lifecycleintents.Registration{
		SchemaVersion: lifecycleintents.AccountLifecycleSchemaV4, Generation: uuid.NewString(), Host: "aithema-test-host",
		Workspaces: []lifecycleintents.Workspace{{Handle: uuid.NewString(), Identity: fmt.Sprintf("%064x", 1)}},
		AccountScopes: []lifecycleintents.AccountScope{{
			AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "acct-main", Label: "Main"}},
			Profiles: []lifecycleintents.Profile{{ID: "codex-sol-high", Version: "2"}, {ID: "codex-luna-medium", Version: "2"}}, AttachmentRevision: 7,
			AccountAvailability: lifecycleintents.AccountAvailabilityAvailable,
		}},
	}
	if advertiseConversation {
		registration.Conversation = &lifecycleintents.ConversationCapability{
			SchemaVersion: lifecycleintents.ConversationSchemaV1, AccountKey: "acct-main", AttachmentRevision: 7,
			DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2",
			ExecutionPolicyID: lifecycleintents.ConversationExecutionPolicyV1, MaxOutputBytes: 262144, MaxEvents: 512,
		}
	}
	f.runtime, err = lifecycleintents.NewService(db.DB).RegisterRuntime(context.Background(), runner, f.projectID, conversationTestLease, registration)
	if err != nil {
		t.Fatalf("register production lifecycle runtime: %v", err)
	}
	router := chi.NewRouter()
	router.Route("/api", mountAPI)
	f.router = router
	if enrollConversation {
		f.enroll(t, "acct-main", registration.Generation)
	}
	return f
}

func (f *conversationRouteFixture) request(t *testing.T, method, path string, body any, bearer string, actor bool) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if bearer == f.runnerKey {
		req.Header.Set(conversationturns.RuntimeLeaseHeader, conversationTestLease)
	}
	if actor {
		req.Header.Set(conversationturns.ActorIssuerHeader, f.actor.Issuer)
		req.Header.Set(conversationturns.ActorSubjectHeader, f.actor.Subject)
	}
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}

func (f *conversationRouteFixture) enrollmentBody(accountKey, generation string) map[string]any {
	return map[string]any{
		"schema_version": 1, "name": "aithema conversation", "project_id": f.projectID,
		"host_id": f.runtime.MachineID, "project_ref": "aithema-project-1", "runtime_id": f.runtime.ID,
		"runtime_generation": generation, "account_key": accountKey, "attachment_revision": 7,
		"dispatch_profile_id": "codex-sol-high", "dispatch_profile_version": "2",
		"actors": []map[string]any{{"issuer": f.actor.Issuer, "subject": f.actor.Subject, "user_id": f.actorID}},
		"limits": map[string]any{"max_input_bytes": 131072, "max_messages": 128, "max_output_bytes": 262144,
			"max_event_bytes": 8192, "max_events": 512, "max_timeout_ms": 180000},
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	}
}

func (f *conversationRouteFixture) enroll(t *testing.T, accountKey, generation string) *httptest.ResponseRecorder {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	enrollment := f.enrollmentBody(accountKey, generation)
	enrollment["age_recipients"] = []string{identity.Recipient().String()}
	body, err := json.Marshal(enrollment)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/conversation-services", bytes.NewReader(body))
	req.Host = "paimos.test"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "paimos_session="+f.sessionID)
	req.Header.Set("Origin", "https://paimos.test")
	req.Header.Set(auth.CSRFHeaderName, f.csrf)
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	if accountKey != "acct-main" || generation != f.runtime.Generation {
		return recorder
	}
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enroll status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		KeyAge  string `json:"key_age_base64"`
		Key     string `json:"key"`
		Binding struct {
			ID string `json:"binding_id"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Key != "" || response.KeyAge == "" || response.Binding.ID == "" {
		t.Fatalf("invalid enrollment response: %v %s", err, recorder.Body.String())
	}
	ciphertext, err := base64.StdEncoding.DecodeString(response.KeyAge)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	f.serviceKey, f.bindingID = strings.TrimSpace(string(plaintext)), response.Binding.ID
	if f.serviceKey == "" {
		t.Fatal("age envelope did not contain a credential")
	}
	return recorder
}

func (f *conversationRouteFixture) postEnrollment(t *testing.T, enrollment map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	enrollment["age_recipients"] = []string{identity.Recipient().String()}
	body, err := json.Marshal(enrollment)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/conversation-services", bytes.NewReader(body))
	req.Host = "paimos.test"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "paimos_session="+f.sessionID)
	req.Header.Set("Origin", "https://paimos.test")
	req.Header.Set(auth.CSRFHeaderName, f.csrf)
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}

func TestConversationEnrollmentRequiresExactCurrentCapability(t *testing.T) {
	t.Run("absent_consumer", func(t *testing.T) {
		f := openConversationRouteFixtureConfigured(t, false, false)
		if response := f.postEnrollment(t, f.enrollmentBody("acct-main", f.runtime.Generation)); response.Code != http.StatusForbidden {
			t.Fatalf("absent consumer enrollment status=%d body=%s", response.Code, response.Body.String())
		}
	})
	for name, mutate := range map[string]func(map[string]any){
		"wrong_account":  func(in map[string]any) { in["account_key"] = "acct-other" },
		"stale_revision": func(in map[string]any) { in["attachment_revision"] = 6 },
		"wrong_profile":  func(in map[string]any) { in["dispatch_profile_id"] = "codex-luna-medium" },
		"wrong_output_cap": func(in map[string]any) {
			in["limits"].(map[string]any)["max_output_bytes"] = 128 << 10
		},
		"wrong_event_cap": func(in map[string]any) { in["limits"].(map[string]any)["max_events"] = 256 },
	} {
		t.Run(name, func(t *testing.T) {
			f := openConversationRouteFixture(t)
			body := f.enrollmentBody("acct-main", f.runtime.Generation)
			mutate(body)
			if response := f.postEnrollment(t, body); response.Code != http.StatusForbidden {
				t.Fatalf("mismatched readiness enrollment status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func (f *conversationRouteFixture) callRequest(requestID, conversationID, purpose string) conversationturns.Request {
	return conversationturns.Request{
		SchemaVersion: 1, RequestID: requestID, BindingID: f.bindingID, BindingRevision: 1, Actor: f.actor,
		ProjectRef: "aithema-project-1", ConversationID: conversationID, TurnID: "turn-1", Purpose: purpose,
		System: "Trusted Aithema business context.", Messages: []conversationturns.Message{{Role: "user", Content: "Help me plan this."}}, TimeoutMS: 60_000,
	}
}

func decodeResponse[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response status=%d: %v body=%s", recorder.Code, err, recorder.Body.String())
	}
	return out
}

func (f *conversationRouteFixture) claim(t *testing.T) conversationturns.ClaimEnvelope {
	t.Helper()
	path := fmt.Sprintf("/api/projects/%d/runtimes/%s/conversation/v1/claim", f.projectID, f.runtime.ID)
	recorder := f.request(t, http.MethodPost, path, conversationturns.ClaimRequest{SchemaVersion: 1, Generation: f.runtime.Generation}, f.runnerKey, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return decodeResponse[conversationturns.ClaimEnvelope](t, recorder)
}

func (f *conversationRouteFixture) report(t *testing.T, callID, execution string, event conversationturns.Event) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/projects/%d/runtimes/%s/conversation/v1/calls/%s/events", f.projectID, f.runtime.ID, callID)
	request := conversationturns.ReportRequest{SchemaVersion: 1, Generation: f.runtime.Generation, ExecutionGeneration: execution, Event: event}
	reqBody, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.runnerKey)
	req.Header.Set(conversationturns.RuntimeLeaseHeader, conversationTestLease)
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}

func TestConversationServiceProductionRouterLifecycle(t *testing.T) {
	f := openConversationRouteFixture(t)
	base := fmt.Sprintf("/api/projects/%d/conversation/v1", f.projectID)
	request := f.callRequest("request-chat-1", "conversation-1", "chat")
	created := f.request(t, http.MethodPost, base+"/calls", request, f.serviceKey, true)
	if created.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", created.Code, created.Body.String())
	}
	call := decodeResponse[conversationturns.Call](t, created)
	if call.State != "queued" || call.DeadlineAt == "" {
		t.Fatalf("unexpected admitted call: %+v", call)
	}
	replay := f.request(t, http.MethodPost, base+"/calls", request, f.serviceKey, true)
	if replay.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, replay).CallID != call.CallID {
		t.Fatalf("identical replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	changed := request
	changed.System = "Changed authenticated context."
	if response := f.request(t, http.MethodPost, base+"/calls", changed, f.serviceKey, true); response.Code != http.StatusConflict {
		t.Fatalf("changed replay status=%d body=%s", response.Code, response.Body.String())
	}
	callerSchema := map[string]any{}
	requestJSON, _ := json.Marshal(f.callRequest("request-schema", "conversation-schema", "understand"))
	_ = json.Unmarshal(requestJSON, &callerSchema)
	callerSchema["output_schema"] = map[string]any{"type": "string"}
	if response := f.request(t, http.MethodPost, base+"/calls", callerSchema, f.serviceKey, true); response.Code != http.StatusBadRequest {
		t.Fatalf("caller output schema status=%d body=%s", response.Code, response.Body.String())
	}
	claimed := f.claim(t)
	if claimed.Claim == nil || claimed.Claim.Call.CallID != call.CallID || claimed.Claim.ExecutionGeneration == "" || claimed.Claim.System != request.System {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	if second := f.claim(t); second.Claim != nil {
		t.Fatalf("claimed call was dispatched twice: %+v", second)
	}
	execution := claimed.Claim.ExecutionGeneration
	started := conversationturns.Event{Sequence: 1, Kind: "started", ThreadID: "thread-native-1", TurnID: "turn-native-1"}
	if response := f.report(t, call.CallID, execution, started); response.Code != http.StatusOK {
		t.Fatalf("started status=%d body=%s", response.Code, response.Body.String())
	}
	delta := conversationturns.Event{Sequence: 2, Kind: "assistant_delta", Text: "Hello", ThreadID: started.ThreadID, TurnID: started.TurnID}
	if response := f.report(t, call.CallID, execution, delta); response.Code != http.StatusOK {
		t.Fatalf("delta status=%d body=%s", response.Code, response.Body.String())
	}
	digest := sha256.Sum256([]byte("Hello"))
	completed := conversationturns.Event{Sequence: 3, Kind: "completed", ThreadID: started.ThreadID, TurnID: started.TurnID, OutputSHA256: hex.EncodeToString(digest[:])}
	if response := f.report(t, call.CallID, execution, completed); response.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", response.Code, response.Body.String())
	}
	if response := f.report(t, call.CallID, execution, completed); response.Code != http.StatusOK {
		t.Fatalf("terminal event replay status=%d body=%s", response.Code, response.Body.String())
	}
	got := f.request(t, http.MethodGet, base+"/calls/"+call.CallID, nil, f.serviceKey, true)
	finished := decodeResponse[conversationturns.Call](t, got)
	if got.Code != http.StatusOK || finished.State != "completed" || finished.OutputText != "Hello" || finished.OutputSHA256 != completed.OutputSHA256 {
		t.Fatalf("completed get status=%d call=%+v", got.Code, finished)
	}
	events := f.request(t, http.MethodGet, base+"/calls/"+call.CallID+"/events?after=1", nil, f.serviceKey, true)
	page := decodeResponse[conversationturns.EventsPage](t, events)
	if events.Code != http.StatusOK || len(page.Events) != 2 || page.Events[0].Kind != "assistant_delta" {
		t.Fatalf("events status=%d page=%+v", events.Code, page)
	}
	understandRequest := f.callRequest("request-understand", "conversation-understand", "understand")
	understandCall := decodeResponse[conversationturns.Call](t, f.request(t, http.MethodPost, base+"/calls", understandRequest, f.serviceKey, true))
	understandClaim := f.claim(t).Claim
	if understandClaim == nil || understandClaim.Call.CallID != understandCall.CallID || understandClaim.OutputSchema == nil {
		t.Fatalf("understanding claim lacks server schema: %+v", understandClaim)
	}
	understandStarted := conversationturns.Event{Sequence: 1, Kind: "started", ThreadID: "thread-understand", TurnID: "turn-understand"}
	if response := f.report(t, understandCall.CallID, understandClaim.ExecutionGeneration, understandStarted); response.Code != http.StatusOK {
		t.Fatalf("understanding start status=%d body=%s", response.Code, response.Body.String())
	}
	understanding := `{"summary":"Known outcome","facts":[],"open_questions":[],"next_question":"","candidate_requirements":[],"project_kinds":["new_product"]}`
	understandDelta := conversationturns.Event{Sequence: 2, Kind: "assistant_delta", Text: understanding, ThreadID: understandStarted.ThreadID, TurnID: understandStarted.TurnID}
	if response := f.report(t, understandCall.CallID, understandClaim.ExecutionGeneration, understandDelta); response.Code != http.StatusOK {
		t.Fatalf("understanding delta status=%d body=%s", response.Code, response.Body.String())
	}
	understandingDigest := sha256.Sum256([]byte(understanding))
	understandComplete := conversationturns.Event{Sequence: 3, Kind: "completed", ThreadID: understandStarted.ThreadID, TurnID: understandStarted.TurnID, OutputSHA256: hex.EncodeToString(understandingDigest[:])}
	if response := f.report(t, understandCall.CallID, understandClaim.ExecutionGeneration, understandComplete); response.Code != http.StatusOK {
		t.Fatalf("understanding completion status=%d body=%s", response.Code, response.Body.String())
	}

	wrongActor := httptest.NewRequest(http.MethodGet, base+"/calls/"+call.CallID, nil)
	wrongActor.Header.Set("Authorization", "Bearer "+f.serviceKey)
	wrongActor.Header.Set(conversationturns.ActorIssuerHeader, f.actor.Issuer)
	wrongActor.Header.Set(conversationturns.ActorSubjectHeader, "other")
	wrongActorRecorder := httptest.NewRecorder()
	f.router.ServeHTTP(wrongActorRecorder, wrongActor)
	if wrongActorRecorder.Code != http.StatusForbidden {
		t.Fatalf("mismatched actor status=%d", wrongActorRecorder.Code)
	}
	cookieRequest := httptest.NewRequest(http.MethodGet, base+"/calls/"+call.CallID, nil)
	cookieRequest.Header.Set("Authorization", "Bearer "+f.serviceKey)
	cookieRequest.Header.Set("Cookie", "aithema_browser_session=forbidden")
	cookieRequest.Header.Set(conversationturns.ActorIssuerHeader, f.actor.Issuer)
	cookieRequest.Header.Set(conversationturns.ActorSubjectHeader, f.actor.Subject)
	cookieRecorder := httptest.NewRecorder()
	f.router.ServeHTTP(cookieRecorder, cookieRequest)
	if cookieRecorder.Code != http.StatusForbidden {
		t.Fatalf("browser cookie on service lane status=%d", cookieRecorder.Code)
	}
	foreign := fmt.Sprintf("/api/projects/%d/conversation/v1/calls/%s", f.foreignProjectID, call.CallID)
	if response := f.request(t, http.MethodGet, foreign, nil, f.serviceKey, true); response.Code != http.StatusForbidden {
		t.Fatalf("cross-project status=%d body=%s", response.Code, response.Body.String())
	}
	wrongGenerationPath := fmt.Sprintf("/api/projects/%d/runtimes/%s/conversation/v1/claim", f.projectID, f.runtime.ID)
	if response := f.request(t, http.MethodPost, wrongGenerationPath, conversationturns.ClaimRequest{SchemaVersion: 1, Generation: uuid.NewString()}, f.runnerKey, false); response.Code != http.StatusForbidden {
		t.Fatalf("wrong generation status=%d body=%s", response.Code, response.Body.String())
	}
	if response := f.enroll(t, "acct-other", f.runtime.Generation); response.Code != http.StatusForbidden {
		t.Fatalf("wrong account enrollment status=%d body=%s", response.Code, response.Body.String())
	}
	if response := f.enroll(t, "acct-main", uuid.NewString()); response.Code != http.StatusForbidden {
		t.Fatalf("wrong runtime generation enrollment status=%d body=%s", response.Code, response.Body.String())
	}
	wrongBindingRevision := f.callRequest("request-wrong-revision", "conversation-wrong-revision", "chat")
	wrongBindingRevision.BindingRevision = 2
	if response := f.request(t, http.MethodPost, base+"/calls", wrongBindingRevision, f.serviceKey, true); response.Code != http.StatusForbidden {
		t.Fatalf("wrong binding revision status=%d body=%s", response.Code, response.Body.String())
	}
	claimPath := fmt.Sprintf("/api/projects/%d/runtimes/%s/conversation/v1/claim", f.projectID, f.runtime.ID)
	claimBody, _ := json.Marshal(conversationturns.ClaimRequest{SchemaVersion: 1, Generation: f.runtime.Generation})
	badLeaseRequest := httptest.NewRequest(http.MethodPost, claimPath, bytes.NewReader(claimBody))
	badLeaseRequest.Header.Set("Content-Type", "application/json")
	badLeaseRequest.Header.Set("Authorization", "Bearer "+f.runnerKey)
	badLeaseRequest.Header.Set(conversationturns.RuntimeLeaseHeader, strings.Repeat("B", 43))
	badLeaseRecorder := httptest.NewRecorder()
	f.router.ServeHTTP(badLeaseRecorder, badLeaseRequest)
	if badLeaseRecorder.Code != http.StatusForbidden {
		t.Fatalf("wrong lease status=%d body=%s", badLeaseRecorder.Code, badLeaseRecorder.Body.String())
	}
}

func TestConversationServiceCancelDeadlineMalformedAndRevocation(t *testing.T) {
	f := openConversationRouteFixture(t)
	base := fmt.Sprintf("/api/projects/%d/conversation/v1", f.projectID)

	cancelRequest := f.callRequest("request-cancel", "conversation-cancel", "chat")
	cancelledCall := decodeResponse[conversationturns.Call](t, f.request(t, http.MethodPost, base+"/calls", cancelRequest, f.serviceKey, true))
	accountBlockedRequest := f.callRequest("request-account-blocked", "conversation-account-blocked", "chat")
	if response := f.request(t, http.MethodPost, base+"/calls", accountBlockedRequest, f.serviceKey, true); response.Code != http.StatusConflict {
		t.Fatalf("account concurrency status=%d body=%s", response.Code, response.Body.String())
	}
	cancelled := f.request(t, http.MethodPost, base+"/calls/"+cancelledCall.CallID+"/cancel", struct{}{}, f.serviceKey, true)
	if cancelled.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, cancelled).State != "cancelled" {
		t.Fatalf("queued cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	released := f.request(t, http.MethodPost, base+"/calls", accountBlockedRequest, f.serviceKey, true)
	if released.Code != http.StatusAccepted {
		t.Fatalf("released account status=%d body=%s", released.Code, released.Body.String())
	}
	releasedCall := decodeResponse[conversationturns.Call](t, released)
	if response := f.request(t, http.MethodPost, base+"/calls/"+releasedCall.CallID+"/cancel", struct{}{}, f.serviceKey, true); response.Code != http.StatusOK {
		t.Fatalf("released account cleanup status=%d body=%s", response.Code, response.Body.String())
	}

	deadlineRequest := f.callRequest("request-deadline", "conversation-deadline", "chat")
	deadlineRequest.TimeoutMS = 1
	deadlineCall := decodeResponse[conversationturns.Call](t, f.request(t, http.MethodPost, base+"/calls", deadlineRequest, f.serviceKey, true))
	time.Sleep(5 * time.Millisecond)
	deadline := f.request(t, http.MethodGet, base+"/calls/"+deadlineCall.CallID, nil, f.serviceKey, true)
	deadlineResult := decodeResponse[conversationturns.Call](t, deadline)
	if deadline.Code != http.StatusOK || deadlineResult.State != "failed" || deadlineResult.ErrorCode != "deadline_exceeded" {
		t.Fatalf("deadline status=%d call=%+v", deadline.Code, deadlineResult)
	}

	malformedRequest := f.callRequest("request-malformed", "conversation-malformed", "chat")
	malformedCall := decodeResponse[conversationturns.Call](t, f.request(t, http.MethodPost, base+"/calls", malformedRequest, f.serviceKey, true))
	claim := f.claim(t).Claim
	if claim == nil || claim.Call.CallID != malformedCall.CallID {
		t.Fatalf("malformed call claim=%+v", claim)
	}
	started := conversationturns.Event{Sequence: 1, Kind: "started", ThreadID: "thread-malformed", TurnID: "turn-malformed"}
	if response := f.report(t, malformedCall.CallID, claim.ExecutionGeneration, started); response.Code != http.StatusOK {
		t.Fatalf("malformed start status=%d body=%s", response.Code, response.Body.String())
	}
	delta := conversationturns.Event{Sequence: 2, Kind: "assistant_delta", Text: "partial", ThreadID: started.ThreadID, TurnID: started.TurnID}
	if response := f.report(t, malformedCall.CallID, claim.ExecutionGeneration, delta); response.Code != http.StatusOK {
		t.Fatalf("malformed delta status=%d body=%s", response.Code, response.Body.String())
	}
	badComplete := conversationturns.Event{Sequence: 3, Kind: "completed", ThreadID: started.ThreadID, TurnID: started.TurnID, OutputSHA256: strings.Repeat("0", 64)}
	if response := f.report(t, malformedCall.CallID, claim.ExecutionGeneration, badComplete); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed completion status=%d body=%s", response.Code, response.Body.String())
	}
	running := decodeResponse[conversationturns.Call](t, f.request(t, http.MethodGet, base+"/calls/"+malformedCall.CallID, nil, f.serviceKey, true))
	if running.State != "running" || running.OutputText != "" {
		t.Fatalf("malformed completion escaped: %+v", running)
	}
	failed := conversationturns.Event{Sequence: 3, Kind: "failed", ThreadID: started.ThreadID, TurnID: started.TurnID, ErrorCode: "malformed_completion"}
	if response := f.report(t, malformedCall.CallID, claim.ExecutionGeneration, failed); response.Code != http.StatusOK {
		t.Fatalf("explicit failure status=%d body=%s", response.Code, response.Body.String())
	}

	runningCancelRequest := f.callRequest("request-running-cancel", "conversation-running-cancel", "chat")
	runningCancelCall := decodeResponse[conversationturns.Call](t, f.request(t, http.MethodPost, base+"/calls", runningCancelRequest, f.serviceKey, true))
	runningClaim := f.claim(t).Claim
	started = conversationturns.Event{Sequence: 1, Kind: "started", ThreadID: "thread-cancel", TurnID: "turn-cancel"}
	if response := f.report(t, runningCancelCall.CallID, runningClaim.ExecutionGeneration, started); response.Code != http.StatusOK {
		t.Fatalf("running cancel start status=%d body=%s", response.Code, response.Body.String())
	}
	cancel := f.request(t, http.MethodPost, base+"/calls/"+runningCancelCall.CallID+"/cancel", struct{}{}, f.serviceKey, true)
	if decodeResponse[conversationturns.Call](t, cancel).State != "cancel_requested" {
		t.Fatalf("running cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	controlPath := fmt.Sprintf("/api/projects/%d/runtimes/%s/conversation/v1/calls/%s/control?execution_generation=%s",
		f.projectID, f.runtime.ID, runningCancelCall.CallID, runningClaim.ExecutionGeneration)
	controlRequest := httptest.NewRequest(http.MethodGet, controlPath, nil)
	controlRequest.Header.Set("Authorization", "Bearer "+f.runnerKey)
	controlRequest.Header.Set(conversationturns.RuntimeLeaseHeader, conversationTestLease)
	controlRecorder := httptest.NewRecorder()
	f.router.ServeHTTP(controlRecorder, controlRequest)
	control := decodeResponse[conversationturns.Control](t, controlRecorder)
	if controlRecorder.Code != http.StatusOK || control.Continue || !control.CancelRequested {
		t.Fatalf("cancel control status=%d control=%+v", controlRecorder.Code, control)
	}
	confirmed := conversationturns.Event{Sequence: 2, Kind: "cancelled", ThreadID: started.ThreadID, TurnID: started.TurnID}
	if response := f.report(t, runningCancelCall.CallID, runningClaim.ExecutionGeneration, confirmed); response.Code != http.StatusOK {
		t.Fatalf("cancel confirmation status=%d body=%s", response.Code, response.Body.String())
	}

	revokedRequest := f.callRequest("request-revoked", "conversation-revoked", "chat")
	revokedCall := decodeResponse[conversationturns.Call](t, f.request(t, http.MethodPost, base+"/calls", revokedRequest, f.serviceKey, true))
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')`, f.actorID, f.projectID); err != nil {
		t.Fatal(err)
	}
	if response := f.request(t, http.MethodGet, base+"/calls/"+revokedCall.CallID, nil, f.serviceKey, true); response.Code != http.StatusForbidden {
		t.Fatalf("revoked actor read status=%d body=%s", response.Code, response.Body.String())
	}
	if claim := f.claim(t); claim.Claim != nil {
		t.Fatalf("revoked actor call was claimable: %+v", claim)
	}
	if _, err := db.DB.Exec(`UPDATE project_members SET access_level='editor' WHERE user_id=? AND project_id=?`, f.actorID, f.projectID); err != nil {
		t.Fatal(err)
	}
	leaseRequest := f.callRequest("request-expired-lease", "conversation-expired-lease", "chat")
	leaseCallResponse := f.request(t, http.MethodPost, base+"/calls", leaseRequest, f.serviceKey, true)
	if leaseCallResponse.Code != http.StatusAccepted {
		t.Fatalf("pre-expiry admission status=%d body=%s", leaseCallResponse.Code, leaseCallResponse.Body.String())
	}
	leaseCall := decodeResponse[conversationturns.Call](t, leaseCallResponse)
	if _, err := db.DB.Exec(`UPDATE lifecycle_runtimes SET expires_at=? WHERE id=?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), f.runtime.ID); err != nil {
		t.Fatal(err)
	}
	if response := f.request(t, http.MethodGet, base+"/calls/"+leaseCall.CallID, nil, f.serviceKey, true); response.Code != http.StatusForbidden {
		t.Fatalf("expired runtime service read status=%d body=%s", response.Code, response.Body.String())
	}
	if response := f.request(t, http.MethodPost, fmt.Sprintf("/api/projects/%d/runtimes/%s/conversation/v1/claim", f.projectID, f.runtime.ID),
		conversationturns.ClaimRequest{SchemaVersion: 1, Generation: f.runtime.Generation}, f.runnerKey, false); response.Code != http.StatusForbidden {
		t.Fatalf("expired runtime claim status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationServiceStartedRaceStaysCancelledAndReleasesOnlyOnTerminalEvidence(t *testing.T) {
	for _, test := range []struct {
		name      string
		timeoutMS int64
		cancel    bool
		revoke    bool
	}{
		{name: "cancel", timeoutMS: 60_000, cancel: true},
		{name: "deadline", timeoutMS: 500},
		{name: "authority_revoked", timeoutMS: 60_000, revoke: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := openConversationRouteFixture(t)
			base := fmt.Sprintf("/api/projects/%d/conversation/v1", f.projectID)
			request := f.callRequest("started-race-"+test.name, "started-race-"+test.name, "chat")
			request.TimeoutMS = test.timeoutMS
			admitted := decodeResponse[conversationturns.Call](t,
				f.request(t, http.MethodPost, base+"/calls", request, f.serviceKey, true))
			claim := f.claim(t).Claim
			if claim == nil || claim.Call.CallID != admitted.CallID {
				t.Fatalf("claim=%+v admitted=%+v", claim, admitted)
			}
			if test.cancel {
				cancelled := f.request(t, http.MethodPost, base+"/calls/"+admitted.CallID+"/cancel", struct{}{}, f.serviceKey, true)
				if cancelled.Code != http.StatusOK || decodeResponse[conversationturns.Call](t, cancelled).State != "cancel_requested" {
					t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
				}
			} else if test.revoke {
				if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')`, f.actorID, f.projectID); err != nil {
					t.Fatal(err)
				}
			} else {
				time.Sleep(550 * time.Millisecond)
			}

			started := conversationturns.Event{Sequence: 1, Kind: "started", ThreadID: "thread-race", TurnID: "turn-race"}
			startedResponse := f.report(t, admitted.CallID, claim.ExecutionGeneration, started)
			startedCall := decodeResponse[conversationturns.Call](t, startedResponse)
			if startedResponse.Code != http.StatusOK || startedCall.State != "cancel_requested" || startedCall.DeadlineAt != admitted.DeadlineAt {
				t.Fatalf("started race status=%d call=%+v", startedResponse.Code, startedCall)
			}
			var released int
			if err := db.DB.QueryRow(`SELECT released_at IS NOT NULL FROM conversation_account_slots WHERE call_id=?`, admitted.CallID).Scan(&released); err != nil || released != 0 {
				t.Fatalf("slot released before stopped evidence=%d err=%v", released, err)
			}
			delta := conversationturns.Event{Sequence: 2, Kind: "assistant_delta", Text: "forbidden", ThreadID: started.ThreadID, TurnID: started.TurnID}
			wantOutputStatus := http.StatusConflict
			if test.revoke {
				wantOutputStatus = http.StatusForbidden
			}
			if response := f.report(t, admitted.CallID, claim.ExecutionGeneration, delta); response.Code != wantOutputStatus {
				t.Fatalf("post-cancel output status=%d want=%d body=%s", response.Code, wantOutputStatus, response.Body.String())
			}
			emptyDigest := sha256.Sum256(nil)
			completed := conversationturns.Event{Sequence: 2, Kind: "completed", ThreadID: started.ThreadID, TurnID: started.TurnID,
				OutputSHA256: hex.EncodeToString(emptyDigest[:])}
			if response := f.report(t, admitted.CallID, claim.ExecutionGeneration, completed); response.Code != wantOutputStatus {
				t.Fatalf("post-cancel completion status=%d want=%d body=%s", response.Code, wantOutputStatus, response.Body.String())
			}
			terminal := conversationturns.Event{Sequence: 2, Kind: "cancelled", ThreadID: started.ThreadID, TurnID: started.TurnID, ErrorCode: "cancelled"}
			terminalResponse := f.report(t, admitted.CallID, claim.ExecutionGeneration, terminal)
			terminalCall := decodeResponse[conversationturns.Call](t, terminalResponse)
			if terminalResponse.Code != http.StatusOK || terminalCall.State != "cancelled" || terminalCall.OutputText != "" {
				t.Fatalf("terminal status=%d call=%+v", terminalResponse.Code, terminalCall)
			}
			if replay := f.report(t, admitted.CallID, claim.ExecutionGeneration, terminal); replay.Code != http.StatusOK {
				t.Fatalf("terminal replay status=%d body=%s", replay.Code, replay.Body.String())
			}
			if err := db.DB.QueryRow(`SELECT released_at IS NOT NULL FROM conversation_account_slots WHERE call_id=?`, admitted.CallID).Scan(&released); err != nil || released != 1 {
				t.Fatalf("slot not released after stopped evidence=%d err=%v", released, err)
			}
			if test.revoke {
				if _, err := db.DB.Exec(`UPDATE project_members SET access_level='editor' WHERE user_id=? AND project_id=?`, f.actorID, f.projectID); err != nil {
					t.Fatal(err)
				}
			}
			next := f.callRequest("started-race-next-"+test.name, "started-race-next-"+test.name, "chat")
			if response := f.request(t, http.MethodPost, base+"/calls", next, f.serviceKey, true); response.Code != http.StatusAccepted {
				t.Fatalf("account remained stranded status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
