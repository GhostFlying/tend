package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestPartialCommitRetainsSourceWithoutAnotherModelTurn(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	old, first := startReviewTurn(t, h)
	h.associate(mainAgent, old)
	commitFixtureTurn(t, h, first.RequestID)
	first.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, first.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	current, d := h.say("A follow-up using the original message")
	second := store.MainTurn{RequestID: d.RequestID, MessageID: current, ThreadID: mainAgent.Thread, TurnID: "native-2", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	h.associate(mainAgent, current)
	c := h.agentContext(w.WorkItemID)
	env := h.env("work_item_context.fact", workTarget(w.WorkItemID), protocol.ContextFact{Scope: "global", Kind: "constraint", Body: "Reused original as a sourced constraint", SourceMessageID: old}, rev(c.Revision))
	env.ParentRequestID = second.RequestID
	op := h.exec(mainAgent, env, ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.prepareAgent(w.WorkItemID)
	state, err := h.app.LogicalTurn(t.Context(), second.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	a := h.associations(current)
	cmd := CommitLogicalTurn{RequestID: second.RequestID}
	op = h.exec(mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "logical_turn", ID: second.RequestID}, protocol.LogicalTurnCommit{Messages: []protocol.LogicalTurnReview{{MessageID: current, ContentRevision: a.ContentRevision, AssociationRevision: a.Revision}}, Contexts: state.Contexts}, nil), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	second.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	state, err = h.app.LogicalTurn(t.Context(), second.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	var ready bool
	var pending int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		ready, err = store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		if err != nil {
			return err
		}
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM message_context_obligations WHERE work_item_id=? AND needs_review=1`, w.WorkItemID).Scan(&pending)
	}); err != nil {
		t.Fatal(err)
	}
	if state.Status != "completed" || state.RepairAttempts != 0 || !ready || pending != 1 {
		t.Fatalf("source interpretation became a completion gate or was erased: %+v ready=%v pending=%d", state, ready, pending)
	}
	var repairs int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM logical_turn_executions WHERE logical_turn_id=? AND repair=1`, state.ID).Scan(&repairs)
	}); err != nil || repairs != 0 {
		t.Fatalf("bookkeeping created another execution: %d %v", repairs, err)
	}
	if len(state.Processing) != 2 {
		t.Fatalf("exact admitted source positions were lost: %+v", state.Processing)
	}
	for _, receipt := range state.Processing {
		if receipt.ProcessedAtMillis == 0 {
			t.Fatalf("terminal native execution has no processing receipt: %+v", receipt)
		}
	}
}
