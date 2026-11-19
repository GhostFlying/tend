package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestSharedReviewCandidateAssessmentContinuesDelivery(t *testing.T) {
	h, runtime, issue, source, candidate, acceptance := sharedReviewFixture(t)
	original := importSharedReviewForTest(t, h, issue, source, acceptance)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue = assignHostedManager(t, h, issue)
	conversation := "issue:" + issue.IssueID
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal("no named Issue owner execution", ok, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + issue.IssueID, Thread: turn.ThreadID, ConversationID: conversation}
	ref := issue.Plan.Deliveries[0]
	candidate.HeadSHA, candidate.BaseSHA, candidate.TargetSHA = strings.Repeat("c", 40), strings.Repeat("d", 40), strings.Repeat("d", 40)
	snapshot := protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{
		URL: candidate.URL, State: "open", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA, TargetSHA: candidate.TargetSHA,
		CI: &protocol.DeliveryCI{HeadSHA: original.HeadSHA, State: "success"},
	}}
	observe := func(o protocol.DeliveryObservation) {
		t.Helper()
		sharedProviderFactForTest(t, h, ref, o, "", 0, 0)
	}
	prepare := func() protocol.Operation {
		t.Helper()
		return updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{
			TaskID: source, Action: "prepare", Candidate: &candidate,
			IssueControlPayload: protocol.IssueControlPayload{Reason: "Prepare this exact rebased candidate under the existing auto scope"},
		})
	}

	observe(snapshot)
	expectWorkCode(t, prepare(), protocol.CodeInvalidTransition)
	r := sharedReviewViewForTest(t, h, ref)
	if r.Valid || r.CurrentCandidate == nil || r.CurrentCandidate.HeadSHA != candidate.HeadSHA || r.CurrentCandidate.BaseSHA != candidate.BaseSHA {
		t.Fatal("the shared review did not expose the new unassessed candidate", r)
	}
	input := r.CurrentCandidate
	payload := protocol.UpdateSharedDeliveryReviewPayload{
		Action: "assess", DeliveryID: ref.DeliveryID,
		IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "The patch is equivalent and the upstream change has no effect on the reviewed behavior"},
		ExpectedHeadSHA:     input.HeadSHA, ExpectedBaseSHA: input.BaseSHA, ExpectedRequirementsDigest: input.RequirementsDigest,
		ExpectedReviewRevision: rev(r.Revision), ExpectedCandidateRevision: rev(input.Revision),
	}
	env := h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	if _, err := h.app.Execute(t.Context(), owner, env, ReviewIssueDelivery{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("a caller outside the named responsibility could assess the new candidate", err)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	assess := func(seq int64, p protocol.UpdateSharedDeliveryReviewPayload) protocol.Operation {
		t.Helper()
		return hostedEffect(t, h, principal, turn, seq, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), p, ReviewIssueDelivery{})
	}
	invalid := payload
	invalid.ExpectedBaseSHA = original.BaseSHA
	expectWorkCode(t, assess(1, invalid), protocol.CodeStaleRevision)
	invalid = payload
	invalid.ExpectedReviewRevision = rev(r.Revision - 1)
	expectWorkCode(t, assess(2, invalid), protocol.CodeStaleRevision)
	invalid = payload
	invalid.ExpectedRequirementsDigest = "stale requirements digest"
	expectWorkCode(t, assess(3, invalid), protocol.CodeStaleRevision)
	r = sharedReviewResult(t, assess(4, payload))
	if !r.Valid || r.Assessment == nil || r.Assessment.Assessor != issue.Assignee || r.Assessment.IssueID != issue.IssueID ||
		r.Assessment.AssignmentEpoch != issue.AssignmentEpoch || r.Assessment.Candidate != *input ||
		r.Assessment.Round != original.Rounds[0].Number || r.Assessment.Reason != payload.Reason ||
		!reflect.DeepEqual(r.Rounds, original.Rounds) || r.HeadSHA != original.HeadSHA || r.BaseSHA != original.BaseSHA {
		t.Fatal("assessment replaced the actual review or lost its own provenance", r)
	}
	expectWorkCode(t, prepare(), protocol.CodeInvalidTransition)
	snapshot.CI = &protocol.DeliveryCI{HeadSHA: candidate.HeadSHA, State: "success", Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}}}
	observe(snapshot)
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	if duty.Status != "authorized" || duty.AttentionID != "" || duty.Authorization.Mode != "auto" {
		t.Fatal("the original integration authority did not reach the assessed candidate", duty)
	}
	if op := createDeliveryExecutionForTest(t, h, issue.IssueID, duty); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	duty = deliveryDutyForTest(t, h, issue.IssueID, source)
	readAuthority := func() *protocol.DeliveryExecutionAuthority {
		t.Helper()
		var authority *protocol.DeliveryExecutionAuthority
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			authority, err = views.DeliveryExecutionAuthority(t.Context(), tx.Q(), duty.ExecutionTaskID)
			return err
		}); err != nil || authority == nil || authority.SharedReview == nil {
			t.Fatal("read actual execution authority", authority, err)
		}
		return authority
	}
	if authority := readAuthority(); !authority.Valid || !authority.SharedReview.Valid || authority.SharedReview.Round.HeadSHA != original.HeadSHA || authority.SharedReview.Assessment == nil {
		t.Fatal("execution authority did not distinguish original review evidence from applicability", authority)
	}

	observe(protocol.DeliveryObservation{ErrorKind: "timeout"})
	if current := sharedReviewViewForTest(t, h, ref); current.Valid || current.CurrentCandidate != nil {
		t.Fatal("unknown observation accepted the assessment", current)
	}
	observe(snapshot)
	if authority := readAuthority(); !authority.Valid {
		t.Fatal("the same provider candidate did not recover after a transient error", authority)
	}

	next := snapshot
	next.BaseSHA = strings.Repeat("e", 40)
	observe(next)
	if authority := readAuthority(); authority.Valid || authority.SharedReview.Valid {
		t.Fatal("the original assessment covered an unrelated base", authority)
	}
	observe(snapshot)
	r = sharedReviewViewForTest(t, h, ref)
	if r.Valid || r.CurrentCandidate == nil || r.CurrentCandidate.Revision == input.Revision || readAuthority().Valid {
		t.Fatal("an old assessment revived after the provider returned to its previous SHA pair", r)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	payload.ExpectedReviewRevision = rev(r.Revision)
	expectWorkCode(t, assess(5, payload), protocol.CodeStaleRevision)

	payload.ExpectedCandidateRevision = rev(r.CurrentCandidate.Revision)
	r = sharedReviewResult(t, assess(6, payload))
	if !r.Valid || r.Assessment == nil || !readAuthority().Valid {
		t.Fatal("a fresh judgment could not cover the returned candidate", r)
	}

	reviewer := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	recheck := protocol.UpdateSharedDeliveryReviewPayload{
		Action: "recheck", DeliveryID: ref.DeliveryID, ReviewerTaskID: reviewer,
		ExpectedHeadSHA: r.CurrentCandidate.HeadSHA, ExpectedBaseSHA: r.CurrentCandidate.BaseSHA, ExpectedRequirementsDigest: r.CurrentCandidate.RequirementsDigest,
		IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Ask the original reviewer to inspect the relevant upstream change"},
	}
	r = sharedReviewResult(t, assess(7, recheck))
	if r.Status != "reviewing" || r.Assessment != nil || len(r.Rounds) != 2 || readAuthority().Valid {
		t.Fatal("an applicable judgment blocked the original reviewer's explicit recheck", r)
	}
	executeSharedReviewForTest(t, h, runtime, r, reviewer, "passed")
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	r = sharedReviewResult(t, assess(8, protocol.UpdateSharedDeliveryReviewPayload{
		Action: "record", DeliveryID: ref.DeliveryID,
		IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Record the original reviewer's focused result"},
	}))
	if !r.Valid || r.Assessment != nil || len(r.Rounds) != 2 || r.Rounds[1].Reviewer != original.Reviewer ||
		!reflect.DeepEqual(r.Rounds[0], original.Rounds[0]) || r.Rounds[1].HeadSHA != candidate.HeadSHA || !readAuthority().Valid {
		t.Fatal("a focused result did not continue the existing delivery with preserved evidence", r)
	}
}

func TestSharedReviewCandidateAssessmentRequiresPassedResult(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	ref := issue.Plan.Deliveries[0]
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{
		Action: "link", DeliveryID: ref.DeliveryID, TaskID: source,
	}))
	assess := func(r protocol.SharedDeliveryReview) {
		t.Helper()
		candidate := r.CurrentCandidate
		expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{
			Action: "assess", DeliveryID: ref.DeliveryID, ExpectedReviewRevision: rev(r.Revision),
			ExpectedHeadSHA: candidate.HeadSHA, ExpectedBaseSHA: candidate.BaseSHA,
			ExpectedRequirementsDigest: candidate.RequirementsDigest, ExpectedCandidateRevision: rev(candidate.Revision),
		}), protocol.CodeInvalidTransition)
	}
	assess(r)
	reviewer := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{
		Action: "start", DeliveryID: ref.DeliveryID, ReviewerTaskID: reviewer,
	}))
	assess(r)
	executeSharedReviewForTest(t, h, runtime, r, reviewer, "changes_requested")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{
		Action: "record", DeliveryID: ref.DeliveryID,
	}))
	assess(r)
}
