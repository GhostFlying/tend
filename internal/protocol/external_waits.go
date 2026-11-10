package protocol

const (
	CapabilityExternalWaits = "external-waits-v1"
	OpRunsWaitExternal      = "runs.wait_external"
	OpRunsYieldExternalWait = "runs.yield_external_wait"
	OpTasksResumeWait       = "tasks.resume_wait"
)

// WaitExternalPayload preserves the condition and the next useful action. It
// creates no decision or permission and never repeats the target operation.
type WaitExternalPayload struct {
	WaitKey          string `json:"wait_key"`
	Kind             string `json:"kind"`
	TargetID         string `json:"target_id"`
	IssueID          string `json:"issue_id,omitempty"`
	Reason           string `json:"reason"`
	Recovery         string `json:"recovery"`
	DeadlineAtMillis int64  `json:"deadline_at_ms,omitempty"`
}

// ExternalWait keeps machine waiting separate from unknown physical execution.
type ExternalWait struct {
	WaitExternalPayload
	WaitID               string `json:"wait_id"`
	RunID                string `json:"run_id"`
	TaskID               string `json:"task_id"`
	Status               string `json:"status"`
	Stopped              bool   `json:"stopped"`
	ThreadID             string `json:"thread_id,omitempty"`
	CreatedAtMillis      int64  `json:"created_at_ms"`
	NextReminderAtMillis int64  `json:"next_reminder_at_ms"`
	InputEventWatermark  int64  `json:"input_event_watermark,omitempty"`
	ObservedRevision     int64  `json:"observed_revision,omitempty"`
	ObservedState        string `json:"observed_state,omitempty"`
	ObservedHead         string `json:"observed_head,omitempty"`
	WakeReason           string `json:"wake_reason,omitempty"`
	ResumeReason         string `json:"resume_reason,omitempty"`
	SuccessorRunID       string `json:"successor_run_id,omitempty"`
}

// ResumeExternalWaitPayload resumes judgment from the original wait. It does
// not approve or retry the external operation recorded by that wait.
type ResumeExternalWaitPayload struct {
	WaitID string `json:"wait_id"`
	Reason string `json:"reason"`
}

type YieldExternalWaitPayload struct {
	WaitID   string `json:"wait_id"`
	ThreadID string `json:"thread_id,omitempty"`
}

const OpRunsRecordSessionStop = "runs.record_session_stop"

// SessionStopPayload is a physical close fact, not permission to resume work.
type SessionStopPayload struct {
	ThreadID string `json:"thread_id"`
}

// SessionContinuation is an optional optimization across two stopped Runs.
// Current context is reconstructed independently; no session is required.
type SessionContinuation struct {
	RunID    string `json:"run_id"`
	ThreadID string `json:"thread_id"`
}
