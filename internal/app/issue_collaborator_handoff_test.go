package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func collaboratorTask(t *testing.T, h *harness, issue protocol.Issue) string {
	t.Helper()
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "reviewer"}, Acceptance: protocol.AgentRequirement{CategoryID: "implementer"}}
	p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Collaborate on the existing Issue", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Independently verify this collaboration's result"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result taskResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.TaskID
}

func changeCollaboratorIssueDefinition(t *testing.T, h *harness, id string) {
	t.Helper()
	issue := canonicalTaskIssue(t, h, id)
	issue.Plan.Title += " with clarified progress"
	p := protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	if op := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: id}, p, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func handoffCollaboratorIssue(t *testing.T, h *harness, id string) protocol.Issue {
	t.Helper()
	issue := canonicalTaskIssue(t, h, id)
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Explicitly take over the original publishing responsibility"}
	if op := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: id}, p, rev(issue.Revision)), HostedIssueAssignment{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return canonicalTaskIssue(t, h, id)
}

func TestIssueCollaboratorKeepsPublishingResponsibilityAcrossOrdinaryChanges(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	before := h.count("work_items")
	task := collaboratorTask(t, h, issue)
	changeCollaboratorIssueDefinition(t, h, issue.IssueID)
	source, _ := h.say("Additional progress evidence for the existing authorized work")
	if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if impl.TaskID != task || impl.Execution.Identity.LocalID != "beta" || impl.Responsibility == nil || impl.Responsibility.AgentRef != issue.Assignee.Ref || impl.Responsibility.AssignmentEpoch != issue.AssignmentEpoch || impl.Responsibility.IssueRevision >= canonicalTaskIssue(t, h, issue.IssueID).Revision {
		t.Fatal("collaborator executor and publishing control origin were conflated", impl)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	changeCollaboratorIssueDefinition(t, h, issue.IssueID)
	accept := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	if accept.Execution.Identity.LocalID != "alpha" || accept.Responsibility == nil || accept.Responsibility.IssueID != issue.IssueID || accept.Responsibility.AssignmentEpoch != issue.AssignmentEpoch || accept.Responsibility.AgentRef != issue.Assignee.Ref {
		t.Fatal("ordinary context edit replaced the original control responsibility", accept)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).Lifecycle != protocol.TaskCompleted || h.count("work_items") != before {
		t.Fatal("ordinary collaboration failed or required a new child Issue", h.task(task))
	}
}

func TestFault_IssueCollaboratorHandoffRetainsOldFactsAndAllowsRealSuccessor(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			h, runtime, issue := issueOwnerHarness(t)
			task := collaboratorTask(t, h, issue)
			as := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
			if role == protocol.RoleAccept {
				if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				as = ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
			}
			handoffCollaboratorIssue(t, h, issue.IssueID)
			operation, cmd, payload := protocol.OpRunsSubmitArtifact, Command(SubmitArtifact{}), any(testArtifact(h.task(task).WorkspaceName))
			table := "artifacts"
			if role == protocol.RoleAccept {
				operation, cmd, payload, table = protocol.OpAcceptancesSubmit, SubmitAcceptance{}, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, "acceptances"
			}
			env := h.env(operation, runTarget(as.RunID), payload, nil)
			op := h.exec(runPrincipal(as.RunID, as.Role), env, cmd)
			if op.Status != protocol.OpSucceeded || h.count(table) != 1 || h.task(task).ControlMode != protocol.ControlPaused || h.task(task).Lifecycle == protocol.TaskCompleted || role == protocol.RoleImplement && h.task(task).Lifecycle == protocol.TaskAwaitingAcceptance {
				t.Fatal("old collaborator result advanced the handed-over responsibility", op, h.task(task))
			}
			if replay := h.exec(runPrincipal(as.RunID, as.Role), env, cmd); replay.Status != op.Status || string(replay.Result) != string(op.Result) || h.count(table) != 1 {
				t.Fatal("late original receipt did not replay exactly", replay)
			}
			if role == protocol.RoleAccept {
				return
			}
			// Finish the explicit takeover and publish a real new responsibility;
			// do not rewrite the original Task's cost, result or owner epoch.
			current := canonicalTaskIssue(t, h, issue.IssueID)
			finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, HandoffID: current.PendingHandoffID, Reason: "Original coordination and worker result are reconciled"}
			if op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(current.Revision)), HostedIssueAssignment{Finish: true}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			current = canonicalTaskIssue(t, h, issue.IssueID)
			request := protocol.RequestIssueManagerPayload{CategoryID: "reviewer", AgentID: "linux-1/beta", ExpectedAssignmentEpoch: current.AssignmentEpoch, Replace: true, Reason: "The new owner handles the remaining work from retained Issue evidence"}
			if op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, request, rev(current.Revision)), RequestIssueOwner{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			current = canonicalTaskIssue(t, h, issue.IssueID)
			current = issueAdmissionStatus(t, h, current, protocol.IssueTodo, nil)
			manager := ownerWorkerClaim(t, h, runtime, protocol.RoleIssueManager)
			if manager.Execution.Identity.LocalID != "beta" {
				t.Fatal("replacement owner did not acquire its real current identity", manager)
			}
			stopOwnerClaim(t, h, manager)
			current = canonicalTaskIssue(t, h, issue.IssueID)
			spec := testSpec
			spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}}
			create := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Title: "Complete the actual remaining work", Spec: spec}
			created := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, create, rev(current.Revision)), CanonicalOwnerTaskCreate{})
			if created.Status != protocol.OpSucceeded {
				t.Fatal(created)
			}
			var successor taskResult
			if err := json.Unmarshal(created.Result, &successor); err != nil {
				t.Fatal(err)
			}
			fresh := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
			if fresh.TaskID != successor.TaskID || fresh.Execution.Identity.LocalID != "alpha" || fresh.Responsibility == nil || fresh.Responsibility.AgentRef != "linux-1/beta" || fresh.Responsibility.AssignmentEpoch != current.AssignmentEpoch {
				t.Fatal("legitimate successor could not use the new control responsibility", fresh)
			}
			if op := h.runAs(fresh, protocol.OpRunsSubmitArtifact, testArtifact(h.task(successor.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if h.task(successor.TaskID).Lifecycle != protocol.TaskCompleted || h.task(task).ControlMode != protocol.ControlPaused || h.count("artifacts") != 2 || h.run(as.RunID).ObservedState != protocol.RunFinished || h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatal("new work was blocked or old execution facts were rewritten", h.task(successor.TaskID), h.task(task))
			}
		})
	}
}

func TestIssueCollaboratorHandoffBeforeClaimCannotAcquireOldTask(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	task := collaboratorTask(t, h, issue)
	current := handoffCollaboratorIssue(t, h, issue.IssueID)
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, HandoffID: current.PendingHandoffID, Reason: "The original coordination is stopped; enable the new owner"}
	if op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(current.Revision)), HostedIssueAssignment{Finish: true}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for range 3 {
		as := ownerClaim(t, h, runtime)
		if as.TaskID == task {
			t.Fatal("old responsibility was newly executed after explicit handoff", as)
		}
		if as.Role == protocol.RoleIssueManager {
			stopOwnerClaim(t, h, as)
		}
	}
	if h.task(task).Lifecycle != protocol.TaskQueued {
		t.Fatal("unclaimed old Task was rewritten instead of retained", h.task(task))
	}
}
