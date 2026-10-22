package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// TaskFilter selects tasks for the owner's paginated task board.
type TaskFilter struct {
	Project    string
	Lifecycles []protocol.TaskLifecycle
}

// ListTaskPage lists every lifecycle, newest update first. Filters apply
// before the cursor and limit; a zero beforeUpdated starts at the newest.
func ListTaskPage(ctx context.Context, q Querier, filter TaskFilter, beforeUpdated int64, beforeID string, limit int) ([]TaskRow, error) {
	query := `SELECT task_id FROM tasks WHERE (? = '' OR project = ?)
		AND (? = 0 OR updated_at < ? OR (updated_at = ? AND task_id < ?))`
	args := []any{filter.Project, filter.Project, beforeUpdated, beforeUpdated, beforeUpdated, beforeID}
	if len(filter.Lifecycles) != 0 {
		query += " AND lifecycle IN (" + strings.TrimSuffix(strings.Repeat("?,", len(filter.Lifecycles)), ",") + ")"
		for _, state := range filter.Lifecycles {
			args = append(args, state)
		}
	}
	query += " ORDER BY updated_at DESC, task_id DESC LIMIT ?"
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
