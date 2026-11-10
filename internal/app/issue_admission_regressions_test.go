package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestIssueAdmissionRegressionLegacyWorkerResumesAfterTodo(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	if op := createOwnerExecution(t, h, i, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	i = admissionCurrent(t, h, i.IssueID)
	i = issueAdmissionStatus(t, h, i, protocol.IssueBacklog, nil)
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	run := h.run(as.RunID)
	var action store.ActionRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var e error
		action, e = store.GetAction(t.Context(), tx.Q(), as.ActionID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if desired := desiredFor(run, action, h.task(as.TaskID)); desired != protocol.DesiredRun {
		t.Fatalf("resumed legacy worker desired=%s, handoff=%+v, allowed=%v", desired, run.PauseHandoff, as.PauseHandoffs)
	}
}

func TestIssueAdmissionRegressionRetainedIssuePauseRestoresOnlyItsTaskControl(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Retained manager"}, rev(i.Revision)), retainedManagerCommand{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := admissionClaim(t, h, r)
	i = admissionCurrent(t, h, i.IssueID)
	paused := h.exec(owner, h.env("issues.pause", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Pause"}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "pause"})
	if paused.Status != protocol.OpSucceeded {
		t.Fatal(paused)
	}
	// Exact task control projection stored by the base branch on Issue pause.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, e := tx.Exec(t.Context(), `DELETE FROM run_pause_handoffs WHERE run_id=?`, as.RunID); e != nil {
			return e
		}
		if _, e := tx.Exec(t.Context(), `UPDATE issue_manager_tasks SET issue_paused=1 WHERE task_id=?`, as.TaskID); e != nil {
			return e
		}
		_, e := tx.Exec(t.Context(), `UPDATE tasks SET control_mode='paused',control_revision=control_revision+1 WHERE task_id=?`, as.TaskID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.InitializeIssueAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	i = admissionCurrent(t, h, i.IssueID)
	resumed := h.exec(owner, h.env("issues.resume", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Resume"}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "resume"})
	if resumed.Status != protocol.OpSucceeded {
		t.Fatal(resumed)
	}
	if task := h.task(as.TaskID); task.ControlMode != protocol.ControlManaged {
		t.Fatalf("resumed Issue retained its old Issue-induced task control: %s", task.ControlMode)
	}
}

func TestIssueAdmissionRegressionReturningAssignedManagerToOwnerDoesNotReclaim(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	assignment := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Return this work to me"}, rev(i.Revision)), HostedIssueAssignment{}))
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: assignment.AssignmentEpoch, HandoffID: assignment.PendingHandoffID, Reason: "Confirmed old owner stopped"}, rev(assignment.Revision)), HostedIssueAssignment{Finish: true}))
	if i.Assignee.Kind != protocol.IssueAssigneeOwner {
		t.Fatal(i)
	}
	if as := admissionClaim(t, h, r); as != nil {
		t.Fatalf("owner's manual handback immediately reallocated to Agent: %+v", as)
	}
}

func TestIssueAdmissionRegressionDeferredHandbackChangesAssignee(t *testing.T) {
	h, _, i := issueOwnerHarness(t)
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", ExpectedAssignmentEpoch: i.AssignmentEpoch, Replace: true, Reason: "Prepare replacement"}, rev(i.Revision)), RequestIssueOwner{}))
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Cancel replacement and return work to me"}, rev(i.Revision)), HostedIssueAssignment{}))
	if i.Assignee.Kind != protocol.IssueAssigneeOwner || i.Assignee.Ref != owner.Subject {
		t.Fatalf("handback cleared selection but retained Agent assignee: %+v", i.IssueControl)
	}
}

func TestIssueAdmissionRegressionInitializerPreservesExplicitlyClearedSelection(t *testing.T) {
	h, _, i := issueOwnerHarness(t)
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", ExpectedAssignmentEpoch: i.AssignmentEpoch, Replace: true, Reason: "Prepare replacement"}, rev(i.Revision)), RequestIssueOwner{}))
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Clear selection"}, rev(i.Revision)), HostedIssueAssignment{}))
	if i.OwnerSelection != nil {
		t.Fatal("selection was not cleared")
	}
	if err := h.app.InitializeIssueAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	i = admissionCurrent(t, h, i.IssueID)
	if i.OwnerSelection != nil {
		t.Fatalf("startup resurrected explicitly cleared selection: %+v", i.OwnerSelection)
	}
}
