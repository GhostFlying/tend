package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ContextBlockers names exact unresolved objects; callers never need to infer
// a review obligation from the fact that a coordinator is still running.
func ContextBlockers(ctx context.Context, q Querier, item string) ([]protocol.ContextBlocker, error) {
	rows, err := q.QueryContext(ctx, `SELECT 'logical_turn_state',l.logical_turn_id,0,l.status FROM logical_turn_contexts lc JOIN logical_turns l USING(logical_turn_id) WHERE lc.work_item_id=? AND l.status IN ('unknown','awaiting_clarification','stopped')
 UNION ALL SELECT 'native_receipt',p.request_id,0,'native execution outcome requires reconciliation' FROM native_turn_receipts_pending p JOIN logical_turn_executions e ON e.request_id=p.request_id JOIN logical_turn_contexts lc USING(logical_turn_id) WHERE lc.work_item_id=?`, item, item)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ContextBlocker{}
	for rows.Next() {
		var b protocol.ContextBlocker
		if err := rows.Scan(&b.Code, &b.ObjectID, &b.Revision, &b.Message); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
