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

// WorkItemContextReady gates background dispatch on actual source review and
// logical completion, not just a nonempty summary or a classification label.
func WorkItemContextReady(ctx context.Context, q Querier, item string) (bool, error) {
	var ready bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_contexts WHERE work_item_id=? AND baseline_revision>0)
 AND NOT EXISTS(SELECT 1 FROM message_work_items a JOIN conversation_messages m ON m.message_id=a.message_id LEFT JOIN message_dispositions d ON d.message_id=m.message_id WHERE a.work_item_id=? AND a.active=1 AND m.conversation_id='main' AND (d.message_id IS NULL OR d.disposition='pending' OR d.facts_revision<>d.content_revision))
 AND NOT EXISTS(SELECT 1 FROM message_context_obligations WHERE work_item_id=? AND needs_review=1)
 AND NOT EXISTS(SELECT 1 FROM logical_turn_contexts lc JOIN logical_turns l ON l.logical_turn_id=lc.logical_turn_id WHERE lc.work_item_id=? AND l.status<>'completed')`, item, item, item, item).Scan(&ready)
	return ready, err
}
