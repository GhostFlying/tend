package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestTakeoverObservationRetryPreservesHandbackAndNewObservation(t *testing.T) {
	h := newHarness(t)
	_, created := h.createTask(owner, createTaskPayload{Title: "takeover", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	send := func(p protocol.TakeoverPayload) protocol.Operation {
		t.Helper()
		raw, _ := json.Marshal(p)
		op, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, &protocol.Envelope{RequestID: h.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunsReportTakeover, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: raw}, ReportTakeover{})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	first := protocol.TakeoverPayload{ObservationID: h.ids.New(protocol.PrefixRequest), Reason: "first actual observation"}
	if op := send(first); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := h.task(created.TaskID)
	h.exec(owner, h.env("control.set_mode", taskTarget(created.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlManaged}, rev(task.ControlRevision)), SetControlMode{})
	after := h.task(created.TaskID)
	if op := send(first); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if task := h.task(created.TaskID); task.ControlMode != protocol.ControlManaged || task.ControlRevision != after.ControlRevision || len(h.events("task.taken_over")) != 1 || len(h.events("run.takeover_observed")) != 1 {
		t.Fatal("old observation undid hand-back", task)
	}
	second := protocol.TakeoverPayload{ObservationID: h.ids.New(protocol.PrefixRequest), Reason: "new actual observation"}
	if op := send(second); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if task := h.task(created.TaskID); task.ControlMode != protocol.ControlHumanControl || len(h.events("task.taken_over")) != 2 || len(h.events("run.takeover_observed")) != 2 {
		t.Fatal("new observation was swallowed", task)
	}
	second.Reason = "different immutable content"
	conflicting, _ := json.Marshal(second)
	if op, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, &protocol.Envelope{RequestID: h.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunsReportTakeover, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: conflicting}, ReportTakeover{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal(op, err)
	}
	if len(h.events("run.takeover_observed")) != 2 {
		t.Fatal("conflict wrote another observation")
	}
	raw, _ := json.Marshal(first)
	if _, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: h.ids.New(protocol.PrefixRun), Role: as.Role}, &protocol.Envelope{RequestID: h.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunsReportTakeover, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: raw}, ReportTakeover{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("foreign run reported another observation", err)
	}
}

func TestTakeoverObservationFromHistoricalEventReadsActualEventProvenance(t *testing.T) {
	h := newHarness(t)
	_, created := h.createTask(owner, createTaskPayload{Title: "legacy takeover", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runs SET protocol=3 WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.TakeoverPayload{Reason: "retained legacy observation"})
	env := &protocol.Envelope{RequestID: h.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunsReportTakeover, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: raw}
	principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	if op, err := h.app.Execute(t.Context(), principal, env, ReportTakeover{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	// The historical format had only its real transition and actor provenance.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM events WHERE object_type='run' AND object_id=? AND kind='run.takeover_observed'`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	task := h.task(created.TaskID)
	h.exec(owner, h.env("control.set_mode", taskTarget(created.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlManaged}, rev(task.ControlRevision)), SetControlMode{})
	after := h.task(created.TaskID)
	if op, err := h.app.Execute(t.Context(), principal, env, ReportTakeover{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	if task := h.task(created.TaskID); task.ControlMode != protocol.ControlManaged || task.ControlRevision != after.ControlRevision || len(h.events("task.taken_over")) != 1 {
		t.Fatal("legacy event was not recovered from its actual provenance", task)
	}
}
