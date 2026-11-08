package app

import (
	"context"
	"database/sql"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

// concurrencyDelay keeps one-second sampling while load is being observed.
// Empty groups have no saturation proof to integrate and wait for facts or an
// actual cooldown/retention deadline. Reactivation still resets missing proof.
func (a *App) concurrencyDelay(ctx context.Context) (time.Duration, error) {
	now := a.now().UnixMilli()
	var active bool
	var next sql.NullInt64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM concurrency_groups WHERE deleted=0 AND (json_extract(state_json,'$.group.occupied')>0 OR json_extract(state_json,'$.group.waiting')>0)) OR EXISTS(SELECT 1 FROM concurrency_executions e JOIN concurrency_bindings b USING(environment_id,runtime_id) JOIN concurrency_groups g ON g.group_id=b.group_id WHERE g.deleted=0 AND e.state<>'stopped')`).Scan(&active); err != nil {
			return err
		}
		return tx.Q().QueryRowContext(ctx, `SELECT MIN(at) FROM (
SELECT MIN(json_extract(state_json,'$.group.cooldown_until')) AS at FROM concurrency_groups WHERE deleted=0 AND json_extract(state_json,'$.group.cooldown_until')>?
UNION ALL SELECT MIN(received_at)+?+1 FROM concurrency_feedback WHERE source_json<>'{}'
UNION ALL SELECT MIN(at)+?+1 FROM concurrency_changes
)`, now, concurrencyRetention.Milliseconds(), concurrencyRetention.Milliseconds()).Scan(&next)
	})
	if err != nil {
		return 0, err
	}
	var delay time.Duration
	if active {
		delay = time.Second
	}
	if next.Valid {
		until := time.Duration(max(1, next.Int64-now)) * time.Millisecond
		if delay == 0 || until < delay {
			delay = until
		}
	}
	return delay, nil
}

func waitConcurrency(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
	var tick <-chan time.Time
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		tick = timer.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-wake:
	case <-tick:
	}
	return true
}
