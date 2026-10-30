package protocol

import "strings"

const CapabilityAgentExecution = "runner-agent-execution-v1"

// AgentRequirement selects metadata; the run role remains separately authorized.
type AgentRequirement struct {
	CategoryID string `json:"category_id"`
	RuntimeID  string `json:"runtime_id,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
}

type TaskAgentSelection struct {
	Implementation AgentRequirement `json:"implementation"`
	Acceptance     AgentRequirement `json:"acceptance"`
}

func (s TaskAgentSelection) Validate() error {
	for _, r := range []AgentRequirement{s.Implementation, s.Acceptance} {
		if r.Model != "" {
			if err := (ExecutionProfile{Model: r.Model, Effort: r.Effort}).Validate(); err != nil {
				return err
			}
		} else if !validExecutionEffort(r.Effort) {
			return Errorf(CodeInvalidRequest, "unsupported agent reasoning effort constraint")
		}
		if !AgentInventoryID(r.CategoryID) || (r.RuntimeID != "" && !AgentInventoryID(r.RuntimeID)) {
			return Errorf(CodeInvalidRequest, "agent selection requires valid category/runtime identities")
		}
	}
	return nil
}

// RunExecutionPin fixes one role's selected request without private configuration.
type RunExecutionPin struct {
	Purpose           string                `json:"purpose,omitempty"`
	Identity          AgentIdentity         `json:"identity"`
	AgentRevision     uint64                `json:"agent_revision"`
	Runtime           AgentRuntimeReference `json:"runtime"`
	InventoryRevision int64                 `json:"inventory_revision"`
	SnapshotID        string                `json:"snapshot_id"`
	PublicSHA256      string                `json:"public_sha256"`
	Profile           ExecutionProfile      `json:"profile"`
	Worker            WorkerKind            `json:"worker"`
	BackendID         string                `json:"backend_id,omitempty"`
}

func (p RunExecutionPin) Validate(host string) error {
	if p.Purpose != "" && p.Purpose != "issue_manager" || p.Purpose == "issue_manager" && (p.BackendID != "" || p.Worker != WorkerAppServer) {
		return Errorf(CodeInvalidRequest, "invalid execution purpose")
	}
	if p.Identity.ExecutionEnvironmentID != host || !AgentInventoryID(p.Identity.LocalID) || p.AgentRevision == 0 || !AgentInventoryID(p.Runtime.RuntimeID) || p.Runtime.Revision == 0 || p.InventoryRevision < 1 || !AgentInventoryID(p.SnapshotID) || !strings.HasPrefix(p.PublicSHA256, "sha256:") || !p.Worker.Valid() || (p.BackendID != "git" && p.BackendID != "orca" && !(p.Purpose == "issue_manager" && p.BackendID == "" && p.Worker == WorkerAppServer)) {
		return Errorf(CodeInvalidRequest, "invalid named execution pin")
	}
	return p.Profile.Validate()
}

// AgentExecutor is a current adapter observation, not a promise about model service.
type AgentExecutor struct {
	Pin       RunExecutionPin `json:"pin"`
	Available bool            `json:"available"`
	Reason    string          `json:"reason,omitempty"`
}

// MatchesProfile constrains selection without overriding local configuration.
// An omitted field adds no requirement, including the model's default effort.
func (r AgentRequirement) MatchesProfile(profile ExecutionProfile) bool {
	return (r.Model == "" || r.Model == profile.Model) && (r.Effort == "" || r.Effort == profile.Effort)
}
