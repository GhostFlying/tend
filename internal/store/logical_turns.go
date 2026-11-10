package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// LogicalTurn returns obligations for one registered execution, never all history.
func LogicalTurn(ctx context.Context, q Querier, request string) (protocol.LogicalTurnState, error) {
	scope, err := HostedIssueScope(ctx, q, request)
	if err != nil {
		return protocol.LogicalTurnState{}, err
	}
	v := protocol.LogicalTurnState{Messages: []protocol.MessageAssociations{}, Contexts: []protocol.LogicalContextReview{}, Awaiting: []protocol.PendingClarification{}}
	if err := q.QueryRowContext(ctx, `SELECT l.logical_turn_id,l.status,l.reason,l.repair_attempts FROM logical_turns l JOIN logical_turn_executions e ON e.logical_turn_id=l.logical_turn_id WHERE e.request_id=?`, request).Scan(&v.ID, &v.Status, &v.Reason, &v.RepairAttempts); err != nil {
		return v, notFound(err, "logical turn")
	}
	processing, err := q.QueryContext(ctx, `SELECT message_id,content_revision,association_revision,processed_at FROM logical_turn_processing WHERE logical_turn_id=? ORDER BY message_id,content_revision,association_revision`, v.ID)
	if err != nil {
		return v, err
	}
	for processing.Next() {
		var receipt protocol.MessageProcessingReceipt
		if err := processing.Scan(&receipt.MessageID, &receipt.ContentRevision, &receipt.AssociationRevision, &receipt.ProcessedAtMillis); err != nil {
			processing.Close()
			return v, err
		}
		v.Processing = append(v.Processing, receipt)
	}
	err = processing.Err()
	processing.Close()
	if err != nil {
		return v, err
	}
	var hosted bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_executions WHERE execution_id=?)`, request).Scan(&hosted); err != nil {
		return v, err
	}
	if hosted {
		v.CoordinationExecutionID, err = HostedCoordinationIdentity(ctx, q, request)
		if err != nil {
			return v, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(effect_seq),0)+1 FROM hosted_coordination_effects WHERE execution_id=?`, v.CoordinationExecutionID).Scan(&v.NextEffectSeq); err != nil {
			return v, err
		}
	}
	pending, err := q.QueryContext(ctx, `SELECT logical_turn_id,message_id,revision,reason FROM logical_turns WHERE status='awaiting_clarification' AND logical_turn_id<>? AND ?='' ORDER BY rowid LIMIT 50`, v.ID, scope)
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
	rows, err := q.QueryContext(ctx, `SELECT c.work_item_id,c.revision FROM work_item_contexts c JOIN logical_turn_contexts lc ON lc.work_item_id=c.work_item_id WHERE lc.logical_turn_id=? AND (?='' OR c.work_item_id=?) ORDER BY c.work_item_id`, v.ID, scope, scope)
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

// WorkItemContextReviewed retains the historical query name for callers. A
// stored context is available without a separate semantic-review certificate.
func WorkItemContextReviewed(ctx context.Context, q Querier, item string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_contexts WHERE work_item_id=?)`, item).Scan(&exists)
	return exists, err
}

// WorkItemContextReady checks real unresolved native outcomes. Message changes
// and historical review metadata are inputs for the responsible Agent's judgment.
func WorkItemContextReady(ctx context.Context, q Querier, item string) (bool, error) {
	ready, err := WorkItemContextReviewed(ctx, q, item)
	if err != nil || !ready {
		return ready, err
	}
	var blocked bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_contexts lc JOIN logical_turns l USING(logical_turn_id) WHERE lc.work_item_id=? AND l.status IN ('unknown','awaiting_clarification','stopped')) OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p JOIN logical_turn_executions e ON e.request_id=p.request_id JOIN logical_turn_contexts lc USING(logical_turn_id) WHERE lc.work_item_id=?)`, item, item).Scan(&blocked)
	return !blocked, err
}
