package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestFault_ManagerPauseConsumesOriginalDisconnectBeforeSingleContinuation(t *testing.T) {
	const observedText = "observed fragment"
	const diagnostic = `,"process_text":[{"thread_id":"original-thread","turn_id":"original-turn","item_id":"unfinished-item","text":"` + observedText + `"}]`
	for _, tc := range []struct {
		name, earlierEvent, tailFields string
		nativeUnknown, blocked         bool
	}{
		{name: "sole transport end"},
		{name: "transport end with worker fragment", tailFields: diagnostic},
		{name: "earlier business event", tailFields: diagnostic, blocked: true, earlierEvent: `{"thread_id":"original-thread","native":{"Kind":"turn_completed","TurnID":"original-turn","TurnStatus":"completed"}}`},
		{name: "unknown original native request", tailFields: diagnostic, blocked: true, nativeUnknown: true},
		{name: "tool callback on the same event", tailFields: diagnostic + `,"call":{"ThreadID":"original-thread","TurnID":"original-turn","CallID":"original-call"}`, blocked: true},
		{name: "malformed diagnostic payload", tailFields: `,"process_text":{"thread_id":"original-thread"}`, blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, runtime := pendingIssueOwnerHarness(t)
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityPauseHandoffs)
			issue := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
			as := admissionClaim(t, h, runtime)
			if as == nil || as.Manager == nil || as.Execution == nil || as.Role != protocol.RoleIssueManager || !as.PauseHandoffs {
				t.Fatal("named owner was not claimed", as)
			}
			originalLease := h.lease(as.RunID)
			var request string
			if tc.nativeUnknown {
				op, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				request = op
				h.runAs(*as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			}
			issue = issueAdmissionStatus(t, h, admissionCurrent(t, h, issue.IssueID), protocol.IssueBacklog, nil)
			sendEvent := func(seq int64, body string) {
				t.Helper()
				op := h.runAs(*as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: seq, Body: json.RawMessage(body)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			seq := int64(1)
			if tc.earlierEvent != "" {
				sendEvent(seq, tc.earlierEvent)
				seq++
			}
			end := `{"sequence":0,"disconnected":true,"thread_id":""` + tc.tailFields + "}"
			sendEvent(seq, end)
			paused := h.run(as.RunID)
			env := h.env(protocol.OpRunsYieldPause, runTarget(as.RunID), protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}, nil)
			if op := h.exec(runPrincipal(as.RunID, as.Role), env, YieldPause{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			before, err := h.app.ManagerRunReconciliation(t.Context(), as.TaskID, as.RunID)
			if err != nil || before.UnappliedEvents != seq || before.Observed != protocol.RunFinished {
				t.Fatalf("fixture did not reach a stopped Run with a retained disconnect: %+v %v", before, err)
			}
			if got := admissionClaim(t, h, runtime); got != nil {
				t.Fatal("pause settlement admitted work while the Issue was in Backlog", got)
			}
			issue = issueAdmissionStatus(t, h, admissionCurrent(t, h, issue.IssueID), protocol.IssueTodo, nil)
			next := admissionClaim(t, h, runtime)
			if tc.blocked {
				if next != nil {
					t.Fatal("unreconciled business facts admitted a follow-up Run", next)
				}
				after, err := h.app.ManagerRunReconciliation(t.Context(), as.TaskID, as.RunID)
				if err != nil || after.UnappliedEvents != seq || tc.nativeUnknown && after.UnresolvedNativeRequests != 1 {
					t.Fatalf("unresolved receipts changed: %+v %v", after, err)
				}
			} else {
				if next == nil || next.RunID == as.RunID || next.LeaseID == as.LeaseID || next.TaskID != as.TaskID ||
					next.Execution == nil || *next.Execution != *as.Execution || next.Manager == nil ||
					next.Manager.AssignmentEpoch != as.Manager.AssignmentEpoch || next.Manager.AssignmentEpoch != issue.AssignmentEpoch ||
					next.PauseContinuation == nil || next.PauseContinuation.RunID != as.RunID {
					t.Fatal("continuation lost the original named responsibility", next)
				}
				after, err := h.app.ManagerRunReconciliation(t.Context(), as.TaskID, as.RunID)
				if err != nil || after.UnappliedEvents != 0 || after.Observed != protocol.RunFinished ||
					after.EventLastSequence != seq || after.CloseRequestID != before.CloseRequestID || after.CloseLastSequence != seq {
					t.Fatalf("invalid original transport settlement: %+v %v", after, err)
				}
			}
			rows, err := h.app.ManagerNativeEvents(t.Context(), as.RunID, 0)
			if err != nil || len(rows) != int(seq) || rows[seq-1].Sequence != seq || !store.SemanticJSONEqual(string(rows[seq-1].Body), end) {
				t.Fatal("lost original transport event", rows, err)
			}
			if !tc.blocked && tc.tailFields == diagnostic {
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					compact, err := views.RunRollout(t.Context(), tx.Q(), as.RunID, 0, 0, false)
					if err != nil {
						return err
					}
					full, err := views.RunRollout(t.Context(), tx.Q(), as.RunID, 0, 0, true)
					if err != nil {
						return err
					}
					if compact.Watermark != 0 || full.Watermark != seq || len(compact.Logs) != 0 || len(full.Logs) != 1 ||
						full.Logs[0].Seq != seq || full.Logs[0].Kind != "native.disconnected" {
						t.Fatalf("process view changed the original disconnect: compact=%+v full=%+v", compact, full)
					}
					var body struct{ Data struct{ Type, Text string } }
					if err := json.Unmarshal(full.Logs[0].Body, &body); err != nil ||
						body.Data.Type != "agentMessageFragment" || body.Data.Text != observedText {
						t.Fatal("full-screen process view lost the observed fragment", body, err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if request != "" {
				var status string
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					return tx.Q().QueryRowContext(t.Context(), `SELECT status FROM manager_native_requests WHERE run_id=? AND request_id=?`, as.RunID, request).Scan(&status)
				}); err != nil || status != "unknown" {
					t.Fatal("original native request lost its unknown state", status, err)
				}
			}
			if l := h.lease(as.RunID); l.LeaseID != originalLease.LeaseID || l.Status != store.LeaseReleased {
				t.Fatal("old lease changed", l)
			}
			if op := h.exec(runPrincipal(as.RunID, as.Role), env, YieldPause{}); op.Status != protocol.OpSucceeded {
				t.Fatal("original pause receipt did not replay", op)
			}
			if again := admissionClaim(t, h, runtime); again != nil {
				t.Fatal("physical stop replay created another continuation", again)
			}
		})
	}
}
