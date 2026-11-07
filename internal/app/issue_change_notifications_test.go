package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func issueChangeSources(t *testing.T, h *harness, issue string) int {
	t.Helper()
	var count int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=? AND s.kind='issue.changed' AND s.cancelled_at=0`, issue).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestIssueChangesCoalesceAndPreserveExternalInputAcrossOwnerWrites(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	plan := issue.Plan
	plan.Title = "External definition clarification"
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}))
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	turn, principal := startHostedIssueReview(t, h, issue)
	var pendingEvent string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT event_id FROM issue_change_notifications WHERE issue_id=? AND due_at>0`, issue.IssueID).Scan(&pendingEvent)
	}); err != nil {
		t.Fatal(err)
	}
	progress := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Owner is implementing the existing agreement", NextStep: "Continue implementation"}
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), progress, UpdateIssueProgress{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var retained string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT event_id FROM issue_change_notifications WHERE issue_id=? AND due_at>0`, issue.IssueID).Scan(&retained); err != nil {
			return err
		}
		if pendingEvent != retained {
			t.Fatal("owner writer tail replaced its pending external input")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	sweepStatus(t, h)
	if got := issueChangeSources(t, h, issue.IssueID); got != 1 {
		t.Fatal("window did not produce one source", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var batches int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(DISTINCT s.batch_id) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=? AND s.kind IN ('issue.changed','issue.status_changed') AND s.cancelled_at=0`, issue.IssueID).Scan(&batches); err != nil {
			return err
		}
		if batches != 1 {
			t.Fatal("status and definition changes created separate owner turns", batches)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Claim freezes the original input. A later external change has a durable
	// successor and cannot mutate or disappear into the consumed batch.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE background_batches SET status='frozen' WHERE work_item_id=? AND status='pending'`, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		issue, err = store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan = issue.Plan
	plan.Title = "Another external clarification after claim"
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}))
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if got := issueChangeSources(t, h, issue.IssueID); got != 2 {
		t.Fatal("post-claim input lost", got)
	}
}

func TestSourceReviewLeavesNormalOwnerAssessmentNotification(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	task, _ := agreementFixture(t, h, issue)
	appendAgreementSource(t, h, issue.IssueID)
	turn, principal := startHostedIssueReview(t, h, issue)
	review := hostedReviewPayload(t, h, issue)
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, review, ReviewHostedIssueContext{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var kind string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT kind FROM issue_change_notifications WHERE issue_id=? AND due_at>0`, issue.IssueID).Scan(&kind); err != nil {
			return err
		}
		if kind != "issue.task_agreement_review_required" {
			t.Fatal("source review lost the remaining task judgement", kind)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertAgreementAdmission(t, h, issue.IssueID, task, true)
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if issueChangeSources(t, h, issue.IssueID) != 1 {
		t.Fatal("repair source work did not leave ordinary coordination input")
	}
}
