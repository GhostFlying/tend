package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestFault_ManagerLateClosePreservesStoppedRunAfterBusinessSettles(t *testing.T) {
	for _, readerFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "already-settled", true: "reader-before-close"}[readerFirst], func(t *testing.T) {
			h, _, _, as, source := reviewManagerAssignment(t)
			ctx := t.Context()
			var business store.MainTurn
			if readerFirst {
				selection, named, err := h.app.HostedRuntimeSelection(ctx, as.Manager.ConversationID)
				if err != nil || !named {
					t.Fatal(selection, named, err)
				}
				business = store.MainTurn{RequestID: h.ids.New(protocol.PrefixRequest), MessageID: source, ThreadID: "original-native", TurnID: "original-turn", Status: store.TurnInterrupted}
				if err := h.store.Write(ctx, func(tx *store.Tx) error {
					if err := tx.PutMainTurn(ctx, business, h.clock.Now().UnixMilli()); err != nil {
						return err
					}
					if err := tx.InsertHostedExecution(ctx, store.HostedExecution{ID: business.RequestID, ConversationID: as.Manager.ConversationID, IssueID: selection.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, business.RequestID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			closeID, err := h.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			claim := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			var command protocol.ManagerNativeRequest
			if err := json.Unmarshal(claim.Result, &command); err != nil || command.ID != closeID {
				t.Fatal(claim, err)
			}
			stopped := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original native independently stopped", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
			if stopped.Status != protocol.OpSucceeded {
				t.Fatal(stopped)
			}
			before := h.run(as.RunID)
			if before.ObservedState != protocol.RunFailed || h.countWhere(`SELECT COUNT(*) FROM leases WHERE run_id=? AND status='released'`, as.RunID) != 1 {
				t.Fatal("physical stop did not release original run", before)
			}
			if readerFirst {
				if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business.RequestID) != 1 {
					t.Fatal("stop fabricated business completion")
				}
				business.Status, business.NativeReconciled = store.TurnCompleted, true
				if err := h.app.PutMainTurn(ctx, business); err != nil {
					t.Fatal(err)
				}
			}
			beforeRuns := h.countWhere(`SELECT COUNT(*) FROM runs WHERE task_id=?`, as.TaskID)
			fragment := protocol.WorkerProcessText{ThreadID: "original-native", TurnID: "original-turn", ItemID: "partial", Text: "observed native fragment"}
			env := h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), protocol.ManagerNativeReply{ID: closeID, Result: json.RawMessage(`null`), ProcessText: []protocol.WorkerProcessText{fragment}}, nil)
			op, err := h.app.Execute(ctx, runPrincipal(as.RunID, as.Role), env, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatalf("late close on physically stopped run: operation=%+v err=%v", op, err)
			}
			if !reflect.DeepEqual(before, h.run(as.RunID)) || h.countWhere(`SELECT COUNT(*) FROM leases WHERE run_id=? AND status='released'`, as.RunID) != 1 || h.countWhere(`SELECT COUNT(*) FROM runs WHERE task_id=?`, as.TaskID) != beforeRuns {
				t.Fatal("late close transitioned or replaced original responsibility")
			}
			if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND run_id=? AND status='completed' AND COALESCE(json_extract(result_json,'$.error'),'')=''`, closeID, as.RunID) != 1 {
				t.Fatal("original successful close was not retained")
			}
			again, err := h.app.Execute(ctx, runPrincipal(as.RunID, as.Role), env, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
			if err != nil || !reflect.DeepEqual(op, again) {
				t.Fatal("retry changed original close receipt", again, err)
			}
			if _, ready, err := h.app.ManagerNativeResult(ctx, as.RunID, closeID); err != nil || !ready {
				t.Fatal("coordinator could not consume the close", ready, err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				compact, err := views.RunRollout(ctx, tx.Q(), as.RunID, 0, 0, false)
				if err != nil {
					return err
				}
				full, err := views.RunRollout(ctx, tx.Q(), as.RunID, 0, 0, true)
				if err != nil {
					return err
				}
				if len(compact.CloseProcessText) != 0 || full.Watermark != 0 || len(full.Logs) != 0 ||
					!reflect.DeepEqual(full.CloseProcessText, []protocol.WorkerProcessText{fragment}) {
					t.Fatalf("close text changed the original Run cursor or process scope: compact=%+v full=%+v", compact, full)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
