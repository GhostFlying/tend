package protocol

import (
	"encoding/json"
	"slices"
)

// Shared worker provider identifiers and hosted tool definitions.

// AddonEnv suppresses legacy installed Tend add-ons in child processes.
// External coordinator entry points have been retired (D114).
const AddonEnv = "TEND_ADDON"

// WorkerMarker is the file tend-runner keeps in the git directory of a
// worktree its TUI worker runs in (D66).
const WorkerMarker = "tend-worker.json"

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

// ToolSpec describes a tool for an agent.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
