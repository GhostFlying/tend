package updater

import (
	"context"
	"log/slog"
	"time"
)

// Log only durable transitions and changed errors. Repeated health polls do
// not produce another log, and a persistence failure is never logged as commit.
func (s *Supervisor) logTransition(before, after State) {
	log := s.opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := time.Now()
	if s.opts.Now != nil {
		now = s.opts.Now()
	}
	if op := after.Operation; op != nil {
		old := before.Operation
		if old == nil || old.ID != op.ID || old.Phase != op.Phase || old.LastError != op.LastError || before.Process != after.Process {
			level := slog.LevelInfo
			currentError := op.LastError
			if op.Phase == "committed" || op.Phase == "cancelled" || old != nil && old.ID == op.ID && old.LastError == op.LastError {
				currentError = ""
			}
			if currentError != "" {
				level = slog.LevelWarn
			}
			attrs := []any{"instance_id", s.cfg.InstanceID, "upgrade_id", op.Binding.UpgradeID, "operation_id", op.ID, "phase", op.Phase, "error", currentError, "historical_error", op.LastError, "elapsed_ms", now.Sub(op.StartedAt).Milliseconds(), "grant_deadline_boottime_ms", op.GrantDeadlineMillis, "process_pid", after.Process.PID, "process_start_ticks", after.Process.StartTicks, "process_launch_id", after.Process.LaunchID, "artifact_sha256", after.Process.SHA256, "health_count", op.HealthCount, "exit_observed", op.LaunchExit != nil}
			if op.LaunchExit != nil {
				attrs = append(attrs, "exit_pid", op.LaunchExit.PID, "exit_launch_id", op.LaunchExit.LaunchID, "exit_code", op.LaunchExit.ExitCode, "exit_signal", op.LaunchExit.Signal)
			}
			log.Log(context.Background(), level, "updater operation changed", attrs...)
		}
	}
	if m := after.Maintenance; m != nil {
		old := before.Maintenance
		if old == nil || old.Plan.MaintenanceID != m.Plan.MaintenanceID || old.Phase != m.Phase || old.LastError != m.LastError || before.Process != after.Process {
			log.Info("updater maintenance changed", "instance_id", s.cfg.InstanceID, "maintenance_id", m.Plan.MaintenanceID, "request_id", m.Plan.RequestID, "phase", m.Phase, "error", m.LastError, "deadline_ms", m.Plan.ExpiresAtMillis, "health_count", m.HealthCount, "process_pid", after.Process.PID, "process_start_ticks", after.Process.StartTicks, "process_launch_id", after.Process.LaunchID)
		}
	}
}
