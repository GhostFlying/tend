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
		if c.NormalRevision != 0 || ctl.Mode != protocol.RunnerMaintenance || ctl.UpgradeID != r.View.UpgradeID || ctl.Revision != c.MaintenanceRevision || rt.Runtime.InstanceID != c.InstanceID || rt.Runtime.Generation != c.Generation || rt.Runtime.UpgradeID != r.View.UpgradeID || (rt.Runtime.Mode != protocol.RunnerMaintenance && rt.Runtime.Mode != protocol.RunnerReady) || rt.Runtime.AppliedControlRevision != c.MaintenanceRevision {
			return "server 重启后主机维护身份或控制代次已改变", nil
		}
		c.NormalRevision = ctl.Revision + 1
		// The same pre-stop ACK can become stale before normal is observed.
		// Keep the exact cohort pending until the actual normal ACK arrives.
		c.Pending = true
		return "", nil
	}
	return "", nil
}

// Waiting preserves unavailable observations and never authorizes drain or
// installation. TickUpgrade enforces the original stage and overall deadlines.
func awaitCutoverRunners(ctx context.Context, x *Exec, q store.Querier, r *store.UpgradeRow, now int64) (bool, string, error) {
	hasPending := false
	for _, c := range r.CutoverRunners {
		hasPending = hasPending || c.Pending
	}
	if !hasPending {
		return false, "", nil
	}
	waiting := false
	for i := range r.CutoverRunners {
		c := &r.CutoverRunners[i]
		facts := []any{
			"upgrade_id", r.View.UpgradeID, "upgrade_revision", r.View.Revision, "upgrade_phase", r.View.Phase,
			"host_id", c.HostID, "expected_instance_id", c.InstanceID, "expected_generation", c.Generation,
			"maintenance_revision", c.MaintenanceRevision, "expected_normal_revision", c.NormalRevision,
			"server_process_instance_id", r.ServerProcess, "server_healthy", r.ServerHealthy,
			"server_previous_hash", r.ServerPreviousHash, "server_hash", r.ServerHash,
			"stage_deadline_ms", r.Deadline, "stage_remaining_ms", max(0, r.Deadline-now),
			"overall_deadline_ms", r.View.OverallDeadlineMillis, "observed_at_ms", now,
		}
		observe := func(level slog.Level, decision, predicate string) {
			// Keep the values read at this decision, not pointers to mutable
			// rows or later restored state. Execution emits after the write.
			attrs := append(append([]any{}, facts...), "decision", decision)
			if predicate != "" {
				attrs = append(attrs, "failed_predicate", predicate)
			}
			x.observeDecision(ctx, level, "upgrade cutover runner decision", attrs...)
		}
		reject := func(predicate, reason string) (bool, string, error) {
			observe(slog.LevelWarn, "rejected", predicate)
			return false, reason, nil
		}
		if r.ServerProcess == "" || r.ServerHealthy < 2 || r.ServerPreviousHash == "" || r.ServerPreviousHash == r.ServerHash || c.NormalRevision != c.MaintenanceRevision+1 || r.Deadline <= now || upgradeBatchExpired(*r, now) {
			return reject("cutover_window_invalid", "server 重启后的主机恢复观察窗口无效或已到期")
		}
		hosts, err := store.ListActiveHosts(ctx, q)
		if err != nil {
			return false, "", err
		}
		found, active := false, false
		for _, h := range hosts {
			if h.HostID == c.HostID {
				facts = append(facts, "host_slots", h.Slots)
				found, active = true, h.Slots > 0
				break
			}
		}
		if !found {
			return reject("host_missing", "等待恢复的原主机已不可用")
		}
		if !active {
			return reject("slots_not_positive", "等待恢复的原主机已不可用")
		}
		rt, err := store.GetHostRuntime(ctx, q, c.HostID)
		if errors.Is(err, store.ErrNotFound) {
			return reject("runtime_missing", "等待恢复的原主机观测缺失")
		}
		if err != nil {
			return false, "", err
		}
		ctl, err := runnerControl(ctx, q, c.HostID, now)
		if err != nil {
			return false, "", err
		}
		facts = append(facts,
			"online", rt.Online, "last_heartbeat_ms", rt.LastHeartbeatAt, "heartbeat_age_ms", now-rt.LastHeartbeatAt,
			"heartbeat_limit_ms", RunnerOfflineAfter.Milliseconds(), "instance_id", rt.Runtime.InstanceID,
			"generation", rt.Runtime.Generation, "runtime_mode", rt.Runtime.Mode, "runtime_upgrade_id", rt.Runtime.UpgradeID,
			"applied_control_revision", rt.Runtime.AppliedControlRevision,
			"control_revision", ctl.Revision, "control_mode", ctl.Mode, "control_upgrade_id", ctl.UpgradeID,
			"control_upgrade_present", ctl.Upgrade != nil, "control_authorization_present", ctl.Authorization != nil)
		if rt.Runtime.InstanceID != c.InstanceID || rt.Runtime.Generation != c.Generation || ctl.Revision != c.NormalRevision || ctl.Mode != protocol.RunnerNormal || ctl.UpgradeID != "" || ctl.Upgrade != nil || ctl.Authorization != nil {
			predicate := "normal_control_payload_changed"
			switch {
			case rt.Runtime.InstanceID != c.InstanceID:
				predicate = "instance_changed"
			case rt.Runtime.Generation != c.Generation:
				predicate = "generation_changed"
			case ctl.Revision != c.NormalRevision:
				predicate = "control_revision_changed"
			case ctl.Mode != protocol.RunnerNormal:
				predicate = "control_mode_not_normal"
			}
			return reject(predicate, "等待恢复的原主机身份或 normal 控制已改变")
		}
		fresh := rt.Online && now >= rt.LastHeartbeatAt && now-rt.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds()
		if fresh && rt.Runtime.Mode == protocol.RunnerNormal && rt.Runtime.AppliedControlRevision == c.NormalRevision && rt.Runtime.UpgradeID == "" {
			if c.Pending {
				observe(slog.LevelDebug, "normal_acknowledged", "")
			}
			c.Pending = false
			continue
		}
		// A host that already recovered cannot re-enter the cutover wait.
		if !c.Pending {
			return reject("recovered_host_unavailable", "已恢复的原主机不再确认 fresh normal 控制")
		}
		if rt.Runtime.UpgradeID != r.View.UpgradeID || (rt.Runtime.Mode != protocol.RunnerMaintenance && rt.Runtime.Mode != protocol.RunnerReady) || rt.Runtime.AppliedControlRevision != c.MaintenanceRevision {
			return reject("maintenance_ack_changed", "原主机尚未确认预期的 normal 控制")
		}
		observe(slog.LevelDebug, "awaiting_normal_ack", "")
		waiting = true
	}
	return waiting, "", nil
}
