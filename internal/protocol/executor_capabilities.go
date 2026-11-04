package protocol

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// CapabilityExecutorEvidence is a wire feature, not an execution capability.
const CapabilityExecutorEvidence = "executor-capabilities-v1"
const OpRunnerCapabilityEvidence = "runner.capability_evidence"
const CapabilityEvidenceTTLMillis int64 = 300000

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
	ToolName           string `json:"tool_name"`
}

type PutCapabilityPayload struct {
	Title              string `json:"title"`
	Description        string `json:"description"`
	State              string `json:"state"`
	ContractVersion    int64  `json:"contract_version"`
	VerificationPolicy string `json:"verification_policy"`
	ToolName           string `json:"tool_name"`
}

func (p PutCapabilityPayload) Validate() error {
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 200 || len(p.Description) > 16000 || !slices.Contains([]string{"active", "retired"}, p.State) || p.ContractVersion != 1 || p.VerificationPolicy != "tool_inventory_v1" || !AgentInventoryID(p.ToolName) {
		return Errorf(CodeInvalidRequest, "invalid capability definition or unsupported verification contract")
	}
	return nil
}

type CapabilityToolSource struct {
	Kind                string `json:"kind"`
	ID                  string `json:"id"`
	Version             string `json:"version"`
	ToolName            string `json:"tool_name"`
	SchemaSHA256        string `json:"schema_sha256"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	ExecutableSHA256    string `json:"executable_sha256"`
}

// ExecutorCapabilityEvidence is an immutable adapter observation. Profile scope
// requires an authoritative adapter contract; observing a session cannot grant
// profile scope. Execution outcomes never replace the inventory qualification.
type ExecutorCapabilityEvidence struct {
	EvidenceID       string                `json:"evidence_id"`
	RequestID        string                `json:"request_id"`
	Capability       CapabilityReference   `json:"capability"`
	Pin              RunExecutionPin       `json:"pin"`
	InstanceID       string                `json:"instance_id"`
	Generation       int64                 `json:"generation"`
	Sequence         int64                 `json:"sequence"`
	Kind             string                `json:"kind"`
	Scope            string                `json:"scope"`
	SessionID        string                `json:"session_id,omitempty"`
	Complete         bool                  `json:"complete"`
	State            string                `json:"state"`
	Reason           string                `json:"reason"`
	Source           *CapabilityToolSource `json:"source,omitempty"`
	ObservedAtMillis int64                 `json:"observed_at_ms"`
	ExpiresAtMillis  int64                 `json:"expires_at_ms"`
	RecordedAtMillis int64                 `json:"recorded_at_ms"`
	OutputSHA256     string                `json:"output_sha256,omitempty"`
	CostState        string                `json:"cost_state,omitempty"`
}

func CapabilityDigestValid(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

func (e ExecutorCapabilityEvidence) Validate(now int64) error {
	_, err := CanonicalCapabilityReferences([]CapabilityReference{e.Capability})
	if err != nil {
		return err
	}
	if e.Pin.Validate(e.Pin.Identity.ExecutionEnvironmentID) != nil || e.Pin.Purpose != "" || e.InstanceID == "" || e.Generation < 1 || e.Sequence < 1 || !slices.Contains([]string{"tool_inventory", "execution", "simulation"}, e.Kind) || !slices.Contains([]string{"profile", "session"}, e.Scope) || e.Scope == "session" && e.SessionID == "" || e.Scope == "profile" && e.SessionID != "" || !slices.Contains([]string{"unverified", "confirmed", "unavailable"}, e.State) || e.ObservedAtMillis <= 0 || e.ObservedAtMillis > now || e.ExpiresAtMillis <= e.ObservedAtMillis || e.ExpiresAtMillis-e.ObservedAtMillis > CapabilityEvidenceTTLMillis {
		return Errorf(CodeInvalidRequest, "invalid capability evidence binding, state or lifetime")
	}
	if !slices.Contains([]string{"tool_exposed", "tool_absent", "adapter_evidence_unsupported", "enumeration_incomplete", "probe_timeout", "session_ended", "execution_succeeded", "execution_failed", "execution_unknown"}, e.Reason) {
		return Errorf(CodeInvalidRequest, "unsupported capability evidence reason")
	}
	if e.Kind == "simulation" && e.State == "confirmed" || e.State == "confirmed" && (!e.Complete || e.Source == nil) || e.Reason == "tool_absent" && (!e.Complete || e.State != "unavailable") || e.Reason == "tool_exposed" && (e.Kind != "tool_inventory" || e.State != "confirmed") || e.State == "confirmed" && e.Kind == "tool_inventory" && e.Reason != "tool_exposed" {
		return Errorf(CodeInvalidRequest, "evidence does not establish its claimed state")
	}
	if s := e.Source; s != nil {
		if !slices.Contains([]string{"native", "plugin", "mcp"}, s.Kind) || !AgentInventoryID(s.ID) || !AgentInventoryID(s.Version) || !AgentInventoryID(s.ToolName) || !CapabilityDigestValid(s.SchemaSHA256) || !CapabilityDigestValid(s.ConfigurationSHA256) || !CapabilityDigestValid(s.ExecutableSHA256) {
			return Errorf(CodeInvalidRequest, "capability evidence requires versioned tool provenance")
		}
	}
	if e.Kind == "execution" && !slices.Contains([]string{"execution_succeeded", "execution_failed", "execution_unknown"}, e.Reason) {
		return Errorf(CodeInvalidRequest, "execution evidence requires an execution outcome")
	}
	if e.Kind == "execution" && e.Reason == "execution_succeeded" && (!CapabilityDigestValid(e.OutputSHA256) || e.CostState != "observed" && e.CostState != "unknown") {
		return Errorf(CodeInvalidRequest, "execution success requires output integrity and cost state")
	}
	return nil
}

type ReportCapabilityEvidencePayload struct {
	Runtime  *RunnerRuntime             `json:"runtime"`
	Evidence ExecutorCapabilityEvidence `json:"evidence"`
}

type CapabilityReason struct {
	Capability      CapabilityReference `json:"capability"`
	State           string              `json:"state"`
	Reason          string              `json:"reason"`
	EvidenceID      string              `json:"evidence_id,omitempty"`
	ExpiresAtMillis int64               `json:"expires_at_ms,omitempty"`
	Recovery        string              `json:"recovery"`
}

type CapabilityMatch struct {
	State           string                `json:"state"`
	Required        []CapabilityReference `json:"required"`
	Reasons         []CapabilityReason    `json:"reasons"`
	EvidenceIDs     []string              `json:"evidence_ids"`
	CheckedAtMillis int64                 `json:"checked_at_ms"`
}

// MatchExecutorCapabilities is shared by scheduling and read-only explanations.
// No inference from a CLI/model/category, heartbeat, or execution success is made.
func MatchExecutorCapabilities(pin RunExecutionPin, required, declared []CapabilityReference, evidence []ExecutorCapabilityEvidence, now int64) CapabilityMatch {
	m := CapabilityMatch{State: "eligible", Required: slices.Clone(required), Reasons: []CapabilityReason{}, EvidenceIDs: []string{}, CheckedAtMillis: now}
	for _, ref := range required {
		r := CapabilityReason{Capability: ref, State: "pending", Reason: "evidence_unverified", Recovery: "verify the exact configured executor once; reconcile any unknown original request"}
		var latest *ExecutorCapabilityEvidence
		for i := range evidence {
			e := &evidence[i]
			if e.Capability == ref && e.Pin == pin && e.Kind != "execution" && (latest == nil || e.Generation > latest.Generation || e.Generation == latest.Generation && e.Sequence > latest.Sequence) {
				latest = e
			}
		}
		if !slices.Contains(declared, ref) {
			r.State, r.Reason, r.Recovery = "unavailable", "capability_undeclared", "revise and register this Agent declaration explicitly"
		} else if latest != nil {
			e := *latest
			r.EvidenceID, r.ExpiresAtMillis = e.EvidenceID, e.ExpiresAtMillis
			switch {
			case e.Validate(now) != nil:
				r.Reason = "evidence_invalid"
			case e.ExpiresAtMillis <= now:
				r.Reason = "evidence_expired"
			case e.Kind == "simulation":
				r.Reason = "simulation_only"
			case e.Scope != "profile":
				r.State, r.Reason = "unavailable", "session_adoption_unsupported"
			case e.State == "unavailable":
				r.State, r.Reason = "unavailable", e.Reason
			case e.State == "unverified":
				r.Reason = e.Reason
				if e.Reason == "adapter_evidence_unsupported" {
					r.State = "unavailable"
				}
			case e.State == "confirmed" && e.Kind == "tool_inventory" && e.Reason == "tool_exposed":
				r.State, r.Reason, r.Recovery = "eligible", "tool_exposed", "recheck the same binding before launch"
				m.EvidenceIDs = append(m.EvidenceIDs, e.EvidenceID)
			}
		}
		m.Reasons = append(m.Reasons, r)
		if r.State == "unavailable" {
			m.State = "unavailable"
		} else if r.State == "pending" && m.State == "eligible" {
			m.State = "pending"
		}
	}
	return m
}

type RunCapabilityRevalidation struct {
	RequestID        string                       `json:"request_id"`
	Pin              RunExecutionPin              `json:"pin"`
	RequiredSHA256   string                       `json:"required_sha256"`
	EvidenceIDs      []string                     `json:"evidence_ids"`
	Evidence         []ExecutorCapabilityEvidence `json:"evidence"`
	ObservedAtMillis int64                        `json:"observed_at_ms"`
}

type RunCapabilityBinding struct {
	CurrentMatch   *CapabilityMatch             `json:"current_match,omitempty"`
	Revalidation   *RunCapabilityRevalidation   `json:"revalidation,omitempty"`
	Evidence       []ExecutorCapabilityEvidence `json:"evidence"`
	Required       []CapabilityReference        `json:"required"`
	RequiredSHA256 string                       `json:"required_sha256"`
	EvidenceIDs    []string                     `json:"evidence_ids"`
	Pin            RunExecutionPin              `json:"pin"`
	BoundAtMillis  int64                        `json:"bound_at_ms"`
}

type CapabilityPage struct {
	Items []CapabilityDefinition `json:"items"`
	Next  string                 `json:"next_after,omitempty"`
	AsOf  int64                  `json:"as_of"`
}

type CapabilityEvidencePage struct {
	Items []ExecutorCapabilityEvidence `json:"items"`
	Next  string                       `json:"next_after,omitempty"`
	AsOf  int64                        `json:"as_of"`
}

type AgentCapabilityView struct {
	SchedulingReason *QueueWaitReason             `json:"scheduling_reason,omitempty"`
	Binding          AgentRuntimeReference        `json:"runtime_binding_ref"`
	Categories       []string                     `json:"categories"`
	Purposes         []string                     `json:"purposes"`
	Identity         AgentIdentity                `json:"identity"`
	AgentRevision    uint64                       `json:"agent_revision"`
	Declared         []CapabilityReference        `json:"declared"`
	Pin              *RunExecutionPin             `json:"pin,omitempty"`
	Current          CapabilityMatch              `json:"current"`
	Evidence         []ExecutorCapabilityEvidence `json:"evidence"`
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

type VerifyLocalCapabilityRequest struct {
	RequestID        string              `json:"request_id"`
	ExpectedRevision int64               `json:"expected_revision"`
	Pin              RunExecutionPin     `json:"pin"`
	Capability       CapabilityReference `json:"capability"`
	ProbeKind        string              `json:"probe_kind"`
	TimeoutMillis    int64               `json:"timeout_ms"`
	MaxAttempts      int                 `json:"max_attempts"`
}
type LocalCapabilityRequestStatus struct {
	ProbeInstanceID string                       `json:"probe_instance_id,omitempty"`
	ProbeGeneration int64                        `json:"probe_generation,omitempty"`
	Request         VerifyLocalCapabilityRequest `json:"request"`
	Sequence        int64                        `json:"sequence"`
	State           string                       `json:"state"`
	Evidence        *ExecutorCapabilityEvidence  `json:"evidence,omitempty"`
	Envelope        *Envelope                    `json:"envelope,omitempty"`
	ErrorCode       string                       `json:"error_code,omitempty"`
	Recovery        string                       `json:"recovery"`
}
type ReconcileLocalCapabilityRequest struct {
	RequestID string `json:"request_id"`
}
type CapabilityEvidenceRequestStatus struct {
	RequestState       string     `json:"request_state"`
	Request            *Operation `json:"request,omitempty"`
	RequestPayloadHash string     `json:"request_payload_hash,omitempty"`
}

const OpRunnerCapabilityEvidenceStatus = "runner.capability_evidence_status"
