package views

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// HostContinuations are usable only by the exact responsibility pin that caused
// their retention. They are never merged into HostExecutors' fresh candidates.
func HostContinuations(ctx context.Context, q store.Querier, host string, now int64) ([]scheduler.Executor, error) {
	return hostContinuations(ctx, q, host, now, false)
}

// includeUnavailable retains ordinary readiness facts for read-only explanations.
// Scheduling callers always use the public function with this flag false.
func hostContinuations(ctx context.Context, q store.Querier, host string, now int64, includeUnavailable bool) ([]scheduler.Executor, error) {
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if (!includeUnavailable && !runtime.Online) || !protocol.RunnerProtocolSupported(runtime.Runtime.Protocol) || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAgentContinuation) || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAgentExecution) || (!includeUnavailable && now > 0 && now-runtime.LastHeartbeatAt >= 60000) {
		return nil, nil
	}
	required, err := store.RequiredAgentExecutions(ctx, q, host)
	if err != nil {
		return nil, err
	}
	out := []scheduler.Executor{}
	for _, e := range runtime.Runtime.AgentContinuations {
		if (!includeUnavailable && !e.Available) || !slices.Contains(required, e.Pin) {
			continue
		}
		inv, err := store.GetAgentInventoryAt(ctx, q, e.Pin)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, a := range inv.Projection.Agents {
			binding := a.Binding
			if e.Pin.Purpose == "issue_manager" && a.OwnerBinding != nil {
				binding = *a.OwnerBinding
			}
			if a.Identity == e.Pin.Identity && a.Revision == e.Pin.AgentRevision && binding == e.Pin.Runtime && a.RequestedModel == e.Pin.Profile.Model && a.RequestedEffort == e.Pin.Profile.Effort {
				categories := slices.Clone(a.Categories)
				if e.Pin.Extensions != (protocol.ExtensionExecutionPin{}) {
					categories = []string{e.Pin.Extensions.CategoryID}
				}
				out = append(out, scheduler.Executor{Continuation: true, Pin: e.Pin, Categories: categories, DeclaredCapabilities: slices.Clone(a.Capabilities), CapabilitySupport: runtime.Runtime.JournalVersion >= 19 && slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityExecutorDeclarations), CapabilityNowMillis: now})
			}
		}
	}
	return out, nil
}
