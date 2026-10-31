package domain

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"strings"
)

// Options of the decision about an unavailable workspace: tend answers
// WaitForHostOption itself once the host is back. StartOverOption is
// available only for workspaces created by this task without a required host.
const (
	WaitForHostOption = optionWaitForHost
	StartOverOption   = optionStartOver
)

// WorkspaceUnavailable blocks a task whose workspace is on a host that is
// retired or has not been seen for too long, and asks the owner how to go
// on (D62). why says what happened to the host. Only a managed task that
// waits for nothing else is blocked: any other block already involves
// someone.
func WorkspaceUnavailable(t Task, hostID, why string) Decision {
	if t.Lifecycle.Terminal() || t.Control != protocol.ControlManaged || t.Block != protocol.BlockNone {
		return Decision{Task: t}
	}
	t.Block = protocol.BlockWorkspaceUnavailable
	return Decision{
		Task:          t,
		Events:        []EventDraft{{Kind: "task.workspace_unavailable", Body: map[string]any{"host_id": hostID, "reason": why}}},
		OpenDecisions: []DecisionDraft{workspaceQuestion(t, hostID, why)},
	}
}

func workspaceQuestion(t Task, hostID, why string) DecisionDraft {
	description := "当前不可用"
	if why == "was retired" {
		description = "已停用"
	} else if since, ok := strings.CutPrefix(why, "has not been heard from since "); ok {
		description = "从 " + since + " 起未连接"
	}
	d := workspaceDecision("任务工作区所在主机 " + hostID + " " + description + "，工作只能在该主机继续。这个任务接下来怎么办？")
	if t.WorkspaceReused {
		d.Question += " 此任务复用固定工作区，不能换主机重新开始。请等待主机恢复，或取消后使用新建或临时工作区（workspace.mode=new 或 temporary）创建任务。"
	}
	if t.RequiredHostID != "" {
		d.Question += " 此任务指定主机 " + t.RequiredHostID + "，不能换主机重新开始。请等待主机恢复，或取消后指定合适主机（requirements.host_id）并使用独立工作区创建任务。"
	}
	if t.WorkspaceReused || t.RequiredHostID != "" {
		d.Options = []string{optionWaitForHost, optionCancel}
		d.Effects = map[string]protocol.DecisionEffect{optionCancel: protocol.EffectCancel}
	}
	return d
}

// AskAboutWorkspaceAgain asks the owner again how a task blocked on its
// unavailable workspace should go on, after the earlier question went
// stale: its effects apply only to the task as it was when asked (D53).
func AskAboutWorkspaceAgain(t Task, hostID, why string) Decision {
	if t.Lifecycle.Terminal() || t.Control != protocol.ControlManaged || t.Block != protocol.BlockWorkspaceUnavailable {
		return Decision{Task: t}
	}
	return Decision{
		Task:          t,
		Events:        []EventDraft{{Kind: "task.workspace_unavailable", Body: map[string]any{"host_id": hostID, "reason": why, "asked_again": true}}},
		OpenDecisions: []DecisionDraft{workspaceQuestion(t, hostID, why)},
	}
}

// WorkspaceAvailable lifts the block of a task whose workspace host came
// back.
func WorkspaceAvailable(t Task, hostID string) Decision {
	if t.Block != protocol.BlockWorkspaceUnavailable {
		return Decision{Task: t}
	}
	t.Block = protocol.BlockNone
	return Decision{Task: t, Events: []EventDraft{{Kind: "task.workspace_available", Body: map[string]any{"host_id": hostID}}}}
}

// StartOver gives up a task's unavailable workspace and starts the next
// implement generation from the base in a new workspace on another host.
// What was done in the old workspace, including an artifact under
// acceptance, is left behind.
func StartOver(t Task, expectedRevision int64) (Decision, error) {
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if t.Lifecycle.Terminal() {
		return Decision{}, errTransition(t, "start over")
	}
	if t.Block != protocol.BlockWorkspaceUnavailable {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidTransition, "a task starts over only while its workspace is unavailable")
	}
	if t.WorkspaceReused {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidTransition, "a reused workspace is pinned to its original resource; wait for its host, or cancel and create a new task with workspace.mode=new or temporary")
	}
	if t.RequiredHostID != "" {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidTransition, "requirements.host_id pins this task to host %s; wait for that host, or cancel and create a new task with a suitable requirements.host_id and an independent workspace", t.RequiredHostID)
	}
	t.ControlRevision++
	t.ImplementGeneration++
	if t.Budget < t.ImplementGeneration {
		t.Budget = t.ImplementGeneration
	}
	t.Lifecycle = protocol.TaskQueued
	t.CurrentArtifactID = ""
	t.Block = protocol.BlockNone
	t.WorkspaceAssigned = false
	return Decision{
		Task:              t,
		Actions:           []ActionDraft{implementAction(t)},
		CancelOpenActions: true,
		AbandonWorkspace:  true,
		Events:            []EventDraft{{Kind: "task.started_over", Body: map[string]any{"generation": t.ImplementGeneration}}},
	}, nil
}
