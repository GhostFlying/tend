package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetRunExecution(ctx context.Context, q Querier, id string) (*protocol.RunExecutionPin, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT pin_json FROM run_agent_executions WHERE run_id=?`, id).Scan(&raw); err != nil {
		return nil, notFound(err, "run execution")
	}
	var p protocol.RunExecutionPin
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return &p, nil
}
func (t *Tx) InsertRunExecution(ctx context.Context, id string, p protocol.RunExecutionPin) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO run_agent_executions(run_id,pin_json) VALUES(?,?)`, id, string(raw))
	return err
}
func (t *Tx) BindWorkspaceBackend(ctx context.Context, id, backend string) error {
	result, err := t.Exec(ctx, `UPDATE workspaces SET backend_id=? WHERE workspace_id=? AND (backend_id='' OR backend_id=?)`, backend, id, backend)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return protocol.Errorf(protocol.CodeConflictPayload, "workspace backend has changed")
	}
	return nil
}

// PreviousTaskExecution reads the actual most recent pin for one unchanged role
// agreement. A new spec or a different task/role is a new selection boundary.
func PreviousTaskExecution(ctx context.Context, q Querier, task string, spec int64, role protocol.RunRole) (*protocol.RunExecutionPin, error) {
	var id string
	if err := q.QueryRowContext(ctx, `SELECT r.run_id FROM runs r
 JOIN plan_actions a USING(action_id) JOIN run_agent_executions e USING(run_id)
 WHERE r.task_id=? AND a.target_spec_version=? AND r.role=?
 AND NOT EXISTS (SELECT 1 FROM abandoned_workspaces w
 WHERE w.task_id=r.task_id AND w.host_id=r.host_id)
 ORDER BY r.created_at DESC,r.rowid DESC LIMIT 1`, task, spec, role).Scan(&id); err != nil {
		return nil, notFound(err, "previous task execution")
	}
	return GetRunExecution(ctx, q, id)
}
