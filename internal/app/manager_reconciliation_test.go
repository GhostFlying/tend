package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func stoppedRetainedManager(t *testing.T) (*harness, protocol.Assignment, string) {
	t.Helper()
	h, as, plan, process, pending := receiptRecoveryFixture(t)
	if _, err := h.app.RegisterCommittedMaintenance(t.Context(), plan, process); err != nil {
		t.Fatal(err)
	}
	closeID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claim := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	if claim.Status != protocol.OpSucceeded {
		t.Fatal(claim)
	}
	close := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeID}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if close.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunUnknown {
		t.Fatal("physical close ended original business responsibility", close)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: plan.MaintenanceID, RequestID: plan.RequestID, Previous: plan.Previous, Successor: plan.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := h.app.LinkCommittedMaintenance(t.Context(), proof); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: plan.PreparationRevision}, false); err != nil {
		t.Fatal(err)
	}
	return h, as, pending
}

func managerReconciliationPayload(t *testing.T, h *harness, as protocol.Assignment) protocol.ReconcileManagerRunPayload {
	t.Helper()
	s, err := h.app.ManagerRunReconciliation(t.Context(), as.TaskID, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.ReconcileManagerRunPayload{RunID: as.RunID, LeaseID: s.LeaseID, CloseRequestID: s.CloseRequestID, CloseResultSHA256: s.CloseResultSHA256, ExpectedAssignmentEpoch: s.AssignmentEpoch, Reason: "Reconcile the original proved-close manager without replaying its business operations"}
}

func resolveRetainedBusinessReceipt(t *testing.T, h *harness, pending string) {
	t.Helper()
	// This fixture supplies the original native reader's terminal ledger fact;
	// the reconciliation command itself cannot resolve or replace that receipt.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM native_turn_receipts_pending WHERE request_id=?`, pending)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRunReconciliationPreservesOriginalFactsAndWaitingResponsibility(t *testing.T) {
	h, as, pending := stoppedRetainedManager(t)
	resolveRetainedBusinessReceipt(t, h, pending)
	// A failed metadata read and an explicitly unexecuted business request
	// retain their actual receipts but are not unknown external business.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, item := range []struct {
			method   string
			rejected bool
		}{{"models", false}, {"turn", true}, {"close", true}} {
			id := h.ids.New(protocol.PrefixRequest)
			message := "Original request returned an error"
			if item.method == "close" {
				message = ""
			}
			raw, err := json.Marshal(protocol.ManagerNativeReply{ID: id, Error: message, Rejected: item.rejected})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,result_json,created_at) VALUES(?,?,?,'{}','completed',?,?)`, id, as.RunID, item.method, string(raw), h.clock.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := managerReconciliationPayload(t, h, as)
	s, err := h.app.ManagerRunReconciliation(t.Context(), as.TaskID, as.RunID)
	if err != nil || !s.CanSettleWaiting || len(s.Blockers) != 0 {
		t.Fatal(s, err)
	}
	var native, originalPin string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT result_json FROM manager_native_requests WHERE request_id=?`, s.CloseRequestID).Scan(&native); err != nil {
			return err
		}
		return tx.Q().QueryRowContext(t.Context(), `SELECT pin_json FROM run_agent_executions WHERE run_id=?`, as.RunID).Scan(&originalPin)
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), before, rev(s.TaskRevision))
	op := h.exec(owner, env, ReconcileManagerRun{})
	if op.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFinished || h.task(as.TaskID).ControlRevision != s.TaskRevision+1 {
		t.Fatal(op, h.task(as.TaskID))
	}
	if replay := h.exec(owner, env, ReconcileManagerRun{}); !reflect.DeepEqual(op, replay) {
		t.Fatal("original independent reconciliation receipt changed", replay)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var after, pin string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT result_json FROM manager_native_requests WHERE request_id=?`, s.CloseRequestID).Scan(&after); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT pin_json FROM run_agent_executions WHERE run_id=?`, as.RunID).Scan(&pin); err != nil {
			return err
		}
		l, err := store.GetLeaseByRun(t.Context(), tx.Q(), as.RunID)
		if after != native || pin != originalPin || err != nil || l.Status != store.LeaseReleased {
			t.Fatal("original native fact/pin changed or lease renewed", after, pin, l, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The original Run cannot be resumed by another reconciliation request.
	next := h.exec(owner, h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), before, rev(s.TaskRevision+1)), ReconcileManagerRun{})
	if next.Status == protocol.OpSucceeded {
		t.Fatal("terminal original run reconciled twice", next)
	}
}

func TestManagerRunReconciliationDoesNotClassifyUnknownOrChangedEvidence(t *testing.T) {
	for _, change := range []string{"business-pending", "later-resume", "later-open", "later-turn", "later-uncertain-close", "unknown-native", "unclassified-business-error", "late-event", "unapplied-event", "wrong-lease", "wrong-close-hash", "old-task-revision"} {
		t.Run(change, func(t *testing.T) {
			h, as, pending := stoppedRetainedManager(t)
			p := managerReconciliationPayload(t, h, as)
			if change != "business-pending" {
				resolveRetainedBusinessReceipt(t, h, pending)
			}
			expected := h.task(as.TaskID).ControlRevision
			switch change {
			case "later-resume", "later-open", "later-turn", "later-uncertain-close":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					method := map[string]string{"later-resume": "resume", "later-open": "open", "later-turn": "turn", "later-uncertain-close": "close"}[change]
					id := h.ids.New(protocol.PrefixRequest)
					reply := protocol.ManagerNativeReply{ID: id, Result: json.RawMessage(`{}`)}
					if change == "later-uncertain-close" {
						reply.Error = "Close outcome was not observed"
					}
					raw, err := json.Marshal(reply)
					if err != nil {
						return err
					}
					_, err = tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,result_json,created_at) VALUES(?,?,?,'{}','completed',?,?)`, id, as.RunID, method, string(raw), h.clock.Now().UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "unknown-native", "unclassified-business-error":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					id := h.ids.New(protocol.PrefixRequest)
					status, result := "unknown", ""
					if change == "unclassified-business-error" {
						status = "completed"
						raw, err := json.Marshal(protocol.ManagerNativeReply{ID: id, Error: "Native business result was not observed"})
						if err != nil {
							return err
						}
						result = string(raw)
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,result_json,created_at) VALUES(?,?,'turn','{}',?,NULLIF(?,''),?)`, id, as.RunID, status, result, h.clock.Now().UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "late-event", "unapplied-event":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_events(run_id,seq,body_json,applied) VALUES(?,1,'{}',?)`, as.RunID, change == "late-event")
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "wrong-lease":
				p.LeaseID = h.ids.New(protocol.PrefixLease)
			case "wrong-close-hash":
				p.CloseResultSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "old-task-revision":
				expected--
			}
			env := h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), p, rev(expected))
			op, err := h.app.Execute(t.Context(), owner, env, ReconcileManagerRun{})
			if (err == nil && op.Status == protocol.OpSucceeded) || h.run(as.RunID).ObservedState != protocol.RunUnknown {
				t.Fatal("unknown execution classified or replayed", op, err)
			}
			replay, replayErr := h.app.Execute(t.Context(), owner, env, ReconcileManagerRun{})
			if !reflect.DeepEqual(replay, op) || protocol.CodeOf(replayErr) != protocol.CodeOf(err) {
				t.Fatal("original blocked request changed", replay, replayErr)
			}
		})
	}
}

func TestManagerRunReconciliationUnknownRetryKeepsOriginalIdentity(t *testing.T) {
	h, as, pending := stoppedRetainedManager(t)
	p := managerReconciliationPayload(t, h, as)
	env := h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), p, rev(h.task(as.TaskID).ControlRevision))
	before := h.run(as.RunID)
	if _, err := h.app.Execute(t.Context(), owner, env, ReconcileManagerRun{}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome || !reflect.DeepEqual(before, h.run(as.RunID)) {
		t.Fatal("unknown receipt was treated as a terminal outcome", err)
	}
	resolveRetainedBusinessReceipt(t, h, pending)
	// Original input, revision, native proof and request ID stay byte-for-byte
	// unchanged. Only the original business receipt has now been reconciled.
	op, err := h.app.Execute(t.Context(), owner, env, ReconcileManagerRun{})
	if err != nil || op.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFinished {
		t.Fatal("original reconciliation request could not continue", op, err)
	}
	if again, err := h.app.Execute(t.Context(), owner, env, ReconcileManagerRun{}); err != nil || !reflect.DeepEqual(op, again) {
		t.Fatal("completed reconciliation replay changed", again, err)
	}
}

func TestManagerRunReconciliationMainRequiresAdmittedOwnerSource(t *testing.T) {
	h, as, pending := stoppedRetainedManager(t)
	resolveRetainedBusinessReceipt(t, h, pending)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, hosted := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(hosted))
	_, delivery := h.say("Reconcile this original proved-close manager after its original business receipts have been read")
	turn := startHostedReview(t, h, "main", delivery)
	p := managerReconciliationPayload(t, h, as)
	// This fixture also has the original coordination input queued. Use the
	// input actually admitted by the dispatcher, never the latest queued ID.
	p.SourceMessageID, p.ExpectedSourceRevision = turn.MessageID, rev(1)
	expected := h.task(as.TaskID).ControlRevision
	attempt := func(payload protocol.ReconcileManagerRunPayload, seq int64) (protocol.Operation, error) {
		return h.app.Execute(t.Context(), mainAgent, h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), payload, rev(expected)), HostedCoordinationCommand{Command: ReconcileManagerRun{}, ExecutionID: turn.RequestID, EffectSeq: seq})
	}
	for n, invalid := range []string{"missing", "wrong-revision", "not-admitted"} {
		bad := p
		switch invalid {
		case "missing":
			bad.SourceMessageID, bad.ExpectedSourceRevision = "", nil
		case "wrong-revision":
			bad.ExpectedSourceRevision = rev(2)
		case "not-admitted":
			bad.SourceMessageID = queueMessage(t, h, "A different queued source is not authority")
		}
		op, err := attempt(bad, int64(n+1))
		if (err == nil && op.Status == protocol.OpSucceeded) || h.run(as.RunID).ObservedState != protocol.RunUnknown {
			t.Fatal("unadmitted owner source settled manager", invalid, op, err)
		}
	}
	op, err := attempt(p, 4)
	if err != nil || op.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFinished {
		t.Fatal("actual admitted main relay rejected", op, err)
	}
}

func TestManagerRunReconciliationRejectsHistoricalSource(t *testing.T) {
	h, as, pending := stoppedRetainedManager(t)
	resolveRetainedBusinessReceipt(t, h, pending)
	h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{})
	_, hosted := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(hosted))
	_, firstDelivery := h.say("Previous owner instruction")
	previous := startHostedReview(t, h, "main", firstDelivery)
	finishHostedReview(t, h, previous)
	_, nextDelivery := h.say("Inspect only; do not reconcile")
	current := startHostedReview(t, h, "main", nextDelivery)
	var admittedToCurrent, admittedEver bool
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		admittedEver, err = store.OwnerInputAdmitted(t.Context(), tx.Q(), previous.MessageID)
		if err != nil {
			return err
		}
		return tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?) OR EXISTS(SELECT 1 FROM main_steers s JOIN main_turns t ON t.thread_id=s.thread_id AND t.turn_id=s.turn_id WHERE t.request_id=? AND s.message_id=? AND s.native_item_id<>'')`, current.RequestID, previous.MessageID, current.RequestID, previous.MessageID).Scan(&admittedToCurrent)
	}); err != nil {
		t.Fatal(err)
	}
	if !admittedEver || admittedToCurrent {
		t.Fatalf("fixture is not historical-only: ever=%t current=%t", admittedEver, admittedToCurrent)
	}
	associations := h.associations(previous.MessageID)
	classified := hostedEffect(t, h, mainAgent, current, 1, protocol.Target{Type: "message", ID: previous.MessageID}, rev(associations.Revision), protocol.SetIssueMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: associations.ContentRevision}, SetIssueMessageAssociations{})
	if classified.Status != protocol.OpSucceeded {
		t.Fatal(classified)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var member bool
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?)`, current.RequestID, previous.MessageID).Scan(&member); err != nil {
			return err
		}
		if !member {
			t.Fatal("historical source was not classified for review")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := managerReconciliationPayload(t, h, as)
	p.SourceMessageID, p.ExpectedSourceRevision = previous.MessageID, rev(1)
	op, err := h.app.Execute(t.Context(), mainAgent, h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), p, rev(h.task(as.TaskID).ControlRevision)), HostedCoordinationCommand{Command: ReconcileManagerRun{}, ExecutionID: current.RequestID, EffectSeq: 2})
	if protocol.CodeOf(err) != protocol.CodeForbidden || h.run(as.RunID).ObservedState != protocol.RunUnknown {
		t.Fatalf("classified historical owner source settled original Run: observed=%s op=%+v", h.run(as.RunID).ObservedState, op)
	}

}
