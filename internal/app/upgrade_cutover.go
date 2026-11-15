package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Only the cohort acknowledged before a real server stop can acquire this wait.
// Legacy batches have no cohort and retain the ordinary freshness policy.
func markCutoverRecovery(ctx context.Context, q store.Querier, r *store.UpgradeRow, host string, ctl protocol.RunnerControl, now int64) (string, error) {
	if r.ServerPreviousHash == "" || r.ServerPreviousHash == r.ServerHash {
		return "", nil
	}
	for i := range r.CutoverRunners {
		c := &r.CutoverRunners[i]
		if c.HostID != host {
			continue
		}
		rt, err := store.GetHostRuntime(ctx, q, host)
		if err != nil {
			return "", err
		}
		if c.NormalRevision != 0 || ctl.Mode != protocol.RunnerMaintenance || ctl.UpgradeID != r.View.UpgradeID || ctl.Revision != c.MaintenanceRevision || rt.Runtime.InstanceID != c.InstanceID || rt.Runtime.Generation != c.Generation || rt.Runtime.UpgradeID != r.View.UpgradeID || rt.Runtime.AppliedControlRevision != c.MaintenanceRevision {
			return "server 重启后主机维护身份或控制代次已改变", nil
		}
		c.NormalRevision = ctl.Revision + 1
		// An outage that occurs after recovery is not a cutover observation gap.
		c.Pending = rt.Runtime.Mode == protocol.RunnerMaintenance && now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds()
		return "", nil
	}
	return "", nil
}

// Waiting preserves unavailable observations and never authorizes drain or
// installation. TickUpgrade enforces the original stage and overall deadlines.
func awaitCutoverRunners(ctx context.Context, x *Exec, q store.Querier, r *store.UpgradeRow, now int64) (bool, string, error) {
	waiting := false
	for i := range r.CutoverRunners {
		c := &r.CutoverRunners[i]
		if !c.Pending {
			continue
		}
		if r.ServerProcess == "" || r.ServerHealthy < 2 || r.ServerPreviousHash == "" || r.ServerPreviousHash == r.ServerHash || c.NormalRevision != c.MaintenanceRevision+1 || r.Deadline <= now || upgradeBatchExpired(*r, now) {
			return false, "server 重启后的主机恢复观察窗口无效或已到期", nil
		}
		hosts, err := store.ListActiveHosts(ctx, q)
		if err != nil {
			return false, "", err
		}
		active := false
		for _, h := range hosts {
			if h.HostID == c.HostID && h.Slots > 0 {
				active = true
				break
			}
		}
		if !active {
			return false, "等待恢复的原主机已不可用", nil
		}
		rt, err := store.GetHostRuntime(ctx, q, c.HostID)
		if errors.Is(err, store.ErrNotFound) {
			return false, "等待恢复的原主机观测缺失", nil
		}
		if err != nil {
			return false, "", err
		}
		ctl, err := runnerControl(ctx, q, c.HostID, now)
		if err != nil {
			return false, "", err
		}
		if rt.Runtime.InstanceID != c.InstanceID || rt.Runtime.Generation != c.Generation || ctl.Revision != c.NormalRevision || ctl.Mode != protocol.RunnerNormal || ctl.UpgradeID != "" || ctl.Upgrade != nil || ctl.Authorization != nil {
			return false, "等待恢复的原主机身份或 normal 控制已改变", nil
		}
		fresh := rt.Online && now >= rt.LastHeartbeatAt && now-rt.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds()
		if fresh && rt.Runtime.Mode == protocol.RunnerNormal && rt.Runtime.AppliedControlRevision == c.NormalRevision && rt.Runtime.UpgradeID == "" {
			c.Pending = false
			continue
		}
		if rt.Runtime.UpgradeID != r.View.UpgradeID || rt.Runtime.Mode != protocol.RunnerMaintenance || rt.Runtime.AppliedControlRevision != c.MaintenanceRevision {
			return false, "原主机尚未确认预期的 normal 控制", nil
		}
		x.observeDecision(ctx, slog.LevelDebug, "upgrade waiting for cutover runner acknowledgment",
			"upgrade_id", r.View.UpgradeID, "host_id", c.HostID, "instance_id", c.InstanceID, "runner_generation", c.Generation,
			"runtime_mode", rt.Runtime.Mode, "online", rt.Online, "heartbeat_age_ms", now-rt.LastHeartbeatAt,
			"applied_control_revision", rt.Runtime.AppliedControlRevision, "expected_normal_revision", c.NormalRevision,
			"stage_deadline_ms", r.Deadline, "overall_deadline_ms", r.View.OverallDeadlineMillis)
		waiting = true
	}
	return waiting, "", nil
}
