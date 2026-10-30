package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/store"
)

// ManagerReplayEvent preserves the original run/sequence while a later
// attachment consumes unacknowledged facts of the same responsibility.
type ManagerReplayEvent struct {
	Cursor   int64
	RunID    string
	Sequence int64
	Body     json.RawMessage
}

func (a *App) ManagerReplayEvents(ctx context.Context, run string, after int64) ([]ManagerReplayEvent, error) {
	var result []ManagerReplayEvent
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT e.rowid,e.run_id,e.seq,e.body_json FROM runs current JOIN runs origin ON origin.task_id=current.task_id AND origin.rowid<=current.rowid JOIN manager_native_events e ON e.run_id=origin.run_id WHERE current.run_id=? AND current.role='issue_manager' AND origin.role='issue_manager' AND e.applied=0 AND e.rowid>? ORDER BY e.rowid LIMIT 100`, run, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event ManagerReplayEvent
			var body string
			if err := rows.Scan(&event.Cursor, &event.RunID, &event.Sequence, &body); err != nil {
				return err
			}
			event.Body = json.RawMessage(body)
			result = append(result, event)
		}
		return rows.Err()
	})
	return result, err
}

// ManagerRetainedToolResult only reads a result already produced for the
// original callback. Missing results never authorize running that tool again.
func (a *App) ManagerRetainedToolResult(ctx context.Context, current, origin, thread, turn, call string) (json.RawMessage, error) {
	var raw string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT json_extract(n.payload_json,'$.tool_result') FROM runs current JOIN runs origin ON origin.task_id=current.task_id AND origin.rowid<current.rowid JOIN manager_native_requests n ON n.run_id=origin.run_id WHERE current.run_id=? AND origin.run_id=? AND current.role='issue_manager' AND origin.role='issue_manager' AND n.method='tool_result' AND json_extract(n.payload_json,'$.thread_id')=? AND json_extract(n.payload_json,'$.turn_id')=? AND json_extract(n.payload_json,'$.call_id')=? ORDER BY n.rowid LIMIT 1`, current, origin, thread, turn, call).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

// ManagerClosingAttachment reads only confirmed metadata of this run. It lets a
// restarted server attach its event pump while the runner is awaiting close
// acknowledgement; queuing another native resume behind close would deadlock.
func (a *App) ManagerClosingAttachment(ctx context.Context, run, method, thread string) (json.RawMessage, error) {
	if method != "models" && method != "resume" {
		return nil, nil
	}
	var raw string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT json_extract(n.result_json,'$.result') FROM manager_native_requests n JOIN runs r USING(run_id)
 WHERE n.run_id=? AND r.role='issue_manager' AND n.status='completed' AND COALESCE(json_extract(n.result_json,'$.error'),'')=''
 AND ((?='models' AND n.method='models') OR (?='resume' AND n.method IN ('start','resume') AND json_extract(n.result_json,'$.result.ID')=?))
 AND EXISTS(SELECT 1 FROM manager_native_requests closing WHERE closing.run_id=n.run_id AND closing.method='close' AND closing.status IN ('pending','started','unknown'))
 ORDER BY n.rowid DESC LIMIT 1`, run, method, method, thread).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

func managerRunClosing(ctx context.Context, q store.Querier, run string) (bool, error) {
	var closing bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests WHERE run_id=? AND method='close' AND status IN ('pending','started','unknown'))`, run).Scan(&closing)
	return closing, err
}
