package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestWorkerShutdownProcessTextKeepsOriginalBusinessPending(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	ctx := t.Context()
	command := ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}
	turn, err := h.app.QueueManagerNative(ctx, as.RunID, "turn", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claim := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var admitted protocol.ManagerNativeClaimResult
	if claim.Status != protocol.OpSucceeded || json.Unmarshal(claim.Result, &admitted) != nil || admitted.ID != turn || !admitted.AcceptsShutdownProcessText {
		t.Fatal("original turn was not claimed", claim)
	}
	tool := protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"thread_id":"thread","call":{"ThreadID":"thread","TurnID":"turn","CallID":"tool"}}`)}
	if op := h.runAs(as, command.Operation(), tool, command); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before, err := h.app.ManagerRunReconciliation(ctx, as.TaskID, as.RunID)
	if err != nil || before.UnappliedEvents != 1 || before.UnresolvedNativeRequests != 1 {
		t.Fatalf("fixture needs pending original work: %+v %v", before, err)
	}
	lease := h.lease(as.RunID)
	const fragment = "observed before Runner shutdown"
	snapshot := protocol.ManagerNativeEvent{Sequence: 2, Body: json.RawMessage(`{"sequence":0,"thread_id":"","process_text":[{"thread_id":"thread","turn_id":"turn","item_id":"partial","text":"` + fragment + `"}]}`)}
	for range 2 {
		if op := h.runAs(as, command.Operation(), snapshot, command); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	facts, err := h.app.ManagerRunReconciliation(ctx, as.TaskID, as.RunID)
	if err != nil || facts.EventLastSequence != 2 || facts.UnappliedEvents != 1 || facts.UnresolvedNativeRequests != 1 ||
		facts.Observed != before.Observed || facts.CloseRequestID != before.CloseRequestID || facts.PendingBusinessReceipts != before.PendingBusinessReceipts {
		t.Fatalf("diagnostic changed business or close facts: %+v %v", facts, err)
	}
	if current := h.lease(as.RunID); current.LeaseID != lease.LeaseID || current.Status != lease.Status {
		t.Fatal("diagnostic changed the original lease", current)
	}
	rows, err := h.app.ManagerReplayEvents(ctx, as.RunID, 0)
	if err != nil || len(rows) != 1 || rows[0].Sequence != 1 {
		t.Fatalf("diagnostic became a replayed native business event: %+v %v", rows, err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		ordinary, err := views.RunRollout(ctx, tx.Q(), as.RunID, 0, 0, false)
		if err != nil {
			return err
		}
		monitor, err := views.RunRollout(ctx, tx.Q(), as.RunID, 0, 0, true)
		if err != nil {
			return err
		}
		if len(ordinary.Logs) != 0 || len(monitor.Logs) != 2 || monitor.Logs[1].Seq != 2 ||
			!bytes.Contains(monitor.Logs[1].Body, []byte(fragment)) || len(monitor.CloseProcessText) != 0 {
			t.Fatalf("diagnostic missing from the original process window: %+v %+v", ordinary, monitor)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
