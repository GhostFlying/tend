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

func TestFault_LiveManagerACKRecoversOnlyUnderOriginalLease(t *testing.T) {
	for _, expire := range []bool{false, true} {
		name := "recovers"
		if expire {
			name = "lease expired"
		}
		t.Run(name, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			const seq = 1
			event := protocol.ManagerNativeEvent{Sequence: seq, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"item_started","TurnID":"turn","ItemID":"reply"}}`)}
			if op := h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			held := make(chan error, 1)
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			go func() {
				held <- h.store.Write(t.Context(), func(tx *store.Tx) error {
					close(entered)
					<-release
					if expire {
						_, err := tx.Exec(t.Context(), `UPDATE leases SET expires_at=0 WHERE run_id=?`, as.RunID)
						return err
					}
					return nil
				})
			}()
			<-entered
			missed := make(chan store.WriteObservation, 1)
			store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
				if o.Source.Operation == "manager.event.ack" && o.Outcome == "deadline_exceeded" {
					select {
					case missed <- o:
					default:
					}
				}
			})(h.store)
			done := make(chan error, 1)
			go func() {
				done <- h.app.RecoverManagerEventACK(t.Context(), as.RunID, seq, "native", "", "", false)
			}()
			select {
			case o := <-missed:
				if o.Stage != "connection_wait" || o.Source.RunID != as.RunID || o.Source.EventSequence != seq {
					t.Error("retry lost original pre-transaction identity", o)
				}
			case err := <-done:
				t.Fatal("ACK returned before writer contention was observed", err)
			case <-time.After(30 * time.Second):
				t.Fatal("writer contention was not observed")
			}
			unblock()
			if err := <-held; err != nil {
				t.Fatal(err)
			}
			err := <-done
			want := int64(1)
			if expire {
				want = 0
				if protocol.CodeOf(err) != protocol.CodeForbidden {
					t.Fatal("retry bypassed lease expiry", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got, err := h.app.ManagerAppliedCursor(t.Context(), as.RunID); got != want || err != nil {
				t.Fatal("original applied cursor", got, err, "want", want)
			}
		})
	}
}
