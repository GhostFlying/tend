package protocol

import (
	"encoding/json"
	"slices"
)

// Wire types of the agent add-on (D45): the requests the tend CLI's hook
// command and MCP bridge make for an agent the owner works in.

// AddonEnv turns the agent add-on off when set to "off". tend-runner sets
// it for the workers it starts: they may inherit the owner's user-level
// MCP and hook configuration, and must not act as the owner's agent.
const AddonEnv = "TEND_ADDON"

// AgentKind is a supported agent.
type AgentKind string

// Supported agents.
const (
	AgentClaude AgentKind = "claude"
	AgentCodex  AgentKind = "codex"
	AgentTraex  AgentKind = "traex"
)

// Agents lists the supported agents.
var Agents = []AgentKind{AgentClaude, AgentCodex, AgentTraex}

// Valid reports whether k is a supported agent.
func (k AgentKind) Valid() bool { return slices.Contains(Agents, k) }

// HookEvent is an agent hook tend answers.
type HookEvent string

// Hook events.
const (
	// HookSessionStart fires when a session starts, resumes, is cleared or
	// compacted, or is forked.
	HookSessionStart HookEvent = "session_start"
	// HookUserPromptSubmit fires before the agent handles the user's input.
	HookUserPromptSubmit HookEvent = "user_prompt_submit"
)

// Valid reports whether e is a known hook event.
func (e HookEvent) Valid() bool { return e == HookSessionStart || e == HookUserPromptSubmit }

// HookRequest is the body of POST /api/v1/agent/hook.
type HookRequest struct {
	RequestID string    `json:"request_id"`
	Agent     AgentKind `json:"agent"`
	Event     HookEvent `json:"event"`
	SessionID string    `json:"session_id"`
	// Source is what started the session, such as startup, resume or
	// compact, for session_start.
	Source string `json:"source,omitempty"`
	// Prompt is the user's input for user_prompt_submit. It is used for
	// this request only and never stored.
	Prompt string `json:"prompt,omitempty"`
}

// HookResponse is the context to add for the agent; empty when there is
// nothing to add.
type HookResponse struct {
	Context string `json:"context,omitempty"`
}

// ToolSpec describes a tool for an agent.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolRequest is the body of POST /api/v1/agent/tools/{name}.
type ToolRequest struct {
	RequestID string    `json:"request_id"`
	Agent     AgentKind `json:"agent"`
	// SessionID is the agent's session, when the bridge knows it.
	SessionID string          `json:"session_id,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResponse is a tool's answer for the model. Content is JSON text.
type ToolResponse struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
}
