package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// AskOwnerTool is a durable question, not a completed implementation or verdict.
const AskOwnerTool = "ask_owner"

var AskOwnerToolSpec = ToolSpec{
	Name:        AskOwnerTool,
	Description: "Ask the owner for a decision needed to continue this run. The call waits for the owner's answer; do not poll or submit a result/verdict merely because you are waiting. Choose a stable question_key and reuse it with identical content after interruption. Evidence must explain the investigation and why owner input is needed. Options are optional; the owner may answer freely.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"question_key":{"type":"string"},"question":{"type":"string"},"evidence":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}},"required":["question_key","question","evidence"],"additionalProperties":false}`),
}

func (r *Results) askOwner(ctx context.Context, call ToolCall) ToolResult {
	if r.Spec.AskOwner == nil {
		return ToolResult{Text: "owner questions are unavailable for this run"}
	}
	var payload protocol.AskOwnerPayload
	if err := json.Unmarshal(call.Arguments, &payload); err != nil || strings.TrimSpace(payload.QuestionKey) == "" || strings.TrimSpace(payload.Question) == "" || strings.TrimSpace(payload.Evidence) == "" {
		return ToolResult{Text: "question_key, question and evidence are required"}
	}
	question, err := r.Spec.AskOwner(ctx, payload)
	if err != nil {
		return ToolResult{Text: fmt.Sprintf("owner question did not finish: %v. After an interruption or uncertain outcome, reuse the same question_key and original content. After a definitive rejection, correct the request with a new question_key.", err)}
	}
	body, err := json.Marshal(question)
	if err != nil {
		return ToolResult{Text: "could not encode the owner answer"}
	}
	return ToolResult{Success: true, Text: string(body)}
}

// questionRecovery carries durable question identity into both a fresh brief and
// a continuation whose original native tool call may no longer exist.
func questionRecovery(spec WorkerSpec) string {
	if len(spec.Questions) == 0 {
		return ""
	}
	body, _ := json.Marshal(spec.Questions)
	return "\n\nOwner questions recorded for this run (original content and answers):\n" + string(body) +
		"\nFor an open question, call ask_owner again using exactly its question_key, question, evidence and options. Do not invent another key for the same question. " +
		"Answered questions retain the owner's answer; use that answer and do not ask it again. Cancelled questions do not authorize work."
}
