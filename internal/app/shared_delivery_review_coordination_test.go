package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestSharedDeliveryReviewRecordsOldScopeResultWithoutStrandingReviewer(t *testing.T) {
	for _, recovery := range []string{"assess", "recheck"} {
		t.Run(recovery, func(t *testing.T) {
			h, runtime, issue, source, _, _ := sharedReviewFixture(t)
			setPRPolicyForTest(t, h, issue.IssueID, "manual")
			sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			first := createSharedReviewerTaskForTest(t, h, issue.IssueID)
			r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: first}))
			originalRequirements := r.RequirementsDigest
			executeSharedReviewForTest(t, h, runtime, r, first, "passed")
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			issue.Plan.Scope = "A real requirement arrived after the review was frozen"
			canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
			r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if r.Status != "passed" || r.Valid || len(r.Rounds) != 1 || r.Rounds[0].Result.Verdict != "passed" || r.Rounds[0].Requirements != originalRequirements || r.Rounds[0].RunID == "" {
				t.Fatal("old-scope evidence was discarded, became current or stranded the reviewer", r)
			}
			if recovery == "assess" {
				r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "assess", DeliveryID: "pr", IssueControlPayload: protocol.IssueControlPayload{Reason: "The owner assessed the exact old candidate and confirms its actual review also covers the clarified requirement"}}))
				if !r.Valid || r.AssessedRequirementsDigest == originalRequirements || len(r.Rounds) != 1 {
					t.Fatal("explicit applicability assessment could not preserve the actual review", r)
				}
			} else {
				second := createSharedReviewerTaskForTest(t, h, issue.IssueID)
				r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: second}))
				executeSharedReviewForTest(t, h, runtime, r, second, "passed")
				r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
				if !r.Valid || len(r.Rounds) != 2 || r.Rounds[1].Reviewer != r.Rounds[0].Reviewer || r.Rounds[1].Requirements == originalRequirements || r.Rounds[0].Result.RequirementsDigest != originalRequirements {
					t.Fatal("the original reviewer could not use the remaining focused round", r)
				}
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var retained int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM shared_delivery_review_history WHERE review_id=? AND json_extract(body_json,'$.rounds[0].result.verdict')='passed' AND json_extract(body_json,'$.rounds[0].requirements_digest')=?`, r.ReviewID, originalRequirements).Scan(&retained); err != nil {
					return err
				}
				if retained == 0 {
					t.Fatal("original real result was not retained in immutable history")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSharedDeliveryReviewAssessmentDoesNotCrossCandidates(t *testing.T) {
	h, runtime, issue, source, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, source, acceptance)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "manual")
	issue.Plan.Scope = "Requirement B assessed for the original candidate only"
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	r := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "assess", DeliveryID: "pr"}))
	originalAssessment := r.AssessedRequirementsDigest
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Scope = "Requirement C reviewed against the new candidate"
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	candidate.HeadSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
	second := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: second}))
	if r.AssessedRequirementsDigest != "" || r.AssessmentReason != "" {
		t.Fatal("new candidate inherited an old applicability assessment", r)
	}
	executeSharedReviewForTest(t, h, runtime, r, second, "passed")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Scope = "Requirement B assessed for the original candidate only"
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return store.SharedDeliveryReviewCurrent(t.Context(), tx.Q(), &r)
	}); err != nil {
		t.Fatal(err)
	}
	if r.Valid || r.AssessedRequirementsDigest != "" {
		t.Fatal("old-head assessment made the new candidate applicable", r)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var retained int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM shared_delivery_review_history WHERE review_id=? AND json_extract(body_json,'$.assessed_requirements_digest')=?`, r.ReviewID, originalAssessment).Scan(&retained); err != nil {
			return err
		}
		if retained == 0 {
			t.Fatal("original assessment was erased from history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "assess", DeliveryID: "pr", IssueControlPayload: protocol.IssueControlPayload{Reason: "The owner now independently confirms applicability for this new exact head"}}))
	if !r.Valid || r.HeadSHA != candidate.HeadSHA || len(r.Rounds) != 2 {
		t.Fatal("new explicit candidate assessment could not recover applicability", r)
	}
}

func TestSharedDeliveryReviewNotifiesLinkedOwnersThroughExistingWindow(t *testing.T) {
	h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	other := createCanonicalOwnerTestIssue(t, h)
	other.Plan.Deliveries = append([]protocol.IssueDelivery{}, issue.Plan.Deliveries...)
	alias := issue.Plan.Deliveries[0]
	alias.DeliveryID = "pr-alias"
	other.Plan.Deliveries = append(other.Plan.Deliveries, alias)
	other = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, other, "update", protocol.UpdateIssuePayload{Plan: other.Plan, ExpectedAssignmentEpoch: other.AssignmentEpoch}))
	other = assignHostedManager(t, h, other)
	unrelated := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	observeDeliveryForTest(t, h, other.IssueID, candidate, "open", "")
	sharedReviewResult(t, sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr"}))
	aliasCandidate := candidate
	aliasCandidate.DeliveryID = alias.DeliveryID
	observeDeliveryForTest(t, h, other.IssueID, aliasCandidate, "open", "")
	sharedReviewResult(t, sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: alias.DeliveryID}))
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: source, AcceptanceID: acceptance}))
	conversation := "issue:" + issue.IssueID
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal("initial owner coordination was not available", ok, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-" + issue.IssueID, Thread: turn.ThreadID, ConversationID: conversation}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM issue_change_notifications WHERE issue_id IN (?,?,?)`, issue.IssueID, other.IssueID, unrelated.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	p := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Record the original whole-PR review for every linked responsibility"}, Action: "record", DeliveryID: "pr"}
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), p, ReviewIssueDelivery{})
	r := sharedReviewResult(t, op)
	if !r.Valid || r.Status != "passed" {
		t.Fatal(r)
	}
	for _, entry := range []struct {
		id      string
		events  int
		pending int
	}{{issue.IssueID, 1, 0}, {other.IssueID, 1, 1}, {unrelated.IssueID, 0, 0}} {
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var events, pending int
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE request_id=? AND object_id=? AND kind='issue.delivery_review_changed'`, op.RequestID, entry.id).Scan(&events); err != nil {
				return err
			}
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_change_notifications WHERE issue_id=? AND due_at>0`, entry.id).Scan(&pending); err != nil {
				return err
			}
			if events != entry.events || pending != entry.pending {
				t.Fatal("shared review lost, duplicated or misrouted owner input", entry.id, events, pending)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	p.Action = "assess"
	p.Reason = "The current owner explicitly confirms applicability for the same exact candidate"
	sharedReviewResult(t, hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), p, ReviewIssueDelivery{}))
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var first, last int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT first_event_seq,event_seq FROM issue_change_notifications WHERE issue_id=? AND due_at>0`, other.IssueID).Scan(&first, &last); err != nil {
			return err
		}
		if first >= last {
			t.Fatal("consecutive review results did not merge into one owner notification window", first, last)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if issueChangeSources(t, h, other.IssueID) != 1 || issueChangeSources(t, h, issue.IssueID) != 0 || issueChangeSources(t, h, unrelated.IssueID) != 0 {
		t.Fatal("shared review did not reuse the existing owner notification window")
	}
	history := h.count("shared_delivery_review_history")
	r = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: source}))
	if !r.Valid || h.count("shared_delivery_review_history") != history {
		t.Fatal("unchanged valid result created another review update", r)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.SharedDeliveryReviews(t.Context(), tx.Q(), other.IssueID, "pr", "", 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if issueChangeSources(t, h, other.IssueID) != 1 {
		t.Fatal("unchanged reread woke the linked owner again")
	}
	sharedReviewResult(t, sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "unlink", DeliveryID: alias.DeliveryID}))
	unlinked := sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "unlink", DeliveryID: "pr"})
	sharedReviewResult(t, unlinked)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var events int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE request_id=? AND object_id=? AND kind='issue.delivery_review_changed'`, unlinked.RequestID, other.IssueID).Scan(&events); err != nil {
			return err
		}
		if events != 1 {
			t.Fatal("removed responsibility lost its final change event", events)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
