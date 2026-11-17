package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Only inventory and provider observations are fixture boundaries. Task creation,
// scheduler selection, Run startup and immutable report submission use real commands.
func TestSharedReviewCategoryTaskBindsActualExecution(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "running"
		if completed {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			h, runtime, issue, source, _, _ := sharedReviewFixture(t)
			linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			task := createSharedReviewerTaskForTest(t, h, issue.IssueID, protocol.AgentRequirement{CategoryID: "reviewer"})
			if h.storedSpec(task, 1).AgentSelection.Implementation.AgentID != "" {
				t.Fatal("producer supplied an identity instead of work requirements")
			}
			run, ok := h.claimInputRun(runtime)
			if !ok || run.TaskID != task || run.Execution == nil || run.Execution.Identity.LocalID != "beta" {
				t.Fatal(run)
			}
			if completed {
				submitSharedReviewForTest(t, h, run, linked, "passed")
			}
			before := h.task(task)
			bound := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task, ExpectedHeadSHA: linked.HeadSHA, ExpectedBaseSHA: linked.BaseSHA, ExpectedRequirementsDigest: linked.RequirementsDigest}))
			round := bound.Rounds[0]
			expectedRun := ""
			if completed {
				expectedRun = run.RunID
			}
			if round.RunID != expectedRun || round.Reviewer != "linux-1/beta" || round.ReviewerProfile == nil || *round.ReviewerProfile != run.Execution.Profile || round.SpecVersion != before.SpecVersion || round.ImplementGeneration != before.ImplementGeneration {
				t.Fatal("round did not freeze the actual selected execution", round, run)
			}
			if got := h.task(task); got.ControlRevision != before.ControlRevision || got.SpecVersion != before.SpecVersion || got.ImplementGeneration != before.ImplementGeneration || got.Lifecycle != before.Lifecycle {
				t.Fatal("binding changed the original execution agreement", got, before)
			}
			if !completed {
				submitSharedReviewForTest(t, h, run, bound, "passed")
			}
			artifact := h.task(task).CurrentArtifactID
			// Continuing after the genuine result must not make record consume a new run.
			releaseSharedReviewerForTest(t, h, task)
			if op := continueTaskForTest(t, h, task, "Preserve the original report while continuing the same responsibility"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			result := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !result.Valid || result.Status != "passed" || result.Rounds[0].RunID != run.RunID || result.Rounds[0].ArtifactID != artifact {
				t.Fatal("original category report did not establish the formal review", result)
			}
		})
	}
}

func TestSharedReviewCategoryTaskWaitsForCurrentExecution(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID, protocol.AgentRequirement{CategoryID: "reviewer"})
	for _, phase := range []string{"unselected", "continued"} {
		if phase == "continued" {
			executeSharedReviewForTest(t, h, runtime, linked, task, "passed")
			releaseSharedReviewerForTest(t, h, task)
			if op := continueTaskForTest(t, h, task, "A new generation requires its own actual execution"); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}
		before := h.count("shared_delivery_review_history")
		expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}), protocol.CodeInvalidTransition)
		if h.count("shared_delivery_review_history") != before {
			t.Fatal("unselected generation spent a review round", phase)
		}
	}
}

func TestSharedReviewCategoryTaskRejectsSupersededSpec(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID, protocol.AgentRequirement{CategoryID: "reviewer"})
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run)
	}
	current := h.task(task)
	spec := h.storedSpec(task, current.SpecVersion)
	spec.Goal += "; use the updated review instructions"
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &spec}, rev(current.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.count("shared_delivery_review_history")
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}), protocol.CodeInvalidTransition)
	if h.count("shared_delivery_review_history") != before {
		t.Fatal("superseded Run was used for a new spec")
	}
}

func TestSharedReviewCategoryTaskRetainsIndependentReviewerCheck(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID, protocol.AgentRequirement{CategoryID: "reviewer"})
	// This report Task also contributes to the linked delivery. Its real executor
	// cannot independently review its own contribution, even with the right category.
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: task}))
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run)
	}
	before := h.count("shared_delivery_review_history")
	current := canonicalTaskIssue(t, h, issue.IssueID)
	payload := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "Check independent review of every linked contribution"}, Action: "start", DeliveryID: "pr", ReviewerTaskID: task}
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(current.Revision)), ReviewIssueDelivery{})
	if technicalWaitErrorCode(op, err) != protocol.CodeForbidden {
		t.Fatal("actual contributor was allowed to review itself", op, err)
	}
	if h.count("shared_delivery_review_history") != before {
		t.Fatal("self review spent a formal round")
	}
}

func TestSharedReviewReportBindsSameGenerationContinuation(t *testing.T) {
	for _, category := range []bool{false, true} {
		name := "named"
		if category {
			name = "category"
		}
		t.Run(name, func(t *testing.T) {
			h, runtime, issue, source, _, _ := sharedReviewFixture(t)
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityExternalWaits)
			runtime.Sequence++
			if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			requirement := protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "linux-1/beta"}
			if category {
				requirement.AgentID = ""
			}
			task := createSharedReviewerTaskForTest(t, h, issue.IssueID, requirement)
			run, ok := h.claimInputRun(runtime)
			if !ok || run.TaskID != task {
				t.Fatal(run)
			}
			bound := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
			if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			wait := waitForTask(t, h, run, source, 0)
			if op := h.runAs(run, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID, ThreadID: "review-session"}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			next, ok := h.claimInputRun(runtime)
			if !ok || next.TaskID != task || next.Generation != run.Generation || next.RunID == run.RunID {
				t.Fatal("not a same-generation continuation", run, next)
			}
			submitSharedReviewForTest(t, h, next, bound, "passed")
			op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"})
			if op.Status != protocol.OpSucceeded {
				t.Fatalf("actual completed same-generation report stranded: round_run=%s original_run=%s result_run=%s status=%s error=%v", bound.Rounds[0].RunID, run.RunID, next.RunID, op.Status, op.Error)
			}
			result := sharedReviewResult(t, op)
			if !result.Valid {
				t.Fatal("completed report not valid", result)
			}
		})
	}
}
