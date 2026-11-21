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
	for _, tc := range []struct {
		name        string
		version     int64
		maintenance bool
		blocked     bool
	}{
		{name: "delegated Run remains current after old deadline", version: protocol.CurrentRunnerProtocol},
		{name: "legacy lease expires", version: protocol.MinimumRunnerProtocol, blocked: true},
		{name: "delegated Run is fenced at explicit maintenance deadline", version: protocol.CurrentRunnerProtocol, maintenance: true, blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t, tc.version)
			if as.Protocol != tc.version {
				t.Fatal("unexpected negotiated protocol", as.Protocol)
			}
			const seq = 1
			event := protocol.ManagerNativeEvent{Sequence: seq, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"item_started","TurnID":"turn","ItemID":"reply"}}`)}
			if op := h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			lease := h.lease(as.RunID)
			if tc.maintenance {
				deadline := lease.ExpiresAt + 1
				control := protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: 1, Authorization: &protocol.MaintenanceAuthorization{
					ID: "manager-ack-maintenance", DeadlineMillis: deadline, RunIDs: []string{as.RunID},
				}}
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					return tx.PutHostControl(t.Context(), lease.HostID, control, h.clock.Now().UnixMilli())
				}); err != nil {
					t.Fatal(err)
				}
			}
			h.clock.Advance(time.UnixMilli(lease.ExpiresAt).Sub(h.clock.Now()) + time.Second)
			lease = h.lease(as.RunID)
			if lease.Current(h.clock.Now().UnixMilli()) == tc.blocked || lease.MaintenanceExpired(h.clock.Now().UnixMilli()) != tc.maintenance {
				t.Fatal("fixture did not reach the expected execution boundary", lease)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			held := make(chan error, 1)
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			missed := make(chan store.WriteObservation, 1)
			store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
				if o.Source.Operation == "manager.event.ack" && o.Outcome == "deadline_exceeded" {
					select {
					case missed <- o:
					default:
					}
				}
			})(h.store)
			go func() {
				held <- h.store.Write(t.Context(), func(*store.Tx) error {
					close(entered)
					<-release
					return nil
				})
			}()
			<-entered
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
			if tc.blocked {
				want = 0
				if protocol.CodeOf(err) != protocol.CodeForbidden {
					t.Fatal("retry bypassed the lease fence", err)
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

func TestFault_LiveManagerACKSettlesAfterCloseTransition(t *testing.T) {
	for _, transition := range []string{"physical stop", "close in flight", "Issue completed"} {
		t.Run(transition, func(t *testing.T) {
			h, _, issue, as, _ := reviewManagerAssignment(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			event := protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"item_started","TurnID":"turn","ItemID":"reply"}}`)}
			if op := h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error { return h.app.requireManagerEventACKLease(ctx, tx.Q(), as.RunID) }); err != nil {
				t.Fatal("not originally live", err)
			}
			release, entered := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			held := make(chan error, 1)
			go func() {
				held <- h.store.Write(ctx, func(*store.Tx) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			<-entered
			missed, transitioned := make(chan struct{}, 1), make(chan struct{})
			finishTransition := sync.OnceFunc(func() { close(transitioned) })
			defer finishTransition()
			store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
				if o.Source.Operation == "manager.event.ack" && o.Source.RunID == as.RunID && o.Source.EventSequence == 1 && o.Outcome == "deadline_exceeded" && o.Stage == "connection_wait" {
					select {
					case missed <- struct{}{}:
					default:
					}
					select {
					case <-transitioned:
					case <-ctx.Done():
					}
				}
			})(h.store)
			done := make(chan error, 1)
			go func() { done <- h.app.RecoverManagerEventACK(ctx, as.RunID, 1, "native", "turn", "reply", false) }()
			select {
			case <-missed:
			case err := <-done:
				t.Fatal("did not wait", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			unblock()
			if err := <-held; err != nil {
				t.Fatal(err)
			}
			switch transition {
			case "physical stop":
				op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original native process stopped", LastSequence: 1}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "close in flight":
				if _, err := h.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`)); err != nil {
					t.Fatal(err)
				}
			case "Issue completed":
				issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "set_status", protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueDone}))
				if issue.State != protocol.IssueClosed {
					t.Fatal(issue)
				}
				if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
					run, err := store.GetRun(ctx, tx.Q(), as.RunID)
					if err != nil {
						return err
					}
					allowed, err := managerNativeBusinessAllowed(ctx, tx.Q(), run)
					if allowed {
						t.Error("the original Run can still initiate business after Issue completion")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			finishTransition()
			err := <-done
			cursor, readErr := h.app.ManagerAppliedCursor(ctx, as.RunID)
			if err != nil || readErr != nil || cursor != 1 {
				t.Errorf("already handled original receipt failed after %s: err=%v cursor=%d readErr=%v", transition, err, cursor, readErr)
			}
		})
	}
}
