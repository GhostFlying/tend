package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestProviderMergeWakesOnlyOriginalAuthorizedSettlement(t *testing.T) {
	h, issue, original, candidate, register := ownerBoundDeliveryFixture(t, false)
	register("Original authorized merge-only execution returned its result")
	h.app.serverDeliveryObservations = true
	merge := strings.Repeat("c", 40)
	o := &protocol.DeliverySnapshot{State: "merged", HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA,
		MergeSHA: merge, TargetSHA: merge, TargetRef: "integration", URL: candidate.URL,
		CI: &protocol.DeliveryCI{HeadSHA: candidate.HeadSHA, State: "success",
			Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}}}}
	report := func() {
		t.Helper()
		current := canonicalTaskIssue(t, h, issue.IssueID)
		p := protocol.RefreshDeliveryPayload{DeliveryID: candidate.DeliveryID}
		if op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(issue.IssueID), p, rev(current.Revision)), RefreshDelivery{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		q, err := h.app.ClaimServerDelivery(t.Context())
		if err != nil || q == nil {
			t.Fatal(q, err)
		}
		if err := h.app.RecordServerDelivery(t.Context(), q.ID, o, ""); err != nil {
			t.Fatal(err)
		}
	}
	report()
	if n := h.count("events WHERE kind='issue.delivery_merge_verified'"); n != 0 {
		t.Fatal("ordinary merge without lineage woke an authorized duty", n)
	}
	o.TargetContainsMerge = true
	report()
	report()
	if n := h.count("events WHERE kind='issue.delivery_merge_verified'"); n != 1 {
		t.Fatal("verified original delivery should emit exactly one business input", n)
	}
	before := deliveryDutyForTest(t, h, issue.IssueID, original.TaskID)
	if before.Status != "executing" || before.ExecutionTaskID != original.TaskID {
		t.Fatal("provider mutated or reassigned the outstanding delivery", before)
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: original.TaskID,
		Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the original verified merge under the recorded grant"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if after := deliveryDutyForTest(t, h, issue.IssueID, original.TaskID); after.Status != "complete" || len(after.Evidence) != 2 {
		t.Fatal("original authorization and actual provider merge did not settle", after)
	}
}
