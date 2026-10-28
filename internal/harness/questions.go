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
	Description: "Ask the owner for a decision needed to continue this run. The call waits for the owner's answer; do not poll or submit a result/verdict merely because you are waiting. Choose a stable question_key and reuse it with identical content after interruption. Evidence must explain the investigation and why owner input is needed. Separate environment faults from evidence gaps and owner choices. Explain the affected operation, remaining work, responsible actor and next action in the visible question; never bury a relevant fault only in evidence. Options are optional; the owner may answer freely.",
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
	// Handback journals retain exact prompt bytes. Keep legacy wording when
	// there are no extended fields to recover, including after an upgrade.
	fields := "question_key, question, evidence and options"
	for _, question := range spec.Questions {
		if len(question.Issues) > 0 {
			fields = "question_key, question, evidence, issues and options"
			break
		}
	}
	return "\n\nOwner questions recorded for this run (original content and answers):\n" + string(body) +
		"\nFor an open question, call ask_owner again using exactly its " + fields + ". Do not invent another key for the same question. " +
		"Answered questions retain the owner's answer; use that answer and do not ask it again. Cancelled questions do not authorize work."
}

// OwnerQuestionTool exposes the extension only after server negotiation.
func OwnerQuestionTool(contextAvailable bool) ToolSpec {
	tool := AskOwnerToolSpec
	if !contextAvailable {
		return tool
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		panic(err)
	}
	var issue map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"issue_id":{"type":"string"},"category":{"type":"string","enum":["environment","evidence","authorization"]},"summary":{"type":"string"},"impact":{"type":"string"},"next_action":{"type":"string"},"actor":{"type":"string","enum":["worker","main_agent","owner"]},"status":{"type":"string","enum":["open","resolved","workaround","deferred"]},"source":{"type":"string"}},"required":["issue_id","category","summary","impact","next_action","actor","status","source"],"additionalProperties":false}`), &issue); err != nil {
		panic(err)
	}
	schema["properties"].(map[string]any)["issues"] = map[string]any{"type": "array", "maxItems": 20, "items": issue}
	tool.InputSchema, _ = json.Marshal(schema)
	tool.Description += " Include issues for environment faults, evidence gaps and missing authorization, with stable issue_id, exact source, affected operation, status, responsible actor and next action. Keep actual owner choices in question/options."
	return tool
}
