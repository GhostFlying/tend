package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A restored host remains admitted while a later host upgrades. Follow its
// supervisor-confirmed restarts without draining ordinary work or re-emitting
// the restoration notification.
func reconcileCompletedRunner(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, i int) error {
	h := &r.Hosts[i]
	if h.Committed == nil {
		return nil
	}
	rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
	if err != nil {
		return err
	}
	ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
	if err != nil {
		return err
	}
	if !committedMatches(*r, *h, rt.Runtime.UpdaterReceipt, rt.Runtime) || !installedRunnerMatches(rt.Runtime, r.Manifest) ||
		ctl.Mode != protocol.RunnerNormal || rt.Runtime.Mode != protocol.RunnerNormal ||
		(ctl.UpgradeID != "" && ctl.UpgradeID != r.View.UpgradeID) || (rt.Runtime.UpgradeID != "" && rt.Runtime.UpgradeID != r.View.UpgradeID) {
		return nil
	}
	if h.HealthInstance == rt.Runtime.InstanceID && h.Healthy >= 2 && committedMatches(*r, *h, h.Committed, rt.Runtime) {
		return nil
	}
	if h.HealthInstance != rt.Runtime.InstanceID {
		h.HealthInstance = rt.Runtime.InstanceID
		h.Healthy, h.LastSequence, h.LastHealthyAt = 0, 0, 0
	}
	if !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || rt.Runtime.AppliedControlRevision != ctl.Revision {
		h.Healthy = 0
		return nil
	}
	if rt.Runtime.Sequence > h.LastSequence && rt.LastHeartbeatAt-h.LastHealthyAt >= 7500 {
		h.Healthy++
		h.LastSequence, h.LastHealthyAt = rt.Runtime.Sequence, rt.LastHeartbeatAt
	}
	if h.Healthy >= 2 {
		proof := *rt.Runtime.UpdaterReceipt
		h.Committed = &proof
	}
	return nil
}
