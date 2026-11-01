package domain

import (
	"github.com/GhostFlying/tend/internal/protocol"
)

// RunFact identifies the action a run executed for, as recorded on the run.
type RunFact struct {
	RunID       string
	Role        protocol.RunRole
	SpecVersion int64
	Generation  int64
	// ArtifactID is the artifact an acceptance run judges.
	ArtifactID string
}

// staleEvent records a fact that no longer matches the task's current
// version. It is kept as evidence but never triggers follow-up work.
func staleEvent(kind string, body map[string]any) EventDraft {
	body["stale"] = true
	return EventDraft{Kind: kind, Body: body}
}

// currentImplement reports whether the run executed the current implement
// generation of the current spec version.
func currentImplement(t Task, r RunFact) bool {
	return r.Role == protocol.RoleImplement && r.SpecVersion == t.SpecVersion && r.Generation == t.ImplementGeneration
}

// RunStarted records that an implement run started. Only the current
// implement generation moves a queued task, or one awaiting changes, to
// running.
func RunStarted(t Task, r RunFact) Decision {
	body := map[string]any{"run_id": r.RunID, "role": r.Role, "spec_version": r.SpecVersion, "generation": r.Generation}
	if r.Role != protocol.RoleImplement {
		return Decision{Task: t, Events: []EventDraft{{Kind: "run.started", Body: body}}}
	}
	if !currentImplement(t, r) || (t.Lifecycle != protocol.TaskQueued && t.Lifecycle != protocol.TaskChangesRequired) {
		return Decision{Task: t, Events: []EventDraft{staleEvent("run.started", body)}}
	}
	t.Lifecycle = protocol.TaskRunning
	return Decision{Task: t, Events: []EventDraft{{Kind: "run.started", Body: body}}}
}

// TakenOver records that the owner took over a run's agent (D67). The task
// of a current run (its implement generation, or the artifact under
// acceptance) passes to human control, which holds its runs until the
// owner hands it back; a stale run's takeover is only recorded.
func TakenOver(t Task, r RunFact, reason string) Decision {
	body := map[string]any{"run_id": r.RunID, "role": r.Role, "reason": reason}
	current := currentImplement(t, r)
	if r.Role == protocol.RoleAccept {
		current = r.SpecVersion == t.SpecVersion && r.ArtifactID == t.CurrentArtifactID && t.Lifecycle == protocol.TaskAwaitingAcceptance
	}
	if t.Lifecycle.Terminal() || !current {
		return Decision{Task: t, Events: []EventDraft{staleEvent("task.taken_over", body)}}
	}
	if t.Control == protocol.ControlHumanControl {
		return Decision{Task: t}
	}
	t.Control = protocol.ControlHumanControl
	t.ControlRevision++
	return Decision{Task: t, Events: []EventDraft{{Kind: "task.taken_over", Body: body}}}
}

// ArtifactSubmitted records an artifact produced by an implement run. When
// it belongs to the current spec version and generation, the task awaits
// acceptance and an independent acceptance action is derived.
func ArtifactSubmitted(t Task, r RunFact, artifactID string) Decision {
	body := map[string]any{"run_id": r.RunID, "artifact_id": artifactID, "spec_version": r.SpecVersion, "generation": r.Generation}
	if !currentImplement(t, r) || t.Lifecycle != protocol.TaskRunning {
		return Decision{Task: t, Events: []EventDraft{staleEvent("artifact.submitted", body)}}
	}
	t.Lifecycle = protocol.TaskAwaitingAcceptance
	t.CurrentArtifactID = artifactID
	return Decision{
		Task:    t,
		Events:  []EventDraft{{Kind: "artifact.submitted", Body: body}},
		Actions: []ActionDraft{{Kind: protocol.ActionAccept, TaskID: t.ID, SpecVersion: t.SpecVersion, ArtifactID: artifactID, Generation: 1}},
	}
}

// AcceptanceFact is a verdict submitted by an acceptance run.
type AcceptanceFact struct {
	RunID       string
	Role        protocol.RunRole
	SpecVersion int64
	ArtifactID  string
	Verdict     protocol.Verdict
}

// AcceptanceSubmitted applies an acceptance verdict. Only a verdict from an
// acceptance run, about the current spec version and the artifact under
// acceptance, can move the task; anything else is recorded as a stale fact.
func AcceptanceSubmitted(t Task, a AcceptanceFact) (Decision, error) {
	if a.Role != protocol.RoleAccept {
		return Decision{}, protocol.Errorf(protocol.CodeForbidden, "only acceptance runs may submit a verdict")
	}
	if !a.Verdict.Valid() {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown verdict %q", a.Verdict)
	}
	body := map[string]any{"run_id": a.RunID, "artifact_id": a.ArtifactID, "spec_version": a.SpecVersion, "verdict": a.Verdict}
	current := t.Lifecycle == protocol.TaskAwaitingAcceptance && a.SpecVersion == t.SpecVersion && a.ArtifactID == t.CurrentArtifactID
	if !current {
		return Decision{Task: t, Events: []EventDraft{staleEvent("acceptance.submitted", body)}}, nil
	}
	d := Decision{Events: []EventDraft{{Kind: "acceptance.submitted", Body: body}}}
	switch a.Verdict {
	case protocol.VerdictPassed:
		// The MVP's only delivery is the accepted commit itself, so a
		// current passing verdict satisfies every completion condition.
		t.Lifecycle = protocol.TaskCompleted
		t.Block = protocol.BlockNone
		d.Events = append(d.Events, EventDraft{Kind: "task.completed", Body: map[string]any{"artifact_id": a.ArtifactID, "spec_version": t.SpecVersion}})
	case protocol.VerdictChangesRequired:
		t.Lifecycle = protocol.TaskChangesRequired
		t.CurrentArtifactID = ""
		if t.ImplementGeneration < t.Budget {
			t.ImplementGeneration++
			t.Block = protocol.BlockNone
			d.Actions = append(d.Actions, implementAction(t))
		} else {
			t.Block = protocol.BlockBudgetExhausted
			d.OpenDecisions = append(d.OpenDecisions, budgetDecision(
				"实现次数已用完，验收仍要求修改。这个任务接下来怎么办？"))
		}
	case protocol.VerdictBlocked, protocol.VerdictInconclusive:
		// An unusable acceptance environment is neither a defect in the
		// artifact nor a pass.
		t.Block = protocol.BlockAcceptanceEnvUnavailable
		d.OpenDecisions = append(d.OpenDecisions, acceptanceDecision("验收未能得出结论。这个任务接下来怎么办？"))
	}
	d.Task = t
	return d, nil
}

// runTransitions lists the allowed observed-state changes of a run besides
// reconciliation out of unknown.
var runTransitions = map[protocol.RunObserved][]protocol.RunObserved{
	protocol.RunStarting:    {protocol.RunRunning, protocol.RunFailed, protocol.RunUnknown},
	protocol.RunRunning:     {protocol.RunWaiting, protocol.RunFinished, protocol.RunFailed, protocol.RunInterrupted, protocol.RunUnknown},
	protocol.RunWaiting:     {protocol.RunRunning, protocol.RunFinished, protocol.RunFailed, protocol.RunInterrupted, protocol.RunUnknown},
	protocol.RunInterrupted: {protocol.RunRunning, protocol.RunFailed, protocol.RunUnknown},
}

// RunTransitionAllowed reports whether a run may move between observed
// states. Finished and failed are terminal. Unknown is left only through
// reconciliation, which may establish any concrete state.
func RunTransitionAllowed(from, to protocol.RunObserved, reconciling bool) bool {
	if !from.Valid() || !to.Valid() || from == to {
		return false
	}
	if from == protocol.RunUnknown {
		return reconciling && to != protocol.RunUnknown
	}
	for _, next := range runTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// RunFailed applies the failure of a run that produced no result, such as a
// worker that crashed, made no changes or was given up by its runner.
// Without a background reasoning thread every such failure needs the owner
// (D31): a failed implementation waits for a decision on how to proceed,
// and a failed acceptance counts as an unusable acceptance environment,
// like a blocked verdict. Failures of runs that are no longer current are
// only recorded.
func RunFailed(t Task, r RunFact, reason string) Decision {
	body := map[string]any{"run_id": r.RunID, "role": r.Role, "spec_version": r.SpecVersion, "generation": r.Generation, "reason": reason}
	switch {
	case r.Role == protocol.RoleImplement && currentImplement(t, r) &&
		(t.Lifecycle == protocol.TaskQueued || t.Lifecycle == protocol.TaskRunning || t.Lifecycle == protocol.TaskChangesRequired):
		// Back to the queue, blocked until the owner decides; another
		// generation is an explicit request.
		t.Lifecycle = protocol.TaskQueued
		t.Block = protocol.BlockWaitingDecision
		return Decision{
			Task:          t,
			Events:        []EventDraft{{Kind: "run.failed", Body: body}},
			OpenDecisions: []DecisionDraft{failedImplementDecision("实现失败（" + failureReasonLabel(reason) + "）。这个任务接下来怎么办？")},
		}
	case r.Role == protocol.RoleAccept && t.Lifecycle == protocol.TaskAwaitingAcceptance &&
		r.SpecVersion == t.SpecVersion && r.ArtifactID == t.CurrentArtifactID:
		t.Block = protocol.BlockAcceptanceEnvUnavailable
		return Decision{
			Task:          t,
			Events:        []EventDraft{{Kind: "run.failed", Body: body}},
			OpenDecisions: []DecisionDraft{acceptanceDecision("验收失败（" + failureReasonLabel(reason) + "）。这个任务接下来怎么办？")},
		}
	}
	return Decision{Task: t, Events: []EventDraft{staleEvent("run.failed", body)}}
}

// RunPreparationFailed blocks an unstarted implementation for its current Issue
// owner to explicitly revise the contract. It never queues a retry.
func RunPreparationFailed(t Task, r RunFact) Decision {
	d := RunFailed(t, r, "workspace_prepare_failed")
	if currentImplement(t, r) && (t.Lifecycle == protocol.TaskQueued || t.Lifecycle == protocol.TaskRunning || t.Lifecycle == protocol.TaskChangesRequired) {
		d.Task.Block = protocol.BlockPreparationRequired
		d.OpenDecisions = nil
		d.Events = append(d.Events, EventDraft{Kind: "task.preparation_blocked", Body: map[string]any{"run_id": r.RunID, "spec_version": r.SpecVersion, "generation": r.Generation}})
	}
	return d
}

// failureReasonLabel describes runner facts without rewriting their reason codes.
func failureReasonLabel(reason string) string {
	switch reason {
	case "worker_failed":
		return "执行 Agent 异常退出"
	case "worker_start_failed":
		return "执行 Agent 启动失败"
	case "workspace_prepare_failed":
		return "工作目录准备失败"
	case "runner_error":
		return "执行主机发生错误"
	case "no_changes":
		return "未产生修改"
	case "no_artifact":
		return "未提交交付物"
	case "cancelled":
		return "执行已取消"
	case "interrupted":
		return "执行已中断"
	case "workspace_report_rejected":
		return "工作区信息未被接受"
	case "start_rejected":
		return "启动请求未被接受"
	case "owner_question_recovery_failed":
		return "待处理问题恢复失败"
	case "invalid_worker_result":
		return "执行结果不符合要求"
	case "missing_report":
		return "未提交执行报告"
	case "missing_files":
		return "未提交要求的文件"
	case "worker_ended":
		return "执行 Agent 已结束"
	default:
		return "执行未正常完成，请查看执行记录"
	}
}
