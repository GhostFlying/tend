package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
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
			body, err = store.ResolveJSON(ctx, tx.Q(), body)
			if err != nil {
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
		var output, state, tool, input string
		var success bool
		err := tx.Q().QueryRowContext(ctx, `SELECT u.native_result_output,u.native_result_success,u.native_delivery_status,u.tool,u.input FROM runs current JOIN runs origin ON origin.task_id=current.task_id AND origin.rowid<current.rowid JOIN conversation_tool_uses u ON u.native_run_id=origin.run_id WHERE current.run_id=? AND origin.run_id=? AND current.role='issue_manager' AND origin.role='issue_manager' AND u.thread_id=? AND u.turn_id=? AND u.item_id=?`, current, origin, thread, turn, call).Scan(&output, &success, &state, &tool, &input)
		if err != nil {
			return err
		}
		if state == store.NativeDeliveryRejectedCurrentTask {
			input, err = store.ResolveJSON(ctx, tx.Q(), input)
			if err != nil {
				return err
			}
			output, err = currentManagerTaskRead(ctx, tx.Q(), current, tool, input)
			success = err == nil
		} else {
			output, err = store.ResolveJSON(ctx, tx.Q(), output)
		}
		if err != nil {
			return err
		}
		data, err := json.Marshal(map[string]any{"Success": success, "Text": output})
		raw = string(data)
		return err
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
	if method != "resume" {
		return nil, nil
	}
	var raw string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		closing, err := managerRunClosing(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		if !closing {
			return sql.ErrNoRows
		}
		raw, err = store.NativeThread(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		var actual struct{ ID string }
		if json.Unmarshal([]byte(raw), &actual) != nil || actual.ID != thread {
			return sql.ErrNoRows
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

func managerRunClosing(ctx context.Context, q store.Querier, run string) (bool, error) {
	var closing bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests WHERE run_id=? AND method='close' AND status IN ('pending','started','unknown'))`, run).Scan(&closing)
	if err != nil || closing {
		return closing, err
	}
	prior, err := unresolvedManagerParkClose(ctx, q, run)
	return prior != nil, err
}

// currentManagerTaskRead reconstructs one known rejected read, never a tool
// handler or external effect. A historical Run cannot restore current authority.
func currentManagerTaskRead(ctx context.Context, q store.Querier, run, tool, input string) (string, error) {
	target := store.NativeTaskReadTarget(tool, input)
	if target == "" {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "original task read arguments are unavailable")
	}
	r, err := store.GetRun(ctx, q, run)
	if err != nil {
		return "", err
	}
	m, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
	if err != nil {
		return "", err
	}
	current, err := store.CurrentIssueManagerTask(ctx, q, m.IssueID)
	if err != nil {
		return "", errNotFound(err, "current Issue responsibility is unavailable")
	}
	issue, err := store.GetCanonicalIssue(ctx, q, m.IssueID)
	if err != nil {
		return "", err
	}
	if current.TaskID != m.TaskID || m.State != "assigned" || issue.AssignmentEpoch != m.AssignmentEpoch || issue.AssignmentState != "active" || r.ObservedState == protocol.RunFinished || r.ObservedState == protocol.RunFailed {
		return "", protocol.Errorf(protocol.CodeForbidden, "historical tool result grants no current Issue read authority")
	}
	if _, err := store.GetTask(ctx, q, target); err != nil {
		return "", errNotFound(err, "task does not exist")
	}
	allowed, err := store.TaskIssueResultReadable(ctx, q, target, m.IssueID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", protocol.Errorf(protocol.CodeForbidden, "task execution facts are outside this Issue")
	}
	value, err := views.Task(ctx, q, target)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{"ok": true, "result": value, "observation": "current", "original_delivery": "rejected"})
	return string(body), err
}
