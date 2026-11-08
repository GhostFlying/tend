package protocol

// IssueDeliveryCandidate is the exact reviewable candidate and requested scope.
// ProviderState must be known; the observation remains independent evidence.
type IssueDeliveryDeployment struct {
	ParkIdleManagers        bool     `json:"park_idle_managers,omitempty"`
	BaselineUpgradeID       string   `json:"baseline_upgrade_id,omitempty"`
	BaselineUpgradeRevision int64    `json:"baseline_upgrade_revision,omitempty"`
	ServerInstance          string   `json:"server_instance"`
	HostIDs                 []string `json:"host_ids"`
}

type IssueDeliveryReconciliation struct {
	SourceCommit   string `json:"source_commit,omitempty"`
	EvidenceTaskID string `json:"evidence_task_id"`
	UpgradeID      string `json:"upgrade_id"`
}

type IssueDeliveryCandidate struct {
	TargetSHA       string                       `json:"target_sha"`
	DeliveryID      string                       `json:"delivery_id"`
	URL             string                       `json:"url"`
	HeadSHA         string                       `json:"head_sha"`
	MergeSHA        string                       `json:"merge_sha,omitempty"`
	BaseSHA         string                       `json:"base_sha"`
	ProviderState   string                       `json:"provider_state"`
	Validation      string                       `json:"validation"`
	DeploymentScope string                       `json:"deployment_scope"`
	Deploy          bool                         `json:"deploy"`
	Reconciliation  *IssueDeliveryReconciliation `json:"reconciliation,omitempty"`
	Deployment      *IssueDeliveryDeployment     `json:"deployment,omitempty"`
}

// IssueDeliveryDuty survives plan edits, notification review and task completion.
type IssueDeliveryDuty struct {
	PolicyAttentionID string                  `json:"policy_attention_id,omitempty"`
	Authorization     *IssueDeliveryGrant     `json:"authorization,omitempty"`
	AssignmentEpoch   int64                   `json:"assignment_epoch,omitempty"`
	TaskID            string                  `json:"task_id"`
	EventID           string                  `json:"event_id"`
	AcceptanceID      string                  `json:"acceptance_id,omitempty"`
	ArtifactID        string                  `json:"artifact_id,omitempty"`
	Revision          int64                   `json:"revision"`
	Status            string                  `json:"status"`
	Candidate         *IssueDeliveryCandidate `json:"candidate,omitempty"`
	AttentionID       string                  `json:"attention_id,omitempty"`
	ExecutionTaskID   string                  `json:"execution_task_id,omitempty"`
	Reason            string                  `json:"reason,omitempty"`
	Evidence          []IssueEvidence         `json:"evidence,omitempty"`
}

type UpdateIssueDeliveryPayload struct {
	IssueControlPayload
	TaskID string `json:"task_id"`
	// bind uses the current Task directly; it never creates or revives a Task.
	ExecutionTaskID             string `json:"execution_task_id,omitempty"`
	ExpectedTaskControlRevision *int64 `json:"expected_task_control_revision,omitempty"`
	// prepare, resume, bind, report, end, complete. End retains owner disposition.
	Action    string                  `json:"action"`
	Candidate *IssueDeliveryCandidate `json:"candidate,omitempty"`
	UpgradeID string                  `json:"upgrade_id,omitempty"`
}

// IssueDeliveryAuthorization selects an answered candidate, never an approval
// supplied by a model. Its scope is frozen by the server into the new task.
type IssueDeliveryAuthorization struct {
	TaskID   string `json:"task_id"`
	Revision int64  `json:"revision"`
}
