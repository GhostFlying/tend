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
		var eventID, batch, state, artifactID string
		var position int64
		err = tx.Q().QueryRowContext(ctx, `SELECT e.event_id,e.event_seq,b.batch_id,b.status,json_extract(e.body_json,'$.artifact_id') FROM events e
JOIN background_sources s ON s.event_id=e.event_id
JOIN background_batches b USING (batch_id)
WHERE e.object_type='task' AND e.object_id=? AND e.kind='task.completed' AND b.work_item_id=?`, task, issue.IssueID).Scan(&eventID, &position, &batch, &state, &artifactID)
		if err != nil {
			return err
		}
		if artifactID == "" {
			t.Fatal("expected the completion event to reference its result")
		}
		for _, mode := range []struct {
			name  string
			query views.IssueActivityQuery
		}{
			{name: "full"},
			{name: "readable", query: views.IssueActivityQuery{Readable: true}},
			{name: "incremental", query: views.IssueActivityQuery{Readable: true, Since: position - 1}},
		} {
			t.Run(mode.name, func(t *testing.T) {
				activity, err := views.ReadIssueActivity(ctx, tx.Q(), issue.IssueID, mode.query)
				if err != nil {
					t.Fatal(err)
				}
				var hasCompletion, hasResult bool
				for _, entry := range activity.Items {
					if entry.Position == position {
						hasCompletion = true
						if entry.Kind != "task.completed" || entry.IssueID != issue.IssueID || entry.TaskID != task ||
							entry.Title != "任务结果已返回" || entry.Author == "" || entry.ArtifactID != "" {
							t.Errorf("expected a task completion milestone with its own diagnostic: %+v", entry)
						}
					}
					if entry.Kind == "artifact.submitted" && entry.ArtifactID == artifactID {
						hasResult = true
					}
				}
				if !hasCompletion || (mode.query.Since == 0 && !hasResult) {
					t.Errorf("expected completion at %d and result %q on initial pages, got completion=%v result=%v", position, artifactID, hasCompletion, hasResult)
				}
			})
		}
		diagnosis, err := views.IssueEventNotification(ctx, tx.Q(), issue.IssueID, position, 0)
		t.Logf("review returned=%s, recorded=%v, completion event=%s/%d, exact batch=%s (%s)", progress.Verdict, progress.Recorded, eventID, position, batch, state)
		if err != nil || diagnosis.EventID != eventID || len(diagnosis.Notifications) != 1 || diagnosis.Notifications[0].BatchID != batch {
			t.Fatalf("the actual review completion notice cannot be diagnosed: %+v, %v", diagnosis, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
