package harness

import (
	"context"
	"encoding/json"
	"strings"
)

type LocalTurns interface {
	Queue(context.Context, ToolCall, string) error
	Begin(context.Context, string, string) (string, string, error)
	Started(context.Context, string, string, string) error
	Root(context.Context, string, string) (string, error)
}

var ContinueRunTool = ToolSpec{Name: "continue_run", Description: "Continue your current assigned goal in another turn on this runner. Give the next concrete step. After this tool succeeds, finish this turn. Only use it for work you can do now under the same assignment. Finish normally when the goal is complete or waiting for external input.", InputSchema: json.RawMessage(`{"type":"object","properties":{"next_step":{"type":"string","minLength":1}},"required":["next_step"],"additionalProperties":false}`)}

func QueueContinuation(ctx context.Context, local LocalTurns, call ToolCall) ToolResult {
	var p struct {
		Next string `json:"next_step"`
	}
	if call.ThreadID == "" || call.TurnID == "" || call.CallID == "" || json.Unmarshal(call.Arguments, &p) != nil || strings.TrimSpace(p.Next) == "" {
		return ToolResult{Text: "The next step and original call identity are required."}
	}
	if err := local.Queue(ctx, call, p.Next); err != nil {
		return ToolResult{Text: err.Error()}
	}
	return ToolResult{Success: true, Text: "Next step saved for this Run. Finish the current turn."}
}
