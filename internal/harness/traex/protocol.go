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
	methodInitialize      = "initialize"
	methodInitialized     = "initialized"
	methodThreadStart     = "thread/start"
	methodThreadResume    = "thread/resume"
	methodThreadInject    = "thread/inject_items"
	methodTurnStart       = "turn/start"
	methodTurnInterrupt   = "turn/interrupt"
	methodToolCall        = "item/tool/call"
	notifyTurnStarted     = "turn/started"
	notifyTurnCompleted   = "turn/completed"
	notifyItemStarted     = "item/started"
	notifyItemCompleted   = "item/completed"
	toolContentInputText  = "inputText"
	userInputText         = "text"
	dynamicToolTypeFunc   = "function"
	developerMessageRole  = "developer"
	developerContentInput = "input_text"
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
	Thread             Thread   `json:"thread"`
	Model              string   `json:"model"`
	InstructionSources []string `json:"instructionSources"`
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
	ThreadID      string      `json:"threadId"`
	Input         []UserInput `json:"input"`
	UserMessageID string      `json:"userMessageId,omitempty"`
}

// UserInput is one piece of user input (the text variant).
type UserInput struct {
	Type         string `json:"type"`
	Text         string `json:"text"`
	TextElements []any  `json:"text_elements"`
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
