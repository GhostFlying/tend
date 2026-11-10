package domain

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
)

func TestTaskContinuationPreservesAcceptedHistoryAndRetryBudget(t *testing.T) {
	old := taskIn(protocol.TaskCompleted)
	old.CurrentArtifactID = "art_original"
	old.ImplementGeneration = old.Budget
	d, err := ContinueTask(old, old.ControlRevision, "Check the unchanged goal after rebase")
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.SpecVersion != old.SpecVersion || d.Task.CurrentArtifactID != old.CurrentArtifactID || d.Task.ImplementGeneration != old.ImplementGeneration+1 || d.Task.ControlRevision != old.ControlRevision+1 || d.Task.Budget-d.Task.ImplementGeneration != old.Budget-old.ImplementGeneration || d.Task.ImplementContinuations != 1 || d.Task.Lifecycle != protocol.TaskQueued || len(d.Actions) != 1 {
		t.Fatal(d)
	}
	stale := ArtifactSubmitted(d.Task, implementRun(old), "art_late")
	if stale.Task.CurrentArtifactID != old.CurrentArtifactID || stale.Task.Lifecycle != protocol.TaskQueued {
		t.Fatal("old result advanced continuation", stale)
	}
	started := RunStarted(d.Task, implementRun(d.Task))
	done := ArtifactSubmittedWithAcceptance(started.Task, implementRun(d.Task), "art_next", false)
	if done.Task.Lifecycle != protocol.TaskCompleted || done.Task.CurrentArtifactID != "art_next" {
		t.Fatal(done)
	}
}

func TestTaskContinuationRequiresCurrentCompletedGoal(t *testing.T) {
	for _, state := range []protocol.TaskLifecycle{protocol.TaskDraft, protocol.TaskQueued, protocol.TaskRunning, protocol.TaskAwaitingAcceptance, protocol.TaskChangesRequired, protocol.TaskCancelled} {
		v := taskIn(state)
		if _, err := ContinueTask(v, v.ControlRevision, "same goal"); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
			t.Fatalf("%s: %v", state, err)
		}
	}
	v := taskIn(protocol.TaskCompleted)
	if _, err := ContinueTask(v, v.ControlRevision-1, "same goal"); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal(err)
	}
	if _, err := ContinueTask(v, v.ControlRevision, " "); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal(err)
	}
}
