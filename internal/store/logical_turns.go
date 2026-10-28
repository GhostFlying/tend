package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// LogicalTurn returns obligations for one registered execution, never all history.
func LogicalTurn(ctx context.Context, q Querier, request string) (protocol.LogicalTurnState, error) {
	v := protocol.LogicalTurnState{Messages: []protocol.MessageAssociations{}, Contexts: []protocol.LogicalContextReview{}, Awaiting: []protocol.PendingClarification{}}
	if err := q.QueryRowContext(ctx, `SELECT l.logical_turn_id,l.status,l.reason,l.repair_attempts FROM logical_turns l JOIN logical_turn_executions e ON e.logical_turn_id=l.logical_turn_id WHERE e.request_id=?`, request).Scan(&v.ID, &v.Status, &v.Reason, &v.RepairAttempts); err != nil {
		return v, notFound(err, "logical turn")
	}
	pending, err := q.QueryContext(ctx, `SELECT logical_turn_id,message_id,revision,reason FROM logical_turns WHERE status='awaiting_clarification' AND logical_turn_id<>? ORDER BY rowid LIMIT 50`, v.ID)
	if err != nil {
		return v, err
	}
	for pending.Next() {
		var p protocol.PendingClarification
		if err = pending.Scan(&p.LogicalTurnID, &p.MessageID, &p.Revision, &p.Question); err != nil {
			pending.Close()
			return v, err
		}
		v.Awaiting = append(v.Awaiting, p)
	}
	err = pending.Err()
	pending.Close()
	if err != nil {
		return v, err
	}
	ids, err := FollowupIDs(ctx, q, `SELECT message_id FROM logical_turn_messages WHERE logical_turn_id=? ORDER BY message_id`, v.ID)
	if err != nil {
		return v, err
	}
	for _, id := range ids {
		m, err := MessageAssociations(ctx, q, id)
		if err != nil {
			return v, err
		}
		v.Messages = append(v.Messages, m)
	}
	rows, err := q.QueryContext(ctx, `SELECT c.work_item_id,c.revision FROM work_item_contexts c JOIN logical_turn_contexts lc ON lc.work_item_id=c.work_item_id WHERE lc.logical_turn_id=? ORDER BY c.work_item_id`, v.ID)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var c protocol.LogicalContextReview
		if err = rows.Scan(&c.WorkItemID, &c.Revision); err != nil {
			return v, err
		}
		v.Contexts = append(v.Contexts, c)
	}
	return v, rows.Err()
}

// WorkItemContextReviewed checks initialization and semantic review independently
// of native/logical execution lifecycle.
func WorkItemContextReviewed(ctx context.Context, q Querier, item string) (bool, error) {
	var ready bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_contexts WHERE work_item_id=? AND baseline_revision>0)
 AND NOT EXISTS(SELECT 1 FROM message_work_items a JOIN conversation_messages m ON m.message_id=a.message_id LEFT JOIN message_dispositions d ON d.message_id=m.message_id WHERE a.work_item_id=? AND a.active=1 AND m.conversation_id='main' AND (d.message_id IS NULL OR d.disposition='pending' OR d.facts_revision<>d.content_revision))
 AND NOT EXISTS(SELECT 1 FROM message_context_obligations WHERE work_item_id=? AND needs_review=1)
 AND NOT EXISTS(SELECT 1 FROM work_item_context_facts f JOIN work_items w ON w.work_item_id=? WHERE f.superseded=0 AND f.needs_review=1 AND (f.work_item_id=w.work_item_id OR f.scope='global' OR (f.scope='project' AND f.project=w.project)))`, item, item, item, item).Scan(&ready)
	return ready, err
}

// WorkItemContextReady admits reviewed facts before native completion, while
// retaining fences for uncertain execution and unreviewed context revisions.
func WorkItemContextReady(ctx context.Context, q Querier, item string) (bool, error) {
	ready, err := WorkItemContextReviewed(ctx, q, item)
	if err != nil || !ready {
		return ready, err
	}
	var blocked bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_contexts lc JOIN logical_turns l USING(logical_turn_id) JOIN work_item_contexts c ON c.work_item_id=lc.work_item_id WHERE lc.work_item_id=? AND l.status<>'completed' AND (l.status<>'processing' OR EXISTS(SELECT 1 FROM logical_turn_messages lm LEFT JOIN message_dispositions d ON d.message_id=lm.message_id WHERE lm.logical_turn_id=l.logical_turn_id AND (d.message_id IS NULL OR d.disposition='pending' OR d.classified_revision<>d.content_revision OR d.facts_revision<>d.content_revision)) OR NOT EXISTS(SELECT 1 FROM logical_turn_context_reviews r WHERE r.logical_turn_id=l.logical_turn_id AND r.work_item_id=c.work_item_id AND r.revision=c.revision))) OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p JOIN logical_turn_executions e ON e.request_id=p.request_id JOIN logical_turn_contexts lc USING(logical_turn_id) WHERE lc.work_item_id=?)`, item, item).Scan(&blocked)
	return !blocked, err
}
