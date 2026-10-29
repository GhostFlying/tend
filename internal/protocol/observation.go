package protocol

const CapabilityWorkerObservation = "worker-observation-v1"

// ObservationScope is explicit permission in an immutable task spec.
type ObservationScope struct {
	WorkItemIDs []string `json:"work_item_ids,omitempty"`
	Deployment  bool     `json:"deployment,omitempty"`
}

// RunObservation is a live ledger snapshot, not browser or filesystem evidence.
type RunObservation struct {
	Version     int64               `json:"version"`
	Seq         int64               `json:"seq"`
	ObservedAt  int64               `json:"observed_at"`
	RunID       string              `json:"run_id"`
	TaskID      string              `json:"task_id"`
	SpecVersion int64               `json:"spec_version"`
	WorkItems   []ObservedWorkItem  `json:"work_items"`
	Deployment  *ObservedDeployment `json:"deployment,omitempty"`
}

type ObservedWorkItem struct {
	Execution      *ExecutionCounts     `json:"execution,omitempty"`
	StageExecution []StageExecutionView `json:"stage_execution,omitempty"`
	FollowOns      []ObservedFollowOn   `json:"follow_ons"`
	WorkItemID     string               `json:"work_item_id"`
	Revision       int64                `json:"revision"`
	Status         WorkItemStatus       `json:"status"`
	Column         string               `json:"column"`
	UpdatedAt      int64                `json:"updated_at"`
	Stages         []ObservedStage      `json:"stages"`
	Tasks          []ObservedTask       `json:"tasks"`
}

type ObservedStage struct {
	StageID string           `json:"stage_id"`
	Status  IssueStageStatus `json:"status"`
	TaskIDs []string         `json:"task_ids"`
}

type ObservedTask struct {
	Runs            []ObservedRun  `json:"runs"`
	TaskID          string         `json:"task_id"`
	Lifecycle       TaskLifecycle  `json:"lifecycle"`
	ControlMode     ControlMode    `json:"control_mode"`
	ControlRevision int64          `json:"control_revision"`
	SpecVersion     int64          `json:"spec_version"`
	BlockReason     BlockReason    `json:"block_reason"`
	Execution       ExecutionState `json:"execution"`
	RunID           string         `json:"run_id,omitempty"`
	Role            RunRole        `json:"role,omitempty"`
}

// ObservedDeployment separates actual process facts from historical release state.
type ObservedDeployment struct {
	Runner         *ObservedRunner  `json:"runner,omitempty"`
	ObservedAt     int64            `json:"observed_at"`
	SchemaVersion  int64            `json:"schema_version"`
	ActualServer   *ObservedServer  `json:"actual_server,omitempty"`
	ProofStatus    string           `json:"proof_status"`
	CommittedProof *UpdaterReceipt  `json:"committed_proof,omitempty"`
	LatestUpgrade  *ObservedUpgrade `json:"latest_upgrade,omitempty"`
}

type ObservedServer struct {
	BinarySHA256      string `json:"binary_sha256"`
	Version           string `json:"version,omitempty"`
	Commit            string `json:"commit,omitempty"`
	Modified          bool   `json:"modified"`
	ProcessInstanceID string `json:"process_instance_id"`
	PID               int64  `json:"pid"`
}

type ObservedUpgrade struct {
	UpgradeID      string                `json:"upgrade_id"`
	ReleaseID      string                `json:"release_id"`
	ManifestSHA256 string                `json:"manifest_sha256"`
	Revision       int64                 `json:"revision"`
	Status         string                `json:"status"`
	Phase          string                `json:"phase"`
	Hosts          []ObservedUpgradeHost `json:"hosts"`
}

type ObservedUpgradeHost struct {
	HostID string `json:"host_id"`
	Status string `json:"status"`
	Phase  string `json:"phase"`
}

// ObservedFollowOn omits prose, sources and evidence from the responsibility ledger.
type ObservedFollowOn struct {
	FollowOnID string              `json:"follow_on_id"`
	Kind       IssueFollowOnKind   `json:"kind"`
	Status     IssueFollowOnStatus `json:"status"`
}

// ObservedRun preserves raw execution state separately from task projections.
type ObservedRun struct {
	RunID         string      `json:"run_id"`
	Role          RunRole     `json:"role"`
	DesiredState  RunDesired  `json:"desired_state"`
	ObservedState RunObserved `json:"observed_state"`
}

// ObservedRunner is the authenticated heartbeat of this run's own host only.
// It is a server-side observation, not a direct process inspection.
type ObservedRunner struct {
	HostID                 string      `json:"host_id"`
	InstanceID             string      `json:"instance_id"`
	Online                 bool        `json:"online"`
	LastHeartbeatAt        int64       `json:"last_heartbeat_at"`
	Mode                   string      `json:"mode"`
	ControlMode            string      `json:"control_mode"`
	ControlRevision        int64       `json:"control_revision"`
	AppliedControlRevision int64       `json:"applied_control_revision"`
	Build                  RunnerBuild `json:"build"`
	Capabilities           []string    `json:"capabilities"`
}
