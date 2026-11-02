package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func frozenAcceptanceHarness(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Issue) {
	t.Helper()
	h, runtime, issue := issueOwnerHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	payload := protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, ParentIssueID: parent.IssueID}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", payload))
	return h, runtime, issue, parent
}

func expandFrozenAcceptanceIssue(t *testing.T, h *harness, issue protocol.Issue, task string) protocol.Issue {
	t.Helper()
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	plan := issue.Plan
	plan.Scope = "NEW_UNACCEPTED_SCOPE"
	plan.Stages = append(plan.Stages, protocol.IssueStage{StageID: "next", Title: "New responsibility", Status: protocol.WorkItemStagePlanned})
	payload := canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 1, Action: protocol.WorkItemTaskContinueExisting, Reason: "Review the submitted artifact against its original contract"}}}
	return canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
}

func changeFrozenAcceptanceAncestor(t *testing.T, h *harness, parent protocol.Issue) {
	t.Helper()
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	plan := parent.Plan
	plan.Scope = "NEW_ANCESTOR_SCOPE"
	payload := canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: parent.AssignmentEpoch}
	canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: parent.IssueID}, payload, rev(parent.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
}

func TestFault_CanonicalAcceptanceRetainsOriginalActionAndNewObligations(t *testing.T) {
	h, runtime, issue, parent := frozenAcceptanceHarness(t)
	task := ownerArtifact(t, h, runtime, issue)
	before := h.task(task.TaskID)
	frozen := h.storedSpec(task.TaskID, 1)
	var action string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT action_id FROM plan_actions WHERE task_id=? AND kind='accept' AND status='pending'`, task.TaskID).Scan(&action)
	}); err != nil {
		t.Fatal(err)
	}
	issue = expandFrozenAcceptanceIssue(t, h, issue, task.TaskID)
	changeFrozenAcceptanceAncestor(t, h, parent)
	accept := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	if accept.TaskID != task.TaskID || accept.ActionID != action || accept.SpecVersion != 1 || accept.Generation != 1 || accept.Execution.Identity.LocalID != "beta" {
		t.Fatal("acceptance replaced its action, contract or independent reviewer", accept)
	}
	if strings.Contains(string(accept.Spec), "NEW_UNACCEPTED_SCOPE") || strings.Contains(string(accept.Spec), "NEW_ANCESTOR_SCOPE") || strings.Contains(string(accept.Spec), "PRIVATE_OWNER_IMPLEMENTATION_DESIGN") {
		t.Fatal("acceptance received new scope or private implementation context")
	}
	if !reflect.DeepEqual(frozen, h.storedSpec(task.TaskID, 1)) {
		t.Fatal("acceptance rewrote the frozen spec")
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := h.task(task.TaskID)
	if after.Lifecycle != protocol.TaskCompleted || after.SpecVersion != before.SpecVersion || after.ControlRevision != before.ControlRevision || after.CurrentArtifactID != before.CurrentArtifactID {
		t.Fatal("the original artifact was not independently accepted", after)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	if current.State != protocol.IssueActive || current.Plan.Scope != "NEW_UNACCEPTED_SCOPE" || current.Plan.Stages[0].Status != protocol.WorkItemStagePlanned {
		t.Fatal("the old artifact settled new Issue obligations", current)
	}
	closure := canonicalLifecycleClosure(current, protocol.WorkItemClosureCompleted)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		pending, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), issue.IssueID)
		for _, review := range pending {
			closure.CompletionReviews = append(closure.CompletionReviews, protocol.CompletionReviewAcknowledgement{EventID: review.EventID, Reason: "The original result is accepted; new work remains"})
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, canonicalLifecycleControl(t, h, current, "close", closure), protocol.CodeInvalidTransition)
}

func TestCanonicalFrozenAcceptancePreservesControlFences(t *testing.T) {
	for _, change := range []string{"owner_handoff", "reviewer_handoff", "issue_pause", "issue_cancel", "review_pause", "review_cancel", "task_pause", "task_cancel", "spec_update"} {
		t.Run(change, func(t *testing.T) {
			h, runtime, issue, parent := frozenAcceptanceHarness(t)
			task := ownerArtifact(t, h, runtime, issue)
			var originalAction string
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT action_id FROM plan_actions WHERE task_id=? AND kind='accept' AND status='pending'`, task.TaskID).Scan(&originalAction)
			}); err != nil {
				t.Fatal(err)
			}
			issue = expandFrozenAcceptanceIssue(t, h, issue, task.TaskID)
			changeFrozenAcceptanceAncestor(t, h, parent)
			review := canonicalTaskIssue(t, h, ownerContract(t, h, task.TaskID).ReviewIssueID)
			switch change {
			case "owner_handoff", "reviewer_handoff":
				target := issue
				if change == "reviewer_handoff" {
					target = review
				}
				payload := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "The owner takes over this responsibility"}
				if op := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: target.IssueID}, payload, rev(target.Revision)), HostedIssueAssignment{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "issue_pause", "issue_cancel", "review_pause", "review_cancel":
				target := issue
				if strings.HasPrefix(change, "review_") {
					target = review
				}
				action := "pause"
				if strings.HasSuffix(change, "cancel") {
					action = "cancel"
				}
				if op := canonicalLifecycleControl(t, h, target, action, canonicalOwnerControl{ExpectedAssignmentEpoch: target.AssignmentEpoch}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "task_pause":
				if op := h.exec(owner, h.env("control.set_mode", taskTarget(task.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(task.TaskID).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "task_cancel":
				if op := h.exec(owner, h.env("tasks.cancel", taskTarget(task.TaskID), nil, rev(h.task(task.TaskID).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "spec_update":
				spec := h.storedSpec(task.TaskID, 1)
				spec.Scope = "Explicitly updated task agreement"
				if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if h.task(task.TaskID).CurrentArtifactID != "" {
					t.Fatal("a new task spec retained the old artifact")
				}
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				action, err := store.GetAction(t.Context(), tx.Q(), originalAction)
				if err != nil {
					return err
				}
				expectedStatus := protocol.ActionPending
				switch change {
				case "issue_cancel", "review_cancel", "task_cancel":
					expectedStatus = protocol.ActionCancelled
				case "spec_update":
					expectedStatus = protocol.ActionSuperseded
				}
				if action.Status != expectedStatus {
					t.Fatalf("control change %s left original action %s, want %s", change, action.Status, expectedStatus)
				}
				rows, err := store.ListClaimCandidates(t.Context(), tx.Q())
				if err != nil {
					return err
				}
				candidates, err := views.SchedulingCandidates(rows)
				if err != nil {
					return err
				}
				if err := views.ApplyIssueDependencies(t.Context(), tx.Q(), candidates); err != nil {
					return err
				}
				found := false
				for _, candidate := range candidates {
					if candidate.ActionID != originalAction {
						continue
					}
					found = true
					if reason := scheduler.HeldReason(candidate); reason == nil || reason.Message == "work item context review is incomplete" {
						t.Fatalf("control change %s lost its independent acceptance fence: %+v", change, reason)
					}
				}
				if found != (expectedStatus == protocol.ActionPending) {
					t.Fatalf("control change %s: original action status %s, candidate present %v", change, expectedStatus, found)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
