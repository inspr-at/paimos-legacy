package managedharness

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/internal/testdb"
	"github.com/inspr-at/paimos/backend/models"
)

const testWorkerLease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func openManagedHarnessTestDB(t *testing.T) (int64, int64) {
	t.Helper()
	previousDataDir := os.Getenv("DATA_DIR")
	previousTestMode := os.Getenv("PAIMOS_TEST_MODE")
	t.Cleanup(func() {
		_ = paimosdb.DB.Close()
		paimosdb.DB = nil
		_ = os.Setenv("DATA_DIR", previousDataDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", previousTestMode)
	})
	_ = os.Setenv("DATA_DIR", t.TempDir())
	_ = os.Setenv("PAIMOS_TEST_MODE", "1")
	_ = os.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	testdb.Prepare(t)
	if err := paimosdb.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('harness-actor','x','member','active')`); err != nil {
		t.Fatal(err)
	}
	project, err := paimosdb.DB.Exec(`INSERT INTO projects(name,key) VALUES('Harness test','HST')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	agent, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := agent.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'sender')`, projectID); err != nil {
		t.Fatal(err)
	}
	return projectID, agentID
}

func TestManagedSteerUsesCanonicalLeaseAndCompletion(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "private-thread-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "steer observation", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongPage, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName,
		WorkerAdapter: agentmessage.AdapterManagedHarness, DeliveryLevel: "steer", TargetID: "11111111-1111-4111-8111-111111111111", Limit: 10,
	})
	if err != nil || len(wrongPage.Messages) != 0 {
		t.Fatalf("wrong target leased work: page=%#v err=%v", wrongPage, err)
	}
	page, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName,
		WorkerAdapter: agentmessage.AdapterManagedHarness, DeliveryLevel: "steer", TargetID: session.MessageTargetID, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("page=%#v", page)
	}
	work := page.Messages[0].DeliveryWork
	if work.TargetRef != "private-thread-ref" {
		t.Fatal("canonical worker did not decrypt the encrypted session ref")
	}
	if _, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName, Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "steer", TargetID: "22222222-2222-4222-8222-222222222222",
	}); err == nil {
		t.Fatal("wrong target completed leased work")
	}
	if _, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: Address(session), Agent: session.AgentName, Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "steer", TargetID: session.MessageTargetID,
	}); err != nil {
		t.Fatal(err)
	}
	var state, requested, effective, fallback, handedOff string
	if err := paimosdb.DB.QueryRow(`SELECT state,requested_level,effective_level,fallback_reason,handed_off_at FROM agent_message_deliveries WHERE delivery_id=?`, work.DeliveryID).Scan(&state, &requested, &effective, &fallback, &handedOff); err != nil {
		t.Fatal(err)
	}
	if state != "handed_off" || requested != "steer" || effective != "steer" || fallback != "" || handedOff == "" {
		t.Fatalf("state=%s requested=%s effective=%s fallback=%s handed_off_at=%s", state, requested, effective, fallback, handedOff)
	}
}

func TestInboxRegistrationUsesTransactionCurrentOrchestratorIdentity(t *testing.T) {
	projectID, workerID := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	_, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "private-thread-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
		Authority: func(ctx context.Context, tx *sql.Tx) (bool, error) {
			// Model a reassignment between middleware admission and registration.
			// Target creation and the harness row must both see this transaction's
			// current protected identity and roll back together.
			if _, err := tx.ExecContext(ctx, `UPDATE instance_orchestrator SET project_agent_id=?,display_label='Worker',revision=1,
				updated_by_user_id=(SELECT id FROM users WHERE username='harness-actor'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
				WHERE singleton_id=1`, workerID); err != nil {
				return false, err
			}
			return false, nil
		},
	})
	var messageErr *agentmessage.CodedError
	if !errors.As(err, &messageErr) || messageErr.Code != "agent_attention_target_forbidden" {
		t.Fatalf("error=%v", err)
	}
	for label, query := range map[string]string{
		"target":  `SELECT COUNT(*) FROM agent_message_targets WHERE project_id=? AND address='codex:worker'`,
		"session": `SELECT COUNT(*) FROM harness_sessions WHERE project_id=? AND agent_name='worker'`,
	} {
		var count int
		if err := paimosdb.DB.QueryRow(query, projectID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", label, count, err)
		}
	}
	var orchestrator sql.NullInt64
	if err := paimosdb.DB.QueryRow(`SELECT project_agent_id FROM instance_orchestrator WHERE singleton_id=1`).Scan(&orchestrator); err != nil || orchestrator.Valid {
		t.Fatalf("orchestrator=%#v err=%v", orchestrator, err)
	}
}

func TestInboxRegistrationRejectsTransactionRevocationWithoutWrite(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	_, _, err := NewService(paimosdb.DB).Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "private-thread-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
		Authority: func(context.Context, *sql.Tx) (bool, error) {
			return false, &agentmessage.CodedError{Code: "agent_message_unauthorized", Err: errors.New("credential revoked")}
		},
	})
	var messageErr *agentmessage.CodedError
	if !errors.As(err, &messageErr) || messageErr.Code != "agent_message_unauthorized" {
		t.Fatalf("error=%v", err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM agent_message_targets WHERE project_id=?`,
		`SELECT COUNT(*) FROM harness_sessions WHERE project_id=?`,
	} {
		var count int
		if err := paimosdb.DB.QueryRow(query, projectID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%q count=%d err=%v", query, count, err)
		}
	}
}

func TestUnavailableAgentdLeaseReroutesToActiveHarnessGeneration(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	fallback, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "ordinary-fallback-thread", MaximumLevel: "simple", Role: "simple_fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterAgentdCodex,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/paimos-agentd-ppm.sock","session_id":"11111111-1111-4111-8111-111111111111"}`,
		MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "leased before generation changed", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	failedPage, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdCodex, Limit: 10,
	})
	if err != nil || len(failedPage.Messages) != 1 || failedPage.Messages[0].DeliveryWork == nil {
		t.Fatalf("failed lease page=%#v err=%v", failedPage, err)
	}

	service := NewService(paimosdb.DB)
	generation, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp-generation-2", SessionRef: "owned-generation-2", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.HeartbeatWithActivity(context.Background(), generation.ID, PhaseWorking, ActivityEvidence{
		Sequence: 1,
		Kind:     "turn_started",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Yield(context.Background(), generation.ID); err != nil {
		t.Fatal(err)
	}
	route, err := bus.RerouteUnavailableLocalDelivery(context.Background(), agentmessage.RerouteUnavailableInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: failedPage.Messages[0].DeliveryWork.DeliveryID, FallbackReason: "idle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.Route != "active_generation" || route.HarnessSessionID != generation.ID || route.TargetID != generation.MessageTargetID {
		t.Fatalf("route=%+v generation=%+v", route, generation)
	}
	if route.TargetID == fallback.ID {
		t.Fatal("active generation incorrectly selected ordinary fallback")
	}
	page, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterManagedHarness,
		TargetID: generation.MessageTargetID, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("generation page=%#v err=%v", page, err)
	}
	if _, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: page.Messages[0].DeliveryWork.DeliveryID, EffectiveLevel: "steer", TargetID: generation.MessageTargetID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StopWithReason(context.Background(), generation.ID, ClosedProcessExited); err != nil {
		t.Fatal(err)
	}
	closedMessage, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "closed generation must not steer", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	closedLease, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdCodex, Limit: 10,
	})
	if err != nil || len(closedLease.Messages) != 1 || closedLease.Messages[0].DeliveryWork == nil {
		t.Fatalf("closed lease=%#v err=%v", closedLease, err)
	}
	closedRoute, err := bus.RerouteUnavailableLocalDelivery(context.Background(), agentmessage.RerouteUnavailableInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: closedMessage.Cursor,
		DeliveryID: closedLease.Messages[0].DeliveryWork.DeliveryID, FallbackReason: "not_steerable",
	})
	if err != nil || closedRoute.Route != "simple_fallback" || closedRoute.TargetID != fallback.ID || closedRoute.HarnessSessionID != "" {
		t.Fatalf("closed route=%+v generation=%+v err=%v", closedRoute, generation, err)
	}
}

// Delivery reads include FIFO diagnostics after the executable head. A later
// row must have no receiver reference, durable attempt, or completion authority.
func requireSingleFIFOLease(t *testing.T, bus *agentmessage.Service, projectID int64, agent, targetID string, page *agentmessage.InboxPage, cursors ...int64) *agentmessage.DeliveryWork {
	t.Helper()
	if page == nil || len(page.Messages) != len(cursors) || len(cursors) == 0 || page.NextCursor != cursors[len(cursors)-1] {
		t.Fatalf("FIFO page does not match outstanding cursors %v", cursors)
	}
	for i, message := range page.Messages {
		work := message.DeliveryWork
		if message.Cursor != cursors[i] || work == nil || work.DeliveryID == "" {
			t.Fatalf("FIFO row %d lost canonical cursor/delivery identity", i)
		}
		if i == 0 {
			if work.State != "leased" || work.TargetRef == "" {
				t.Fatal("FIFO head has no executable lease and receiver reference")
			}
			continue
		}
		if work.State != "pending" || work.FallbackReason != "fifo_blocked" || work.TargetRef != "" {
			t.Fatalf("FIFO row %d is not a blocked diagnostic without receiver authority", i)
		}
		var state, reason string
		var attempts int
		var lease sql.NullString
		if err := paimosdb.DB.QueryRow(`SELECT state,attempt_count,lease_until,last_error_code FROM agent_message_deliveries WHERE delivery_id=?`, work.DeliveryID).Scan(&state, &attempts, &lease, &reason); err != nil {
			t.Fatal(err)
		}
		if state != "pending" || attempts != 0 || lease.Valid || reason != "fifo_blocked" {
			t.Fatal("later FIFO row obtained a durable delivery attempt or lease")
		}
		_, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
			ProjectID: projectID, Address: page.Address, Agent: agent, TargetID: targetID,
			Cursor: message.Cursor, DeliveryID: work.DeliveryID, EffectiveLevel: "simple",
		})
		var coded *agentmessage.CodedError
		if !errors.As(err, &coded) || coded.Code != "agent_message_delivery_not_leased" {
			t.Fatalf("unleased FIFO row %d completion was not rejected: %v", i, err)
		}
	}
	var leased int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries d JOIN agent_messages m ON m.id=d.message_row_id JOIN project_agents a ON a.id=m.to_agent_id WHERE a.project_id=? AND m.to_address=? AND d.state='leased'`, projectID, page.Address).Scan(&leased); err != nil || leased != 1 {
		t.Fatalf("FIFO owns %d leases, want exactly one: %v", leased, err)
	}
	var cursor int64
	err := paimosdb.DB.QueryRow(`SELECT cursor FROM agent_message_cursors WHERE project_id=? AND address=?`, projectID, page.Address).Scan(&cursor)
	if (err != nil && !errors.Is(err, sql.ErrNoRows)) || cursor != page.Cursor {
		t.Fatal("diagnostic read or rejected completion advanced the durable cursor")
	}
	return page.Messages[0].DeliveryWork
}

func TestUnavailableAgentdLeaseFallsBackWithoutWedgingFIFO(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "ordinary-fallback-thread", MaximumLevel: "simple", Role: "simple_fallback",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterAgentdCodex,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/paimos-agentd-ppm.sock","session_id":"22222222-2222-4222-8222-222222222222"}`,
		MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	first, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "turn already ended", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "later ordinary message", DeliveryLevel: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdCodex, Limit: 10,
	})
	if err != nil || len(failed.Messages) < 1 || failed.Messages[0].Cursor != first.Cursor || failed.Messages[0].DeliveryWork == nil || failed.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("failed page=%#v err=%v", failed, err)
	}
	route, err := bus.RerouteUnavailableLocalDelivery(context.Background(), agentmessage.RerouteUnavailableInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: first.Cursor,
		DeliveryID: failed.Messages[0].DeliveryWork.DeliveryID, FallbackReason: "idle",
	})
	if err != nil || route.Route != "simple_fallback" {
		t.Fatalf("route=%+v err=%v", route, err)
	}
	completeFallback := func(wantCursor int64, wantReason string, waiting ...int64) {
		t.Helper()
		page, listErr := bus.ListInbox(context.Background(), agentmessage.InboxInput{
			ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterCodex, Limit: 10,
		})
		if listErr != nil {
			t.Fatal(listErr)
		}
		work := requireSingleFIFOLease(t, bus, projectID, "worker", "", page, append([]int64{wantCursor}, waiting...)...)
		completed, completeErr := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
			ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: wantCursor,
			DeliveryID: work.DeliveryID, EffectiveLevel: "simple", FallbackReason: wantReason,
		})
		if completeErr != nil || completed.Cursor != wantCursor {
			t.Fatalf("fallback completion did not advance exactly its leased cursor: %v", completeErr)
		}
	}
	completeFallback(first.Cursor, "idle", second.Cursor)
	completeFallback(second.Cursor, "")
	var rows int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries WHERE state='handed_off' AND effective_level='simple' AND handed_off_at IS NOT NULL`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("handed-off rows=%d err=%v", rows, err)
	}
}

func TestUnavailableClaudeAgentdLeaseUsesFinalDurableSimpleReroute(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "claude:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	fallback, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "claude:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "claude-ordinary-fallback", MaximumLevel: "simple", Role: "simple_fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "claude:worker", Adapter: agentmessage.AdapterAgentdClaude,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/missing-claude-agentd.sock","session_id":"85000000-0000-4000-8000-000000000850"}`,
		MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "claude:worker", Body: "owned Query unavailable", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "claude:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdClaude, Limit: 10,
	})
	if err != nil || len(failed.Messages) != 1 || failed.Messages[0].DeliveryWork == nil ||
		failed.Messages[0].DeliveryWork.Adapter != agentmessage.AdapterAgentdClaude {
		t.Fatalf("failed page=%#v err=%v", failed, err)
	}
	work := failed.Messages[0].DeliveryWork
	route, err := bus.RerouteUnavailableLocalDelivery(context.Background(), agentmessage.RerouteUnavailableInput{
		ProjectID: projectID, Address: "claude:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, FallbackReason: "transport_error",
	})
	if err != nil || route.Route != "simple_fallback" || route.TargetID != fallback.ID {
		t.Fatalf("route=%+v fallback=%+v err=%v", route, fallback, err)
	}
	page, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "claude:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterCodex, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.DeliveryID != work.DeliveryID {
		t.Fatalf("fallback page=%#v err=%v", page, err)
	}
	if _, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: "claude:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: work.DeliveryID, EffectiveLevel: "simple", FallbackReason: "transport_error",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnavailableAgentdIgnoresStaleWorkingGeneration(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	fallback, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "fresh-fallback", MaximumLevel: "simple", Role: "simple_fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterAgentdCodex,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/stale-agentd.sock","session_id":"44444444-4444-4444-8444-444444444444"}`,
		MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "stale generation must not steer", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdCodex, Limit: 10,
	})
	if err != nil || len(failed.Messages) == 0 || failed.Messages[0].DeliveryWork == nil {
		t.Fatalf("page=%#v err=%v", failed, err)
	}
	service := NewService(paimosdb.DB)
	stale, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "stale-host", SessionRef: "stale-generation", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.HeartbeatWithActivity(context.Background(), stale.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-91 seconds') WHERE id=?`, stale.ID); err != nil {
		t.Fatal(err)
	}
	route, err := bus.RerouteUnavailableLocalDelivery(context.Background(), agentmessage.RerouteUnavailableInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: failed.Messages[0].DeliveryWork.DeliveryID, FallbackReason: "idle",
	})
	if err != nil || route.Route != "simple_fallback" || route.TargetID != fallback.ID || route.HarnessSessionID != "" {
		t.Fatalf("route=%+v stale=%+v err=%v", route, stale, err)
	}
	page, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterCodex, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil {
		t.Fatalf("fallback page=%#v err=%v", page, err)
	}
	if _, err := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: page.Messages[0].DeliveryWork.DeliveryID, EffectiveLevel: "simple", FallbackReason: "idle",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSimpleAgentdWithoutFallbackBlocksHonestlyAndDoesNotRelease(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterAgentdCodex,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/simple-agentd.sock","session_id":"55555555-5555-4555-8555-555555555555"}`,
		MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "simple cannot be managed steer", DeliveryLevel: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdCodex, Limit: 10,
	})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].DeliveryWork == nil || page.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	_, err = bus.RerouteUnavailableLocalDelivery(context.Background(), agentmessage.RerouteUnavailableInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: page.Messages[0].DeliveryWork.DeliveryID, FallbackReason: "not_steerable",
	})
	var coded *agentmessage.CodedError
	if !errors.As(err, &coded) || coded.Code != "agent_message_target_missing" {
		t.Fatalf("reroute error=%v", err)
	}
	var state, fallback, lastError string
	if err := paimosdb.DB.QueryRow(`SELECT state,fallback_reason,last_error_code FROM agent_message_deliveries WHERE delivery_id=?`, page.Messages[0].DeliveryWork.DeliveryID).Scan(&state, &fallback, &lastError); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || fallback != "target_missing" || lastError != "managed_target_blocked" {
		t.Fatalf("state=%s fallback=%s last_error=%s", state, fallback, lastError)
	}
	retry, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "codex:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdCodex, Limit: 10,
	})
	if err != nil || len(retry.Messages) != 0 {
		t.Fatalf("blocked delivery was leased again: page=%#v err=%v", retry, err)
	}
}

func TestAgentdPrimaryAndManagedStandbySetupIsOrderStable(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	agentdTarget, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterAgentdCodex,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/stable-agentd.sock","session_id":"66666666-6666-4666-8666-666666666666"}`,
		MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(paimosdb.DB)
	input := RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "stable-host", SessionRef: "stable-managed-generation", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	}
	first, _, err := service.Register(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stop(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	replayedStop, err := service.Stop(context.Background(), first.ID)
	if err != nil || replayedStop.ID != first.ID || replayedStop.Phase != PhaseStopped {
		t.Fatalf("exact stopped replay=%#v err=%v", replayedStop, err)
	}
	stoppedYield, err := service.Yield(context.Background(), first.ID)
	if err != nil || stoppedYield.Session.ID != first.ID || stoppedYield.Session.Phase != PhaseStopped || len(stoppedYield.Controls) != 0 {
		t.Fatalf("stopped yield replay=%#v err=%v", stoppedYield, err)
	}
	second, _, err := service.Register(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if second.MessageTargetID != first.MessageTargetID {
		t.Fatalf("standby target rotated across generation: first=%s second=%s", first.MessageTargetID, second.MessageTargetID)
	}
	targets, err := bus.ListTargets(context.Background(), projectID, "codex:worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.ID == agentdTarget.ID && !target.Enabled {
			t.Fatal("managed generation disabled the configured agentd primary")
		}
		if target.ID == second.MessageTargetID && target.Enabled {
			t.Fatal("coexisting managed_harness target must remain standby")
		}
	}

	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'reverse-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	reverseInput := RegisterInput{
		ProjectID: projectID, AgentName: "reverse-worker", Harness: "codex", Host: "reverse-host", SessionRef: "reverse-managed-generation", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	}
	reverseManaged, _, err := service.Register(context.Background(), reverseInput)
	if err != nil {
		t.Fatal(err)
	}
	reverseAgentd, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:reverse-worker", Adapter: agentmessage.AdapterAgentdCodex,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/reverse-agentd.sock","session_id":"77777777-7777-4777-8777-777777777777"}`,
		MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Register(context.Background(), reverseInput); err != nil {
		t.Fatal(err)
	}
	reverseTargets, err := bus.ListTargets(context.Background(), projectID, "codex:reverse-worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range reverseTargets {
		if target.ID == reverseAgentd.ID && !target.Enabled {
			t.Fatal("managed-first setup disabled the later agentd primary")
		}
		if target.ID == reverseManaged.MessageTargetID && target.Enabled {
			t.Fatal("managed-first setup did not converge to a managed_harness standby")
		}
	}
}

func TestClaudeAgentdPrimaryAndManagedStandbySetupIsOrderStable(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	claudeTarget, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "claude:worker", Adapter: agentmessage.AdapterAgentdClaude,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/stable-claude-agentd.sock","session_id":"85000000-0000-4000-8000-000000000001"}`,
		MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(paimosdb.DB)
	input := RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "claude", Host: "stable-claude-host", SessionRef: "stable-claude-generation", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	}
	generation, _, err := service.Register(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := bus.ListTargets(context.Background(), projectID, "claude:worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.ID == claudeTarget.ID && !target.Enabled {
			t.Fatal("Claude managed generation disabled the configured agentd_claude primary")
		}
		if target.ID == generation.MessageTargetID && target.Enabled {
			t.Fatal("coexisting Claude managed_harness target must remain standby")
		}
	}

	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'reverse-claude-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	reverseInput := RegisterInput{
		ProjectID: projectID, AgentName: "reverse-claude-worker", Harness: "claude", Host: "reverse-claude-host", SessionRef: "reverse-claude-generation", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	}
	reverseGeneration, _, err := service.Register(context.Background(), reverseInput)
	if err != nil {
		t.Fatal(err)
	}
	reverseClaude, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "claude:reverse-claude-worker", Adapter: agentmessage.AdapterAgentdClaude,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/reverse-claude-agentd.sock","session_id":"85000000-0000-4000-8000-000000000002"}`,
		MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	reverseTargets, err := bus.ListTargets(context.Background(), projectID, "claude:reverse-claude-worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range reverseTargets {
		if target.ID == reverseClaude.ID && !target.Enabled {
			t.Fatal("Claude managed-first setup disabled the later agentd_claude primary")
		}
		if target.ID == reverseGeneration.MessageTargetID && target.Enabled {
			t.Fatal("Claude managed-first setup did not converge to a managed_harness standby")
		}
	}
}

func TestUnavailableClaudeAgentdLeaseReroutesToFreshActiveGeneration(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "claude:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "claude:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "claude-active-fallback", MaximumLevel: "simple", Role: "simple_fallback",
	}); err != nil {
		t.Fatal(err)
	}
	claudeTarget, err := bus.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "claude:worker", Adapter: agentmessage.AdapterAgentdClaude,
		TargetKind:   agentmessage.TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/active-claude-agentd.sock","session_id":"85000000-0000-4000-8000-000000000003"}`,
		MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "claude:worker", Body: "reroute to live Claude generation", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	failedPage, err := bus.ListInbox(context.Background(), agentmessage.InboxInput{
		ProjectID: projectID, Address: "claude:worker", Agent: "worker", WorkerAdapter: agentmessage.AdapterAgentdClaude, Limit: 10,
	})
	if err != nil || len(failedPage.Messages) != 1 || failedPage.Messages[0].DeliveryWork == nil {
		t.Fatalf("failed lease page=%#v err=%v", failedPage, err)
	}
	service := NewService(paimosdb.DB)
	generation, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "claude", Host: "fresh-claude-host", SessionRef: "fresh-claude-generation", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.HeartbeatWithActivity(context.Background(), generation.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_completed"}); err != nil {
		t.Fatal(err)
	}
	route, err := bus.RerouteUnavailableLocalDelivery(context.Background(), agentmessage.RerouteUnavailableInput{
		ProjectID: projectID, Address: "claude:worker", Agent: "worker", Cursor: message.Cursor,
		DeliveryID: failedPage.Messages[0].DeliveryWork.DeliveryID, FallbackReason: "idle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.Route != "active_generation" || route.HarnessSessionID != generation.ID || route.TargetID != generation.MessageTargetID {
		t.Fatalf("route=%+v generation=%+v", route, generation)
	}
	targets, err := bus.ListTargets(context.Background(), projectID, "claude:worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.ID == claudeTarget.ID && !target.Enabled {
			t.Fatal("fresh Claude generation disabled the agentd_claude primary")
		}
		if target.ID == generation.MessageTargetID && target.Enabled {
			t.Fatal("fresh Claude generation target must remain standby until durable reroute")
		}
	}
}

func TestRegisterEnforcesManagedAndExternalSteerTruth(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	base := RegisterInput{ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0",
		SessionRef: "thread-1", WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker,
		SteerMode: SteerOwned, Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true}}
	session, created, err := service.Register(context.Background(), base)
	if err != nil || !created {
		t.Fatalf("register: session=%#v created=%v err=%v", session, created, err)
	}
	if session.AgentName != "worker" || session.ManagementMode != ManagementManaged || !session.Capabilities.Steer {
		t.Fatalf("session=%#v", session)
	}
	if _, created, err := service.Register(context.Background(), base); err != nil || created {
		t.Fatalf("exact registration replay: created=%v err=%v", created, err)
	}
	encoded, err := json.Marshal(session)
	if err != nil || strings.Contains(string(encoded), "thread-1") || strings.Contains(string(encoded), "session_ref") {
		t.Fatalf("private session reference escaped response: %s err=%v", encoded, err)
	}
	var digest, workerDigest []byte
	if err := paimosdb.DB.QueryRow(`SELECT session_ref_digest,worker_lease_digest FROM harness_sessions WHERE id=?`, session.ID).Scan(&digest, &workerDigest); err != nil || len(digest) != 32 || len(workerDigest) != 32 {
		t.Fatalf("digest lengths=(%d,%d) err=%v", len(digest), len(workerDigest), err)
	}
	if strings.Contains(string(workerDigest), testWorkerLease) || strings.Contains(string(encoded), testWorkerLease) {
		t.Fatal("raw worker lease escaped digest-only storage or public response")
	}
	if ok, err := service.VerifyWorkerLease(context.Background(), projectID, session.ID, testWorkerLease); err != nil || !ok {
		t.Fatalf("exact worker lease rejected: ok=%v err=%v", ok, err)
	}
	if ok, err := service.VerifyWorkerLease(context.Background(), projectID, session.ID, "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"); err != nil || ok {
		t.Fatalf("wrong worker lease accepted: ok=%v err=%v", ok, err)
	}
	if _, err := service.Stop(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}

	unmanagedCodex := base
	unmanagedCodex.Host, unmanagedCodex.SessionRef = "mbp1", "thread-2"
	unmanagedCodex.ManagementMode, unmanagedCodex.SteerMode = ManagementUnmanaged, SteerCodexExternal
	unmanagedCodex.Capabilities.Interrupt, unmanagedCodex.Capabilities.Stop = false, false
	target, err := agentmessage.NewService(paimosdb.DB).RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "thread-2", MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	unmanagedCodex.MessageTargetID = target.ID
	unmanagedSession, _, err := service.Register(context.Background(), unmanagedCodex)
	if err != nil {
		t.Fatalf("documented Codex external steer rejected: %v", err)
	}
	if _, err := service.Stop(context.Background(), unmanagedSession.ID); err != nil {
		t.Fatal(err)
	}
	simpleTarget, err := agentmessage.NewService(paimosdb.DB).RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "thread-simple", MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	capped := unmanagedCodex
	capped.Host, capped.SessionRef, capped.MessageTargetID = "mbp-cap", "thread-simple", simpleTarget.ID
	if _, _, err := service.Register(context.Background(), capped); !IsCode(err, CodeCapabilityInvalid) {
		t.Fatalf("unmanaged steer exceeded target MaximumLevel: %v", err)
	}

	unmanagedClaude := unmanagedCodex
	unmanagedClaude.Harness, unmanagedClaude.Host, unmanagedClaude.SessionRef = "claude", "mbp2", "11111111-1111-4111-8111-111111111111"
	if _, _, err := service.Register(context.Background(), unmanagedClaude); !IsCode(err, CodeCapabilityInvalid) {
		t.Fatalf("unmanaged Claude steer error=%v", err)
	}

	privateSocket := base
	privateSocket.Host, privateSocket.SessionRef = "mbp3", "/tmp/private.sock"
	if _, _, err := service.Register(context.Background(), privateSocket); !IsCode(err, CodeInvalid) {
		t.Fatalf("private socket error=%v", err)
	}
}

func TestRegisterPersistsExactDispatchAndRejectsExclusiveWorkspaceReuse(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker2')`, projectID); err != nil {
		t.Fatal(err)
	}
	service := NewService(paimosdb.DB)
	workspace := &models.HarnessWorkspaceProvenance{
		CanonicalPath: "/workspace/paimos-worktrees/pai-906", GitTopLevel: "/workspace/paimos-worktrees/pai-906",
		GitBranch: "feat/pai-906-dispatch-profiles", Identity: strings.Repeat("a", 64), Kind: "git_worktree", Mode: "exclusive",
	}
	input := RegisterInput{ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "profile-session", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
		Workspace:    workspace, DispatchProfileID: "codex-sol-high", DispatchProfileVersion: "2", AccountLabel: "chatgpt", AccountKey: "coordinator"}
	session, created, err := service.Register(context.Background(), input)
	if err != nil || !created {
		t.Fatalf("register = %+v created=%v err=%v", session, created, err)
	}
	if session.MachineID != "mbp0" || session.Workspace == nil || *session.Workspace != *workspace || session.DispatchProfile == nil ||
		session.DispatchProfile.ID != "codex-sol-high" || session.DispatchProfile.Model != "gpt-6-sol" || session.AccountLabel != "chatgpt" || session.AccountKey != "coordinator" {
		t.Fatalf("stored provenance = %+v", session)
	}
	replay, created, err := service.Register(context.Background(), input)
	if err != nil || created || replay.ID != session.ID || replay.AccountKey != "coordinator" {
		t.Fatalf("replay = %+v created=%v err=%v", replay, created, err)
	}
	conflict := input
	conflict.AgentName, conflict.SessionRef = "worker2", "profile-session-2"
	if _, _, err := service.Register(context.Background(), conflict); !IsCode(err, CodeConflict) || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("workspace conflict = %v", err)
	}
	badProfile := input
	badProfile.SessionRef, badProfile.DispatchProfileVersion = "profile-session-3", "latest"
	if _, _, err := service.Register(context.Background(), badProfile); !IsCode(err, CodeInvalid) {
		t.Fatalf("unpinned profile error = %v", err)
	}
	pathKey := input
	pathKey.SessionRef, pathKey.AccountKey = "profile-session-4", "/tmp/codex"
	if _, _, err := service.Register(context.Background(), pathKey); !IsCode(err, CodeInvalid) {
		t.Fatalf("path account key error = %v", err)
	}
	classKey := input
	classKey.SessionRef, classKey.AccountKey = "profile-session-5", "chatgpt"
	if _, _, err := service.Register(context.Background(), classKey); !IsCode(err, CodeInvalid) {
		t.Fatalf("class label account key error = %v", err)
	}
}

func TestStoredRetiredDispatchProfileRemainsReadable(t *testing.T) {
	profile, err := storedDispatchProfile("retired-profile", "2026-08", "codex", "retired-model", "high", "exclusive")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "retired-profile" || profile.Version != "2026-08" || profile.Model != "retired-model" {
		t.Fatalf("retired profile snapshot = %+v", profile)
	}
}

func TestStoppedSessionCanRegisterNewActiveGeneration(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	input := RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "stable-thread-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	}
	first, created, err := service.Register(context.Background(), input)
	if err != nil || !created {
		t.Fatalf("first register: created=%v err=%v", created, err)
	}
	if _, err := service.Heartbeat(context.Background(), first.ID, PhaseWorking); err != nil {
		t.Fatal(err)
	}
	bus := agentmessage.NewService(paimosdb.DB)
	if err := bus.AllowSender(context.Background(), projectID, "codex:worker", "paimos:sender"); err != nil {
		t.Fatal(err)
	}
	simpleMessage, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "queued before restart", DeliveryLevel: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stop(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	second, created, err := service.Register(context.Background(), input)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("replacement register: first=%s second=%s created=%v err=%v", first.ID, second.ID, created, err)
	}
	if second.MessageTargetID != first.MessageTargetID {
		t.Fatalf("replacement rotated stable encrypted binding: first=%s second=%s", first.MessageTargetID, second.MessageTargetID)
	}
	old, err := service.GetByID(context.Background(), first.ID)
	if err != nil || old.Phase != PhaseStopped {
		t.Fatalf("old session=%#v err=%v", old, err)
	}
	if _, err := service.Heartbeat(context.Background(), second.ID, PhaseWorking); err != nil {
		t.Fatalf("replacement heartbeat: %v", err)
	}
	yielded, err := service.Yield(context.Background(), second.ID)
	if err != nil || yielded.Session.ID != second.ID || yielded.Session.Phase != PhaseYielded || yielded.Session.YieldSequence != 1 {
		t.Fatalf("replacement yield=%#v err=%v", yielded, err)
	}
	steerMessage, err := bus.SendEnvelope(context.Background(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:worker", Body: "queued after restart", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	drain := func(wantCursor int64, wantLevel string, waiting ...int64) {
		t.Helper()
		page, listErr := bus.ListInbox(context.Background(), agentmessage.InboxInput{
			ProjectID: projectID, Address: Address(second), Agent: second.AgentName,
			WorkerAdapter: agentmessage.AdapterManagedHarness, TargetID: second.MessageTargetID, Limit: 100,
		})
		if listErr != nil {
			t.Fatal(listErr)
		}
		work := requireSingleFIFOLease(t, bus, projectID, second.AgentName, second.MessageTargetID, page, append([]int64{wantCursor}, waiting...)...)
		if work.RequestedLevel != wantLevel || work.State != "leased" || work.TargetRef != input.SessionRef {
			t.Fatalf("drain work=%#v want level=%s", work, wantLevel)
		}
		completed, completeErr := bus.CompleteLocalDelivery(context.Background(), agentmessage.CompleteDeliveryInput{
			ProjectID: projectID, Address: Address(second), Agent: second.AgentName, Cursor: wantCursor,
			DeliveryID: work.DeliveryID, EffectiveLevel: wantLevel, TargetID: second.MessageTargetID,
		})
		if completeErr != nil || completed.Cursor != wantCursor {
			t.Fatalf("complete cursor=%d level=%s did not advance exactly its lease: %v", wantCursor, wantLevel, completeErr)
		}
	}
	drain(simpleMessage.Cursor, "simple", steerMessage.Cursor)
	drain(steerMessage.Cursor, "steer")
	var handedOff int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_deliveries WHERE state='handed_off' AND handed_off_at IS NOT NULL`).Scan(&handedOff); err != nil || handedOff != 2 {
		t.Fatalf("canonical delivery completions=%d err=%v", handedOff, err)
	}

	const replays = 8
	var wg sync.WaitGroup
	results := make(chan models.HarnessSession, replays)
	errors := make(chan error, replays)
	createdResults := make(chan bool, replays)
	for range replays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, replayCreated, replayErr := service.Register(context.Background(), input)
			results <- session
			createdResults <- replayCreated
			errors <- replayErr
		}()
	}
	wg.Wait()
	close(results)
	close(createdResults)
	close(errors)
	for replayErr := range errors {
		if replayErr != nil {
			t.Fatalf("active replay: %v", replayErr)
		}
	}
	for replayCreated := range createdResults {
		if replayCreated {
			t.Fatal("active replay created another generation")
		}
	}
	for replay := range results {
		if replay.ID != second.ID {
			t.Fatalf("active replay id=%s want %s", replay.ID, second.ID)
		}
	}
	var total, active int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*),SUM(phase<>'stopped') FROM harness_sessions WHERE project_id=? AND harness=? AND host=?`, projectID, input.Harness, input.Host).Scan(&total, &active); err != nil {
		t.Fatal(err)
	}
	if total != 2 || active != 1 {
		t.Fatalf("history total=%d active=%d", total, active)
	}
}

func TestConcurrentInitialRegistrationReplayCreatesOneActiveRow(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	input := RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "concurrent-thread-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	}
	type result struct {
		session models.HarnessSession
		created bool
		err     error
	}
	const attempts = 8
	start := make(chan struct{})
	results := make(chan result, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			session, created, err := service.Register(context.Background(), input)
			results <- result{session: session, created: created, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var id string
	createdCount := 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("concurrent replay: %v", got.err)
		}
		if got.created {
			createdCount++
		}
		if id == "" {
			id = got.session.ID
		}
		if got.session.ID != id {
			t.Fatalf("concurrent replay IDs: got=%s want=%s", got.session.ID, id)
		}
		if got.session.MessageTargetID == "" {
			t.Fatal("concurrent replay returned before the encrypted target binding was durable")
		}
	}
	if createdCount != 1 {
		t.Fatalf("created results=%d want 1", createdCount)
	}
	var active int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_sessions WHERE project_id=? AND phase<>'stopped'`, projectID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active rows=%d err=%v", active, err)
	}
}

func TestRegisterRecoversAfterTargetCommitBeforeSessionInsert(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	input := RegisterInput{ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "crash-host", SessionRef: "crash-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true}}
	// This is the only durable partial state the pre-insert architecture can
	// leave: the encrypted target committed, but no active session row exists.
	prepared, err := agentmessage.NewService(paimosdb.DB).RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterManagedHarness,
		TargetKind: agentmessage.TargetKindHarnessSession, TargetRef: input.SessionRef, MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(paimosdb.DB)
	session, created, err := service.Register(context.Background(), input)
	if err != nil || !created || session.MessageTargetID != prepared.ID {
		t.Fatalf("session=%+v created=%v err=%v", session, created, err)
	}
	var targets int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_targets WHERE project_id=? AND address='codex:worker' AND adapter='managed_harness'`, projectID).Scan(&targets); err != nil || targets != 1 {
		t.Fatalf("target count=%d err=%v", targets, err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET message_target_id=NULL WHERE id=?`, session.ID); err == nil {
		t.Fatal("database allowed an advertised inbox to lose its target")
	}
	wrong := input
	wrong.WorkerLease = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
	wrong.Capabilities.Stop = true
	if _, _, err := service.Register(context.Background(), wrong); !IsCode(err, CodeInvalid) {
		t.Fatalf("wrong-lease replay disclosed registration mismatch: %v", err)
	}
}

func TestYieldClaimsOwnedInterruptAndStopRequests(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "thread-1", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	interrupt, err := service.RequestControl(context.Background(), session.ID, ControlInterrupt, 1)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := service.RequestControl(context.Background(), session.ID, ControlStop, 1)
	if err != nil {
		t.Fatal(err)
	}
	yielded, err := service.Yield(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if yielded.Session.YieldSequence != 1 || yielded.Session.Phase != PhaseYielded || len(yielded.Controls) != 2 {
		t.Fatalf("yield=%#v", yielded)
	}
	if yielded.Controls[0].ID != interrupt.ID || yielded.Controls[1].ID != stop.ID ||
		yielded.Controls[0].State != ControlClaimed || yielded.Controls[1].State != ControlClaimed {
		t.Fatalf("controls=%#v", yielded.Controls)
	}
	completed, err := service.CompleteControl(context.Background(), session.ID, interrupt.ID, ControlApplied, ReasonApplied)
	if err != nil || completed.State != ControlApplied {
		t.Fatalf("complete=%#v err=%v", completed, err)
	}
	replayed, err := service.CompleteControl(context.Background(), session.ID, interrupt.ID, ControlApplied, ReasonApplied)
	if err != nil || replayed.ID != completed.ID || replayed.CompletedAt != completed.CompletedAt {
		t.Fatalf("exact completion replay=%#v err=%v", replayed, err)
	}
	if _, err := service.CompleteControl(context.Background(), session.ID, interrupt.ID, ControlRejected, ReasonFailed); !IsCode(err, CodeConflict) {
		t.Fatalf("mismatched completion replay error=%v", err)
	}
	if _, err := service.Stop(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	var stoppedState, stoppedReason string
	if err := paimosdb.DB.QueryRow(`SELECT state,reason FROM harness_session_controls WHERE id=?`, stop.ID).Scan(&stoppedState, &stoppedReason); err != nil || stoppedState != ControlRejected || stoppedReason != ReasonOwnershipLost {
		t.Fatalf("control on stopped generation state=%q reason=%q err=%v", stoppedState, stoppedReason, err)
	}
}

func TestGetControlReturnsExactScopedOutcomeWithoutWorkerSecrets(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "outcome-host", SessionRef: "private-outcome-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := service.RequestControl(context.Background(), session.ID, ControlInterrupt, 1)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := service.GetControl(context.Background(), projectID, session.ID, control.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.ID != control.ID || pending.ProjectID != projectID || pending.HarnessSessionID != session.ID ||
		pending.CorrelationID != control.ID || pending.Kind != ControlInterrupt || pending.State != ControlPending ||
		pending.Outcome != "" || pending.Reason != "" || pending.RequestedAt == "" || pending.ClaimedAt != "" || pending.CompletedAt != "" {
		t.Fatalf("pending outcome=%+v", pending)
	}
	if _, err := service.Yield(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteControl(context.Background(), session.ID, control.ID, ControlRejected, ReasonFailed); err != nil {
		t.Fatal(err)
	}
	completed, err := service.GetControl(context.Background(), projectID, session.ID, control.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != ControlRejected || completed.Outcome != ControlRejected || completed.Reason != ReasonFailed ||
		completed.ClaimedAt == "" || completed.CompletedAt == "" {
		t.Fatalf("completed outcome=%+v", completed)
	}
	if _, err := service.GetControl(context.Background(), projectID+1, session.ID, control.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("wrong-project lookup error=%v", err)
	}
	if _, err := service.GetControl(context.Background(), projectID, uuid.NewString(), control.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("wrong-session lookup error=%v", err)
	}
	if _, err := service.GetControl(context.Background(), projectID, session.ID, uuid.NewString()); !IsCode(err, CodeNotFound) {
		t.Fatalf("wrong-control lookup error=%v", err)
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-outcome-ref", testWorkerLease, "message_target_id", "requested_by_user_id"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("operator outcome leaked %q: %s", secret, raw)
		}
	}
}

func TestUnmanagedSessionCannotRequestOwnedControl(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	target, err := agentmessage.NewService(paimosdb.DB).RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:worker", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "thread-1", MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "mbp0", SessionRef: "thread-1", WorkerLease: testWorkerLease, MessageTargetID: target.ID,
		ManagementMode: ManagementUnmanaged, Role: RoleCoordinator, SteerMode: SteerCodexExternal,
		Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestControl(context.Background(), session.ID, ControlInterrupt, 1); !IsCode(err, CodeCapabilityUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestStopRacesControlRequestWithoutStrandingControl(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	for iteration := 0; iteration < 20; iteration++ {
		session, _, err := service.Register(context.Background(), RegisterInput{
			ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "race-host", SessionRef: fmt.Sprintf("race-ref-%d", iteration), WorkerLease: testWorkerLease,
			ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerNone,
			Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errorsOut := make(chan error, 2)
		go func() {
			<-start
			_, requestErr := service.RequestControl(context.Background(), session.ID, ControlInterrupt, 1)
			errorsOut <- requestErr
		}()
		go func() {
			<-start
			_, stopErr := service.Stop(context.Background(), session.ID)
			errorsOut <- stopErr
		}()
		close(start)
		for range 2 {
			if raceErr := <-errorsOut; raceErr != nil && !IsCode(raceErr, CodeNotFound) {
				t.Fatalf("iteration=%d race error=%v", iteration, raceErr)
			}
		}
		var nonterminal int
		if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_controls WHERE harness_session_id=? AND state IN ('pending','claimed')`, session.ID).Scan(&nonterminal); err != nil || nonterminal != 0 {
			t.Fatalf("iteration=%d nonterminal=%d err=%v", iteration, nonterminal, err)
		}
	}
}
