package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/GhostFlying/tend/internal/protocol"
)

// VerdictTool is the tool an acceptance worker reports its verdict with.
const VerdictTool = "submit_verdict"

// VerdictToolSpec describes VerdictTool.
var VerdictToolSpec = ToolSpec{
	Name:        VerdictTool,
	Description: "Report your acceptance verdict. Call it exactly once, at the end.",
	InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "verdict": {"type": "string", "enum": ["passed", "changes_required", "blocked", "inconclusive"],
      "description": "passed: every check holds. changes_required: the work falls short. blocked: you could not check because the environment failed. inconclusive: you could not reach a verdict for another reason."},
    "evidence": {"type": "object", "description": "What you ran and saw, briefly."},
    "issues": {"type": "array", "items": {"type": "string"}, "description": "For changes_required: each thing to fix."}
  },
  "required": ["verdict", "evidence"],
  "additionalProperties": false
}`),
}

// Verdicts records the verdict an acceptance worker reports through
// VerdictTool; the last valid call counts.
type Verdicts struct {
	mu      sync.Mutex
	verdict *protocol.VerdictPayload
}

// Handle answers a call of VerdictTool.
func (v *Verdicts) Handle(_ context.Context, c ToolCall) ToolResult {
	if c.Tool != VerdictTool {
		return ToolResult{Text: "unknown tool"}
	}
	var p protocol.VerdictPayload
	if err := json.Unmarshal(c.Arguments, &p); err != nil || !p.Verdict.Valid() {
		return ToolResult{Text: "the verdict must be passed, changes_required, blocked or inconclusive"}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.verdict = &p
	return ToolResult{Success: true, Text: "verdict recorded"}
}

// Verdict is the recorded verdict, nil when there is none.
func (v *Verdicts) Verdict() *protocol.VerdictPayload {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.verdict
}

// briefSpec is the part of a task spec a worker reads.
type briefSpec struct {
	Goal        string   `json:"goal"`
	Scope       string   `json:"scope"`
	Constraints []string `json:"constraints"`
	Acceptance  []string `json:"acceptance"`
}

// Continuation is the turn input that continues a worker's thread after
// its attempt was interrupted (D61). The thread holds the task and the
// work so far, so it only says what happened.
func Continuation(spec WorkerSpec) string {
	s := "Your work on this task was interrupted: tend or its runner stopped, or the task was paused. " +
		"The workspace is as it was when you stopped; anything running then was stopped too. " +
		"Check the state of the working trees, then continue with the same task where you left off."
	if spec.Role == protocol.RoleAccept {
		s += " End by calling " + VerdictTool + " with your verdict, even if you called it before the interruption: that call was lost."
	}
	return s
}

// Brief returns a worker's developer instructions and its turn input. The
// spec's own words are passed on in full: workers get the complete
// requirements, never a summary (v0.1 §16).
func Brief(spec WorkerSpec) (instructions, input string) {
	var s briefSpec
	_ = json.Unmarshal(spec.Spec, &s)
	var b strings.Builder
	fmt.Fprintf(&b, "Goal:\n%s\n", s.Goal)
	if s.Scope != "" {
		fmt.Fprintf(&b, "\nScope:\n%s\n", s.Scope)
	}
	if len(s.Constraints) > 0 {
		b.WriteString("\nConstraints:\n")
		for _, c := range s.Constraints {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	b.WriteString("\nAcceptance checks:\n")
	for _, c := range s.Acceptance {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	common := "You work in " + spec.Dir + ". Its AGENTS.md describes the workspace: one sub-directory per repository. " +
		"Read each repository's own AGENTS.md before working in it. Keep temporary files in " + spec.ScratchDir + ". "
	if spec.Role == protocol.RoleAccept {
		instructions = "You are an independent reviewer for tend. " + common +
			"The workspace is the task's own, holding the work to review exactly as submitted. Do not fix it: judge it. " +
			"You may build and run tests, but do not edit code, clean the working trees, commit or otherwise change git history. " +
			"Run whatever the acceptance checks need, then call " + VerdictTool + " exactly once with your verdict and evidence. " +
			"If you cannot run the checks because the environment fails, the verdict is blocked, not changes_required."
		input = "Review the submitted work against this task.\n\n" + b.String()
		return instructions, input
	}
	instructions = "You implement a task for tend. " + common +
		"Change the files in the working trees so the goal is met and the acceptance checks pass; run them yourself. " +
		"Whether and how to commit is yours to decide, as the task and each repository's own rules ask; tend does not commit for you, " +
		"and records each repository's HEAD and working tree as you leave them. " +
		"An independent reviewer will check your work, so say plainly in your final message what you changed and what you verified."
	input = "Implement this task.\n\n" + b.String()
	if spec.Feedback != nil {
		input += "\nThe last review asked for changes:\n" + string(spec.Feedback.Issues) + "\n"
		if len(spec.Feedback.Evidence) > 0 {
			input += "Its evidence:\n" + string(spec.Feedback.Evidence) + "\n"
		}
	}
	if spec.Attempt > 1 {
		input += "\nAn earlier attempt at this run was interrupted; the working trees may already hold some of its changes. Check them before continuing.\n"
	}
	return instructions, input
}
