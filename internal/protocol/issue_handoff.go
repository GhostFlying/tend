package protocol

// IssueHandoffPrompt restores ledger context independently of a native session.
// It is a read-only locator and does not grant task or workspace control.
type IssueHandoffPrompt struct {
	IssueID             string      `json:"issue_id"`
	Title               string      `json:"title"`
	Revision            int64       `json:"revision"`
	AssignmentEpoch     int64       `json:"assignment_epoch"`
	ContextRevision     int64       `json:"context_revision"`
	Watermark           int64       `json:"watermark"`
	ContextSyncStatus   string      `json:"context_sync_status"`
	ContextGaps         string      `json:"context_gaps"`
	SourcesMore         bool        `json:"sources_more"`
	NextSource          int64       `json:"next_source,omitempty"`
	ConversationID      string      `json:"conversation_id,omitempty"`
	ConversationThrough int64       `json:"conversation_through,omitempty"`
	TaskID              string      `json:"task_id,omitempty"`
	RunID               string      `json:"run_id,omitempty"`
	HostID              string      `json:"host_id,omitempty"`
	ControlMode         ControlMode `json:"control_mode,omitempty"`
	ControlRevision     int64       `json:"control_revision,omitempty"`
	RunObserved         RunObserved `json:"run_observed,omitempty"`
	Queries             []string    `json:"queries"`
	Prompt              string      `json:"prompt"`
}
