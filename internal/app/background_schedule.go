package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

// BackgroundNow shares the injected ledger clock with local attachment retries.
func (a *App) BackgroundNow() time.Time { return a.now() }

// BackgroundDelay names the next future queue/reconciliation deadline. Already
// due but inadmissible work waits for a committed fact change, not another poll.
// Callers capture Changes().Wait before reading candidates and this deadline.
func (a *App) BackgroundDelay(ctx context.Context) (time.Duration, error) {
	now := a.now().UnixMilli()
	var next sql.NullInt64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT MIN(at) FROM (
SELECT MIN(next_attempt_at) AS at FROM outbox WHERE status='pending' AND destination LIKE 'background_thread:%' AND next_attempt_at>?
UNION ALL SELECT MIN(next_reconcile_at) FROM native_turn_receipts_pending WHERE next_reconcile_at>?
UNION ALL SELECT MIN(next_reconcile_at) FROM agent_recoveries WHERE status IN ('starting','running','unknown') AND conversation_id<>'main' AND next_reconcile_at>?
)`, now, now, now).Scan(&next)
	})
	if err != nil || !next.Valid {
		return 0, err
	}
	return time.Duration(max(1, next.Int64-now)) * time.Millisecond, nil
}
