package traex

import "context"

// Hosted coordination preserves the selected runtime's native tools, MCP and
// device policy. Responsibility labels do not install a separate deny hook.
// Business tools retain their authenticated object and lease boundaries.
func startHostedCoordinator(ctx context.Context, opts Options) (*Session, error) {
	return startProcess(ctx, opts, nil)
}
