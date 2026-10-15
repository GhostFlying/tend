package harness

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrRejected marks an error the harness answered with: the request
// certainly did not take effect. Any other error, such as a broken
// connection, leaves the outcome open.
var ErrRejected = errors.New("harness: request rejected")

// Session is one running harness, such as an app-server process, hosting
// any number of threads.
type Session interface {
	// StartThread creates a thread.
	StartThread(ctx context.Context, cfg ThreadConfig) (Thread, error)
	// ResumeThread reopens a thread the harness persisted, for example after
	// the harness or tend restarted.
	ResumeThread(ctx context.Context, threadID string, cfg ThreadConfig) (Thread, error)
	// Done is closed when the harness exits.
	Done() <-chan struct{}
	// Close stops the harness.
	Close() error
}

// ThreadConfig configures a thread.
type ThreadConfig struct {
	Cwd                   string
	Model                 string
	DeveloperInstructions string
	// Sandbox and ApprovalPolicy use the harness's own names, such as
	// "danger-full-access" and "never".
	Sandbox        string
	ApprovalPolicy string
	// Config overrides harness configuration by dotted key, the form of
	// `-c key=value`.
	Config map[string]any
	// Tools are implemented by tend; OnToolCall answers their calls.
	Tools      []ToolSpec
	OnToolCall ToolHandler
}

// ToolSpec describes a tool tend implements for the model.
type ToolSpec struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments.
	InputSchema json.RawMessage
}

// ToolCall is the model calling one of tend's tools. ThreadID, TurnID and
// CallID together identify the call; CallID alone is opaque.
type ToolCall struct {
	ThreadID  string
	TurnID    string
	CallID    string
	Namespace string
	Tool      string
	Arguments json.RawMessage
}

// ToolResult is tend's answer to a tool call.
type ToolResult struct {
	Success bool
	Text    string
}

// ToolHandler answers tool calls. It runs on its own goroutine per call.
type ToolHandler func(ctx context.Context, call ToolCall) ToolResult

// TurnInput is a user turn.
type TurnInput struct {
	Text string
	// UserMessageID, when set, is recorded with the turn's user message so
	// the turn can be found again after an uncertain start.
	UserMessageID string
}

// Thread is a conversation in a harness.
type Thread interface {
	ID() string
	// InjectDeveloper appends developer messages to the model-visible
	// history without starting a turn. The harness does not deduplicate.
	InjectDeveloper(ctx context.Context, texts ...string) error
	// StartTurn starts a turn and returns its ID once the harness accepted
	// it; progress arrives as events.
	StartTurn(ctx context.Context, in TurnInput) (string, error)
	// Interrupt stops a running turn.
	Interrupt(ctx context.Context, turnID string) error
	// Events delivers the thread's events in order. It is closed when the
	// session ends.
	Events() <-chan Event
}

// EventKind classifies thread events.
type EventKind string

// Event kinds.
const (
	EventTurnStarted   EventKind = "turn_started"
	EventTurnCompleted EventKind = "turn_completed"
	EventItemStarted   EventKind = "item_started"
	EventItemCompleted EventKind = "item_completed"
)

// Item types tend acts on. Other types pass through as they are.
const (
	ItemAgentMessage      = "agentMessage"
	ItemUserMessage       = "userMessage"
	ItemContextCompaction = "contextCompaction"
)

// Event is something that happened in a thread.
type Event struct {
	Kind   EventKind
	TurnID string
	// ItemID and ItemType describe the item of item events.
	ItemID   string
	ItemType string
	// Text is an agent message's text.
	Text string
	// TurnStatus is the final status of a completed turn: completed,
	// interrupted or failed.
	TurnStatus string
	// Raw is the harness's own payload.
	Raw json.RawMessage
}
