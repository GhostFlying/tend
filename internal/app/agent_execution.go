package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func hostExecutors(ctx context.Context, q store.Querier, host string, now int64) ([]scheduler.Executor, error) {
	return views.HostExecutors(ctx, q, host, now)
}

// Named selection uses current per-adapter/backend facts in the scheduler. The
// legacy host worker remains authoritative for tasks without explicit selection.
func checkSpecMounts(ctx context.Context, q store.Querier, spec domain.Spec) error {
	if spec.AgentSelection != nil {
		return nil
	}
	return checkMounts(ctx, q, spec.Workspace)
}

func runtimeSupportsAgentExecution(r *protocol.RunnerRuntime) bool {
	return r != nil && protocol.RunnerProtocolSupported(r.Protocol) && r.JournalVersion >= 12 && slices.Contains(r.Capabilities, protocol.CapabilityAgentExecution)
}

func checkNamedRunRecovery(ctx context.Context, q store.Querier, host string, runtime *protocol.RunnerRuntime) error {
	if runtimeSupportsAgentExecution(runtime) {
		return nil
	}
	runs, err := store.ListOpenRunsOnHost(ctx, q, host)
	if err != nil {
		return err
	}
	for _, run := range runs {
		_, err := store.GetRunExecution(ctx, q, run.RunID)
		if err == nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "runner needs named execution capability and its original journal to recover this run")
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}
