package app

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (h *harness) desired(as protocol.Assignment) protocol.RunDesired {
	h.t.Helper()
	hb, err := h.app.Heartbeat(context.Background(), runner("linux-1"), []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}})
	if err != nil || len(hb.Runs) != 1 {
		h.t.Fatalf("heartbeat = %+v, %v", hb, err)
	}
	return hb.Runs[0].Desired
}

func TestTakeoverHoldsTheRunUntilHandedBack(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(as, protocol.OpRunsReportTakeover, protocol.TakeoverPayload{Reason: "the owner typed"}, ReportTakeover{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("report_takeover: %+v", op)
	}
	task := h.task(res.TaskID)
	if task.ControlMode != protocol.ControlHumanControl || task.Lifecycle != protocol.TaskRunning {
		t.Fatalf("task = %s/%s, want running under human control", task.Lifecycle, task.ControlMode)
	}
	if ev := h.events("task.taken_over"); len(ev) != 1 || ev[0].Body["run_id"] != as.RunID || ev[0].Body["reason"] != "the owner typed" {
		t.Errorf("events = %+v", ev)
	}
	if d := h.desired(as); d != protocol.DesiredHeld {
		t.Errorf("desired = %s, want held", d)
	}
	// Noticed again, say after a restart: recorded once.
	h.runAs(as, protocol.OpRunsReportTakeover, protocol.TakeoverPayload{Reason: "again"}, ReportTakeover{})
	if n := len(h.events("task.taken_over")); n != 1 {
		t.Errorf("%d takeover events, want 1", n)
	}
	// The owner hands it back.
	op := h.exec(owner, h.env("control.set_mode", taskTarget(res.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlManaged}, rev(h.task(res.TaskID).ControlRevision)), SetControlMode{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("resume: %+v", op)
	}
	if d := h.desired(as); d != protocol.DesiredRun {
		t.Errorf("desired = %s, want run", d)
	}
}

func TestTakeoverOnlyByTheRunItself(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	env := h.env(protocol.OpRunsReportTakeover, protocol.Target{Type: "run", ID: as.RunID}, protocol.TakeoverPayload{}, nil)
	if _, err := h.app.Execute(context.Background(), runner("linux-1"), env, ReportTakeover{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Errorf("host report = %v, want forbidden", err)
	}
}

func TestTakeoverOfAStaleRunIsOnlyRecorded(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	changed := testSpec
	changed.Goal = "something else"
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &changed}, rev(res.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("update_intent: %+v", op)
	}
	h.runAs(as, protocol.OpRunsReportTakeover, protocol.TakeoverPayload{Reason: "late"}, ReportTakeover{})
	if task := h.task(res.TaskID); task.ControlMode != protocol.ControlManaged {
		t.Errorf("control = %s, want managed", task.ControlMode)
	}
	if ev := h.events("task.taken_over"); len(ev) != 1 || ev[0].Body["stale"] != true {
		t.Errorf("events = %+v, want one stale record", ev)
	}
}
