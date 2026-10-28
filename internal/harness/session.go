package harness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
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

// ThreadForker creates disposable isolated conversations without changing the source.
// ReleaseThread releases only forks created by this session. Callers must interrupt
// an unfinished turn before release; native unloading may follow an idle delay.
type ThreadForker interface {
	ForkThread(ctx context.Context, sourceThreadID string, cfg ThreadConfig) (Thread, error)
	ReleaseThread(ctx context.Context, threadID string) error
}

// ThreadLocation exposes a native history location when the harness has one.
// Disposable threads can have an empty path.
type ThreadLocation interface {
	RolloutPath() string
}

// Model is a selectable entry from a harness's live model catalog.
type Model struct {
	ID, Name, Label, Provider string
	DefaultEffort             string
	SupportedEfforts          []string
	Default                   bool
}

// ModelCatalog is implemented by sessions that expose model capabilities.
type ModelCatalog interface {
	Models(ctx context.Context) ([]Model, error)
}

// ModelSelection is the actual provider configuration of a turn.
type ModelSelection struct {
	Model, Provider, Effort string
}

// ConfiguredThread reports the defaults the harness actually selected.
type ConfiguredThread interface {
	ModelDefaults() ModelSelection
}

// PermissionSettings is the effective policy reported by a native thread.
// Empty fields mean the runtime did not report that part of the policy.
type PermissionSettings struct {
	Sandbox, ApprovalPolicy, ApprovalsReviewer string
}

// PermissionedThread reports native policy, rather than requested defaults.
type PermissionedThread interface {
	PermissionDefaults() PermissionSettings
}

// ThreadConfig configures a thread.
type ThreadConfig struct {
	// NetworkAccess explicitly enables networking without widening filesystem access.
	NetworkAccess         bool
	Cwd                   string
	Model                 string
	DeveloperInstructions string
	// Sandbox and ApprovalPolicy use the harness's own names, such as
	// "danger-full-access" and "never".
	Sandbox        string
	ApprovalPolicy string
	// ApprovalsReviewer is who reviews the requests ApprovalPolicy asks
	// for, such as "auto_review"; empty leaves the harness default.
	ApprovalsReviewer string
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
	// UserText and UserItemID snapshot the latest native user item at request
	// arrival, before asynchronous tool execution can race later input.
	UserText   string
	UserItemID string
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
	// Repair marks a coordinator continuation with no business-write authority.
	Repair    bool
	ImageURLs []string
	Text      string
	// Model, ModelProvider and Effort override the defaults for this turn.
	Model         string
	ModelProvider string
	Effort        string
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
	EventTurnStarted       EventKind = "turn_started"
	EventTurnCompleted     EventKind = "turn_completed"
	EventContextUsage      EventKind = "context_usage"
	EventItemStarted       EventKind = "item_started"
	EventItemCompleted     EventKind = "item_completed"
	EventAgentMessageDelta EventKind = "agent_message_delta"
)

// Item types tend acts on. Other types pass through as they are.
const (
	ItemAgentMessage      = "agentMessage"
	ItemUserMessage       = "userMessage"
	ItemContextCompaction = "contextCompaction"
)

// Event is something that happened in a thread.
type Event struct {
	ContextUsage *protocol.AgentContextUsage
	Kind         EventKind
	TurnID       string
	// ItemID and ItemType describe the item of item events.
	ItemID   string
	ItemType string
	// Text is an agent message's text, or an incremental fragment for a delta.
	Text string
	// TurnStatus is the final status of a completed turn: completed,
	// interrupted or failed.
	TurnStatus string
	// Tool is set on the item events of a tool the agent used.
	Tool *ToolUse
	// Raw is the harness's own payload.
	Raw json.RawMessage
}

// ToolUse is a tool the agent used, as one item of its turn shows it: one
// of tend's tools, a shell command, an MCP tool, a web search and so on.
type ToolUse struct {
	// Name is the tool: the name of one of tend's or an MCP server's
	// tools, or the harness's own, such as shell or web_search.
	Name string
	// Input and Output show the call's arguments and result as text;
	// either may be empty.
	Input  string
	Output string
	Status protocol.ToolStatus
}

// ContextManager exposes native current-window usage and explicit compaction.
// Acceptance of Compact is not completion; the contextCompaction item confirms it.
type ContextManager interface {
	ContextUsage(context.Context) (protocol.AgentContextUsage, error)
	Compact(context.Context) error
}

// TurnReader reconciles an uncertain start against the original caller identity.
// Missing history is not proof that the turn did not execute.
type TurnReader interface {
	FindTurn(context.Context, string, string) (TurnReceipt, bool, error)
}
type TurnReceipt struct {
	ID       string
	Status   string
	Messages []Event
	// Items retains native user/assistant order for source attribution. Nil
	// means the reader did not provide that evidence, rather than empty history.
	Items []Event
	Raw   json.RawMessage
}
