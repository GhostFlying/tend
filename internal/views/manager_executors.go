package views

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"slices"
)

func HostExecutors(ctx context.Context, q store.Querier, host string, now int64) ([]scheduler.Executor, error) {
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !runtime.Online || !(protocol.RunnerProtocolSupported(runtime.Runtime.Protocol) && runtime.Runtime.JournalVersion >= 12 && slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAgentExecution)) || (now > 0 && now-runtime.LastHeartbeatAt >= 60000) {
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
		if (p.Purpose == "issue_manager" && runtime.Runtime.JournalVersion < 13) || !e.Available || p.Validate(host) != nil || p.SnapshotID != inv.Receipt.SnapshotID || p.InventoryRevision != inv.Receipt.LocalRevision || p.PublicSHA256 != inv.Receipt.PublicSHA256 {
			continue
		}
		for _, a := range inv.Projection.Agents {
			if a.Identity != p.Identity || a.Revision != p.AgentRevision || a.Binding != p.Runtime || !a.Enabled || a.RequestedModel != p.Profile.Model || a.RequestedEffort != p.Profile.Effort || ((p.Purpose == "issue_manager" && (!slices.Contains(a.Purposes, "issue_manager") || p.Worker != protocol.WorkerAppServer || p.BackendID != "")) || (p.Purpose != "issue_manager" && !slices.Contains(a.Purposes, "worker_run"))) {
				continue
			}
			runtimeEnabled := false
			for _, r := range inv.Projection.Runtimes {
				if r.RuntimeID == p.Runtime.RuntimeID && r.Revision == p.Runtime.Revision && r.Enabled && (p.Purpose != "issue_manager" || slices.Contains(r.DeclaredPurposes, "issue_manager")) && ((r.Kind == "appserver-worker" && p.Worker == protocol.WorkerAppServer) || (r.Kind == "orca-worker" && p.Worker == protocol.WorkerOrca)) {
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
