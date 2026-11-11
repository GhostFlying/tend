package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AssignedRun reads an actual committed assignment for a legacy transport call.
// The protocol adapter uses existing business events rather than storing aliases.
func (a *App) AssignedRun(ctx context.Context, host, correlation string) (string, error) {
	var id string
	var count int
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MIN(r.run_id),''),COUNT(DISTINCT r.run_id) FROM events e JOIN runs r ON r.run_id=e.object_id WHERE e.kind='run.assigned' AND e.object_type='run' AND e.request_id=? AND r.host_id=?`, correlation, host).Scan(&id, &count)
	})
	if err != nil {
		return "", err
	}
	if count > 1 {
		return "", protocol.Errorf(protocol.CodeUnknownOutcome, "original assignment is ambiguous; reconcile its actual Runs")
	}
	return id, nil
}
