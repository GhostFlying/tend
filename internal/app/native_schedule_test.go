package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNativeTextOnlyWakesSchedulingWhenClosing(t *testing.T) {
	h, _, _, assignment, _ := reviewManagerAssignment(t)
	var last int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(MAX(seq),0) FROM manager_native_events WHERE run_id=?`, assignment.RunID).Scan(&last)
	}); err != nil {
		t.Fatal(err)
	}
	appendText := func(sequence int64) {
		t.Helper()
		op := h.runAs(assignment, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{
			Sequence: sequence,
			Body:     json.RawMessage(`{"thread_id":"native","native":{"Kind":"agent_message_delta","TurnID":"turn","ItemID":"reply","Text":"x"}}`),
		}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
		if op.Error != nil {
			t.Fatal(op)
		}
	}
	for i := int64(1); i <= 100; i++ {
		h.clock.Advance(600 * time.Millisecond)
		wake, visible := h.app.SchedulingChanges().Wait(), h.app.Changes().Wait()
		appendText(last + i)
		select {
		case <-wake:
			t.Fatal("native text woke full background eligibility scan")
		default:
		}
		select {
		case <-visible:
		default:
			t.Fatal("native reader did not receive its event wake")
		}
	}
	if _, err := h.app.QueueManagerNative(t.Context(), assignment.RunID, "close", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	wake := h.app.SchedulingChanges().Wait()
	appendText(last + 101)
	select {
	case <-wake:
	default:
		t.Fatal("new closing event did not wake its receipt attachment")
	}
}
