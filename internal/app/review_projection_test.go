package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestCorrectionDoesNotHideStoppedResponsibility(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	source, first := startReviewTurn(t, h)
	if op := h.associate(mainAgent, source, w.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	stop := InterruptReply{}
	op := h.exec(owner, h.env(stop.Operation(), protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: source}, nil), stop)
	if op.Status == protocol.OpFailed {
		t.Fatal(op)
	}
	first.Status = store.TurnInterrupted
	if err := h.app.PutMainTurn(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	for _, d := range projectionInFlight(t, h) {
		if d.RequestID == first.RequestID {
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A normal owner association correction queues review of that same original.
	if op := h.associate(owner, source, w.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	review := startAnswerReview(t, h, source)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, review.RequestID)
	review.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), review); err != nil {
		t.Fatal(err)
	}
	original, err := h.app.LogicalTurn(t.Context(), first.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	a := h.associations(source)
	var ready bool
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		ready, err = store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if original.Status == "stopped" && !ready && a.Processing.Status == "completed" {
		t.Fatalf("unfinished turn %s still fences dispatch, but the original message now exposes only completed turn %s", original.ID, a.Processing.LogicalTurnID)
	}
}

func projectionInFlight(t *testing.T, h *harness) []Delivery {
	t.Helper()
	ds, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func TestMetadataRepairDoesNotEraseBusinessFailure(t *testing.T) {
	for _, status := range []string{store.TurnFailed, store.TurnInterrupted} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			source, original := startReviewTurn(t, h)
			original.Status = status
			if err := h.app.PutMainTurn(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			for _, d := range projectionInFlight(t, h) {
				if d.RequestID == original.RequestID {
					if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
						t.Fatal(err)
					}
				}
			}
			repair, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			running := store.MainTurn{RequestID: repair.RequestID, MessageID: source, ThreadID: mainAgent.Thread, TurnID: "repair", Status: store.TurnStarted}
			if err := h.app.PutMainTurn(t.Context(), running); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				item, err := store.GetConversationItem(t.Context(), tx.Q(), source)
				if err == nil && item.State != status {
					t.Fatalf("repair start hid ordinary outcome: %s", item.State)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Restricted repair only classifies the message; it does not answer the owner.
			if op := h.associate(mainAgent, source); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			commitFixtureTurn(t, h, repair.RequestID)
			rec := store.MainTurn{RequestID: repair.RequestID, MessageID: source, ThreadID: mainAgent.Thread, TurnID: "repair", Status: store.TurnCompleted}
			if err := h.app.SettleOpenTurns(t.Context(), mainAgent.Thread); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				item, err := store.GetConversationItem(t.Context(), tx.Q(), source)
				if err == nil && item.State != status {
					t.Fatalf("repair crash hid ordinary outcome: %s", item.State)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			rec.NativeReconciled = true
			if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				item, err := store.GetConversationItem(t.Context(), tx.Q(), source)
				if err != nil {
					return err
				}
				var replies int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM conversation_messages WHERE author='main_agent' AND reply_to=?`, source).Scan(&replies); err != nil {
					return err
				}
				if item.State != status {
					t.Fatalf("original native execution failed with %d replies, but metadata-only repair projects owner message as %s", replies, item.State)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

		})
	}
}
