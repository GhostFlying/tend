package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
	"time"
)

func TestReviewCancelledFollowupMustNotRemainDeliverable(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	r := h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 300, ExpiresAt: h.clock.Now().Add(24 * time.Hour).UnixMilli()})
	h.clock.Advance(5 * time.Minute)
	h.sweepFollowups()
	r = h.readFollowup(r.RuleID)
	op := h.controlFollowup(r, "pause", nil)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination("work-item:"+w.WorkItemID))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("cancelled followup still deliverable: %+v", d)
	}
}

func TestReviewAssociationEditMustInvalidateCachedSummary(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	source := "source-message"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, Author: "main_agent", Body: "Work item association", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.associate(mainAgent, source, w.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.BindMainThread(t.Context(), "main", "summary-thread", "tool-hash", ""); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := h.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: "main", Snapshot: before, Body: "This source belongs to the work item", FormatVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if op := h.associate(mainAgent, source); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Snapshot.Covers(after) {
		t.Fatalf("obsolete summary still current at conversation version %d", after.Version)
	}
}

func TestReviewBackgroundUnknownDeliveryMustEscalate(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	op := h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	id := "work-item:" + w.WorkItemID
	op = h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: id, Body: "Please inspect"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(id))
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	var p HandleMessage
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AgentContext(t.Context(), id, d.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: p.MessageID, ThreadID: "background-thread", Status: store.TurnStarting}); err != nil {
		t.Fatal(err)
	}
	before := h.count("outbox")
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown, Reason: "turn/start lost"}); err != nil {
		t.Fatal(err)
	}
	if h.count("outbox") != before+1 {
		t.Fatalf("unknown background turn produced no main escalation: outbox count %d -> %d", before, h.count("outbox"))
	}
}

func TestReviewSummaryObservationMustIncludeCorrectedAssociations(t *testing.T) {
	h := newHarness(t)
	w1, w2 := h.createWork(workPlan()), h.createWork(workPlan())
	const source = "summary-source"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, Author: "owner", Body: "Proceed with that item", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.associate(mainAgent, source, w1.WorkItemID)
	if _, err := h.app.BindMainThread(t.Context(), "main", "summary-thread", "hash", ""); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: "main", Snapshot: before, Body: "Source refers to first work item", FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	h.associate(owner, source, w2.WorkItemID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE destination='main_thread'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input, err := h.app.HandoffSummaryInput(t.Context(), "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(input.Observations, &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item["message_id"] == source {
			if item["associations"] == nil {
				t.Fatalf("source replacement omits corrected associations: %s", input.Observations)
			}
			return
		}
	}
	t.Fatal("corrected source not in summary observations")
}
