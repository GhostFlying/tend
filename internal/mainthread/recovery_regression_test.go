package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type recoveryHookSession struct {
	harness.Session
	hook func()
}

func (s recoveryHookSession) StartThread(ctx context.Context, c harness.ThreadConfig) (harness.Thread, error) {
	th, err := s.Session.StartThread(ctx, c)
	if err != nil {
		return nil, err
	}
	return &recoveryHookThread{th, s.hook}, nil
}
func (s recoveryHookSession) ResumeThread(ctx context.Context, id string, c harness.ThreadConfig) (harness.Thread, error) {
	th, err := s.Session.ResumeThread(ctx, id, c)
	if err != nil {
		return nil, err
	}
	return &recoveryHookThread{th, s.hook}, nil
}

type recoveryHookThread struct {
	harness.Thread
	hook func()
}

func (th recoveryHookThread) FindTurn(ctx context.Context, i, k string) (harness.TurnReceipt, bool, error) {
	return th.Thread.(harness.TurnReader).FindTurn(ctx, i, k)
}
func (th recoveryHookThread) InjectDeveloper(ctx context.Context, texts ...string) error {
	th.hook()
	return th.Thread.InjectDeveloper(ctx, texts...)
}

func TestRecoveryStopDuringContextInjectionPreventsStart(t *testing.T) {
	h := testkit.NewHarness()
	var continued, stopped atomic.Int32
	var e *env
	h.Turn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		if in.Continue {
			continued.Add(1)
			return "completed"
		}
		if err := e.app.InvalidateBaseline(t.Context(), e.m.threadKey(th.ID())); err != nil {
			t.Error(err)
		}
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: id, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded"}})
		return "failed"
	}
	e = newEnv(t, h, func(c *Config) {
		c.Open = func(ctx context.Context) (harness.Session, error) {
			s, err := h.Open(ctx)
			return recoveryHookSession{s, func() {
				if e == nil {
					return
				}
				e.m.mu.Lock()
				a := e.m.active
				isRecovery := a != nil && a.recovery
				message := ""
				if isRecovery {
					message = a.messageID
				}
				e.m.mu.Unlock()
				if !isRecovery || !stopped.CompareAndSwap(0, 1) {
					return
				}
				e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: message}, protocol.InterruptReplyPayload{MessageID: message}, app.InterruptReply{})
			}}, err
		}
	})
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 1})(e.app)
	e.say("stop this before continuation starts")
	eventually(t, "context injection hook stops continuation", func() bool { return stopped.Load() == 1 })
	eventually(t, "recovery handler finishes", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE operation='conversation.recover_agent' AND status='delivered'`) == 1
	})
	if n := e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id IN (SELECT request_id FROM agent_recovery_attempts) AND status='starting'`); n != 0 {
		t.Fatalf("retained %d unsent startup intents", n)
	}
	if n := continued.Load(); n != 0 {
		t.Fatalf("native continuation started %d times despite cancellation committed before turn/start", n)
	}
}

func TestRecoveryReconnectRetainsPriorSteerSources(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 1})(e.app)
	original := e.say("original owner input")
	d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("delivery %t %v", ok, err)
	}
	s, err := e.h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base, err := s.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	th := &receiptThread{Thread: base, found: true}
	if err := e.app.RegisterAgentThread(t.Context(), "main", th.ID()); err != nil {
		t.Fatal(err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: original, ThreadID: th.ID(), TurnID: "original-native", Status: store.TurnStarted}
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	supplement := e.say("important supplemental constraint")
	sd, ok, err := e.app.ClaimMainSteer(t.Context(), d.RequestID, th.ID(), rec.TurnID)
	if err != nil || !ok {
		t.Fatalf("claim %t %v", ok, err)
	}
	steer, _, err := e.app.MainSteer(t.Context(), sd.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	steer.InputText = "supplement-native"
	steer.Status = "accepted"
	if err := e.app.UpdateMainSteer(t.Context(), steer); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.app.ConsumeMainSteer(t.Context(), th.ID(), rec.TurnID, steer.InputText, "supplement-item"); err != nil || !ok {
		t.Fatalf("consume %t %v", ok, err)
	}
	if err := e.app.FinishDelivery(t.Context(), sd, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnFailed
	rec.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := e.app.FinishDelivery(t.Context(), d, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	rd, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("recovery %t %v", ok, err)
	}
	if yes, err := e.app.AdmitAgentRecovery(t.Context(), rd.RequestID, th.ID()); err != nil || !yes {
		t.Fatalf("admit %t %v", yes, err)
	}
	rec.RequestID = rd.RequestID
	rec.TurnID = ""
	rec.Status = store.TurnStarting
	rec.Failure = nil
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.TurnID = "continued-native"
	rec.Status = store.TurnStarted
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := e.app.SettleOpenTurns(t.Context(), th.ID()); err != nil {
		t.Fatal(err)
	}
	th.receipt = harness.TurnReceipt{ID: rec.TurnID, Status: "completed", Raw: []byte(`{"status":"completed"}`), Items: []harness.Event{{ItemType: harness.ItemAgentMessage, ItemID: "continued-answer", Text: "answer respecting the latest constraint"}}}
	if err := e.m.reconcileNativeReceipts(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM conversation_messages m JOIN conversation_agent_messages a ON a.message_id=m.message_id WHERE a.item_id='continued-answer' AND m.reply_to=?`, supplement); n != 1 {
		t.Fatalf("recovered continuation answer lost latest owner source; count=%d", n)
	}
}

func setupRetainedRecovery(t *testing.T, withSupplement bool) (*env, harness.Thread, store.MainTurn, app.Delivery, string) {
	t.Helper()
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 1})(e.app)
	original := e.say("original question")
	d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	s, err := e.h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: original, ThreadID: th.ID(), TurnID: "original-native", Status: store.TurnStarted}
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	supplement := ""
	if withSupplement {
		supplement = e.say("actual owner answer after the initial question")
		sd, ok, err := e.app.ClaimMainSteer(t.Context(), d.RequestID, th.ID(), rec.TurnID)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		steer, _, err := e.app.MainSteer(t.Context(), sd.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		steer.InputText = "supplement-input"
		steer.Status = "accepted"
		if err := e.app.UpdateMainSteer(t.Context(), steer); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := e.app.ConsumeMainSteer(t.Context(), th.ID(), rec.TurnID, steer.InputText, "supplement-native"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if err := e.app.FinishDelivery(t.Context(), sd, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
			t.Fatal(err)
		}
	}
	rec.Status = store.TurnFailed
	rec.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := e.app.FinishDelivery(t.Context(), d, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	rd, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if yes, err := e.app.AdmitAgentRecovery(t.Context(), rd.RequestID, th.ID(), 0); err != nil || !yes {
		t.Fatal(yes, err)
	}
	rec.RequestID = rd.RequestID
	rec.TurnID = ""
	rec.Status = store.TurnStarting
	rec.Failure = nil
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	return e, th, rec, rd, supplement
}
func TestRecoveryAbandonedStartProjectsUnknown(t *testing.T) {
	e, th, rec, d, _ := setupRetainedRecovery(t, false)
	if err := e.app.SettleOpenTurns(t.Context(), th.ID()); err != nil {
		t.Fatal(err)
	}
	result := e.m.Reconcile(t.Context(), d)
	if result.Outcome != app.Unknown {
		t.Fatalf("result=%+v", result)
	}
	if err := e.app.FinishDelivery(t.Context(), d, result); err != nil {
		t.Fatal(err)
	}
	r, _, err := e.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "unknown" {
		t.Fatalf("abandoned empty-input start projects %q instead of unknown", r.Status)
	}
}
func TestRecoveryToolDefaultsToLatestRetainedOwner(t *testing.T) {
	e, th, rec, _, supplement := setupRetainedRecovery(t, true)
	rec.Status = store.TurnStarted
	rec.TurnID = "continuation-native"
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	focus, sources, seq, err := e.m.retainedRecoverySources(t.Context(), rec.RequestID, rec.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	e.m.mu.Lock()
	e.m.active = &turn{requestID: rec.RequestID, messageID: rec.MessageID, turnID: rec.TurnID, sourceMessageID: focus, sourceSeq: seq, sources: sources, recovery: true}
	e.m.mu.Unlock()
	callID := "new-tool"
	e.m.onToolCall(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: rec.TurnID, CallID: callID, Tool: "conversation_read", Arguments: []byte(`{}`)})
	source, found, err := e.app.MainToolSource(t.Context(), th.ID()+"/"+rec.TurnID+"/"+callID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if source != supplement {
		t.Fatalf("tool source=%s; expected latest retained owner=%s (initial=%s)", source, supplement, rec.MessageID)
	}
}

func TestRecoverySettlesRetainedSupplement(t *testing.T) {
	e, th, rec, _, supp := setupRetainedRecovery(t, true)
	source, err := e.app.RecoverySourceRequest(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := e.app.MainTurn(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.app.SettleMainSteers(t.Context(), th.ID(), old.TurnID, store.TurnFailed); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnStarted
	rec.TurnID = "continued"
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE message_dispositions SET disposition='unassigned',classified_revision=content_revision,facts_revision=content_revision WHERE message_id IN (SELECT lm.message_id FROM logical_turn_messages lm JOIN logical_turn_executions e ON e.logical_turn_id=lm.logical_turn_id WHERE e.request_id=?)`, rec.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.SettleMainSteers(t.Context(), th.ID(), rec.TurnID, store.TurnCompleted); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnCompleted
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	v, err := e.app.LogicalTurn(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		item, err := store.GetConversationItem(t.Context(), tx.Q(), supp)
		if err != nil {
			return err
		}
		t.Logf("logical=%s retained supplement state=%s", v.Status, item.State)
		if v.Status == "completed" && item.State != "processed" {
			t.Fatalf("continued responsibility completed but consumed supplement is %s", item.State)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type recoveryReaderSession struct {
	harness.Session
	reads *atomic.Int32
}

func (s recoveryReaderSession) StartThread(ctx context.Context, c harness.ThreadConfig) (harness.Thread, error) {
	th, e := s.Session.StartThread(ctx, c)
	return recoveryReaderThread{th, s.reads}, e
}
func (s recoveryReaderSession) ResumeThread(ctx context.Context, id string, c harness.ThreadConfig) (harness.Thread, error) {
	th, e := s.Session.ResumeThread(ctx, id, c)
	return recoveryReaderThread{th, s.reads}, e
}

type recoveryReaderThread struct {
	harness.Thread
	reads *atomic.Int32
}

func (th recoveryReaderThread) FindTurn(ctx context.Context, i, k string) (harness.TurnReceipt, bool, error) {
	if th.reads.Add(1) == 1 {
		return harness.TurnReceipt{}, false, errors.New("transient thread/read error")
	}
	return th.Thread.(harness.TurnReader).FindTurn(ctx, i, k)
}
func TestRecoveryPrestartReadFailureIsRetriedWithoutInput(t *testing.T) {
	h := testkit.NewHarness()
	var reads atomic.Int32
	h.Turn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		if in.Continue {
			return "completed"
		}
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: id, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded"}})
		return "failed"
	}
	e := newEnv(t, h, func(c *Config) {
		c.Open = func(ctx context.Context) (harness.Session, error) {
			s, err := h.Open(ctx)
			return recoveryReaderSession{s, &reads}, err
		}
	})
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 2})(e.app)
	e.say("finish retained work")
	eventually(t, "pre-start read failure is retried", func() bool {
		return reads.Load() >= 2 && e.count(`SELECT COUNT(*) FROM agent_recoveries WHERE status='completed'`) == 1
	})
	if n := e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id IN (SELECT request_id FROM agent_recovery_attempts)`); n != 1 {
		t.Fatalf("native startup intents=%d", n)
	}
	if blocked, err := e.app.AgentRecoveryUncertain(t.Context(), "main"); err != nil || blocked {
		t.Fatalf("read failure orphaned a fence: %t %v", blocked, err)
	}
}

func TestRecoveryInheritedOwnerStopTargetsCurrentNativeTurn(t *testing.T) {
	e, th, rec, _, supplement := setupRetainedRecovery(t, true)
	rec.Status = store.TurnStarted
	rec.TurnID = "current-continuation"
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: supplement}, protocol.InterruptReplyPayload{MessageID: supplement}, app.InterruptReply{})
	r, _, err := e.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil || r.Status != "cancelled" {
		t.Fatalf("retained owner stop: %+v %v", r, err)
	}
	d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadControlDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var stop app.InterruptTurn
	if err := json.Unmarshal(d.Payload, &stop); err != nil {
		t.Fatal(err)
	}
	if stop.MessageID != rec.MessageID {
		t.Fatalf("stop routed to %s; current native input is %s on %s", stop.MessageID, rec.MessageID, th.ID())
	}
}

func TestRecoveryRejectedContinuationRecoversAfterRestart(t *testing.T) {
	e, _, rec, d, _ := setupRetainedRecovery(t, false)
	// The native call was definitely rejected; crash between persisting this
	// rejection and RequeueRejectedAgentRecovery.
	rec.Status = store.TurnRejected
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	result := e.m.Reconcile(t.Context(), d)
	if result.Outcome != app.Retry {
		t.Fatalf("reconcile: %+v", result)
	}
	result = e.m.Deliver(t.Context(), d)
	r, _, err := e.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome == app.Unknown || r.Status != "waiting" {
		t.Fatalf("definitely rejected continuation stranded: result=%+v recovery=%+v", result, r)
	}
}

type recoveryBrokenCatalog struct{ harness.Session }

func (s recoveryBrokenCatalog) Models(context.Context) ([]harness.Model, error) {
	return nil, errors.New("model/list temporary transport failure")
}
func TestRecoveryTransientCatalogReadKeepsRecovery(t *testing.T) {
	e, th, rec, d, _ := setupRetainedRecovery(t, false)
	if err := e.app.RequeueRejectedAgentRecovery(t.Context(), d.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_recoveries SET next_attempt_at=0 WHERE active_request_id=?`, d.RequestID); err != nil {
			return err
		}
		return tx.PutConversationSettings(t.Context(), store.ConversationSettings{Model: "selected", Effort: "high", Revision: 1}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	e.m.mu.Lock()
	e.m.thread = &receiptThread{Thread: th, found: true, receipt: harness.TurnReceipt{ID: "original-native", Status: "failed", ContinuationSafe: true}}
	e.m.session = recoveryBrokenCatalog{}
	e.m.mu.Unlock()
	result := e.m.Deliver(t.Context(), d)
	r, _, err := e.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != app.Retry || r.Status != "waiting" {
		t.Fatalf("transient read permanently ended recovery: result=%+v recovery=%+v", result, r)
	}
}

func TestRecoveryTerminalWriteFailureReconciles(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		if in.Continue {
			return "completed"
		}
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: id, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded"}})
		return "failed"
	}
	e := newEnv(t, h)
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 1})(e.app)
	err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fixture_fail_terminal BEFORE UPDATE ON main_turns WHEN NEW.status='completed' AND EXISTS(SELECT 1 FROM agent_recovery_attempts WHERE request_id=NEW.request_id) BEGIN SELECT RAISE(ABORT,'fixture terminal persistence failure'); END;`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.say("probe terminal persistence")
	eventually(t, "terminal settlement returns Retry", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE operation='conversation.recover_agent' AND status='pending' AND attempts>0`) > 0
	})
	err = e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER fixture_fail_terminal`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.m.workMu.Lock()
	e.m.mu.Lock()
	th := e.m.thread
	e.m.mu.Unlock()
	if err := e.m.reconcileNativeReceipts(t.Context(), th); err != nil {
		e.m.workMu.Unlock()
		t.Fatal(err)
	}
	e.m.workMu.Unlock()
	eventually(t, "retry acknowledges continuation", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE operation='conversation.recover_agent' AND status='delivered'`) > 0
	})
	t.Logf("after transient write failure: running_recovery=%d started_turn=%d pending_receipt=%d", e.count(`SELECT COUNT(*) FROM agent_recoveries WHERE status='running'`), e.count(`SELECT COUNT(*) FROM main_turns t JOIN agent_recovery_attempts r ON r.request_id=t.request_id WHERE t.status='started'`), e.count(`SELECT COUNT(*) FROM native_turn_receipts_pending`))
	if e.count(`SELECT COUNT(*) FROM agent_recoveries WHERE status='completed'`) == 0 {
		t.Error("native completion is lost: retry delivers outbox without recovering terminal receipt")
	}
}

type recoveryRejectedInjectionSession struct {
	harness.Session
	reject func() bool
}

func (s recoveryRejectedInjectionSession) StartThread(ctx context.Context, c harness.ThreadConfig) (harness.Thread, error) {
	th, err := s.Session.StartThread(ctx, c)
	if err != nil {
		return nil, err
	}
	return &recoveryRejectedInjectionThread{th, s.reject}, nil
}
func (s recoveryRejectedInjectionSession) ResumeThread(ctx context.Context, id string, c harness.ThreadConfig) (harness.Thread, error) {
	th, err := s.Session.ResumeThread(ctx, id, c)
	if err != nil {
		return nil, err
	}
	return &recoveryRejectedInjectionThread{th, s.reject}, nil
}

type recoveryRejectedInjectionThread struct {
	harness.Thread
	reject func() bool
}

func (th recoveryRejectedInjectionThread) FindTurn(ctx context.Context, i, k string) (harness.TurnReceipt, bool, error) {
	return th.Thread.(harness.TurnReader).FindTurn(ctx, i, k)
}
func (th recoveryRejectedInjectionThread) InjectDeveloper(ctx context.Context, texts ...string) error {
	if th.reject() {
		return harness.ErrRejected
	}
	return th.Thread.InjectDeveloper(ctx, texts...)
}
func TestRecoveryKnownUnsentInjectionDoesNotFence(t *testing.T) {
	h := testkit.NewHarness()
	var e *env
	var rejected atomic.Bool
	h.Turn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		if in.Continue {
			return "completed"
		}
		if err := e.app.InvalidateBaseline(t.Context(), e.m.threadKey(th.ID())); err != nil {
			t.Error(err)
		}
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: id, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded"}})
		return "failed"
	}
	e = newEnv(t, h, func(c *Config) {
		c.Open = func(ctx context.Context) (harness.Session, error) {
			s, err := h.Open(ctx)
			return recoveryRejectedInjectionSession{s, func() bool {
				if e == nil {
					return false
				}
				e.m.mu.Lock()
				a := e.m.active
				isRecovery := a != nil && a.recovery
				e.m.mu.Unlock()
				return isRecovery && rejected.CompareAndSwap(false, true)
			}}, err
		}
	})
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 1})(e.app)
	e.say("probe known rejected injection")
	eventually(t, "injection rejected once", func() bool { return rejected.Load() })
	eventually(t, "handler exits", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE operation='conversation.recover_agent' AND status='failed'`) > 0
	})
	blocked, err := e.app.AgentRecoveryUncertain(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("known no-input rejection: conversation blocked=%t, unknown_no_turn=%d", blocked, e.count(`SELECT COUNT(*) FROM main_turns WHERE status='starting' AND COALESCE(turn_id,'')=''`))
	if blocked {
		t.Error("definite pre-StartTurn rejection permanently fences the conversation as an unknown native execution")
	}
}
