package protocol

const (
	CapabilityExternalWaits = "external-waits-v1"
	OpRunsWaitExternal      = "runs.wait_external"
	OpRunsYieldExternalWait = "runs.yield_external_wait"
	OpTasksResumeWait       = "tasks.resume_wait"
	ExternalWaitSatisfied   = "satisfied"
	EventPRWaitSatisfied    = "run.pr_wait_satisfied"
)

// WaitExternalPayload preserves the condition and the next useful action. It
// creates no decision or permission and never repeats the target operation.
type WaitExternalPayload struct {
	WaitKey          string           `json:"wait_key"`
	Kind             string           `json:"kind"`
	TargetID         string           `json:"target_id"`
	IssueID          string           `json:"issue_id,omitempty"`
	Reason           string           `json:"reason"`
	Recovery         string           `json:"recovery"`
	DeadlineAtMillis int64            `json:"deadline_at_ms,omitempty"`
	Condition        *PRWaitCondition `json:"condition,omitempty"`
}

func (p WaitExternalPayload) Equal(other WaitExternalPayload) bool {
	left, right := p.Condition, other.Condition
	p.Condition, other.Condition = nil, nil
	return p == other && (left == nil && right == nil || left != nil && right != nil && *left == *right)
}

const (
	PRWaitCheckCompleted  = "check_completed"
	PRWaitConflictAppears = "conflict_appears"
	PRWaitConflictClears  = "conflict_clears"
	PRWaitTargetMoved     = "target_moved"
	PRWaitMerged          = "merged"
	PRWaitClosed          = "closed"
)

// PRWaitCondition names a provider fact. A head and current target bind checks
// and conflicts to one candidate; an explicit target movement has its own base.
type PRWaitCondition struct {
	Kind          string `json:"kind"`
	HeadSHA       string `json:"head_sha,omitempty"`
	TargetRef     string `json:"target_ref,omitempty"`
	TargetSHA     string `json:"target_sha,omitempty"`
	Name          string `json:"name,omitempty"`
	Source        string `json:"source,omitempty"`
	App           string `json:"app,omitempty"`
	WorkflowRunID int64  `json:"workflow_run_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
}

// PRWaitEvidence is the first matching accepted provider fact for this wait.
type PRWaitEvidence struct {
	Provider     IssueDeliveryProvider `json:"provider"`
	Repository   string                `json:"repository"`
	Number       int64                 `json:"number"`
	FactRevision int64                 `json:"fact_revision"`
	ObservedAt   int64                 `json:"observed_at_ms"`
	Reason       string                `json:"reason"`
	State        string                `json:"state"`
	HeadSHA      string                `json:"head_sha,omitempty"`
	TargetRef    string                `json:"target_ref,omitempty"`
	TargetSHA    string                `json:"target_sha,omitempty"`
	URL          string                `json:"url,omitempty"`
	Check        *DeliveryCheck        `json:"check,omitempty"`
}

// ExternalWait keeps machine waiting separate from unknown physical execution.
type ExternalWait struct {
	NoticeRestored bool   `json:"notice_restored,omitempty"`
	SourceEvent    string `json:"source_event,omitempty"`
	SourceMessage  string `json:"source_message,omitempty"`
	WaitExternalPayload
	PRBinding            *IssueDelivery  `json:"pr_binding,omitempty"`
	PREvidence           *PRWaitEvidence `json:"pr_evidence,omitempty"`
	WaitID               string          `json:"wait_id"`
	RunID                string          `json:"run_id"`
	TaskID               string          `json:"task_id"`
	Status               string          `json:"status"`
	Stopped              bool            `json:"stopped"`
	ThreadID             string          `json:"thread_id,omitempty"`
	CreatedAtMillis      int64           `json:"created_at_ms"`
	NextReminderAtMillis int64           `json:"next_reminder_at_ms"`
	InputEventWatermark  int64           `json:"input_event_watermark,omitempty"`
	ObservedRevision     int64           `json:"observed_revision,omitempty"`
	ObservedState        string          `json:"observed_state,omitempty"`
	ObservedBase         string          `json:"observed_base,omitempty"`
	ObservedTargetRef    string          `json:"observed_target_ref,omitempty"`
	ObservedTarget       string          `json:"observed_target,omitempty"`
	ObservedHead         string          `json:"observed_head,omitempty"`
	WakeReason           string          `json:"wake_reason,omitempty"`
	ResumeReason         string          `json:"resume_reason,omitempty"`
	SuccessorRunID       string          `json:"successor_run_id,omitempty"`
}

// CIContinuation describes a single original Issue-owner wait and the source of its wake.
type CIContinuation struct {
	NoticeRestored bool   `json:"notice_restored,omitempty"`
	IssueID        string `json:"issue_id"`
	TaskID         string `json:"task_id"`
	RunID          string `json:"run_id"`
	WaitID         string `json:"wait_id"`
	DeliveryID     string `json:"delivery_id"`
	Head           string `json:"head"`
	Base           string `json:"base,omitempty"`
	Target         string `json:"target,omitempty"`
	Result         string `json:"result"`
	SourceEvent    string `json:"source_event,omitempty"`
	SourceMessage  string `json:"source_message,omitempty"`
	OwnerTurn      string `json:"owner_turn,omitempty"`
	State          string `json:"state"`
}

// ResumeExternalWaitPayload resumes judgment from the original wait. It does
// not approve or retry the external operation recorded by that wait.
type ResumeExternalWaitPayload struct {
	RestoreNotice bool   `json:"restore_ci_notice,omitempty"`
	WaitID        string `json:"wait_id"`
	Reason        string `json:"reason"`
}

type YieldExternalWaitPayload struct {
	WaitID   string `json:"wait_id"`
	ThreadID string `json:"thread_id,omitempty"`
}

const OpRunsRecordSessionStop = "runs.record_session_stop"

// SessionStopPayload is a physical close fact, not permission to resume work.
type SessionStopPayload struct {
	EventSequence int64  `json:"event_sequence,omitempty"`
	PhysicalExit  bool   `json:"physical_exit,omitempty"`
	ThreadID      string `json:"thread_id"`
}

// SessionContinuation is an optional optimization across two stopped Runs.
// Current context is reconstructed independently; no session is required.
type SessionContinuation struct {
	RunID    string `json:"run_id"`
	ThreadID string `json:"thread_id"`
}
