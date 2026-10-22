package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// HistoricalRun includes completed runs, whose external terminal may outlive them.
func (j *Journal) HistoricalRun(ctx context.Context, id string) (Run, error) {
	var r Run
	var body string
	err := j.db.QueryRowContext(ctx, `SELECT assignment_json,phase,fenced,attempts,COALESCE(thread_id,''),held,stopped_confirmed,updated_at,worker_kind FROM runs WHERE run_id=?`, id).Scan(&body, &r.Phase, &r.Fenced, &r.Attempts, &r.Thread, &r.Held, &r.StoppedConfirmed, &r.UpdatedAtMillis, &r.WorkerKind)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(body), &r.Assignment)
	return r, err
}

func (j *Journal) ConfirmStopped(ctx context.Context, id string) error {
	return j.exec(ctx, `UPDATE runs SET stopped_confirmed=1 WHERE run_id=?`, id)
}

func (j *Journal) QueueWorkspaceRelease(ctx context.Context, p protocol.WorkspaceRelease) error {
	_, err := j.db.ExecContext(ctx, `INSERT OR IGNORE INTO workspace_releases(workspace_id,task_id) VALUES(?,?)`, p.WorkspaceID, p.TaskID)
	return err
}

func (j *Journal) WorkspaceReleases(ctx context.Context) ([]protocol.WorkspaceRelease, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT workspace_id,task_id FROM workspace_releases ORDER BY workspace_id,task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.WorkspaceRelease
	for rows.Next() {
		var p protocol.WorkspaceRelease
		if err := rows.Scan(&p.WorkspaceID, &p.TaskID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (j *Journal) ForgetWorkspaceRelease(ctx context.Context, p protocol.WorkspaceRelease) error {
	_, err := j.db.ExecContext(ctx, `DELETE FROM workspace_releases WHERE workspace_id=? AND task_id=?`, p.WorkspaceID, p.TaskID)
	return err
}
