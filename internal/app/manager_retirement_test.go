package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func cancellingRetirementFixture(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Assignment) {
	t.Helper()
	h, runtime, issue, as := cancellationManager(t)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Withdraw this scope and let the owner settle cancellation"}))
	return h, runtime, issue, as
}

func retirementPayload(t *testing.T, h *harness, as protocol.Assignment) (protocol.ManagerRetirementStatus, protocol.RetireManagerRunPayload) {
	t.Helper()
	s, err := h.app.ManagerRetirement(t.Context(), as.TaskID, as.RunID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	return s, protocol.RetireManagerRunPayload{IssueID: s.IssueID, ExpectedIssueRevision: s.IssueRevision, ExpectedAssignmentEpoch: s.AssignmentEpoch, RunID: s.RunID, HostID: s.HostID, LeaseID: s.LeaseID, ExecutionSHA256: s.ExecutionSHA256, Reason: "Retire only the original manager without asserting a native stop"}
}

func retireFixtureManager(t *testing.T, h *harness, as protocol.Assignment) (*protocol.Envelope, protocol.Operation) {
	t.Helper()
	s, p := retirementPayload(t, h, as)
	if !s.CanRetire {
		t.Fatal("original cancelling manager cannot retire", s)
	}
	env := h.env(protocol.OpRetireManagerRun, taskTarget(as.TaskID), p, rev(s.TaskRevision))
	op := h.exec(owner, env, RetireManagerRun{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return env, op
}

func finishRetiredCancellation(t *testing.T, h *harness, issue protocol.Issue) protocol.Operation {
	t.Helper()
	current := canonicalTaskIssue(t, h, issue.IssueID)
	return canonicalLifecycleControl(t, h, current, "finish_cancel", protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "The original cancelled responsibility is fully settled"}})
}

func TestManagerRetirementStopsAssignedCancellationWithoutStopProof(t *testing.T) {
	h, runtime, issue, as := cancellingRetirementFixture(t)
	s, err := h.app.ManagerRetirement(t.Context(), as.TaskID, as.RunID, "", 50)
	if err != nil || !s.CanRetire || s.Desired != protocol.DesiredRun {
		t.Fatal("baseline no longer reproduces retained assigned coordination", s, err)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); err != nil {
		t.Fatal("baseline cancelling manager cannot coordinate", err)
	}
	beforeRun, beforeLease, beforeRuns := h.run(as.RunID), h.lease(as.RunID), h.count("runs")
	env, first := retireFixtureManager(t, h, as)
	if replay := h.exec(owner, env, RetireManagerRun{}); !reflect.DeepEqual(replay, first) {
		t.Fatal("original retirement replay changed", first, replay)
	}
	after := h.run(as.RunID)
	if after.ObservedState != beforeRun.ObservedState || after.DesiredState != protocol.DesiredStopped || !reflect.DeepEqual(h.lease(as.RunID), beforeLease) || h.count("runs") != beforeRuns {
		t.Fatal("control fabricated physical stop, released capacity or created a Run", after, h.lease(as.RunID))
	}
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("retiring manager admitted new native business", err)
	}
	runtime.Sequence++
	op := h.exec(runner(as.Execution.Identity.ExecutionEnvironmentID), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: as.Execution.Identity.ExecutionEnvironmentID}, protocol.HelloPayload{Worker: h.worker(as.Execution.Identity.ExecutionEnvironmentID), Runtime: runtime, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}}, nil), Hello{})
	var hello protocol.HelloResult
	if err := json.Unmarshal(op.Result, &hello); err != nil || len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictStop {
		t.Fatal("original runner did not receive Stop", op, err)
	}
	if result := finishRetiredCancellation(t, h, issue); result.Status == protocol.OpSucceeded {
		t.Fatal("cancellation finished before the original physical receipt", result)
	}
	if h.run(as.RunID).ObservedState != beforeRun.ObservedState {
		t.Fatal("Hello Stop was treated as observed native exit")
	}
}

func TestManagerRetirementRejectsChangedControlAndExecution(t *testing.T) {
	for _, change := range []string{"task-revision", "issue-revision", "epoch", "issue", "host", "lease", "pin", "active-issue", "other-principal"} {
		t.Run(change, func(t *testing.T) {
			h, _, issue, as := cancellingRetirementFixture(t)
			s, p := retirementPayload(t, h, as)
			expected := s.TaskRevision
			principal := owner
			switch change {
			case "task-revision":
				expected--
			case "issue-revision":
				p.ExpectedIssueRevision--
			case "epoch":
				p.ExpectedAssignmentEpoch++
			case "issue":
				p.IssueID = h.ids.New(protocol.PrefixWorkItem)
			case "host":
				p.HostID = "different-host"
			case "lease":
				p.LeaseID = h.ids.New(protocol.PrefixLease)
			case "pin":
				p.ExecutionSHA256 = strings.Repeat("a", 64)
			case "active-issue":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE issue_controls SET state='active' WHERE issue_id=?`, issue.IssueID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "other-principal":
				principal = runPrincipal(as.RunID, as.Role)
			}
			beforeRun, beforeLease, beforeTask := h.run(as.RunID), h.lease(as.RunID), h.task(as.TaskID)
			op, err := h.app.Execute(t.Context(), principal, h.env(protocol.OpRetireManagerRun, taskTarget(as.TaskID), p, rev(expected)), RetireManagerRun{})
			if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("changed control or execution retired", change, op)
			}
			if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) || !reflect.DeepEqual(beforeTask, h.task(as.TaskID)) {
				t.Fatal("rejected retirement changed the original execution")
			}
		})
	}
}

func TestManagerRetirementWaitsForOriginalStopAndLateNativeResult(t *testing.T) {
	h, _, issue, as := cancellationManager(t)
	started, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{"original":"business"}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	pending, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{"never":"claimed"}`))
	if err != nil {
		t.Fatal(err)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel and account for the original native operations"}))
	retireFixtureManager(t, h, as)
	if op := finishRetiredCancellation(t, h, issue); op.Status == protocol.OpSucceeded {
		t.Fatal("stop intent ended cancellation", op)
	}
	stopped := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "The original runner confirms native process exit", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if stopped.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFailed || h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("original stopped receipt failed to release physical capacity", stopped)
	}
	s, err := h.app.ManagerRetirement(t.Context(), as.TaskID, as.RunID, "", 1)
	if err != nil || len(s.NativeRequests) != 1 || !s.More || s.NextAfter == "" || s.UnresolvedNativeRequests != 1 || s.LeaseStatus != string(store.LeaseReleased) {
		t.Fatal("original native requests were not paginated", s, err)
	}
	next, err := h.app.ManagerRetirement(t.Context(), as.TaskID, as.RunID, s.NextAfter, 1)
	if err != nil || len(next.NativeRequests) != 1 || next.More || next.NextAfter != "" {
		t.Fatal("last original request page changed", next, err)
	}
	rows := append(s.NativeRequests, next.NativeRequests...)
	for _, row := range rows {
		if row.ResultSHA256 == "" || !row.ResultPresent || row.ResultHashOmitted {
			t.Fatal("original result identity was lost", row)
		}
		switch row.RequestID {
		case started:
			if row.Status != "unknown" || row.Rejected {
				t.Fatal("claimed business was classified as rejected or successful", row)
			}
		case pending:
			if row.Status != "completed" || !row.Rejected {
				t.Fatal("never-claimed business did not receive definite rejection", row)
			}
		default:
			t.Fatal("original native request was replaced", row)
		}
	}
	if op := finishRetiredCancellation(t, h, issue); op.Status == protocol.OpSucceeded {
		t.Fatal("physical stop cleared an unknown original native result", op)
	}
	// A late original result is a fact under the same native request ID. It
	// neither restarts the Run nor restores the withdrawn business authority.
	reply := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: started, Result: json.RawMessage(`{"original_result":true}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if reply.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFailed {
		t.Fatal("late original native result reopened or lost the execution", reply)
	}
	result := finishRetiredCancellation(t, h, issue)
	if result.Status != protocol.OpSucceeded || canonicalTaskIssue(t, h, issue.IssueID).State != protocol.IssueCancelled {
		t.Fatal("genuinely settled original cancellation could not finish", result)
	}
}

func TestManagerRetirementSuppressesAutomaticReceiptAttachment(t *testing.T) {
	h, _, issue, as := cancellationManager(t)
	startHostedIssueReview(t, h, issue)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Withdraw this manager including automatic attachments"}))
	retireFixtureManager(t, h, as)
	if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process remains stopped", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.count("plan_actions")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return refreshManagerTasks(t.Context(), &Exec{IDs: h.ids, Now: h.clock.Now()}, tx)
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("plan_actions") != before {
		t.Fatal("retirement silently created another receipt or model coordinator")
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		facts, err := canonicalOwnerEndpointFacts(t.Context(), tx.Q(), current)
		if err != nil {
			return err
		}
		for _, blocker := range facts.UnknownOperations {
			if blocker.Code == "manager_business_unknown" && blocker.ObjectID == as.TaskID {
				return nil
			}
		}
		t.Fatal("original started business was omitted from retirement facts", facts)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if op := finishRetiredCancellation(t, h, issue); op.Status == protocol.OpSucceeded {
		t.Fatal("withdrawal cleared original pending business receipts", op)
	}
}

func TestManagerRetirementKeepsUnappliedEventsAfterPhysicalStop(t *testing.T) {
	h, _, issue, as := cancellingRetirementFixture(t)
	retireFixtureManager(t, h, as)
	if op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"thread_id":"original","native":{"Kind":"request_usage"}}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process stopped after its final event", LastSequence: 1}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := finishRetiredCancellation(t, h, issue); op.Status == protocol.OpSucceeded {
		t.Fatal("unapplied original native event was lost at the cancellation endpoint", op)
	}
	s, err := h.app.ManagerRetirement(t.Context(), as.TaskID, as.RunID, "", 1)
	if err != nil || s.UnappliedEvents != 1 || s.EventLastSequence != 1 || s.Observed != protocol.RunFailed {
		t.Fatal("retirement query hid the remaining original event", s, err)
	}
	if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, 1, "original", "", "", false); err != nil {
		t.Fatal(err)
	}
	if op := finishRetiredCancellation(t, h, issue); op.Status != protocol.OpSucceeded {
		t.Fatal("original consumed event did not release cancellation", op)
	}
}

func TestManagerRetirementAcceptsLateOriginalCloseWithoutReending(t *testing.T) {
	h, _, issue, as := cancellationManager(t)
	closeID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Close the original manager and retain its pending receipt"}))
	retireFixtureManager(t, h, as)
	if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process exit is independently confirmed", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	if op := finishRetiredCancellation(t, h, issue); op.Status == protocol.OpSucceeded {
		t.Fatal("unknown original close was dropped", op)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeID, LastSequence: 0, Result: json.RawMessage(`null`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal("late original close could not reconcile the stopped Run", op)
	}
	if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
		t.Fatal("late close changed original failure/lease facts")
	}
	if op := finishRetiredCancellation(t, h, issue); op.Status != protocol.OpSucceeded {
		t.Fatal("reconciled original close did not release cancellation", op)
	}
}

func TestManagerRetirementResultHashBudgetUsesBytes(t *testing.T) {
	h, _, _, as := cancellationManager(t)
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "models", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result, _ := json.Marshal(strings.Repeat("界", 22000))
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Result: result}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	s, err := h.app.ManagerRetirement(t.Context(), as.TaskID, as.RunID, "", 1)
	if err != nil || len(s.NativeRequests) != 1 || !s.NativeRequests[0].ResultPresent || !s.NativeRequests[0].ResultHashOmitted || s.NativeRequests[0].ResultSHA256 != "" {
		t.Fatal("multi-byte result exceeded the byte budget without explicit omission", s, err)
	}
}

func TestManagerRetirementRejectedCloseIsNotStopEvidence(t *testing.T) {
	h, _, issue, as := cancellationManager(t)
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpManagerNativeClaim, struct{}{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Stop the original manager only with real evidence"}))
	retireFixtureManager(t, h, as)
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Rejected: true}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
		t.Fatal("a rejected native close was treated as physical exit")
	}
	if op := finishRetiredCancellation(t, h, issue); op.Status == protocol.OpSucceeded {
		t.Fatal("rejected close finished cancellation", op)
	}
}

func TestManagerRetirementExplicitStopOverridesReceiptOnly(t *testing.T) {
	h, _, _, as := cancellingRetirementFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plan_actions SET trigger_json='{"receipt_only":true}' WHERE action_id=?`, as.ActionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	retireFixtureManager(t, h, as)
	s, err := h.app.ManagerRetirement(t.Context(), as.TaskID, as.RunID, "", 50)
	if err != nil || s.Desired != protocol.DesiredStopped || s.RetirementRequestID == "" {
		t.Fatal("receipt-only bypassed explicit retirement", s, err)
	}
	// This also covers an in-flight receipt action whose status has not yet
	// changed: the original Run's explicit Stop wins before the exception.
	if got := desiredFor(store.RunRow{Role: protocol.RoleIssueManager, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunRunning}, store.ActionRow{Status: protocol.ActionDispatched, TriggerJSON: `{"receipt_only":true}`}, store.TaskRow{}); got != protocol.DesiredStopped {
		t.Fatal("receipt-only action overrode the original Run's Stop", got)
	}
}
