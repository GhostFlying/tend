package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// TaskFilter selects tasks for the owner's paginated task board.
type TaskFilter struct {
	Project        string
	Lifecycles     []protocol.TaskLifecycle
	CompletedOrder bool
}

// taskCompletedAtSQL is shared by detail projection and completion pagination.
// Missing historical evidence remains zero rather than borrowing updated_at.
const taskCompletedAtSQL = `CASE WHEN lifecycle = 'completed' THEN COALESCE((
 SELECT MAX(created_at) FROM events WHERE object_type = 'task'
 AND object_id = tasks.task_id AND kind = 'task.completed'), 0) ELSE 0 END`

// ListTaskPage lists tasks by update time, or by completion time when requested.
// Filters apply before the cursor and limit; an empty beforeID starts a page.
func ListTaskPage(ctx context.Context, q Querier, filter TaskFilter, beforeUpdated int64, beforeID string, limit int) ([]TaskRow, error) {
	order := "updated_at"
	if filter.CompletedOrder {
		order = taskCompletedAtSQL
	}
	query := `SELECT task_id FROM (SELECT tasks.*, ` + order + ` AS sort_at FROM tasks) WHERE (? = '' OR project = ?)
		AND (? = '' OR sort_at < ? OR (sort_at = ? AND task_id < ?))`
	args := []any{filter.Project, filter.Project, beforeID, beforeUpdated, beforeUpdated, beforeID}
	if len(filter.Lifecycles) != 0 {
		query += " AND lifecycle IN (" + strings.TrimSuffix(strings.Repeat("?,", len(filter.Lifecycles)), ",") + ")"
		for _, state := range filter.Lifecycles {
			args = append(args, state)
		}
	}
	query += " ORDER BY sort_at DESC, task_id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list task page: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan task page: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: iterate task page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("store: close task page: %w", err)
	}
	out := make([]TaskRow, 0, len(ids))
	for _, id := range ids {
		task, err := GetTask(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}
