package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ReconcileWorkspaces lists terminal ledger runs without claiming their
// native processes stopped. The upgraded runner reconciles exact local run
// records, then confirms quiescence through ReleaseWorkspace.
type ReconcileWorkspaces struct{}

func (ReconcileWorkspaces) Operation() string { return protocol.OpWorkspaceReconcile }
func (ReconcileWorkspaces) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	_, err := requireActiveHost(ctx, x, q)
	return err
}
func (ReconcileWorkspaces) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var payload struct{}
	if len(x.Env.Payload) > 0 {
		if err := decodePayload(x.Env, &payload); err != nil {
			return Outcome{}, err
		}
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT w.workspace_id,w.occupied_by,owner.lifecycle IN ('completed','cancelled')
 FROM workspaces w JOIN tasks owner ON owner.task_id=w.occupied_by
 WHERE w.host_id=? AND w.status NOT IN ('removing','removed')
 AND (owner.lifecycle IN ('completed','cancelled') OR NOT EXISTS
   (SELECT 1 FROM task_workspace_bindings b WHERE b.task_id=w.occupied_by AND b.workspace_id=w.workspace_id))
 AND NOT EXISTS(SELECT 1 FROM workspace_runs wr JOIN runs r ON r.run_id=wr.run_id
   WHERE wr.workspace_id=w.workspace_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown'))
 ORDER BY w.workspace_id`, x.Principal.Subject)
	if err != nil {
		return Outcome{}, fmt.Errorf("app: list workspace reconciliation: %w", err)
	}
	result := protocol.WorkspaceReconcileResult{Workspaces: []protocol.WorkspaceReconcile{}}
	for rows.Next() {
		var item protocol.WorkspaceReconcile
		if err := rows.Scan(&item.WorkspaceID, &item.TaskID, &item.TaskTerminal); err != nil {
			rows.Close()
			return Outcome{}, fmt.Errorf("app: scan workspace reconciliation: %w", err)
		}
		result.Workspaces = append(result.Workspaces, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Outcome{}, fmt.Errorf("app: list workspace reconciliation: %w", err)
	}
	for i := range result.Workspaces {
		item := &result.Workspaces[i]
		item.RunIDs = []string{}
		runs, err := tx.Q().QueryContext(ctx, `SELECT r.run_id,r.observed_state FROM workspace_runs wr JOIN runs r ON r.run_id=wr.run_id
   WHERE wr.workspace_id=? AND r.task_id=? AND r.host_id=? ORDER BY r.run_id`, item.WorkspaceID, item.TaskID, x.Principal.Subject)
		if err != nil {
			return Outcome{}, fmt.Errorf("app: list workspace reconciliation runs: %w", err)
		}
		for runs.Next() {
			var id string
			var observed protocol.RunObserved
			if err := runs.Scan(&id, &observed); err != nil {
				runs.Close()
				return Outcome{}, fmt.Errorf("app: scan reconciliation run: %w", err)
			}
			item.RunIDs = append(item.RunIDs, id)
			if observed == protocol.RunFinished {
				item.FinishedRunIDs = append(item.FinishedRunIDs, id)
			}
		}
		err = runs.Err()
		runs.Close()
		if err != nil {
			return Outcome{}, fmt.Errorf("app: list reconciliation runs: %w", err)
		}
	}
	return Outcome{Result: result, NoEffect: true}, nil
}
