package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// LocalTurns scopes native input admission and recovery to the original
// Run. A missing native start receipt never authorizes replay.
type LocalTurns struct {
	Journal *Journal
	RunID   string
}

func (m LocalTurns) Root(ctx context.Context, thread, turn string) (string, error) {
	var root string
	err := m.Journal.db.QueryRowContext(ctx, `SELECT root_turn FROM manager_local_turns WHERE run_id=? AND thread_id=? AND native_turn=?`, m.RunID, thread, turn).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return root, err
}
func (m LocalTurns) Queue(ctx context.Context, call harness.ToolCall, next string) error {
	return m.Journal.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO manager_local_turns(run_id,thread_id,previous_turn,root_turn,call_id,next_input,state)
    SELECT ?,?,?,COALESCE((SELECT root_turn FROM manager_local_turns WHERE run_id=? AND thread_id=? AND native_turn=?),?),?,?,'queued'
    ON CONFLICT(run_id,thread_id,previous_turn) DO NOTHING`, m.RunID, call.ThreadID, call.TurnID, m.RunID, call.ThreadID, call.TurnID, call.TurnID, call.CallID, next)
		if err != nil {
			return err
		}
		var same bool
		if err = tx.QueryRowContext(ctx, `SELECT call_id=? AND next_input=? FROM manager_local_turns WHERE run_id=? AND thread_id=? AND previous_turn=?`, call.CallID, next, m.RunID, call.ThreadID, call.TurnID).Scan(&same); err != nil {
			return err
		}
		if !same {
			return errors.New("the original turn already chose a different continuation")
		}
		return nil
	})
}
func (m LocalTurns) Begin(ctx context.Context, thread, turn string) (next, call string, err error) {
	err = m.Journal.db.QueryRowContext(ctx, `UPDATE manager_local_turns SET state='starting' WHERE run_id=? AND thread_id=? AND previous_turn=? AND state='queued' RETURNING next_input,call_id`, m.RunID, thread, turn).Scan(&next, &call)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return
}
func (m LocalTurns) Started(ctx context.Context, thread, previous, native string) error {
	res, err := m.Journal.db.ExecContext(ctx, `UPDATE manager_local_turns SET state='started',native_turn=? WHERE run_id=? AND thread_id=? AND previous_turn=? AND state='starting'`, native, m.RunID, thread, previous)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("missing original continuation for turn %s", previous)
	}
	return nil
}

func (m LocalTurns) HasNext(ctx context.Context, thread, previous string) (bool, error) {
	var exists bool
	err := m.Journal.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_local_turns WHERE run_id=? AND thread_id=? AND previous_turn=?)`, m.RunID, thread, previous).Scan(&exists)
	return exists, err
}
