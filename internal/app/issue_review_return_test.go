package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueDiagnosticForActualReviewReturn(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, hosted := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(hosted), WithMainOwner(owner.Subject))
	issue = assignHostedManager(t, h, issue)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	executeSharedReviewForTest(t, h, runtime, review, task, "changes_requested")

	ctx := t.Context()
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		deliveries, err := views.CanonicalIssueDeliveries(ctx, tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		progress := deliveries.Items[0].Review
		if progress == nil || progress.Verdict != "changes_requested" || progress.Recorded {
			t.Fatalf("expected an actual returned review before record: %+v", progress)
		}
		var eventID, batch, state string
		var position int64
		err = tx.Q().QueryRowContext(ctx, `SELECT e.event_id,e.event_seq,b.batch_id,b.status FROM events e
JOIN background_sources s ON s.event_id=e.event_id
JOIN background_batches b USING (batch_id)
WHERE e.object_type='task' AND e.object_id=? AND e.kind='task.completed' AND b.work_item_id=?`, task, issue.IssueID).Scan(&eventID, &position, &batch, &state)
		if err != nil {
			return err
		}
		activity, err := views.IssueActivity(ctx, tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		hasCompletion := false
		for _, entry := range activity.Items {
			if entry.Position == position && entry.Kind == "task.completed" && entry.IssueID == issue.IssueID && entry.TaskID == task {
				hasCompletion = true
			}
		}
		diagnosis, err := views.IssueEventNotification(ctx, tx.Q(), issue.IssueID, position, 0)
		t.Logf("review returned=%s, recorded=%v, completion event=%s/%d, exact batch=%s (%s), completion activity=%v, diagnostic error=%v", progress.Verdict, progress.Recorded, eventID, position, batch, state, hasCompletion, err)
		if err != nil || !hasCompletion || diagnosis.EventID != eventID || len(diagnosis.Notifications) != 1 || diagnosis.Notifications[0].BatchID != batch {
			t.Fatalf("the actual review completion notice cannot be diagnosed: %+v, %v", diagnosis, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
