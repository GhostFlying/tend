package protocol

import (
	"encoding/json"
	"time"
)

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

// APIResponse is the body of an owner API write, and of any request that
// failed.
type APIResponse struct {
	Operation *Operation `json:"operation,omitempty"`
	Error     *Error     `json:"error,omitempty"`
	// Current is the task's state after a stale write about it, so the
	// caller can decide again.
	Current         *TaskSummary  `json:"current,omitempty"`
	CurrentWorkItem *WorkItemView `json:"current_work_item,omitempty"`
}

// SubmitMessagePayload is the payload of conversation.submit_message.
type SubmitMessagePayload struct {
	Images      []ConversationImageInput `json:"images,omitempty"`
	Attachments []string                 `json:"attachments,omitempty"`
	Body        string                   `json:"body"`
	// ReplyTo and LastSeenSeq are what the client showed when the owner
	// wrote: the message replied to and the last message seen (v0.1
	// §6.2, D27).
	ReplyTo     string `json:"reply_to,omitempty"`
	LastSeenSeq int64  `json:"last_seen_seq,omitempty"`
	// Decision is the option of an open decision the message chooses,
	// when the option is one tend does not carry out itself (D71).
	Decision *DecisionChoice `json:"decision,omitempty"`
}

// DecisionChoice is an option of a decision, as the owner saw it.
type DecisionChoice struct {
	DecisionID string `json:"decision_id"`
	Version    int64  `json:"version"`
	Option     string `json:"option"`
}

// InterruptReplyPayload is the payload of conversation.interrupt_reply:
// stop the main agent's reply to an owner message (D69).
type InterruptReplyPayload struct {
	MessageID string `json:"message_id"`
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

// AnswerResult is the result of answering a decision.
type AnswerResult struct {
	DecisionID string          `json:"decision_id"`
	Status     string          `json:"status"`
	Version    int64           `json:"version"`
	Answer     json.RawMessage `json:"answer"`
	// Effect is present when the chosen option has one (D53).
	Effect *EffectResult `json:"effect,omitempty"`
}

// EffectResult tells what tend did about the effect of a chosen option.
type EffectResult struct {
	Effect  DecisionEffect `json:"effect"`
	Applied bool           `json:"applied"`
	// Reason says why the effect was not applied, for example because the
	// task changed after the question was asked.
	Reason string `json:"reason,omitempty"`
}

// SetControlModePayload is the payload of control.set_mode.
type SetControlModePayload struct {
	Mode ControlMode `json:"mode"`
}

// RateTaskPayload is the payload of tasks.rate.
type RateTaskPayload struct {
	Rating Rating `json:"rating"`
}

// CreateActionPayload is the payload of actions.create.
type CreateActionPayload struct {
	Kind       ActionKind `json:"kind"`
	Generation int64      `json:"generation"`
	// SourceMessageID and CommitSeq are set by the hosted main agent only.
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
}
