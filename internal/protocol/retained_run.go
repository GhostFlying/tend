package protocol

const OpContinueRetainedRun = "runs.continue_retained"

// RetainedRunProtection preserves the exact original maintenance identity.
type RetainedRunProtection struct {
	MaintenanceID string `json:"maintenance_id"`
	RunID         string `json:"run_id"`
	TaskID        string `json:"task_id"`
	ActionID      string `json:"action_id"`
	HostID        string `json:"host_id"`
	LeaseID       string `json:"lease_id"`
	// OriginalRequestsJSON distinguishes the original null/empty encodings.
	OriginalRequestsJSON string `json:"original_requests_json"`
	Linked               bool   `json:"linked"`
}

// RetainedRunIdentity is frozen by an independent continuation resolution.
type RetainedRunIdentity struct {
	TaskID      string          `json:"task_id"`
	ActionID    string          `json:"action_id"`
	Generation  int64           `json:"generation"`
	SpecVersion int64           `json:"spec_version"`
	HostID      string          `json:"host_id"`
	LeaseID     string          `json:"lease_id"`
	Execution   RunExecutionPin `json:"execution"`
}

// ContinueRetainedRunPayload cannot declare unknown receipts settled.
type ContinueRetainedRunPayload struct {
	RecoveryAuthorizationID string `json:"recovery_authorization_id,omitempty"`
	IssueSourceReference
	RetainedRunIdentity
	Protections         []RetainedRunProtection `json:"protections"`
	ProtectionSetSHA256 string                  `json:"protection_set_sha256"`
	Reason              string                  `json:"reason"`
}

// RetainedRunReconciliation is a ledger read, not a provider observation.
type RetainedRunReconciliation struct {
	RecoveryAuthorizationID  string                  `json:"recovery_authorization_id,omitempty"`
	RunID                    string                  `json:"run_id"`
	Identity                 RetainedRunIdentity     `json:"identity"`
	TaskRevision             int64                   `json:"task_revision"`
	Role                     RunRole                 `json:"role"`
	Observed                 RunObserved             `json:"observed"`
	LeaseStatus              string                  `json:"lease_status"`
	Protections              []RetainedRunProtection `json:"protections"`
	ProtectionSetSHA256      string                  `json:"protection_set_sha256"`
	OriginalRequestIDs       []string                `json:"original_request_ids"`
	UnresolvedNativeRequests int64                   `json:"unresolved_native_requests"`
	UnappliedEvents          int64                   `json:"unapplied_events"`
	PendingBusinessReceipts  bool                    `json:"pending_business_receipts"`
	ContinuationRequestID    string                  `json:"continuation_request_id,omitempty"`
	ContinuationTaskRevision int64                   `json:"continuation_task_revision,omitempty"`
	Blockers                 []string                `json:"blockers"`
	CanContinue              bool                    `json:"can_continue"`
}

// RetainedRunResume is sent only to an explicitly negotiated strict runner.
// The request identity selects a durable independent continuation permission.
type RetainedRunResume struct {
	RecoveryAuthorizationID string `json:"recovery_authorization_id,omitempty"`
	RequestID               string `json:"request_id"`
	Mode                    string `json:"mode"`
}
