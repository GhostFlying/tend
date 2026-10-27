package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestReviewStaleAssessmentWakesMainOnce(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	id := "work-item:" + w.WorkItemID
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: id, Body: "inspect"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(id))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var msg HandleMessage
	_ = json.Unmarshal(d.Payload, &msg)
	if _, err = h.app.AgentContext(t.Context(), id, d.RequestID); err != nil {
		t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: msg.MessageID, ThreadID: "bg-thread", TurnID: "bg-turn", Status: store.TurnStarted}
	if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	revision := h.agentContext(w.WorkItemID).Revision
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=?`, w.WorkItemID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	count := h.count("outbox")
	bg := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "background", Thread: turn.ThreadID, ConversationID: id}
	result := h.exec(bg, h.env("background.submit", protocol.Target{Type: "conversation", ID: id}, protocol.BackgroundAssessmentPayload{ContextRevision: revision, Disposition: "waiting", Note: "No change"}, nil), SubmitBackgroundAssessment{MessageID: msg.MessageID, TurnID: turn.TurnID, DeliveryID: d.RequestID})
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	t.Logf("after stale assessment: main returns=%d", h.count("outbox")-count)
	turn.Status = store.TurnCompleted
	if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	t.Logf("after turn completion: main returns=%d", h.count("outbox")-count)
	if h.count("outbox") != count+1 {
		t.Error("stale result generated duplicate main coordination turns")
	}
}

func TestReviewBackgroundActivityDoesNotInvalidateMainSummary(t *testing.T) {
	h := newHarness(t)
	if _, err := h.app.BindMainThread(t.Context(), "main", "main-native", "hash", ""); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,created_at) VALUES('bg-only',0)`); err != nil {
			return err
		}
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{ConversationID: "bg-only", MessageID: "bg-only-message", Author: "owner", Body: "background only"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("main summary snapshot version=%d -> %d maxseq=%d -> %d", before.Version, after.Version, before.MaxSeq, after.MaxSeq)
	if !before.Covers(after) {
		t.Error("background-only message invalidated main summary coverage")
	}
}
