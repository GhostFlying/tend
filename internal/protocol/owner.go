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
