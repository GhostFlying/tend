package mainthread

import (
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestCompletedTurnDoesNotScheduleMetadataRecoveryAttempts(t *testing.T) {
	f := newHandoffFixture(t)
	id := f.e.say("A completed message without a separate source-review ceremony")
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
	attempts := 0
	f.e.h.StartTurnErr = func(harness.TurnInput) error { attempts++; return harness.ErrRejected }
	if delivery, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination); err != nil || ok {
		t.Fatal("completion created a model repair delivery", delivery, ok, err)
	}
	state, err := f.e.app.LogicalTurn(t.Context(), d.RequestID)
	if err != nil || state.Status != "completed" || state.RepairAttempts != 0 || attempts != 0 {
		t.Fatal("source metadata manufactured recovery work", state, err, attempts)
	}
	if f.e.count(`SELECT COUNT(*) FROM logical_turn_executions WHERE repair=1`) != 0 {
		t.Fatal("metadata-only execution created")
	}
}
