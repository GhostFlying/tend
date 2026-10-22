package traex

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// toolUse reads the tool use an item shows, or returns nil for an item
// that is not one. completed tells item/completed from item/started, for
// items that carry no status of their own.
func toolUse(itemType string, raw json.RawMessage, completed bool) *harness.ToolUse {
	settled := protocol.ToolRunning
	if completed {
		settled = protocol.ToolCompleted
	}
	switch itemType {
	case itemDynamicToolCall:
		var it DynamicToolCallItem
		if json.Unmarshal(raw, &it) != nil {
			return nil
		}
		var out []string
		for _, c := range it.ContentItems {
			if c.Text != "" {
				out = append(out, c.Text)
			}
		}
		u := &harness.ToolUse{Name: it.Tool, Input: compact(it.Arguments), Output: strings.Join(out, "\n"), Status: toolStatus(it.Status)}
		if it.Success != nil && !*it.Success && u.Status == protocol.ToolCompleted {
			u.Status = protocol.ToolFailed
		}
		return u
	case itemMcpToolCall:
		var it McpToolCallItem
		if json.Unmarshal(raw, &it) != nil {
			return nil
		}
		u := &harness.ToolUse{Name: it.Server + "/" + it.Tool, Input: compact(it.Arguments), Status: toolStatus(it.Status)}
		switch {
		case it.Error != nil:
			u.Output, u.Status = it.Error.Message, protocol.ToolFailed
		case it.Result != nil:
			var out []string
			for _, c := range it.Result.Content {
				if c.Text != "" {
					out = append(out, c.Text)
				}
			}
			u.Output = strings.Join(out, "\n")
			if it.Result.IsError != nil && *it.Result.IsError && u.Status == protocol.ToolCompleted {
				u.Status = protocol.ToolFailed
			}
		}
		return u
	case itemCommand:
		var it CommandItem
		if json.Unmarshal(raw, &it) != nil {
			return nil
		}
		u := &harness.ToolUse{Name: "shell", Input: it.Command, Status: toolStatus(it.Status)}
		if it.AggregatedOutput != nil {
			u.Output = *it.AggregatedOutput
		}
		if it.ExitCode != nil && *it.ExitCode != 0 && u.Status == protocol.ToolCompleted {
			u.Status = protocol.ToolFailed
		}
		return u
	case itemWebSearch:
		var it WebSearchItem
		if json.Unmarshal(raw, &it) != nil {
			return nil
		}
		return &harness.ToolUse{Name: "web_search", Input: it.Query, Status: settled}
	case itemImageView:
		var it ImageViewItem
		if json.Unmarshal(raw, &it) != nil {
			return nil
		}
		return &harness.ToolUse{Name: "view_image", Input: it.Path, Status: settled}
	case itemCollabAgent:
		var it CollabAgentItem
		if json.Unmarshal(raw, &it) != nil {
			return nil
		}
		u := &harness.ToolUse{Name: it.Tool, Status: toolStatus(it.Status)}
		if it.Prompt != nil {
			u.Input = *it.Prompt
		}
		return u
	}
	return nil
}

// toolStatus maps an item's status. A command moved to the background is
// still running; one the reviewer declined failed.
func toolStatus(s string) protocol.ToolStatus {
	switch s {
	case "completed":
		return protocol.ToolCompleted
	case "failed", "declined":
		return protocol.ToolFailed
	}
	return protocol.ToolRunning
}

// compact shows JSON arguments on one line.
func compact(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}
