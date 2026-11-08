package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func createReviewCorrectionReportForTest(t *testing.T, h *harness, issueID string) string {
	t.Helper()
	issue := canonicalTaskIssue(t, h, issueID)
	spec := protocol.OwnerTaskSpec{Goal: "Correct the serialized identity in the original immutable review report using the Issue's saved materials", Scope: "Append format and evidence corrections only; preserve the original report, findings and formal review round", Acceptance: []string{"Submit the exact original candidate and requirements in report.shared_review; do not rerun the formal review"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "linux-1/beta"}}}
	payload := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Append the original reviewer's format correction", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "This ordinary report appends original-reviewer evidence, not a separate review-of-review"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	return created.TaskID
}

func TestSharedReviewOriginalReviewerCorrectsTerminalReportWithoutAnotherRound(t *testing.T) {
	h, runtime, issue, source, candidate, _ := sharedReviewFixture(t)
	setPRPolicyForTest(t, h, issue.IssueID, "manual")
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	original := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: original}))
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != original {
		t.Fatal(run)
	}
	bad := &protocol.SharedReviewResult{ReviewID: review.ReviewID, HeadSHA: review.HeadSHA, BaseSHA: review.BaseSHA, Verdict: "passed", RepositoryCheck: "Actual review completed, but the serialized requirement identity was omitted"}
	if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Original immutable formal report", SharedReview: bad}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	prior := h.task(original)
	if prior.Lifecycle != protocol.TaskCompleted {
		t.Fatal(prior)
	}
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}), protocol.CodeInvalidRequest)
	var oldBytes string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, err := store.GetArtifact(t.Context(), tx.Q(), prior.CurrentArtifactID)
		oldBytes = a.RefJSON
		return err
	}); err != nil {
		t.Fatal(err)
	}

	expectedBefore := review.Revision
	for _, test := range []struct {
		name     string
		revision *int64
		report   string
		code     protocol.Code
	}{
		{"missing review CAS", nil, original, protocol.CodeInvalidRequest},
		{"stale review CAS", rev(review.Revision - 1), original, protocol.CodeStaleRevision},
		{"uncorrected original bytes", &expectedBefore, original, protocol.CodeInvalidRequest},
		{"implementation is not a reviewer report", &expectedBefore, source, protocol.CodeInvalidRequest},
	} {
		beforeHistory := h.count("shared_delivery_review_history")
		expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "correct", DeliveryID: "pr", ReviewerTaskID: test.report, ExpectedReviewRevision: test.revision}), test.code)
		if h.count("shared_delivery_review_history") != beforeHistory {
			t.Fatal("rejected correction appended a fact", test.name)
		}
	}
	wrong := createReviewCorrectionReportForTest(t, h, issue.IssueID)
	wrongCandidate := review
	wrongCandidate.HeadSHA = "cccccccccccccccccccccccccccccccccccccccc"
	executeSharedReviewForTest(t, h, runtime, wrongCandidate, wrong, "passed")
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "correct", DeliveryID: "pr", ReviewerTaskID: wrong, ExpectedReviewRevision: &expectedBefore}), protocol.CodeInvalidRequest)
	// The responsible Agent chooses ordinary report work in a new actual session.
	// This is evidence correction, not a second formal-review round or old lease.
	changeSharedReviewerDefaults(t, h, runtime, protocol.ExecutionProfile{Model: "updated-reviewer-model", Effort: "high"})
	report := createReviewCorrectionReportForTest(t, h, issue.IssueID)
	executeSharedReviewForTest(t, h, runtime, review, report, "passed")
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	turn, principal := startHostedIssueReview(t, h, issue)
	expected := review.Revision
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	payload := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Relay the original reviewer's actual immutable format correction, retaining the original report"}, Action: "correct", DeliveryID: "pr", ReviewerTaskID: report, ExpectedReviewRevision: &expected}
	envelope := h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	command := HostedCoordinationCommand{Command: ReviewIssueDelivery{}, ExecutionID: turn.RequestID, EffectSeq: 1}
	op := h.exec(principal, envelope, command)
	review = sharedReviewResult(t, op)
	if !review.Valid || len(review.Rounds) != 1 || len(review.Rounds[0].Corrections) != 1 || review.Rounds[0].TaskID != original {
		t.Fatal("correction became another formal review round", review)
	}
	correction := review.Rounds[0].Corrections[0]
	if correction.TaskID != report || correction.RunID == run.RunID || correction.Result.RequirementsDigest != review.RequirementsDigest || correction.RequestID != envelope.RequestID || correction.Profile.Model != "updated-reviewer-model" {
		t.Fatal("correction lost its actual source", correction)
	}
	if current := h.task(original); current != prior {
		t.Fatal("correction resurrected or changed the original terminal Task", current, prior)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, err := store.GetArtifact(t.Context(), tx.Q(), prior.CurrentArtifactID)
		if a.RefJSON != oldBytes {
			t.Fatal("original report was overwritten")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	count := h.count("shared_delivery_review_history")
	if replay := h.exec(principal, envelope, command); string(replay.Result) != string(op.Result) || h.count("shared_delivery_review_history") != count {
		t.Fatal("unknown response retry appended twice", replay)
	}

	setPRPolicyForTest(t, h, issue.IssueID, "auto")
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	if created := createDeliveryExecutionForTest(t, h, issue.IssueID, duty); created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	duty = deliveryDutyForTest(t, h, issue.IssueID, source)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		authority, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), duty.ExecutionTaskID)
		if err != nil {
			return err
		}
		if authority == nil || authority.SharedReview == nil || authority.SharedReview.Round == nil || authority.SharedReview.Round.Correction == nil {
			t.Fatal("worker authority lost the corrected result's actual source", authority)
		}
		actual := authority.SharedReview.Round.Correction
		if actual.TaskID != report || actual.RunID != correction.RunID || actual.ArtifactID != correction.ArtifactID || actual.RequestID != envelope.RequestID || actual.Profile != correction.Profile || authority.SharedReview.Round.TaskID != original || authority.SharedReview.Round.RunID != run.RunID || authority.SharedReview.RoundsUsed != 1 || authority.SharedReview.RoundLimit != 0 || authority.SharedReview.SuggestedRounds != 2 {
			t.Fatal("projection attributed the correction to the old report or added a hard limit", authority.SharedReview)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expected = review.Revision
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "correct", DeliveryID: "pr", ReviewerTaskID: report, ExpectedReviewRevision: &expected}), protocol.CodeInvalidTransition)
}
