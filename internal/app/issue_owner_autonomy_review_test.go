package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSharedReviewResponsibleAgentContinuesBeyondDefaultTwoRounds(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	setPRPolicyForTest(t, h, issue.IssueID, "manual")
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	var review protocol.SharedDeliveryReview
	for number := 0; number < 2; number++ {
		if number > 0 {
			sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "Fix and verify the actual finding"}}}))
		}
		task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
		action := "start"
		if number > 0 {
			action = "recheck"
		}
		review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: action, DeliveryID: "pr", ReviewerTaskID: task}))
		executeSharedReviewForTest(t, h, runtime, review, task, "changes_requested")
		review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	}
	if review.Status != "changes_requested" || len(review.Rounds) != 2 || len(review.BudgetAuthorizations) != 0 {
		t.Fatal("default convergence advice became a human authorization gate", review)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "Resolve the second actual finding without resetting prior cost"}}}))
	third := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	turn, principal := startHostedIssueReview(t, h, issue)
	payload := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Continue the focused review within the existing Issue authorization and actual task budget"}, Action: "recheck", DeliveryID: "pr", ReviewerTaskID: third}
	review = sharedReviewResult(t, hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), payload, ReviewIssueDelivery{}))
	if len(review.Rounds) != 3 || len(review.BudgetAuthorizations) != 0 || review.Rounds[0].Result == nil || review.Rounds[1].Result == nil {
		t.Fatal("responsible agent lost the actual rounds or needed a new human grant", review)
	}
	executeSharedReviewForTest(t, h, runtime, review, third, "passed")
	review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !review.Valid || review.Status != "passed" {
		t.Fatal(review)
	}
}

func TestSharedReviewStructuredReportRetainsWaitingEvidence(t *testing.T) {
	for _, verdict := range []string{"blocked", "inconclusive"} {
		t.Run(verdict, func(t *testing.T) {
			h, runtime, issue, source, _, _ := sharedReviewFixture(t)
			setPRPolicyForTest(t, h, issue.IssueID, "manual")
			sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
			review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
			executeSharedReviewForTest(t, h, runtime, review, task, verdict)
			review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if review.Status != "waiting" || review.Valid || review.Rounds[0].Result.Verdict != verdict || len(review.BudgetAuthorizations) != 0 {
				t.Fatal("actual waiting verdict became approval or a human-only authorization gate", review)
			}
		})
	}
}
