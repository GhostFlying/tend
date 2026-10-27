package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestClarificationAdoptionRequiresReconciledNativeHistory(t *testing.T) {
	for _, phase := range []string{"before_adoption", "after_adoption"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			w := h.createWork(workPlan())
			h.prepareAgent(w.WorkItemID)
			source, first := startReviewTurn(t, h)
			h.associate(mainAgent, source, w.WorkItemID)
			wait := AwaitTurnClarification{RequestID: first.RequestID}
			op := h.exec(mainAgent, h.env(wait.Operation(), protocol.Target{Type: "logical_turn", ID: first.RequestID}, protocol.TurnClarification{Question: "Confirm scope"}, nil), wait)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
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
			markPending := func() {
				t.Helper()
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, first.RequestID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "before_adoption" {
				markPending()
			}
			current, second := startReviewTurn(t, h)
			second.TurnID = "native-successor"
			if err := h.app.PutMainTurn(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			adopt := func() protocol.Operation {
				state, err := h.app.LogicalTurn(t.Context(), second.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Awaiting) != 1 {
					t.Fatal(state)
				}
				cmd := AdoptClarification{RequestID: second.RequestID}
				return h.exec(mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "logical_turn", ID: first.RequestID}, protocol.AdoptClarificationPayload{}, rev(state.Awaiting[0].Revision)), cmd)
			}
			if phase == "before_adoption" {
				op = adopt()
				if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
					t.Fatal("adopted unresolved native history", op)
				}
				first.NativeReconciled = true
				if err := h.app.PutMainTurn(t.Context(), first); err != nil {
					t.Fatal(err)
				}
			}
			if op = adopt(); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			h.associate(mainAgent, current, w.WorkItemID)
			h.prepareAgent(w.WorkItemID)
			commitFixtureTurn(t, h, second.RequestID)
			if phase == "after_adoption" {
				markPending()
			}
			second.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			ready := func() bool {
				var ready bool
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					ready, err = store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return ready
			}
			if phase == "after_adoption" {
				state, err := h.app.LogicalTurn(t.Context(), second.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				if state.Status != "unknown" || ready() {
					t.Fatal("completed inherited uncertainty", state)
				}
				first.NativeReconciled = true
				if err := h.app.PutMainTurn(t.Context(), first); err != nil {
					t.Fatal(err)
				}
			}
			for _, req := range []string{first.RequestID, second.RequestID} {
				state, err := h.app.LogicalTurn(t.Context(), req)
				if err != nil || state.Status != "completed" || state.RepairAttempts != 0 {
					t.Fatalf("responsibility not completed exactly once: %+v %v", state, err)
				}
			}
			if !ready() {
				t.Fatal("reconciled context remains blocked")
			}
		})
	}
}

func TestAncestorReceiptPreservesSuccessorResponsibility(t *testing.T) {
	for _, status := range []string{"recovering", "awaiting_clarification", "stopped"} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			source, first := startReviewTurn(t, h)
			wait := AwaitTurnClarification{RequestID: first.RequestID}
			op := h.exec(mainAgent, h.env(wait.Operation(), protocol.Target{Type: "logical_turn", ID: first.RequestID}, protocol.TurnClarification{Question: "Confirm scope"}, nil), wait)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
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
			current, second := startReviewTurn(t, h)
			second.TurnID = "successor"
			if err := h.app.PutMainTurn(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			state, err := h.app.LogicalTurn(t.Context(), second.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			adopt := AdoptClarification{RequestID: second.RequestID}
			op = h.exec(mainAgent, h.env(adopt.Operation(), protocol.Target{Type: "logical_turn", ID: first.RequestID}, protocol.AdoptClarificationPayload{}, rev(state.Awaiting[0].Revision)), adopt)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if status != "recovering" {
				h.associate(mainAgent, source)
				h.associate(mainAgent, current)
				commitFixtureTurn(t, h, second.RequestID)
				if status == "awaiting_clarification" {
					wait = AwaitTurnClarification{RequestID: second.RequestID}
					op = h.exec(mainAgent, h.env(wait.Operation(), protocol.Target{Type: "logical_turn", ID: second.RequestID}, protocol.TurnClarification{Question: "Another explicit question"}, nil), wait)
				} else {
					stop := InterruptReply{}
					op = h.exec(owner, h.env(stop.Operation(), protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: current}, nil), stop)
				}
				if op.Status == protocol.OpFailed {
					t.Fatal(op)
				}
			}
			second.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			first.NativeReconciled = true
			for range 2 {
				if err := h.app.PutMainTurn(t.Context(), first); err != nil {
					t.Fatal(err)
				}
			}
			state, err = h.app.LogicalTurn(t.Context(), second.RequestID)
			wantAttempts := 0
			if status == "recovering" {
				wantAttempts = 1
			}
			if err != nil || state.Status != status || state.RepairAttempts != wantAttempts {
				t.Fatalf("receipt changed successor responsibility: %+v %v", state, err)
			}
			var count int
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM logical_turn_executions WHERE logical_turn_id=? AND repair=1`, state.ID).Scan(&count)
			}); err != nil || count != wantAttempts {
				t.Fatalf("repair count=%d: %v", count, err)
			}
		})
	}
}

func TestClarificationSurvivesUnknownDelivery(t *testing.T) {
	h := newHarness(t)
	_, rec := startReviewTurn(t, h)
	cmd := AwaitTurnClarification{RequestID: rec.RequestID}
	op := h.exec(mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "logical_turn", ID: rec.RequestID}, protocol.TurnClarification{Question: "Which work item should receive this message?"}, nil), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Restart settles open turns and retains the native receipt obligation.
	if err := h.app.SettleOpenTurns(t.Context(), rec.ThreadID); err != nil {
		t.Fatal(err)
	}
	deliveries, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deliveries {
		if d.RequestID == rec.RequestID {
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown, Reason: "Native execution history requires reconciliation"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	rec.Status = store.TurnCompleted
	rec.NativeReconciled = true
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "awaiting_clarification" || state.RepairAttempts != 0 {
		t.Fatalf("clarification responsibility changed across restart: status=%s attempts=%d reason=%s", state.Status, state.RepairAttempts, state.Reason)
	}
}
