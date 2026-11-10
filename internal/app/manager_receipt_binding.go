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

// A read-only transport retains the mode accepted by its original ordinary
// execution. Current runtime availability cannot identify an older thread.
func retainedManagerNativeOwnerMode(ctx context.Context, q store.Querier, m store.IssueManagerTask) (bool, error) {
	var run string
	var mode sql.NullBool
	err := q.QueryRowContext(ctx, `SELECT r.run_id,json_extract(a.trigger_json,'$.native_owner_tools') FROM runs r JOIN plan_actions a USING(action_id) WHERE r.task_id=? AND COALESCE(json_extract(a.trigger_json,'$.receipt_only'),0)=0 ORDER BY a.generation DESC LIMIT 1`, m.TaskID).Scan(&run, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return false, protocol.Errorf(protocol.CodeUnknownOutcome, "original manager native binding mode is unavailable")
	}
	if err != nil {
		return false, err
	}
	pin, err := store.GetRunExecution(ctx, q, run)
	if errors.Is(err, store.ErrNotFound) {
		return false, protocol.Errorf(protocol.CodeUnknownOutcome, "original manager native execution pin is unavailable")
	}
	if err != nil {
		return false, err
	}
	if m.Pin == nil || pin == nil || *m.Pin != *pin {
		return false, protocol.Errorf(protocol.CodeUnknownOutcome, "original manager native binding execution changed")
	}
	if mode.Valid {
		return mode.Bool, nil
	}
	// Older actions may predate the mode field. Their admitted execution
	// snapshot still identifies the original binding, including its false default.
	var raw string
	err = q.QueryRowContext(ctx, `SELECT private_snapshot FROM hosted_executions WHERE issue_id=? AND assignment_epoch=? AND json_extract(private_snapshot,'$.runner_run_id')=? ORDER BY created_at DESC,execution_id DESC LIMIT 1`, m.IssueID, m.AssignmentEpoch, run).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, protocol.Errorf(protocol.CodeUnknownOutcome, "original manager native binding mode is unavailable")
	}
	if err != nil {
		return false, err
	}
	var original hostedconfig.Selection
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		return false, err
	}
	if !original.RunnerManaged || original.RunnerRunID != run || original.Identity != pin.Identity {
		return false, protocol.Errorf(protocol.CodeUnknownOutcome, "original manager native binding identity changed")
	}
	return original.NativeOwnerTools, nil
}
