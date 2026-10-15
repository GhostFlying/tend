package protocol

// Owner API wire types besides the envelope and the read views. The
// payloads of operations other callers share carry fields only the main
// agent sets, marked as such.

// SessionRequest creates an owner session for a client. The client also
// sends ClientID in the X-Tend-Client header.
type SessionRequest struct {
	ClientID string `json:"client_id"`
}

// SessionView is the caller's session.
type SessionView struct {
	Login    string `json:"login"`
	ClientID string `json:"client_id"`
}

// APIResponse is the body of an owner API write, and of any request that
// failed.
type APIResponse struct {
	Operation *Operation `json:"operation,omitempty"`
	Error     *Error     `json:"error,omitempty"`
	// Current is the task's state after a stale write about it, so the
	// client can decide again.
	Current *TaskSummary `json:"current,omitempty"`
}

// Owner operations that only the owner API performs.
const (
	OpInterruptReply = "control.interrupt_reply"
)

// InterruptReplyResult is the result of control.interrupt_reply. Running
// reports whether a turn was handling the message; if so the operation is
// accepted and the message becomes interrupted when the turn ends.
type InterruptReplyResult struct {
	MessageID string `json:"message_id"`
	Running   bool   `json:"running"`
}

// SubmitMessagePayload is the payload of conversation.submit_message.
type SubmitMessagePayload struct {
	Body string `json:"body"`
	// ReplyTo and LastSeenSeq are what the client showed when the owner
	// wrote: the message replied to and the last message seen (v0.1
	// §6.2, D27).
	ReplyTo     string `json:"reply_to,omitempty"`
	LastSeenSeq int64  `json:"last_seen_seq,omitempty"`
}

// AnswerDecisionPayload is the payload of decisions.respond and
// decisions.resolve_from_message.
type AnswerDecisionPayload struct {
	// Version is the decision version the answerer saw.
	Version int64 `json:"version"`
	// Option is one of the decision's options; Text is a free-form answer.
	// At least one is required.
	Option string `json:"option,omitempty"`
	Text   string `json:"text,omitempty"`
	// SourceMessageID is set by the main agent only.
	SourceMessageID string `json:"source_message_id,omitempty"`
}

// SetControlModePayload is the payload of control.set_mode.
type SetControlModePayload struct {
	Mode ControlMode `json:"mode"`
}

// CreateActionPayload is the payload of actions.create.
type CreateActionPayload struct {
	Kind       ActionKind `json:"kind"`
	Generation int64      `json:"generation"`
	// SourceMessageID and CommitSeq are set by the main agent only.
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
}
