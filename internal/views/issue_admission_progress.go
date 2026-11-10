package views

import (
	"context"
	"database/sql"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (f executionFacts) issueAdmissionProgress(ctx context.Context, q store.Querier, i protocol.Issue) (*protocol.QueueWaitReason, *protocol.IssuePauseProgress, error) {
	var reason *protocol.QueueWaitReason
	if c, ok := f.issues[i.IssueID]; ok {
		reason = f.waitReason(c)
	}
	if reason == nil && i.State == protocol.IssueActive && i.BoardStatus() != protocol.IssueBacklog {
		var stopPending, receiptPending bool
		err := q.QueryRowContext(ctx, `WITH RECURSIVE scope(id) AS (SELECT ? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope s ON c.parent_issue_id=s.id) SELECT EXISTS(SELECT 1 FROM run_pause_handoffs h JOIN runs r USING(run_id) JOIN task_work_item_owners o USING(task_id) JOIN scope s ON s.id=o.work_item_id WHERE h.stopped=0 AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')),EXISTS(SELECT 1 FROM run_pause_handoffs h JOIN runs r USING(run_id) JOIN task_work_item_owners o USING(task_id) JOIN scope s ON s.id=o.work_item_id WHERE h.stopped=1 AND h.continuation_action_id IS NULL AND h.successor_run_id IS NULL AND EXISTS(SELECT 1 FROM manager_native_requests n WHERE n.run_id=h.run_id AND n.status IN ('started','unknown') AND n.method NOT IN ('models','find','usage')))`, i.IssueID).Scan(&stopPending, &receiptPending)
		if err != nil {
			return nil, nil, err
		}
		if stopPending {
			reason = &protocol.QueueWaitReason{Code: protocol.QueueTaskBlocked, Message: "Previous Issue execution awaits physical stop confirmation"}
		}
		if receiptPending {
			reason = &protocol.QueueWaitReason{Code: protocol.QueueTaskBlocked, Message: "Original native operation outcome requires reconciliation before continuation"}
		}
	}
	var p protocol.IssuePauseProgress
	err := q.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (SELECT ? UNION ALL SELECT i.parent_issue_id FROM issue_controls i JOIN ancestors a ON i.issue_id=a.id WHERE i.parent_issue_id<>'') SELECT o.issue_id,o.issue_revision FROM issue_pause_operations o JOIN ancestors a ON a.id=o.issue_id JOIN issue_controls i ON i.issue_id=o.issue_id WHERE i.work_status='backlog' OR i.state='paused' ORDER BY o.created_at DESC,o.issue_revision DESC LIMIT 1`, i.IssueID).Scan(&p.IssueID, &p.IssueRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return reason, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN h.stopped=0 AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN h.stopped=1 OR r.observed_state IN ('finished','failed') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN h.stopped=0 AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND NOT EXISTS(SELECT 1 FROM run_pause_contracts c WHERE c.run_id=h.run_id AND c.handoff_allowed=1) THEN 1 ELSE 0 END),0) FROM run_pause_handoffs h JOIN runs r USING(run_id) JOIN issue_pause_tasks t ON t.task_id=r.task_id AND t.issue_id=? AND t.issue_revision=?`, p.IssueID, p.IssueRevision).Scan(&p.Pending, &p.Stopped, &p.Unsupported)
	return reason, &p, err
}
