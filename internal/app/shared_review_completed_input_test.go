package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSharedReviewCompletedInputPreflightRetainsActualReportChecks(t *testing.T) {
	for _, input := range []string{"current", "changed_candidate", "wrong_requirements", "coordination_only"} {
		t.Run(input, func(t *testing.T) {
			h, runtime, issue, source, candidate, _ := sharedReviewFixture(t)
			linked := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
			if input == "coordination_only" {
				run, ok := h.claimInputRun(runtime)
				if !ok || run.TaskID != task {
					t.Fatal(run)
				}
				if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Coordination ended without a complete review result"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				report := linked
				if input == "wrong_requirements" {
					report.RequirementsDigest = strings.Repeat("e", 64)
				}
				executeSharedReviewForTest(t, h, runtime, report, task, "passed")
			}
			if input == "changed_candidate" {
				candidate.HeadSHA = strings.Repeat("e", 40)
				observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
			}
			before := h.count("shared_delivery_review_history")
			op := sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task})
			if input != "current" {
				expectWorkCode(t, op, protocol.CodeInvalidRequest)
				if h.count("shared_delivery_review_history") != before {
					t.Fatal("invalid completed report spent a round or rebound its candidate", input, op)
				}
				return
			}
			r := sharedReviewResult(t, op)
			if r.Rounds[0].AcceptanceID != "" || r.Rounds[0].ReviewerRole != "" || r.Rounds[0].ArtifactID == "" {
				t.Fatal("completed report was mistaken for an acceptance import", r)
			}
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid || r.Status != "passed" {
				t.Fatal("actual current complete report could not bind and record", r)
			}
		})
	}
}

func TestLegacySharedAcceptanceImportCannotApproveChangedCandidate(t *testing.T) {
	h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	candidate.HeadSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
	// The legacy request omits reviewer_role. Binding the actual acceptance
	// preserves its evidence; it does not establish quality for a new head.
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: source, AcceptanceID: acceptance}))
	if r.Valid || r.Rounds[0].AcceptanceID != acceptance || r.Rounds[0].ReviewerRole != "" || r.Rounds[0].Result != nil {
		t.Fatal("legacy import fabricated current-candidate review quality", r)
	}
	before := h.count("shared_delivery_review_history")
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}), protocol.CodeInvalidRequest)
	if h.count("shared_delivery_review_history") != before || h.count("issue_delivery_executions") != 0 {
		t.Fatal("wrong candidate became approval or execution")
	}
}

func TestLegacySharedAcceptanceImportRejectsUnknownVerdictIdentity(t *testing.T) {
	h, _, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	before := h.count("shared_delivery_review_history")
	expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: source, AcceptanceID: "missing-actual-acceptance"}), protocol.CodeInvalidRequest)
	if h.count("shared_delivery_review_history") != before {
		t.Fatal("invented verdict identity spent a review round")
	}
}
