package mainthread

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/tools"
)

// hostedSpecs decorates only hosted tools. Worker and addon schemas are unchanged.
func hostedSpecs() []harness.ToolSpec {
	specs := tools.Specs()
	for i := range specs {
		if backgroundTool(specs[i].Name) {
			continue
		}
		specs[i] = tools.HostedOwnerSourceSpec(specs[i])
	}
	return specs
}

// selectOwnerSource removes the hosted-only field before strict business argument
// decoding. The native callback's immutable binding is recorded after selection.
func (m *Manager) selectOwnerSource(ctx context.Context, c *tools.Call) error {
	if backgroundTool(c.Tool) {
		return nil
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(c.Arguments, &args) != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "tool arguments must be an object")
	}
	raw, selected := args["source_message_id"]
	if !selected {
		return nil
	}
	if c.SourceMessageID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "source selection requires a known message delivery")
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || id == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "source_message_id must identify owner input")
	}
	msg, err := m.app.Message(ctx, id)
	if err != nil || msg.Author != "owner" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "source must identify an owner message")
	}
	withdrawn, err := m.app.MessageWithdrawn(ctx, id)
	if err != nil {
		return err
	}
	if withdrawn {
		return protocol.Errorf(protocol.CodeInvalidRequest, "source was withdrawn")
	}
	if id != c.SourceMessageID {
		requestID, err := m.app.OwnerMessageDelivery(ctx, id, c.Principal.Thread, c.TurnID, c.Tool == "decisions_resolve")
		if err != nil {
			return err
		}
		c.SourceMessageID, c.ParentRequestID = id, requestID
	}
	delete(args, "source_message_id")
	c.Arguments, err = json.Marshal(args)
	return err
}

func backgroundTool(name string) bool {
	return name == "background_sources" || name == "background_checkin"
}
