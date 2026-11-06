package updater

import "context"

// BoundedCommand executes the independent local installer surface. Read-only
// reconciliation never requires a responsive service or updater control socket.
func BoundedCommand(ctx context.Context, cfg Config, action, planFile, authorizationFile, requestID string, generation uint64, expectedPhase string) (any, error) {
	return boundedCommand(ctx, cfg, action, planFile, authorizationFile, requestID, generation, expectedPhase)
}
