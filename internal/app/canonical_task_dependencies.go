package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Reuse the persisted primary and task prerequisites in the graph write txn.
func validateCanonicalTaskDependencies(ctx context.Context, q store.Querier) error {
	ids, err := store.FollowupIDs(ctx, q, `SELECT issue_id FROM issue_controls ORDER BY issue_id`)
	if err != nil || len(ids) == 0 {
		return err
	}
	issues := make([]protocol.Issue, 0, len(ids))
	primary := map[string][]string{}
	lives := map[string]protocol.TaskLifecycle{}
	for _, id := range ids {
		issue, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
		}
		issues = append(issues, issue)
		primary[id], err = store.IssueTaskIDs(ctx, q, id)
		if err != nil {
			return err
		}
		for _, task := range primary[id] {
			lives[task] = ""
		}
	}
	deps := map[string][]string{}
	rows, err := q.QueryContext(ctx, `SELECT task_id,depends_on FROM task_dependencies`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var task, dep string
		if err := rows.Scan(&task, &dep); err != nil {
			rows.Close()
			return err
		}
		deps[task] = append(deps[task], dep)
		lives[task], lives[dep] = "", ""
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id := range lives {
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return err
		}
		lives[id] = task.Lifecycle
	}
	return domain.ValidateIssuePrimaryTaskWaits(issues, primary, lives, deps)
}
