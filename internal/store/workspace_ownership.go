package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetTaskIssueOwner returns the immutable primary work item, or an empty
// string for legacy tasks whose original spec supplies no attributable owner.
func GetTaskIssueOwner(ctx context.Context, q Querier, taskID string) (string, error) {
	var owner string
	err := q.QueryRowContext(ctx, `SELECT work_item_id FROM task_work_item_owners WHERE task_id=?`, taskID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read task work item owner: %w", err)
	}
	return owner, nil
}

// TaskIssueLabel returns the primary item's current title for task listings.
func TaskIssueLabel(ctx context.Context, q Querier, taskID string) (id, title string, err error) {
	err = q.QueryRowContext(ctx, `SELECT o.work_item_id, json_extract(w.body_json, '$.title') FROM task_work_item_owners o
 JOIN work_items w ON w.work_item_id=o.work_item_id WHERE o.task_id=?`, taskID).Scan(&id, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("store: read task work item label: %w", err)
	}
	return id, title, nil
}

// InsertTaskIssueOwner records creation provenance once, after spec v1 exists.
func (t *Tx) InsertTaskIssueOwner(ctx context.Context, taskID, itemID string) error {
	_, err := t.Exec(ctx, `INSERT INTO task_work_item_owners(task_id,work_item_id,source_spec_version) VALUES(?,?,1)`, taskID, itemID)
	if err != nil {
		return fmt.Errorf("store: insert task work item owner: %w", err)
	}
	return nil
}

// DetachUnusedWorkspace removes only an unexecuted reuse binding. It does not
// release or modify the resource, which may still be occupied by another task.
func (t *Tx) DetachUnusedWorkspace(ctx context.Context, taskID, workspaceID string) error {
	result, err := t.Exec(ctx, `DELETE FROM task_workspace_bindings
 WHERE task_id=? AND workspace_id=? AND mode='reuse'
 AND NOT EXISTS(SELECT 1 FROM runs WHERE task_id=?)
 AND NOT EXISTS(SELECT 1 FROM workspaces WHERE occupied_by=?)
 AND NOT EXISTS(SELECT 1 FROM plan_actions WHERE task_id=? AND status='dispatched')`, taskID, workspaceID, taskID, taskID, taskID)
	if err != nil {
		return fmt.Errorf("store: detach unused workspace: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("store: workspace binding is not unused: %w", ErrNotFound)
	}
	return nil
}
