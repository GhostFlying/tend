package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/GhostFlying/tend/internal/protocol"
)

// WithLogger uses the caller's existing handler, level and output lifecycle.
// An App without a logger produces no additional diagnostic output.
func WithLogger(log *slog.Logger) Option {
	return func(a *App) { a.log = log }
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
		case "task", "run", "issue", "work_item", "upgrade", "attention":
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
