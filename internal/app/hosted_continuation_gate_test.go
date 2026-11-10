package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedOrdinaryContinuationWaitsForNativeReceipt(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	batch, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	finishHostedReview(t, h, startHostedReview(t, h, conversation, batch))
	ordinary, _ := startHostedIssueReview(t, h, issue)
	if err := h.app.AwaitNativeReceipts(t.Context(), ordinary.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), ordinary.ThreadID); err != nil {
		t.Fatal(err)
	}
	inflight, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range inflight {
		if d.RequestID == ordinary.RequestID {
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if h.count("agent_recoveries") != 0 {
		t.Fatal("fixture is recovery rather than ordinary native uncertainty")
	}
	submitHostedAdmissionInput(t, h, conversation)
	if ids, err := h.app.PendingAgentConversations(t.Context()); err != nil || !hasConversation(ids, conversation) {
		t.Fatal("receipt-only discovery lost", ids, err)
	}
	if d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || ok {
		t.Fatal("new input bypassed ordinary receipt fence", d, ok, err)
	}
	state, err := h.app.LogicalTurn(t.Context(), ordinary.RequestID)
	if err != nil || state.Status != "unknown" {
		t.Fatal("disconnect fabricated a terminal processing result", state, err)
	}
	ordinary.Status = store.TurnInterrupted
	ordinary.NativeReconciled = true
	if err := h.app.PutMainTurn(t.Context(), ordinary); err != nil {
		t.Fatal(err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || d.Operation != OpHandleMessage || d.RequestID == ordinary.RequestID {
		t.Fatal("reconciled native receipt did not release the new input", d, ok, err)
	}
	if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
		t.Fatal(err)
	}
	actual, found, err := h.app.MainTurn(t.Context(), ordinary.RequestID)
	if err != nil || !found || actual.Status != store.TurnInterrupted || actual.TurnID != ordinary.TurnID {
		t.Fatal("new admission rewrote the interrupted native fact", actual, found, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM outbox WHERE operation='conversation.repair_turn'`) != 0 {
		t.Fatal("ordinary receipt recovery created a metadata-only model round")
	}
}

func TestHostedMainFrozenRepairSurvivesUnavailableCurrentConfiguration(t *testing.T) {
	for _, pendingReceipt := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "receipt-only"}[pendingReceipt], func(t *testing.T) { testHostedMainUnavailableRepair(t, pendingReceipt) })
	}
}
func testHostedMainUnavailableRepair(t *testing.T, pendingReceipt bool) {
	h := hostedManagerHarness(t)
	_, delivery := h.say("Original owner goal must finish its metadata")
	original := startHostedReview(t, h, "main", delivery)
	selected, named, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil || !named {
		t.Fatal(named, err)
	}
	finishHostedReview(t, h, original)
	// Use the same existing bounded repair delay; a later owner input is queued.
	h.clock.Advance(11 * time.Minute)
	var ordinary store.MainTurn
	if pendingReceipt {
		_, d := h.say("Already admitted ordinary main turn requires reconciliation")
		ordinary = startHostedReview(t, h, "main", d)
		if err := h.app.AwaitNativeReceipts(t.Context(), ordinary.ThreadID); err != nil {
			t.Fatal(err)
		}
		if err := h.app.SettleOpenTurns(t.Context(), ordinary.ThreadID); err != nil {
			t.Fatal(err)
		}
		if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
			t.Fatal(err)
		}
	}
	queued := queueMessage(t, h, "New goal waits for available current configuration")
	h.app = New(h.store, h.ids, h.clock.Now)
	runtime, named, err := h.app.HostedRuntimeSelection(t.Context(), "main")
	if err != nil || !named || runtime.ConfigurationRevision != selected.ConfigurationRevision {
		t.Fatal("unavailable new foreground stranded frozen main repair", runtime, named, err)
	}
	if pendingReceipt {
		if d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
			t.Fatal("new unavailable-foreground fallback bypassed ordinary main receipt", d, ok, err)
		}
		ordinary.Status = store.TurnInterrupted
		ordinary.NativeReconciled = true
		if err := h.app.PutMainTurn(t.Context(), ordinary); err != nil {
			t.Fatal(err)
		}
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != "conversation.repair_turn" {
		t.Fatal("claim disagreed with serviceable frozen runtime", d, ok, err)
	}
	if pin, named, err := h.app.HostedSelectionForAdmission(t.Context(), d.RequestID, "main"); err != nil || !named || pin.ConfigurationRevision != selected.ConfigurationRevision {
		t.Fatal("frozen main pin lost", pin, named, err)
	}
	var input HandleMessage
	if err := json.Unmarshal(d.Payload, &input); err != nil {
		t.Fatal(err)
	}
	resumed := store.MainTurn{RequestID: d.RequestID, MessageID: input.MessageID, ThreadID: original.ThreadID, Status: store.TurnStarting, Hosted: &runtime, Model: runtime.Definition.Model, Effort: runtime.Definition.Effort}
	if err := h.app.PutMainTurn(t.Context(), resumed); err != nil {
		t.Fatal("frozen main could not admit original native executor", err)
	}
	resumed.Status, resumed.TurnID = store.TurnStarted, "resumed-main-repair"
	if err := h.app.PutMainTurn(t.Context(), resumed); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var status string
		err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM outbox WHERE operation='main_thread.handle_message' AND json_extract(payload_json,'$.message_id')=?`, queued).Scan(&status)
		if err == nil && status != "pending" {
			t.Fatal("unavailable new goal was claimed", status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
