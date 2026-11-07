package protocol

const OpReconcileManagerRun = "tasks.reconcile_manager_run"

// ManagerRunReconciliation describes ledger evidence, never a new native probe
// or a claim that a missing session stopped. A write rechecks every condition.
type ManagerRunReconciliation struct {
	TaskID                   string           `json:"task_id"`
	RunID                    string           `json:"run_id"`
	ActionID                 string           `json:"action_id"`
	HostID                   string           `json:"host_id"`
	LeaseID                  string           `json:"lease_id"`
	TaskRevision             int64            `json:"task_revision"`
	AssignmentEpoch          int64            `json:"assignment_epoch"`
	Observed                 RunObserved      `json:"observed"`
	Execution                *RunExecutionPin `json:"execution,omitempty"`
	CloseRequestID           string           `json:"close_request_id,omitempty"`
	CloseResultSHA256        string           `json:"close_result_sha256,omitempty"`
	CloseLastSequence        int64            `json:"close_last_sequence"`
	StoppedRequestID         string           `json:"stopped_request_id,omitempty"`
	StoppedPayloadSHA256     string           `json:"stopped_payload_sha256,omitempty"`
	StoppedLastSequence      int64            `json:"stopped_last_sequence"`
	EventLastSequence        int64            `json:"event_last_sequence"`
	UnappliedEvents          int64            `json:"unapplied_events"`
	UnresolvedNativeRequests int64            `json:"unresolved_native_requests"`
	UnresolvedReadRequests   int64            `json:"unresolved_read_requests,omitempty"`
	PendingBusinessReceipts  bool             `json:"pending_business_receipts"`
	CanSettleWaiting         bool             `json:"can_settle_waiting"`
	Blockers                 []string         `json:"blockers"`
}

// ReconcileManagerRunPayload selects existing evidence; it cannot assert a stop,
// alter the original native result, renew a lease, or authorize replay.
type ReconcileManagerRunPayload struct {
	IssueSourceReference
	// Empty retains the original settle-waiting contract. read_receipts only
	// releases proved-closed transport capacity for a pinned receipt reader.
	Mode                    string `json:"mode,omitempty"`
	RunID                   string `json:"run_id"`
	LeaseID                 string `json:"lease_id"`
	CloseRequestID          string `json:"close_request_id"`
	CloseResultSHA256       string `json:"close_result_sha256"`
	StoppedRequestID        string `json:"stopped_request_id,omitempty"`
	StoppedPayloadSHA256    string `json:"stopped_payload_sha256,omitempty"`
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	Reason                  string `json:"reason"`
}
