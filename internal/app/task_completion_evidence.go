package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// completedTaskEvidence names the actual basis of completion. Waiving a
// separate verdict cannot manufacture one or verify a provider/deployment.
func completedTaskEvidence(ctx context.Context, q store.Querier, taskID, summary string) (protocol.IssueEvidence, error) {
	return store.CompletedTaskEvidence(ctx, q, taskID, summary)
}
