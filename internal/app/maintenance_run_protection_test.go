package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func receiptRecoveryFixture(t *testing.T) (*harness, protocol.Assignment, protocol.CommittedMaintenancePlan, protocol.RecoveryIdentity, string) {
	t.Helper()
	h, _, _, as, source := reviewManagerAssignment(t)
	ctx := t.Context()
	selection, named, err := h.app.HostedRuntimeSelection(ctx, as.Manager.ConversationID)
	if err != nil || !named {
		t.Fatal(selection, named, err)
	}
	request := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateRunObserved(ctx, as.RunID, protocol.RunUnknown, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: request, MessageID: source, ThreadID: "retained-native", TurnID: "retained-turn", Status: store.TurnInterrupted}, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if err := tx.InsertHostedExecution(ctx, store.HostedExecution{ID: request, ConversationID: as.Manager.ConversationID, IssueID: selection.IssueID, AssignmentEpoch: selection.AssignmentEpoch, Selection: selection, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "receipt-recovery", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "receipt-recovery", InstanceID: "server", ProcessInstanceID: "successor", ArtifactSHA256: strings.Repeat("b", 64)}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{}}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		open, err := store.MaintenanceOpenRuns(ctx, tx.Q())
		if err != nil {
			return err
		}
		for _, r := range open {
			a, err := store.GetAction(ctx, tx.Q(), r.ActionID)
			if err != nil {
				return err
			}
			p.ReceiptRecoveryStop.Runs = append(p.ReceiptRecoveryStop.Runs, protocol.CommittedRetainedRun{TaskID: r.TaskID, Run: protocol.RunView{RunID: r.RunID, Role: r.Role, Host: r.HostID, Generation: r.Generation, SpecVersion: a.TargetSpecVersion, Observed: r.ObservedState}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "successor", SHA256: p.Successor.ArtifactSHA256}
	h.app.committedMaintenanceID = p.MaintenanceID
	return h, as, p, process, request
}

func TestReceiptRecoveryStopPreservesPhysicalFactsAndLateReceipts(t *testing.T) {
	h, as, p, process, original := receiptRecoveryFixture(t)
	ctx := t.Context()
	beforeRun, beforeTask := h.run(as.RunID), h.task(as.TaskID)
	var beforeAction store.ActionRow
	var beforePin *protocol.RunExecutionPin
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		beforeAction, err = store.GetAction(ctx, tx.Q(), beforeRun.ActionID)
		if err != nil {
			return err
		}
		beforePin, err = store.GetRunExecution(ctx, tx.Q(), as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal("registration retry", err)
	}
	if pending, err := h.app.ReceiptRecoveryStartupPending(ctx); err != nil || !pending {
		t.Fatal(pending, err)
	}
	if _, err := h.app.QueueManagerNative(ctx, as.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("protected manager started fresh business", err)
	}
	principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: protocol.RoleIssueManager}
	env := h.env(protocol.OpManagerNativeStopped, protocol.Target{Type: "run", ID: as.RunID}, protocol.ManagerStoppedPayload{Reason: "lease fenced during explicit server recovery", LastSequence: 0}, nil)
	op, err := h.app.Execute(ctx, principal, env, ManagerNativeCommand{OperationName: env.Operation})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if pending, err := h.app.ReceiptRecoveryStartupPending(ctx); err != nil || pending {
		t.Fatal(pending, err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM native_turn_receipts_pending WHERE request_id=?`, original)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		var sequence int
		var name string
		return tx.Q().QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &name, &path)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	h.store = reopened
	h.app = New(reopened, h.ids, h.clock.Now, WithCommittedMaintenanceStartup(p.MaintenanceID))
	// Simulate another late physical proof after linkage and receipt reading.
	late := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "late original physical stop", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if late.Status != protocol.OpSucceeded {
		t.Fatal(late)
	}
	replayed, err := h.app.Execute(ctx, principal, env, ManagerNativeCommand{OperationName: env.Operation})
	if err != nil || !reflect.DeepEqual(op, replayed) {
		t.Fatal("original stop receipt changed", op, replayed, err)
	}
	closeID, err := h.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if claim := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); claim.Status != protocol.OpSucceeded {
		t.Fatal(claim)
	}
	if reply := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeID}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); reply.Status != protocol.OpSucceeded {
		t.Fatal(reply)
	}
	if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeTask, h.task(as.TaskID)) {
		t.Fatal("maintenance physical stop changed business Run or Task")
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		action, err := store.GetAction(ctx, tx.Q(), beforeRun.ActionID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(action, beforeAction) {
			t.Fatal("physical stop changed action")
		}
		pin, err := store.GetRunExecution(ctx, tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(pin, beforePin) {
			t.Fatal("physical stop changed retained pin")
		}
		var facts int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM maintenance_physical_stops WHERE run_id=?`, as.RunID).Scan(&facts); err != nil {
			return err
		}
		if facts != 3 {
			t.Fatalf("physical stop facts=%d, want 3 with exact replay deduplicated", facts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptRecoveryNativeFenceFollowsUnlinkedLeaf(t *testing.T) {
	h, _, p, process, _ := receiptRecoveryFixture(t)
	ctx := t.Context()
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}
	child := protocol.CommittedMaintenanceLedger{Plan: protocol.CommittedMaintenancePlan{MaintenanceID: "repair-leaf", RequestID: h.ids.New(protocol.PrefixRequest), ParentMaintenanceID: p.MaintenanceID, ReceiptRecoveryStop: p.ReceiptRecoveryStop}}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		return tx.PutCommittedMaintenance(ctx, child, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	for _, linked := range []bool{false, true} {
		child.Linked = linked
		if err := h.store.Write(ctx, func(tx *store.Tx) error {
			return tx.PutCommittedMaintenance(ctx, child, h.clock.Now().UnixMilli())
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
			fenced, err := maintenanceNativeBusinessFenced(ctx, tx.Q(), store.RunRow{RunID: "independent-unprotected-manager"})
			if err != nil {
				return err
			}
			if fenced == linked {
				t.Fatal("obsolete parent blocked independent native admission", linked, fenced)
			}
			parent, err := store.GetCommittedMaintenance(ctx, tx.Q(), p.MaintenanceID)
			if err != nil {
				return err
			}
			if parent.Linked {
				t.Fatal("child rewrote parent linkage")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReceiptRecoveryStopRejectsIncompleteInventory(t *testing.T) {
	for _, change := range []string{"omitted", "rebound", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			h, _, p, process, _ := receiptRecoveryFixture(t)
			switch change {
			case "omitted":
				p.ReceiptRecoveryStop.Runs = nil
			case "rebound":
				p.ReceiptRecoveryStop.Runs[0].Run.Host = "different-host"
			case "duplicate":
				p.ReceiptRecoveryStop.Runs = append(p.ReceiptRecoveryStop.Runs, p.ReceiptRecoveryStop.Runs[0])
			}
			if _, err := h.app.RegisterCommittedMaintenance(t.Context(), p, process); err == nil {
				t.Fatal("changed inventory accepted")
			}
		})
	}
}

func TestReceiptRecoveryStopDoesNotSwallowExplicitControl(t *testing.T) {
	for _, change := range []string{"cancelled", "desired_stopped", "lease_changed", "already_terminal", "no_original_receipt"} {
		t.Run(change, func(t *testing.T) {
			h, as, p, process, original := receiptRecoveryFixture(t)
			if change == "already_terminal" || change == "no_original_receipt" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					if change == "already_terminal" {
						return tx.UpdateRunObserved(t.Context(), as.RunID, protocol.RunFinished, h.clock.Now().UnixMilli())
					}
					_, err := tx.Exec(t.Context(), `DELETE FROM native_turn_receipts_pending WHERE request_id=?`, original)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.app.RegisterCommittedMaintenance(t.Context(), p, process); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				var query string
				switch change {
				case "cancelled":
					query = `UPDATE tasks SET lifecycle='cancelled' WHERE task_id=?`
				case "desired_stopped":
					query = `UPDATE runs SET desired_state='stopped' WHERE task_id=?`
				case "lease_changed":
					_, err := tx.Exec(t.Context(), `UPDATE leases SET lease_id=? WHERE run_id=?`, h.ids.New(protocol.PrefixLease), as.RunID)
					return err
				default:
					return nil
				}
				_, err := tx.Exec(t.Context(), query, as.TaskID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "normal stopped process", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if got := h.run(as.RunID); maintenanceRunOpen(got.ObservedState) {
				t.Fatal("explicit control or ordinary stop was swallowed", got)
			}
		})
	}
}

func TestReceiptRecoveryPhysicalStopThenOwnerCancel(t *testing.T) {
	h, as, p, process, _ := receiptRecoveryFixture(t)
	ctx := t.Context()
	if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
		t.Fatal(err)
	}
	principal := runPrincipal(as.RunID, as.Role)
	env := h.env(protocol.OpManagerNativeStopped, runTarget(as.RunID), protocol.ManagerStoppedPayload{Reason: "original lease fenced", LastSequence: 0}, nil)
	stopped, err := h.app.Execute(ctx, principal, env, ManagerNativeCommand{OperationName: env.Operation})
	if err != nil || stopped.Status != protocol.OpSucceeded {
		t.Fatal(stopped, err)
	}
	if _, err := h.app.LinkCommittedMaintenance(ctx, protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: p.PreparationRevision}, false); err != nil {
		t.Fatal(err)
	}
	var issue protocol.Issue
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		m, e := store.GetIssueManagerTask(ctx, tx.Q(), as.TaskID)
		if e != nil {
			return e
		}
		issue, e = store.GetCanonicalIssue(ctx, tx.Q(), m.IssueID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	control := h.env("issues.cancel", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "independent owner cancellation"}, rev(issue.Revision))
	op := h.exec(owner, control, CanonicalOwnerIssueCommand{Action: "cancel"})
	if op.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFailed {
		t.Fatal(op, h.run(as.RunID))
	}
	replay, err := h.app.Execute(ctx, principal, env, ManagerNativeCommand{OperationName: env.Operation})
	if err != nil || !reflect.DeepEqual(stopped, replay) {
		t.Fatal("original physical receipt changed", stopped, replay, err)
	}
	if again := h.exec(owner, control, CanonicalOwnerIssueCommand{Action: "cancel"}); !reflect.DeepEqual(op, again) {
		t.Fatal("owner retry changed", op, again)
	}
}

func TestReceiptRecoveryWorkerBusinessResultAndExplicitCancelRemainAuthoritative(t *testing.T) {
	for _, outcome := range []string{"artifact", "cancel_before_give_up"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			ctx := t.Context()
			_, task := h.createTask(owner, createTaskPayload{Title: "retained worker", Spec: testSpec})
			as, ok, _ := h.claim("linux-1")
			if !ok {
				t.Fatal("missing worker")
			}
			if op := h.reportWorkspace(as); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			r := h.run(as.RunID)
			prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
			if err != nil {
				t.Fatal(err)
			}
			p := protocol.CommittedMaintenancePlan{MaintenanceID: "worker-results", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "previous", ArtifactSHA256: strings.Repeat("a", 64)}, Successor: protocol.UpgradeBinding{UpgradeID: "worker-results", InstanceID: "server", ProcessInstanceID: "successor", ArtifactSHA256: strings.Repeat("b", 64)}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{Runs: []protocol.CommittedRetainedRun{{TaskID: as.TaskID, Run: protocol.RunView{RunID: as.RunID, Role: as.Role, Host: r.HostID, Generation: r.Generation, SpecVersion: 1, Observed: r.ObservedState}}}}}
			process := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "successor", SHA256: p.Successor.ArtifactSHA256}
			if _, err := h.app.RegisterCommittedMaintenance(ctx, p, process); err != nil {
				t.Fatal(err)
			}
			if _, err := h.app.LinkCommittedMaintenance(ctx, protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 9, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
				t.Fatal(err)
			}
			want := protocol.RunFinished
			if outcome == "artifact" {
				op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				if op := h.exec(owner, h.env("tasks.cancel", taskTarget(task.TaskID), struct{}{}, rev(h.task(task.TaskID).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "explicit owner cancellation"}, Release{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				want = protocol.RunFailed
			}
			if got := h.run(as.RunID); got.ObservedState != want || h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatal("protection swallowed authoritative result", got)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				var n int
				err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM maintenance_physical_stops WHERE run_id=?`, as.RunID).Scan(&n)
				if n != 0 {
					t.Fatal("normal business result treated as uncertain physical stop", n)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
