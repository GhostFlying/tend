package agentconfig

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// Availability is separate from declared configuration. This package has no
// verifier, so it can only produce unavailable results with stable reasons.
type Availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

type BindingReference struct {
	RuntimeID string `json:"runtime_id"`
	Revision  uint64 `json:"revision"`
}

// PublicInventory is a whitelist projection, not a serialized local Config.
type PublicInventory struct {
	FormatVersion          uint64          `json:"format_version"`
	ExecutionEnvironmentID string          `json:"execution_environment_id"`
	SourceKind             SourceKind      `json:"source_kind"`
	Runtimes               []PublicRuntime `json:"runtimes"`
	Agents                 []PublicAgent   `json:"agents"`
}

type PublicRuntime struct {
	RuntimeID        string       `json:"runtime_id"`
	Revision         uint64       `json:"revision"`
	Kind             RuntimeKind  `json:"kind"`
	CLI              string       `json:"cli,omitempty"`
	DeclaredPurposes []Purpose    `json:"declared_purposes"`
	Enabled          bool         `json:"enabled"`
	Registration     string       `json:"registration"`
	Availability     Availability `json:"availability"`
}

type PublicAgent struct {
	Identity        Identity         `json:"identity"`
	Revision        uint64           `json:"revision"`
	DisplayName     string           `json:"display_name"`
	Binding         BindingReference `json:"runtime_binding_ref"`
	Categories      []string         `json:"categories"`
	Purposes        []Purpose        `json:"purposes"`
	RequestedModel  string           `json:"requested_model"`
	RequestedEffort string           `json:"requested_effort"`
	Enabled         bool             `json:"enabled"`
	Registration    string           `json:"registration"`
	Availability    Availability     `json:"availability"`
}

// Public constructs a fresh detached projection. No private source reference, executable
// reference, raw config/instructions, or private digest is copied into it.
func (d *Definitions) Public() PublicInventory {
	p := PublicInventory{FormatVersion: 1, ExecutionEnvironmentID: d.environmentID, SourceKind: d.source.Kind,
		Runtimes: make([]PublicRuntime, 0, len(d.config.Runtimes)), Agents: make([]PublicAgent, 0, len(d.config.Agents))}
	runtimes := make(map[string]RuntimeBinding, len(d.config.Runtimes))
	for _, r := range d.config.Runtimes {
		runtimes[r.ID] = r
		purposes := slices.Clone(r.DeclaredPurposes)
		slices.Sort(purposes)
		p.Runtimes = append(p.Runtimes, PublicRuntime{RuntimeID: r.ID, Revision: r.Revision, Kind: r.Kind, CLI: r.CLI,
			DeclaredPurposes: purposes, Enabled: r.Enabled, Registration: "staged", Availability: unavailable(r, true)})
	}
	for _, a := range d.config.Agents {
		r := runtimes[a.RuntimeID]
		categories, purposes := slices.Clone(a.Categories), slices.Clone(a.Purposes)
		slices.Sort(categories)
		slices.Sort(purposes)
		p.Agents = append(p.Agents, PublicAgent{Identity: Identity{ExecutionEnvironmentID: d.environmentID, LocalID: a.LocalID},
			Revision: a.Revision, DisplayName: a.DisplayName, Binding: BindingReference{RuntimeID: r.ID, Revision: r.Revision},
			Categories: categories, Purposes: purposes, RequestedModel: a.Model, RequestedEffort: a.Effort,
			Enabled: a.Enabled, Registration: "staged", Availability: unavailable(r, a.Enabled)})
	}
	slices.SortFunc(p.Runtimes, func(a, b PublicRuntime) int { return cmp.Compare(a.RuntimeID, b.RuntimeID) })
	slices.SortFunc(p.Agents, func(a, b PublicAgent) int { return cmp.Compare(a.Identity.LocalID, b.Identity.LocalID) })
	return p
}

func unavailable(r RuntimeBinding, agentEnabled bool) Availability {
	switch {
	case !agentEnabled:
		return Availability{Reason: "definition_disabled"}
	case !r.Enabled:
		return Availability{Reason: "binding_disabled"}
	case r.Kind == HostedManager:
		return Availability{Reason: "isolation_not_verified"}
	default:
		return Availability{Reason: "runtime_not_verified"}
	}
}

// CanonicalPublic fixes the initial public format's struct-field and set ordering;
// it does not claim RFC 8785 interoperability. The digest covers only public
// bytes and is not an identity or proof of private runtime snapshot equality.
func (d *Definitions) CanonicalPublic() ([]byte, string, error) {
	b, err := json.Marshal(d.Public())
	if err != nil {
		return nil, "", invalid("projection_error", "$")
	}
	sum := sha256.Sum256(append([]byte("tend-agent-public-v1\n"), b...))
	return b, "sha256:" + hex.EncodeToString(sum[:]), nil
}
