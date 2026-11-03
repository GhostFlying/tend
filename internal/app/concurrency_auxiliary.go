package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AcquireAuxiliaryConcurrency reserves an independent model execution before
// forking. Unbound runtimes still retain lifecycle facts for a later binding.
func (a *App) AcquireAuxiliaryConcurrency(ctx context.Context, conversation string, selection *hostedconfig.Selection) (string, error) {
	if selection == nil {
		return "", nil
	}
	id := "aux:" + a.ids.New(protocol.PrefixRequest)
	now := a.now().UnixMilli()
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		return acquireAuxiliaryConcurrency(ctx, tx, id, conversation, selection, now)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func acquireAuxiliaryConcurrency(ctx context.Context, tx *store.Tx, id, conversation string, selection *hostedconfig.Selection, now int64) error {
	if selection == nil {
		return nil
	}
	source := protocol.ConcurrencySource{ExecutionID: id, SessionID: id, ConversationID: conversation, EnvironmentID: selection.Identity.ExecutionEnvironmentID, RuntimeID: selection.Runtime.ID, AgentID: selection.Identity.LocalID, IssueID: selection.IssueID, RunID: selection.RunnerRunID, Attempt: id, Purpose: string(selection.Purpose)}
	if source.IssueID != "" {
		err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(json_extract(body_json,'$.title'),'') FROM work_items WHERE work_item_id=?`, source.IssueID).Scan(&source.IssueTitle)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	group, err := concurrencyBinding(ctx, tx.Q(), source.EnvironmentID, source.RuntimeID)
	if err != nil {
		return err
	}
	if group == "" {
		return putConcurrencyExecution(ctx, tx, source, "reserved", now)
	}
	available, err := concurrencyAvailable(ctx, tx.Q(), source.EnvironmentID, source.RuntimeID, id, now)
	if err != nil {
		return err
	}
	if !available {
		if selection.RunnerRunID == "" {
			return ErrConcurrencyCapacity
		}
		control, err := concurrencyState(ctx, tx.Q(), group)
		if err != nil {
			return err
		}
		if now < control.Group.CooldownUntil {
			return ErrConcurrencyCapacity
		}
		parent := "run:" + selection.RunnerRunID
		var canBorrow bool
		err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM concurrency_executions e JOIN runs r ON e.execution_id='run:'||r.run_id WHERE e.execution_id=? AND e.environment_id=? AND e.runtime_id=? AND e.state IN ('reserved','running') AND r.role='issue_manager' AND r.observed_state NOT IN ('finished','failed','unknown','interrupted') AND NOT EXISTS(SELECT 1 FROM concurrency_executions child WHERE child.parent_execution_id=e.execution_id AND child.state<>'stopped') AND NOT EXISTS(SELECT 1 FROM main_turns t JOIN hosted_executions h ON h.execution_id=t.request_id WHERE json_extract(h.private_snapshot,'$.runner_run_id')=r.run_id AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id))))`, parent, source.EnvironmentID, source.RuntimeID).Scan(&canBorrow)
		if err != nil {
			return err
		}
		if !canBorrow {
			return ErrConcurrencyCapacity
		}
		source.ParentExecutionID = parent
		source.RunID = selection.RunnerRunID
	}
	return putConcurrencyExecution(ctx, tx, source, "reserved", now)
}

// Compact state and its permit share a transaction, including native unknowns.
func concurrencyCompactState(ctx context.Context, tx *store.Tx, op, thread, state string, now int64) error {
	var raw string
	err := tx.Q().QueryRowContext(ctx, `SELECT source_json FROM concurrency_executions WHERE execution_id=?`, "aux:compact:"+op).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var source protocol.ConcurrencySource
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		return err
	}
	source.NativeSessionID = thread
	source.Purpose = "manual_compaction"
	next := "unknown"
	switch state {
	case "starting":
		next = "reserved"
	case "running":
		next = "running"
	case "completed", "failed":
		next = "stopped"
	}
	return putConcurrencyExecution(ctx, tx, source, next, now)
}
func (a *App) ObserveAuxiliaryConcurrency(ctx context.Context, id string, f protocol.ConcurrencyFeedback, stopped bool) error {
	if id == "" {
		return nil
	}
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		var raw, state string
		if err := tx.Q().QueryRowContext(ctx, `SELECT source_json,state FROM concurrency_executions WHERE execution_id=?`, id).Scan(&raw, &state); err != nil {
			return err
		}
		var source protocol.ConcurrencySource
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			return err
		}
		if state == "stopped" {
			return nil
		}
		source.NativeSessionID = f.SessionID
		source.TurnID = f.TurnID
		next := state
		if f.Kind == "" && !stopped {
			next = "unknown"
		}
		if f.Kind == "running" {
			next = "running"
		}
		if stopped {
			next = "stopped"
		}
		if err := putConcurrencyExecution(ctx, tx, source, next, a.now().UnixMilli()); err != nil {
			return err
		}
		return recordConcurrencyFeedback(ctx, tx, source, f, a.now().UnixMilli())
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

// StopAuxiliaryConcurrency settles only forks owned by the exact closed session.
func (a *App) StopAuxiliaryConcurrency(ctx context.Context, ids []string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE concurrency_executions SET state='stopped',last_seen=? WHERE execution_id=? AND execution_id LIKE 'aux:%'`, a.now().UnixMilli(), id); err != nil {
				return err
			}
		}
		return nil
	})
}
