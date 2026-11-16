package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_ManagerEventACKDeadlineKeepsOriginalEventForNormalSettlement(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	var seq int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(MAX(seq),0)+1 FROM manager_native_events WHERE run_id=?`, as.RunID).Scan(&seq)
	}); err != nil {
		t.Fatal(err)
	}
	event := protocol.ManagerNativeEvent{Sequence: seq, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"item_completed","TurnID":"turn","ItemID":"reply","Text":"settled"}}`)}
	if op := h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	go func() {
		done <- h.store.Write(t.Context(), func(*store.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		unblock()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	err := h.app.AcknowledgeManagerEvent(ctx, as.RunID, seq, "native", "turn", "reply", true)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued original ACK = %v", err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq=? AND applied=0`, as.RunID, seq) != 1 {
		t.Fatal("deadline discarded or acknowledged the original event")
	}
	unblock()
	for range 2 {
		if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, seq, "native", "turn", "reply", true); err != nil {
			t.Fatal(err)
		}
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq=? AND applied=1`, as.RunID, seq) != 1 {
		t.Fatal("normal same-identity settlement lost or duplicated the event")
	}
}
