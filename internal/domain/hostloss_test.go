package domain

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkspaceUnavailable(t *testing.T) {
	task := taskIn(protocol.TaskAwaitingAcceptance)
	d := WorkspaceUnavailable(task, "linux-1", "was retired")
	if d.Task.Block != protocol.BlockWorkspaceUnavailable || len(d.OpenDecisions) != 1 || len(d.Events) != 1 {
		t.Fatalf("WorkspaceUnavailable = %+v", d)
	}
	if q := d.OpenDecisions[0].Question; !strings.Contains(q, "linux-1, which was retired") {
		t.Errorf("question = %q", q)
	}
	if got := d.OpenDecisions[0].Effects[StartOverOption]; got != protocol.EffectStartOver {
		t.Errorf("start over effect = %q", got)
	}
	if _, ok := d.OpenDecisions[0].Effects[WaitForHostOption]; ok {
		t.Error("waiting for the host has an effect; tend answers it when the host is back")
	}

	// A task that is not managed, already blocked or finished is left alone.
	for name, mutate := range map[string]func(*Task){
		"paused":    func(t *Task) { t.Control = protocol.ControlPaused },
		"blocked":   func(t *Task) { t.Block = protocol.BlockWaitingDecision },
		"completed": func(t *Task) { t.Lifecycle = protocol.TaskCompleted },
	} {
		task := taskIn(protocol.TaskAwaitingAcceptance)
		mutate(&task)
		if d := WorkspaceUnavailable(task, "linux-1", "was retired"); d.Task != task || len(d.Events)+len(d.OpenDecisions) != 0 {
			t.Errorf("%s: WorkspaceUnavailable = %+v", name, d)
		}
	}

	again := AskAboutWorkspaceAgain(d.Task, "linux-1", "was retired")
	if again.Task != d.Task || len(again.OpenDecisions) != 1 || len(again.Events) != 1 {
		t.Errorf("AskAboutWorkspaceAgain = %+v", again)
	}
	if again := AskAboutWorkspaceAgain(task, "linux-1", "was retired"); len(again.OpenDecisions) != 0 {
		t.Errorf("asked again about a task that is not blocked: %+v", again)
	}

	back := WorkspaceAvailable(d.Task, "linux-1")
	if back.Task.Block != protocol.BlockNone || len(back.Events) != 1 {
		t.Errorf("WorkspaceAvailable = %+v", back)
	}
	if again := WorkspaceAvailable(back.Task, "linux-1"); len(again.Events) != 0 {
		t.Errorf("WorkspaceAvailable on an unblocked task = %+v", again)
	}
}

func TestStartOver(t *testing.T) {
	task := taskIn(protocol.TaskAwaitingAcceptance)
	task.Block, task.WorkspaceAssigned, task.Budget = protocol.BlockWorkspaceUnavailable, true, 1
	d, err := StartOver(task, task.ControlRevision)
	if err != nil {
		t.Fatal(err)
	}
	got := d.Task
	if got.Lifecycle != protocol.TaskQueued || got.ImplementGeneration != 2 || got.Budget != 2 || got.CurrentArtifactID != "" ||
		got.Block != protocol.BlockNone || got.WorkspaceAssigned || got.ControlRevision != task.ControlRevision+1 {
		t.Errorf("task after StartOver = %+v", got)
	}
	if !d.AbandonWorkspace || !d.CancelOpenActions || len(d.Actions) != 1 || d.Actions[0].Kind != protocol.ActionImplement || d.Actions[0].Generation != 2 {
		t.Errorf("StartOver = %+v", d)
	}

	if _, err := StartOver(task, task.ControlRevision-1); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Errorf("stale revision: %v", err)
	}
	unblocked := taskIn(protocol.TaskAwaitingAcceptance)
	if _, err := StartOver(unblocked, unblocked.ControlRevision); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Errorf("without the block: %v", err)
	}
	done := task
	done.Lifecycle = protocol.TaskCancelled
	if _, err := StartOver(done, done.ControlRevision); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Errorf("cancelled: %v", err)
	}
}
