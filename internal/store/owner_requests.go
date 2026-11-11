package store

import (
	"context"
)

// HasOwnerRequestAuthority recognizes the actual actor of an immutable Issue
// definition. It never grants authority based on a generic transport receipt.
func HasOwnerRequestAuthority(ctx context.Context, q Querier, id string) (bool, error) {
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_control_revisions WHERE request_id=? AND authorizing_owner<>'')`, id).Scan(&allowed)
	return allowed, err
}
