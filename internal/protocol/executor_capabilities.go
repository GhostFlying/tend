package protocol

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// CapabilityExecutorDeclarations negotiates declaration-based scheduling.
// Runners without this contract never receive capability-constrained work.
const CapabilityExecutorDeclarations = "executor-capability-declarations-v1"

type CapabilityReference struct {
	CapabilityID       string `json:"capability_id"`
	DefinitionRevision int64  `json:"definition_revision"`
}

// CanonicalCapabilityReferences rejects ambiguous sets and detaches the caller's
// memory. Empty sets stay nil so historical spec and inventory hashes stay stable.
func CanonicalCapabilityReferences(refs []CapabilityReference) ([]CapabilityReference, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > 64 {
		return nil, Errorf(CodeInvalidRequest, "too many capability references")
	}
	out := slices.Clone(refs)
	slices.SortFunc(out, func(a, b CapabilityReference) int { return cmp.Compare(a.CapabilityID, b.CapabilityID) })
	for i, r := range out {
		if !AgentInventoryID(r.CapabilityID) || r.DefinitionRevision < 1 || i > 0 && out[i-1].CapabilityID == r.CapabilityID {
			return nil, Errorf(CodeInvalidRequest, "capability references require unique identities and explicit revisions")
		}
	}
	return out, nil
}

func CapabilityReferencesEqual(a, b []CapabilityReference) bool {
	x, e1 := CanonicalCapabilityReferences(a)
	y, e2 := CanonicalCapabilityReferences(b)
	return e1 == nil && e2 == nil && slices.Equal(x, y)
}

func CapabilityRequiredDigest(refs []CapabilityReference) string {
	canonical, err := CanonicalCapabilityReferences(refs)
	if err != nil {
		return ""
	}
	b, _ := json.Marshal(canonical)
	sum := sha256.Sum256(append([]byte("tend-required-capabilities-v1\n"), b...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type CapabilityDefinition struct {
	CapabilityID       string `json:"capability_id"`
	Revision           int64  `json:"revision"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	State              string `json:"state"`
	ContractVersion    int64  `json:"contract_version"`
	VerificationPolicy string `json:"verification_policy"`
}

type PutCapabilityPayload struct {
	Title              string `json:"title"`
	Description        string `json:"description"`
	State              string `json:"state"`
	ContractVersion    int64  `json:"contract_version"`
	VerificationPolicy string `json:"verification_policy"`
}

func (p PutCapabilityPayload) Validate() error {
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 200 || len(p.Description) > 16000 || !slices.Contains([]string{"active", "retired"}, p.State) || p.ContractVersion != 1 {
		return Errorf(CodeInvalidRequest, "invalid capability definition")
	}
	if p.VerificationPolicy != "declaration_v1" {
		return Errorf(CodeInvalidRequest, "only declaration_v1 is supported")
	}
	return nil
}

type CapabilityReason struct {
	Capability CapabilityReference `json:"capability"`
	State      string              `json:"state"`
	Reason     string              `json:"reason"`
	Recovery   string              `json:"recovery"`
}

type CapabilityMatch struct {
	State           string                `json:"state"`
	Required        []CapabilityReference `json:"required"`
	Reasons         []CapabilityReason    `json:"reasons"`
	CheckedAtMillis int64                 `json:"checked_at_ms"`
}

// MatchExecutorCapabilities checks explicit declarations, including their exact
// definition revisions. The caller binds declared
// to the immutable Agent revision of pin and checks ordinary executor readiness.
func MatchExecutorCapabilities(_ RunExecutionPin, required, declared []CapabilityReference, now int64) CapabilityMatch {
	m := CapabilityMatch{State: "eligible", Required: slices.Clone(required), Reasons: []CapabilityReason{}, CheckedAtMillis: now}
	for _, ref := range required {
		reason := CapabilityReason{Capability: ref, State: "eligible", Reason: "capability_declared", Recovery: "configuration declaration only; actual tool execution is not verified"}
		if !slices.Contains(declared, ref) {
			reason.State, reason.Reason, reason.Recovery = "unavailable", "capability_undeclared", "explicitly declare this definition revision and register the Agent configuration"
			if slices.ContainsFunc(declared, func(d CapabilityReference) bool { return d.CapabilityID == ref.CapabilityID }) {
				reason.Reason = "capability_revision_mismatch"
			}
			m.State = "unavailable"
		}
		m.Reasons = append(m.Reasons, reason)
	}
	return m
}

type RunCapabilityBinding struct {
	MatchPolicy    string                `json:"match_policy,omitempty"`
	CurrentMatch   *CapabilityMatch      `json:"current_match,omitempty"`
	Required       []CapabilityReference `json:"required"`
	RequiredSHA256 string                `json:"required_sha256"`
	Pin            RunExecutionPin       `json:"pin"`
	BoundAtMillis  int64                 `json:"bound_at_ms"`
}

type CapabilityPage struct {
	Items []CapabilityDefinition `json:"items"`
	Next  string                 `json:"next_after,omitempty"`
	AsOf  int64                  `json:"as_of"`
}

type AgentCapabilityView struct {
	SchedulingReason   *QueueWaitReason             `json:"scheduling_reason,omitempty"`
	Binding            AgentRuntimeReference        `json:"runtime_binding_ref"`
	Categories         []string                     `json:"categories"`
	Purposes           []string                     `json:"purposes"`
	Identity           AgentIdentity                `json:"identity"`
	AgentRevision      uint64                       `json:"agent_revision"`
	Declared           []CapabilityReference        `json:"declared"`
	DefinitionStatuses []CapabilityDefinitionStatus `json:"definition_statuses,omitempty"`
	Pin                *RunExecutionPin             `json:"pin,omitempty"`
	Current            CapabilityMatch              `json:"current"`
}

// CapabilityDefinitionStatus separates the current definition from an immutable
// declaration and its frozen matching semantics. It does not change admission.
type CapabilityDefinitionStatus struct {
	Declared          CapabilityReference   `json:"declared"`
	CurrentDefinition *CapabilityDefinition `json:"current_definition,omitempty"`
	Explanation       string                `json:"explanation"`
}

type AgentCapabilityPage struct {
	Items []AgentCapabilityView `json:"items"`
	Next  string                `json:"next_after,omitempty"`
	AsOf  int64                 `json:"as_of"`
}

type AgentCapabilityQuery struct {
	Category    string `json:"category,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
	Environment string `json:"environment,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
	Capability  string `json:"capability,omitempty"`
	State       string `json:"state,omitempty"`
	After       string `json:"after,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type TaskCapabilityExplanation struct {
	Next            string                `json:"next_after,omitempty"`
	AsOf            int64                 `json:"as_of"`
	TaskID          string                `json:"task_id"`
	SpecVersion     int64                 `json:"spec_version"`
	Role            RunRole               `json:"role"`
	Required        []CapabilityReference `json:"required"`
	State           string                `json:"state"`
	Candidates      []AgentCapabilityView `json:"candidates"`
	Reasons         []string              `json:"reasons"`
	CheckedAtMillis int64                 `json:"checked_at_ms"`
	DeadlineMillis  int64                 `json:"deadline_ms"`
	Recovery        string                `json:"recovery"`
}

// AgentCapabilityRevision is a historical configuration declaration, without
// any claim about the availability of its executor or native tools.
type AgentCapabilityRevision struct {
	Definition        AgentInventoryDefinition `json:"definition"`
	InventoryRevision int64                    `json:"inventory_revision"`
	ServerRevision    int64                    `json:"server_revision"`
	SnapshotID        string                   `json:"snapshot_id"`
	PublicSHA256      string                   `json:"public_sha256"`
}
type AgentCapabilityRevisionPage struct {
	Items []AgentCapabilityRevision `json:"items"`
	Next  string                    `json:"next_after,omitempty"`
	AsOf  int64                     `json:"as_of"`
}
