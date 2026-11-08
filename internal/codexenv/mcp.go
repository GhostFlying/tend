package codexenv

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RecordMCP saves redacted native observations alongside the exact adopted
// generation. It does not change process state, adoption, or business receipts.
func (r *Reference) RecordMCP(ctx context.Context, statuses []protocol.ExtensionMCPStatus) error {
	if r == nil || r.Execution == "" {
		return nil
	}
	unlock, err := lock(ctx, r.Directory)
	if err != nil {
		return err
	}
	defer unlock()
	prior, ok, err := readAdoption(ctx, r.Directory, r.Execution)
	if err != nil {
		return err
	}
	if !ok || prior.Generation != r.Generation {
		return ErrConflict
	}
	prior.MCP = statuses
	return writeAdoption(ctx, r.Directory, prior)
}
