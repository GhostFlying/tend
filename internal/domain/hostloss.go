package domain

import "github.com/GhostFlying/tend/internal/protocol"

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
	d := workspaceDecision("The task's workspace is on host " + hostID + ", which " + why +
		". Its work can only go on there. How should this task proceed?")
	if t.WorkspaceReused {
		d.Question += " This task reuses a fixed workspace and cannot start over on another host. Wait for its host, or cancel and create a new task with workspace.mode=new or temporary."
	}
	if t.RequiredHostID != "" {
		d.Question += " This task requires host " + t.RequiredHostID + " and cannot start over on another host. Wait for that host, or cancel and create a new task with a suitable requirements.host_id and an independent workspace."
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
