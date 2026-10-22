package protocol

import "encoding/json"

// Runner protocol operations. The runner sends the runner.* operations with
// its host credential and the runs.* and acceptances.* operations with the
// credential of the run they concern.
const (
	OpRunnerHello         = "runner.hello"
	OpRunnerClaim         = "runner.claim"
	OpRunnerHeartbeat     = "runner.heartbeat"
	OpRunnerEvents        = "runner.events"
	OpRunsReportWorkspace = "runs.report_workspace"
	OpRunsReportStarted   = "runs.report_started"
	OpRunsSubmitArtifact  = "runs.submit_artifact"
	OpAcceptancesSubmit   = "acceptances.submit"
)

// HeldRun is a run the runner's journal says it holds. Observed is the
// runner's last knowledge of it; terminal states are only established by
// submitting the run's result or releasing it, so a run whose result is
// still to be delivered is not reported as finished.
type HeldRun struct {
	RunID    string      `json:"run_id"`
	LeaseID  string      `json:"lease_id"`
	Observed RunObserved `json:"observed"`
}

// HelloPayload opens a runner session. The runner reports every run it
// holds before claiming new work.
type HelloPayload struct {
	WorkspaceProtocol int64     `json:"workspace_protocol,omitempty"`
	Held              []HeldRun `json:"held"`
	// Version is the runner build, for diagnostics.
	Version string `json:"version,omitempty"`
	// Worker is the harness the runner runs; it must be the one the
	// server's configuration names for the host (D64). Runners from
	// before D64 leave it empty and run the app-server.
	Worker WorkerKind `json:"worker,omitempty"`
}

// ClaimPayload asks for the next run. Worker is as in HelloPayload, so a
// host whose configured worker changed while its runner kept running
// does not take work it would run with the wrong harness.
type ClaimPayload struct {
	WorkspaceProtocol int64      `json:"workspace_protocol,omitempty"`
	Worker            WorkerKind `json:"worker,omitempty"`
}

// Reconciliation verdicts for held runs.
const (
	// VerdictContinue means the run is current and its lease is active.
	VerdictContinue = "continue"
	// VerdictStop means the runner must stop the run and drop it.
	VerdictStop = "stop"
)

// RunVerdict tells the runner what to do with a run it reported.
type RunVerdict struct {
	RunID   string     `json:"run_id"`
	Verdict string     `json:"verdict"`
	Desired RunDesired `json:"desired"`
	Reason  string     `json:"reason,omitempty"`
}

// HelloResult answers a hello. Reassigned lists runs allocated to the host
// that the runner did not report; they never started, so the runner takes
// them over as if it had just claimed them.
type HelloResult struct {
	// LeaseTTLMillis is the lease duration tend currently grants.
	LeaseTTLMillis int64        `json:"lease_ttl_ms"`
	Runs           []RunVerdict `json:"runs"`
	Reassigned     []Assignment `json:"reassigned"`
}

// Assignment hands a run to a runner.
type Assignment struct {
	RunID          string `json:"run_id"`
	LeaseID        string `json:"lease_id"`
	LeaseTTLMillis int64  `json:"lease_ttl_ms"`
	// RunToken authenticates the run's own requests. It is derived from
	// the lease when the assignment is sent and never stored.
	RunToken string  `json:"run_token,omitempty"`
	TaskID   string  `json:"task_id"`
	ActionID string  `json:"action_id"`
	Role     RunRole `json:"role"`
	// Desired is what tend wants the run to do now. A paused run is
	// handed over but must not start until a heartbeat says run.
	Desired     RunDesired `json:"desired"`
	Generation  int64      `json:"generation"`
	SpecVersion int64      `json:"spec_version"`
	// Spec is the immutable task spec version the run works on.
	Relations []TaskRelationView  `json:"relations,omitempty"`
	Spec      json.RawMessage     `json:"spec"`
	Workspace AssignmentWorkspace `json:"workspace"`
	// Artifact is the artifact reference an acceptance run judges.
	Artifact json.RawMessage `json:"artifact,omitempty"`
	// Feedback is what the last acceptance of the same spec version asked
	// to change, for an implementation after a changes-required verdict.
	Feedback *Feedback `json:"feedback,omitempty"`
}

// Feedback is an acceptance verdict an implementation must address.
type Feedback struct {
	Verdict  Verdict         `json:"verdict"`
	Issues   json.RawMessage `json:"issues"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

// AssignmentWorkspace tells the runner how to lay out the workspace.
type AssignmentWorkspace struct {
	ID                 string          `json:"id,omitempty"`
	Kind               string          `json:"kind,omitempty"`
	Reused             bool            `json:"reused,omitempty"`
	TaskTitle          string          `json:"task_title,omitempty"`
	ShortTaskID        string          `json:"short_task_id,omitempty"`
	ProviderID         string          `json:"provider_id,omitempty"`
	ProviderInstanceID string          `json:"provider_instance_id,omitempty"`
	Path               string          `json:"path,omitempty"`
	Baseline           json.RawMessage `json:"baseline,omitempty"`
	Name               string          `json:"name"`
	Branch             string          `json:"branch"`
	Profile            string          `json:"profile,omitempty"`
	Mounts             []AssignedMount `json:"mounts"`
}

// AssignedMount is one repository of an assigned workspace.
type AssignedMount struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	RemoteURL string `json:"remote_url"`
	// BaseBranch or BaseCommit pin the base; when both are empty the
	// runner tries BaseCandidates and then its own heuristics.
	BaseBranch     string   `json:"base_branch,omitempty"`
	BaseCommit     string   `json:"base_commit,omitempty"`
	BaseCandidates []string `json:"base_candidates,omitempty"`
	Profile        string   `json:"profile,omitempty"`
}

// HeartbeatPayload renews the leases of the runs a runner holds.
type HeartbeatPayload struct {
	Runs []LeaseRef `json:"runs"`
}

// LeaseRef names a run and the lease the runner holds for it.
type LeaseRef struct {
	RunID   string `json:"run_id"`
	LeaseID string `json:"lease_id"`
}

// HeartbeatResult reports, per run, whether the lease was renewed and what
// tend wants the run to do.
type HeartbeatResult struct {
	LeaseTTLMillis int64        `json:"lease_ttl_ms"`
	Runs           []LeaseState `json:"runs"`
}

// LeaseState is the outcome of renewing one lease. A lease that is no
// longer active is not renewed; the runner must fence itself and reconcile
// through hello.
type LeaseState struct {
	RunID   string     `json:"run_id"`
	Renewed bool       `json:"renewed"`
	Desired RunDesired `json:"desired"`
}

// RunEvent is a fact a runner observed about a run. Seq is assigned by the
// runner, starts at 1 and never changes for the same fact.
type RunEvent struct {
	Seq      int64           `json:"seq"`
	NativeID string          `json:"native_id,omitempty"`
	Kind     string          `json:"kind"`
	Body     json.RawMessage `json:"body,omitempty"`
}

// Run event kinds that change the run's observed state. Other kinds are
// kept as run logs.
const (
	EventRunRunning = "run.running"
	EventRunWaiting = "run.waiting"
)

// EventsPayload uploads a batch of run events.
type EventsPayload struct {
	Events []RunEvent `json:"events"`
}

// EventsResult acknowledges an upload.
type EventsResult struct {
	// Accepted counts events seen for the first time.
	Accepted int `json:"accepted"`
}

// WorkspaceReport records how the runner laid out a run's workspace.
type WorkspaceReport struct {
	ProviderID         string          `json:"provider_id,omitempty"`
	ProviderInstanceID string          `json:"provider_instance_id,omitempty"`
	Path               string          `json:"path,omitempty"`
	Baseline           json.RawMessage `json:"baseline,omitempty"`
	Mounts             []ResolvedMount `json:"mounts"`
}

// ResolvedMount is the fact of where one mount started.
type ResolvedMount struct {
	Path            string `json:"path"`
	ResolvedBaseSHA string `json:"resolved_base_sha"`
	// ResolvedFrom names the explicit base or the candidate that matched,
	// for example "commit", "branch:release/3.2" or "candidate:main".
	ResolvedFrom string `json:"resolved_from"`
	Branch       string `json:"branch"`
}

// Further run operations.
const (
	// OpRunsRelease gives up a run that will not produce a result, for
	// example after a stop verdict. The run ends as failed.
	OpRunsRelease = "runs.release"
	// OpRunsReportTakeover reports that the owner took over the run's
	// agent (D67).
	OpRunsReportTakeover = "runs.report_takeover"
)

// TakeoverPayload says how the runner noticed a takeover.
type TakeoverPayload struct {
	Reason string `json:"reason"`
}

// ArtifactPayload submits immutable outputs of an implement run. Commit
// deliverables list every mount; reports and files are independently optional.
type ArtifactPayload struct {
	Mounts []ArtifactMount `json:"mounts"`
	Report *ResultReport   `json:"report,omitempty"`
	Files  []ArtifactFile  `json:"files,omitempty"`
}

// ArtifactMount is one repository's state in an artifact.
type ArtifactMount struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	BaseSHA string `json:"base_sha"`
	// Branch is the branch HEAD was on when the run ended, empty when
	// detached; CommitSHA is that HEAD (D57).
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha"`
	// SnapshotSHA is a commit on top of CommitSHA holding the working tree
	// the run left, when it held changes HEAD does not; empty otherwise.
	SnapshotSHA string `json:"snapshot_sha,omitempty"`
	Changed     bool   `json:"changed"`
}

// VerdictPayload submits the conclusion of an acceptance run.
type VerdictPayload struct {
	Verdict  Verdict         `json:"verdict"`
	Evidence json.RawMessage `json:"evidence"`
	Issues   json.RawMessage `json:"issues,omitempty"`
}

// ReleasePayload explains why a run is given up.
type ReleasePayload struct {
	Reason string `json:"reason"`
}

// ResultReport is a durable Markdown investigation or operation report.
type ResultReport struct {
	Body          string   `json:"body"`
	Evidence      []string `json:"evidence,omitempty"`
	Uncertainties []string `json:"uncertainties,omitempty"`
}

// ArtifactFile identifies a durable immutable uploaded file.
type ArtifactFile struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
