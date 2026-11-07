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
	return fmt.Sprintf("Use read_observation with {} to independently refresh live server facts within this immutable observation scope: %s. For ordinary acceptance, optional shared_reviews exposes only this Task/spec/artifact in its existing authorized Issue scope, including genuine pending links before a formal round, candidate/provider/repository identity, requirement digest and remaining round budget. A prebound round names reviewer_role=accept and the frozen reviewer/profile; use submit_verdict evidence.delivery_review for actual whole-PR evidence, not report.shared_review. Missing bindings are unavailable evidence; never invent a digest or round. Both implementers and reviewers must read their own fresh snapshot when the checks require current state. Cite its observed_at, seq, and source; distinguish ledger facts, actual process facts, and unavailable evidence. When delivery_authorization is present, it reads this task's server-bound frozen candidate, original approval or policy, and current validity. Its provider.ci and shared_review blocks preserve the actual head's checks and formal review's repository check and original task/acceptance provenance; missing, failed or unknown facts are not success. Use these server facts without repeating native status queries solely to obtain them. The create-request delivery_authorization is not a task-spec field: do not use owner task CLI or search for owner credentials to find it. A missing block on an older server is unavailable evidence, not permission. Check candidate identities and independent provider mutation receipts; this snapshot is not a provider mutation ledger. For merge-only work, deployment proof_status=unconfirmed is not itself a merge denial; read the actual admission controls and scoped host facts separately. Published recovery release is not proof that unknown runs stopped. This tool does not prove browser rendering, interaction, or product acceptance. Do not search for owner credentials or expand this scope.\n\n", scope)
}
