package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
)

const WaitExternalTool = "wait_external"

var WaitExternalToolSpec = ToolSpec{Name: WaitExternalTool, Description: "Persist an external wait and yield this execution and its slot. Use for existing CI, review, question answers, Task completion or controlled upgrade. Include the existing target identity, why waiting is useful and the exact recovery position. The server observes the original operation and resumes this Task; do not sleep, poll, create a human decision, resubmit the operation or submit a result while handing off. Reuse the same wait_key and original content after receipt loss.", InputSchema: json.RawMessage(`{"type":"object","properties":{"wait_key":{"type":"string"},"kind":{"type":"string","enum":["checks","review","answer","task","upgrade"]},"target_id":{"type":"string"},"issue_id":{"type":"string"},"reason":{"type":"string"},"recovery":{"type":"string"},"deadline_at_ms":{"type":"integer"}},"required":["wait_key","kind","target_id","reason","recovery"],"additionalProperties":false}`)}

func (r *Results) waitExternal(ctx context.Context, c ToolCall) ToolResult {
	if r.Spec.WaitExternal == nil {
		return ToolResult{Text: "external waiting is unavailable"}
	}
	var p protocol.WaitExternalPayload
	if json.Unmarshal(c.Arguments, &p) != nil {
		return ToolResult{Text: "invalid external wait"}
	}
	v, err := r.Spec.WaitExternal(ctx, p)
	if err != nil {
		return ToolResult{Text: fmt.Sprintf("external wait outcome: %v; preserve the original wait_key and target after uncertainty", err)}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ToolResult{Text: "could not encode external wait receipt"}
	}
	return ToolResult{Success: true, Text: string(raw)}
}
func externalWaitRecovery(spec WorkerSpec) string {
	if spec.ExternalWaitContinuation == nil {
		return ""
	}
	raw, _ := json.Marshal(spec.ExternalWaitContinuation)
	return "\nThis Run resumes the same Task after a confirmed physical stop for an external wait. The durable condition, outcome and recovery position follow. Continue from these facts with read_context; never repeat unknown external operations. Session history is optional, not authority.\n" + string(raw) + "\n"
}
