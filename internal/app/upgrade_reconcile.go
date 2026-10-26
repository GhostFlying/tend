package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A paused batch may reconcile its pinned candidates, but never issue another
// installation or advance to a different host. The original deadlines remain
// unchanged; the owner can retry or cancel after the outcome becomes known.
func reconcilePausedCandidates(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, o ServerObservation) error {
	if r.View.Phase == "server_ready" || r.View.Phase == "server_checking" {
		_, err := reconcileServerCandidate(ctx, x, tx, r, o)
		return err
	}
	if r.View.Phase != "runners" {
		return nil
	}
	for i, h := range r.Hosts {
		if r.View.Hosts[i].Status == "completed" {
			if err := reconcileCompletedRunner(ctx, x, tx, r, i); err != nil {
				return err
			}
			continue
		}
		if h.CandidateInstance == "" || h.Rollback != nil || r.View.Hosts[i].Status == "skipped" {
			continue
		}
		if _, _, err := reconcileRunnerCandidate(ctx, x, tx, r, i); err != nil {
			return err
		}
	}
	return nil
}

func reconcileServerCandidate(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, o ServerObservation) (string, error) {
	if r.ServerProcess == "" || o.ProcessInstanceID != r.ServerProcess || o.BinarySHA256 != r.ServerHash || !o.Healthy || !o.MainDrained || (o.InstanceID != "" && o.InstanceID != r.ServerInstance) {
		r.ServerHealthy = 0
		return "", nil
	}
	matches, err := installedServerMatches(ctx, tx.Q(), r.Manifest)
	if err != nil {
		return "", err
	}
	if !matches {
		return "候选 server 的协议或数据库版本与发布清单不一致", nil
	}
	now := x.Now.UnixMilli()
	r.View.Phase = "server_checking"
	if now-r.ServerHealthAt >= 5000 {
		r.ServerHealthy++
		r.ServerHealthAt = now
	}
	if r.ServerHealthy < 2 {
		return "", nil
	}
	for _, h := range r.Hosts {
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, now)
		if err != nil {
			return "", err
		}
		ctl.Revision++
		ctl.Mode = protocol.RunnerNormal
		ctl.Authorization = nil
		ctl.Upgrade = nil
		ctl.UpgradeID = ""
		ctl.ReleaseID = ""
		if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
			return "", err
		}
	}
	r.View.Phase = "runners"
	if r.View.Status == "running" {
		r.Deadline = now + r.DrainMillis
	}
	return "", nil
}

func reconcileRunnerCandidate(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, i int) (bool, string, error) {
	h, v := &r.Hosts[i], &r.View.Hosts[i]
	if h.CandidateInstance == "" || h.Rollback != nil {
		return false, "", nil
	}
	now := x.Now.UnixMilli()
	rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
	if err != nil {
		return false, "", err
	}
	ctl, err := runnerControl(ctx, tx.Q(), h.HostID, now)
	if err != nil {
		return false, "", err
	}
	if ctl.UpgradeID != r.View.UpgradeID || rt.Runtime.UpgradeID != r.View.UpgradeID || rt.Runtime.Build.BinarySHA256 != h.TargetHash || !runnerCandidateMatches(*r, *h, rt.Runtime) {
		return false, "", nil
	}
	if !installedRunnerMatches(rt.Runtime, r.Manifest) {
		return false, "候选 runner 的协议或 journal 版本与发布清单不一致", nil
	}
	if h.HealthInstance != rt.Runtime.InstanceID {
		h.HealthInstance = rt.Runtime.InstanceID
		h.Healthy, h.LastSequence, h.LastHealthyAt = 0, 0, 0
		if h.Committed != nil {
			v.Phase = "checking"
		}
	}
	fresh := rt.Online && now-rt.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds() && rt.Runtime.AppliedControlRevision == ctl.Revision
	if v.Phase != "restored" {
		runs, err := store.CountOpenRuns(ctx, tx.Q(), h.HostID)
		if err != nil {
			return false, "", err
		}
		v.Phase = "checking"
		admitted := h.Committed != nil && ctl.Mode == protocol.RunnerNormal && rt.Runtime.Mode == protocol.RunnerNormal
		healthy := runnerConfigurationMatches(*h, rt.Runtime) && fresh && (admitted || ctl.Mode == protocol.RunnerMaintenance && rt.Runtime.Mode == protocol.RunnerReady && runs == 0 && rt.Runtime.PendingClaims == 0 && rt.Runtime.PendingOperations == 0 && len(rt.Runtime.Blockers) == 0)
		if healthy && rt.Runtime.Sequence > h.LastSequence && rt.LastHeartbeatAt-h.LastHealthyAt >= 7500 {
			h.Healthy++
			h.LastSequence = rt.Runtime.Sequence
			h.LastHealthyAt = rt.LastHeartbeatAt
		}
		if !healthy {
			h.Healthy = 0
		}
		if healthy && h.Healthy >= 2 && committedMatches(*r, *h, rt.Runtime.UpdaterReceipt, rt.Runtime) {
			proof := *rt.Runtime.UpdaterReceipt
			h.Committed = &proof
			if ctl.Mode != protocol.RunnerNormal {
				ctl.Revision++
			}
			ctl.Mode = protocol.RunnerNormal
			if h.Configuration == nil {
				ctl.Upgrade = nil
			}
			ctl.Authorization = nil
			// Retain the batch identity until normal mode is acknowledged.
			if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
				return false, "", err
			}
			v.Phase = "restored"
			return true, "", nil
		}
	}
	if v.Phase == "restored" && committedMatches(*r, *h, h.Committed, rt.Runtime) && fresh && ctl.Mode == protocol.RunnerNormal && rt.Runtime.Mode == protocol.RunnerNormal {
		v.Status = "completed"
		v.CurrentVersion = rt.Runtime.Build.Version
		v.ProtocolVersion = rt.Runtime.Protocol
		if r.View.Status == "running" {
			r.Deadline = now + r.DrainMillis
		}
		ctl.Revision++
		ctl.Upgrade = nil
		ctl.UpgradeID = ""
		ctl.ReleaseID = ""
		if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
			return false, "", err
		}
		return true, "", upgradeNotice(ctx, x, tx, *r, "upgrade_host_restored", h.HostID, fmt.Sprintf("执行主机 `%s` 已升级并恢复接收任务。", h.HostID))
	}
	return true, "", nil
}
