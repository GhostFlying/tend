package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestResumeStoppedTurnWaitsForNativeReceipt(t *testing.T) {
	h := newHarness(t)
	id, rec := startReviewTurn(t, h)
	stop := InterruptReply{}
	op := h.exec(owner, h.env(stop.Operation(), protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: id}, nil), stop)
	if op.Status == protocol.OpFailed {
		t.Fatal(op)
	}
	if err := h.app.SettleOpenTurns(t.Context(), rec.ThreadID); err != nil {
		t.Fatal(err)
	}
	// Deliver gets the cleanup interruption and acknowledges its delivery, but
	// native history has not yet been reconciled.
	rec.Status = store.TurnInterrupted
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, rec.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a := h.associations(id)
	pending, err := h.app.NativeReceiptPending(t.Context(), rec.RequestID)
	if err != nil || !pending {
		t.Fatal(err, pending)
	}
	resume := ResumeLogicalTurn{}
	op = h.exec(owner, h.env(resume.Operation(), protocol.Target{Type: "logical_turn", ID: rec.RequestID}, struct{}{}, rev(a.Processing.Revision)), resume)
	state, _ := h.app.LogicalTurn(t.Context(), rec.RequestID)
	t.Logf("native receipt pending=%v; resume operation=%s; logical status=%s; repair attempts=%d", pending, op.Status, state.Status, state.RepairAttempts)
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition || state.Status != "stopped" || state.RepairAttempts != 0 {
		t.Fatal("resumed a stopped responsibility with an unresolved native execution", op, state)
	}
}
