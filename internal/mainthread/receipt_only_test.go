package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func receiptOnlySelection() *hostedconfig.Selection {
	return &hostedconfig.Selection{
		RunnerManaged: true, RunnerRunID: "receipt-reader-run", ReceiptOnly: true, RequireReceiptReader: true,
		Purpose:      agentconfig.IssueManager,
		Definition:   agentconfig.AgentDefinition{Model: "original-catalog-model", Effort: "high"},
		AppliedModel: "original-native-model", AppliedProvider: "original-provider", AppliedEffort: "high",
	}
}

type receiptOnlySession struct {
	harness.Session
	thread          harness.Thread
	resumeErr       error
	starts, resumes int
	config          harness.ThreadConfig
}

func (s *receiptOnlySession) StartThread(context.Context, harness.ThreadConfig) (harness.Thread, error) {
	s.starts++
	return nil, errors.New("receipt reader must not start a thread")
}

func (s *receiptOnlySession) ResumeThread(_ context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.resumes++
	s.config = cfg
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	if s.thread == nil || id != s.thread.ID() {
		return nil, harness.ErrRejected
	}
	return s.thread, nil
}

func TestReceiptOnlyRequiresOriginalThreadAndAppliedModel(t *testing.T) {
	for _, missing := range []string{"thread", "model"} {
		t.Run(missing, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			e.m.named = receiptOnlySelection()
			e.m.cfg.MaintenanceEnabled = true
			if missing == "model" {
				e.m.named.AppliedModel = ""
				if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), "original-thread"); err != nil {
					t.Fatal(err)
				}
			}
			session := &receiptOnlySession{}
			if _, err := e.m.openThread(t.Context(), session); err == nil {
				t.Fatal("incomplete original receipt identity was admitted")
			}
			if session.starts != 0 || session.resumes != 0 {
				t.Fatal("incomplete original identity reached native lifecycle", session.starts, session.resumes)
			}
		})
	}
}

func TestReceiptOnlyResumesOriginalWithoutToolsetHandoff(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_toolset", true: "rejected_resume"}[rejected], func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			e.m.named = receiptOnlySelection()
			e.m.cfg.MaintenanceEnabled = true
			if _, err := e.app.BindMainThread(t.Context(), e.m.bindingInstance(), "original-thread", "older-toolset", "retained-original.jsonl"); err != nil {
				t.Fatal(err)
			}
			original := &receiptThread{Thread: &historyReference{id: "original-thread", path: "retained-original.jsonl"}}
			session := &receiptOnlySession{thread: original}
			if rejected {
				session.resumeErr = harness.ErrRejected
			}
			thread, err := e.m.openThread(t.Context(), session)
			if rejected {
				if !errors.Is(err, harness.ErrRejected) {
					t.Fatal("original rejection was replaced by recovery or handoff", err)
				}
			} else if err != nil || thread != original {
				t.Fatal("receipt read did not preserve its original thread", thread, err)
			}
			if session.starts != 0 || session.resumes != 1 || len(e.h.Starts()) != 0 {
				t.Fatal("receipt attachment entered a new native execution", session.starts, session.resumes, e.h.Starts())
			}
			if session.config.Purpose != harness.ReceiptHistoryPurpose || session.config.Model != "original-native-model" || session.config.Config["model_provider"] != "original-provider" || session.config.Config["model_reasoning_effort"] != "high" {
				t.Fatal("receipt resume changed frozen model selection", session.config)
			}
			binding, found, err := e.app.MainThreadBinding(t.Context(), e.m.bindingInstance())
			if err != nil || !found || binding.ThreadID != "original-thread" || binding.ToolsetHash != "older-toolset" || e.count(`SELECT COUNT(*) FROM main_thread_handoffs`) != 0 {
				t.Fatal("receipt attachment replaced the original binding", binding, found, err)
			}
		})
	}
}

func TestReceiptOnlyRejectsFreshDeliveriesAndToolCalls(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.named = receiptOnlySelection()
	for _, op := range []string{app.OpHandleMessage, app.OpRecoverAgent, "conversation.compact", "conversation.handoff"} {
		result := e.m.Deliver(t.Context(), app.Delivery{Operation: op, Payload: []byte(`{}`)})
		if result.Outcome != app.Retry {
			t.Fatalf("receipt reader admitted %s: %+v", op, result)
		}
	}
	if result := e.m.onToolCall(t.Context(), harness.ToolCall{Tool: "repos_list", Arguments: []byte(`{}`)}); result.Success {
		t.Fatal("receipt reader executed a fresh native tool callback", result)
	}
	if result := e.m.Reconcile(t.Context(), app.Delivery{Operation: "conversation.handoff", Payload: []byte(`{}`)}); result.Outcome != app.Unknown {
		t.Fatal("receipt reader resumed native handoff", result)
	}
}

func TestReceiptOnlyClearsPendingOnlyForOriginalTerminalHistory(t *testing.T) {
	for _, status := range []string{"missing", "inProgress", "completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			message := e.say("original input")
			request := e.ids.New(protocol.PrefixRequest)
			const thread, turn = "original-thread", "original-turn"
			if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: request, MessageID: message, ThreadID: thread, TurnID: turn, Status: store.TurnStarted}); err != nil {
				t.Fatal(err)
			}
			if err := e.app.SettleOpenTurns(t.Context(), thread); err != nil {
				t.Fatal(err)
			}
			e.m.named = receiptOnlySelection()
			reader := &receiptThread{Thread: &historyReference{id: thread}, found: status != "missing", receipt: harness.TurnReceipt{ID: turn, Status: status, Raw: []byte(`{"fixture":"retained-native-history"}`)}}
			if err := e.m.reconcileNativeReceipts(t.Context(), reader); err != nil {
				t.Fatal(err)
			}
			pending, err := e.app.NativeReceiptPending(t.Context(), request)
			terminal := status == "completed" || status == "interrupted" || status == "failed"
			if err != nil || pending == terminal || reader.known != turn || len(e.h.Starts()) != 0 {
				t.Fatal("receipt pending did not reflect exact terminal native history", pending, reader.known, err)
			}
		})
	}
}

// These are synthetic confirmed Run/session facts, not provider terminal proof.
func seedClosingReceiptMetadata(t *testing.T, e *env, modelsStatus string, closing bool) {
	t.Helper()
	ctx := t.Context()
	err := e.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.InsertTask(ctx, store.TaskRow{TaskID: "task_cached", Kind: protocol.TaskKindIssueManager, Title: "Cached attachment", Project: "default", Lifecycle: protocol.TaskRunning, ControlMode: protocol.ControlManaged, SpecVersion: 1}); err != nil {
			return err
		}
		if err := tx.InsertAction(ctx, store.ActionRow{ActionID: "action_cached", TaskID: "task_cached", Kind: protocol.ActionImplement, EffectKey: "cached", TriggerJSON: `{}`, AuthorizationJSON: `{}`, TargetSpecVersion: 1, Generation: 1, Status: protocol.ActionDispatched, CreatedRequestID: "request_cached"}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, store.RunRow{RunID: "receipt-reader-run", TaskID: "task_cached", ActionID: "action_cached", Generation: 1, Role: protocol.RoleIssueManager, DesiredState: protocol.DesiredRun, ObservedState: protocol.RunUnknown}); err != nil {
			return err
		}
		if modelsStatus == "completed" {
			if _, err := tx.Exec(ctx, `UPDATE runs SET native_thread_id='original-thread',native_thread_json='{"ID":"original-thread","Model":{"Model":"original-native-model","Provider":"original-provider","Effort":"high"},"Permissions":{"Sandbox":"read-only","ApprovalPolicy":"never"}}' WHERE run_id='receipt-reader-run'`); err != nil {
				return err
			}
		}
		if closing {
			_, err := tx.Exec(ctx, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES('close_cached','receipt-reader-run','close','{}','pending',0)`)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReceiptOnlyClosingAttachmentRestoresConfirmedModel(t *testing.T) {
	for _, test := range []struct {
		name, status     string
		closing, allowed bool
	}{
		{"confirmed_closing", "completed", true, true},
		{"unconfirmed_model", "unknown", true, false},
		{"ordinary_reader", "completed", false, false},
		{"unconfirmed_resume", "completed", true, false},
		{"wrong_original_thread", "completed", true, false},
		{"changed_catalog_model", "completed", true, true},
		{"non_runner_reader", "completed", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			e.m.named = receiptOnlySelection()
			e.m.named.AppliedModel, e.m.named.AppliedProvider, e.m.named.AppliedEffort = "", "", ""
			seedClosingReceiptMetadata(t, e, test.status, test.closing)
			if test.name == "unconfirmed_resume" {
				if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE runs SET native_thread_id='',native_thread_json='' WHERE run_id='receipt-reader-run'`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "changed_catalog_model" {
				e.m.named.Definition.Model = "unavailable-model"
			}
			if test.name == "non_runner_reader" {
				e.m.named.RunnerManaged = false
			}
			originalID := "original-thread"
			if test.name == "wrong_original_thread" {
				originalID = "unrelated-thread"
			}
			if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), originalID); err != nil {
				t.Fatal(err)
			}
			original := &receiptThread{Thread: &historyReference{id: "original-thread"}}
			session := &receiptOnlySession{thread: original}
			before := e.count(`SELECT COUNT(*) FROM manager_native_requests`)
			thread, err := e.m.openThread(t.Context(), session)
			if test.allowed {
				if err != nil || thread != original || session.resumes != 1 {
					t.Fatal("confirmed closing metadata could not restore its attachment", thread, err, session.resumes)
				}
				if session.config.Model != "original-native-model" || session.config.Config["model_provider"] != "original-provider" || session.config.Config["model_reasoning_effort"] != "high" {
					t.Fatal("attachment did not preserve confirmed startup configuration", session.config)
				}
			} else if err == nil || session.resumes != 0 {
				t.Fatal("reader lacking confirmed closing metadata reached native resume", err, session.resumes)
			}
			if session.starts != 0 || e.count(`SELECT COUNT(*) FROM manager_native_requests`) != before {
				t.Fatal("metadata attachment created a fresh native request")
			}
		})
	}
}

func TestReceiptOnlyClosingRunnerTransportDrainsWithoutNativeRequest(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.named = receiptOnlySelection()
	e.m.named.AppliedModel, e.m.named.AppliedProvider, e.m.named.AppliedEffort = "", "", ""
	seedClosingReceiptMetadata(t, e, "completed", true)
	if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), "original-thread"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('receipt-reader-run',1,'{"thread_id":"original-thread","native":{"Kind":"request_usage"}}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := e.count(`SELECT COUNT(*) FROM manager_native_requests`)
	ctx, cancel := context.WithCancel(t.Context())
	session := coordination.New(ctx, &runnerTransport{app: e.app, run: "receipt-reader-run", origins: map[int64]managerEventOrigin{}})
	defer func() {
		cancel()
		<-session.Done()
	}()
	thread, err := e.m.openThread(ctx, session)
	if err != nil || thread.ID() != "original-thread" {
		t.Fatal("confirmed closing metadata did not attach the real runner transport", thread, err)
	}
	select {
	case event := <-thread.Events():
		if event.Kind != harness.EventRequestUsage {
			t.Fatal("closing attachment lost its original uploaded event", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing attachment did not drain its original uploaded event")
	}
	selected := e.m.namedSelection()
	if selected.AppliedModel != "original-native-model" || selected.AppliedProvider != "original-provider" || selected.AppliedEffort != "high" || e.count(`SELECT COUNT(*) FROM manager_native_requests`) != before {
		t.Fatal("metadata attachment changed model or queued a native RPC", selected)
	}
}

func TestReceiptOnlyClosingReaderRestoresConfirmedResumeWithoutModels(t *testing.T) {
	for _, catalog := range []string{"absent", "legacy_differs"} {
		t.Run(catalog, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			e.m.named = receiptOnlySelection()
			e.m.named.AppliedModel, e.m.named.AppliedProvider, e.m.named.AppliedEffort = "", "", ""
			seedClosingReceiptMetadata(t, e, "completed", true)
			if catalog == "legacy_differs" {
				e.m.named.Definition.Model = "different-current-catalog"
			}
			// The strict reader never issued Models. Its confirmed resume is the
			// persisted source of the actual provider configuration after teardown.
			if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE runs SET native_thread_json='{"ID":"original-thread","Model":{"Model":"retained-native-model","Provider":"retained-provider","Effort":"medium"},"Permissions":{"Sandbox":"read-only","ApprovalPolicy":"never"}}' WHERE run_id='receipt-reader-run'`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), "original-thread"); err != nil {
				t.Fatal(err)
			}
			if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('receipt-reader-run',1,'{"thread_id":"original-thread","native":{"Kind":"request_usage"}}')`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := e.count(`SELECT COUNT(*) FROM manager_native_requests`)
			ctx, cancel := context.WithCancel(t.Context())
			session := coordination.New(ctx, &runnerTransport{app: e.app, run: "receipt-reader-run", origins: map[int64]managerEventOrigin{}})
			defer func() { cancel(); <-session.Done() }()
			thread, err := e.m.openThread(ctx, session)
			if err != nil || thread.ID() != "original-thread" {
				t.Fatal("strict closing reader could not restore its confirmed resume metadata", thread, err)
			}
			select {
			case event := <-thread.Events():
				if event.Kind != harness.EventRequestUsage {
					t.Fatal("strict closing reader lost original uploaded event", event)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("strict closing reader did not drain original uploaded event")
			}
			selected := e.m.namedSelection()
			if selected.AppliedModel != "retained-native-model" || selected.AppliedProvider != "retained-provider" || selected.AppliedEffort != "medium" || e.count(`SELECT COUNT(*) FROM manager_native_requests`) != before {
				t.Fatal("strict closing reader did not retain actual confirmed resume configuration", selected)
			}
		})
	}
}

func TestReceiptOnlyReconcilesPersistedCompactCompletion(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.named = receiptOnlySelection()
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,created_at,updated_at) VALUES('receipt-compact','main','original-thread','running','manual',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	delivery := app.Delivery{Operation: "conversation.compact", Payload: json.RawMessage(`{"operation_id":"receipt-compact"}`)}
	before := len(e.h.Starts())
	if result := e.m.Reconcile(t.Context(), delivery); result.Outcome != app.Unknown {
		t.Fatal("unconfirmed compact was not retained as unknown", result)
	}
	if err := e.app.RecordRolloutEvent(t.Context(), "main", "original-thread", "original-compact-turn", "original-compact-item", string(harness.EventItemCompleted), json.RawMessage(`{"id":"original-compact-item","type":"contextCompaction"}`)); err != nil {
		t.Fatal(err)
	}
	if result := e.m.Deliver(t.Context(), delivery); result.Outcome != app.Retry {
		t.Fatal("receipt-only attachment admitted fresh native compact", result)
	}
	if result := e.m.Reconcile(t.Context(), delivery); result.Outcome != app.Delivered {
		t.Fatal("receipt-only attachment could not reconcile durable compact evidence", result)
	}
	if state, err := e.app.CompactState(t.Context(), "receipt-compact"); err != nil || state != "completed" {
		t.Fatal("original compact was not reconciled from ledger evidence", state, err)
	}
	if result := e.m.Reconcile(t.Context(), delivery); result.Outcome != app.Delivered {
		t.Fatal("completed compact reconciliation was not idempotent", result)
	}
	if len(e.h.Starts()) != before {
		t.Fatal("ledger reconciliation entered native execution")
	}
}

func TestLegacyReceiptOnlyRetainsOriginalResumeWithoutFallback(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_applied_model", true: "rejected_resume_retains_original"}[rejected], func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			e.m.named = receiptOnlySelection()
			e.m.named.RequireReceiptReader = false
			e.m.named.AppliedModel, e.m.named.AppliedProvider, e.m.named.AppliedEffort = "", "", ""
			if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), "legacy-thread"); err != nil {
				t.Fatal(err)
			}
			session := &receiptOnlySession{thread: &receiptThread{Thread: &historyReference{id: "legacy-thread"}}}
			if rejected {
				session.resumeErr = harness.ErrRejected
			}
			thread, err := e.m.openThread(t.Context(), session)
			if !rejected && (err != nil || thread != session.thread) {
				t.Fatal("legacy receipt-only resume acquired the new strict model requirement", thread, err)
			}
			if rejected && !errors.Is(err, harness.ErrRejected) {
				t.Fatal("original rejected resume was not preserved", err)
			}
			if session.resumes != 1 || session.starts != 0 {
				t.Fatal("legacy receipt-only lifecycle changed", session.resumes, session.starts, err)
			}
		})
	}
}

func TestReceiptOnlyBindingIgnoresStrictReaderFlag(t *testing.T) {
	strict := *receiptOnlySelection()
	legacy := strict
	legacy.RequireReceiptReader = false
	m := &Manager{cfg: Config{Instance: "original-manager"}, named: &strict}
	binding := m.bindingInstance()
	m.named = &legacy
	if binding != m.bindingInstance() || !sameNamedBinding(strict, legacy) {
		t.Fatal("strict reader flag replaced the original frozen named binding")
	}
}

func TestLegacyReceiptOnlyDeliversPersistedCompact(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.named = receiptOnlySelection()
	e.m.named.RequireReceiptReader = false
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,created_at,updated_at) VALUES('legacy-compact','main','legacy-thread','completed','manual',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result := e.m.Deliver(t.Context(), app.Delivery{Operation: "conversation.compact", Payload: json.RawMessage(`{"operation_id":"legacy-compact"}`)})
	if result.Outcome != app.Delivered {
		t.Fatal("legacy receipt-only delivery acquired strict native admission", result)
	}
}

func TestLegacyReceiptOnlyRetainsHandoffReconciliation(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.named = receiptOnlySelection()
	e.m.named.RequireReceiptReader = false
	if result := e.m.Reconcile(t.Context(), app.Delivery{RequestID: "legacy-handoff", Operation: "conversation.handoff", Payload: json.RawMessage(`{}`)}); result.Outcome != app.Retry {
		t.Fatal("legacy unstarted handoff no longer uses original ledger reconciliation", result)
	}
}
