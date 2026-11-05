package protocol

const OpRetireManagerRun = "tasks.retire_manager_run"

// RetireManagerRunPayload withdraws coordination on one original manager Run.
// It requests a physical stop; it never supplies or replaces stop evidence.
type RetireManagerRunPayload struct {
	IssueSourceReference
	IssueID                 string `json:"issue_id"`
	ExpectedIssueRevision   int64  `json:"expected_issue_revision"`
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	RunID                   string `json:"run_id"`
	HostID                  string `json:"host_id"`
	LeaseID                 string `json:"lease_id"`
	ExecutionSHA256         string `json:"execution_sha256"`
	Reason                  string `json:"reason"`
}

// ManagerNativeRequestStatus exposes original request identities without their
// native payload, prompt, credentials or result body.
type ManagerNativeRequestStatus struct {
	RequestID         string `json:"request_id"`
	Method            string `json:"method"`
	Status            string `json:"status"`
	ResultPresent     bool   `json:"result_present"`
	ResultSHA256      string `json:"result_sha256,omitempty"`
	ResultHashOmitted bool   `json:"result_hash_omitted,omitempty"`
	Error             bool   `json:"error"`
	Rejected          bool   `json:"rejected"`
}

// ManagerRetirementStatus contains current control facts, not a native probe or
// physical stop assertion. Native requests are paginated by original request ID.
type ManagerRetirementStatus struct {
	IssueID                  string                       `json:"issue_id"`
	IssueRevision            int64                        `json:"issue_revision"`
	AssignmentEpoch          int64                        `json:"assignment_epoch"`
	TaskID                   string                       `json:"task_id"`
	TaskRevision             int64                        `json:"task_revision"`
	RunID                    string                       `json:"run_id"`
	ActionID                 string                       `json:"action_id"`
	HostID                   string                       `json:"host_id"`
	LeaseID                  string                       `json:"lease_id"`
	LeaseStatus              string                       `json:"lease_status"`
	Observed                 RunObserved                  `json:"observed"`
	Desired                  RunDesired                   `json:"desired"`
	Execution                *RunExecutionPin             `json:"execution,omitempty"`
	ExecutionSHA256          string                       `json:"execution_sha256,omitempty"`
	RetirementRequestID      string                       `json:"retirement_request_id,omitempty"`
	CanRetire                bool                         `json:"can_retire"`
	Blockers                 []string                     `json:"blockers"`
	EventLastSequence        int64                        `json:"event_last_sequence"`
	UnappliedEvents          int64                        `json:"unapplied_events"`
	UnresolvedNativeRequests int64                        `json:"unresolved_native_requests"`
	PendingBusinessReceipts  bool                         `json:"pending_business_receipts"`
	NativeRequests           []ManagerNativeRequestStatus `json:"native_requests"`
	More                     bool                         `json:"more"`
	NextAfter                string                       `json:"next_after,omitempty"`
}
