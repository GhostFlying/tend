package protocol

const CapabilityWorkerObservation = "worker-observation-v1"

// ObservationScope is explicit permission in an immutable task spec.
type ObservationScope struct {
	WorkItemIDs []string `json:"work_item_ids,omitempty"`
	Deployment  bool     `json:"deployment,omitempty"`
}

// RunObservation is a live ledger snapshot, not browser or filesystem evidence.
type RunObservation struct {
	DeliveryAuthorization *DeliveryExecutionAuthority `json:"delivery_authorization,omitempty"`
	Version               int64                       `json:"version"`
	Seq                   int64                       `json:"seq"`
	ObservedAt            int64                       `json:"observed_at"`
	RunID                 string                      `json:"run_id"`
	TaskID                string                      `json:"task_id"`
	SpecVersion           int64                       `json:"spec_version"`
	WorkItems             []ObservedWorkItem          `json:"work_items"`
	Deployment            *ObservedDeployment         `json:"deployment,omitempty"`
}

// DeliveryExecutionAuthority is a server-read binding, not a caller-supplied
// approval or a record of provider mutations. Its candidate remains frozen even
// when current policy, owner sources or provider facts invalidate execution.
type DeliveryExecutionAuthority struct {
	Source                 string                    `json:"source"`
	ObservedAt             int64                     `json:"observed_at,omitempty"`
	TaskID                 string                    `json:"task_id"`
	SourceTaskID           string                    `json:"source_task_id"`
	IssueID                string                    `json:"issue_id"`
	AssignmentEpoch        int64                     `json:"assignment_epoch"`
	CurrentAssignmentEpoch int64                     `json:"current_assignment_epoch"`
	DutyRevision           int64                     `json:"duty_revision"`
	CurrentDutyRevision    int64                     `json:"current_duty_revision"`
	DutyStatus             string                    `json:"duty_status"`
	ControlRevision        int64                     `json:"control_revision"`
	SpecVersion            int64                     `json:"spec_version"`
	RunID                  string                    `json:"run_id,omitempty"`
	LeaseID                string                    `json:"lease_id,omitempty"`
	Role                   RunRole                   `json:"role,omitempty"`
	Candidate              IssueDeliveryCandidate    `json:"candidate"`
	Grant                  *IssueDeliveryGrant       `json:"grant,omitempty"`
	Policy                 *ObservedDeliveryPolicy   `json:"policy,omitempty"`
	Approval               *ObservedDeliveryApproval `json:"approval,omitempty"`
	Provider               *ObservedDeliveryProvider `json:"provider,omitempty"`
	SharedReview           *ObservedDeliveryReview   `json:"shared_review,omitempty"`
	UpgradeID              string                    `json:"upgrade_id,omitempty"`
	Valid                  bool                      `json:"valid"`
	InvalidReason          string                    `json:"invalid_reason,omitempty"`
}

type ObservedDeliveryPolicy struct {
	IssueID         string `json:"issue_id"`
	IssueRevision   int64  `json:"issue_revision"`
	Mode            string `json:"mode"`
	Repository      string `json:"repository"`
	Digest          string `json:"digest"`
	RequestID       string `json:"request_id,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	SourceRevision  int64  `json:"source_revision,omitempty"`
}

type ObservedDeliveryApproval struct {
	Current               bool   `json:"current"`
	AttentionID           string `json:"attention_id"`
	DecisionID            string `json:"decision_id"`
	DecisionVersion       int64  `json:"decision_version"`
	DecisionStatus        string `json:"decision_status"`
	Option                string `json:"option,omitempty"`
	SourceMessageID       string `json:"source_message_id,omitempty"`
	SourceRevision        int64  `json:"source_revision,omitempty"`
	CurrentSourceRevision int64  `json:"current_source_revision,omitempty"`
	SourceRequestID       string `json:"source_request_id,omitempty"`
	AnsweredAt            int64  `json:"answered_at,omitempty"`
}

// ObservedDeliveryProvider retains only independently recorded public candidate
// identities and timestamps, never private query nonces or command output.
type ObservedDeliveryProvider struct {
	TargetContainsMerge bool        `json:"target_contains_merge,omitempty"`
	LineageErrorKind    string      `json:"lineage_error_kind,omitempty"`
	CI                  *DeliveryCI `json:"ci,omitempty"`
	Mergeability        string      `json:"mergeability,omitempty"`
	Generation          int64       `json:"generation"`
	State               string      `json:"state"`
	HeadSHA             string      `json:"head_sha"`
	BaseSHA             string      `json:"base_sha"`
	TargetSHA           string      `json:"target_sha"`
	MergeSHA            string      `json:"merge_sha,omitempty"`
	CheckedAt           int64       `json:"checked_at"`
	AttemptedAt         int64       `json:"attempted_at"`
	RefreshPending      bool        `json:"refresh_pending"`
	ErrorKind           string      `json:"error_kind,omitempty"`
}

// ObservedDeliveryReview omits unrelated Issue links and finding bodies. Its
// optional round preserves the actual formal result's immutable provenance.
type ObservedDeliveryReview struct {
	Source                     string                       `json:"source"`
	ReviewID                   string                       `json:"review_id"`
	Revision                   int64                        `json:"revision"`
	Status                     string                       `json:"status"`
	Reviewer                   string                       `json:"reviewer,omitempty"`
	HeadSHA                    string                       `json:"head_sha"`
	BaseSHA                    string                       `json:"base_sha"`
	RequirementsDigest         string                       `json:"requirements_digest"`
	AssessedRequirementsDigest string                       `json:"assessed_requirements_digest,omitempty"`
	Valid                      bool                         `json:"valid"`
	InvalidReason              string                       `json:"invalid_reason,omitempty"`
	Round                      *ObservedDeliveryReviewRound `json:"round,omitempty"`
}

type ObservedDeliveryReviewRound struct {
	SupplementTaskID      string `json:"supplement_task_id,omitempty"`
	SupplementSpecVersion int64  `json:"supplement_spec_version,omitempty"`
	SupplementRunID       string `json:"supplement_run_id,omitempty"`
	SupplementArtifactID  string `json:"supplement_artifact_id,omitempty"`
	Number                int    `json:"number"`
	TaskID                string `json:"task_id"`
	SpecVersion           int64  `json:"spec_version"`
	RunID                 string `json:"run_id,omitempty"`
	ArtifactID            string `json:"artifact_id,omitempty"`
	AcceptanceID          string `json:"acceptance_id,omitempty"`
	Reviewer              string `json:"reviewer,omitempty"`
	HeadSHA               string `json:"head_sha"`
	BaseSHA               string `json:"base_sha"`
	RequirementsDigest    string `json:"requirements_digest"`
	Verdict               string `json:"verdict,omitempty"`
	RepositoryCheck       string `json:"repository_check,omitempty"`
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
	Admission      *ObservedServerAdmission `json:"admission,omitempty"`
	Runner         *ObservedRunner          `json:"runner,omitempty"`
	ObservedAt     int64                    `json:"observed_at"`
	SchemaVersion  int64                    `json:"schema_version"`
	ActualServer   *ObservedServer          `json:"actual_server,omitempty"`
	ProofStatus    string                   `json:"proof_status"`
	CommittedProof *UpdaterReceipt          `json:"committed_proof,omitempty"`
	LatestUpgrade  *ObservedUpgrade         `json:"latest_upgrade,omitempty"`
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
	UpdatedAt      int64                 `json:"updated_at,omitempty"`
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
	Source                           string      `json:"source,omitempty"`
	BoundedRecoveryAuthorizationID   string      `json:"bounded_recovery_authorization_id,omitempty"`
	PublishedRecoveryReleaseID       string      `json:"published_recovery_release_id,omitempty"`
	PublishedRecoveryAuthorizationID string      `json:"published_recovery_authorization_id,omitempty"`
	PendingOperations                *int64      `json:"pending_operations,omitempty"`
	PendingClaims                    *int64      `json:"pending_claims,omitempty"`
	UpgradeID                        string      `json:"upgrade_id,omitempty"`
	UpgradePhase                     string      `json:"upgrade_phase,omitempty"`
	HostID                           string      `json:"host_id"`
	InstanceID                       string      `json:"instance_id"`
	Online                           bool        `json:"online"`
	LastHeartbeatAt                  int64       `json:"last_heartbeat_at"`
	Mode                             string      `json:"mode"`
	ControlMode                      string      `json:"control_mode"`
	ControlRevision                  int64       `json:"control_revision"`
	AppliedControlRevision           int64       `json:"applied_control_revision"`
	Build                            RunnerBuild `json:"build"`
	Capabilities                     []string    `json:"capabilities"`
}

// ObservedServerAdmission reports durable admission controls at ObservedAt. It
// does not assert that every ordinary run or unknown native operation stopped.
type ObservedServerAdmission struct {
	Source                    string `json:"source"`
	ObservedAt                int64  `json:"observed_at"`
	ArchivePrepared           bool   `json:"archive_prepared"`
	ArchiveRevision           int64  `json:"archive_revision"`
	ArchiveUpdatedAt          int64  `json:"archive_updated_at"`
	CommittedMaintenanceFence bool   `json:"committed_maintenance_fence"`
}
