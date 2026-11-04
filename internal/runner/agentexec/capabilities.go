package agentexec

import (
	"context"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// ObserveCapability never starts a session or calls a model. Adapters without
// authoritative inventory and atomic launch enforcement are explicitly unknown.
func (p *Pool) ObserveCapability(ctx context.Context, pin protocol.RunExecutionPin, ref protocol.CapabilityReference) (protocol.ExecutorCapabilityEvidence, error) {
	e := protocol.ExecutorCapabilityEvidence{Pin: pin, Capability: ref, Kind: "tool_inventory", Scope: "profile", State: "unverified", Reason: "adapter_evidence_unsupported"}
	resolved, err := p.Resolve(ctx, pin)
	if err != nil {
		return e, err
	}
	adapter, ok := resolved.Workers.(harness.CapabilityWorkers)
	if !ok {
		return e, nil
	}
	return adapter.CapabilityInventory(ctx, pin, ref)
}
