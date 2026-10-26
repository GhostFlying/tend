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

// Cancelled receipts prove the supervisor fenced a staged operation before
// replacement began. They are not a database rollback.
func cancelledMatches(r store.UpgradeRow, receipt *protocol.UpdaterReceipt, instance, candidate, target, previous, current, hash string) bool {
	return receipt != nil && receipt.Phase == "cancelled" && !receipt.RollbackConfirmed && !receipt.CommittedConfirmed &&
		bindingMatches(r, receipt.Binding) && candidate != "" && receipt.Binding.InstanceID == instance &&
		receipt.Binding.ProcessInstanceID == candidate && receipt.Binding.ArtifactSHA256 == target &&
		release.ValidSHA(previous) && receipt.CurrentSHA256 == previous && hash == previous &&
		current != "" && current != candidate && receipt.CurrentProcessInstanceID == current
}

func restorationReason(r store.UpgradeRow) string {
	if r.ServerRollback != nil && r.ServerRollback.Phase == "cancelled" {
		return "已确认取消尚未执行的候选替换；取消本批次后可重新升级"
	}
	for _, h := range r.Hosts {
		if h.Rollback != nil && h.Rollback.Phase == "cancelled" {
			return "已确认取消尚未执行的候选替换；取消本批次后可重新升级"
		}
	}
	return "候选升级已回退，已确认恢复旧版本；取消本批次后可重新升级"
}

// Publish cancellation intent without lifting maintenance. Only a terminal
// receipt may establish that a granted replacement did not start.
func cancelUncommittedRunners(ctx context.Context, x *Exec, tx *store.Tx, r store.UpgradeRow) error {
	for _, h := range r.Hosts {
		if h.CandidateInstance == "" || h.Committed != nil || h.Rollback != nil {
			continue
		}
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return err
		}
		if ctl.UpgradeID != r.View.UpgradeID || ctl.Mode != protocol.RunnerMaintenance || ctl.CancelledUpgradeID == r.View.UpgradeID {
			continue
		}
		ctl.Revision++
		ctl.CancelledUpgradeID = r.View.UpgradeID
		if err := tx.PutHostControl(ctx, h.HostID, ctl, x.Now.UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile receipts even after the batch timed out. Preserve the failed
// candidate identity as history; a new installation needs a new batch.
func reconcileUpgradeRollbacks(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, o ServerObservation) (bool, error) {
	changed := false
	serverRolledBack := r.ServerHealthy == 0 && rollbackMatches(*r, o.UpdaterReceipt, r.ServerInstance, r.ServerProcess, r.ServerHash, r.ServerPreviousHash, o.ProcessInstanceID, o.BinarySHA256)
	serverCancelled := r.View.Status == "attention" && r.ServerGrantDeadline > 0 && x.Now.UnixMilli() >= r.ServerGrantDeadline && cancelledMatches(*r, o.UpdaterReceipt, r.ServerInstance, r.ServerProcess, r.ServerHash, r.ServerPreviousHash, o.ProcessInstanceID, o.BinarySHA256)
	if r.ServerRollback == nil && o.Healthy && o.MainDrained && o.InstanceID == r.ServerInstance && (serverRolledBack || serverCancelled) {
		r.ServerRollback = o.UpdaterReceipt
		changed = true
	}
	for i := range r.Hosts {
		h := &r.Hosts[i]
		if h.Rollback != nil || h.CandidateInstance == "" || h.Committed != nil {
			continue
		}
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			return false, err
		}
		if !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() ||
			!(rollbackMatches(*r, rt.Runtime.UpdaterReceipt, h.HostID, h.CandidateInstance, h.TargetHash, h.PreviousHash, rt.Runtime.InstanceID, rt.Runtime.Build.BinarySHA256) || r.View.Status == "attention" && cancelledMatches(*r, rt.Runtime.UpdaterReceipt, h.HostID, h.CandidateInstance, h.TargetHash, h.PreviousHash, rt.Runtime.InstanceID, rt.Runtime.Build.BinarySHA256)) {
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
		if h.Configuration != nil && rt.Runtime.ConfigurationSHA256 != h.Configuration.PreviousSHA256 {
			continue
		}
		h.Rollback = rt.Runtime.UpdaterReceipt
		r.View.Hosts[i].Status = "attention"
		r.View.Hosts[i].Phase = "rolled_back"
		r.View.Hosts[i].Reason = "候选升级已回退，已确认恢复旧版本"
		if h.Rollback.Phase == "cancelled" {
			r.View.Hosts[i].Phase = "cancelled"
			r.View.Hosts[i].Reason = "已取消尚未执行的候选替换，继续使用旧版本"
		}
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
