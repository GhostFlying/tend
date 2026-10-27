package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_FailedRootAttentionIsDurableAndDeduplicated(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "root failure", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no assignment")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	for _, seq := range []int64{1, 1, 2} {
		if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: seq, Kind: "worker.turn_failed", Body: json.RawMessage(`{"turn_at":123,"background_pending":true}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var notices, sources int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM message_notices WHERE notice_type='run_attention'`).Scan(&notices); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM background_sources WHERE kind='run.attention_required'`).Scan(&sources); err != nil {
			return err
		}
		if notices != 1 || sources != 1 {
			t.Fatalf("notices=%d sources=%d", notices, sources)
		}
		run, err := store.GetRun(t.Context(), tx.Q(), as.RunID)
		if run.ObservedState != protocol.RunRunning {
			t.Fatalf("run released or changed: %+v", run)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view := h.executionTask(task.TaskID).Execution
	if view.State != protocol.ExecutionRecovering || view.Recovery == nil || !view.Recovery.BackgroundPending {
		t.Fatalf("%+v", view)
	}
	// Reconstructing the app must not reset the persisted reminder budget.
	h.app = New(h.store, h.ids, h.clock.Now)
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 3, Kind: "worker.turn_failed", Body: json.RawMessage(`{"turn_at":123,"background_pending":true}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM message_notices WHERE notice_type='run_attention'`).Scan(&n)
		if n != 1 {
			t.Fatalf("restart duplicated notice: %d", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRootAttentionDoesNotRepeatAnOpenDecision(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "waiting root", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.exec(owner, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{TaskID: task.TaskID, Question: "How should we handle the failure?"}, nil), OpenDecision{})
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "worker.turn_failed", Body: json.RawMessage(`{"turn_at":123,"background_pending":true}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM message_notices WHERE notice_type='run_attention'`).Scan(&n)
		if n != 0 {
			t.Fatalf("open decision duplicated notice: %d", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRootObservationResolutionClearsOnlyUnknownProjection(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "native observation", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	p := runPrincipal(as.RunID, as.Role)
	if _, err := h.app.RecordRunEvents(t.Context(), p, as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "worker.root_observation_unknown", Body: json.RawMessage(`{"turn_at":123,"background_pending":true}`)}}); err != nil {
		t.Fatal(err)
	}
	if got := h.executionTask(task.TaskID).Execution; got.Recovery == nil || got.Recovery.Phase != "observation_unknown" {
		t.Fatal(got)
	}
	if _, err := h.app.RecordRunEvents(t.Context(), p, as.RunID, []protocol.RunEvent{{Seq: 2, Kind: "worker.root_observation_resolved"}}); err != nil {
		t.Fatal(err)
	}
	if got := h.executionTask(task.TaskID).Execution; got.Recovery != nil || got.State != protocol.ExecutionRunning {
		t.Fatalf("resolved observation remained unknown: %+v", got)
	}
}

func TestRootFailureKeepsUnknownOperationFactsAndLaterConvergence(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "unknown failure", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	p := runPrincipal(as.RunID, as.Role)
	events := []protocol.RunEvent{
		{Seq: 1, Kind: "worker.root_observation_unknown", Body: json.RawMessage(`{"turn_at":100,"managed_turn_at":100,"background_pending":true,"root_state":"failed","code":"response_stream_disconnected"}`)},
		{Seq: 2, Kind: "worker.turn_failed", Body: json.RawMessage(`{"turn_at":200,"managed_turn_at":100,"background_pending":true,"root_turn":{"state":"failed","operations":"unknown","errorCode":"response_stream_disconnected"}}`)},
	}
	if _, err := h.app.RecordRunEvents(t.Context(), p, as.RunID, events); err != nil {
		t.Fatal(err)
	}
	got := h.executionTask(task.TaskID).Execution.Recovery
	if got == nil || got.Phase != "observation_unknown" || got.RootState != "failed" || got.Code != "response_stream_disconnected" {
		t.Fatalf("lost native uncertainty: %+v", got)
	}
	if _, err := h.app.RecordRunEvents(t.Context(), p, as.RunID, []protocol.RunEvent{{Seq: 3, Kind: "worker.root_observation_resolved", Body: json.RawMessage(`{"root_state":"failed","background_pending":false,"code":"response_stream_disconnected"}`)}}); err != nil {
		t.Fatal(err)
	}
	got = h.executionTask(task.TaskID).Execution.Recovery
	if got == nil || got.Phase != "turn_failed" || got.BackgroundPending {
		t.Fatalf("lost operation convergence: %+v", got)
	}
}
