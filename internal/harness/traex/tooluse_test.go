package traex

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestToolUse(t *testing.T) {
	cases := []struct {
		name, itemType, raw string
		completed           bool
		want                *harness.ToolUse
	}{
		{"tend tool running", itemDynamicToolCall, `{"tool":"tasks_list","arguments":{ "include_finished": true },"status":"inProgress"}`, false,
			&harness.ToolUse{Name: "tasks_list", Input: `{"include_finished":true}`, Status: protocol.ToolRunning}},
		{"tend tool answered", itemDynamicToolCall, `{"tool":"tasks_list","arguments":{},"status":"completed","success":true,
			"contentItems":[{"type":"inputText","text":"no tasks"}]}`, true,
			&harness.ToolUse{Name: "tasks_list", Input: `{}`, Output: "no tasks", Status: protocol.ToolCompleted}},
		{"tend tool unsuccessful", itemDynamicToolCall, `{"tool":"tasks_read","arguments":null,"status":"completed","success":false}`, true,
			&harness.ToolUse{Name: "tasks_read", Status: protocol.ToolFailed}},
		{"mcp tool", itemMcpToolCall, `{"server":"docs","tool":"search","arguments":{"q":"x"},"status":"completed",
			"result":{"content":[{"type":"text","text":"found"},{"type":"image","data":"..."}]}}`, true,
			&harness.ToolUse{Name: "docs/search", Input: `{"q":"x"}`, Output: "found", Status: protocol.ToolCompleted}},
		{"mcp tool error", itemMcpToolCall, `{"server":"docs","tool":"search","arguments":{},"status":"failed","error":{"message":"down"}}`, true,
			&harness.ToolUse{Name: "docs/search", Input: `{}`, Output: "down", Status: protocol.ToolFailed}},
		{"command", itemCommand, `{"command":"git log -1","aggregatedOutput":"abc\n","exitCode":0,"status":"completed","commandActions":[],"cwd":"/w"}`, true,
			&harness.ToolUse{Name: "shell", Input: "git log -1", Output: "abc\n", Status: protocol.ToolCompleted}},
		{"command exit code", itemCommand, `{"command":"false","exitCode":1,"status":"completed","commandActions":[],"cwd":"/w"}`, true,
			&harness.ToolUse{Name: "shell", Input: "false", Status: protocol.ToolFailed}},
		{"command declined", itemCommand, `{"command":"rm -rf x","status":"declined","commandActions":[],"cwd":"/w"}`, true,
			&harness.ToolUse{Name: "shell", Input: "rm -rf x", Status: protocol.ToolFailed}},
		{"web search started", itemWebSearch, `{"query":"sqlite strict"}`, false,
			&harness.ToolUse{Name: "web_search", Input: "sqlite strict", Status: protocol.ToolRunning}},
		{"web search done", itemWebSearch, `{"query":"sqlite strict","results":[]}`, true,
			&harness.ToolUse{Name: "web_search", Input: "sqlite strict", Status: protocol.ToolCompleted}},
		{"sub-agent", itemCollabAgent, `{"tool":"spawnAgent","prompt":"look","status":"inProgress"}`, false,
			&harness.ToolUse{Name: "spawnAgent", Input: "look", Status: protocol.ToolRunning}},
		{"not a tool", harness.ItemAgentMessage, `{"text":"hi"}`, true, nil},
	}
	for _, c := range cases {
		got := toolUse(c.itemType, json.RawMessage(c.raw), c.completed)
		if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("%s: toolUse = %+v, want %+v", c.name, got, c.want)
		}
	}
}
