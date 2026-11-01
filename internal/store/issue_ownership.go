package store

import "context"

func IssueUsesOwnerExecution(ctx context.Context, q Querier, issue string) (bool, error) {
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_ownership_policies WHERE issue_id=?)`, issue).Scan(&enabled)
	return enabled, err
}
