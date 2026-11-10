package protocol

const (
	CapabilityPauseHandoffs = "run-pause-handoff-v1"
	OpRunsYieldPause        = "runs.yield_pause"
)

// PauseHandoff identifies the original Issue instruction and frozen Task control.
// It is present only for an execution that negotiated physical slot handoff.
type PauseHandoff struct {
	RequestID       string `json:"request_id"`
	IssueID         string `json:"issue_id"`
	IssueRevision   int64  `json:"issue_revision"`
	ControlRevision int64  `json:"control_revision"`
}

// YieldPausePayload confirms actual process exit, independently of business results.
type YieldPausePayload struct {
	PauseHandoff
	ThreadID string `json:"thread_id,omitempty"`
}

type PauseContinuation struct {
	RunID    string `json:"run_id"`
	ThreadID string `json:"thread_id,omitempty"`
}
