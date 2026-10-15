package protocol

import "time"

// Owner API wire types besides the envelope and the read views. The
// payloads of operations other callers share carry fields only the hosted
// main agent sets, marked as such.

// TokenScope is what an owner API token may be used for (D49).
type TokenScope string

// Token scopes.
const (
	// ScopeOwner is for the owner's own commands, such as the tend CLI.
	ScopeOwner TokenScope = "owner"
	// ScopeAgent is for an agent the owner works in: its hooks and the
	// tools it calls through the MCP bridge.
	ScopeAgent TokenScope = "agent"
)

// Valid reports whether s is a known scope.
func (s TokenScope) Valid() bool { return s == ScopeOwner || s == ScopeAgent }

// SessionRequest asks for a token for a client the caller generated and
// keeps, such as one tend CLI installation.
type SessionRequest struct {
	ClientID string     `json:"client_id"`
	Scope    TokenScope `json:"scope"`
}

// SessionView describes the token a request carries. Token is set only in
// the answer to a SessionRequest; it goes in the Authorization header as a
// bearer token.
type SessionView struct {
	Login     string     `json:"login"`
	ClientID  string     `json:"client_id"`
	Scope     TokenScope `json:"scope"`
	ExpiresAt time.Time  `json:"expires_at"`
	Token     string     `json:"token,omitempty"`
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
	// SourceMessageID is set by the hosted main agent only.
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
	// SourceMessageID and CommitSeq are set by the hosted main agent only.
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
}
