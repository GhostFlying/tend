package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestLogicalReviewLateReplyAfterClarificationAdoption(t *testing.T) {
	h := newHarness(t)
	id, first := startReviewTurn(t, h)
	wait := AwaitTurnClarification{RequestID: first.RequestID}
	op := h.exec(mainAgent, h.env(wait.Operation(), protocol.Target{Type: "logical_turn", ID: first.RequestID}, protocol.TurnClarification{Question: "Which item?"}, nil), wait)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	first.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	// Set the original claimed outbox delivery to delivered, as dispatch does.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, first.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	answer, second := startReviewTurn(t, h)
	second.TurnID = "native-2"
	if err := h.app.PutMainTurn(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), second.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	pending := state.Awaiting[0]
	adopt := AdoptClarification{RequestID: second.RequestID}
	op = h.exec(mainAgent, h.env(adopt.Operation(), protocol.Target{Type: "logical_turn", ID: first.RequestID}, protocol.AdoptClarificationPayload{LogicalTurnID: first.RequestID, Revision: pending.Revision}, rev(pending.Revision)), adopt)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A final native item from the first turn arrives after adoption copied its set.
	op = h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{ReplyTo: id, TurnID: first.TurnID, ItemID: "late-final", Body: "Late final text"}, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.associate(mainAgent, id)
	h.associate(mainAgent, answer)
	adopted, err := h.app.LogicalTurn(t.Context(), second.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(adopted.Messages) != 3 {
		t.Fatalf("late reply not transferred: %+v", adopted.Messages)
	}
	for _, m := range adopted.Messages {
		if m.Disposition == "pending" {
			h.associate(mainAgent, m.MessageID)
		}
	}
	commitFixtureTurn(t, h, second.RequestID)
	second.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	original, err := h.app.LogicalTurn(t.Context(), first.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range original.Messages {
		if original.Status == "completed" && (m.Disposition == "pending" || !m.FactsReviewed) {
			t.Fatalf("completed original with unreviewed late item: %+v", m)
		}
	}
}

func TestLogicalReviewGlobalFactSourceGrowthInvalidatesInheritedContext(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "Global constraint draft", ReplyTo: id, TurnID: turn.TurnID, ItemID: "progress", Status: protocol.AssistantStreaming}, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	h.associate(mainAgent, id)
	h.associate(mainAgent, result.MessageID)
	c := h.agentContext(w.WorkItemID)
	env := h.env("work_item_context.fact", workTarget(w.WorkItemID), protocol.ContextFact{Scope: "global", Kind: "constraint", Body: "Global rule", SourceMessageID: result.MessageID}, rev(c.Revision))
	env.ParentRequestID = turn.RequestID
	op = h.exec(mainAgent, env, ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	before := h.agentContext(w.WorkItemID)
	op = h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "Global constraint draft, now revised", ReplyTo: id, TurnID: turn.TurnID, ItemID: "progress", Status: protocol.AssistantCompleted}, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := h.agentContext(w.WorkItemID)
	if after.Revision == before.Revision {
		t.Fatalf("inherited context revision stayed %d while fact needs_review=%v and sync_status=%s", after.Revision, after.Facts[0].NeedsReview, after.SyncStatus)
	}
}

func TestLogicalReviewRepairStartFailureLeavesRecoveryStranded(t *testing.T) {
	h := newHarness(t)
	_, turn := startReviewTurn(t, h)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, turn.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != "conversation.repair_turn" {
		t.Fatal(d, ok, err)
	}
	// Deliver returns this before creating the repair's MainTurn if turnModel
	// reports that the selected model or effort is no longer available.
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Failed, Reason: "the selected model is no longer available; update conversation settings"}); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "recovering" || state.RepairAttempts != 2 {
		t.Fatalf("failed admission did not retain recovery: %+v", state)
	}
	next, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || next.Operation != "conversation.repair_turn" || next.RequestID == d.RequestID {
		t.Fatal(next, ok, err)
	}
}

func TestBackgroundFactSourceGrowthInvalidatesInheritedContext(t *testing.T) {
	h := newHarness(t)
	sourceWork := h.createWork(workPlan())
	inheritedWork := h.createWork(workPlan())
	source := "background-fact-source"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, ConversationID: "work-item:" + sourceWork.WorkItemID, Author: "main_agent", Body: "Original background evidence", CreatedAt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c := h.agentContext(inheritedWork.WorkItemID)
	op := h.exec(mainAgent, h.env("work_item_context.fact", workTarget(inheritedWork.WorkItemID), protocol.ContextFact{Scope: "global", Kind: "constraint", Body: "Global rule from evidence", SourceMessageID: source}, rev(c.Revision)), ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.prepareAgent(inheritedWork.WorkItemID)
	before := h.agentContext(inheritedWork.WorkItemID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET body='Corrected background evidence' WHERE message_id=?`, source)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after := h.agentContext(inheritedWork.WorkItemID)
	if after.Revision <= before.Revision || !after.Facts[0].NeedsReview {
		t.Fatal(before, after)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := store.WorkItemContextReady(t.Context(), tx.Q(), inheritedWork.WorkItemID)
		if ready {
			t.Error("inherited source change did not fence dispatch")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalReviewClaimedRepairStartsAfterOwnerStop(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, turn.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != "conversation.repair_turn" {
		t.Fatal(d, ok, err)
	}
	op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: id}, nil), InterruptReply{})
	if op.Status == protocol.OpFailed {
		t.Fatal(op)
	}
	state, _ := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if state.Status != "stopped" {
		t.Fatal(state)
	}
	err = h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: mainAgent.Thread, Status: store.TurnStarting})
	if err == nil {
		t.Fatal("already-claimed repair admitted a new native execution after owner stop")
	}
}

func TestLogicalReviewHandoffSnapshotOmitsLogicalRepair(t *testing.T) {
	h := newHarness(t)
	_, turn := startReviewTurn(t, h)
	if err := h.app.SetMainThread(t.Context(), "main", mainAgent.Thread); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, turn.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	state, _ := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if state.Status != "recovering" {
		t.Fatal(state)
	}
	snap, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Pending && !snap.Active {
		t.Fatal("handoff snapshot declares idle with an unfinished logical turn and queued repair")
	}
}

func TestLogicalReviewStoppedRepairPublishesMessageState(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, turn.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := h.count("events")
	op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: id}, nil), InterruptReply{})
	if op.Status == protocol.OpFailed {
		t.Fatal(op)
	}
	if h.count("events") == before {
		t.Fatal("stopping pending repair mutated responsibility and outbox without any durable event for SSE clients")
	}
}
