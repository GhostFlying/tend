package protocol

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

const CapabilityAgentInventory = "runner-agent-inventory-v1"
const OpRunnerInventoryRegister = "runner.inventory_register"
const OpRunnerInventoryStatus = "runner.inventory_status"

type AgentSourceKind string

const (
	AgentSourceLocalFile AgentSourceKind = "local_file"
	AgentSourceInline    AgentSourceKind = "inline"
)

// AgentCategory describes a global classification, never a run permission.
type AgentCategory struct {
	CategoryID  string `json:"category_id"`
	Revision    int64  `json:"revision"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
}

type PutAgentCategoryPayload struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
}

type AgentIdentity struct {
	ExecutionEnvironmentID string `json:"execution_environment_id"`
	LocalID                string `json:"local_id"`
}

type AgentRuntimeReference struct {
	RuntimeID string `json:"runtime_id"`
	Revision  uint64 `json:"revision"`
}

type AgentInventoryAvailability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// AgentInventoryProjection matches local public format 1. No private configuration
// or executable/source references belong on this wire surface.
type AgentInventoryProjection struct {
	FormatVersion          uint64                     `json:"format_version"`
	ExecutionEnvironmentID string                     `json:"execution_environment_id"`
	SourceKind             AgentSourceKind            `json:"source_kind"`
	Runtimes               []AgentInventoryRuntime    `json:"runtimes"`
	Agents                 []AgentInventoryDefinition `json:"agents"`
}

type AgentInventoryRuntime struct {
	RuntimeID        string                     `json:"runtime_id"`
	Revision         uint64                     `json:"revision"`
	Kind             string                     `json:"kind"`
	CLI              string                     `json:"cli,omitempty"`
	DeclaredPurposes []string                   `json:"declared_purposes"`
	Enabled          bool                       `json:"enabled"`
	Registration     string                     `json:"registration"`
	Availability     AgentInventoryAvailability `json:"availability"`
}

type AgentInventoryDefinition struct {
	Identity        AgentIdentity              `json:"identity"`
	Revision        uint64                     `json:"revision"`
	DisplayName     string                     `json:"display_name"`
	Binding         AgentRuntimeReference      `json:"runtime_binding_ref"`
	Categories      []string                   `json:"categories"`
	Purposes        []string                   `json:"purposes"`
	RequestedModel  string                     `json:"requested_model"`
	RequestedEffort string                     `json:"requested_effort"`
	Enabled         bool                       `json:"enabled"`
	Registration    string                     `json:"registration"`
	Availability    AgentInventoryAvailability `json:"availability"`
}

type RegisterAgentInventoryPayload struct {
	Runtime       *RunnerRuntime           `json:"runtime"`
	LocalRevision int64                    `json:"local_revision"`
	SnapshotID    string                   `json:"snapshot_id"`
	PublicSHA256  string                   `json:"public_sha256"`
	Projection    AgentInventoryProjection `json:"projection"`
}

type AgentInventoryReceipt struct {
	HostID         string          `json:"host_id"`
	ServerRevision int64           `json:"server_revision"`
	LocalRevision  int64           `json:"local_revision"`
	SnapshotID     string          `json:"snapshot_id"`
	PublicSHA256   string          `json:"public_sha256"`
	RequestID      string          `json:"request_id"`
	State          string          `json:"state"`
	Categories     []AgentCategory `json:"categories"`
}

type AgentInventoryStatusPayload struct {
	Runtime   *RunnerRuntime `json:"runtime"`
	RequestID string         `json:"request_id,omitempty"`
}

type AgentInventoryStatus struct {
	Executors             []AgentExecutor           `json:"executors,omitempty"`
	Current               *AgentInventoryReceipt    `json:"current,omitempty"`
	Projection            *AgentInventoryProjection `json:"projection,omitempty"`
	Request               *Operation                `json:"request,omitempty"`
	RequestPayloadHash    string                    `json:"request_payload_hash,omitempty"`
	RequestState          string                    `json:"request_state"`
	Online                bool                      `json:"online"`
	LastHeartbeatAtMillis int64                     `json:"last_heartbeat_at_ms"`
}

// CanonicalAgentInventory reproduces initial local public format 1, including
// its hash domain. It normalizes sets without changing the caller's slices.
func CanonicalAgentInventory(p AgentInventoryProjection) ([]byte, string, error) {
	bad := func() ([]byte, string, error) {
		return nil, "", Errorf(CodeInvalidRequest, "invalid agent inventory projection")
	}
	if (p.FormatVersion != 1 && p.FormatVersion != 2) || !AgentInventoryID(p.ExecutionEnvironmentID) || (p.SourceKind != AgentSourceInline && p.SourceKind != AgentSourceLocalFile) {
		return bad()
	}
	p.Runtimes = append([]AgentInventoryRuntime{}, p.Runtimes...)
	p.Agents = append([]AgentInventoryDefinition{}, p.Agents...)
	runtimes := map[string]AgentInventoryRuntime{}
	for i, r := range p.Runtimes {
		if !AgentInventoryID(r.RuntimeID) || r.Revision == 0 || r.Registration != "staged" || r.Availability.Available {
			return bad()
		}
		if _, found := runtimes[r.RuntimeID]; found {
			return bad()
		}
		if r.Kind != "appserver-worker" && r.Kind != "orca-worker" && r.Kind != "hosted-manager" {
			return bad()
		}
		if (r.Kind == "orca-worker" && r.CLI != "codex" && r.CLI != "traex") || (r.Kind != "orca-worker" && r.CLI != "" && !(p.FormatVersion == 2 && r.Kind == "appserver-worker" && (r.CLI == "codex" || r.CLI == "traex"))) {
			return bad()
		}
		if p.FormatVersion == 2 && r.Kind == "appserver-worker" && slices.Contains(r.DeclaredPurposes, "issue_manager") && r.CLI == "" {
			return nil, "", Errorf(CodeInvalidRequest, "manager runtime requires its CLI identity")
		}
		if !inventoryPurposes(r.DeclaredPurposes, r.Kind, p.FormatVersion) || r.Availability.Reason != inventoryUnavailable(r, true) {
			return bad()
		}
		r.DeclaredPurposes = slices.Clone(r.DeclaredPurposes)
		slices.Sort(r.DeclaredPurposes)
		p.Runtimes[i] = r
		runtimes[r.RuntimeID] = r
	}
	seen := map[string]bool{}
	for i, a := range p.Agents {
		r, found := runtimes[a.Binding.RuntimeID]
		if a.Identity.ExecutionEnvironmentID != p.ExecutionEnvironmentID || !AgentInventoryID(a.Identity.LocalID) || seen[a.Identity.LocalID] || !found || a.Binding.Revision != r.Revision || a.Revision == 0 || strings.TrimSpace(a.DisplayName) == "" || strings.TrimSpace(a.RequestedModel) == "" || a.Registration != "staged" || a.Availability.Available || a.Availability.Reason != inventoryUnavailable(r, a.Enabled) {
			return bad()
		}
		seen[a.Identity.LocalID] = true
		if !inventoryPurposes(a.Purposes, r.Kind, p.FormatVersion) || len(a.Categories) == 0 || !uniqueInventoryStrings(a.Categories) || !slices.Contains([]string{"", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}, a.RequestedEffort) {
			return bad()
		}
		for _, purpose := range a.Purposes {
			if !slices.Contains(r.DeclaredPurposes, purpose) {
				return bad()
			}
		}
		a.Categories = slices.Clone(a.Categories)
		slices.Sort(a.Categories)
		a.Purposes = slices.Clone(a.Purposes)
		slices.Sort(a.Purposes)
		p.Agents[i] = a
	}
	slices.SortFunc(p.Runtimes, func(a, b AgentInventoryRuntime) int { return cmp.Compare(a.RuntimeID, b.RuntimeID) })
	slices.SortFunc(p.Agents, func(a, b AgentInventoryDefinition) int { return cmp.Compare(a.Identity.LocalID, b.Identity.LocalID) })
	b, err := json.Marshal(p)
	if err != nil {
		return bad()
	}
	sum := sha256.Sum256(append([]byte("tend-agent-public-v1\n"), b...))
	return b, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// AgentInventoryID follows local configuration identifiers, not process IDs.
func AgentInventoryID(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i == 0 || c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func uniqueInventoryStrings(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if !AgentInventoryID(v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func inventoryPurposes(values []string, kind string, format uint64) bool {
	if len(values) == 0 || !uniqueInventoryStrings(values) {
		return false
	}
	for _, v := range values {
		if kind == "hosted-manager" {
			if v != "main" && v != "issue_manager" && v != "consultation" {
				return false
			}
		} else if v != "worker_run" && !(format == 2 && kind == "appserver-worker" && v == "issue_manager") {
			return false
		}
	}
	return true
}

func inventoryUnavailable(r AgentInventoryRuntime, agentEnabled bool) string {
	if !agentEnabled {
		return "definition_disabled"
	}
	if !r.Enabled {
		return "binding_disabled"
	}
	if r.Kind == "hosted-manager" {
		return "isolation_not_verified"
	}
	return "runtime_not_verified"
}
