package app

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func checkRunMessageRecovery(ctx context.Context, q store.Querier, host string, runtime *protocol.RunnerRuntime) error {
	if runtime != nil && slices.Contains(runtime.Capabilities, protocol.CapabilityRunMessages) {
		return nil
	}
	var pending int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_messages m JOIN runs r ON r.run_id=m.run_id
	 WHERE r.host_id=? AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')
	 AND m.status IN ('queued','dispatching','unknown')`, host).Scan(&pending)
	if err != nil {
		return err
	}
	if pending > 0 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner requires run-messages-v1 to recover unresolved message deliveries")
	}
	return nil
}
