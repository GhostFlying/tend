package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// initialOwnerHandoffBlockers shares the same original blocker predicate as
// materialized managers, scoped to the caller's current transaction and epochs.
func initialOwnerHandoffBlockers(ctx context.Context, q store.Querier, issues []protocol.Issue) (map[string][]protocol.IssueBlocker, error) {
	epochs := make(map[string]int64, len(issues))
	for _, issue := range issues {
		epochs[issue.IssueID] = issue.AssignmentEpoch + 1
	}
	return managerHandoffBlockersBatch(ctx, q, epochs)
}
