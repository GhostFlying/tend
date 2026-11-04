package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_ManagerFrozenCloseWaitsForOriginalReplyPrefixes(t *testing.T) {
	for _, otherItem := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate_item_%t", otherItem), func(t *testing.T) {
			h, _, _, as, source := reviewManagerAssignment(t)
			selection, named, err := h.app.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
			if err != nil || !named {
				t.Fatal(selection, named, err)
			}
			business := h.ids.New(protocol.PrefixRequest)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: business, MessageID: source, ThreadID: "native", TurnID: "original-turn", Status: store.TurnInterrupted}, h.clock.Now().UnixMilli()); err != nil {
					return err
				}
				if err := tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: business, ConversationID: as.Manager.ConversationID, IssueID: selection.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, business)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for sequence := int64(1); sequence <= 19; sequence++ {
				item := "original-item"
				if otherItem && sequence == 18 {
					item = "another-item"
				}
				body := json.RawMessage(fmt.Sprintf(`{"thread_id":"native","native":{"Kind":"agent_message_delta","TurnID":"original-turn","ItemID":%q,"Text":"original fragment"}}`, item))
				if op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: sequence, Body: body}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if sequence <= 16 {
					if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, sequence, "native", "original-turn", item, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			claim := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			var original protocol.ManagerNativeRequest
			if err := json.Unmarshal(claim.Result, &original); err != nil || original.ID != id {
				t.Fatal(claim, err)
			}
			env := h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), protocol.ManagerNativeReply{ID: id, Result: json.RawMessage(`null`), LastSequence: 19}, nil)
			principal := runPrincipal(as.RunID, as.Role)
			replay := func() (protocol.Operation, error) {
				return h.app.Execute(t.Context(), principal, env, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
			}
			if _, err := replay(); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
				t.Fatalf("frozen close overtook three unacknowledged deltas: %v", err)
			}
			record := func(item, body string, state protocol.AssistantState) {
				t.Helper()
				p := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
				if op := h.exec(p, h.env("conversation.record_agent_message", protocol.Target{Type: "message"}, map[string]any{"body": body, "turn_id": "original-turn", "item_id": item, "status": state}, nil), RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			record("original-item", "original cumulative body", protocol.AssistantStreaming)
			if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "original cumulative body"); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
				t.Fatal("ordinary streaming save acknowledged a replayable prefix", err)
			}
			record("original-item", "original cumulative body", protocol.AssistantInterrupted)
			for _, mismatch := range []struct {
				run, thread, turn, item, body string
				sequence                      int64
			}{
				{"another-run", "native", "original-turn", "original-item", "original cumulative body", 19},
				{as.RunID, "another-thread", "original-turn", "original-item", "original cumulative body", 19},
				{as.RunID, "native", "another-turn", "original-item", "original cumulative body", 19},
				{as.RunID, "native", "original-turn", "another-item", "original cumulative body", 19},
				{as.RunID, "native", "original-turn", "original-item", "different body", 19},
				{as.RunID, "native", "original-turn", "original-item", "original cumulative body", 20},
			} {
				if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), mismatch.run, mismatch.sequence, mismatch.thread, mismatch.turn, mismatch.item, mismatch.body); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
					t.Fatal("mismatched prefix was acknowledged", mismatch, err)
				}
			}
			// Recording against an existing terminal item can succeed without changing
			// its body. That no-op cannot justify acknowledging different consumed text.
			record("original-item", "different body", protocol.AssistantInterrupted)
			if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "different body"); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
				t.Fatal("terminal record no-op was mistaken for durable new text", err)
			}
			// A persisted terminal reply covers only the consumed prefix of its item.
			// Another item's delta must continue blocking the identical close envelope.
			if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "original cumulative body"); err != nil {
				t.Fatal(err)
			}
			if otherItem {
				if _, err := replay(); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
					t.Fatalf("one reply prefix discarded another item's pending delta: %v", err)
				}
				record("another-item", "other cumulative body", protocol.AssistantUnknown)
				if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 18, "native", "original-turn", "another-item", "other cumulative body"); err != nil {
					t.Fatal(err)
				}
			}
			op, err := replay()
			if err != nil || op.Status != protocol.OpSucceeded || op.RequestID != env.RequestID {
				t.Fatal("original frozen close could not commit after exact prefix acknowledgements", op, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var count, last int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*),MAX(seq) FROM manager_native_events WHERE run_id=?`, as.RunID).Scan(&count, &last); err != nil {
					return err
				}
				if count != 19 || last != 19 {
					t.Fatalf("close recovery fabricated a new native boundary: count=%d last=%d", count, last)
				}
				var pending int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business).Scan(&pending); err != nil {
					return err
				}
				if pending != 1 {
					t.Fatal("physical close fabricated original business reconciliation")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if again, err := replay(); err != nil || string(again.Result) != string(op.Result) {
				t.Fatal("committed original close was not idempotent", again, err)
			}
		})
	}
}
