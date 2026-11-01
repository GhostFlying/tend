package agentconfig

import (
	"cmp"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Public constructs a fresh detached projection. No private source reference, executable
// reference, raw config/instructions, or private digest is copied into it.
func (d *Definitions) Public() protocol.AgentInventoryProjection {
	p := protocol.AgentInventoryProjection{FormatVersion: d.config.FormatVersion, ExecutionEnvironmentID: d.environmentID, SourceKind: protocol.AgentSourceKind(d.source.Kind),
		Runtimes: make([]protocol.AgentInventoryRuntime, 0, len(d.config.Runtimes)), Agents: make([]protocol.AgentInventoryDefinition, 0, len(d.config.Agents))}
	runtimes := make(map[string]RuntimeBinding, len(d.config.Runtimes))
	for _, r := range d.config.Runtimes {
		runtimes[r.ID] = r
		purposes := publicPurposes(r.DeclaredPurposes)
		slices.Sort(purposes)
		p.Runtimes = append(p.Runtimes, protocol.AgentInventoryRuntime{RuntimeID: r.ID, Revision: r.Revision, Kind: string(r.Kind), CLI: r.CLI,
			DeclaredPurposes: purposes, Enabled: r.Enabled, Registration: "staged", Availability: unavailable(r, true)})
	}
	for _, a := range d.config.Agents {
		r := runtimes[a.RuntimeID]
		var owner *protocol.AgentRuntimeReference
		if a.OwnerRuntimeID != "" {
			binding := runtimes[a.OwnerRuntimeID]
			owner = &protocol.AgentRuntimeReference{RuntimeID: binding.ID, Revision: binding.Revision}
		}
		categories, purposes := slices.Clone(a.Categories), publicPurposes(a.Purposes)
		slices.Sort(categories)
		slices.Sort(purposes)
		p.Agents = append(p.Agents, protocol.AgentInventoryDefinition{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: d.environmentID, LocalID: a.LocalID},
			Revision: a.Revision, DisplayName: a.DisplayName, Binding: protocol.AgentRuntimeReference{RuntimeID: r.ID, Revision: r.Revision}, OwnerBinding: owner,
			Categories: categories, Purposes: purposes, RequestedModel: a.Model, RequestedEffort: a.Effort,
			Enabled: a.Enabled, Registration: "staged", Availability: unavailable(r, a.Enabled)})
	}

	slices.SortFunc(p.Runtimes, func(a, b protocol.AgentInventoryRuntime) int { return cmp.Compare(a.RuntimeID, b.RuntimeID) })
	slices.SortFunc(p.Agents, func(a, b protocol.AgentInventoryDefinition) int {
		return cmp.Compare(a.Identity.LocalID, b.Identity.LocalID)
	})
	return p
}

func unavailable(r RuntimeBinding, agentEnabled bool) protocol.AgentInventoryAvailability {
	switch {
	case !agentEnabled:
		return protocol.AgentInventoryAvailability{Reason: "definition_disabled"}
	case !r.Enabled:
		return protocol.AgentInventoryAvailability{Reason: "binding_disabled"}
	case r.Kind == HostedManager:
		return protocol.AgentInventoryAvailability{Reason: "isolation_not_verified"}
	default:
		return protocol.AgentInventoryAvailability{Reason: "runtime_not_verified"}
	}
}

// CanonicalPublic fixes the initial public format's struct-field and set ordering;
// it does not claim RFC 8785 interoperability. The digest covers only public
// bytes and is not an identity or proof of private runtime snapshot equality.
func (d *Definitions) CanonicalPublic() ([]byte, string, error) {
	b, hash, err := protocol.CanonicalAgentInventory(d.Public())
	if err != nil {
		return nil, "", invalid("projection_error", "$")
	}
	return b, hash, nil
}

func publicPurposes(values []Purpose) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}
