package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBackgroundKeepsInterleavedOrder(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	_, turn := h.startBackground()
	firstAt := h.clock.Now().UnixMilli()
	h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "before tool", ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "text-first"}, nil), RecordAgentMessage{})
	h.clock.Advance(time.Second)
	h.exec(mainAgent, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "tool-second", Tool: "tasks_list", Status: protocol.ToolCompleted}, nil), RecordToolUse{})
	h.clock.Advance(time.Second)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := store.TranscriptReplies(t.Context(), tx.Q(), turn.MessageID, 0, 50)
		if err != nil {
			return err
		}

		if len(rows) != 2 || rows[0].Body != "before tool" {
			t.Errorf("reply order no longer matches observed arrival")
		}
		if len(rows) == 2 && (rows[0].CreatedAt != firstAt || rows[1].CreatedAt != firstAt+1000) {
			t.Errorf("arrival times changed: %+v", rows)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestBackgroundCancelledSourceDoesNotInheritCheckin(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r1 := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	if op := h.controlFollowup(r1, "cancel", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	d, turn := h.startBackground()
	if op := h.backgroundCall(d, turn, "sources", map[string]any{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "checked only uncancelled source"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	o := h.occurrences(r1.RuleID)[0]

	if o.Checkin != nil {
		t.Error("cancelled source was attributed the surviving source's checkin")
	}
}
func TestBackgroundSettlementRechecksSourceEventReviews(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var items []protocol.WorkItem
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		items, err = store.ListTaskIssues(t.Context(), tx.Q(), taskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, w := range items {
		var reviews []protocol.WorkItemCompletionReview
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			reviews, err = store.ListPendingCompletionReviews(t.Context(), tx.Q(), w.WorkItemID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var acks []protocol.CompletionReviewAcknowledgement
		for _, r := range reviews {
			acks = append(acks, protocol.CompletionReviewAcknowledgement{EventID: r.EventID, Reason: "Reviewed accepted facts; responsibility continues"})
		}
		if op := h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan, CompletionReviews: acks}, rev(w.Revision)), UpdateIssue{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	d, turn := h.startBackground()
	if op := h.backgroundCall(d, turn, "sources", map[string]any{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "All known completion reviews settled"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	plan := workPlan()
	plan.Stages[0].Status = "active"
	plan.Stages[0].TaskIDs = []string{taskID}
	h.createWork(plan)
	if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "Rechecked after new linked responsibility"}); op.Status != protocol.OpFailed {
		t.Fatal("new review did not block check-in", op)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	b := h.backgroundPage(turn.MessageID, 0).Batch
	if b.Checkin != nil {
		t.Errorf("settlement preserved stale check-in despite the now-pending source event review: %+v", b)
	}
}
func TestBackgroundAcceptsEmptyAuthoritativeCompletion(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	_, turn := h.startBackground()
	for _, p := range []recordAgentMessagePayload{
		{Body: "draft later removed", ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "reply", Status: protocol.AssistantStreaming},
		{Body: "", ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "reply", Status: protocol.AssistantCompleted},
	} {
		if op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, p, nil), RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Errorf("valid empty completed item prevents turn settlement: %v", err)
	}
	stored, _, err := h.app.MainTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != store.TurnCompleted || h.backgroundPage(turn.MessageID, 0).Batch.Status != "completed" {
		t.Fatal("empty completed body blocked settlement")
	}

}

func TestBackgroundInterruptedToolAcceptsLateResultWithoutReordering(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	_, turn := h.startBackground()
	record := func(status protocol.ToolStatus, output string) {
		op := h.exec(mainAgent, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "tool", Tool: "tasks_list", Status: status, Output: output}, nil), RecordToolUse{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	record(protocol.ToolRunning, "")
	turn.Status = store.TurnInterrupted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	before := h.count("conversation_messages")
	record(protocol.ToolCompleted, "retained late result")
	record(protocol.ToolRunning, "stale start")
	if h.count("conversation_messages") != before {
		t.Fatal("late tool duplicated its transcript item")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		u, err := store.GetToolUse(t.Context(), tx.Q(), turn.TurnID, "tool")
		if err == nil && (u.Status != string(protocol.ToolCompleted) || u.Output != "retained late result") {
			t.Fatal(u)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundCompletionDoesNotCertifyUnfinishedText(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	_, turn := h.startBackground()
	h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "partial text", ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "reply", Status: protocol.AssistantStreaming}, nil), RecordAgentMessage{})
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		r, err := store.GetAgentMessage(t.Context(), tx.Q(), mainAgent.Thread, turn.TurnID, "reply")
		if err == nil && r.Status != string(protocol.AssistantUnknown) {
			t.Fatal(r)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundUnclaimedCancellationStaysOutOfTranscript(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	if op := h.controlFollowup(r, "cancel", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		blocks, err := store.TranscriptBlocks(t.Context(), tx.Q(), 0, 20)
		if err == nil && len(blocks) != 0 {
			t.Fatalf("unclaimed cancellation added transcript blocks: %+v", blocks)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if o := h.occurrences(r.RuleID)[0]; o.Status != "cancelled" || o.Checkin != nil {
		t.Fatal("cancellation history lost", o)
	}
}
