package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestRealIssueNotificationsRetainSourceLinks(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	plan := issue.Plan
	plan.Title = "External definition clarification"
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}))
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var conversation, message string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT m.conversation_id,b.message_id FROM background_sources s JOIN background_batches b USING(batch_id) JOIN conversation_messages m ON m.message_id=b.message_id WHERE s.kind='issue.status_changed' AND b.work_item_id=? LIMIT 1`, issue.IssueID).Scan(&conversation, &message); err != nil {
			return err
		}
		page, err := views.Conversation(t.Context(), tx.Q(), 0, 0, 50, conversation)
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			if item.MessageID == message {
				found := map[string]bool{}
				for _, s := range item.Background.Presentation.Sources {
					if s.Kind == "issue.status_changed" || s.Kind == "issue.changed" {
						found[s.Kind] = true
						if s.ObjectType != "issue" || s.ObjectID != issue.IssueID {
							t.Errorf("real %s source omitted known Issue link: %+v", s.Kind, s)
						}
					}
				}
				if !found["issue.status_changed"] || !found["issue.changed"] {
					t.Fatalf("missing real producer sources: %+v", found)
				}
				return nil
			}
		}
		t.Fatal("no real source preview")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
