package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func failedRecoveryTurn(t *testing.T, h *harness) store.MainTurn {
	t.Helper()
	id := queueMessage(t, h, "finish the existing work")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("delivery: %t %v", ok, err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: "native-thread", TurnID: "native-failed", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnFailed
	rec.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestAgentRecoveryPersistsDelayAndDeduplicates(t *testing.T) {
	h := newHarness(t)
	rec := failedRecoveryTurn(t, h)
	r, ok, err := h.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil || !ok || r.Status != "waiting" || r.Attempts != 1 {
		t.Fatalf("%+v %t %v", r, ok, err)
	}
	if r.NextAttemptAt <= h.clock.Now().UnixMilli() {
		t.Fatal("recovery was not delayed")
	}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	next, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || ok {
		t.Fatalf("early delivery %+v %v %v", next, ok, err)
	}
	// Reopen the use-case layer as a server restart would, without resetting time.
	restarted := New(h.store, h.ids, h.clock.Now)
	same, _, err := restarted.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil || same.ActiveRequestID != r.ActiveRequestID || same.NextAttemptAt != r.NextAttemptAt {
		t.Fatalf("restart changed recovery: %+v %v", same, err)
	}
	h.clock.Advance(time.Minute)
	d, _, ok, err := restarted.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != OpRecoverAgent {
		t.Fatalf("due delivery %+v %t %v", d, ok, err)
	}
	if admitted, err := restarted.AdmitAgentRecovery(t.Context(), d.RequestID, rec.ThreadID); err != nil || !admitted {
		t.Fatalf("admit %t %v", admitted, err)
	}
	if admitted, err := restarted.AdmitAgentRecovery(t.Context(), d.RequestID, rec.ThreadID); err != nil || admitted {
		t.Fatalf("duplicate admit %t %v", admitted, err)
	}
}

func TestAgentRecoveryBudgetAndTerminalCompletion(t *testing.T) {
	for _, status := range []string{store.TurnCompleted, store.TurnFailed} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			h.app.agentRecoveryPolicy = domain.AgentRecoveryPolicy{BaseDelay: time.Second, MaxDelay: time.Second, MaxElapsed: time.Hour, MaxAttempts: 1}
			rec := failedRecoveryTurn(t, h)
			r, _, _ := h.app.AgentRecovery(t.Context(), rec.RequestID)
			h.clock.Advance(time.Minute)
			d, _, _, _ := h.app.NextDelivery(t.Context(), MainThreadDestination)
			admitted, err := h.app.AdmitAgentRecovery(t.Context(), d.RequestID, rec.ThreadID)
			if err != nil || !admitted {
				t.Fatalf("admit %t %v", admitted, err)
			}
			rec.RequestID = d.RequestID
			rec.TurnID = "continued"
			rec.Status = store.TurnStarting
			if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			rec.Status = status
			if status == store.TurnCompleted {
				rec.Failure = nil
			}
			if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			after, _, err := h.app.AgentRecovery(t.Context(), r.RootRequestID)
			if err != nil {
				t.Fatal(err)
			}
			want := "exhausted"
			if status == store.TurnCompleted {
				want = "completed"
			}
			if after.Status != want || after.Attempts != 1 {
				t.Fatalf("%+v", after)
			}
		})
	}
}

func TestAgentRecoveryStopCancelsPendingForeground(t *testing.T) {
	h := newHarness(t)
	rec := failedRecoveryTurn(t, h)
	op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "message", ID: rec.MessageID}, protocol.InterruptReplyPayload{MessageID: rec.MessageID}, nil), InterruptReply{})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	r, _, err := h.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil || r.Status != "cancelled" {
		t.Fatalf("%+v %v", r, err)
	}
	h.clock.Advance(time.Hour)
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, rec.ThreadID); err != nil || yes {
		t.Fatalf("cancelled admitted %t %v", yes, err)
	}
}

func TestAgentRecoveryNewOwnerInputAdoptsResponsibility(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	next := queueMessage(t, h, "apply the new constraint first")
	r, _, err := h.app.AgentRecovery(t.Context(), old.RequestID)
	if err != nil || r.Status != "cancelled" {
		t.Fatalf("superseded chain: %+v %v", r, err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != OpHandleMessage {
		t.Fatalf("new input: %+v %t %v", d, ok, err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: next, ThreadID: old.ThreadID, TurnID: "new-owner-turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	v, err := h.app.LogicalTurn(t.Context(), d.RequestID)
	if err != nil || len(v.Messages) != 2 {
		t.Fatalf("lost responsibility: %+v %v", v, err)
	}
	h.clock.Advance(time.Minute)
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, old.ThreadID); err != nil || admitted {
		t.Fatalf("superseded admitted %t %v", admitted, err)
	}
}

func TestAgentRecoveryDeadlineAndSettingsGate(t *testing.T) {
	h := newHarness(t)
	rec := failedRecoveryTurn(t, h)
	r, _, _ := h.app.AgentRecovery(t.Context(), rec.RequestID)
	h.clock.Advance(time.Minute)
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, rec.ThreadID, 999); err != nil || admitted {
		t.Fatalf("stale settings admitted %t %v", admitted, err)
	}
	h.clock.Advance(time.Hour)
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, rec.ThreadID); err != nil || admitted {
		t.Fatalf("expired admitted %t %v", admitted, err)
	}
	r, _, err := h.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil || r.Status != "exhausted" {
		t.Fatalf("deadline: %+v %v", r, err)
	}
	before := h.count("conversation_messages")
	if err := h.app.BlockAgentRecovery(t.Context(), r.ActiveRequestID, "still blocked"); err != nil {
		t.Fatal(err)
	}
	if h.count("conversation_messages") != before {
		t.Fatal("duplicate terminal notice")
	}
}

func TestAgentRecoveryBackgroundDelayReleasesAdmissionAndStop(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	conversation := "work-item:" + w.WorkItemID
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "inspect with automatic recovery"}, nil), SubmitMessage{})
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok {
		t.Fatal(err)
	}
	var input HandleMessage
	if err := json.Unmarshal(d.Payload, &input); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
		t.Fatal(err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: input.MessageID, ThreadID: "bg-thread", TurnID: "bg-failed", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnFailed
	rec.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || len(ids) != 0 {
		t.Fatalf("waiting occupied process admission: %v %v", ids, err)
	}
	h.clock.Advance(time.Minute)
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || len(ids) != 1 || ids[0] != conversation {
		t.Fatalf("due recovery not admitted: %v %v", ids, err)
	}
	op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "message", ID: rec.MessageID}, protocol.InterruptReplyPayload{MessageID: rec.MessageID}, nil), InterruptReply{})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	r, _, err := h.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil || r.Status != "cancelled" {
		t.Fatalf("background stop %+v %v", r, err)
	}
}

func TestAgentRecoveryLateAncestorReceiptCannotRegressStoppedMessage(t *testing.T) {
	h := newHarness(t)
	rec := failedRecoveryTurn(t, h)
	h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "message", ID: rec.MessageID}, protocol.InterruptReplyPayload{MessageID: rec.MessageID}, nil), InterruptReply{})
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		item, err := store.GetConversationItem(t.Context(), tx.Q(), rec.MessageID)
		if err == nil && item.State != "interrupted" {
			t.Fatalf("late receipt regressed stopped message: %s", item.State)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRecoveryWithdrawnSuccessorRetainsResponsibility(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	successor := queueMessage(t, h, "replacement constraint")
	q := readMessageQueue(t, h, "owner")
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: successor}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision)), WithdrawMessage{})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	next := queueMessage(t, h, "the actual new instruction")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("delivery %v %v", ok, err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: next, ThreadID: old.ThreadID, TurnID: "next", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	v, err := h.app.LogicalTurn(t.Context(), d.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range v.Messages {
		if m.MessageID == old.MessageID {
			return
		}
	}
	t.Fatalf("unfinished original %s not adopted by actual successor: %+v", old.MessageID, v)
}

func TestAgentRecoveryBackgroundReconnectRetainsNativeReceipt(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	conversation := "work-item:" + w.WorkItemID
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "inspect"}, nil), SubmitMessage{})
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok {
		t.Fatalf("delivery %v %v", ok, err)
	}
	var input HandleMessage
	if err := json.Unmarshal(d.Payload, &input); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
		t.Fatal(err)
	}
	m := store.MainTurn{RequestID: d.RequestID, MessageID: input.MessageID, ThreadID: "bg-native", TurnID: "bg-root", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m.Status = store.TurnFailed
	m.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	recovery, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok {
		t.Fatalf("recovery %v %v", ok, err)
	}
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), recovery.RequestID, m.ThreadID); err != nil || !yes {
		t.Fatalf("admit %v %v", yes, err)
	}
	m.RequestID = recovery.RequestID
	m.Status = store.TurnStarting
	m.TurnID = ""
	m.Failure = nil
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m.Status = store.TurnStarted
	m.TurnID = "bg-continuation"
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), m.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), m.ThreadID); err != nil {
		t.Fatal(err)
	}
	turns, err := h.app.UncertainNativeTurns(t.Context(), m.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) == 0 {
		r, _, _ := h.app.AgentRecovery(t.Context(), recovery.RequestID)
		blocked, _ := h.app.AgentRecoveryUncertain(t.Context(), conversation)
		t.Fatalf("known continuation lost from receipt reconciliation; recovery=%+v blocked=%t", r, blocked)
	}
}

func TestAgentRecoveryNewInputFencesAdmittedAttempt(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	r, _, _ := h.app.AgentRecovery(t.Context(), old.RequestID)
	h.clock.Advance(time.Minute)
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, old.ThreadID, 0); err != nil || !yes {
		t.Fatal(yes, err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: r.ActiveRequestID, MessageID: old.MessageID, ThreadID: old.ThreadID, Status: store.TurnStarting}); err != nil {
		t.Fatal(err)
	}
	queueMessage(t, h, "do not continue the old plan; apply this new instruction first")
	yes, err := h.app.ConfirmAgentRecoveryStart(t.Context(), r.ActiveRequestID, old.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if yes {
		t.Fatal("old empty-input continuation is still authorized after new owner input committed before native start")
	}
}
func TestAgentRecoveryDisabledPolicyFencesPersistedAttempt(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	r, _, _ := h.app.AgentRecovery(t.Context(), old.RequestID)
	p := h.app.agentRecoveryPolicy
	p.MaxAttempts = 0
	restarted := New(h.store, h.ids, h.clock.Now, WithAgentRecoveryPolicy(p))
	h.clock.Advance(time.Minute)
	yes, err := restarted.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, old.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if yes {
		t.Fatal("persisted recovery admitted after restart with max_attempts=0")
	}
}

func TestAgentRecoveryDeadlineSettlesLogicalResponsibility(t *testing.T) {
	h := newHarness(t)
	original := failedRecoveryTurn(t, h)
	r, _, err := h.app.AgentRecovery(t.Context(), original.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)
	yes, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, original.ThreadID, 0)
	if err != nil || yes {
		t.Fatal(yes, err)
	}
	v, err := h.app.LogicalTurn(t.Context(), original.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ = h.app.AgentRecovery(t.Context(), original.RequestID)
	t.Logf("recovery=%s logical=%s", r.Status, v.Status)
	if v.Status == "processing" {
		t.Fatal("expired continuation left a processing responsibility with no runnable execution")
	}
}

func TestAgentRecoveryUnknownSupplementIsNotSuccessor(t *testing.T) {
	h := newHarness(t)
	id := queueMessage(t, h, "original request")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: "native", TurnID: "root", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	next := queueMessage(t, h, "important newer owner input")
	sd, ok, err := h.app.ClaimMainSteer(t.Context(), d.RequestID, rec.ThreadID, rec.TurnID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	s, _, err := h.app.MainSteer(t.Context(), sd.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	s.InputText = "unknown-steer"
	s.Status = "unknown"
	if err := h.app.UpdateMainSteer(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), sd, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnFailed
	rec.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
	if err := h.app.SettleMainSteers(t.Context(), rec.ThreadID, rec.TurnID, rec.Status); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	r, _, err := h.app.AgentRecovery(t.Context(), d.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	nd, _, runnable, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recovery=%s successor message=%s runnable=%v next=%+v", r.Status, next, runnable, nd)
	if r.Status == "cancelled" && !runnable {
		t.Fatal("responsibility transferred to an unknown steer that has no runnable outbox delivery")
	}
}

func TestAgentRecoveryUnknownBackgroundSchedulesReceiptReads(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	conversation := "work-item:" + w.WorkItemID
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "inspect"}, nil), SubmitMessage{})
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var input HandleMessage
	if err := json.Unmarshal(d.Payload, &input); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
		t.Fatal(err)
	}
	m := store.MainTurn{RequestID: d.RequestID, MessageID: input.MessageID, ThreadID: "bg-native", TurnID: "bg-root", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m.Status = store.TurnFailed
	m.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	rd, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), rd.RequestID, m.ThreadID); err != nil || !yes {
		t.Fatal(yes, err)
	}
	m.RequestID = rd.RequestID
	m.Status = store.TurnStarting
	m.TurnID = ""
	m.Failure = nil
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	m.Status = store.TurnStarted
	m.TurnID = "bg-continuation"
	if err := h.app.PutMainTurn(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), m.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), m.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.BlockAgentRecovery(t.Context(), rd.RequestID, "temporary native history read failure"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), rd, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	pending, err := h.app.NativeReceiptPending(t.Context(), rd.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pending receipt=%v scheduled backgrounds=%v", pending, ids)
	if pending && len(ids) == 0 {
		t.Fatal("known native execution still needs receipt reads but no background process will be scheduled")
	}
	if pending, err := h.app.DeferAgentReceiptRead(t.Context(), conversation); err != nil || !pending {
		t.Fatal(pending, err)
	}
	restarted := New(h.store, h.ids, h.clock.Now)
	ids, err = restarted.PendingAgentConversations(t.Context())
	if err != nil || len(ids) != 0 {
		t.Fatalf("receipt backoff was not retained across restart: %v %v", ids, err)
	}
	h.clock.Advance(31 * time.Second)
	ids, err = restarted.PendingAgentConversations(t.Context())
	if err != nil || len(ids) != 1 || ids[0] != conversation {
		t.Fatalf("receipt read not rescheduled: %v %v", ids, err)
	}
}

func TestAgentRecoveryWithdrawalRestoresRetainedCandidate(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	successor := queueMessage(t, h, "new constraint to withdraw")
	q := readMessageQueue(t, h, "owner")
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: successor}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision)), WithdrawMessage{})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	h.clock.Advance(time.Hour)
	r, _, _ := h.app.AgentRecovery(t.Context(), old.RequestID)
	l, _ := h.app.LogicalTurn(t.Context(), old.RequestID)
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	t.Logf("recovery=%s logical=%s next=%+v runnable=%t err=%v", r.Status, l.Status, d, ok, err)
	if r.Status == "cancelled" && l.Status == "processing" && !ok {
		t.Error("original responsibility is stranded after withdrawal")
	}
}
func TestAgentRecoverySuccessorProjectsOriginalMessage(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	successor := queueMessage(t, h, "new constraint")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: successor, ThreadID: old.ThreadID, TurnID: "successor-native", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE message_dispositions SET disposition='unassigned',classified_revision=content_revision,facts_revision=content_revision WHERE message_id IN (?,?)`, old.MessageID, successor)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		item, err := store.GetConversationItem(t.Context(), tx.Q(), old.MessageID)
		if err != nil {
			return err
		}
		a, err := store.MessageAssociations(t.Context(), tx.Q(), old.MessageID)
		if err != nil {
			return err
		}
		t.Logf("old state=%s logical=%+v", item.State, a.Processing)
		if item.State == "processing" && a.Processing.Status == "completed" {
			t.Error("completed responsibility still projects processing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRecoveryBackgroundStopBeforeFinalRateLimitIsDurable(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	conversation := "work-item:" + w.WorkItemID
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "inspect"}, nil), SubmitMessage{})
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var input HandleMessage
	if err := json.Unmarshal(d.Payload, &input); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
		t.Fatal(err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: input.MessageID, ThreadID: "background-native", TurnID: "running-native", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "message", ID: rec.MessageID}, protocol.InterruptReplyPayload{MessageID: rec.MessageID}, nil), InterruptReply{})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	rec.Status = store.TurnFailed
	rec.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if _, found, err := h.app.AgentRecovery(t.Context(), rec.RequestID); err != nil || found {
		t.Fatalf("stopped background scheduled recovery: %t %v", found, err)
	}
}

func TestAgentRecoveryQueuedOwnerAfterSecondRateLimit(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	r, _, _ := h.app.AgentRecovery(t.Context(), old.RequestID)
	h.clock.Advance(time.Minute)
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), d.RequestID, old.ThreadID); err != nil || !yes {
		t.Fatal(yes, err)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: old.MessageID, ThreadID: old.ThreadID, Status: store.TurnStarting}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnStarted
	rec.TurnID = "continued-native"
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	successor := queueMessage(t, h, "apply this newer owner constraint")
	rec.Status = store.TurnFailed
	rec.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	r, _, _ = h.app.AgentRecovery(t.Context(), r.RootRequestID)
	next, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: next.RequestID, MessageID: successor, ThreadID: old.ThreadID, TurnID: "successor-native", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	v, err := h.app.LogicalTurn(t.Context(), next.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("old recovery status=%s active=%s; successor responsibility=%+v", r.Status, r.ActiveRequestID, v.Messages)
	if len(v.Messages) != 2 {
		t.Fatalf("new owner did not inherit failed recovery responsibility: recovery=%s messages=%d", r.Status, len(v.Messages))
	}
}

func TestAgentRecoveryReplacementFailureRetainsResponsibility(t *testing.T) {
	h := newHarness(t)
	old := failedRecoveryTurn(t, h)
	queueMessage(t, h, "new constraint whose selected model is unavailable")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Failed, Reason: "selected model unavailable"}); err != nil {
		t.Fatal(err)
	}
	r, _, err := h.app.AgentRecovery(t.Context(), old.RequestID)
	if err != nil || r.Status != "blocked" || h.count("conversation_return_links") == 0 {
		t.Fatalf("retained responsibility invisible: %+v %v", r, err)
	}
	v, err := h.app.LogicalTurn(t.Context(), old.RequestID)
	if err != nil || v.Status != "needs_review" {
		t.Fatalf("stranded responsibility: %+v %v", v, err)
	}
}

func TestAgentRecoveryUnknownBackgroundReplacementSchedulesRead(t *testing.T) {
	for _, nativeID := range []string{"", "replacement-native"} {
		name := nativeID
		if name == "" {
			name = "missing-native-id"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			w := h.createWork(workPlan())
			h.prepareAgent(w.WorkItemID)
			h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
			conversation := "work-item:" + w.WorkItemID
			submit := func(body string) {
				h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: body}, nil), SubmitMessage{})
				h.reviewBackgroundOwnerNotices()
			}
			submit("original background responsibility")
			d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			var in HandleMessage
			if err := json.Unmarshal(d.Payload, &in); err != nil {
				t.Fatal(err)
			}
			if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
				t.Fatal(err)
			}
			old := store.MainTurn{RequestID: d.RequestID, MessageID: in.MessageID, ThreadID: "background-thread", TurnID: "failed-native", Status: store.TurnStarted}
			if err := h.app.PutMainTurn(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			old.Status = store.TurnFailed
			old.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
			if err := h.app.PutMainTurn(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
			submit("replacement constraint")
			d, _, ok, err = h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if err := json.Unmarshal(d.Payload, &in); err != nil {
				t.Fatal(err)
			}
			if _, err := h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
				t.Fatal(err)
			}
			rec := store.MainTurn{RequestID: d.RequestID, MessageID: in.MessageID, ThreadID: old.ThreadID, TurnID: nativeID, Status: store.TurnStarting}
			if nativeID != "" {
				rec.Status = store.TurnStarted
			}
			if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			if err := h.app.AwaitNativeReceipts(t.Context(), rec.ThreadID); err != nil {
				t.Fatal(err)
			}
			if err := h.app.SettleOpenTurns(t.Context(), rec.ThreadID); err != nil {
				t.Fatal(err)
			}
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
				t.Fatal(err)
			}
			pending, err := h.app.NativeReceiptPending(t.Context(), rec.RequestID)
			if err != nil || !pending {
				t.Fatalf("replacement lost receipt: %t %v", pending, err)
			}
			blocked, err := h.app.AgentRecoveryUncertain(t.Context(), conversation)
			if err != nil || !blocked {
				t.Fatalf("replacement lost fence: %t %v", blocked, err)
			}
			ids, err := h.app.PendingAgentConversations(t.Context())
			if err != nil || len(ids) != 1 || ids[0] != conversation {
				t.Fatalf("replacement lost read candidate: %v %v", ids, err)
			}
			if deferred, err := h.app.DeferAgentReceiptRead(t.Context(), conversation); err != nil || !deferred {
				t.Fatal(deferred, err)
			}
			ids, err = h.app.PendingAgentConversations(t.Context())
			if err != nil || len(ids) != 0 {
				t.Fatalf("receipt read did not back off: %v %v", ids, err)
			}
		})
	}
}

func TestAgentRecoveryEndedWaitPublishesBufferedOutput(t *testing.T) {
	for _, finish := range []string{"stop", "deadline"} {
		t.Run(finish, func(t *testing.T) {
			h := newHarness(t)
			w := h.createWork(workPlan())
			h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
			d, turn := h.startBackground()
			op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "partial result retained before rate limit", ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "partial-review-result", Status: protocol.AssistantCompleted}, nil), RecordAgentMessage{})
			if op.Error != nil {
				t.Fatal(op.Error)
			}
			turn.Status = store.TurnFailed
			turn.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded"}
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
			r, ok, err := h.app.AgentRecovery(t.Context(), turn.RequestID)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if finish == "deadline" {
				h.clock.Advance(time.Hour)
				if yes, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, turn.ThreadID); err != nil || yes {
					t.Fatal(yes, err)
				}
			} else {
				op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "message", ID: turn.MessageID}, protocol.InterruptReplyPayload{MessageID: turn.MessageID}, nil), InterruptReply{})
				if op.Error != nil {
					t.Fatal(op.Error)
				}
			}
			batch := h.backgroundPage(turn.MessageID, 0).Batch
			var unpublished int
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_replies WHERE batch_id=? AND published=0`, batch.BatchID).Scan(&unpublished)
			}); err != nil {
				t.Fatal(err)
			}
			t.Logf("after %s: background batch status=%s unpublished output=%d", finish, batch.Status, unpublished)
			if batch.Status == "frozen" || unpublished > 0 {
				t.Error("ended recovery leaves buffered native output permanently unpublished")
			}
		})
	}
}
