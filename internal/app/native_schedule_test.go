package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNativeProgressOnlyWakesSchedulingWhenClosing(t *testing.T) {
	h, _, _, assignment, _ := reviewManagerAssignment(t)
	var last int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(MAX(seq),0) FROM manager_native_events WHERE run_id=?`, assignment.RunID).Scan(&last)
	}); err != nil {
		t.Fatal(err)
	}
	appendText := func(sequence int64, kind string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"thread_id": "native", "native": map[string]any{"Kind": kind, "TurnID": "turn", "ItemID": "reply", "Text": "x"}})
		op := h.runAs(assignment, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{
			Sequence: sequence,
			Body:     body,
		}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
		if op.Error != nil {
			t.Fatal(op)
		}
	}
	for i := int64(1); i <= 100; i++ {
		h.clock.Advance(600 * time.Millisecond)
		wake, visible := h.app.SchedulingChanges().Wait(), h.app.Changes().Wait()
		appendText(last+i, []string{"agent_message_delta", "item_started", "item_completed", "context_usage", "request_usage", "child_rollout"}[i%6])
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
	appendText(last+101, "item_completed")
	select {
	case <-wake:
	default:
		t.Fatal("new closing event did not wake its receipt attachment")
	}
}

func TestManagerEventACKOnlyWakesSchedulingForClosingOrTerminalReceipt(t *testing.T) {
	h, _, _, assignment, _ := reviewManagerAssignment(t)
	var sequence int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(MAX(seq),0) FROM manager_native_events WHERE run_id=?`, assignment.RunID).Scan(&sequence)
	}); err != nil {
		t.Fatal(err)
	}
	appendEvent := func() {
		t.Helper()
		sequence++
		op := h.runAs(assignment, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: sequence, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"agent_message_delta","TurnID":"turn","ItemID":"reply","Text":"x"}}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
		if op.Error != nil {
			t.Fatal(op)
		}
	}
	ack := func(wantChange, wantScheduling bool) {
		t.Helper()
		visible, scheduling := h.app.Changes().Wait(), h.app.SchedulingChanges().Wait()
		if err := h.app.AcknowledgeManagerEvent(t.Context(), assignment.RunID, sequence, "native", "turn", "reply", false); err != nil {
			t.Fatal(err)
		}
		select {
		case <-visible:
			if !wantChange {
				t.Fatal("repeated ACK woke readers")
			}
		default:
			if wantChange {
				t.Fatal("actual ACK did not wake readers")
			}
		}
		select {
		case <-scheduling:
			if !wantScheduling {
				t.Fatal("ordinary or repeated ACK reran scheduler")
			}
		default:
			if wantScheduling {
				t.Fatal("closing ACK did not wake receipt scheduling")
			}
		}
	}
	appendEvent()
	ack(true, false)
	ack(false, false)
	if _, err := h.app.QueueManagerNative(t.Context(), assignment.RunID, "close", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	appendEvent()
	ack(true, true)
	ack(false, false)
}
