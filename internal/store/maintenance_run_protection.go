package store

import (
	"context"
	"fmt"
)

// MaintenanceOpenRuns reads the same complete observed-state set as archive
// host counts, including unfinished runs belonging to terminal tasks.
func MaintenanceOpenRuns(ctx context.Context, q Querier) ([]RunRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE observed_state IN `+openRunStates+` ORDER BY run_id`)
	if err != nil {
		return nil, fmt.Errorf("store: maintenance open runs: %w", err)
	}
	defer rows.Close()
	var result []RunRow
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

type MaintenanceRunProtection struct {
	MaintenanceID, RunID, TaskID, ActionID, HostID, LeaseID string
	OriginalRequestsJSON                                    string
}

func MaintenanceProtectedRuns(ctx context.Context, q Querier, run string) ([]MaintenanceRunProtection, error) {
	rows, err := q.QueryContext(ctx, `SELECT maintenance_id,run_id,task_id,action_id,host_id,lease_id,original_requests_json FROM maintenance_protected_runs WHERE run_id=? ORDER BY created_at,maintenance_id`, run)
	if err != nil {
		return nil, fmt.Errorf("store: maintenance protection: %w", err)
	}
	defer rows.Close()
	var result []MaintenanceRunProtection
	for rows.Next() {
		var p MaintenanceRunProtection
		if err := rows.Scan(&p.MaintenanceID, &p.RunID, &p.TaskID, &p.ActionID, &p.HostID, &p.LeaseID, &p.OriginalRequestsJSON); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (t *Tx) InsertMaintenanceRunProtection(ctx context.Context, p MaintenanceRunProtection, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO maintenance_protected_runs(maintenance_id,run_id,task_id,action_id,host_id,lease_id,original_requests_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, p.MaintenanceID, p.RunID, p.TaskID, p.ActionID, p.HostID, p.LeaseID, p.OriginalRequestsJSON, now)
	return err
}
