package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func activeSteerEnv(t *testing.T) (*harness, Delivery) {
	t.Helper()
	h := newHarness(t)
	first := queueMessage(t, h, "original")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: first, ThreadID: "native", TurnID: "turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	return h, d
}

func TestMainSteerAdmissionOrderWithdrawalAndDecisionSource(t *testing.T) {
	h, active := activeSteerEnv(t)
	first := queueMessage(t, h, "withdraw this")
	q := readMessageQueue(t, h, "owner")
	for _, it := range q.Items {
		if it.Item.MessageID == first {
			op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: first}, protocol.WithdrawMessagePayload{}, rev(it.Revision)), WithdrawMessage{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}
	}
	second := queueMessage(t, h, "keep this")
	d, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn")
	if err != nil || !ok {
		t.Fatal(err)
	}
	var p HandleMessage
	_ = json.Unmarshal(d.Payload, &p)
	if p.MessageID != second {
		t.Fatal("withdrawn or unordered input claimed")
	}
	if _, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn"); err != nil || ok {
		t.Fatal("same steer claimed twice")
	}
	for _, it := range readMessageQueue(t, h, "owner").Items {
		if it.Item.MessageID == second && it.CanWithdraw {
			t.Fatal("claimed steer remains withdrawable")
		}
	}
	rec, _, _ := h.app.MainSteer(t.Context(), d.RequestID)
	rec.InputText = "exact source"
	if err := h.app.UpdateMainSteer(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "other", "turn", rec.InputText, "item"); err != nil || ok {
		t.Fatal("foreign session consumed input")
	}
	if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "native", "turn", rec.InputText, "item"); err != nil || !ok {
		t.Fatal(err)
	}
	// The user-item receipt can precede a lost RPC reply; its proof must win.
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	for _, it := range readMessageQueue(t, h, "owner").Items {
		if it.Item.MessageID == second {
			t.Fatal("consumed steer left pending")
		}
	}
	// Native completion does not discharge unreviewed owner input.
	if err := h.app.SettleMainSteers(t.Context(), "native", "turn", store.TurnCompleted); err != nil {
		t.Fatal(err)
	}
	_ = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetConversationItem(t.Context(), tx.Q(), second)
		if err != nil {
			return err
		}
		if row.State != string(protocol.MessageProcessing) {
			t.Errorf("unreviewed supplement completed early: state=%s", row.State)
		}
		return nil
	})
}

func TestMainSteerDoesNotCrossHandoffOrSteerSystemEvents(t *testing.T) {
	h, active := activeSteerEnv(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: h.ids.New(protocol.PrefixOutbox), RequestID: h.ids.New(protocol.PrefixRequest), Destination: MainThreadDestination, Operation: "conversation.handoff", PayloadJSON: `{}`})
	}); err != nil {
		t.Fatal(err)
	}
	queueMessage(t, h, "after handoff")
	if _, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn"); err != nil || ok {
		t.Fatal("owner input crossed handoff")
	}
	if _, ok, err := h.app.ClaimMainSteer(context.Background(), active.RequestID, "native", "different"); err != nil || ok {
		t.Fatal("wrong current turn accepted")
	}
}

func TestExecutionScopeKeepsEventReviewWithHistoricalAuditParent(t *testing.T) {
	h := newHarness(t)
	work := h.createWork(workPlan())
	oldMessage := queueMessage(t, h, "old owner input")
	currentMessage := queueMessage(t, h, "current owner input")
	oldRequest, currentRequest := h.ids.New(protocol.PrefixRequest), h.ids.New(protocol.PrefixRequest)
	for _, turn := range []store.MainTurn{
		{RequestID: oldRequest, MessageID: oldMessage, ThreadID: "native", TurnID: "old", Status: store.TurnStarted},
		{RequestID: currentRequest, MessageID: currentMessage, ThreadID: "native", TurnID: "current", Status: store.TurnStarted},
	} {
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
	}
	work.Title = "Revised within the current execution"
	env := h.env("work_items.update", workTarget(work.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: work.WorkItemPlan}, rev(work.Revision))
	env.ParentRequestID = oldRequest
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
	if op := h.exec(principal, env, WithExecutionRequest(UpdateWorkItem{}, currentRequest)); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	state, err := h.app.LogicalTurn(t.Context(), currentRequest)
	if err != nil || len(state.Contexts) != 1 || state.Contexts[0].WorkItemID != work.WorkItemID {
		t.Fatalf("event review escaped current execution: %+v, %v", state, err)
	}
	old, err := h.app.LogicalTurn(t.Context(), oldRequest)
	if err != nil || len(old.Contexts) != 0 {
		t.Fatalf("audit parent acquired current event review: %+v, %v", old, err)
	}
}
