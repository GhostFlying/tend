package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

type receiptThread struct {
	harness.Thread
	receipt harness.TurnReceipt
	found   bool
	known   string
}

func (t *receiptThread) FindTurn(_ context.Context, _ string, known string) (harness.TurnReceipt, bool, error) {
	t.known = known
	return t.receipt, t.found, nil
}

func TestReconnectRecoversFinalNativeMessagesBeforeMetadataRepair(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	source := e.say("Original owner request")
	session, err := e.h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	base, err := session.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	th := &receiptThread{Thread: base}
	if err := e.app.RegisterAgentThread(t.Context(), "main", th.ID()); err != nil {
		t.Fatal(err)
	}
	req := e.ids.New(protocol.PrefixRequest)
	rec := store.MainTurn{RequestID: req, MessageID: source, ThreadID: th.ID(), TurnID: "native-known", Status: store.TurnStarted}
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	partial := harness.Event{TurnID: rec.TurnID, ItemID: "progress", ItemType: harness.ItemAgentMessage, Text: "Partial"}
	if err := e.m.recordReplyState(t.Context(), th, partial, protocol.AssistantStreaming); err != nil {
		t.Fatal(err)
	}
	if err := e.app.SettleOpenTurns(t.Context(), th.ID()); err != nil {
		t.Fatal(err)
	}
	// An old Deliver completion racing cleanup cannot bypass the receipt fence.
	rec.Status = store.TurnInterrupted
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := e.m.reconcileNativeReceipts(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	state, err := e.app.LogicalTurn(t.Context(), req)
	if err != nil || state.Status != "unknown" || state.RepairAttempts != 0 {
		t.Fatal(state, err)
	}
	th.found = true
	th.receipt = harness.TurnReceipt{ID: rec.TurnID, Status: "completed", Raw: json.RawMessage(`{"id":"native-known","status":"completed","items":[]}`), Messages: []harness.Event{
		{TurnID: rec.TurnID, ItemID: "progress", ItemType: harness.ItemAgentMessage, Text: "Partial text completed in native history"},
		{TurnID: rec.TurnID, ItemID: "final", ItemType: harness.ItemAgentMessage, Text: "Final answer never received before crash"},
	}}
	if err := e.m.reconcileNativeReceipts(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	if th.known != rec.TurnID {
		t.Fatal("did not locate the exact known native turn")
	}
	state, err = e.app.LogicalTurn(t.Context(), req)
	if err != nil || state.Status != "recovering" || state.RepairAttempts != 1 || len(state.Messages) != 3 {
		t.Fatal(state, err)
	}
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body IN ('Partial text completed in native history','Final answer never received before crash')`) != 2 {
		t.Fatal("native final messages were not restored")
	}
	if err := e.m.reconcileNativeReceipts(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM logical_turn_executions WHERE repair=1`) != 1 || len(e.h.Starts()) != 0 {
		t.Fatal("reconciliation replayed a model execution")
	}
}

type transientReceiptThread struct {
	harness.Thread
	calls atomic.Int32
}

func (t *transientReceiptThread) FindTurn(context.Context, string, string) (harness.TurnReceipt, bool, error) {
	if t.calls.Add(1) == 1 {
		return harness.TurnReceipt{}, false, errors.New("temporary history read failure")
	}
	return harness.TurnReceipt{ID: "native-recovered", Status: "completed", Raw: json.RawMessage(`{"id":"native-recovered","status":"completed"}`)}, true, nil
}
func TestNativeReceiptPollingRecoversAfterTransientReadFailure(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	source := e.say("Retain responsibility across a history read failure")
	session, err := e.h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	base, err := session.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	th := &transientReceiptThread{Thread: base}
	if err := e.app.RegisterAgentThread(t.Context(), "main", th.ID()); err != nil {
		t.Fatal(err)
	}
	req := e.ids.New(protocol.PrefixRequest)
	rec := store.MainTurn{RequestID: req, MessageID: source, ThreadID: th.ID(), TurnID: "native-recovered", Status: store.TurnStarted}
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := e.app.SettleOpenTurns(t.Context(), th.ID()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); e.m.pollNativeReceipts(ctx, th, time.Millisecond) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(5 * time.Second)
	for {
		state, err := e.app.LogicalTurn(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status == "recovering" {
			if state.RepairAttempts != 1 || th.calls.Load() != 2 || len(e.h.Starts()) != 0 {
				t.Fatal(state, th.calls.Load())
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("unknown receipt never recovered")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestHandoffCanStrandNativeReceipt(t *testing.T) {
	f := newHandoffFixture(t)
	id := f.e.say("Original request requiring reconciliation")
	d, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: f.source.ID(), TurnID: "original-native", Status: store.TurnStarted}
	if err := f.e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.SettleOpenTurns(t.Context(), f.source.ID()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.FinishDelivery(t.Context(), d, app.DeliveryResult{Outcome: app.Unknown}); err != nil {
		t.Fatal(err)
	}
	target, err := f.e.m.openManagedThread(t.Context(), f.session)
	if err != nil {
		t.Log("handoff correctly blocked", err)
		return
	}
	old, _ := f.e.app.UncertainNativeTurns(t.Context(), f.source.ID())
	current, _ := f.e.app.UncertainNativeTurns(t.Context(), target.ID())
	if target.ID() != f.source.ID() && len(old) == 1 && len(current) == 0 {
		t.Fatal("handoff switched threads with an unresolved old native receipt; current-thread polling cannot revisit it")
	}
}

func TestClaimedRepairStoppedBeforeNativeAdmission(t *testing.T) {
	f := newHandoffFixture(t)
	id := f.e.say("Stop this logical responsibility")
	d, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: f.source.ID(), TurnID: "original", Status: store.TurnCompleted}
	if err := f.e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.FinishDelivery(t.Context(), d, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	repair, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok || repair.Operation != "conversation.repair_turn" {
		t.Fatal(repair, err)
	}
	f.e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: id}, app.InterruptReply{})
	f.e.m.mu.Lock()
	f.e.m.session, f.e.m.thread = f.session, f.source
	close(f.e.m.ready)
	f.e.m.mu.Unlock()
	result := f.e.m.Deliver(t.Context(), repair)
	if result.Outcome != app.Delivered {
		t.Fatal(result)
	}
	if err := f.e.app.FinishDelivery(t.Context(), repair, result); err != nil {
		t.Fatal(err)
	}
	state, err := f.e.app.LogicalTurn(t.Context(), d.RequestID)
	if err != nil || state.Status != "stopped" || state.RepairAttempts != 1 || len(f.e.h.Starts()) != 0 {
		t.Fatal(state, err)
	}
	snap, err := f.e.app.HandoffSnapshot(t.Context(), "main")
	if err != nil || snap.Pending || snap.Active {
		t.Fatal(snap, err)
	}
}

func TestRepairExposesOriginalMessageAsActiveUntilNativeStop(t *testing.T) {
	f := newHandoffFixture(t)
	id := f.e.say("Stop a running metadata repair")
	d, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: f.source.ID(), TurnID: "original", Status: store.TurnCompleted}
	if err := f.e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.FinishDelivery(t.Context(), d, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	repair, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok || repair.Operation != "conversation.repair_turn" {
		t.Fatal(repair, err)
	}
	rec.RequestID, rec.TurnID, rec.Status = repair.RequestID, "repair", store.TurnStarted
	if err := f.e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	check := func(wantActive bool) {
		t.Helper()
		if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			page, err := views.ConversationQueue(t.Context(), tx.Q(), "owner", 0, 30)
			if err != nil {
				return err
			}
			if wantActive {
				if page.Active == nil || page.Active.Item.MessageID != id || page.Active.Status != "processing" || page.Active.CanWithdraw {
					t.Fatalf("repair missing stoppable original message: %+v", page)
				}
			} else if page.Active != nil {
				t.Fatalf("settled repair remains active: %+v", page.Active)
			}
			// A metadata retry is not a new owner input that can be withdrawn or edited.
			if len(page.Items) != 0 {
				t.Fatalf("repair leaked into owner input queue: %+v", page.Items)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(true)
	f.e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: id}, app.InterruptReply{})
	if f.e.count(`SELECT COUNT(*) FROM outbox WHERE operation='main_thread.interrupt_turn' AND status='pending'`) != 1 {
		t.Fatal("stop did not queue a native interrupt")
	}
	check(true)
	rec.Status = store.TurnInterrupted
	if err := f.e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.FinishDelivery(t.Context(), repair, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	check(false)
	state, err := f.e.app.LogicalTurn(t.Context(), d.RequestID)
	if err != nil || state.Status != "stopped" {
		t.Fatal(state, err)
	}
}
