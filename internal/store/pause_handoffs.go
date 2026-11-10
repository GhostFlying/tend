package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
)

func NegotiatedPauseHandoff(ctx context.Context, q Querier, r RunRow) (*protocol.PauseHandoff, error) {
	if r.PauseHandoff == nil {
		return nil, nil
	}
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_pause_contracts WHERE run_id=? AND handoff_allowed=1)`, r.RunID).Scan(&allowed)
	if err != nil || !allowed {
		return nil, err
	}
	return r.PauseHandoff, nil
}

func AssignmentPause(ctx context.Context, q Querier, as *protocol.Assignment) error {
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_pause_contracts WHERE run_id=? AND handoff_allowed=1)`, as.RunID).Scan(&as.PauseHandoffs); err != nil {
		return err
	}
	r, err := GetRun(ctx, q, as.RunID)
	if err != nil {
		return err
	}
	as.PauseHandoff, err = NegotiatedPauseHandoff(ctx, q, r)
	if err != nil {
		return err
	}
	var previous protocol.PauseContinuation
	err = q.QueryRowContext(ctx, `SELECT run_id,thread_id FROM run_pause_handoffs WHERE successor_run_id=? AND stopped=1`, as.RunID).Scan(&previous.RunID, &previous.ThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	as.PauseContinuation = &previous
	return nil
}

func PendingPausePredecessor(ctx context.Context, q Querier, action string) (string, error) {
	var run string
	err := q.QueryRowContext(ctx, `SELECT run_id FROM run_pause_handoffs WHERE continuation_action_id=? AND successor_run_id IS NULL AND stopped=1`, action).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return run, err
}
