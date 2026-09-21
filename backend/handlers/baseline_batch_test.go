package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/db"
)

func TestBaselineBatchVerticalSlice(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "INSPR stream", "key": "INS"}))
	otherID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Legacy", "key": "LEG"}))
	optIn(t, ts, projectID)

	secondReq := baselinebatch.Requirement{
		Ref: "req.two", Statement: "Second small input",
		AcceptanceCriteria: []string{"Magic links expire"}, ConstraintRefs: []string{},
	}
	twoReqs := []baselinebatch.Requirement{{
		Ref: "req.login", Statement: "Users sign in with email",
		AcceptanceCriteria: []string{"Magic links expire"}, ConstraintRefs: []string{},
	}, secondReq}
	digest2, _ := baselinebatch.ContentDigest(twoReqs, nil)
	seal2, _ := baselinebatch.RevisionSeal("baseline:v1", 1, digest2)

	// AC8: a legacy project keeps its behaviour and is never asked for a baseline.
	legacy := ts.get(t, fmt.Sprintf("/api/projects/%d/issues", otherID), ts.adminCookie)
	if legacy.StatusCode != 200 {
		t.Fatalf("legacy project issues=%d", legacy.StatusCode)
	}
	legacyWorkflow := workflowOf(t, ts, otherID)
	if legacyWorkflow.INSPRStreamEnabled || legacyWorkflow.INSPRGating || !legacyWorkflow.LegacyUnaffected {
		t.Fatalf("legacy project gated: %+v", legacyWorkflow)
	}

	// Forged upstream approver is retained as a claim, not authority.
	handover2, _ := handoverWith(t, twoReqs, digest2, seal2)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover":                  json.RawMessage(handover2),
		"selected_requirement_refs": []string{"req.login"},
	})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	if draft.Baseline.Authenticity != baselinebatch.ImportedClaimAuthenticity || draft.Baseline.ImportedClaimedApprovedBy != "party:forged" {
		t.Fatalf("imported claim treated as authority: %+v", draft.Baseline)
	}

	// Two small inputs remain one draft.
	again := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover":                  json.RawMessage(handover2),
		"selected_requirement_refs": []string{"req.two"},
	})
	if again.StatusCode != 201 {
		t.Fatalf("second import=%d %s", again.StatusCode, baselineReadBody(again))
	}
	decode(t, again, &draft)
	if len(draft.Selected.RequirementRefs) != 2 {
		t.Fatalf("selected=%v", draft.Selected.RequirementRefs)
	}

	// Tampered seal and tampered content are both refused on an opted-in project.
	optIn(t, ts, otherID)
	bad := bytes.ReplaceAll(handover2, []byte(draft.Baseline.RevisionSeal), []byte("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", otherID), json.RawMessage(bad)); resp.StatusCode != 400 {
		t.Fatalf("tampered seal=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	tamperedContent := bytes.Replace(handover2, []byte("Users sign in with email"), []byte("Users sign in with SSO!!"), 1)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", otherID), json.RawMessage(tamperedContent)); resp.StatusCode != 400 ||
		!strings.Contains(baselineReadBody(resp), "tampered content_digest") {
		t.Fatalf("tampered content=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	// Executable/ready claims.
	withReady := bytes.Replace(handover2, []byte(`"handover_version"`), []byte(`"ready":true,"handover_version"`), 1)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", otherID), json.RawMessage(withReady)); resp.StatusCode != 400 {
		t.Fatalf("ready:true import=%d", resp.StatusCode)
	}

	// External users are blocked from the internal API group before project
	// visibility is considered.
	if resp := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/", projectID), ts.externalCookie); resp.StatusCode != 403 && resp.StatusCode != 404 {
		t.Fatalf("external view=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	// A draft is reachable only through its own project's path.
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", otherID, draft.ID), map[string]any{
		"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"},
	}); resp.StatusCode != 404 {
		t.Fatalf("cross-project review=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), map[string]any{
		"execution_mode":            "manual",
		"selected_requirement_refs": draft.Selected.RequirementRefs,
	})
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	if draft.ReviewID == nil || !draft.ReviewValid {
		t.Fatalf("review not bound: %+v", draft)
	}

	// Mode change invalidates the unsubmitted review.
	patched := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID), map[string]any{
		"execution_mode": baselinebatch.ModeAssisted,
	})
	if patched.StatusCode != 200 {
		t.Fatalf("patch=%d %s", patched.StatusCode, baselineReadBody(patched))
	}
	decode(t, patched, &draft)
	if draft.ReviewValid {
		t.Fatal("stale review remained valid after mode change")
	}

	// A changed named account invalidates the unsubmitted review too.
	review = postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), map[string]any{
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
	})
	if review.StatusCode != 200 {
		t.Fatalf("manual review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	if !draft.ReviewValid {
		t.Fatalf("review not bound before account change: %+v", draft)
	}
	staleReviewID := *draft.ReviewID
	staleRevision := draft.Revision
	accountChange := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID), map[string]any{
		"worker": map[string]any{"account_key": "someone-else"},
	})
	if accountChange.StatusCode != 200 {
		t.Fatalf("account patch=%d %s", accountChange.StatusCode, baselineReadBody(accountChange))
	}
	var afterAccountChange baselinebatch.Draft
	decode(t, accountChange, &afterAccountChange)
	if afterAccountChange.ReviewValid || afterAccountChange.ReviewID != nil {
		t.Fatalf("changed account left the review valid: %+v", afterAccountChange)
	}
	draft = afterAccountChange

	// Confirming the superseded review is refused as stale, not honoured.
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key":           "stale-review-start-01",
		"review_id":                 staleReviewID,
		"draft_revision":            staleRevision,
		"confirm":                   true,
		"content_digest":            draft.Baseline.ContentDigest,
		"revision_seal":             draft.Baseline.RevisionSeal,
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
		"worker":                    map[string]any{},
	}); resp.StatusCode != 409 || !strings.Contains(baselineReadBody(resp), "stale") {
		t.Fatalf("stale review start=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	review = postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), map[string]any{
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
	})
	if review.StatusCode != 200 {
		t.Fatalf("re-review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)

	startBody := map[string]any{
		"idempotency_key":           "batch-start-key-001",
		"review_id":                 *draft.ReviewID,
		"draft_revision":            draft.Revision,
		"confirm":                   true,
		"content_digest":            draft.Baseline.ContentDigest,
		"revision_seal":             draft.Baseline.RevisionSeal,
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
		"worker":                    map[string]any{},
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), startBody)
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if batch.AttemptID == nil || batch.IssueID == 0 || batch.Status != baselinebatch.BatchActive {
		t.Fatalf("start wiring=%+v", batch)
	}
	if batch.Readiness != nil {
		t.Fatal("manual delivery invented an AI readiness claim")
	}
	measured, guess := forecastKinds(batch.Forecasts)
	if guess == nil || guess.Label != "guessed" || guess.Observed {
		t.Fatalf("educated guess=%+v", guess)
	}
	if measured == nil || measured.Label != "observed" || measured.Percent != 10 || !measured.Observed {
		t.Fatalf("measured forecast=%+v", measured)
	}
	if measured.ETASeconds != nil {
		t.Fatalf("measured must not invent an observed ETA: %+v", measured)
	}
	if measured.EducatedETASeconds == nil || *measured.EducatedETASeconds <= 0 {
		t.Fatalf("measured educated ETA=%+v", measured.EducatedETASeconds)
	}
	if measured.EducatedETALabel != "guessed" {
		t.Fatalf("measured educated label=%q", measured.EducatedETALabel)
	}
	if len(batch.Progress.Stages) != 5 {
		t.Fatalf("canonical stages=%d", len(batch.Progress.Stages))
	}
	spec := stageByKey(batch.Progress.Stages, "specification")
	if spec == nil || !spec.Satisfied || spec.NeverSignaled {
		t.Fatalf("reviewed start did not record specification evidence: %+v", batch.Progress.Stages)
	}
	if batch.Progress.NextAction != baselinebatch.NextActionImplementationEvidence {
		t.Fatalf("next_action=%q", batch.Progress.NextAction)
	}
	for _, key := range []string{"implementation", "qa", "deployment", "verification"} {
		stage := stageByKey(batch.Progress.Stages, key)
		if stage == nil || stage.Satisfied {
			t.Fatalf("start treated %s as done: %+v", key, stage)
		}
	}

	replay := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), startBody)
	if replay.StatusCode != 200 {
		t.Fatalf("idempotent replay=%d %s", replay.StatusCode, baselineReadBody(replay))
	}
	var replayed baselinebatch.Batch
	decode(t, replay, &replayed)
	if replayed.ID != batch.ID || replayed.AttemptID == nil || *replayed.AttemptID != *batch.AttemptID {
		t.Fatalf("replay created a second batch %+v vs %+v", replayed, batch)
	}

	// Client ready:true does not authenticate.
	startBody["idempotency_key"] = "batch-start-key-ready"
	startBody["ready"] = true
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), startBody); resp.StatusCode != 400 {
		t.Fatalf("client ready start=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	// Later input stays in a new draft and cannot mutate the active batch.
	later, _ := validHandover(t)
	laterImport := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover": json.RawMessage(later),
	})
	if laterImport.StatusCode != 201 {
		t.Fatalf("later draft=%d %s", laterImport.StatusCode, baselineReadBody(laterImport))
	}
	var laterDraft baselinebatch.Draft
	decode(t, laterImport, &laterDraft)
	if laterDraft.ID == draft.ID {
		t.Fatal("later input mutated the started draft")
	}
	got := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d", projectID, batch.ID), ts.adminCookie)
	decode(t, got, &batch)
	if len(batch.Scope.RequirementRefs) != 1 || batch.Scope.RequirementRefs[0] != "req.login" {
		t.Fatalf("active scope mutated: %+v", batch.Scope)
	}

	// Manual pause and resume are durable human states with no invented worker.
	paused := postBaseline(t, ts, ts.adminCookie, controlPath(projectID, batch.ID), map[string]any{"action": "pause"})
	if paused.StatusCode != 200 {
		t.Fatalf("manual pause=%d %s", paused.StatusCode, baselineReadBody(paused))
	}
	decode(t, paused, &batch)
	if batch.Status != baselinebatch.BatchPaused {
		t.Fatalf("manual pause state=%s", batch.Status)
	}
	resumed := postBaseline(t, ts, ts.adminCookie, controlPath(projectID, batch.ID), map[string]any{"action": "resume"})
	if resumed.StatusCode != 200 {
		t.Fatalf("manual resume=%d %s", resumed.StatusCode, baselineReadBody(resumed))
	}
	decode(t, resumed, &batch)
	if batch.Status != baselinebatch.BatchActive {
		t.Fatalf("manual resume state=%s", batch.Status)
	}

	// Racing confirmation: exactly one batch, and the loser gets a typed answer.
	review2 := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, laterDraft.ID), map[string]any{
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
	})
	decode(t, review2, &laterDraft)
	cancel := postBaseline(t, ts, ts.adminCookie, controlPath(projectID, batch.ID), map[string]any{"action": "cancel"})
	if cancel.StatusCode != 200 {
		t.Fatalf("cancel=%d %s", cancel.StatusCode, baselineReadBody(cancel))
	}

	startA := map[string]any{
		"idempotency_key":           "race-key-aaaa-bbbb",
		"review_id":                 *laterDraft.ReviewID,
		"draft_revision":            laterDraft.Revision,
		"confirm":                   true,
		"content_digest":            laterDraft.Baseline.ContentDigest,
		"revision_seal":             laterDraft.Baseline.RevisionSeal,
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
		"worker":                    map[string]any{},
	}
	var wg sync.WaitGroup
	type raceOutcome struct {
		code int
		id   int64
		body string
	}
	outcomes := make(chan raceOutcome, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, laterDraft.ID), startA)
			out := raceOutcome{code: resp.StatusCode, body: baselineReadBody(resp)}
			if resp.StatusCode == 200 {
				var b baselinebatch.Batch
				decode(t, resp, &b)
				out.id = b.ID
			}
			outcomes <- out
		}()
	}
	wg.Wait()
	close(outcomes)
	seen := map[int64]bool{}
	for outcome := range outcomes {
		switch outcome.code {
		case 200:
			seen[outcome.id] = true
		case 409:
		default:
			t.Fatalf("racing start answered %d: %s", outcome.code, outcome.body)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("racing start produced %d batches", len(seen))
	}
	var batchRows int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM baseline_batch_batches WHERE project_id=? AND idempotency_key=?`,
		projectID, "race-key-aaaa-bbbb").Scan(&batchRows); err != nil {
		t.Fatal(err)
	}
	if batchRows != 1 {
		t.Fatalf("racing start durably created %d batches", batchRows)
	}

	// Agent mode without an owned observation blocks, and manual stays usable.
	// Review binds only a current advertised generation; invented IDs fail
	// closed before start instead of pretending a missing host was reviewed.
	blockedProject := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Agent stream", "key": "AGT"}))
	optIn(t, ts, blockedProject)
	blockedHandover, _ := validHandover(t)
	imp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", blockedProject), map[string]any{"handover": json.RawMessage(blockedHandover)})
	decode(t, imp, &draft)
	invented := map[string]any{
		"worker_name": "codex", "runtime_id": uuid.NewString(), "runtime_generation": uuid.NewString(),
		"account_label": "chatgpt", "account_key": "coordinator", "profile_id": "codex-sol-high",
		"profile_version": "2", "workspace_handle": uuid.NewString(),
	}
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", blockedProject, draft.ID), map[string]any{
		"execution_mode": "automatic", "worker": invented, "selected_requirement_refs": []string{"req.login"},
	}); resp.StatusCode != 400 || !strings.Contains(baselineReadBody(resp), "current advertised generation") {
		t.Fatalf("invented generation review=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	workspace := uuid.NewString()
	runtimeID, runtimeGen := insertSyntheticRuntime(t, blockedProject, userIDFor(t, "admin"), map[string]any{
		"generation": uuid.NewString(), "host": "fixture-host", "schema_version": 2,
		"account_label": "chatgpt",
		"accounts":      []map[string]string{{"key": "coordinator", "label": "Coordinator"}},
		"profiles":      []map[string]string{{"id": "codex-sol-high", "version": "2"}},
		"workspaces":    []map[string]any{{"handle": workspace, "identity": strings.Repeat("a", 64), "label": "Work"}},
	})
	worker := map[string]any{
		"worker_name": "codex", "runtime_id": runtimeID, "runtime_generation": runtimeGen,
		"account_label": "chatgpt", "account_key": "coordinator", "profile_id": "codex-sol-high",
		"profile_version": "2", "workspace_handle": workspace,
	}
	rev := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", blockedProject, draft.ID), map[string]any{
		"execution_mode": "automatic", "worker": worker, "selected_requirement_refs": []string{"req.login"},
	})
	if rev.StatusCode != 200 {
		t.Fatalf("auto review=%d %s", rev.StatusCode, baselineReadBody(rev))
	}
	decode(t, rev, &draft)
	autoStart := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", blockedProject, draft.ID), map[string]any{
		"idempotency_key":           "auto-missing-proof",
		"review_id":                 *draft.ReviewID,
		"draft_revision":            draft.Revision,
		"confirm":                   true,
		"content_digest":            draft.Baseline.ContentDigest,
		"revision_seal":             draft.Baseline.RevisionSeal,
		"execution_mode":            "automatic",
		"selected_requirement_refs": []string{"req.login"},
		"worker":                    worker,
	})
	if autoStart.StatusCode != 409 || !strings.Contains(baselineReadBody(autoStart), "readiness_observation_missing") {
		t.Fatalf("missing observation start=%d %s", autoStart.StatusCode, baselineReadBody(autoStart))
	}

	// Deleted project invalidates stale authority.
	if resp := ts.del(t, fmt.Sprintf("/api/projects/%d", otherID), ts.adminCookie); resp.StatusCode != 200 && resp.StatusCode != 204 {
		t.Fatalf("delete project=%d", resp.StatusCode)
	}
	if resp := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/", otherID), ts.adminCookie); resp.StatusCode != 404 {
		t.Fatalf("deleted project workflow=%d", resp.StatusCode)
	}

	workflow := workflowOf(t, ts, projectID)
	if !workflow.INSPRStreamEnabled || !workflow.INSPRGating || workflow.LegacyUnaffected {
		t.Fatalf("gating flags %+v", workflow)
	}
	if len(workflow.Batches) < 2 {
		t.Fatalf("batch history=%d", len(workflow.Batches))
	}
}

func TestBaselineBatchStartChangedScopeAndOrder(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Scope order", "key": "SCO"}))
	optIn(t, ts, projectID)

	twoReqs := []baselinebatch.Requirement{{
		Ref: "req.login", Statement: "Users sign in with email",
		AcceptanceCriteria: []string{"Magic links expire"}, ConstraintRefs: []string{},
	}, {
		Ref: "req.two", Statement: "Second small input",
		AcceptanceCriteria: []string{"Magic links expire"}, ConstraintRefs: []string{},
	}}
	handover, _ := handoverWith(t, twoReqs, "", "")
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover":                  json.RawMessage(handover),
		"selected_requirement_refs": []string{"req.login", "req.two"},
	})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)

	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), map[string]any{
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.two", "req.login"},
	})
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	if draft.ReviewID == nil || !draft.ReviewValid {
		t.Fatalf("review not bound: %+v", draft)
	}
	if len(draft.Selected.RequirementRefs) != 2 || draft.Selected.RequirementRefs[0] != "req.login" || draft.Selected.RequirementRefs[1] != "req.two" {
		t.Fatalf("reviewed canonical scope=%v", draft.Selected.RequirementRefs)
	}

	startPath := fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID)
	startBody := map[string]any{
		"idempotency_key":           "changed-scope-start-01",
		"review_id":                 *draft.ReviewID,
		"draft_revision":            draft.Revision,
		"confirm":                   true,
		"content_digest":            draft.Baseline.ContentDigest,
		"revision_seal":             draft.Baseline.RevisionSeal,
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.two"},
		"worker":                    map[string]any{},
	}
	changed := postBaseline(t, ts, ts.adminCookie, startPath, startBody)
	if changed.StatusCode != 409 || !strings.Contains(baselineReadBody(changed), "confirmation does not bind reviewed mode, scope, or worker") {
		t.Fatalf("changed-scope start=%d %s", changed.StatusCode, baselineReadBody(changed))
	}
	still := workflowOf(t, ts, projectID)
	if still.Draft == nil || !still.Draft.ReviewValid || still.Draft.ReviewID == nil || *still.Draft.ReviewID != *draft.ReviewID {
		t.Fatalf("changed-scope start consumed or invalidated the review: %+v", still.Draft)
	}

	startBody["idempotency_key"] = "reordered-scope-start-01"
	startBody["selected_requirement_refs"] = []string{"req.two", "req.login"}
	reordered := postBaseline(t, ts, ts.adminCookie, startPath, startBody)
	if reordered.StatusCode != 200 {
		t.Fatalf("reordered same-scope start=%d %s", reordered.StatusCode, baselineReadBody(reordered))
	}
	var batch baselinebatch.Batch
	decode(t, reordered, &batch)
	if len(batch.Scope.RequirementRefs) != 2 || batch.Scope.RequirementRefs[0] != "req.login" || batch.Scope.RequirementRefs[1] != "req.two" {
		t.Fatalf("canonical started scope=%v", batch.Scope.RequirementRefs)
	}
}

func TestBaselineBatchStartDelimiterCollidingMemberships(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Scope delimiter", "key": "SDL"}))
	optIn(t, ts, projectID)

	fourReqs := []baselinebatch.Requirement{{
		Ref: "req.a,req.b", Statement: "Comma pair left",
		AcceptanceCriteria: []string{"Accepted"}, ConstraintRefs: []string{},
	}, {
		Ref: "req.c", Statement: "Single right",
		AcceptanceCriteria: []string{"Accepted"}, ConstraintRefs: []string{},
	}, {
		Ref: "req.a", Statement: "Single left",
		AcceptanceCriteria: []string{"Accepted"}, ConstraintRefs: []string{},
	}, {
		Ref: "req.b,req.c", Statement: "Comma pair right",
		AcceptanceCriteria: []string{"Accepted"}, ConstraintRefs: []string{},
	}}
	digest, err := baselinebatch.ContentDigest(fourReqs, nil)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := baselinebatch.RevisionSeal("baseline:v1", 1, digest)
	if err != nil {
		t.Fatal(err)
	}
	handover, _ := handoverWith(t, fourReqs, digest, seal)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover":                  json.RawMessage(handover),
		"selected_requirement_refs": []string{"req.a,req.b", "req.c", "req.a", "req.b,req.c"},
	})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	if draft.Baseline.ContentDigest != digest || draft.Baseline.RevisionSeal != seal {
		t.Fatalf("imported digest drifted: %s %s", draft.Baseline.ContentDigest, draft.Baseline.RevisionSeal)
	}
	if len(draft.Requirements) != 4 {
		t.Fatalf("authentic baseline refs=%d", len(draft.Requirements))
	}

	reviewA := []string{"req.a,req.b", "req.c"}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), map[string]any{
		"execution_mode":            "manual",
		"selected_requirement_refs": reviewA,
	})
	if review.StatusCode != 200 {
		t.Fatalf("review A=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	if draft.ReviewID == nil || !draft.ReviewValid {
		t.Fatalf("review A not bound: %+v", draft)
	}
	reviewID := *draft.ReviewID
	revision := draft.Revision
	selectedAfterReview := append([]string{}, draft.Selected.RequirementRefs...)

	startPath := fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID)
	startB := postBaseline(t, ts, ts.adminCookie, startPath, map[string]any{
		"idempotency_key":           "delimiter-collide-start-b",
		"review_id":                 reviewID,
		"draft_revision":            revision,
		"confirm":                   true,
		"content_digest":            draft.Baseline.ContentDigest,
		"revision_seal":             draft.Baseline.RevisionSeal,
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.a", "req.b,req.c"},
		"worker":                    map[string]any{},
	})
	if startB.StatusCode != 409 || !strings.Contains(baselineReadBody(startB), "confirmation does not bind reviewed mode, scope, or worker") {
		t.Fatalf("start B=%d %s", startB.StatusCode, baselineReadBody(startB))
	}

	still := workflowOf(t, ts, projectID)
	if still.Draft == nil || still.Draft.ID != draft.ID || still.Draft.Revision != revision || !still.Draft.ReviewValid || still.Draft.ReviewID == nil || *still.Draft.ReviewID != reviewID {
		t.Fatalf("start B patched or invalidated the review: %+v", still.Draft)
	}
	if len(still.Draft.Selected.RequirementRefs) != len(selectedAfterReview) {
		t.Fatalf("start B patched selected=%v", still.Draft.Selected.RequirementRefs)
	}
	for i, ref := range selectedAfterReview {
		if still.Draft.Selected.RequirementRefs[i] != ref {
			t.Fatalf("start B patched selected=%v want %v", still.Draft.Selected.RequirementRefs, selectedAfterReview)
		}
	}
	for table, query := range map[string]string{
		"batches":  `SELECT COUNT(*) FROM baseline_batch_batches WHERE project_id=?`,
		"intents":  `SELECT COUNT(*) FROM lifecycle_intents WHERE project_id=? AND json_extract(request_json,'$.operation')='start'`,
		"attempts": `SELECT COUNT(*) FROM issues WHERE project_id=? AND title LIKE 'Delivery batch%'`,
	} {
		var count int
		if err := db.DB.QueryRow(query, projectID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("start B left %d %s behind", count, table)
		}
	}

	reordered := postBaseline(t, ts, ts.adminCookie, startPath, map[string]any{
		"idempotency_key":           "delimiter-same-set-reorder",
		"review_id":                 reviewID,
		"draft_revision":            revision,
		"confirm":                   true,
		"content_digest":            draft.Baseline.ContentDigest,
		"revision_seal":             draft.Baseline.RevisionSeal,
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.c", "req.a,req.b"},
		"worker":                    map[string]any{},
	})
	if reordered.StatusCode != 200 {
		t.Fatalf("same-set reorder start=%d %s", reordered.StatusCode, baselineReadBody(reordered))
	}
	var batch baselinebatch.Batch
	decode(t, reordered, &batch)
	if len(batch.Scope.RequirementRefs) != 2 || batch.Scope.RequirementRefs[0] != "req.a,req.b" || batch.Scope.RequirementRefs[1] != "req.c" {
		t.Fatalf("canonical started scope=%v", batch.Scope.RequirementRefs)
	}
}

func TestBaselineBatchReconcileIsNotUnconditionalAdvance(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Reconcile bridge", "key": "RCB"}))
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover": json.RawMessage(handover),
	})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), map[string]any{
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
	})
	decode(t, review, &draft)
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key":           "reconcile-manual-start",
		"review_id":                 *draft.ReviewID,
		"draft_revision":            draft.Revision,
		"confirm":                   true,
		"content_digest":            draft.Baseline.ContentDigest,
		"revision_seal":             draft.Baseline.RevisionSeal,
		"execution_mode":            "manual",
		"selected_requirement_refs": []string{"req.login"},
		"worker":                    map[string]any{},
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	reconciled := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if reconciled.StatusCode != 200 {
		t.Fatalf("manual reconcile=%d %s", reconciled.StatusCode, baselineReadBody(reconciled))
	}
	decode(t, reconciled, &batch)
	if batch.Progress.Handoff != nil {
		t.Fatalf("manual reconcile created a handoff: %+v", batch.Progress.Handoff)
	}
	if batch.Progress.NextAction != baselinebatch.NextActionImplementationEvidence {
		t.Fatalf("manual reconcile next_action=%q", batch.Progress.NextAction)
	}
}

func validHandover(t *testing.T) ([]byte, []baselinebatch.Requirement) {
	t.Helper()
	reqs := []baselinebatch.Requirement{{
		Ref:                "req.login",
		Statement:          "Users sign in with email",
		AcceptanceCriteria: []string{"Magic links expire"},
		ConstraintRefs:     []string{},
	}}
	raw, _ := handoverWith(t, reqs, "", "")
	return raw, reqs
}

func handoverWith(t *testing.T, reqs []baselinebatch.Requirement, digest, seal string) ([]byte, []baselinebatch.Requirement) {
	t.Helper()
	if digest == "" {
		digest, _ = baselinebatch.ContentDigest(reqs, nil)
		seal, _ = baselinebatch.RevisionSeal("baseline:v1", 1, digest)
	}
	raw, err := json.Marshal(map[string]any{
		"handover_version": baselinebatch.HandoverVersion,
		"stream_ref":       "stream:export",
		"exported_at":      "2026-09-07T11:05:00.000Z",
		"baseline": map[string]any{
			"baseline_ref":   "baseline:v1",
			"revision":       1,
			"content_digest": digest,
			"revision_seal":  seal,
			"approved_by":    "party:forged",
			"approved_at":    "2026-09-07T11:00:00.000Z",
			"requirements":   reqs,
			"constraints":    []baselinebatch.Constraint{},
		},
		"pending_proposals": []any{},
		"decisions":         []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw, reqs
}

func postBaseline(t *testing.T, ts *testServer, cookie, path string, body any) *http.Response {
	t.Helper()
	raw, ok := body.(json.RawMessage)
	if !ok {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	req, _ := http.NewRequest(http.MethodPost, ts.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", ts.srv.URL)
	req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, cookie))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func patchBaseline(t *testing.T, ts *testServer, cookie, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPatch, ts.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", ts.srv.URL)
	req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, cookie))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func baselineReadBody(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return string(b)
}

func TestBaselineBatchExportSafeJSON(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Export boundary", "key": "EXP"}))
	optIn(t, ts, projectID)

	reqs := []baselinebatch.Requirement{{
		Ref:       "req.xss",
		Statement: `<img src=x onerror=alert(1)>`,
		AcceptanceCriteria: []string{
			"Line one\u2028line two",
			`</script><script>alert(1)</script>`,
			"\uf8ff after \U0001f642",
		},
		ConstraintRefs: []string{"con.\uf8ff"},
	}}
	cons := []baselinebatch.Constraint{{
		Ref: "con.\uf8ff", Kind: "technical", Statement: "Private-use <script>alert(1)</script>.",
	}}
	digest, err := baselinebatch.ContentDigest(reqs, cons)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := baselinebatch.RevisionSeal("baseline:v1", 1, digest)
	if err != nil {
		t.Fatal(err)
	}
	handover, err := json.Marshal(map[string]any{
		"handover_version": baselinebatch.HandoverVersion,
		"stream_ref":       "stream:export",
		"exported_at":      "2026-09-07T11:05:00.000Z",
		"baseline": map[string]any{
			"baseline_ref":   "baseline:v1",
			"revision":       1,
			"content_digest": digest,
			"revision_seal":  seal,
			"approved_by":    "party:forged",
			"approved_at":    "2026-09-07T11:00:00.000Z",
			"requirements":   reqs,
			"constraints":    cons,
		},
		"pending_proposals": []any{},
		"decisions":         []any{},
	})
	if err != nil {
		t.Fatal(err)
	}

	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover": json.RawMessage(handover),
	})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)

	export := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/export", projectID, draft.ID), ts.adminCookie)
	if export.StatusCode != 200 {
		t.Fatalf("export=%d %s", export.StatusCode, baselineReadBody(export))
	}
	if ct := export.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	if export.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}
	body := baselineReadBody(export)
	if strings.Contains(body, "<script") || strings.Contains(body, "<img") {
		t.Fatalf("export body contains unescaped HTML: %s", body)
	}

	var payload struct {
		Baseline struct {
			ContentDigest string                      `json:"content_digest"`
			RevisionSeal  string                      `json:"revision_seal"`
			Requirements  []baselinebatch.Requirement `json:"requirements"`
			Constraints   []baselinebatch.Constraint  `json:"constraints"`
		} `json:"baseline"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	roundtripDigest, err := baselinebatch.ContentDigest(payload.Baseline.Requirements, payload.Baseline.Constraints)
	if err != nil {
		t.Fatal(err)
	}
	if roundtripDigest != digest || payload.Baseline.ContentDigest != digest {
		t.Fatalf("digest roundtrip=%s stored=%s want=%s", roundtripDigest, payload.Baseline.ContentDigest, digest)
	}
	roundtripSeal, err := baselinebatch.RevisionSeal("baseline:v1", 1, roundtripDigest)
	if err != nil {
		t.Fatal(err)
	}
	if roundtripSeal != seal || payload.Baseline.RevisionSeal != seal {
		t.Fatalf("seal roundtrip=%s stored=%s want=%s", roundtripSeal, payload.Baseline.RevisionSeal, seal)
	}
}

func stageByKey(stages []baselinebatch.StageView, key string) *baselinebatch.StageView {
	for i := range stages {
		if stages[i].StageKey == key {
			return &stages[i]
		}
	}
	return nil
}
