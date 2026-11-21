package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// warmCandidate finds the installation bound to this independent journal.
// A durable local commit can identify a restarted process of the same journal.
func warmCandidate(ctx context.Context, q store.Querier, host string, rt *protocol.RunnerRuntime) (*store.UpgradeRow, int, error) {
	if rt == nil || rt.UpgradeID == "" || rt.JournalID == "" {
		return nil, 0, nil
	}
	batch, err := store.GetUpgrade(ctx, q, rt.UpgradeID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if batch.View.Phase == "runners" && (batch.View.Status == "running" || batch.View.Status == "attention") {
		for i, h := range batch.Hosts {
			if h.HostID == host && h.WarmRevision != 0 && h.Committed == nil && h.Rollback == nil &&
				h.CandidateInstance == rt.JournalID && h.TargetHash == rt.Build.BinarySHA256 &&
				runnerCandidateMatches(batch, h, *rt) {
				return &batch, i, nil
			}
		}
	}
	return nil, 0, nil
}

func warmControl(batch store.UpgradeRow, host store.UpgradeHost) protocol.RunnerControl {
	// The candidate can inspect its supervisor's delivery receipt, but has
	// neither the predecessor's host control nor any host-wide work targets.
	return protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: host.WarmRevision,
		UpgradeID: batch.View.UpgradeID, ReleaseID: batch.View.ReleaseID, Upgrade: runnerUpgradeTarget(batch, host, false)}
}

func observeWarmCandidate(ctx context.Context, tx *store.Tx, host string, rt *protocol.RunnerRuntime, now int64, hello bool) (*protocol.RunnerControl, error) {
	batch, i, err := warmCandidate(ctx, tx.Q(), host, rt)
	if err != nil || batch == nil {
		return nil, err
	}
	h := &batch.Hosts[i]
	old, err := store.GetHostRuntime(ctx, tx.Q(), host)
	if err != nil {
		return nil, err
	}
	if err := checkRuntime(ctx, tx.Q(), host, rt, hello); err != nil {
		return nil, err
	}
	if old.Runtime.InstanceID != h.RollingFrom || rt.Mode == protocol.RunnerNormal || !batch.Manifest.Protocol.Contains(rt.Protocol) {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "rolling candidate or predecessor changed")
	}
	if previous := h.WarmRuntime; previous != nil {
		prior := previous.Runtime
		if prior.InstanceID != rt.InstanceID {
			if !hello || rt.Generation <= prior.Generation || !committedMatches(*batch, *h, rt.UpdaterReceipt, *rt) {
				return nil, protocol.Errorf(protocol.CodeConflictPayload, "rolling candidate incarnation changed")
			}
		} else if prior.Generation != rt.Generation || prior.Protocol != rt.Protocol || prior.Build.PID != rt.Build.PID {
			return nil, protocol.Errorf(protocol.CodeConflictPayload, "rolling candidate identity changed")
		} else if prior.Sequence >= rt.Sequence {
			return nil, &protocol.Error{Code: protocol.CodeConflictPayload, Message: "runner observation sequence is stale", Details: []byte(`{"reason":"stale_sequence"}`)}
		}
	} else if !hello || now >= h.GrantDeadline || batch.View.Status != "running" {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "rolling candidate launch was not confirmed within its grant")
	}
	h.WarmRuntime = &store.HostRuntimeRow{Runtime: *rt, Online: true, LastHeartbeatAt: now}
	if err := tx.PutUpgrade(ctx, *batch); err != nil {
		return nil, err
	}
	control := warmControl(*batch, *h)
	return &control, nil
}

func reconcileWarmCandidate(ctx context.Context, x *Exec, tx *store.Tx, batch *store.UpgradeRow, i int, old store.HostRuntimeRow, control protocol.RunnerControl) (bool, string, error) {
	h, view := &batch.Hosts[i], &batch.View.Hosts[i]
	if h.WarmRuntime == nil {
		return true, "", nil
	}
	rt := h.WarmRuntime.Runtime
	if control.Mode != protocol.RunnerNormal || !rollingTargetMatches(*batch, control, old.Runtime, h.RollingProcess) {
		return true, "原接单 runner 或主机控制已变化", nil
	}
	if !installedRunnerMatches(rt, batch.Manifest) {
		return true, "候选 runner 的协议或 journal 版本与发布清单不一致", nil
	}
	if !runnerSelfMatches(*batch, *h, rt) {
		return true, "", nil
	}
	view.Updater = rt.Updater
	view.Phase = "checking"
	if !warmCandidateReady(*h, x.Now.UnixMilli()) {
		h.Healthy = 0
		return true, "", nil
	}
	h.HealthInstance, h.LastSequence, h.LastHealthyAt, h.Healthy = rt.InstanceID, rt.Sequence, h.WarmRuntime.LastHeartbeatAt, 1
	if !committedMatches(*batch, *h, rt.UpdaterReceipt, rt) {
		return true, "", nil
	}

	control.Revision = max(control.Revision, h.WarmRevision) + 1
	drain := protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: control.Revision}
	if err := tx.PreserveDrainingRunner(ctx, h.HostID, batch.View.UpgradeID, old, drain); err != nil {
		return true, "", err
	}
	if err := tx.PutHostRuntime(ctx, h.HostID, *h.WarmRuntime); err != nil {
		return true, "", err
	}
	control.Upgrade = runnerUpgradeTarget(*batch, *h, false)
	control.Upgrade.Updater.Authorized = true
	control.Authorization = nil
	if err := tx.PutHostControl(ctx, h.HostID, control, x.Now.UnixMilli()); err != nil {
		return true, "", err
	}
	h.Committed = rt.UpdaterReceipt
	h.WarmRuntime = nil
	view.Phase = "restored"
	return true, "", nil
}

func warmCandidateReady(h store.UpgradeHost, now int64) bool {
	r := h.WarmRuntime
	if r == nil || !r.Online || now-r.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
		return false
	}
	rt := r.Runtime
	return rt.AppliedControlRevision == h.WarmRevision && rt.Mode == protocol.RunnerReady &&
		rt.PendingClaims == 0 && rt.PendingOperations == 0 && len(rt.Blockers) == 0
}
