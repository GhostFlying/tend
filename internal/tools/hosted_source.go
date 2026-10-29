package tools

import (
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// HostedOwnerSourceSpec keeps the existing optional selector on hosted tools.
// Worker/add-on schemas are unchanged; dispatch still checks actual admission.
func HostedOwnerSourceSpec(spec harness.ToolSpec) harness.ToolSpec {
	var schema map[string]any
	if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
		panic(fmt.Sprintf("tools: invalid hosted tool schema: %v", err))
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		schema["properties"] = props
	}
	description := "Optional actual owner message this action answers within the current native turn. Use its [message ID] when an earlier request is followed by a status question. Only current or consumed owner input from this turn is allowed; this does not grant authority or override newer constraints. Omit to use the latest native source. Keep commit_seq increasing within this turn."
	if spec.Name == "decisions_resolve" {
		description = "Optional actual admitted owner answer message ID. Use the earlier answer's [message ID] when a later status question arrives, or when recovering an already admitted answer. Queued, withdrawn or system input cannot answer decisions; this does not override newer owner constraints."
	}
	props["source_message_id"] = map[string]any{"type": "string", "minLength": 1, "description": description}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("tools: encode hosted tool schema: %v", err))
	}
	spec.InputSchema = encoded
	return spec
}
