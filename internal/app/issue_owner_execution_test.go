package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func issueOwnerHarness(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue) {
	t.Helper()
	h, r, p, _ := inventorySetup(t)
	p.FormatVersion = 2
	p.Runtimes[0].CLI = "codex"
	p.Agents[0].Categories = []string{"implementer"}
	p.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "owner-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager, protocol.CapabilityIssueOwner)
	r.JournalVersion = 13
	for _, a := range p.Agents {
		pin := protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "owner-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: pin})
		pin.Purpose, pin.BackendID = "issue_manager", ""
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: pin})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", AgentID: "linux-1/alpha", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Accept responsibility with an ordinary implementation Agent"}, rev(issue.Revision)), RequestIssueOwner{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerClaim(t, h, r)
	if as.Role != protocol.RoleIssueManager || as.Execution == nil || as.Execution.Identity.LocalID != "alpha" {
		t.Fatalf("ordinary worker did not become owner: %+v", as)
	}
	stopOwnerClaim(t, h, as)
	return h, r, canonicalTaskIssue(t, h, issue.IssueID)
}

func ownerClaim(t *testing.T, h *harness, r *protocol.RunnerRuntime) protocol.Assignment {
	t.Helper()
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result claimResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Assignment
}

func stopOwnerClaim(t *testing.T, h *harness, as protocol.Assignment) {
	t.Helper()
	op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "The coordination turn released its execution slot", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func ownerWorkerClaim(t *testing.T, h *harness, r *protocol.RunnerRuntime, role protocol.RunRole) protocol.Assignment {
	t.Helper()
	for range 5 {
		as := ownerClaim(t, h, r)
		if as.Role == role {
			return as
		}
		if as.Role != protocol.RoleIssueManager {
			t.Fatalf("expected %s, got %+v", role, as)
		}
		stopOwnerClaim(t, h, as)
	}
	t.Fatal("owner did not release work to its execution adapter")
	return protocol.Assignment{}
}

func createOwnerExecution(t *testing.T, h *harness, issue protocol.Issue, implementation string) protocol.Operation {
	t.Helper()
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	spec := testSpec
	spec.ImplementationContext = "PRIVATE_OWNER_IMPLEMENTATION_DESIGN"
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: implementation}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Execute own responsibility", Spec: spec}
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if err != nil {
		return protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeOf(err), "execution rejected")}
	}
	return op
}

func TestIssueOwnerExecutesWithSameIdentityAndReviewChild(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	before := h.count("work_items")
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if impl.Execution.Identity.LocalID != "alpha" || impl.Responsibility == nil || impl.Responsibility.IssueID != issue.IssueID {
		t.Fatalf("self execution lost responsibility: %+v", impl)
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if accept.Execution.Identity.LocalID != "beta" || accept.Responsibility == nil || accept.Responsibility.IssueID == issue.IssueID {
		t.Fatalf("reviewer has no distinct Issue: %+v", accept)
	}
	if strings.Contains(string(accept.Spec), "PRIVATE_OWNER_IMPLEMENTATION_DESIGN") {
		t.Fatal("implementation design leaked into independent acceptance")
	}
	review := canonicalTaskIssue(t, h, accept.Responsibility.IssueID)
	if review.ParentIssueID != issue.IssueID || review.Assignee.Ref != "linux-1/beta" || strings.Contains(review.Plan.Intent+review.Plan.Scope, "PRIVATE_OWNER_IMPLEMENTATION_DESIGN") {
		t.Fatalf("review responsibility=%+v", review)
	}
	verdict := h.env(protocol.OpAcceptancesSubmit, runTarget(accept.RunID), protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, nil)
	result := h.exec(runPrincipal(accept.RunID, accept.Role), verdict, SubmitAcceptance{})
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	if replay := h.exec(runPrincipal(accept.RunID, accept.Role), verdict, SubmitAcceptance{}); string(replay.Result) != string(result.Result) || h.count("work_items") != before {
		t.Fatal("replay created another responsibility", replay)
	}
	if review = canonicalTaskIssue(t, h, review.IssueID); review.State != protocol.IssueClosed || review.Closure == nil {
		t.Fatal("bounded review responsibility did not settle", review)
	}
	if parent := canonicalTaskIssue(t, h, issue.IssueID); parent.State != protocol.IssueActive {
		t.Fatal("review completion implicitly closed parent", parent)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.CanonicalIssue(t.Context(), tx.Q(), review.IssueID, 0)
		if err == nil && (len(view.ResponsibilityRuns) != 1 || view.ResponsibilityRuns[0].RunID != accept.RunID) {
			t.Fatal("review trace lost its run", view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueOwnerCannotDispatchAnotherAgentWithoutChild(t *testing.T) {
	h, _, issue := issueOwnerHarness(t)
	beforeTasks, beforeIssues := h.count("tasks"), h.count("work_items")
	op := createOwnerExecution(t, h, issue, "reviewer")
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeForbidden {
		t.Fatal("cross-Agent work bypassed child Issue", op)
	}
	if h.count("tasks") != beforeTasks || h.count("work_items") != beforeIssues {
		t.Fatal("rejected delegation left partial work")
	}
}

func TestFault_IssueOwnerHandoffRetainsOldResultWithoutAdvancing(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	_ = json.Unmarshal(op.Result, &task)
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	assigned := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Human intervention takes over responsibility"}, rev(issue.Revision)), HostedIssueAssignment{})
	if assigned.Status != protocol.OpSucceeded {
		t.Fatal(assigned)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("old result was lost instead of retained", op)
	}
	if h.count("artifacts") != 1 || h.task(task.TaskID).ControlMode != protocol.ControlPaused || h.task(task.TaskID).Lifecycle == protocol.TaskAwaitingAcceptance {
		t.Fatal("old execution advanced new responsibility", h.task(task.TaskID))
	}
}

func TestIssueOwnerDelegatesChildAndChildCanExecute(t *testing.T) {
	h, r, parent := issueOwnerHarness(t)
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Investigate the delegated part", Project: parent.Plan.Project, Intent: "Produce the requested investigation result", Stages: []protocol.IssueStage{}}, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), Owner: &protocol.IssueOwnerRequest{CategoryID: "reviewer", AgentID: "linux-1/beta", Reason: "Delegate a bounded part to another Agent"}}
	env := h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil)
	op := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var child protocol.Issue
	if err := json.Unmarshal(op.Result, &child); err != nil {
		t.Fatal(err)
	}
	before := h.count("work_items")
	if replay := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "create"}); string(replay.Result) != string(op.Result) || h.count("work_items") != before {
		t.Fatal("child delegation was repeated", replay)
	}
	as := ownerClaim(t, h, r)
	for range 4 {
		if as.Manager != nil && as.Manager.IssueID == child.IssueID {
			break
		}
		if as.Role != protocol.RoleIssueManager {
			t.Fatal("unexpected execution while waiting for child owner", as)
		}
		stopOwnerClaim(t, h, as)
		as = ownerClaim(t, h, r)
	}
	if as.Manager == nil || as.Manager.IssueID != child.IssueID || as.Execution.Identity.LocalID != "beta" {
		t.Fatal("child did not route to its requested owner", as)
	}
	stopOwnerClaim(t, h, as)
	child = canonicalTaskIssue(t, h, child.IssueID)
	if child.ParentIssueID != parent.IssueID || child.Assignee.Ref != "linux-1/beta" {
		t.Fatal(child)
	}
	if op := createOwnerExecution(t, h, child, "reviewer"); op.Status != protocol.OpSucceeded {
		t.Fatal("child owner could not execute itself", op)
	}
	if got := canonicalTaskIssue(t, h, parent.IssueID); got.Assignee != parent.Assignee || got.AssignmentEpoch != parent.AssignmentEpoch {
		t.Fatal("delegation transferred parent responsibility", got)
	}
}

func TestIssueOwnerRequiresCapableRunnerWithoutChangingLegacyProtocol(t *testing.T) {
	h, r, parent := issueOwnerHarness(t)
	if op := createOwnerExecution(t, h, parent, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Queued child", Project: parent.Plan.Project, Intent: "Wait for a capable owner adapter", Stages: []protocol.IssueStage{}}, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), Owner: &protocol.IssueOwnerRequest{CategoryID: "implementer", Reason: "Publish a new responsibility contract"}}
	if op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), CanonicalOwnerIssueCommand{Action: "create"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := *r
	old.Capabilities = nil
	for _, capability := range r.Capabilities {
		if capability != protocol.CapabilityIssueOwner {
			old.Capabilities = append(old.Capabilities, capability)
		}
	}
	if as := ownerClaim(t, h, &old); as.RunID != "" {
		t.Fatal("runner without the optional owner capability claimed new work", as)
	}
}

func TestIssueOwnerTaskUpdateCannotRemoveOrReplaceSelfIdentity(t *testing.T) {
	h, _, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	_ = json.Unmarshal(op.Result, &task)
	for _, erase := range []bool{false, true} {
		spec := h.storedSpec(task.TaskID, 1)
		if erase {
			spec.AgentSelection = nil
		} else {
			selection := *spec.AgentSelection
			selection.Implementation = protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "linux-1/beta"}
			spec.AgentSelection = &selection
		}
		result, err := h.app.Execute(t.Context(), owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
		if err == nil && result.Status != protocol.OpFailed {
			t.Fatal("task update bypassed owner identity", result)
		}
		if h.task(task.TaskID).SpecVersion != 1 {
			t.Fatal("rejected change replaced immutable self-execution")
		}
	}
}

func ownerContract(t *testing.T, h *harness, task string) store.TaskOwnerContract {
	t.Helper()
	var contract store.TaskOwnerContract
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		contract, err = store.GetTaskOwnerContract(t.Context(), tx.Q(), task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return contract
}

func ownerArtifact(t *testing.T, h *harness, r *protocol.RunnerRuntime, issue protocol.Issue) taskResult {
	t.Helper()
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	_ = json.Unmarshal(op.Result, &task)
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return task
}

func TestFault_IssueOwnerPendingReviewPreservesCompletedHumanHandoff(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	review := canonicalTaskIssue(t, h, ownerContract(t, h, task.TaskID).ReviewIssueID)
	if op := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: review.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: review.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Take over this pending review manually"}, rev(review.Revision)), HostedIssueAssignment{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	review = canonicalTaskIssue(t, h, review.IssueID)
	if op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: review.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: review.AssignmentEpoch, HandoffID: review.PendingHandoffID, Reason: "The previous responsibility has no active executor"}, rev(review.Revision)), HostedIssueAssignment{Finish: true}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for range 5 {
		as := ownerClaim(t, h, r)
		if as.RunID == "" {
			break
		}
		if as.Role != protocol.RoleIssueManager {
			t.Fatal("pending acceptance undid human handoff", as)
		}
		stopOwnerClaim(t, h, as)
	}
	if got := canonicalTaskIssue(t, h, review.IssueID); got.Assignee.Kind != protocol.IssueAssigneeOwner || got.Assignee.Ref != owner.Subject {
		t.Fatal("human review ownership was overwritten", got)
	}
}

func TestFault_IssueOwnerCancellationSettlesOnlyReconciledReview(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_claim", true: "active_execution"}[active], func(t *testing.T) {
			h, r, issue := issueOwnerHarness(t)
			op := createOwnerExecution(t, h, issue, "implementer")
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var task taskResult
			_ = json.Unmarshal(op.Result, &task)
			contract := ownerContract(t, h, task.TaskID)
			var impl protocol.Assignment
			if active {
				impl = ownerWorkerClaim(t, h, r, protocol.RoleImplement)
			}
			if op := h.exec(owner, h.env("tasks.cancel", taskTarget(task.TaskID), nil, rev(h.task(task.TaskID).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if active {
				if got := canonicalTaskIssue(t, h, contract.ReviewIssueID); got.State != protocol.IssueActive {
					t.Fatal("review settled before physical execution reconciliation", got)
				}
				if op := h.runAs(impl, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Confirmed the cancelled attempt stopped"}, Release{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if got := canonicalTaskIssue(t, h, contract.ReviewIssueID); got.State != protocol.IssueCancelled {
				t.Fatal("generated review orphaned cancelled responsibility", got)
			}
		})
	}
}

func TestFault_IssueOwnerReviewEndpointPreservesActiveChild(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	review := canonicalTaskIssue(t, h, accept.Responsibility.IssueID)
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Additional review follow-up", Project: issue.Plan.Project, Intent: "Keep the manually requested responsibility open", Stages: []protocol.IssueStage{}}, ParentIssueID: review.IssueID, ExpectedParentRevision: rev(review.Revision)}
	if op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), CanonicalOwnerIssueCommand{Action: "create"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal("verdict was lost because responsibility cannot close", op)
	}
	if h.task(task.TaskID).Lifecycle != protocol.TaskCompleted || h.count("acceptances") != 1 {
		t.Fatal("independent verdict was not retained")
	}
	if got := canonicalTaskIssue(t, h, review.IssueID); got.State != protocol.IssueActive || got.Closure != nil {
		t.Fatal("review closed over an active child", got)
	}
}

func TestFault_IssueOwnerReviewCancellationStopsAndReconcilesItsRun(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	review := canonicalTaskIssue(t, h, accept.Responsibility.IssueID)
	control := protocol.IssueControlPayload{ExpectedAssignmentEpoch: review.AssignmentEpoch, Reason: "Withdraw this review attempt"}
	op := h.exec(owner, h.env("issues.cancel", protocol.Target{Type: "issue", ID: review.IssueID}, control, rev(review.Revision)), CanonicalOwnerIssueCommand{Action: "cancel"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	review = canonicalTaskIssue(t, h, review.IssueID)
	if h.run(accept.RunID).DesiredState != protocol.DesiredStopped || h.lease(accept.RunID).Status != store.LeaseActive {
		t.Fatal("cancellation did not request a stop or fabricated its receipt")
	}
	finish := protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: review.AssignmentEpoch, Reason: "Finish only after confirmed stop"}}
	blocked := h.exec(owner, h.env("issues.finish_cancel", protocol.Target{Type: "issue", ID: review.IssueID}, finish, rev(review.Revision)), CanonicalOwnerIssueCommand{Action: "finish_cancel"})
	expectWorkCode(t, blocked, protocol.CodeInvalidTransition)
	if op := h.runAs(accept, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Confirmed the withdrawn review stopped"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.exec(owner, h.env("issues.finish_cancel", protocol.Target{Type: "issue", ID: review.IssueID}, finish, rev(review.Revision)), CanonicalOwnerIssueCommand{Action: "finish_cancel"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task.TaskID).Lifecycle == protocol.TaskCancelled || canonicalTaskIssue(t, h, issue.IssueID).State != protocol.IssueActive {
		t.Fatal("cancelling the review cancelled its parent's implementation responsibility")
	}
}

func TestFault_IssueOwnerPreservesVerdictAfterReviewPrerequisiteReopens(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["Address the remaining case"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	prerequisite := createCanonicalOwnerTestIssue(t, h)
	closure := canonicalLifecycleClosure(prerequisite, protocol.WorkItemClosureCompleted)
	if op := h.exec(owner, h.env("issues.close", protocol.Target{Type: "issue", ID: prerequisite.IssueID}, closure, rev(prerequisite.Revision)), CanonicalOwnerIssueCommand{Action: "close"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	prerequisite = canonicalTaskIssue(t, h, prerequisite.IssueID)
	review := canonicalTaskIssue(t, h, accept.Responsibility.IssueID)
	dependencies := protocol.SetIssueDependenciesPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: review.AssignmentEpoch, Reason: "Require the independently completed prerequisite"}, DependsOn: []string{prerequisite.IssueID}}
	if op := h.exec(owner, h.env("issues.dependencies", protocol.Target{Type: "issue", ID: review.IssueID}, dependencies, rev(review.Revision)), CanonicalOwnerIssueCommand{Action: "dependencies"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept = ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if op := h.exec(owner, h.env("issues.reopen", protocol.Target{Type: "issue", ID: prerequisite.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: prerequisite.AssignmentEpoch, Reason: "The prerequisite needs additional work"}, rev(prerequisite.Revision)), CanonicalOwnerIssueCommand{Action: "reopen"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal("an unmet responsibility endpoint rolled back the independent verdict", op)
	}
	if h.task(task.TaskID).Lifecycle != protocol.TaskCompleted || h.count("acceptances") != 2 || canonicalTaskIssue(t, h, review.IssueID).State != protocol.IssueActive {
		t.Fatal("verdict and responsibility endpoint were conflated")
	}
}

func TestFault_IssueOwnerRefreshRejectsChangedExecutionConfiguration(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	_ = json.Unmarshal(op.Result, &task)
	var projection protocol.AgentInventoryProjection
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		inventory, err := store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		projection = inventory.Projection
		return err
	}); err != nil {
		t.Fatal(err)
	}
	projection.Agents[0].Revision++
	projection.Agents[0].RequestedModel = "new-model"
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, projection, hash, 2, 1, "owner-updated"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := h.storedSpec(task.TaskID, 1)
	selection := *spec.AgentSelection
	selection.Implementation.Model = ""
	spec.AgentSelection = &selection
	result, err := h.app.Execute(t.Context(), owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	if err != nil || result.Status != protocol.OpFailed || result.Error.Code != protocol.CodeStaleRevision || h.task(task.TaskID).SpecVersion != 1 {
		t.Fatal("an incompatible refresh left a successful but unschedulable contract", result, err)
	}
}

func TestIssueOwnerTaskRefreshCannotReplaceClaimedReviewer(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["Revise the implementation"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := h.storedSpec(task.TaskID, 1)
	selection := *spec.AgentSelection
	selection.Acceptance.AgentID = "linux-1/alpha"
	spec.AgentSelection = &selection
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
	if h.task(task.TaskID).SpecVersion != 1 || canonicalTaskIssue(t, h, accept.Responsibility.IssueID).Assignee.Ref != "linux-1/beta" {
		t.Fatal("task refresh replaced review responsibility")
	}
}

func TestFault_IssueOwnerHandoffFencesQuestionsRetriesAndFailure(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityWorkerQuestions)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	_ = json.Unmarshal(op.Result, &task)
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	question := protocol.AskOwnerPayload{QuestionKey: "original-question", Question: "Choose the original execution's next step", Options: []string{"continue", "stop"}}
	if op := h.runAs(impl, protocol.OpRunsAskOwner, question, AskOwner{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Human takeover fences this execution's business authority"}, rev(issue.Revision)), HostedIssueAssignment{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "The coordinator's earlier native operations are reconciled"}, rev(issue.Revision)), HostedIssueAssignment{Finish: true}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	questions, err := h.app.WorkerQuestions(t.Context(), runPrincipal(impl.RunID, impl.Role))
	if err != nil || len(questions) != 1 || questions[0].Status != store.DecisionCancelled || len(questions[0].Answer) != 0 {
		t.Fatal("old question remained actionable after takeover", questions, err)
	}
	question.QuestionKey = "new-question-after-handoff"
	expectWorkCode(t, h.runAs(impl, protocol.OpRunsAskOwner, question, AskOwner{}), protocol.CodeStaleRevision)
	retry := h.exec(owner, h.env("actions.create", taskTarget(task.TaskID), protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 2}, rev(h.task(task.TaskID).ControlRevision)), CreateAction{})
	expectWorkCode(t, retry, protocol.CodeStaleRevision)
	if op := h.runAs(impl, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("decisions") != 1 || h.task(task.TaskID).ControlMode != protocol.ControlPaused || h.run(impl.RunID).ObservedState != protocol.RunFailed || h.lease(impl.RunID).Status != store.LeaseReleased {
		t.Fatal("failure of a fenced run created fresh business obligations", h.task(task.TaskID))
	}
}

func TestIssueOwnerBoundedReviewDoesNotCloseExpandedHumanScope(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["Correct the artifact"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	review := canonicalTaskIssue(t, h, accept.Responsibility.IssueID)
	plan := review.Plan
	plan.Intent = "Also investigate a newly reported concern after reviewing the artifact"
	if op := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: review.IssueID}, protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: review.AssignmentEpoch}, rev(review.Revision)), CanonicalOwnerIssueCommand{Action: "update"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept = ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task.TaskID).Lifecycle != protocol.TaskCompleted || canonicalTaskIssue(t, h, review.IssueID).State != protocol.IssueActive {
		t.Fatal("the bounded verdict silently completed the user's added responsibility")
	}
}
