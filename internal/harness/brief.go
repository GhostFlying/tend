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
	Inputs       []protocol.TaskInput `json:"inputs"`
	Goal         string               `json:"goal"`
	Scope        string               `json:"scope"`
	Constraints  []string             `json:"constraints"`
	Acceptance   []string             `json:"acceptance"`
	Deliverables []string             `json:"deliverables"`
	Delivery     string               `json:"delivery"`
}

// VerdictReminder is the turn input for an acceptance worker that ended
// its turn without a verdict tend received.
const VerdictReminder = "tend did not receive a verdict from you. Call " + VerdictTool + " now with your verdict and evidence, " +
	"even if you called it before: that call may have been lost. Do not repeat the review unless you have to."

// HandBack is the turn input that returns a worker's agent to tend after
// managed control was interrupted (D67). Handback does not prove the owner
// actually changed the terminal: automatic detection may have been mistaken.
func HandBack(spec WorkerSpec) string {
	s := "The owner has handed the task back to tend in this terminal. " +
		"Reconcile the working trees and any instructions already received, then finish the same task. " +
		"Preserve completed work and checks; do not repeat them merely to resubmit a result."
	if spec.Role == protocol.RoleAccept {
		s += " End by calling " + VerdictTool + " with your verdict, even if you called it before."
	}
	return s + "\n" + observationBrief(spec) + questionRecovery(spec)
}

// IsHandBack recognizes the exact saved turn input for a handback. Journals can
// retain the previous wording across upgrades after a lost send receipt; replay
// that original request without issuing another handback under a new identity.
// This reads a historical local journal format, not an old wire protocol.
func IsHandBack(spec WorkerSpec, input string) bool {
	if input == HandBack(spec) {
		return true
	}
	s := "The owner worked with you in this terminal and has handed the task back to tend. " +
		"Check the state of the working trees and anything the owner changed or asked for, then finish the same task."
	if spec.Role == protocol.RoleAccept {
		s += " End by calling " + VerdictTool + " with your verdict, even if you called it before."
	}
	return input == s+"\n"+observationBrief(spec)+questionRecovery(spec)
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
	return s + "\n" + observationBrief(spec) + questionRecovery(spec)
}

// Brief returns a worker's developer instructions and its turn input. The
// spec's own words are passed on in full: workers get the complete
// requirements, never a summary (v0.1 §16).
func Brief(spec WorkerSpec) (instructions, input string) {
	var s briefSpec
	_ = json.Unmarshal(spec.Spec, &s)
	var b strings.Builder
	if spec.AskOwner != nil {
		b.WriteString("When owner judgment is required, use ask_owner with investigation evidence and a stable question_key. Wait for its answer in this same task; asking does not complete your work. Do not use an implementation result or acceptance verdict just to ask a question.\n\n")
	}
	b.WriteString(observationBrief(spec))
	b.WriteString(questionRecovery(spec))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Goal:\n%s\n", s.Goal)
	if len(spec.Relations) > 0 {
		b.WriteString("\nTask references (provenance only; use this task spec and configured workspace base):\n")
		for _, r := range spec.Relations {
			fmt.Fprintf(&b, "- %s %s: %s (%s), spec %d, %s\n", r.Kind, r.Direction, r.Task.TaskID, r.Task.Title, r.Task.SpecVersion, r.Task.Lifecycle)
		}
	}
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
	if len(s.Deliverables) > 0 {
		fmt.Fprintf(&b, "\nRequired deliverables: %s\n", strings.Join(s.Deliverables, ", "))
	} else if s.Delivery != "" {
		fmt.Fprintf(&b, "\nRequired deliverable: %s\n", s.Delivery)
	}
	if len(spec.Baseline) > 0 {
		b.WriteString("\nTask starting commits (use these for task-scoped commit and diff checks):\n")
		for _, m := range spec.Baseline {
			fmt.Fprintf(&b, "- %s: task starts at %s; resource original base %s.\n", m.Path, m.StartHEAD, m.ResourceBase)
		}
	}
	if len(s.Inputs) > 0 {
		b.WriteString("\nOriginal task inputs (separate from submitted deliverables):\n")
		for _, file := range s.Inputs {
			fmt.Fprintf(&b, "- file_id: %s; name: %q; media_type: %s; size: %d; sha256: %s; source_message_id: %s\n", file.FileID, file.Name, file.MediaType, file.Size, file.SHA256, file.SourceMessageID)
		}
		b.WriteString("Use download_file with these file IDs to obtain verified local copies. The original bytes are preserved, including every GIF frame; choose the appropriate reader for the task. Do not look for owner credentials or use chat attachments_read: the run's download_file handles access.\n")
	}
	if len(spec.Artifact) > 0 {
		fmt.Fprintf(&b, "\nSubmitted artifact (report and file references included):\n%s\n", string(spec.Artifact))
	}
	common := "You work in " + spec.Dir + ". Its AGENTS.md describes the workspace: one sub-directory per repository. " +
		"Read each repository's own AGENTS.md before working in it. Keep temporary files in " + spec.ScratchDir + ". "
	if spec.Guide != "" {
		common = "You work in " + spec.Dir + ".\n\n" + spec.Guide + "\nKeep temporary files in " + spec.ScratchDir + ". "
	}
	if spec.Role == protocol.RoleAccept {
		instructions = "You are an independent reviewer for tend. " + common +
			"The workspace is the task's own, holding the work to review exactly as submitted. Do not fix it: judge it. " +
			"You may build and run tests, but do not edit code, clean the working trees, commit or otherwise change git history. " +
			"For operational tasks verify resulting state and evidence without repeating mutations. Use download_file for submitted files. " +
			"Run whatever the acceptance checks need, then call " + VerdictTool + " exactly once with your verdict and evidence. " +
			"If you cannot run the checks because the environment fails, the verdict is blocked, not changes_required."
		input = "Review the submitted work against this task.\n\n" + b.String()
		return instructions, input
	}
	instructions = "You implement a task for tend. " + common +
		"Change the files in the working trees so the goal is met and the acceptance checks pass; run them yourself. " +
		"Whether and how to commit is yours to decide, as the task and each repository's own rules ask; tend does not commit for you, " +
		"and records each repository's HEAD and working tree as you leave them. " +
		"An independent reviewer will check your work. For report or file delivery, call submit_result with the requested report and explicitly selected files before ending your turn. Report-only work does not require Git changes. State evidence and uncertainties honestly."
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
