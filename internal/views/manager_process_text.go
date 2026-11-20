package views

import (
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
)

// projectManagerProcessEvent exposes the collected execution timeline through
// the existing Run window. Tool payloads and user inputs stay in their
// original read paths. Only received replies contribute text here.
func projectManagerProcessEvent(row *protocol.RunLogView) error {
	var event coordination.Event
	if err := json.Unmarshal(row.Body, &event); err != nil {
		return err
	}
	body := map[string]any{"thread_id": event.ThreadID}
	data := map[string]any{}
	switch {
	case event.Disconnected:
		row.Kind = "native.disconnected"
	case event.Native != nil:
		native := event.Native
		row.Kind, row.NativeID = string(native.Kind), native.ItemID
		body["turn_id"], data["type"] = native.TurnID, native.ItemType
		switch native.Kind {
		case harness.EventAgentMessageDelta:
			data["type"], data["text"] = "agentMessageFragment", native.Text
		case harness.EventItemCompleted:
			if native.ItemType == harness.ItemAgentMessage {
				data["text"] = native.Text
			}
		case harness.EventTurnCompleted:
			data["status"] = native.TurnStatus
		}
		if native.Tool != nil {
			data["tool"], data["status"] = native.Tool.Name, native.Tool.Status
		}
	default:
		row.Kind = "native.event"
	}
	if len(event.ProcessText) != 0 {
		text := make([]string, 0, len(event.ProcessText))
		truncated := false
		for _, fragment := range event.ProcessText {
			text = append(text, fragment.Text)
			truncated = truncated || fragment.Truncated
		}
		data["type"], data["text"] = "agentMessageFragment", strings.Join(text, "\n\n")
		if truncated {
			data["truncated"] = true
		}
	}
	body["data"] = data
	body["kind"] = row.Kind
	var err error
	row.Body, err = json.Marshal(body)
	return err
}
