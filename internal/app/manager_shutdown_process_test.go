package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestFault_ManagerProcessTextSurvivesArchiveOutage(t *testing.T) {
	for _, separate := range []bool{false, true} {
		name := "terminal attachment"
		if separate {
			name = "shutdown record"
		}
		t.Run(name, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			ctx := t.Context()
			path, err := h.store.FileStore().Path(strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(filepath.Dir(filepath.Dir(path)), "archives")
			if err := os.Rename(archive, archive+".saved"); err == nil {
				t.Cleanup(func() { _ = os.Rename(archive+".saved", archive) })
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.WriteFile(archive, []byte("archive unavailable"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(archive) })

			const terminal = `"thread_id":"thread","native":{"Kind":"turn_completed","TurnID":"turn","TurnStatus":"interrupted"}`
			fragment := strings.Repeat("observed partial text ", 160)
			diagnostic := `"process_text":[{"thread_id":"thread","turn_id":"turn","item_id":"partial","text":"` + fragment + `"}]`
			firstBody := "{" + terminal + "," + diagnostic + "}"
			last := int64(1)
			if separate {
				firstBody = `{"sequence":0,"thread_id":"",` + diagnostic + "}"
				last = 2
			}
			const opName = protocol.OpManagerNativeEvent
			cmd := ManagerNativeCommand{OperationName: opName}
			event := func(seq int64, body string) *protocol.Envelope {
				return h.env(opName, runTarget(as.RunID), protocol.ManagerNativeEvent{Sequence: seq, Body: json.RawMessage(body)}, nil)
			}
			principal := runPrincipal(as.RunID, as.Role)
			first := event(1, firstBody)
			for _, envelope := range []*protocol.Envelope{first, first, event(1, firstBody)} {
				op, err := h.app.Execute(ctx, principal, envelope, cmd)
				if err != nil || op.Status != protocol.OpSucceeded || !store.SemanticJSONEqual(string(op.Result), `{"ack":1}`) {
					t.Fatalf("original process event was not acknowledged: %+v %v", op, err)
				}
			}
			changed := event(1, strings.Replace(firstBody, fragment, "different text", 1))
			if op, err := h.app.Execute(ctx, principal, changed, cmd); protocol.CodeOf(err) != protocol.CodeConflictPayload {
				t.Fatalf("original sequence accepted different content: %+v %v", op, err)
			}
			if separate {
				op, err := h.app.Execute(ctx, principal, event(last, "{"+terminal+"}"), cmd)
				if err != nil || op.Status != protocol.OpSucceeded || !store.SemanticJSONEqual(string(op.Result), `{"ack":2}`) {
					t.Fatalf("subsequent terminal event did not retain its sequence: %+v %v", op, err)
				}
			}
			rows, err := h.app.ManagerNativeEvents(ctx, as.RunID, 0)
			if err != nil || len(rows) != int(last) || !store.SemanticJSONEqual(string(rows[0].Body), firstBody) {
				t.Fatalf("could not read original process text during archive outage: %+v %v", rows, err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				full, err := views.RunRollout(ctx, tx.Q(), as.RunID, 0, 0, true)
				if err != nil {
					return err
				}
				if len(full.Logs) != int(last) || !bytes.Contains(full.Logs[0].Body, []byte(fragment)) {
					t.Fatalf("full-screen fallback lost the original fragment: %+v", full.Logs)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			completeBody := `{"thread_id":"thread","native":{"Kind":"item_completed","TurnID":"turn","ItemID":"complete","ItemType":"agentMessage","Text":"` + fragment + `"}}`
			if op, err := h.app.Execute(ctx, principal, event(last+1, completeBody), cmd); err == nil || op.Status == protocol.OpSucceeded {
				t.Fatalf("formal item bypassed original archival: %+v %v", op, err)
			}
			before, err := h.app.ManagerRunReconciliation(ctx, as.TaskID, as.RunID)
			if err != nil || before.EventLastSequence != last || before.UnappliedEvents != 1 || h.lease(as.RunID).Status != store.LeaseActive {
				t.Fatalf("diagnostics changed pending terminal event or lease: %+v %v", before, err)
			}
			if err := h.app.AcknowledgeManagerEvent(ctx, as.RunID, last, "thread", "turn", "", true); err != nil {
				t.Fatal(err)
			}
			stopped := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{
				Reason: "isolated native process stopped", LastSequence: last,
			}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
			if stopped.Status != protocol.OpSucceeded || h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatalf("original stop receipt did not settle its lease: %+v", stopped)
			}
		})
	}
}

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
