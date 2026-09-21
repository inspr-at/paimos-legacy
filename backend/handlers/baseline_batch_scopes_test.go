package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func TestBaselineBatchV3ScopedChoicesAndReview(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Scoped workers", "key": "SCP"}))
	optIn(t, ts, projectID)
	userID := userIDFor(t, "admin")
	workspace := uuid.NewString()
	identity := strings.Repeat("a", 64)
	v3ID, v3Gen := insertSyntheticRuntime(t, projectID, userID, map[string]any{
		"generation":     uuid.NewString(),
		"host":           "fixture-host",
		"schema_version": 3,
		"workspaces":     []map[string]any{{"handle": workspace, "identity": identity, "label": "Work"}},
		"account_scopes": []map[string]any{
			{
				"account_label": "chatgpt",
				"accounts":      []map[string]string{{"key": "codex-work", "label": "Work"}},
				"profiles":      []map[string]string{{"id": "codex-sol-high", "version": "2"}},
			},
			{
				"account_label": "cursor_context",
				"accounts":      []map[string]string{{"key": "cursor-op", "label": "Cursor"}},
				"profiles":      []map[string]string{{"id": "cursor-composer", "version": "2"}},
			},
		},
	})
	claudeWS := uuid.NewString()
	claudeID, claudeGen := insertSyntheticRuntime(t, projectID, userID, map[string]any{
		"generation":     uuid.NewString(),
		"host":           "fixture-claude",
		"schema_version": 3,
		"workspaces":     []map[string]any{{"handle": claudeWS, "identity": identity, "label": "Claude"}},
		"account_scopes": []map[string]any{
			{
				"account_label": "claude_ai_max",
				"profiles":      []map[string]string{{"id": "claude-opus-xhigh", "version": "2"}},
			},
		},
	})
	v2WS := uuid.NewString()
	v2ID, v2Gen := insertSyntheticRuntime(t, projectID, userID, map[string]any{
		"generation":     uuid.NewString(),
		"host":           "fixture-v2",
		"schema_version": 2,
		"account_label":  "chatgpt",
		"accounts":       []map[string]string{{"key": "coordinator", "label": "Coordinator"}},
		"profiles":       []map[string]string{{"id": "codex-sol-high", "version": "2"}},
		"workspaces":     []map[string]any{{"handle": v2WS, "identity": identity, "label": "V2"}},
	})

	workflow := workflowOf(t, ts, projectID)
	v3 := findChoice(t, workflow, v3ID)
	if v3.SchemaVersion != lifecycleintents.AccountScopeSchemaV3 || len(v3.Accounts) != 0 || len(v3.Profiles) != 0 || len(v3.AccountScopes) != 2 {
		t.Fatalf("v3 choice flattened: %+v", v3)
	}
	if v3.AccountScopes[0].AccountLabel != "chatgpt" || v3.AccountScopes[1].AccountLabel != "cursor_context" {
		t.Fatalf("v3 scopes=%+v", v3.AccountScopes)
	}
	v2 := findChoice(t, workflow, v2ID)
	if v2.SchemaVersion != lifecycleintents.AccountChoiceSchemaV2 || v2.AccountLabel != "chatgpt" || len(v2.Accounts) != 1 || len(v2.AccountScopes) != 0 {
		t.Fatalf("v2 choice lost: %+v", v2)
	}

	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)

	codexWorker := map[string]any{
		"worker_name": "builder", "runtime_id": v3ID, "runtime_generation": v3Gen,
		"account_label": "chatgpt", "account_key": "codex-work",
		"profile_id": "codex-sol-high", "profile_version": "2", "workspace_handle": workspace,
	}
	cursorWorker := map[string]any{
		"worker_name": "builder", "runtime_id": v3ID, "runtime_generation": v3Gen,
		"account_label": "cursor_context", "account_key": "cursor-op",
		"profile_id": "cursor-composer", "profile_version": "2", "workspace_handle": workspace,
	}
	classOnly := map[string]any{
		"worker_name": "builder", "runtime_id": claudeID, "runtime_generation": claudeGen,
		"account_label": "claude_ai_max", "profile_id": "claude-opus-xhigh", "profile_version": "2",
		"workspace_handle": claudeWS,
	}
	v2Worker := map[string]any{
		"worker_name": "builder", "runtime_id": v2ID, "runtime_generation": v2Gen,
		"account_label": "chatgpt", "account_key": "coordinator",
		"profile_id": "codex-sol-high", "profile_version": "2", "workspace_handle": v2WS,
	}

	crossedBefore := map[string]any{
		"worker_name": "builder", "runtime_id": v3ID, "runtime_generation": v3Gen,
		"account_label": "chatgpt", "account_key": "codex-work",
		"profile_id": "cursor-composer", "profile_version": "2", "workspace_handle": workspace,
	}
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": crossedBefore}); resp.StatusCode != 200 {
		t.Fatalf("patch crossed worker=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), map[string]any{}); resp.StatusCode != 400 {
		t.Fatalf("readiness crossed profile=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	reviewed := decodeDraft(t, reviewWorker(t, ts, projectID, draft.ID, "assisted", codexWorker))
	if reviewed.Worker.AccountLabel != "chatgpt" || reviewed.Worker.AccountKey != "codex-work" ||
		reviewed.Worker.RuntimeGeneration != v3Gen || reviewed.Worker.ProfileID != "codex-sol-high" {
		t.Fatalf("codex worker not preserved: %+v", reviewed.Worker)
	}

	reviewed = decodeDraft(t, reviewWorker(t, ts, projectID, draft.ID, "assisted", cursorWorker))
	if reviewed.Worker.AccountLabel != "cursor_context" || reviewed.Worker.AccountKey != "cursor-op" {
		t.Fatalf("cursor worker not preserved: %+v", reviewed.Worker)
	}

	reviewed = decodeDraft(t, reviewWorker(t, ts, projectID, draft.ID, "assisted", classOnly))
	if reviewed.Worker.AccountLabel != "claude_ai_max" || reviewed.Worker.AccountKey != "" || reviewed.Worker.ProfileID != "claude-opus-xhigh" {
		t.Fatalf("class-only invented a key: %+v", reviewed.Worker)
	}
	draft = reviewed

	if resp := reviewWorker(t, ts, projectID, draft.ID, "assisted", v2Worker); resp.StatusCode != 200 {
		t.Fatalf("v2 review=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	crossed := map[string]any{
		"worker_name": "builder", "runtime_id": v3ID, "runtime_generation": v3Gen,
		"account_label": "chatgpt", "account_key": "codex-work",
		"profile_id": "cursor-composer", "profile_version": "2", "workspace_handle": workspace,
	}
	if resp := reviewWorker(t, ts, projectID, draft.ID, "assisted", crossed); resp.StatusCode != 400 {
		t.Fatalf("cross-scope profile=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	cursorOnly := map[string]any{
		"worker_name": "builder", "runtime_id": v3ID, "runtime_generation": v3Gen,
		"account_label": "cursor_context", "profile_id": "cursor-composer", "profile_version": "2",
		"workspace_handle": workspace,
	}
	if resp := reviewWorker(t, ts, projectID, draft.ID, "assisted", cursorOnly); resp.StatusCode != 400 {
		t.Fatalf("cursor without key=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	invented := map[string]any{
		"worker_name": "builder", "runtime_id": claudeID, "runtime_generation": claudeGen,
		"account_label": "claude_ai_max", "account_key": "claude-home",
		"profile_id": "claude-opus-xhigh", "profile_version": "2", "workspace_handle": claudeWS,
	}
	if resp := reviewWorker(t, ts, projectID, draft.ID, "assisted", invented); resp.StatusCode != 400 {
		t.Fatalf("invented class-only key=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	staleGen := map[string]any{
		"worker_name": "builder", "runtime_id": v3ID, "runtime_generation": uuid.NewString(),
		"account_label": "chatgpt", "account_key": "codex-work",
		"profile_id": "codex-sol-high", "profile_version": "2", "workspace_handle": workspace,
	}
	if resp := reviewWorker(t, ts, projectID, draft.ID, "assisted", staleGen); resp.StatusCode != 400 {
		t.Fatalf("stale generation=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	manual := reviewWorker(t, ts, projectID, draft.ID, "manual", codexWorker)
	draft = decodeDraft(t, manual)
	if draft.Worker != (baselinebatch.WorkerSelection{}) {
		t.Fatalf("manual kept worker authority: %+v", draft.Worker)
	}
}

func TestBaselineBatchUnknownSchemaFailsClosed(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Unknown schema", "key": "UNK"}))
	optIn(t, ts, projectID)
	userID := userIDFor(t, "admin")
	workspace := uuid.NewString()
	id, gen := insertSyntheticRuntime(t, projectID, userID, map[string]any{
		"generation":     uuid.NewString(),
		"host":           "fixture-unknown",
		"schema_version": 9,
		"account_label":  "chatgpt",
		"profiles":       []map[string]string{{"id": "codex-sol-high", "version": "2"}},
		"workspaces":     []map[string]any{{"handle": workspace, "identity": strings.Repeat("a", 64)}},
	})
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	worker := map[string]any{
		"worker_name": "builder", "runtime_id": id, "runtime_generation": gen,
		"account_label": "chatgpt", "profile_id": "codex-sol-high", "profile_version": "2",
		"workspace_handle": workspace,
	}
	if resp := reviewWorker(t, ts, projectID, draft.ID, "assisted", worker); resp.StatusCode != 400 {
		t.Fatalf("unknown schema review=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
}

func userIDFor(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username=?`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertSyntheticRuntime(t *testing.T, projectID, userID int64, registration map[string]any) (string, string) {
	t.Helper()
	body, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	generation := uuid.NewString()
	res, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,?,?,'fixture','*')`, userID, "scoped-fixture-"+id, id)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := res.LastInsertId()
	_, err = db.DB.Exec(`INSERT INTO lifecycle_runtimes(
		id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,expires_at,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		id, projectID, generation, "fixture-host", userID, keyID, make([]byte, 32), string(body),
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	return id, generation
}

func findChoice(t *testing.T, workflow baselinebatch.Workflow, runtimeID string) baselinebatch.RuntimeChoice {
	t.Helper()
	for _, choice := range workflow.Choices.Runtimes {
		if choice.RuntimeID == runtimeID {
			return choice
		}
	}
	t.Fatalf("runtime %s missing from %+v", runtimeID, workflow.Choices.Runtimes)
	return baselinebatch.RuntimeChoice{}
}

func decodeDraft(t *testing.T, resp *http.Response) baselinebatch.Draft {
	t.Helper()
	if resp.StatusCode != 200 {
		t.Fatalf("review=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	var draft baselinebatch.Draft
	decode(t, resp, &draft)
	return draft
}

func reviewWorker(t *testing.T, ts *testServer, projectID, draftID int64, mode string, worker map[string]any) *http.Response {
	t.Helper()
	return postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draftID), map[string]any{
		"execution_mode":            mode,
		"selected_requirement_refs": []string{"req.login"},
		"worker":                    worker,
	})
}
