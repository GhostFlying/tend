package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

const ReadObservationTool = "read_observation"

var ReadObservationToolSpec = ToolSpec{
	Name:        ReadObservationTool,
	Description: "Read a fresh, server-authorized snapshot of this task's declared work items and deployment scope. No arguments. Reports ledger and process facts, not browser rendering or product acceptance.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
}

func observationScope(spec WorkerSpec) *protocol.ObservationScope {
	var s struct {
		Observation *protocol.ObservationScope `json:"observation"`
	}
	if json.Unmarshal(spec.Spec, &s) != nil {
		return nil
	}
	return s.Observation
}

// HasObservation requires both explicit spec permission and a run-bound reader.
func HasObservation(spec WorkerSpec) bool {
	return spec.ReadObservation != nil && observationScope(spec) != nil
}

func (r *Results) readObservation(ctx context.Context, c ToolCall) ToolResult {
	if !HasObservation(r.Spec) {
		return ToolResult{Text: "observation is not available for this task"}
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(c.Arguments, &args) != nil || args == nil || len(args) != 0 {
		return ToolResult{Text: "read_observation requires an empty object and accepts no arguments"}
	}
	snapshot, err := r.Spec.ReadObservation(ctx)
	if err != nil {
		// Transport errors may contain credentials or response bodies. Keep them out
		// of the worker's context; the server remains the authorization authority.
		return ToolResult{Text: "observation could not be read; current run authorization or server availability could not be confirmed"}
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return ToolResult{Text: "observation could not be encoded"}
	}
	return ToolResult{Success: true, Text: string(body)}
}

func observationBrief(spec WorkerSpec) string {
	if !HasObservation(spec) {
		return ""
	}
	scope, _ := json.Marshal(observationScope(spec))
	return fmt.Sprintf("Use read_observation with {} to independently refresh live server facts within this immutable observation scope: %s. Both implementers and reviewers must read their own fresh snapshot when the checks require current state. Cite its observed_at, seq, and source; distinguish ledger facts, actual process facts, and unavailable evidence. This tool does not prove browser rendering, interaction, or product acceptance. Do not search for owner credentials or expand this scope.\n\n", scope)
}
