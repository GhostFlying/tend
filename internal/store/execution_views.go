package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// WorkspaceOpenRunTask identifies an unresolved run holding a resource.
func WorkspaceOpenRunTask(ctx context.Context, q Querier, resourceID string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT r.task_id FROM runs r JOIN workspace_runs w ON w.run_id=r.run_id WHERE w.workspace_id=? AND r.observed_state IN `+openRunStates+` ORDER BY r.created_at,r.run_id LIMIT 1`, resourceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read workspace open run task: %w", err)
	}
	return id, nil
}

// FirstIncompleteDependency supplies a navigable task for a dependency wait.
func FirstIncompleteDependency(ctx context.Context, q Querier, taskID string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT d.depends_on FROM task_dependencies d JOIN tasks t ON t.task_id=d.depends_on WHERE d.task_id=? AND t.lifecycle<>'completed' ORDER BY d.depends_on LIMIT 1`, taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read incomplete dependency: %w", err)
	}
	return id, nil
}
