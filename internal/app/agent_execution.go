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
		if runtimeSupportsCapabilityDeclarations(runtime) {
			return nil
		}
		var count int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_capability_bindings b JOIN runs r USING(run_id) WHERE r.host_id=? AND r.observed_state NOT IN ('finished','failed') AND (?=0 OR COALESCE(json_extract(b.binding_json,'$.match_policy'),'')<>'')`, host, runtimeSupportsExecutorCapabilities(runtime)).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "capability-constrained runs require their declaration scheduling contract")
		}
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

func runtimeSupportsAgentContinuation(r *protocol.RunnerRuntime) bool {
	return runtimeSupportsAgentExecution(r) && slices.Contains(r.Capabilities, protocol.CapabilityAgentContinuation)
}

func runtimeSupportsExecutorCapabilities(r *protocol.RunnerRuntime) bool {
	return runtimeSupportsAgentExecution(r) && r.JournalVersion >= 16 && slices.Contains(r.Capabilities, protocol.CapabilityExecutorEvidence)
}

func runtimeSupportsCapabilityDeclarations(r *protocol.RunnerRuntime) bool {
	return runtimeSupportsAgentExecution(r) && r.JournalVersion >= 17 && slices.Contains(r.Capabilities, protocol.CapabilityExecutorDeclarations)
}
func runtimeSupportsCapabilityInventory(r *protocol.RunnerRuntime) bool {
	return runtimeSupportsExecutorCapabilities(r) || runtimeSupportsCapabilityDeclarations(r)
}
