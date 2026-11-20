package store

import (
	"context"
	"database/sql"
	"errors"
)

// ReceiptTurn resolves the execution's current native turn under its original Run.
func ReceiptTurn(ctx context.Context, q Querier, rec MainTurn) (id string, waiting bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT c.native_turn,c.awaiting_start FROM hosted_executions e JOIN manager_continued_turns c ON c.run_id=json_extract(e.private_snapshot,'$.runner_run_id') WHERE e.execution_id=? AND c.thread_id=? AND c.root_turn=?`, rec.RequestID, rec.ThreadID, rec.TurnID).Scan(&id, &waiting)
	if errors.Is(err, sql.ErrNoRows) {
		return rec.TurnID, false, nil
	}
	return
}
