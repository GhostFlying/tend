package protocol

import "encoding/json"

// Read views: the shapes in which the main agent's tools and the owner API
// show the ledger. Both use the same views so they cannot drift apart.

// TaskSummary is a task as listed.
type TaskSummary struct {
	TaskID          string        `json:"task_id"`
	Title           string        `json:"title"`
	Lifecycle       TaskLifecycle `json:"lifecycle"`
	ControlMode     ControlMode   `json:"control_mode"`
	ControlRevision int64         `json:"control_revision"`
	SpecVersion     int64         `json:"spec_version"`
	Priority        int64         `json:"priority"`
	BlockReason     BlockReason   `json:"block_reason"`
	Workspace       string        `json:"workspace"`
	// NextImplementGeneration is the generation a retry of the
	// implementation must name.
	NextImplementGeneration int64 `json:"next_implement_generation"`
}

// TaskDetail is one task in full.
type TaskDetail struct {
	TaskSummary
	Spec        json.RawMessage  `json:"spec"`
	Actions     []ActionView     `json:"actions"`
	Runs        []RunView        `json:"runs"`
	Artifacts   []ArtifactView   `json:"artifacts"`
	Acceptances []AcceptanceView `json:"acceptances"`
	Decisions   []DecisionView   `json:"decisions"`
}

// ActionView is a plan action of a task.
type ActionView struct {
	ActionID    string       `json:"action_id"`
	Kind        ActionKind   `json:"kind"`
	Status      ActionStatus `json:"status"`
	Generation  int64        `json:"generation"`
	SpecVersion int64        `json:"spec_version"`
	ArtifactID  string       `json:"artifact_id,omitempty"`
}

// RunView is a run of a task.
type RunView struct {
	RunID      string      `json:"run_id"`
	Role       RunRole     `json:"role"`
	Generation int64       `json:"generation"`
	Host       string      `json:"host"`
	Observed   RunObserved `json:"observed"`
}

// ArtifactView is an artifact a run submitted.
type ArtifactView struct {
	ArtifactID string          `json:"artifact_id"`
	RunID      string          `json:"run_id"`
	Ref        json.RawMessage `json:"ref"`
}

// AcceptanceView is a verdict on an artifact.
type AcceptanceView struct {
	ArtifactID  string          `json:"artifact_id"`
	SpecVersion int64           `json:"spec_version"`
	Verdict     Verdict         `json:"verdict"`
	Issues      json.RawMessage `json:"issues"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
}

// DecisionView is a question for the owner.
type DecisionView struct {
	DecisionID string          `json:"decision_id"`
	Question   string          `json:"question"`
	Options    json.RawMessage `json:"options"`
	Version    int64           `json:"version"`
	Status     string          `json:"status"`
	Answer     json.RawMessage `json:"answer,omitempty"`
}

// ArtifactDetail is an artifact with every verdict on it.
type ArtifactDetail struct {
	Artifact    ArtifactView     `json:"artifact"`
	TaskID      string           `json:"task_id"`
	Acceptances []AcceptanceView `json:"acceptances"`
}

// RequestView is the recorded outcome of a request.
type RequestView struct {
	RequestID string          `json:"request_id"`
	Operation string          `json:"operation"`
	Status    OpStatus        `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}
