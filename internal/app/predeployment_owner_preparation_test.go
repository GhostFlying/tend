package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestPredeploymentOwnerProgressRetainsCandidatePreparationAPI(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "waived", true: "required"}[required], func(t *testing.T) {
			for _, ordinary := range []bool{false, true} {
				t.Run(map[bool]string{false: "direct", true: "after-progress"}[ordinary], func(t *testing.T) {

					h, i, as, old, progress := ownerBoundDeliveryFixture(t, true)
					if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); err != nil {
						t.Fatal(err)
					}
					if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					merged := old
					merged.TargetSHA = strings.Repeat("c", 40)
					observeDeliveryForTest(t, h, i.IssueID, merged, "merged", merged.TargetSHA)
					i = canonicalTaskIssue(t, h, i.IssueID)
					i.Plan.Deliveries = append(i.Plan.Deliveries, protocol.IssueDelivery{DeliveryID: "next", Provider: "github", Repository: "example/repo", Number: 18, URL: "https://example.invalid/repo/pull/18"})
					i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
					next := old
					next.DeliveryID = "next"
					next.URL = i.Plan.Deliveries[1].URL
					next.HeadSHA = strings.Repeat("d", 40)
					next.BaseSHA = merged.TargetSHA
					next.TargetSHA = merged.TargetSHA
					observeDeliveryForTest(t, h, i.IssueID, next, "open", "")
					stage := testArtifact(h.task(as.TaskID).WorkspaceName)
					stage.Mounts[0].CommitSHA = next.HeadSHA
					stage.Report = &protocol.ResultReport{Body: "Actual new candidate with frozen waiver", Predeployment: &protocol.PredeploymentResult{DeliveryID: next.DeliveryID, HeadSHA: next.HeadSHA, BaseSHA: next.BaseSHA}}
					if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: as.TaskID, RunID: as.RunID, Result: &stage, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: required, Reason: "Freeze this actual stage quality policy"}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Retain the checked replacement candidate"}}); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}

					var rt protocol.RunnerRuntime
					if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
						x, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
						rt = x.Runtime
						return err
					}); err != nil {
						t.Fatal(err)
					}
					reviewNextOwnerCandidateForTest(t, h, &rt, i, as.TaskID, next)
					originalStage := h.task(as.TaskID).CurrentArtifactID
					reviewerArtifact := ""
					if required {
						reviewerArtifact = reviewOwnerStageForTest(t, h, &rt, i, originalStage)
					}
					if ordinary {
						progress("Ordinary progress after completed stage and whole-PR review")
						progress("Another ordinary progress report retains that stage")
					}
					if ordinary && h.task(as.TaskID).CurrentArtifactID == originalStage {
						t.Fatal("fixture failed to replace pointer")
					}
					op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, ReviewerArtifactID: reviewerArtifact, Candidate: &next, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Prepare the same actually checked candidate under the original scope"}})
					if op.Status != protocol.OpSucceeded {
						t.Fatal("prepare rejected actual accepted stage after progress", op)
					}
					if h.count("issue_delivery_execution_history") != 1 {
						t.Fatal("old candidate history was not retained")
					}
					duty := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
					if duty.ArtifactID != originalStage || duty.AcceptanceID != "" {
						t.Fatal("new duty borrowed the ordinary progress report", duty, originalStage)
					}

				})
			}
		})
	}
}
func reviewNextOwnerCandidateForTest(t *testing.T, h *harness, r *protocol.RunnerRuntime, i protocol.Issue, task string, next protocol.IssueDeliveryCandidate) {
	t.Helper()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, next.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.CI = &protocol.DeliveryCI{HeadSHA: next.HeadSHA, State: "success", Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}}}
		return putDeliveryObservationForTest(tx, t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: next.DeliveryID, TaskID: task}))
	i = canonicalTaskIssue(t, h, i.IssueID)
	spec := testSpec
	spec.Delivery = ""
	spec.Deliverables = []string{domain.DeliveryReport}
	spec.Workspace = domain.Workspace{Mode: domain.WorkspaceTemporary}
	spec.ReviewDeliveryID = next.DeliveryID
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/beta"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Review the corrected candidate", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Independent reviewer reports once"}}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	review := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: next.DeliveryID, ReviewerTaskID: created.TaskID}))
	executeSharedReviewForTest(t, h, r, review, created.TaskID, "passed")
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: next.DeliveryID}))
}

func reviewOwnerStageForTest(t *testing.T, h *harness, r *protocol.RunnerRuntime, i protocol.Issue, artifact string) string {
	t.Helper()
	i = canonicalTaskIssue(t, h, i.IssueID)
	spec := testSpec
	spec.ReviewArtifactID = artifact
	spec.Delivery, spec.Deliverables, spec.Workspace = "", []string{domain.DeliveryReport}, domain.Workspace{Mode: domain.WorkspaceTemporary}
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/beta"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Independently review this immutable owner stage", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "This actual independent review reports once"}}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	reviewer, ok := h.claimInputRun(r)
	if !ok || reviewer.TaskID != task.TaskID {
		t.Fatal(reviewer, ok)
	}
	if op := h.runAs(reviewer, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(reviewer, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: reviewer.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(reviewer, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Independently checked this actual candidate stage and its evidence", ReviewedArtifactID: artifact, Verdict: protocol.VerdictPassed}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h.task(task.TaskID).CurrentArtifactID
}
