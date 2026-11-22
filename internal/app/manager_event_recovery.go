package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type managerProjectionHead struct {
	run      string
	sequence int64
}

const managerProjectionSourceRequired = "original_source_required"

// A parked source dependency becomes runnable only when its original delivery
// is unambiguous. Recheck it during selection so a concurrent source write
// cannot get lost between a failed projection and recording its dependency.
const managerProjectionSourceAvailable = `(SELECT COUNT(*)=1 AND MIN(t.message_id)<>''
 FROM main_turns t JOIN hosted_executions h ON h.execution_id=t.request_id
 WHERE t.thread_id=json_extract(e.body_json,'$.thread_id')
 AND t.turn_id=COALESCE(NULLIF(json_extract(e.body_json,'$.root_turn_id'),''),json_extract(e.body_json,'$.native.TurnID'))
 AND h.conversation_id=m.conversation_id AND json_extract(h.private_snapshot,'$.runner_run_id')=e.run_id)`

func (a *App) managerProjectionHeads(ctx context.Context) (heads []managerProjectionHead, delay time.Duration, err error) {
	now := a.now().UnixMilli()
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT e.run_id,e.seq FROM manager_native_events e
 JOIN runs r USING(run_id) JOIN issue_manager_tasks m USING(task_id)
 LEFT JOIN manager_event_apply_failures f ON f.run_id=e.run_id AND f.seq=e.seq
 WHERE e.applied=0 AND `+managerProjectionRecoveryRun+`
 AND NOT EXISTS(SELECT 1 FROM manager_native_events p WHERE p.run_id=e.run_id AND p.seq<e.seq AND p.applied=0)
 AND (f.run_id IS NULL OR f.retry_at>0 AND f.retry_at<=?
      OR f.failure_code='`+managerProjectionSourceRequired+`' AND `+managerProjectionSourceAvailable+`)
 ORDER BY e.rowid LIMIT 16`, now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h managerProjectionHead
			if err := rows.Scan(&h.run, &h.sequence); err != nil {
				rows.Close()
				return err
			}
			heads = append(heads, h)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		var future sql.NullInt64
		if err := tx.Q().QueryRowContext(ctx, `SELECT MIN(f.retry_at) FROM manager_event_apply_failures f
 JOIN manager_native_events e USING(run_id,seq) JOIN runs r USING(run_id) JOIN issue_manager_tasks m USING(task_id)
 WHERE e.applied=0 AND f.retry_at>? AND `+managerProjectionRecoveryRun, now).Scan(&future); err != nil {
			return err
		}
		if future.Valid {
			delay = time.Duration(future.Int64-now) * time.Millisecond
		}
		return nil
	})
	return heads, delay, err
}

func (a *App) recordManagerProjectionFailure(ctx context.Context, h managerProjectionHead, failure error) error {
	ctx = store.WithWriteOperation(store.WithWriteEvent(ctx, h.run, h.sequence), "manager.event.recovery_failure")
	code := string(protocol.CodeOf(failure))
	retryAt := a.now().Add(time.Second).UnixMilli()
	switch {
	case errors.Is(failure, errManagerProjectionUnsupported):
		code = "original_receipt_required"
	case errors.Is(failure, errManagerProjectionSourceRequired):
		code, retryAt = managerProjectionSourceRequired, 0
	}
	return a.write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO manager_event_apply_failures(run_id,seq,attempts,retry_at,failure_code)
 SELECT run_id,seq,1,?,? FROM manager_native_events WHERE run_id=? AND seq=? AND applied=0
 ON CONFLICT(run_id) DO UPDATE SET seq=excluded.seq,
 attempts=CASE WHEN seq=excluded.seq AND failure_code<>'`+managerProjectionSourceRequired+`' THEN attempts+1 ELSE 1 END,
 retry_at=CASE WHEN seq=excluded.seq AND failure_code<>'`+managerProjectionSourceRequired+`' AND attempts>=3 THEN 0 ELSE excluded.retry_at END,
 failure_code=excluded.failure_code`, retryAt, code, h.run, h.sequence)
		return err
	})
}

// RunManagerEventRecovery consumes only retained projections. It is independent
// of a native process, workspace, attachment, or startup recovery budget.
func (a *App) RunManagerEventRecovery(ctx context.Context, log *slog.Logger) {
	var unavailableDelay time.Duration
	for ctx.Err() == nil {
		wake := a.SchedulingChanges().Wait()
		heads, delay, err := a.managerProjectionHeads(ctx)
		ledgerUnavailable := err != nil
		for _, h := range heads {
			err := a.projectManagerNativeEvent(ctx, h.run, h.sequence)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				continue
			}
			mark, cancel := context.WithTimeout(ctx, managerProjectionBudget)
			markErr := a.recordManagerProjectionFailure(mark, h, err)
			cancel()
			if markErr != nil {
				ledgerUnavailable = true
				log.WarnContext(ctx, "could not record native event application failure", "run_id", h.run, "sequence", h.sequence, "err", markErr)
			}
		}
		if ctx.Err() != nil {
			return
		}
		if ledgerUnavailable {
			// An unavailable writer cannot even persist the per-Run budget.
			// Back off the inventory read independently of that budget.
			unavailableDelay = min(30*time.Second, max(time.Second, 2*unavailableDelay))
			delay = unavailableDelay
		} else {
			unavailableDelay = 0
			if len(heads) != 0 {
				continue
			}
		}

		if delay == 0 {
			select {
			case <-ctx.Done():
			case <-wake:
			}
			continue
		}
		timer := time.NewTimer(delay)
		if ledgerUnavailable {
			wake = nil
		}
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-wake:
		}
		timer.Stop()
	}
}
