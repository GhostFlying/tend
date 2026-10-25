package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ListPendingCompletionReviews survives notification delivery and failed plan writes.
func ListPendingCompletionReviews(ctx context.Context, q Querier, itemID string) ([]protocol.WorkItemCompletionReview, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_id,task_id,acceptance_id,created_at FROM work_item_completion_reviews WHERE work_item_id=? AND reviewed_revision IS NULL ORDER BY created_at,event_id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.WorkItemCompletionReview{}
	for rows.Next() {
		var r protocol.WorkItemCompletionReview
		if err := rows.Scan(&r.EventID, &r.TaskID, &r.AcceptanceID, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutCompletionReview deduplicates by the immutable completion event and item.
func (t *Tx) PutCompletionReview(ctx context.Context, itemID string, r protocol.WorkItemCompletionReview) error {
	_, err := t.Exec(ctx, `INSERT INTO work_item_completion_reviews(work_item_id,event_id,task_id,acceptance_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, itemID, r.EventID, r.TaskID, r.AcceptanceID, r.CreatedAt)
	return err
}

// AcknowledgeCompletionReview retains the assessment and revision for audit.
func (t *Tx) AcknowledgeCompletionReview(ctx context.Context, itemID, eventID string, revision int64, reason string) error {
	result, err := t.Exec(ctx, `UPDATE work_item_completion_reviews SET reviewed_revision=?,review_reason=? WHERE work_item_id=? AND event_id=? AND reviewed_revision IS NULL`, revision, reason, itemID, eventID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "completion_reviews.event_id must name a pending review on this work item; read work_items_read.pending_reviews")
	}
	return nil
}

// EnsureCompletionReviews also covers completed tasks linked after their notice.
func (t *Tx) EnsureCompletionReviews(ctx context.Context, itemID string) error {
	_, err := t.Exec(ctx, `INSERT INTO work_item_completion_reviews(work_item_id,event_id,task_id,acceptance_id,created_at)
 SELECT DISTINCT w.work_item_id,e.event_id,t.task_id,
 (SELECT a.acceptance_id FROM acceptances a WHERE a.task_id=t.task_id AND a.artifact_id=t.current_artifact_id AND a.spec_version=t.spec_version AND a.verdict='passed' ORDER BY a.created_at DESC,a.acceptance_id DESC LIMIT 1),e.created_at
 FROM work_items w JOIN json_each(w.body_json,'$.stages') s
 JOIN json_each(s.value,'$.task_ids') link
 JOIN tasks t ON t.task_id=link.value
 JOIN events e ON e.object_id=t.task_id AND e.kind='task.completed'
 WHERE w.work_item_id=? AND w.status!='closed' AND json_extract(s.value,'$.status') IN ('active','planned')
 AND t.lifecycle='completed'
 AND EXISTS(SELECT 1 FROM acceptances a WHERE a.task_id=t.task_id AND a.artifact_id=t.current_artifact_id AND a.spec_version=t.spec_version AND a.verdict='passed')
 ON CONFLICT DO NOTHING`, itemID)
	return err
}
