package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestTaskEndViewUsesTerminalEventTime(t *testing.T) {
	h := newHarness(t)
	id, accept := h.toAwaitingAcceptance()
	h.clock.Advance(time.Minute)
	completedAt := h.clock.Now().UnixMilli()
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	completed := relationDetail(t, h, id)
	if completed.EndedAt != completedAt || completed.CompletedAt != completedAt {
		t.Fatalf("completed timestamp: %+v", completed.TaskSummary)
	}
	_, next := h.createTask(owner, createTaskPayload{Title: "cancel next", Spec: testSpec})
	if relationDetail(t, h, next.TaskID).EndedAt != 0 {
		t.Fatal("live task has terminal time")
	}
	h.clock.Advance(time.Minute)
	cancelledAt := h.clock.Now().UnixMilli()
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(next.TaskID), nil, rev(h.task(next.TaskID).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	cancelled := relationDetail(t, h, next.TaskID)
	if cancelled.EndedAt != cancelledAt || cancelled.CompletedAt != 0 {
		t.Fatalf("cancelled timestamp: %+v", cancelled.TaskSummary)
	}
}
