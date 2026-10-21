package domain

import "github.com/GhostFlying/tend/internal/protocol"

// Options of the decision about an unavailable workspace: tend answers
// WaitForHostOption itself once the host is back, and StartOverOption
// identifies the decision.
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
		OpenDecisions: []DecisionDraft{workspaceQuestion(hostID, why)},
	}
}

func workspaceQuestion(hostID, why string) DecisionDraft {
	return workspaceDecision("The task's workspace is on host " + hostID + ", which " + why +
		". Its work can only go on there. How should this task proceed?")
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
		OpenDecisions: []DecisionDraft{workspaceQuestion(hostID, why)},
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
