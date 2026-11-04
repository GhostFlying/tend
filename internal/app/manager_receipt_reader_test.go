package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func receiptReaderPayload(t *testing.T, h *harness, as protocol.Assignment) map[string]any {
	t.Helper()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		runtime, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		runtime.Runtime.Capabilities = append(runtime.Runtime.Capabilities, protocol.CapabilityManagerReceiptReader)
		return tx.PutHostRuntime(t.Context(), "linux-1", runtime)
	}); err != nil {
		t.Fatal(err)
	}
	stopped := protocol.ManagerStoppedPayload{Reason: "Original native process stopped", LastSequence: 0}
	op := h.runAs(as, protocol.OpManagerNativeStopped, stopped, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	raw, _ := json.Marshal(managerReconciliationPayload(t, h, as))
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	stopRaw, _ := json.Marshal(stopped)
	hash := sha256.Sum256(stopRaw)
	payload["mode"] = "read_receipts"
	payload["stopped_request_id"] = op.RequestID
	payload["stopped_payload_sha256"] = hex.EncodeToString(hash[:])
	return payload
}

func TestFault_ManagerReceiptReaderReleasesOnlyProvedClosedTransport(t *testing.T) {
	h, as, pending := stoppedRetainedManager(t)
	payload := receiptReaderPayload(t, h, as)
	beforeTask := h.task(as.TaskID)
	beforeRun := h.run(as.RunID)
	var beforeTurn store.MainTurn
	var beforePin *protocol.RunExecutionPin
	var runtime protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		beforeTurn, err = store.GetMainTurn(t.Context(), tx.Q(), pending)
		if err != nil {
			return err
		}
		beforePin, err = store.GetRunExecution(t.Context(), tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		host, err := store.GetHostRuntime(t.Context(), tx.Q(), beforeRun.HostID)
		runtime = host.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), payload, rev(beforeTask.ControlRevision))
	op, err := h.app.Execute(t.Context(), owner, env, ReconcileManagerRun{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("proved-close transport could not admit its original receipt reader", op, err)
	}
	if h.run(as.RunID).ObservedState != protocol.RunFailed || h.task(as.TaskID).ControlMode != beforeTask.ControlMode {
		t.Fatal("physical transport release changed the owner control or claimed success")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		turn, err := store.GetMainTurn(t.Context(), tx.Q(), pending)
		if err != nil {
			return err
		}
		if lease.Status != store.LeaseReleased || !reflect.DeepEqual(turn, beforeTurn) {
			t.Fatal("original lease revived or business turn rewritten", lease, turn)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, pending) != 1 {
		t.Fatal("receipt-reader admission fabricated a native terminal receipt")
	}
	oldRuntime := runtime
	oldRuntime.Capabilities = slices.DeleteFunc(slices.Clone(runtime.Capabilities), func(capability string) bool { return capability == protocol.CapabilityManagerReceiptReader })
	oldClaim := h.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: &oldRuntime, WorkspaceProtocol: 1}, nil), Claim{})
	if string(oldClaim.Result) != `{"assignment":null}` {
		t.Fatal("old runner claimed the new strictly read-only responsibility", oldClaim)
	}
	reader := reviewClaimManager(t, h, &runtime)
	if reader.RunID == as.RunID || reader.TaskID != as.TaskID || reader.Manager == nil || !reader.Manager.ReceiptOnly || !reflect.DeepEqual(reader.Execution, beforePin) {
		t.Fatal("reader did not retain the original frozen responsibility", reader)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), reader.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("receipt reader could start new native business", err)
	}
	oldRuntime.Sequence++
	hello, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{Runtime: &oldRuntime, WorkspaceProtocol: 1}, nil), Hello{})
	if err == nil && hello.Status == protocol.OpSucceeded {
		t.Fatal("downgraded runner recovered a strict reader with its ordinary business probe")
	}
	if again, err := h.app.Execute(t.Context(), owner, env, ReconcileManagerRun{}); err != nil || !reflect.DeepEqual(again, op) {
		t.Fatal("same admission request changed its result", again, err)
	}
}

func TestManagerReceiptReaderRetainsAllFences(t *testing.T) {
	for _, invalid := range []string{"default_settle", "wrong_mode", "missing_stop", "wrong_stop", "wrong_stop_hash", "wrong_close_hash", "wrong_lease", "wrong_epoch", "stale_revision", "missing_proof", "wrong_cursor", "unapplied_event", "later_activity", "changed_pin", "run_principal", "old_runner"} {
		t.Run(invalid, func(t *testing.T) {
			h, as, pending := stoppedRetainedManager(t)
			payload := receiptReaderPayload(t, h, as)
			principal := owner
			expected := h.task(as.TaskID).ControlRevision
			switch invalid {
			case "default_settle":
				delete(payload, "mode")
			case "wrong_mode":
				payload["mode"] = "guess_complete"
			case "missing_stop":
				delete(payload, "stopped_request_id")
			case "wrong_stop":
				payload["stopped_request_id"] = h.ids.New(protocol.PrefixRequest)
			case "wrong_stop_hash":
				payload["stopped_payload_sha256"] = payload["close_result_sha256"]
			case "wrong_close_hash":
				payload["close_result_sha256"] = payload["stopped_payload_sha256"]
			case "wrong_lease":
				payload["lease_id"] = h.ids.New(protocol.PrefixLease)
			case "wrong_epoch":
				payload["expected_assignment_epoch"] = float64(987)
			case "stale_revision":
				expected++
			case "run_principal":
				principal = runPrincipal(as.RunID, as.Role)
			default:
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					var query string
					switch invalid {
					case "missing_proof":
						query = `DELETE FROM maintenance_physical_stops WHERE operation='manager.native_stopped'`
					case "wrong_cursor":
						query = `UPDATE maintenance_physical_stops SET last_sequence=1 WHERE operation='manager.native_stopped'`
					case "unapplied_event":
						_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES(?,1,'{}')`, as.RunID)
						return err
					case "later_activity":
						_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,result_json,created_at) VALUES(?,?,'resume','{}','completed','{}',?)`, h.ids.New(protocol.PrefixRequest), as.RunID, h.clock.Now().UnixMilli())
						return err
					case "changed_pin":
						query = `UPDATE issue_manager_tasks SET pin_json=json_set(pin_json,'$.profile.model','different')`
					case "old_runner":
						runtime, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
						if err != nil {
							return err
						}
						runtime.Runtime.Capabilities = slices.DeleteFunc(runtime.Runtime.Capabilities, func(capability string) bool { return capability == protocol.CapabilityManagerReceiptReader })
						return tx.PutHostRuntime(t.Context(), "linux-1", runtime)
					}
					_, err := tx.Exec(t.Context(), query)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := h.run(as.RunID)
			op, err := h.app.Execute(t.Context(), principal, h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), payload, rev(expected)), ReconcileManagerRun{})
			if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("invalid original receipt-reader responsibility was admitted", invalid, op)
			}
			if !reflect.DeepEqual(before, h.run(as.RunID)) || h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, pending) != 1 || h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='task.manager_receipt_reader_requested'`) != 0 {
				t.Fatal("rejected admission changed the original unknown responsibility", invalid)
			}
		})
	}
}
