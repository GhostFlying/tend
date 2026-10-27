// Package traex drives a traex app-server over stdio and implements the
// harness session for it.
//
// Only the messages tend uses are written out here, by hand (spike A8). A
// test checks them against the subset of traex's generated JSON Schema kept
// in generated/, so a traex upgrade that changes them fails loudly.
package traex

import "encoding/json"

// Request methods and notifications tend sends or handles.
const (
	methodInitialize        = "initialize"
	methodInitialized       = "initialized"
	methodThreadStart       = "thread/start"
	methodThreadResume      = "thread/resume"
	methodThreadInject      = "thread/inject_items"
	methodTurnStart         = "turn/start"
	methodTurnInterrupt     = "turn/interrupt"
	methodTurnSteer         = "turn/steer"
	methodModelList         = "model/list"
	methodToolCall          = "item/tool/call"
	notifyTurnStarted       = "turn/started"
	notifyTurnCompleted     = "turn/completed"
	notifyItemStarted       = "item/started"
	notifyItemCompleted     = "item/completed"
	notifyAgentMessageDelta = "item/agentMessage/delta"
	toolContentInputText    = "inputText"
	userInputText           = "text"
	dynamicToolTypeFunc     = "function"
	developerMessageRole    = "developer"
	developerContentInput   = "input_text"
)

// InitializeParams opens a connection.
type InitializeParams struct {
	ClientInfo   ClientInfo              `json:"clientInfo"`
	Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
}

// ClientInfo names the client.
type ClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title"`
	Version string  `json:"version"`
}

// InitializeCapabilities opts into experimental API such as dynamic tools.
type InitializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi"`
}

// ThreadStartParams creates a thread.
type ThreadStartParams struct {
	ExperimentalRawEvents bool              `json:"experimentalRawEvents"`
	Model                 string            `json:"model,omitempty"`
	Cwd                   string            `json:"cwd,omitempty"`
	ApprovalPolicy        string            `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer     string            `json:"approvalsReviewer,omitempty"`
	Sandbox               string            `json:"sandbox,omitempty"`
	DeveloperInstructions string            `json:"developerInstructions,omitempty"`
	Config                map[string]any    `json:"config,omitempty"`
	DynamicTools          []DynamicToolSpec `json:"dynamicTools,omitempty"`
}

// ThreadResumeParams reopens a persisted thread.
type ThreadResumeParams struct {
	ThreadID              string         `json:"threadId"`
	Model                 string         `json:"model,omitempty"`
	Cwd                   string         `json:"cwd,omitempty"`
	ApprovalPolicy        string         `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer     string         `json:"approvalsReviewer,omitempty"`
	Sandbox               string         `json:"sandbox,omitempty"`
	DeveloperInstructions string         `json:"developerInstructions,omitempty"`
	Config                map[string]any `json:"config,omitempty"`
}

// ThreadStartResponse answers thread/start and thread/resume.
type ThreadStartResponse struct {
	Thread             Thread         `json:"thread"`
	Model              string         `json:"model"`
	ModelProvider      string         `json:"modelProvider"`
	ReasoningEffort    *string        `json:"reasoningEffort"`
	InstructionSources []string       `json:"instructionSources"`
	ApprovalPolicy     string         `json:"approvalPolicy"`
	ApprovalsReviewer  string         `json:"approvalsReviewer"`
	Sandbox            map[string]any `json:"sandbox"`
}

// Thread is the harness's thread record.
type Thread struct {
	ID   string  `json:"id"`
	Path *string `json:"path"`
}

// DynamicToolSpec registers a tool tend implements (the function variant).
type DynamicToolSpec struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ThreadInjectItemsParams appends raw Responses API items to a thread.
type ThreadInjectItemsParams struct {
	ThreadID string            `json:"threadId"`
	Items    []json.RawMessage `json:"items"`
}

// TurnStartParams starts a turn.
type TurnStartParams struct {
	SandboxPolicy map[string]any `json:"sandboxPolicy,omitempty"`
	ThreadID      string         `json:"threadId"`
	Input         []UserInput    `json:"input"`
	UserMessageID string         `json:"userMessageId,omitempty"`
	Model         string         `json:"model,omitempty"`
	ModelProvider string         `json:"modelProvider,omitempty"`
	Effort        string         `json:"effort,omitempty"`
}

// ModelListParams pages the visible model catalog without changing settings.
type ModelListParams struct {
	Cursor        *string `json:"cursor,omitempty"`
	Limit         uint32  `json:"limit,omitempty"`
	IncludeHidden bool    `json:"includeHidden"`
}

// ModelListResponse is one page of models.
type ModelListResponse struct {
	Data       []CatalogModel `json:"data"`
	NextCursor *string        `json:"nextCursor"`
}

// CatalogModel keeps identity, routing and reasoning capabilities separate.
type CatalogModel struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	ModelProviderID           *string                 `json:"modelProviderId"`
	IsDefault                 bool                    `json:"isDefault"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
}

// ReasoningEffortOption is a reasoning setting the model supports.
type ReasoningEffortOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
}

// AgentMessageDeltaNotification carries an ordered fragment of one item.
type AgentMessageDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// UserInput is one piece of user input (the text variant).
type UserInput struct {
	imageURL     string
	Type         string `json:"type"`
	Text         string `json:"text"`
	TextElements []any  `json:"text_elements"`
}

// ImageUserInput is the native image URL variant (including data URLs).
type ImageUserInput struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// MarshalJSON preserves the native tagged union without text fields on images.
func (in UserInput) MarshalJSON() ([]byte, error) {
	if in.Type == "image" {
		return json.Marshal(ImageUserInput{Type: in.Type, URL: in.imageURL})
	}
	type textInput UserInput
	return json.Marshal(textInput(in))
}

// TurnStartResponse answers turn/start.
type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

// Turn is the harness's turn record.
type Turn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// TurnInterruptParams stops a turn.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// DynamicToolCallParams is the server's request to run one of tend's
// tools.
type DynamicToolCallParams struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Namespace *string         `json:"namespace"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// DynamicToolCallResponse answers a tool call.
type DynamicToolCallResponse struct {
	Success      bool               `json:"success"`
	ContentItems []ToolResponseItem `json:"contentItems"`
}

// ToolResponseItem is one piece of a tool's answer (the text variant).
type ToolResponseItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TurnNotification is turn/started or turn/completed.
type TurnNotification struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

// ItemNotification is item/started or item/completed.
type ItemNotification struct {
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId"`
	Item     json.RawMessage `json:"item"`
}

// ThreadItem is the part of an item tend reads.
type ThreadItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// Tool item types, and the parts of them tend reads to show what the
// agent did.
const (
	itemDynamicToolCall = "dynamicToolCall"
	itemMcpToolCall     = "mcpToolCall"
	itemCommand         = "commandExecution"
	itemWebSearch       = "webSearch"
	itemImageView       = "imageView"
	itemCollabAgent     = "collabAgentToolCall"
)

// DynamicToolCallItem is a call of one of tend's tools.
type DynamicToolCallItem struct {
	Tool         string             `json:"tool"`
	Arguments    json.RawMessage    `json:"arguments"`
	ContentItems []ToolResponseItem `json:"contentItems"`
	Success      *bool              `json:"success"`
	Status       string             `json:"status"`
}

// McpToolCallItem is a call of an MCP server's tool.
type McpToolCallItem struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *McpToolResult  `json:"result"`
	Error     *McpToolError   `json:"error"`
	Status    string          `json:"status"`
}

// McpToolResult is an MCP tool's result.
type McpToolResult struct {
	Content []McpContent `json:"content"`
	IsError *bool        `json:"isError"`
}

// McpContent is one part of an MCP tool's result; tend shows the text.
type McpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// McpToolError is a failed MCP tool call.
type McpToolError struct {
	Message string `json:"message"`
}

// CommandItem is a shell command.
type CommandItem struct {
	Command          string  `json:"command"`
	AggregatedOutput *string `json:"aggregatedOutput"`
	ExitCode         *int    `json:"exitCode"`
	Status           string  `json:"status"`
}

// WebSearchItem is a web search.
type WebSearchItem struct {
	Query string `json:"query"`
}

// ImageViewItem is the agent looking at an image file.
type ImageViewItem struct {
	Path string `json:"path"`
}

// CollabAgentItem is the agent working with one of its sub-agents.
type CollabAgentItem struct {
	Tool   string  `json:"tool"`
	Prompt *string `json:"prompt"`
	Status string  `json:"status"`
}

// RawResponseCompletedNotification is opt-in request-level usage. Metadata is
// retained only for supported counter presence checks; arbitrary metadata is
// never exposed as diagnostics or stored by this adapter.
type RawResponseCompletedNotification struct {
	ThreadID      string             `json:"threadId"`
	TurnID        string             `json:"turnId"`
	ResponseID    string             `json:"responseId"`
	Usage         *RequestTokenUsage `json:"usage"`
	UsageMetadata *struct {
		Metadata json.RawMessage `json:"metadata"`
	} `json:"usageMetadata"`
}

// RequestTokenUsage is a native request snapshot, not a thread total.
type RequestTokenUsage struct {
	Input      *int64 `json:"inputTokens"`
	CacheRead  *int64 `json:"cachedInputTokens"`
	CacheWrite *int64 `json:"cacheCreationInputTokens"`
	Output     *int64 `json:"outputTokens"`
	Total      *int64 `json:"totalTokens"`
}
