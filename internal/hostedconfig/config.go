// Package hostedconfig freezes the existing agent configuration value model for
// hosted coordination. Configuration contains private local instructions; it is
// never an owner API response or a runner inventory payload.
package hostedconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Configuration is the single server-file authority for named hosted agents.
// Revision changes explicitly when the owner accepts a different configuration.
type Configuration struct {
	Revision      int64              `json:"revision"`
	EnvironmentID string             `json:"execution_environment_id"`
	MainAgentID   string             `json:"main_agent_id"`
	Definitions   agentconfig.Config `json:"definitions"`
}

// Prepared is detached from the caller's mutable configuration.
type Prepared struct {
	configuration Configuration
	private       []byte
	privateHash   string
	public        protocol.AgentInventoryProjection
}

func Prepare(c Configuration) (*Prepared, error) {
	if c.Revision < 1 || c.MainAgentID == "" {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "hosted configuration needs revision and a named main agent")
	}
	// Reuse the strict P2 decoder after normalizing valid typed empty collections.
	if c.Definitions.Runtimes == nil {
		c.Definitions.Runtimes = []agentconfig.RuntimeBinding{}
	}
	if c.Definitions.Agents == nil {
		c.Definitions.Agents = []agentconfig.AgentDefinition{}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var detached Configuration
	if err = json.Unmarshal(raw, &detached); err != nil {
		return nil, err
	}
	definitions, err := json.Marshal(detached.Definitions)
	if err != nil {
		return nil, err
	}
	detached.Definitions, err = agentconfig.Decode(definitions)
	if err != nil {
		return nil, err
	}
	d, err := agentconfig.Validate(detached.EnvironmentID, detached.Definitions, agentconfig.SourceReference{Kind: agentconfig.Inline})
	if err != nil {
		return nil, err
	}
	// This source serves hosted coordination only. It cannot advertise worker
	// credentials or silently substitute an executable worker runtime.
	for _, runtime := range detached.Definitions.Runtimes {
		if runtime.Kind != agentconfig.HostedManager {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "server named configuration requires hosted runtimes")
		}
	}
	slices.SortFunc(detached.Definitions.Agents, func(a, b agentconfig.AgentDefinition) int { return strings.Compare(a.LocalID, b.LocalID) })
	slices.SortFunc(detached.Definitions.Runtimes, func(a, b agentconfig.RuntimeBinding) int { return strings.Compare(a.ID, b.ID) })
	for i := range detached.Definitions.Agents {
		a := &detached.Definitions.Agents[i]
		slices.Sort(a.Categories)
		slices.Sort(a.Purposes)
		slices.Sort(a.Skills)
		slices.Sort(a.MCPServers)
	}
	for i := range detached.Definitions.Runtimes {
		slices.Sort(detached.Definitions.Runtimes[i].DeclaredPurposes)
	}
	raw, err = json.Marshal(detached)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	p := &Prepared{configuration: detached, private: raw, privateHash: hex.EncodeToString(digest[:]), public: d.Public()}
	if _, err = p.Select(detached.MainAgentID, agentconfig.Main); err != nil {
		return nil, err
	}
	return p, nil
}

// Selection is a private execution snapshot, not a public inventory type.
// Definition uses the sole P2/P3 model instead of copying its fields.
type Selection struct {
	ReceiptOnly           bool                        `json:"receipt_only,omitempty"`
	RunnerManaged         bool                        `json:"runner_managed,omitempty"`
	RunnerRunID           string                      `json:"runner_run_id,omitempty"`
	IssueID               string                      `json:"issue_id,omitempty"`
	AssignmentEpoch       int64                       `json:"assignment_epoch,omitempty"`
	AppliedModel          string                      `json:"applied_model,omitempty"`
	AppliedProvider       string                      `json:"applied_provider,omitempty"`
	AppliedEffort         string                      `json:"applied_effort,omitempty"`
	ConfigurationRevision int64                       `json:"configuration_revision"`
	ConfigurationHash     string                      `json:"configuration_hash"`
	Identity              protocol.AgentIdentity      `json:"identity"`
	Definition            agentconfig.AgentDefinition `json:"definition"`
	Runtime               agentconfig.RuntimeBinding  `json:"runtime"`
	Purpose               agentconfig.Purpose         `json:"purpose"`
}

func (p *Prepared) Select(localID string, purpose agentconfig.Purpose) (Selection, error) {
	for _, a := range p.configuration.Definitions.Agents {
		if a.LocalID != localID {
			continue
		}
		if !a.Enabled || !slices.Contains(a.Purposes, purpose) {
			break
		}
		for _, runtime := range p.configuration.Definitions.Runtimes {
			if runtime.ID != a.RuntimeID {
				continue
			}
			if !runtime.Enabled || runtime.Kind != agentconfig.HostedManager || !slices.Contains(runtime.DeclaredPurposes, purpose) {
				break
			}
			// No plugin/MCP factory is activated by named management configuration.
			// Runtime restrictions remain real adapter configuration, not instructions.
			if len(a.Skills) > 0 || len(a.MCPServers) > 0 {
				return Selection{}, protocol.Errorf(protocol.CodeInvalidRequest, "hosted skills and MCP adapters are not enabled")
			}
			result := Selection{ConfigurationRevision: p.configuration.Revision, ConfigurationHash: p.privateHash, Identity: protocol.AgentIdentity{ExecutionEnvironmentID: p.configuration.EnvironmentID, LocalID: a.LocalID}, Definition: a, Runtime: runtime, Purpose: purpose}
			raw, err := json.Marshal(result)
			if err != nil {
				return Selection{}, err
			}
			var detached Selection
			err = json.Unmarshal(raw, &detached)
			return detached, err
		}
	}
	return Selection{}, protocol.Errorf(protocol.CodeInvalidTransition, "the selected hosted definition is unavailable for this role")
}
func (p *Prepared) Revision() int64     { return p.configuration.Revision }
func (p *Prepared) Hash() string        { return p.privateHash }
func (p *Prepared) MainAgentID() string { return p.configuration.MainAgentID }
func (p *Prepared) PrivateJSON() []byte { return slices.Clone(p.private) }
func (p *Prepared) Public() protocol.AgentInventoryProjection {
	raw, _ := json.Marshal(p.public)
	var result protocol.AgentInventoryProjection
	_ = json.Unmarshal(raw, &result)
	return result
}

// Restore decodes a server-local accepted snapshot. It does not read the current
// editable file or infer that a runtime is available merely from registration.
func Restore(raw []byte) (*Prepared, error) {
	var c Configuration
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return Prepare(c)
}

// IdentityRef is opaque business responsibility identity; each component keeps
// the same meaning as the public two-component AgentIdentity.
func IdentityRef(identity protocol.AgentIdentity) string {
	return identity.ExecutionEnvironmentID + "/" + identity.LocalID
}

// ValidateUpdate keeps definition versions meaningful for the same stable
// execution identity. It does not revoke earlier execution snapshots.
func (p *Prepared) ValidateUpdate(previous *Prepared) error {
	if previous == nil || previous.configuration.EnvironmentID != p.configuration.EnvironmentID {
		return nil
	}
	for _, next := range p.configuration.Definitions.Agents {
		for _, old := range previous.configuration.Definitions.Agents {
			if next.LocalID != old.LocalID {
				continue
			}
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(next)
			if next.Revision < old.Revision || (next.Revision == old.Revision && string(a) != string(b)) {
				return protocol.Errorf(protocol.CodeInvalidRequest, "changed agent definition requires a new revision")
			}
		}
	}
	for _, next := range p.configuration.Definitions.Runtimes {
		for _, old := range previous.configuration.Definitions.Runtimes {
			if next.ID != old.ID {
				continue
			}
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(next)
			if next.Revision < old.Revision || (next.Revision == old.Revision && string(a) != string(b)) {
				return protocol.Errorf(protocol.CodeInvalidRequest, "changed runtime binding requires a new revision")
			}
		}
	}
	return nil
}
