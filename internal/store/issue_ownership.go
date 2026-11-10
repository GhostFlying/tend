package store

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
)

func IssueUsesOwnerExecution(ctx context.Context, q Querier, issue string) (bool, error) {
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT CASE WHEN owner_selection_json<>'' THEN COALESCE(json_extract(owner_selection_json,'$.ownership_policy')=?,0) ELSE EXISTS(SELECT 1 FROM issue_ownership_policies WHERE issue_id=?) END FROM issue_controls WHERE issue_id=?),0)`, protocol.IssueOwnerPolicy, issue, issue).Scan(&enabled)
	return enabled, err
}
