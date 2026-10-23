package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// A hash alone cannot distinguish a completed rollback from an uncertain
// installation. Require the supervisor's terminal receipt and the authenticated
// restored process to agree with the batch's original candidate binding.
func rollbackMatches(r store.UpgradeRow, receipt *protocol.UpdaterReceipt, instance, candidate, target, previous, current, hash string) bool {
	return receipt != nil && receipt.Phase == "rolled_back" && receipt.RollbackConfirmed &&
		bindingMatches(r, receipt.Binding) && candidate != "" &&
		receipt.Binding.InstanceID == instance && receipt.Binding.ProcessInstanceID == candidate &&
		receipt.Binding.ArtifactSHA256 == target && release.ValidSHA(previous) &&
		receipt.CurrentSHA256 == previous && hash == previous && current != "" && current != candidate &&
		receipt.CurrentProcessInstanceID == current
}

// Reconcile receipts even after the batch timed out. Preserve the failed
// candidate identity as history; a new installation needs a new batch.
func reconcileUpgradeRollbacks(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, o ServerObservation) (bool, error) {
	changed := false
	if r.ServerRollback == nil && r.ServerHealthy == 0 && o.Healthy && o.MainDrained && o.InstanceID == r.ServerInstance &&
		rollbackMatches(*r, o.UpdaterReceipt, r.ServerInstance, r.ServerProcess, r.ServerHash, r.ServerPreviousHash, o.ProcessInstanceID, o.BinarySHA256) {
		r.ServerRollback = o.UpdaterReceipt
		changed = true
	}
	for i := range r.Hosts {
		h := &r.Hosts[i]
		if h.Rollback != nil || h.CandidateInstance == "" || h.Healthy >= 2 {
			continue
		}
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			return false, err
		}
		if !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() ||
			!rollbackMatches(*r, rt.Runtime.UpdaterReceipt, h.HostID, h.CandidateInstance, h.TargetHash, h.PreviousHash, rt.Runtime.InstanceID, rt.Runtime.Build.BinarySHA256) {
			continue
		}
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return false, err
		}
		runs, err := store.CountOpenRuns(ctx, tx.Q(), h.HostID)
		if err != nil {
			return false, err
		}
		if ctl.UpgradeID != r.View.UpgradeID || rt.Runtime.UpgradeID != r.View.UpgradeID ||
			ctl.Mode != protocol.RunnerMaintenance || rt.Runtime.Mode != protocol.RunnerReady ||
			rt.Runtime.AppliedControlRevision != ctl.Revision || runs != 0 ||
			rt.Runtime.PendingClaims != 0 || rt.Runtime.PendingOperations != 0 || len(rt.Runtime.Blockers) != 0 {
			continue
		}
		h.Rollback = rt.Runtime.UpdaterReceipt
		r.View.Hosts[i].Status = "attention"
		r.View.Hosts[i].Phase = "rolled_back"
		r.View.Hosts[i].Reason = "候选版本未启动，已确认恢复旧版本"
		ctl.Revision++
		ctl.Mode = protocol.RunnerNormal
		ctl.Upgrade = nil
		ctl.Authorization = nil
		ctl.UpgradeID = ""
		ctl.ReleaseID = ""
		if err := tx.PutHostControl(ctx, h.HostID, ctl, x.Now.UnixMilli()); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}
