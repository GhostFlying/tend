package views

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func HostExecutors(ctx context.Context, q store.Querier, host string, now int64) ([]scheduler.Executor, error) {
	return hostExecutors(ctx, q, host, now, false)
}

// includeUnavailable retains ordinary readiness facts for read-only explanations.
// Scheduling callers always use the public function with this flag false.
func hostExecutors(ctx context.Context, q store.Querier, host string, now int64, includeUnavailable bool) ([]scheduler.Executor, error) {
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if (!includeUnavailable && !runtime.Online) || !(protocol.RunnerProtocolSupported(runtime.Runtime.Protocol) && runtime.Runtime.JournalVersion >= 12 && slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAgentExecution)) || (!includeUnavailable && now > 0 && now-runtime.LastHeartbeatAt >= 60000) {
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
	state, err := store.GetExtensionState(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, e := range runtime.Runtime.AgentExecutors {
		p := e.Pin
		if (p.Purpose == "issue_manager" && runtime.Runtime.JournalVersion < 13) || (!includeUnavailable && !e.Available) || p.Validate(host) != nil || p.SnapshotID != inv.Receipt.SnapshotID || p.InventoryRevision != inv.Receipt.LocalRevision || p.PublicSHA256 != inv.Receipt.PublicSHA256 {
			continue
		}
		for _, a := range inv.Projection.Agents {
			binding := a.Binding
			if p.Purpose == "issue_manager" && a.OwnerBinding != nil {
				binding = *a.OwnerBinding
			}
			if a.Identity != p.Identity || a.Revision != p.AgentRevision || binding != p.Runtime || !a.Enabled || a.RequestedModel != p.Profile.Model || a.RequestedEffort != p.Profile.Effort || p.Purpose == "issue_manager" && (p.Worker != protocol.WorkerAppServer || p.BackendID != "") {
				continue
			}
			runtimeEnabled := false
			for _, r := range inv.Projection.Runtimes {
				if r.RuntimeID == p.Runtime.RuntimeID && r.Revision == p.Runtime.Revision && r.Enabled && (p.Purpose != "issue_manager" || r.CLI != "") && ((r.Kind == "appserver-worker" && p.Worker == protocol.WorkerAppServer) || ((r.Kind == "orca-worker" || r.Kind == "central-traex-worker") && p.Worker == protocol.WorkerOrca)) {
					runtimeEnabled = true
				}
			}
			if !runtimeEnabled {
				continue
			}
			managed := runtime.Runtime.Extensions != nil && runtime.Runtime.Extensions.Enabled && slices.Contains(runtime.Runtime.Extensions.Runtimes, p.Runtime.RuntimeID)
			if managed {
				if !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityManagedExtensions) || state.Published == nil || p.Extensions.ReleaseID != state.Published.ID || !slices.Contains(a.Categories, p.Extensions.CategoryID) {
					continue
				}
				release, _, err := store.GetExtensionRelease(ctx, q, state.Published.ID)
				if err != nil {
					return nil, err
				}
				if _, err = extensions.VerifyCombination(release, p.Extensions); err != nil {
					continue
				}
			} else if p.Extensions != (protocol.ExtensionExecutionPin{}) {
				continue
			}
			categories := []string{}
			for _, id := range a.Categories {
				if managed && id != p.Extensions.CategoryID {
					continue
				}
				category, err := store.GetAgentCategory(ctx, q, id)
				if err != nil {
					return nil, err
				}
				if category.State == "active" {
					categories = append(categories, id)
				}
			}
			out = append(out, scheduler.Executor{Pin: p, Categories: categories, DeclaredCapabilities: slices.Clone(a.Capabilities), CapabilitySupport: runtime.Runtime.JournalVersion >= 19 && slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityExecutorDeclarations), CapabilityNowMillis: now})
		}
	}
	return out, nil
}
