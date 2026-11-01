package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// NativeExecutionControl grants no new work: it observes the existing run lease.
func (a *App) NativeExecutionControl(ctx context.Context, p protocol.Principal) (protocol.RunDesired, error) {
	var desired protocol.RunDesired
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		if p.Kind != protocol.PrincipalRun || (p.Role != protocol.RoleImplement && p.Role != protocol.RoleAccept) {
			return protocol.Errorf(protocol.CodeForbidden, "native worker requires an execution run")
		}
		rc, err := loadRun(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		pin, err := store.GetRunExecution(ctx, tx.Q(), p.Subject)
		if err != nil {
			return protocol.Errorf(protocol.CodeForbidden, "native worker requires a frozen central runtime")
		}
		inventory, err := store.GetAgentInventorySnapshot(ctx, tx.Q(), rc.run.HostID, pin.SnapshotID, pin.PublicSHA256)
		if err != nil {
			return err
		}
		central := false
		for _, runtime := range inventory.Projection.Runtimes {
			if runtime.RuntimeID == pin.Runtime.RuntimeID && runtime.Revision == pin.Runtime.Revision && runtime.Kind == "central-traex-worker" {
				central = true
			}
		}
		if !central {
			return protocol.Errorf(protocol.CodeForbidden, "run is not bound to a central runtime")
		}
		desired = desiredFor(rc.run, rc.action, rc.task)
		if lease.Status != store.LeaseActive || lease.ExpiresAt <= a.now().UnixMilli() || p.Role != rc.run.Role || desired == protocol.DesiredStopped {
			return protocol.Errorf(protocol.CodeForbidden, "native worker lease is not active")
		}
		return nil
	})
	return desired, err
}
