package protocol

const (
	CapabilityRunMessages = "run-messages-v1"
	OpRunsSendMessage     = "runs.send_message"
	OpRunsClaimMessage    = "runs.claim_message"
	OpRunsReportMessage   = "runs.report_message"
	MaxRunMessageBytes    = 16 << 10
)

// RunMessageStatus describes delivery evidence, never model consumption.
type RunMessageStatus string

const (
	RunMessageQueued      RunMessageStatus = "queued"
	RunMessageDispatching RunMessageStatus = "dispatching"
	RunMessageAccepted    RunMessageStatus = "accepted"
	RunMessageFailed      RunMessageStatus = "failed"
	RunMessageUnknown     RunMessageStatus = "unknown"
	RunMessageUnsupported RunMessageStatus = "unsupported"
)

// SendRunMessagePayload supplements a frozen task contract without revising it.
type SendRunMessagePayload struct {
	TaskID          string `json:"task_id"`
	SpecVersion     int64  `json:"spec_version"`
	Generation      int64  `json:"generation"`
	Text            string `json:"text"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
}

// RunMessageDelivery binds a single native invocation to an executor incarnation.
type RunMessageDelivery struct {
	MessageID string `json:"message_id"`
	Receiver  string `json:"receiver"`
	ThreadID  string `json:"thread_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
}

// RunMessageReceipt reports only fixed, non-sensitive reasons and native identity.
type RunMessageReceipt struct {
	RunMessageDelivery
	Status RunMessageStatus `json:"status"`
	Reason string           `json:"reason,omitempty"`
}

// RunMessageHistory is an append-only record of a proven delivery transition.
type RunMessageHistory struct {
	Status    RunMessageStatus `json:"status"`
	Reason    string           `json:"reason,omitempty"`
	CreatedAt int64            `json:"created_at"`
}

// RunMessage is both the queue record and an auditable delivery projection.
type RunMessage struct {
	RunMessageDelivery
	TaskID          string              `json:"task_id"`
	RunID           string              `json:"run_id"`
	ActionID        string              `json:"action_id"`
	SpecVersion     int64               `json:"spec_version"`
	Generation      int64               `json:"generation"`
	ControlRevision int64               `json:"control_revision"`
	Sequence        int64               `json:"sequence"`
	Text            string              `json:"text"`
	Status          RunMessageStatus    `json:"status"`
	Reason          string              `json:"reason,omitempty"`
	RequestID       string              `json:"request_id"`
	SourceMessageID string              `json:"source_message_id,omitempty"`
	Source          Principal           `json:"source"`
	CreatedAt       int64               `json:"created_at"`
	UpdatedAt       int64               `json:"updated_at"`
	History         []RunMessageHistory `json:"history"`
}

// RunMessagePage bounds both main-agent history and runner polling.
type RunMessagePage struct {
	Messages []RunMessage `json:"messages"`
	More     bool         `json:"more"`
	Next     int64        `json:"next"`
}
