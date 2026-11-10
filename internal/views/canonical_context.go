package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A shared planner context is not ownership of an Issue execution. Resolve the
// complete logical chain, including old epochs and repair requests, using its
// persisted execution identity. Only historical chains without a hosted identity
// fall back to the original request's conversation, never arbitrary reply text.
const canonicalContextExecutions = `WITH owned_logical AS (
 SELECT DISTINCT e.logical_turn_id FROM logical_turn_executions e
 JOIN hosted_executions h ON h.execution_id=e.request_id WHERE h.issue_id=?
 UNION SELECT DISTINCT e.logical_turn_id FROM logical_turn_executions e
 JOIN main_turns t ON t.request_id=e.request_id
 JOIN conversation_messages m ON m.message_id=t.message_id
 WHERE e.repair=0 AND m.conversation_id='issue:'||?
 AND NOT EXISTS(SELECT 1 FROM logical_turn_executions all_e
 JOIN hosted_executions h ON h.execution_id=all_e.request_id WHERE all_e.logical_turn_id=e.logical_turn_id)
 ) `

// canonicalContextStatus projects this Issue's review and reconciliation duties.
// It does not authorize task creation or replace the scheduler's admission.
func canonicalContextStatus(ctx context.Context, q store.Querier, out *protocol.IssueContextView) error {
	id := out.IssueID
	out.DispatchBlockers = []protocol.ContextBlocker{}
	out.SyncStatus = "available"
	rows, err := q.QueryContext(ctx, canonicalContextExecutions+`SELECT 'logical_turn_state',l.logical_turn_id,c.revision,l.status FROM owned_logical o JOIN logical_turns l USING(logical_turn_id) JOIN work_item_contexts c ON c.work_item_id=? WHERE l.status IN ('unknown','awaiting_clarification','stopped')
 UNION ALL SELECT 'native_receipt',p.request_id,0,'native execution outcome requires reconciliation' FROM native_turn_receipts_pending p JOIN logical_turn_executions e ON e.request_id=p.request_id JOIN owned_logical o USING(logical_turn_id)`, id, id, id)

	if err != nil {
		return err
	}
	for rows.Next() {
		var b protocol.ContextBlocker
		if err := rows.Scan(&b.Code, &b.ObjectID, &b.Revision, &b.Message); err != nil {
			rows.Close()
			return err
		}
		out.DispatchBlockers = append(out.DispatchBlockers, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, canonicalContextExecutions+`SELECT COALESCE((SELECT CASE WHEN EXISTS(SELECT 1 FROM native_turn_receipts_pending p JOIN logical_turn_executions e ON e.request_id=p.request_id WHERE e.logical_turn_id=l.logical_turn_id) THEN 'recovering' ELSE l.status END FROM owned_logical o JOIN logical_turns l USING(logical_turn_id) WHERE l.status IN ('processing','unknown','awaiting_clarification','stopped') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p JOIN logical_turn_executions e ON e.request_id=p.request_id WHERE e.logical_turn_id=l.logical_turn_id) ORDER BY l.updated_at DESC,l.logical_turn_id LIMIT 1),'idle')`, id, id).Scan(&out.CoordinationState); err != nil {
		return err
	}
	out.DispatchReady = len(out.DispatchBlockers) == 0
	return nil
}
