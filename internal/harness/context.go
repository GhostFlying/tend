package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

const ReadContextTool = "read_context"

var ReadContextToolSpec = ToolSpec{
	Name:        ReadContextTool,
	Description: "Read saved Issue context and original materials, including complete retained source pages, historical facts, messages, tasks, artifacts, acceptances, questions, files, existing delivery facts and bound execution authority, Capability definitions/history, Agent declarations/history and current Task matching. Inputs are an optional issue_id selector, kind, exact id, after cursor and limit. Use kind issues to discover project Issues; issue_id selects an allowed related Issue instead of the default Task context. Follow pagination and sources_more instead of treating one page as complete history. No task-spec observation permission is required; reading does not change the task or execute an action.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"issue_id":{"type":"string"},"kind":{"type":"string"},"id":{"type":"string"},"after":{"type":"string"},"limit":{"type":"integer"}},"additionalProperties":false}`),
}

func (r *Results) readContext(ctx context.Context, call ToolCall) ToolResult {
	if r.Spec.ReadContext == nil {
		return ToolResult{Text: "Issue context reader is not available"}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &fields) != nil || fields == nil {
		return ToolResult{Text: "read_context takes an object with optional issue_id, kind, id, after and limit"}
	}
	var query protocol.IssueMaterialsQuery
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&query); err != nil {
		return ToolResult{Text: "read_context takes optional string issue_id, kind, id, after and integer limit"}
	}
	result, err := r.Spec.ReadContext(ctx, query)
	if err != nil {
		if failure, ok := errors.AsType[*protocol.Error](err); ok {
			return ToolResult{Text: failure.Error()}
		}
		return ToolResult{Text: "unknown_outcome: Issue context could not be read; network or server response is unconfirmed"}
	}
	body, err := json.Marshal(result)
	if err != nil {
		return ToolResult{Text: "Issue context could not be encoded"}
	}
	return ToolResult{Success: true, Text: string(body)}
}

func contextBrief(spec WorkerSpec) string {
	if spec.ReadContext == nil {
		return ""
	}
	return "Use read_context to independently find and read saved Issue context and original materials. spec.inputs is a starting snapshot, not a reading allowlist. You can read needed saved materials directly, including later answers, background sources, related tasks, reports and acceptance evidence. Use kind issues to discover project Issues; optional issue_id selects a permitted related Issue, while omitting it keeps the Task's default context. Select kind overview, sources, facts, messages, tasks, artifacts, acceptances, questions, files, deliveries, delivery_authorization or deployment; id selects an exact object; omit id for this Task's delivery_authorization. sources pages retain original messages including queued and withdrawn status; facts includes historical superseded and needs_review records. Capability kinds capabilities, agent_capabilities, agent_capability_history and task_capabilities expose definitions/history, Agent declarations/history and current Task matching; omit id for current Task matching. Follow more/next with after and limit for additional pages; the first page is not all history. A file's sources_more means additional retained provenance exists: read source pages instead of treating its listed sources as complete. Preserve source versions, hashes, observed_at/seq and current/history/withdrawn/queued status when assessing evidence. Use download_file with a returned or known file_id for verified original bytes. Reading context does not change the task agreement, treat a queued message as a new instruction, or execute a new business action.\n\n"
}
