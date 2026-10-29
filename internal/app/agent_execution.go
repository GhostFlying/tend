package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func hostExecutors(ctx context.Context, q store.Querier, host string, now int64) ([]scheduler.Executor, error) {
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !runtime.Online || !runtimeSupportsAgentExecution(&runtime.Runtime) || now-runtime.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
		return nil, nil
	}
	inv, err := store.GetAgentInventory(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []scheduler.Executor
	for _, e := range runtime.Runtime.AgentExecutors {
		p := e.Pin
		if !e.Available || p.Validate(host) != nil || p.SnapshotID != inv.Receipt.SnapshotID || p.InventoryRevision != inv.Receipt.LocalRevision || p.PublicSHA256 != inv.Receipt.PublicSHA256 {
			continue
		}
		for _, a := range inv.Projection.Agents {
			if a.Identity != p.Identity || a.Revision != p.AgentRevision || a.Binding != p.Runtime || !a.Enabled || a.RequestedModel != p.Profile.Model || a.RequestedEffort != p.Profile.Effort || !slices.Contains(a.Purposes, "worker_run") {
				continue
			}
			runtimeEnabled := false
			for _, r := range inv.Projection.Runtimes {
				if r.RuntimeID == p.Runtime.RuntimeID && r.Revision == p.Runtime.Revision && r.Enabled && ((r.Kind == "appserver-worker" && p.Worker == protocol.WorkerAppServer) || (r.Kind == "orca-worker" && p.Worker == protocol.WorkerOrca)) {
					runtimeEnabled = true
				}
			}
			if !runtimeEnabled {
				continue
			}
			categories := []string{}
			for _, id := range a.Categories {
				category, err := store.GetAgentCategory(ctx, q, id)
				if err != nil {
					return nil, err
				}
				if category.State == "active" {
					categories = append(categories, id)
				}
			}
			out = append(out, scheduler.Executor{Pin: p, Categories: categories})
		}
	}
	return out, nil
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
	return r != nil && r.Protocol == protocol.CurrentRunnerProtocol && r.JournalVersion >= 12 && slices.Contains(r.Capabilities, protocol.CapabilityAgentExecution)
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
