package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// WithLogger uses the caller's existing handler, level and output lifecycle.
// An App without a logger produces no additional diagnostic output.
func WithLogger(log *slog.Logger) Option {
	return func(a *App) { a.log = log }
}

type executionDiagnostic struct {
	level   slog.Level
	message string
	attrs   []any
}

// observeDecision retains only values read by the current command. They are
// emitted after the write finishes, so diagnostics cannot hold its transaction.
func (x *Exec) observeDecision(ctx context.Context, level slog.Level, message string, attrs ...any) {
	if x.log != nil && x.log.Enabled(ctx, level) {
		x.diagnostics = append(x.diagnostics, executionDiagnostic{level, message, attrs})
	}
}

func (a *App) logExecution(ctx context.Context, x *Exec, operation protocol.Operation, err error) {
	if a.log == nil {
		return
	}
	result := "no_effect"
	if err != nil {
		result = "failed"
	} else if operation.Error != nil {
		result = "rejected"
	} else if len(x.diagnosticEvents) > 0 {
		result = "committed"
	}
	principal := x.ActualPrincipal()
	attrs := []any{"trace_id", x.TraceID, "request_id", x.Env.RequestID, "operation", x.Env.Operation, "target_type", x.Env.Target.Type, "target_id", x.Env.Target.ID, "principal_kind", principal.Kind, "principal_subject", principal.Subject, "command_outcome", result}
	if principal.Role != "" {
		attrs = append(attrs, "principal_role", principal.Role)
	}
	if principal.Thread != "" {
		attrs = append(attrs, "thread_id", principal.Thread)
	}
	if x.Env.ExpectedControlRevision != nil {
		attrs = append(attrs, "expected_control_revision", *x.Env.ExpectedControlRevision)
	}
	for _, d := range x.diagnostics {
		a.log.Log(ctx, d.level, d.message, append(append([]any{}, attrs...), d.attrs...)...)
	}
	if operation.Error != nil || err != nil {
		code := protocol.CodeInternal
		if operation.Error != nil {
			code = operation.Error.Code
			var details map[string]any
			if json.Unmarshal(operation.Error.Details, &details) == nil {
				for _, key := range []string{"field", "max", "actual", "unit", "task_id", "run_id", "spec_version", "generation", "current_control_revision", "current_assignment_epoch", "observed_state", "desired_state", "lifecycle", "recovery_tool", "continuation_tool"} {
					if value, ok := details[key]; ok {
						switch value.(type) {
						case string, float64, bool:
							attrs = append(attrs, key, value)
						}
					}
				}
			}
		} else if pe, ok := errors.AsType[*protocol.Error](err); ok {
			code = pe.Code
		}
		level := slog.LevelInfo
		if err != nil && result == "failed" {
			level = slog.LevelWarn
		}
		// Error messages and payloads may contain user input or credentials.
		// The correlated boundary logs retain their own safe detail.
		a.log.Log(ctx, level, "app command rejected", append(attrs, "code", code, "cancelled", errors.Is(err, context.Canceled), "deadline_exceeded", errors.Is(err, context.DeadlineExceeded))...)
		return
	}
	for _, event := range x.diagnosticEvents {
		level := slog.LevelDebug
		switch event.ObjectType {
		case "task", "run", "work_item", "upgrade", "attention":
			level = slog.LevelInfo
		}
		fields := append(append([]any{}, attrs...), "event", event.Kind, "object_type", event.ObjectType, "object_id", event.ObjectID, "object_revision", event.ObjectVersion)
		// Enumerate only internal state fields; never serialize an event body.
		for _, key := range []string{"task_id", "run_id", "lease_id", "host_id", "action_id", "role", "state", "status", "phase", "desired_state", "observed_state"} {
			if value, ok := event.Body[key]; ok {
				fields = append(fields, key, value)
			}
		}
		a.log.Log(ctx, level, "app state transition", fields...)
	}
}

type upgradeHostAvailability struct {
	HostID           string                      `json:"host_id"`
	Slots            int64                       `json:"slots"`
	Runtime          *upgradeRuntimeAvailability `json:"runtime,omitempty"`
	Control          *upgradeControlAvailability `json:"control,omitempty"`
	FailedPredicates []string                    `json:"failed_predicates"`
}

type upgradeRuntimeAvailability struct {
	Online                 bool   `json:"online"`
	LastHeartbeatMillis    int64  `json:"last_heartbeat_ms"`
	HeartbeatAgeMillis     int64  `json:"heartbeat_age_ms"`
	HeartbeatLimitMillis   int64  `json:"heartbeat_limit_ms"`
	InstanceID             string `json:"instance_id"`
	Mode                   string `json:"mode"`
	AppliedControlRevision int64  `json:"applied_control_revision"`
	UpgradeID              string `json:"upgrade_id"`
	PendingClaims          int64  `json:"pending_claims"`
	PendingOperations      int64  `json:"pending_operations"`
}

type upgradeControlAvailability struct {
	Mode      string `json:"mode"`
	Revision  int64  `json:"revision"`
	UpgradeID string `json:"upgrade_id"`
}

// inspectOtherUpgradeHosts retains the exact predicates and the short-circuit
// reads used by the existing rolling-host decision. In particular, zero-slot
// and missing-runtime hosts do not trigger extra control or runtime queries.
func inspectOtherUpgradeHosts(ctx context.Context, q store.Querier, exclude string, now int64) (bool, []upgradeHostAvailability, error) {
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return false, nil, err
	}
	var observations []upgradeHostAvailability
	for _, h := range hosts {
		if h.HostID == exclude {
			continue
		}
		o := upgradeHostAvailability{HostID: h.HostID, Slots: h.Slots, FailedPredicates: []string{}}
		if h.Slots <= 0 {
			o.FailedPredicates = append(o.FailedPredicates, "slots_not_positive")
			observations = append(observations, o)
			continue
		}
		rt, err := store.GetHostRuntime(ctx, q, h.HostID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				o.FailedPredicates = append(o.FailedPredicates, "runtime_missing")
				observations = append(observations, o)
				continue
			}
			return false, observations, err
		}
		ctl, err := runnerControl(ctx, q, h.HostID, now)
		if err != nil {
			return false, observations, err
		}
		o.Runtime = &upgradeRuntimeAvailability{Online: rt.Online, LastHeartbeatMillis: rt.LastHeartbeatAt, HeartbeatAgeMillis: now - rt.LastHeartbeatAt, HeartbeatLimitMillis: RunnerOfflineAfter.Milliseconds(), InstanceID: rt.Runtime.InstanceID, Mode: rt.Runtime.Mode, AppliedControlRevision: rt.Runtime.AppliedControlRevision, UpgradeID: rt.Runtime.UpgradeID, PendingClaims: rt.Runtime.PendingClaims, PendingOperations: rt.Runtime.PendingOperations}
		o.Control = &upgradeControlAvailability{Mode: ctl.Mode, Revision: ctl.Revision, UpgradeID: ctl.UpgradeID}
		if !rt.Online {
			o.FailedPredicates = append(o.FailedPredicates, "offline")
		}
		if now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
			o.FailedPredicates = append(o.FailedPredicates, "heartbeat_stale")
		}
		if ctl.Mode != protocol.RunnerNormal {
			o.FailedPredicates = append(o.FailedPredicates, "control_mode_not_normal")
		}
		if rt.Runtime.Mode != protocol.RunnerNormal {
			o.FailedPredicates = append(o.FailedPredicates, "runtime_mode_not_normal")
		}
		if rt.Runtime.AppliedControlRevision != ctl.Revision {
			o.FailedPredicates = append(o.FailedPredicates, "control_revision_unacknowledged")
		}
		observations = append(observations, o)
		if len(o.FailedPredicates) == 0 {
			return true, observations, nil
		}
	}
	return false, observations, nil
}

func observeUpgradeHostDecision(ctx context.Context, x *Exec, batch store.UpgradeRow, target, decision string, level slog.Level, hosts []upgradeHostAvailability) {
	x.observeDecision(ctx, level, "upgrade host availability decision", "upgrade_id", batch.View.UpgradeID, "upgrade_revision", batch.View.Revision, "upgrade_phase", batch.View.Phase, "target_host_id", target, "decision", decision, "observed_at_ms", x.Now.UnixMilli(), "stage_deadline_ms", batch.Deadline, "stage_remaining_ms", max(0, batch.Deadline-x.Now.UnixMilli()), "overall_deadline_ms", batch.View.OverallDeadlineMillis, "other_hosts", hosts)
}
