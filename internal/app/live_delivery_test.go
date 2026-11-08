package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func liveReviewedDeliveryFixture(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Issue, protocol.Assignment, protocol.IssueDeliveryCandidate) {
	t.Helper()
	h, runtime, issue, source, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, source, acceptance)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	spec := h.storedSpec(source, 1)
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var input protocol.OwnerTaskSpec
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{Title: "Continue the same complete delivery", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: input}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	impl, ok := h.claimInputRun(runtime)
	if !ok || impl.TaskID != created.TaskID || impl.Role != protocol.RoleImplement {
		t.Fatal(impl)
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: candidate.DeliveryID, TaskID: impl.TaskID}))
	if !linked.Valid {
		t.Fatal("identical business agreement unexpectedly lost its real whole-PR review", linked)
	}
	return h, runtime, canonicalTaskIssue(t, h, issue.IssueID), impl, candidate
}

func prepareLiveDeliveryForTest(t *testing.T, h *harness, issue, task string, candidate protocol.IssueDeliveryCandidate) protocol.IssueDeliveryDuty {
	t.Helper()
	op := updateDeliveryForTest(t, h, issue, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: task, ExpectedTaskControlRevision: rev(h.task(task).ControlRevision), Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{Reason: "Continue this live Task using the actual reviewed PR and Issue authorization"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return deliveryDutyForTest(t, h, issue, task)
}

func TestLiveDeliveryBindsSameRunningTaskWithoutArtificialResultOrSuccessor(t *testing.T) {
	h, runtime, issue, impl, candidate := liveReviewedDeliveryFixture(t)
	before := h.task(impl.TaskID)
	tasks, runs, artifacts := h.count("tasks"), h.count("runs"), h.count("artifacts")
	duty := prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, candidate)
	if duty.Status != "authorized" || duty.AcceptanceID != "" || duty.ArtifactID != "" || duty.EventID == "" {
		t.Fatal("live source was promoted to a fictional result", duty)
	}
	p := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(before.ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Execute the exact authorized candidate within the original live Task"}}
	op := updateDeliveryForTest(t, h, issue.IssueID, p)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	duty = deliveryDutyForTest(t, h, issue.IssueID, impl.TaskID)
	if duty.Status != "executing" || duty.ExecutionTaskID != impl.TaskID || h.count("tasks") != tasks || h.count("runs") != runs || h.count("artifacts") != artifacts {
		t.Fatal("binding manufactured a phase Task, Run or result", duty)
	}
	if after := h.task(impl.TaskID); after.SpecVersion != before.SpecVersion || after.ControlRevision != before.ControlRevision || after.CurrentArtifactID != "" {
		t.Fatal("binding rewrote the frozen Task", after)
	}
	context, err := h.app.ReadRunContext(t.Context(), runPrincipal(impl.RunID, impl.Role), protocol.IssueMaterialsQuery{Kind: "delivery_authorization"})
	if err != nil || context.Materials.DeliveryAuthorization == nil || !context.Materials.DeliveryAuthorization.Valid || context.Materials.DeliveryAuthorization.SourceTaskID != impl.TaskID {
		t.Fatal("ordinary context cannot read this Task's real delivery binding", context, err)
	}
	if h.storedSpec(impl.TaskID, 1).Observation != nil {
		t.Fatal("fixture relied on a special observation permission")
	}
	// The provider receipt is an external fact. The original Run then submits
	// its actual final output and receives the unchanged independent verdict.
	candidate.TargetSHA = strings.Repeat("c", 40)
	observeDeliveryForTest(t, h, issue.IssueID, candidate, "merged", candidate.TargetSHA)
	result := testArtifact(before.WorkspaceName)
	result.Mounts[0].Changed, result.Mounts[0].CommitSHA = true, candidate.HeadSHA
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.TaskID != impl.TaskID || accept.Role != protocol.RoleAccept {
		t.Fatal("final independent acceptance did not retain the original Task", accept)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(impl.TaskID)
	if !h.releaseResource(impl.TaskID, resource.WorkspaceID) {
		t.Fatal("release actual accepted resource")
	}
	op = updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "complete", TaskID: impl.TaskID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Actual whole-PR integration and original Task acceptance are complete"}})
	if op.Status != protocol.OpSucceeded || h.count("issue_delivery_duties") != 2 || h.count("issue_delivery_executions") != 1 {
		t.Fatal("live delivery did not reach its actual endpoint", op)
	}
}

func TestLiveDeliveryBindingRetainsActualAuthorityAndTaskCAS(t *testing.T) {
	for _, changed := range []string{"stale_control", "wrong_task", "missing_review", "revoked_policy"} {
		t.Run(changed, func(t *testing.T) {
			h, _, issue, impl, candidate := liveReviewedDeliveryFixture(t)
			prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, candidate)
			p := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(h.task(impl.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Bind only real authority"}}
			switch changed {
			case "stale_control":
				*p.ExpectedTaskControlRevision++
			case "wrong_task":
				other := createCanonicalOwnerTestIssue(t, h)
				p.ExecutionTaskID = createCanonicalTaskForTest(t, h, other)
			case "missing_review":
				sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "unlink", DeliveryID: candidate.DeliveryID, TaskID: impl.TaskID}))
			case "revoked_policy":
				setPRPolicyForTest(t, h, issue.IssueID, "manual")
			}
			before := h.count("issue_delivery_executions")
			current := canonicalTaskIssue(t, h, issue.IssueID)
			p.ExpectedAssignmentEpoch = current.AssignmentEpoch
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(current.Revision)), UpdateIssueDelivery{})
			code := protocol.CodeOf(err)
			if op.Error != nil {
				code = op.Error.Code
			}
			want := map[string]protocol.Code{"stale_control": protocol.CodeStaleRevision, "wrong_task": protocol.CodeForbidden, "missing_review": protocol.CodeInvalidTransition, "revoked_policy": protocol.CodeForbidden}[changed]
			if code != want || h.count("issue_delivery_executions") != before {
				t.Fatal("binding bypassed concrete authority", op, err)
			}
		})
	}
}

func TestLiveDeliveryBindingDoesNotAcquireAnotherRunningDeploymentsTarget(t *testing.T) {
	h, _, cases := twoDeliveryCases(t, true)
	first, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal(first)
	}
	second, ok, _ := h.claim("linux-2")
	if !ok {
		t.Fatal("ordinary work could not run beside a different delivery", second)
	}
	if first.TaskID == cases[1].execution {
		first, second = second, first
	}
	if first.TaskID != cases[0].execution || second.TaskID != cases[1].execution {
		t.Fatal("did not claim the two actual tasks", first, second)
	}
	p := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: cases[1].source, ExecutionTaskID: second.TaskID, ExpectedTaskControlRevision: rev(h.task(second.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Continue the exact approved deployment within this live Task"}}
	before := h.count("issue_delivery_executions")
	current := canonicalTaskIssue(t, h, cases[1].issue.IssueID)
	p.ExpectedAssignmentEpoch = current.AssignmentEpoch
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: current.IssueID}, p, rev(current.Revision)), UpdateIssueDelivery{})
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome || h.count("issue_delivery_executions") != before {
		t.Fatal("binding double-acquired the controlled target", op, err)
	}
	row := h.task(first.TaskID)
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(first.TaskID), map[string]any{}, rev(row.ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(first, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Original executor confirms it stopped without starting an upgrade"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = updateDeliveryForTest(t, h, cases[1].issue.IssueID, p)
	if op.Status != protocol.OpSucceeded || h.count("issue_delivery_executions") != before+1 || h.count("upgrades") != 0 {
		t.Fatal("confirmed target release did not permit the exact same live binding", op)
	}
}
