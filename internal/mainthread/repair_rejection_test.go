package mainthread

import (
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRepairRejectionExhaustsBoundedRecovery(t *testing.T) {
	f := newHandoffFixture(t)
	id := f.e.say("A message requiring metadata repair")
	d, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: f.source.ID(), TurnID: "original", Status: store.TurnCompleted}
	if err := f.e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.FinishDelivery(t.Context(), d, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	f.e.m.mu.Lock()
	f.e.m.session, f.e.m.thread = f.session, f.source
	close(f.e.m.ready)
	f.e.m.mu.Unlock()
	attempts := 0
	f.e.h.StartTurnErr = func(harness.TurnInput) error { attempts++; return harness.ErrRejected }
	for i := 0; i < 5; i++ {
		if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE outbox SET next_attempt_at=0 WHERE status='pending'`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		repair, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
		if err != nil || !ok {
			t.Fatal(err, ok)
		}
		result := f.e.m.Deliver(t.Context(), repair)
		if err := f.e.app.FinishDelivery(t.Context(), repair, result); err != nil {
			t.Fatal(err)
		}
	}
	state, err := f.e.app.LogicalTurn(t.Context(), d.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d repair start attempts, persisted repair count=%d, status=%s", attempts, state.RepairAttempts, state.Status)
	if attempts != 5 || state.RepairAttempts != 5 || state.Status != "needs_review" {
		t.Fatal("definite native rejection bypasses bounded repair budget")
	}
}
