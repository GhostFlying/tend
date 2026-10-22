package store

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// TaskRelationRow is a declared reference between tasks.
type TaskRelationRow struct {
	SourceTaskID string
	TargetTaskID string
	Kind         protocol.TaskRelationKind
}

// ListTaskRelations reads both declarations and backlinks without truncation.
func ListTaskRelations(ctx context.Context, q Querier, taskID string) ([]TaskRelationRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT source_task_id, target_task_id, kind FROM task_relations
 WHERE source_task_id = ? OR target_task_id = ? ORDER BY source_task_id, kind, target_task_id`, taskID, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list task relations: %w", err)
	}
	defer rows.Close()
	var out []TaskRelationRow
	for rows.Next() {
		var r TaskRelationRow
		if err := rows.Scan(&r.SourceTaskID, &r.TargetTaskID, &r.Kind); err != nil {
			return nil, fmt.Errorf("store: scan task relation: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceTaskRelations replaces only this task's outgoing declarations.
func (t *Tx) ReplaceTaskRelations(ctx context.Context, taskID string, refs []protocol.TaskRelation, now int64) error {
	if _, err := t.Exec(ctx, `DELETE FROM task_relations WHERE source_task_id = ?`, taskID); err != nil {
		return fmt.Errorf("store: clear task relations: %w", err)
	}
	for _, r := range refs {
		if _, err := t.Exec(ctx, `INSERT INTO task_relations (source_task_id, target_task_id, kind, created_at) VALUES (?, ?, ?, ?)`, taskID, r.TaskID, r.Kind, now); err != nil {
			return fmt.Errorf("store: insert task relation: %w", err)
		}
	}
	return nil
}

// FollowsUp reports whether from reaches target along follow-up references.
func FollowsUp(ctx context.Context, q Querier, from, target string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `WITH RECURSIVE ancestors(task_id) AS (
 SELECT ? UNION SELECT r.target_task_id FROM task_relations r JOIN ancestors a ON r.source_task_id = a.task_id WHERE r.kind = 'follow_up'
 ) SELECT EXISTS(SELECT 1 FROM ancestors WHERE task_id = ?)`, from, target).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("store: trace follow-up references: %w", err)
	}
	return found, nil
}
