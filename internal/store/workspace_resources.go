package store

import (
	"context"
	"database/sql"
	"fmt"
)

// WorkspaceRow combines a resource with the requesting task's binding.
type WorkspaceRow struct {
	WorkspaceID        string
	TaskID             string
	CreatorTaskID      string
	OwnerWorkItemID    string
	Name               string
	HostID             string
	Status             string
	Kind               string
	Mode               string
	SourceTaskID       string
	OccupiedBy         string
	ProviderID         string
	ProviderInstanceID string
	Path               string
	ReleasedAt         int64
	CleanupID          string
}

const (
	WorkspaceAssigned = "assigned"
	WorkspaceReady    = "ready"
	WorkspaceRetained = "retained"
	WorkspaceRemoving = "removing"
	WorkspaceRemoved  = "removed"
)

const resourceColumns = `w.workspace_id,w.task_id,w.name,w.host_id,w.status,w.kind,
	COALESCE(w.occupied_by,''),w.provider_id,w.provider_instance_id,w.path,
	COALESCE(w.released_at,0),COALESCE(w.cleanup_id,''),COALESCE(w.owner_work_item_id,'')`

func scanWorkspace(s interface{ Scan(...any) error }) (WorkspaceRow, error) {
	var w WorkspaceRow
	err := s.Scan(&w.WorkspaceID, &w.CreatorTaskID, &w.Name, &w.HostID, &w.Status, &w.Kind,
		&w.OccupiedBy, &w.ProviderID, &w.ProviderInstanceID, &w.Path, &w.ReleasedAt, &w.CleanupID, &w.OwnerWorkItemID)
	return w, err
}

// GetWorkspace returns a stable resource, independently of any task binding.
func GetWorkspace(ctx context.Context, q Querier, id string) (WorkspaceRow, error) {
	w, err := scanWorkspace(q.QueryRowContext(ctx, `SELECT `+resourceColumns+` FROM workspaces w WHERE w.workspace_id=?`, id))
	if err != nil {
		return WorkspaceRow{}, notFound(err, "workspace "+id)
	}
	return w, nil
}

// GetWorkspaceByTask returns the resource pinned by the task's binding.
func GetWorkspaceByTask(ctx context.Context, q Querier, taskID string) (WorkspaceRow, error) {
	var id, mode string
	var source sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT workspace_id,mode,source_task_id FROM task_workspace_bindings WHERE task_id=?`, taskID).Scan(&id, &mode, &source); err != nil {
		return WorkspaceRow{}, notFound(err, "workspace of task "+taskID)
	}
	w, err := GetWorkspace(ctx, q, id)
	w.TaskID, w.Mode, w.SourceTaskID = taskID, mode, source.String
	return w, err
}

func (t *Tx) BindWorkspace(ctx context.Context, taskID, id, mode, source string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO task_workspace_bindings(task_id,workspace_id,mode,source_task_id,created_at) VALUES(?,?,?,?,?)`, taskID, id, mode, nullString(source), now)
	if err != nil {
		return fmt.Errorf("store: bind workspace: %w", err)
	}
	return nil
}

// InsertWorkspace creates a resource and its initial task binding atomically.
func (t *Tx) InsertWorkspace(ctx context.Context, w WorkspaceRow, now int64) error {
	if w.Kind == "" {
		w.Kind = "git"
	}
	if w.Mode == "" {
		w.Mode = "new"
	}
	owner, err := GetTaskWorkItemOwner(ctx, t.Q(), w.TaskID)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO workspaces(workspace_id,task_id,name,host_id,status,kind,occupied_by,created_at,updated_at,owner_work_item_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, w.WorkspaceID, w.TaskID, w.Name, w.HostID, w.Status, w.Kind, w.TaskID, now, now, nullString(owner))
	if err != nil {
		return fmt.Errorf("store: insert workspace: %w", err)
	}
	return t.BindWorkspace(ctx, w.TaskID, w.WorkspaceID, w.Mode, "", now)
}

// AcquireWorkspace retains task ownership across all implementation and acceptance runs.
func (t *Tx) AcquireWorkspace(ctx context.Context, id, taskID string, now int64) error {
	r, err := t.Exec(ctx, `UPDATE workspaces SET occupied_by=?,released_at=NULL,updated_at=? WHERE workspace_id=? AND status NOT IN ('removing','removed') AND (occupied_by IS NULL OR occupied_by=?)`, taskID, now, id, taskID)
	if err != nil {
		return fmt.Errorf("store: acquire workspace: %w", err)
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return fmt.Errorf("store: workspace is occupied or unavailable: %w", ErrNotFound)
	}
	return nil
}

// AbandonWorkspace detaches this task without deleting a shared physical resource.
func (t *Tx) AbandonWorkspace(ctx context.Context, taskID string, now int64) (WorkspaceRow, error) {
	w, err := GetWorkspaceByTask(ctx, t.Q(), taskID)
	if err != nil {
		return WorkspaceRow{}, err
	}
	_, err = t.Exec(ctx, `INSERT INTO abandoned_workspaces(workspace_id,task_id,name,host_id,mounts_json,created_at,abandoned_at)
	 SELECT w.workspace_id,?,w.name,w.host_id,(SELECT json_group_array(json_object('path',m.path,'repo',m.repo,'branch',m.branch,'resolved_base_sha',m.resolved_base_sha)) FROM workspace_mounts m WHERE m.workspace_id=w.workspace_id),w.created_at,?
	 FROM workspaces w WHERE workspace_id=? ON CONFLICT(workspace_id) DO NOTHING`, taskID, now, w.WorkspaceID)
	if err != nil {
		return WorkspaceRow{}, fmt.Errorf("store: keep abandoned workspace: %w", err)
	}
	if _, err = t.Exec(ctx, `DELETE FROM task_workspace_bindings WHERE task_id=?`, taskID); err != nil {
		return WorkspaceRow{}, fmt.Errorf("store: detach workspace: %w", err)
	}
	return w, nil
}

// RecordWorkspaceLocation accepts only the first provider identity and path.
func (t *Tx) RecordWorkspaceLocation(ctx context.Context, id, provider, instance, path string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE workspaces SET provider_id=?,provider_instance_id=?,path=?,updated_at=? WHERE workspace_id=?`, provider, instance, path, now, id)
	if err != nil {
		return fmt.Errorf("store: record workspace location: %w", err)
	}
	return nil
}

func (t *Tx) RecordWorkspaceBaseline(ctx context.Context, id, taskID, body string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO workspace_baselines(workspace_id,task_id,body_json,created_at) VALUES(?,?,?,?) ON CONFLICT(workspace_id,task_id) DO NOTHING`, id, taskID, body, now)
	if err != nil {
		return fmt.Errorf("store: record workspace baseline: %w", err)
	}
	return nil
}

func WorkspaceBaseline(ctx context.Context, q Querier, id, taskID string) (string, error) {
	var body string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM workspace_baselines WHERE workspace_id=? AND task_id=?`, id, taskID).Scan(&body)
	if err != nil {
		return "", notFound(err, "workspace baseline")
	}
	return body, nil
}
