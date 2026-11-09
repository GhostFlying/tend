package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func sharedProviderFactForTest(t *testing.T, h *harness, ref protocol.IssueDelivery, observation protocol.DeliveryObservation, nonce string, refresh, observed int64) {
	t.Helper()
	observation.Observer = "server"
	observation.AttemptedAt = h.clock.Now().UnixMilli()
	observation.CheckedAt = h.clock.Now().UnixMilli()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.PutDeliveryObservationFact(t.Context(), ref, observation); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE server_delivery_jobs SET nonce=?,refresh_revision=?,observed_refresh_revision=? WHERE provider=? AND repository=? AND number=?`, nonce, refresh, observed, ref.Provider, ref.Repository, ref.Number)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func sharedReviewViewForTest(t *testing.T, h *harness, ref protocol.IssueDelivery) protocol.SharedDeliveryReview {
	t.Helper()
	var review protocol.SharedDeliveryReview
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		review, err = store.SharedDeliveryReviewForTarget(t.Context(), tx.Q(), ref)
		if err != nil {
			return err
		}
		return store.SharedDeliveryReviewCurrent(t.Context(), tx.Q(), &review)
	}); err != nil {
		t.Fatal(err)
	}
	return review
}

func TestSharedReviewHistoricalIssueObservationDoesNotOverrideCurrentPR(t *testing.T) {
	h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, issue, source, acceptance)
	ref := issue.Plan.Deliveries[0]
	old := createCanonicalOwnerTestIssue(t, h)
	old.Plan.Deliveries = issue.Plan.Deliveries
	old = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, old, "update", protocol.UpdateIssuePayload{Plan: old.Plan, ExpectedAssignmentEpoch: old.AssignmentEpoch}))
	prior := candidate
	prior.HeadSHA, prior.BaseSHA = strings.Repeat("c", 40), strings.Repeat("d", 40)
	observeDeliveryForTest(t, h, old.IssueID, prior, "open", "")
	sharedReviewResult(t, sharedReviewAction(t, h, old.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: ref.DeliveryID}))
	old = canonicalTaskIssue(t, h, old.IssueID)
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, old, "close", canonicalLifecycleClosure(old, protocol.WorkItemClosureCompleted)))
	sharedProviderFactForTest(t, h, ref, protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{URL: candidate.URL, State: "open", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA, TargetSHA: candidate.TargetSHA, CI: &protocol.DeliveryCI{HeadSHA: candidate.HeadSHA, State: "success", Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}}}}}, "", 0, 0)
	assessed := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "assess", DeliveryID: ref.DeliveryID, IssueControlPayload: protocol.IssueControlPayload{Reason: "The passed whole-PR result covers the linked completed responsibility"}}))
	if !assessed.Valid || len(assessed.Rounds) != 1 || len(assessed.Links) != 2 {
		t.Fatal("a historical Issue copy overrode the actual shared provider fact", assessed)
	}
	next := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: ref.DeliveryID, ReviewerTaskID: source, AcceptanceID: acceptance}))
	if !next.Valid || next.Revision != assessed.Revision || len(next.Rounds) != 1 {
		t.Fatal("an unchanged shared candidate consumed another review", next)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetDeliveryObservation(t.Context(), tx.Q(), old.IssueID, ref.DeliveryID)
		if err != nil {
			return err
		}
		if current.Observation.HeadSHA != candidate.HeadSHA || current.Observation.BaseSHA != candidate.BaseSHA {
			t.Fatal("a closed responsibility did not read the PR's current fact", current)
		}
		var query string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT query_json FROM delivery_observations WHERE work_item_id=? AND delivery_id=?`, old.IssueID, ref.DeliveryID).Scan(&query); err != nil {
			return err
		}
		if strings.Contains(query, candidate.HeadSHA) || strings.Contains(query, prior.HeadSHA) || strings.Contains(query, "head_sha") {
			t.Fatal("an Issue query still stored a provider candidate", query)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	setPRPolicyForTest(t, h, issue.IssueID, "auto")
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	if duty.Status != "authorized" || duty.Authorization == nil || duty.Authorization.Mode != "auto" {
		t.Fatal("the actual passed review did not permit the original integration flow", duty)
	}
}

func TestSharedReviewActionsUseCurrentPRBeforeIssueFanout(t *testing.T) {
	h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
	ref := issue.Plan.Deliveries[0]
	prior := candidate
	prior.HeadSHA, prior.BaseSHA = strings.Repeat("c", 40), strings.Repeat("d", 40)
	observeDeliveryForTest(t, h, issue.IssueID, prior, "open", "")
	sharedProviderFactForTest(t, h, ref, protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA}}, "", 0, 0)
	review := importSharedReviewForTest(t, h, issue, source, acceptance)
	if !review.Valid || review.HeadSHA != candidate.HeadSHA || review.BaseSHA != candidate.BaseSHA {
		t.Fatal("review binding used a stale responsibility copy", review)
	}
	assessed := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "assess", DeliveryID: ref.DeliveryID}))
	if !assessed.Valid || len(assessed.Rounds) != 1 {
		t.Fatal("assessment used a stale responsibility copy", assessed)
	}
}

func TestSharedReviewCurrentProviderUncertaintyRetainsPassedRound(t *testing.T) {
	for _, state := range []string{"error", "reading", "refresh", "missing-head"} {
		t.Run(state, func(t *testing.T) {
			h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
			original := importSharedReviewForTest(t, h, issue, source, acceptance)
			ref := issue.Plan.Deliveries[0]
			fact := protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA}}
			nonce := ""
			switch state {
			case "error":
				fact.ErrorKind = "timeout"
			case "reading":
				nonce = "provider-read-in-progress"
			case "missing-head":
				fact.HeadSHA = ""
			}
			sharedProviderFactForTest(t, h, ref, fact, nonce, 0, 0)
			if state == "refresh" {
				current := canonicalTaskIssue(t, h, issue.IssueID)
				op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(issue.IssueID), protocol.RefreshDeliveryPayload{DeliveryID: ref.DeliveryID}, rev(current.Revision)), RefreshDelivery{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			current := sharedReviewViewForTest(t, h, ref)
			if current.Valid || current.Status != "passed" || !strings.Contains(current.InvalidReason, "unavailable") || len(current.Rounds) != 1 {
				t.Fatal("current uncertainty was hidden by an old successful Issue copy", current)
			}
			expectWorkCode(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: ref.DeliveryID, ReviewerTaskID: source, AcceptanceID: acceptance}), protocol.CodeStaleRevision)
			current = sharedReviewViewForTest(t, h, ref)
			if current.Revision != original.Revision || len(current.Rounds) != 1 {
				t.Fatal("uncertain provider read spent the original review budget", current)
			}
		})
	}
}

func TestSharedReviewActualProviderCandidateChangeInvalidatesOldResult(t *testing.T) {
	for _, field := range []string{"head", "base"} {
		t.Run(field, func(t *testing.T) {
			h, _, issue, source, candidate, acceptance := sharedReviewFixture(t)
			importSharedReviewForTest(t, h, issue, source, acceptance)
			fact := protocol.DeliveryObservation{DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA}}
			if field == "head" {
				fact.HeadSHA = strings.Repeat("f", 40)
			} else {
				fact.BaseSHA = strings.Repeat("f", 40)
			}
			sharedProviderFactForTest(t, h, issue.Plan.Deliveries[0], fact, "", 0, 0)
			current := sharedReviewViewForTest(t, h, issue.Plan.Deliveries[0])
			if current.Valid || current.InvalidReason != "provider candidate changed" || len(current.Rounds) != 1 {
				t.Fatal("a real provider change reused the old candidate's verdict", current)
			}
		})
	}
}
