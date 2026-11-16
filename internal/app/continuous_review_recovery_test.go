package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestContinuousTaskReadmitsItsGeneratedReviewAfterProgressAndNewArtifact(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	first := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	review := canonicalTaskIssue(t, h, first.Responsibility.IssueID)
	progress := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: review.AssignmentEpoch, Summary: "The reviewer is checking the original artifact", NextStep: "Submit the actual independent verdict"}
	if op := h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: review.IssueID}, progress, rev(review.Revision)), UpdateIssueProgress{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.submitVerdict(first.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	closed := canonicalTaskIssue(t, h, review.IssueID)
	if closed.State != protocol.IssueClosed || closed.Closure == nil {
		t.Fatal("assessment-only progress prevented the real review endpoint", closed)
	}
	before := h.task(task.TaskID)
	resource := h.resourceForTask(task.TaskID)
	if !h.releaseResource(task.TaskID, resource.WorkspaceID) {
		t.Fatal("first review resource was not released")
	}
	if op := continueTaskForTest(t, h, task.TaskID, "Continue the original delivery with its next real artifact"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	second := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if op := h.runAs(second, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(second, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	readmitted := canonicalTaskIssue(t, h, review.IssueID)
	if readmitted.State != protocol.IssueActive || readmitted.WorkStatus != protocol.IssueTodo || readmitted.Closure != nil || readmitted.AssignmentEpoch != closed.AssignmentEpoch || readmitted.Assignee != closed.Assignee {
		t.Fatal("same Task review was not readmitted with its retained reviewer", readmitted)
	}
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if accept.TaskID != task.TaskID || accept.Responsibility.IssueID != review.IssueID || accept.Execution.Identity != first.Execution.Identity || h.task(task.TaskID).CurrentArtifactID == before.CurrentArtifactID {
		t.Fatal("new artifact did not reach the original reviewer through normal admission", accept)
	}
	if op := h.submitVerdict(accept.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("acceptances") != 2 || h.task(task.TaskID).SpecVersion != before.SpecVersion || h.count("issue_controls") != 2 {
		t.Fatal("continuation replaced history or created another carrier")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		old, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), review.IssueID, closed.Revision)
		if err == nil && !reflect.DeepEqual(old.Closure, closed.Closure) {
			t.Fatal("previous review closure changed", old)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalWaitRejectsBadDeliveryIdentityWithoutPersistingOrPausing(t *testing.T) {
	h, _, as, _ := externalWaitFixture(t)
	var issue string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		issue, err = store.GetTaskIssueOwner(t.Context(), tx.Q(), as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target string
		code   protocol.Code
	}{{"https://example.invalid/actions/runs/42", protocol.CodeInvalidRequest}, {"missing-delivery", protocol.CodeNotFound}} {
		p := protocol.WaitExternalPayload{Kind: "checks", WaitKey: "checks", IssueID: issue, TargetID: tc.target, Reason: "Await the original CI", Recovery: "Continue the same Task"}
		env := h.env(protocol.OpRunsWaitExternal, runTarget(as.RunID), p, nil)
		for range 2 {
			op := h.exec(runPrincipal(as.RunID, as.Role), env, WaitExternal{})
			if op.Status != protocol.OpFailed || protocol.CodeOf(op.Error) != tc.code || !strings.Contains(op.Error.Message, "checks.target_id") || !strings.Contains(op.Error.Message, "delivery_id") {
				t.Fatal("deterministic input rejection lost its field or business identity", op)
			}
		}
		if h.count("run_external_waits") != 0 || h.run(as.RunID).DesiredState != protocol.DesiredRun || h.lease(as.RunID).Status != store.LeaseActive {
			t.Fatal("invalid wait changed the original execution")
		}
	}
}

func TestContinuousTaskDoesNotReadmitHumanDispositionOfReviewCarrier(t *testing.T) {
	for _, disposition := range []string{"backlog", "pause", "cancel"} {
		t.Run(disposition, func(t *testing.T) {
			h, r, issue := issueOwnerHarness(t)
			task := ownerArtifact(t, h, r, issue)
			first := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
			if op := h.submitVerdict(first.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			review := canonicalTaskIssue(t, h, first.Responsibility.IssueID)
			control := protocol.IssueControlPayload{ExpectedAssignmentEpoch: review.AssignmentEpoch, Reason: "An actual owner changed the review carrier responsibility"}
			review = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, review, "reopen", control))
			if disposition != "backlog" {
				control.ExpectedAssignmentEpoch = review.AssignmentEpoch
				review = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, review, disposition, control))
			}
			resource := h.resourceForTask(task.TaskID)
			if !h.releaseResource(task.TaskID, resource.WorkspaceID) {
				t.Fatal("first resource retained")
			}
			if op := continueTaskForTest(t, h, task.TaskID, "Continue the same implementation goal without overriding the owner's review disposition"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			second := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
			if op := h.runAs(second, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			after := canonicalTaskIssue(t, h, review.IssueID)
			if !reflect.DeepEqual(after, review) {
				t.Fatal("new artifact overwrote the owner's explicit review disposition", review, after)
			}
			if got, ok := h.claimInputRun(r); ok && got.TaskID == task.TaskID && got.Role == protocol.RoleAccept {
				t.Fatal("unadmitted review carrier dispatched acceptance", got)
			}
		})
	}
}
