package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_ManagerTransportRecoveryRequiresConfirmedOriginalReceipts(t *testing.T) {
	for _, test := range []struct {
		name            string
		unknown         bool
		receiptPending  bool
		pause           bool
		reason          string
		protocolVersion int64
	}{
		{name: "settled", reason: protocol.ManagerStoppedTransportLease, protocolVersion: 6},
		{name: "unknown command", unknown: true, reason: protocol.ManagerStoppedTransportLease, protocolVersion: 6},
		{name: "unknown turn receipt", receiptPending: true, reason: protocol.ManagerStoppedTransportLease, protocolVersion: 6},
		{name: "issue paused", pause: true, reason: protocol.ManagerStoppedTransportLease, protocolVersion: 6},
		{name: "ordinary stop", reason: "manager run fenced", protocolVersion: 6},
		{name: "explicit deadline protocol", reason: protocol.ManagerStoppedTransportLease, protocolVersion: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, runtime, issue, as, source := reviewManagerAssignment(t, test.protocolVersion)
			selection, named, err := h.app.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
			if err != nil || !named || selection.RunnerRunID != as.RunID {
				t.Fatal(selection, named, err)
			}
			execution := h.ids.New(protocol.PrefixRequest)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE destination=?`, store.ConversationDestination(as.Manager.ConversationID)); err != nil {
					return err
				}
				now := h.clock.Now().UnixMilli()
				if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: execution, MessageID: source, ThreadID: "native",
					TurnID: "original-turn", Status: store.TurnInterrupted}, now); err != nil {
					return err
				}
				if err := tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: execution, ConversationID: as.Manager.ConversationID,
					IssueID: selection.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: now}); err != nil {
					return err
				}
				if test.receiptPending {
					_, err := tx.Exec(t.Context(), `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, execution)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "resume", json.RawMessage(`{"thread_id":"native"}`))
			if err != nil {
				t.Fatal(err)
			}
			if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Error != nil {
				t.Fatal(op)
			}
			if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Result: json.RawMessage(`{"ID":"native"}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Error != nil {
				t.Fatal(op)
			}
			var unknownRequest string
			if test.unknown {
				if unknownRequest, err = h.app.QueueManagerNative(t.Context(), as.RunID, "tool_result", json.RawMessage(`{"thread_id":"native","turn_id":"original-turn","call_id":"original-tool","tool_result":{"Success":true,"Text":"original result"}}`)); err != nil {
					t.Fatal(err)
				}
				op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
				var claimed protocol.ManagerNativeRequest
				if op.Error != nil || json.Unmarshal(op.Result, &claimed) != nil || claimed.ID != unknownRequest {
					t.Fatal("the original command was not claimed", op)
				}
			}
			event := protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"request_usage"}}`)}
			if op := h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Error != nil {
				t.Fatal(op)
			}
			assertNoRecovery := func() {
				t.Helper()
				if h.countWhere(`SELECT COUNT(*) FROM plan_actions WHERE task_id=? AND generation>?`, as.TaskID, as.Generation) != 0 {
					t.Fatal("successor dispatched before its original facts settled")
				}
			}
			assertNoRecovery()
			if as.Protocol == 6 {
				h.clock.Advance(DefaultLeaseTTL + time.Second)
				if count, err := h.app.ExpireLeases(t.Context()); err != nil || count != 1 {
					t.Fatal("expected the original lease to lapse without proof of stop", count, err)
				}
				assertNoRecovery()
			}
			stopped := protocol.ManagerStoppedPayload{Reason: test.reason, LastSequence: event.Sequence}
			if op := h.runAs(as, protocol.OpManagerNativeStopped, stopped, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Error != nil {
				t.Fatal(op)
			}
			assertNoRecovery()
			if test.unknown && h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='unknown'`, unknownRequest) != 1 {
				t.Fatal("original command outcome did not remain unknown")
			}
			if test.pause {
				env := h.env("issues.pause", protocol.Target{Type: "issue", ID: issue.IssueID},
					protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Pause before tail settlement"}, rev(issue.Revision))
				if op := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "pause"}); op.Error != nil {
					t.Fatal(op)
				}
			}
			if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, event.Sequence, "native", "", "", false); err != nil {
				t.Fatal(err)
			}
			claim := func() *protocol.Assignment {
				t.Helper()
				op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"},
					protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
				var result struct {
					Assignment *protocol.Assignment `json:"assignment"`
				}
				if op.Error != nil || json.Unmarshal(op.Result, &result) != nil {
					t.Fatal(op)
				}
				return result.Assignment
			}
			runtime.Sequence++
			if op := h.versionedHello("linux-1", runtime); op.Error != nil {
				t.Fatal(op)
			}
			first := claim()
			if test.name != "settled" {
				assertNoRecovery()
				if first != nil {
					t.Fatal("unexpected successor", first)
				}
				return
			}
			if first == nil || first.TaskID != as.TaskID || first.RunID == as.RunID || first.Generation != as.Generation+1 {
				t.Fatalf("expected one same-Task successor, got %v. markers=%d admitted=%d task=%+v", first,
					h.count("manager_transport_recoveries"), h.countWhere(`SELECT COUNT(*) FROM manager_transport_recoveries WHERE successor_generation IS NOT NULL`), h.task(as.TaskID))
			}
			if op := h.runAs(as, protocol.OpManagerNativeStopped, stopped, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Error != nil {
				t.Fatal(op)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				wake, err := store.HeadOutbox(t.Context(), tx.Q(), store.ConversationDestination(as.Manager.ConversationID))
				if err != nil {
					return err
				}
				var p HandleMessage
				if err := json.Unmarshal([]byte(wake.PayloadJSON), &p); err != nil {
					return err
				}
				if p.EventKind != "run.manager_transport_recovery" || p.MessageID == source {
					t.Fatal("recovery replayed the original input", p)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			successorStopped := protocol.ManagerStoppedPayload{Reason: stopped.Reason}
			if op := h.runAs(*first, protocol.OpManagerNativeStopped, successorStopped, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Error != nil {
				t.Fatal(op)
			}
			// The new wake can still be pending when this Run stops. It must
			// not be serviced by a third generation of the same Task.
			if third := claim(); third != nil {
				t.Fatal("recovery wake escaped its bound successor", third)
			}
			if h.countWhere(`SELECT COUNT(*) FROM plan_actions WHERE task_id=? AND generation>?`, as.TaskID, as.Generation) != 1 ||
				h.countWhere(`SELECT COUNT(*) FROM events WHERE object_id=? AND kind='run.manager_transport_recovery'`, as.RunID) != 1 ||
				h.countWhere(`SELECT COUNT(*) FROM main_turns WHERE request_id=? AND status='interrupted'`, execution) != 1 {
				t.Fatal("original receipt or successor identity changed after stop replay")
			}
		})
	}
}
