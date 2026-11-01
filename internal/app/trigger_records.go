package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func recordFollowupOccurrence(ctx context.Context, x *Exec, tx *store.Tx, r protocol.FollowupRule, o protocol.FollowupOccurrence, key string) error {
	if err := tx.InsertFollowupOccurrence(ctx, o, key); err != nil {
		return err
	}
	if r.WorkItemID == "" {
		return nil
	}
	return tx.InsertTrigger(ctx, protocol.TriggerEvent{ID: o.OccurrenceID, IssueID: r.WorkItemID, ScheduleID: r.RuleID, Title: r.Purpose, Source: "followup", Status: "pending", Reason: o.Reason, RequestID: x.Env.RequestID, MessageID: o.MessageID, PlannedAt: r.NextAt, CreatedAt: o.CreatedAt, UpdatedAt: o.CreatedAt, Steps: []protocol.TriggerStep{{Kind: "triggered", At: o.CreatedAt, Detail: o.Reason}}})
}

// Messages admitted while an Issue was owner-owned remain facts. Assignment
// makes them eligible for the same Issue queue; it never creates a new trigger.
func routePendingTriggers(ctx context.Context, x *Exec, tx *store.Tx) (int, error) {
	if !x.FollowupsEnabled {
		return 0, nil
	}
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT e.id FROM trigger_events e JOIN issue_controls c ON c.issue_id=e.issue_id JOIN followup_occurrences f ON f.message_id=e.message_id JOIN followup_rules r ON r.rule_id=f.rule_id WHERE r.status IN ('active','finished') AND c.assignment_state='active' AND e.source='followup' AND c.state='active' AND c.assignee_kind='agent' AND f.status='queued' AND NOT EXISTS(SELECT 1 FROM background_sources s WHERE s.message_id=e.message_id) ORDER BY e.id LIMIT 100`)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		e, err := store.GetTrigger(ctx, tx.Q(), id)
		if err != nil {
			return 0, err
		}
		var eventID string
		if err = tx.Q().QueryRowContext(ctx, `SELECT event_id FROM events WHERE object_type='message' AND object_id=? ORDER BY event_seq LIMIT 1`, e.MessageID).Scan(&eventID); err != nil {
			return 0, err
		}
		var occurrence protocol.FollowupOccurrence
		if err = tx.Q().QueryRowContext(ctx, `SELECT occurrence_id,rule_id,revision,work_item_id,message_id,reason,status,evidence,created_at FROM followup_occurrences WHERE message_id=?`, e.MessageID).Scan(&occurrence.OccurrenceID, &occurrence.RuleID, &occurrence.Revision, &occurrence.WorkItemID, &occurrence.MessageID, &occurrence.Reason, &occurrence.Status, &occurrence.Evidence, &occurrence.CreatedAt); err != nil {
			return 0, err
		}
		raw, _ := json.Marshal(occurrence)
		if err = tx.TriggerStep(ctx, id, protocol.TriggerStep{Kind: "routed", At: x.Now.UnixMilli(), Detail: e.IssueID}); err != nil {
			return 0, err
		}
		if err = appendBackgroundSource(ctx, x, tx, "work_item:"+e.IssueID, e.IssueID, e.MessageID, eventID, "followup.triggered", raw); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func settleObservationJobs(ctx context.Context, tx *store.Tx, item protocol.WorkItem, now int64, allowRefresh bool) (int, error) {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT delivery_id FROM observation_jobs WHERE issue_id=? AND status IN ('pending','claimed')`, item.WorkItemID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		exists := false
		for _, d := range item.Deliveries {
			exists = exists || d.DeliveryID == id
		}
		if exists && item.Status != "closed" {
			continue
		}
		if exists && allowRefresh {
			var refresh bool
			if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_observations WHERE work_item_id=? AND delivery_id=? AND refresh_requested=1)`, item.WorkItemID, id).Scan(&refresh); err != nil {
				return n, err
			}
			if refresh {
				continue
			}
		}
		var event string
		if err = tx.Q().QueryRowContext(ctx, `SELECT event_id FROM observation_jobs WHERE issue_id=? AND delivery_id=?`, item.WorkItemID, id).Scan(&event); err != nil {
			return n, err
		}
		if err = tx.FinishTrigger(ctx, event, "cancelled", "Issue closed or delivery reference removed", now); err != nil {
			return n, err
		}
		if _, err = tx.Exec(ctx, `UPDATE observation_jobs SET status='cancelled',updated_at=? WHERE issue_id=? AND delivery_id=?`, now, item.WorkItemID, id); err != nil {
			return n, err
		}
		if _, err = tx.Exec(ctx, `UPDATE delivery_observations SET generation=generation+1,nonce='',host_id='',instance_id='',query_until=0,refresh_requested=0 WHERE work_item_id=? AND delivery_id=? AND (nonce<>'' OR refresh_requested<>0)`, item.WorkItemID, id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
func observationIssueEvents(ctx context.Context, x *Exec, tx *store.Tx, events []Event) error {
	seen := map[string]bool{}
	for _, e := range events {
		if (e.ObjectType != "issue" && e.ObjectType != "work_item") || seen[e.ObjectID] {
			continue
		}
		seen[e.ObjectID] = true
		if !oneOf(e.Kind, "issue.closed", "issue.cancelled", "issue.updated", "work_item.closed", "work_item.updated") {
			continue
		}
		var exists bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM observation_jobs WHERE issue_id=? AND status IN ('pending','claimed'))`, e.ObjectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		item, err := store.GetIssue(ctx, tx.Q(), e.ObjectID)
		if err != nil {
			return err
		}
		if _, err = settleObservationJobs(ctx, tx, item, x.Now.UnixMilli(), false); err != nil {
			return err
		}
	}
	return nil
}
