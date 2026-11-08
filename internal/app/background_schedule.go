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
// Callers capture Changes().Wait and scannedAt before reading candidates.
// Deadlines crossed during the scan still schedule an immediate rescan.
func (a *App) BackgroundDelay(ctx context.Context, scannedAt time.Time) (time.Duration, error) {
	now := a.now().UnixMilli()
	boundary := scannedAt.UnixMilli()
	var next sql.NullInt64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT MIN(at) FROM (
SELECT MIN(next_attempt_at) AS at FROM outbox WHERE status='pending' AND destination LIKE 'background_thread:%' AND next_attempt_at>?
UNION ALL SELECT MIN(p.next_reconcile_at) FROM native_turn_receipts_pending p JOIN main_turns t ON t.request_id=p.request_id JOIN hosted_executions h ON h.execution_id=t.request_id WHERE h.conversation_id<>'main' AND p.next_reconcile_at>? AND NOT EXISTS(SELECT 1 FROM agent_recoveries r WHERE r.active_request_id=t.request_id OR r.successor_message_id=t.message_id)
UNION ALL SELECT MIN(r.next_reconcile_at) FROM agent_recoveries r JOIN main_turns t ON t.request_id=r.active_request_id OR t.message_id=r.successor_message_id JOIN native_turn_receipts_pending p ON p.request_id=t.request_id WHERE r.conversation_id<>'main' AND r.next_reconcile_at>? AND (COALESCE(t.turn_id,'')<>'' OR NOT EXISTS(SELECT 1 FROM agent_recovery_attempts e WHERE e.request_id=t.request_id))
)`, boundary, boundary, boundary).Scan(&next)
	})
	if err != nil || !next.Valid {
		return 0, err
	}
	return time.Duration(max(1, next.Int64-now)) * time.Millisecond, nil
}
