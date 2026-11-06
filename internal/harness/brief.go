package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
      "description": "passed: every required check holds. changes_required: a required outcome or quality gate is unmet, including a check that ran and failed; the precise cause may still need diagnosis. blocked: a demonstrated external prerequisite prevents checking. inconclusive: the available evidence cannot establish whether a requirement holds; do not assume an environment failure."},
    "evidence": {"type": "object", "description": "What you ran, its result, and the evidence for any unavailable prerequisite or remaining uncertainty. Separate observed failure from its cause."},
    "issues": {"type": "array", "items": {"type": "string"}, "description": "For changes_required: each unmet requirement and the necessary diagnosis or repair. A known product defect is not required when an agreed quality gate failed."}
  },
  "required": ["verdict", "evidence"],
  "additionalProperties": false
}`),
}

// Verdicts records the verdict an acceptance worker reports through
// VerdictTool. Legacy calls retain the last valid verdict; negotiated evidence
// submissions preserve the original acknowledged verdict and its sealed input.
type Verdicts struct {
	StateDir string
	Spec     WorkerSpec
	// RequireBinding prevents acknowledgments before a native send is journaled.
	RequireBinding bool
	binding        string
	nativeBinding  string
	sealedIdentity string
	mu             sync.Mutex
	verdict        *protocol.VerdictPayload
}

// Bind selects the original execution scope. A new native turn cannot inherit
// the preceding turn's verdict. It is independent of worker attempt/authority.
func (v *Verdicts) Bind(binding string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.binding != binding {
		v.binding, v.verdict, v.nativeBinding, v.sealedIdentity = binding, nil, "", ""
	}
}

type boundVerdict struct {
	Binding        string                  `json:"binding"`
	NativeBinding  string                  `json:"native_binding,omitempty"`
	SealedIdentity string                  `json:"sealed_identity,omitempty"`
	Payload        protocol.VerdictPayload `json:"payload"`
}

// Handle answers a call of VerdictTool.
func (v *Verdicts) Handle(ctx context.Context, c ToolCall) ToolResult {
	if c.Tool != VerdictTool {
		return ToolResult{Text: "unknown tool"}
	}
	p, selections, canonical, err := decodeVerdictCall(c.Arguments, v.Spec.AcceptanceEvidenceFiles)
	if err != nil {
		return ToolResult{Text: err.Error()}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.RequireBinding && v.binding == "" {
		return ToolResult{Text: "original verdict execution is not journaled"}
	}
	if !v.Spec.AcceptanceEvidenceFiles && v.stateDir() != "" {
		pending, err := v.readPendingVerdict()
		if err != nil || pending != nil {
			return ToolResult{Text: "original evidence submission must be reconciled before another verdict"}
		}
	}
	if v.Spec.AcceptanceEvidenceFiles {
		binding := v.binding
		if binding == "" && c.ThreadID != "" && c.TurnID != "" {
			raw, _ := json.Marshal(struct{ ThreadID, TurnID string }{c.ThreadID, c.TurnID})
			binding = string(raw)
		}
		if err := v.recordFileVerdict(ctx, binding, canonical, selections, &p); err != nil {
			return ToolResult{Text: fmt.Sprintf("verdict was not recorded: %v", err)}
		}
		v.nativeBinding = binding
	} else if err := v.persistVerdict(p, "", ""); err != nil {
		return ToolResult{Text: fmt.Sprintf("verdict was not recorded: %v", err)}
	}
	v.verdict = &p
	return ToolResult{Success: true, Text: "verdict recorded"}
}

// Restore retains an acknowledged verdict across attempts of the same run.
func (v *Verdicts) Restore() error {
	return v.RestoreContext(context.Background())
}

// RestoreContext reconciles an unfinished original submission before a worker
// can start another native turn. It never reopens a selection under a new input.
func (v *Verdicts) RestoreContext(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.verdict, v.nativeBinding, v.sealedIdentity = nil, "", ""
	if v.stateDir() == "" {
		return nil
	}
	if err := v.restorePendingVerdict(ctx); err != nil {
		return fmt.Errorf("%w: restore original verdict submission: %w", ErrRetainedResumePending, err)
	}
	raw, err := os.ReadFile(filepath.Join(v.stateDir(), "verdict.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read original verdict: %w", err)
	}
	var p protocol.VerdictPayload
	var record boundVerdict
	if err := json.Unmarshal(raw, &record); err != nil {
		return fmt.Errorf("decode original verdict: %w", err)
	}
	if v.binding != "" || record.NativeBinding != "" {
		if record.Binding != v.binding {
			return nil
		}
		p = record.Payload
		v.nativeBinding, v.sealedIdentity = record.NativeBinding, record.SealedIdentity
	} else if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decode original verdict: %w", err)
	}
	if !p.Verdict.Valid() {
		return fmt.Errorf("original verdict is invalid")
	}
	if err := validateVerdictManifests(p.Files, nil); err != nil {
		return fmt.Errorf("original verdict files are invalid: %w", err)
	}
	v.verdict = &p
	return nil
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
	return s + "\n" + observationBrief(spec) + messageBrief(spec) + questionRecovery(spec)
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
	legacy := s + "\n" + observationBrief(spec)
	return input == legacy+questionRecovery(spec) || input == legacy+messageBrief(spec)+questionRecovery(spec)
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
	return s + "\n" + observationBrief(spec) + messageBrief(spec) + questionRecovery(spec)
}

// Brief returns a worker's developer instructions and its turn input. The
// spec's own words are passed on in full: workers get the complete
// requirements, never a summary (v0.1 §16).
func Brief(spec WorkerSpec) (instructions, input string) {
	defer func() {
		if spec.RoleInstructions != "" {
			instructions += "\n\nNamed agent instructions (apply within the task and run-role contract above):\n" + spec.RoleInstructions
		}
	}()
	var s briefSpec
	_ = json.Unmarshal(spec.Spec, &s)
	var b strings.Builder
	if spec.AskOwner != nil {
		if spec.QuestionHandoffs {
			b.WriteString("Use ask_owner to persist a blocking problem and hand it to the automatically selected upper Issue owner. This ends the current run and releases its slot. Do not continue working or submit a result/verdict after handing off. The responsible answer schedules a new run with the original agreement, question, evidence and answer; it grants no new user authorization.\n\n")
		} else if spec.SupervisorQuestions {
			b.WriteString("Submit blocking problems only to your immediate Issue supervisor through ask_owner, using evidence and a stable question_key. That supervisor can be an Agent and escalates unresolved problems through the responsibility chain. Never choose a human recipient or directly request human intervention. An answer stays within the frozen task and grants no new user authorization. Wait in this run; asking is not a result or acceptance verdict.\n\n")
		} else {
			b.WriteString("When owner judgment is required, use ask_owner with investigation evidence and a stable question_key. Wait for its answer in this same task; asking does not complete your work. Do not use an implementation result or acceptance verdict just to ask a question.\n\n")
		}
	}
	b.WriteString(observationBrief(spec))
	b.WriteString(messageBrief(spec))
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
	if spec.Execution != nil {
		fmt.Fprintf(&b, "\nRequested acceptance execution: model=%s, effort=%s (empty means native model default). This is requested configuration, not independent proof of the running model.\n", spec.Execution.Model, spec.Execution.Effort)
	}
	if spec.Role == protocol.RoleAccept {
		instructions = "You are an independent reviewer for tend. " + common +
			"The workspace is the task's own, holding the work to review exactly as submitted. Do not fix it: judge it. " +
			"You may build and run tests, but do not edit code, clean the working trees, commit or otherwise change git history. " +
			"For operational tasks verify resulting state and evidence without repeating mutations. Use download_file for submitted files. " +
			"Run whatever the acceptance checks need, then call " + VerdictTool + " exactly once with your verdict and evidence. " +
			"When a required check ran and failed, report changes_required with the unmet requirement and the necessary diagnosis or repair, even if its precise cause is unknown. Do not infer a product defect from an exit code alone. " +
			"Use blocked only when evidence establishes an external prerequisite that prevents checking; name that prerequisite and the next action. " +
			"Use inconclusive when the evidence cannot establish whether a requirement holds; state the uncertainty and the next investigation. Unknown cause alone does not turn an observed unmet quality gate into an environment failure."
		if spec.AcceptanceEvidenceFiles {
			instructions += " Acceptance scratch is temporary and deleted after acknowledged final submission. Preserve evidence by explicitly selecting ordinary files with submit_verdict files: [{path, name}], for any verdict including blocked. Tend seals those bytes and confirms durable file references before acknowledging the verdict. Paths mentioned only in evidence JSON do not preserve files. Do not resubmit changed bytes or a different selection while an original submission is awaiting reconciliation."
		}
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
		if len(spec.Feedback.Files) > 0 {
			input += "\nPreserved evidence files from acceptance " + spec.Feedback.AcceptanceID + ":\n"
			for _, file := range spec.Feedback.Files {
				input += fmt.Sprintf("- file_id: %s; name: %q; size: %d; sha256: %s\n", file.FileID, file.Name, file.Size, file.SHA256)
			}
			input += "Use download_file with only the explicitly listed file IDs to obtain verified local copies. Paths inside evidence JSON are observations, not downloadable file references.\n"
		}
	}
	if spec.Attempt > 1 {
		input += "\nAn earlier attempt at this run was interrupted; the working trees may already hold some of its changes. Check them before continuing.\n"
	}
	return instructions, input
}
