package protocol

// IssueDeliveryCandidate is the exact reviewable candidate and requested scope.
// ProviderState must be known; the observation remains independent evidence.
type IssueDeliveryDeployment struct {
	ServerInstance string   `json:"server_instance"`
	HostIDs        []string `json:"host_ids"`
}

type IssueDeliveryCandidate struct {
	DeliveryID      string                   `json:"delivery_id"`
	URL             string                   `json:"url"`
	HeadSHA         string                   `json:"head_sha"`
	MergeSHA        string                   `json:"merge_sha,omitempty"`
	BaseSHA         string                   `json:"base_sha"`
	ProviderState   string                   `json:"provider_state"`
	Validation      string                   `json:"validation"`
	DeploymentScope string                   `json:"deployment_scope"`
	Deploy          bool                     `json:"deploy"`
	Deployment      *IssueDeliveryDeployment `json:"deployment,omitempty"`
}

// IssueDeliveryDuty survives plan edits, notification review and task completion.
type IssueDeliveryDuty struct {
	TaskID          string                  `json:"task_id"`
	EventID         string                  `json:"event_id"`
	AcceptanceID    string                  `json:"acceptance_id"`
	Revision        int64                   `json:"revision"`
	Status          string                  `json:"status"`
	Candidate       *IssueDeliveryCandidate `json:"candidate,omitempty"`
	AttentionID     string                  `json:"attention_id,omitempty"`
	ExecutionTaskID string                  `json:"execution_task_id,omitempty"`
	Reason          string                  `json:"reason,omitempty"`
	Evidence        []IssueEvidence         `json:"evidence,omitempty"`
}

type UpdateIssueDeliveryPayload struct {
	IssueControlPayload
	TaskID string `json:"task_id"`
	// prepare, resume, report, end, complete. End requires a real owner disposition.
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
