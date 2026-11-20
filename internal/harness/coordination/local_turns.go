package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type LocalTurns interface {
	Queue(context.Context, harness.ToolCall, string) error
	Begin(context.Context, string, string) (string, string, error)
	Started(context.Context, string, string, string) error
	Root(context.Context, string, string) (string, error)
}

var continueRunTool = harness.ToolSpec{Name: "continue_run", Description: "Continue your current assigned goal in another turn on this runner. Give the next concrete step. After this tool succeeds, finish this turn. Only use it for work you can do now under the same assignment. Finish normally when the goal is complete or waiting for external input.", InputSchema: json.RawMessage(`{"type":"object","properties":{"next_step":{"type":"string","minLength":1}},"required":["next_step"],"additionalProperties":false}`)}

func (d *Driver) forwardNative(ctx context.Context, t harness.Thread, e harness.Event, local bool) error {
	event := Event{ThreadID: t.ID(), Native: &e}
	if !local || e.TurnID == "" {
		return d.emit(ctx, event)
	}
	var err error
	event.RootTurnID, err = d.LocalTurns.Root(ctx, t.ID(), e.TurnID)
	if err != nil {
		return err
	}
	var next, call string
	if e.Kind == harness.EventTurnCompleted && e.TurnStatus == "completed" {
		next, call, err = d.LocalTurns.Begin(ctx, t.ID(), e.TurnID)
		if err != nil {
			return err
		}
	}
	event.Continued = next != ""
	if err = d.emit(ctx, event); err != nil || next == "" {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	input := harness.TurnInput{Text: next, Model: d.Profile.Model, Effort: d.Profile.Effort,
		UserMessageID: protocol.NativeObjectID(protocol.PrefixMessage, t.ID(), e.TurnID+"/"+call)}
	id, err := t.StartTurn(ctx, input)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("local continuation returned no native turn identity")
	}
	return d.LocalTurns.Started(ctx, t.ID(), e.TurnID, id)
}
func (d *Driver) queueLocal(ctx context.Context, call harness.ToolCall) harness.ToolResult {
	var p struct {
		Next string `json:"next_step"`
	}
	if call.ThreadID == "" || call.TurnID == "" || call.CallID == "" || json.Unmarshal(call.Arguments, &p) != nil || strings.TrimSpace(p.Next) == "" {
		return harness.ToolResult{Text: "The next step and original call identity are required."}
	}
	if err := d.LocalTurns.Queue(ctx, call, p.Next); err != nil {
		return harness.ToolResult{Text: err.Error()}
	}
	return harness.ToolResult{Success: true, Text: "Next step saved for this Run. Finish the current turn."}
}
