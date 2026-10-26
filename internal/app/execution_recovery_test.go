package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestExecutionRecoveryUsesLatestRunFactAndDoesNotReplayEvents(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "recover", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	before := h.count("events")
	for index, step := range []struct{ kind, body, phase string }{
		{"worker.turn_failed", `{"background_pending":true}`, "turn_failed"},
		{"worker.recovery_pending", `{"code":"invalid_encrypted_content","attempts":1,"transcript_path":"/private/session"}`, "pending"},
		{"worker.recovery_started", `{"code":"invalid_encrypted_content","attempts":1}`, "running"},
		{"turn.started", `{}`, "running"},
		{"worker.recovery_completed", `{"attempts":1}`, ""},
		{"worker.recovery_failed", `{"code":"response_stream_disconnected","attempts":1}`, "failed"},
		{"worker.recovery_handed_back", `{"phase":"handed_back","attempts":1}`, ""},
		{"worker.turn_failed", `{"background_pending":true}`, "turn_failed"},
	} {
		batch := []protocol.RunEvent{{Seq: int64(index + 1), Kind: step.kind, Body: json.RawMessage(step.body)}}
		for repeat := range 2 {
			result, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, batch)
			if err != nil || result.Accepted != 1-repeat {
				t.Fatalf("event dedup: %+v %v", result, err)
			}
		}
		detail := h.executionTask(task.TaskID)
		if step.phase == "" {
			if detail.Execution.State != protocol.ExecutionRunning || detail.Execution.Recovery != nil {
				t.Fatal(detail.Execution)
			}
		} else if detail.Execution.State != protocol.ExecutionRecovering || detail.Execution.Recovery == nil || detail.Execution.Recovery.Phase != step.phase {
			t.Fatal(detail.Execution)
		}
		encoded, _ := json.Marshal(detail.Execution)
		if strings.Contains(string(encoded), "/private/session") {
			t.Fatal("native payload exposed instead of projection")
		}
	}
	if got := h.count("events") - before; got != 7 {
		t.Fatalf("recovery invalidation events=%d", got)
	}
	// Terminal task lifecycle wins even when old worker logs arrive late.
	op := h.exec(owner, h.env("tasks.cancel", taskTarget(task.TaskID), struct{}{}, rev(h.task(task.TaskID).ControlRevision)), CancelTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.executionTask(task.TaskID); got.Execution.State != protocol.ExecutionCancelled || got.Execution.Recovery != nil {
		t.Fatal(got.Execution)
	}
}

func TestStartupEventsInvalidateLiveExecutionOnce(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "startup", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	before := h.count("events")
	for i, kind := range []string{"worker.startup_failed", "worker.startup_retry_pending", "worker.startup_started"} {
		batch := []protocol.RunEvent{{Seq: int64(i + 1), Kind: kind, Body: json.RawMessage(`{"attempts":1,"diagnostic":{"code":"readiness_timeout","promptWriteState":"none","elapsedMs":60000}}`)}}
		for repeat := range 2 {
			changed := h.app.Changes().Wait()
			result, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, batch)
			if err != nil || result.Accepted != 1-repeat {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if woken(changed) != (repeat == 0) {
				t.Fatalf("kind=%s repeat=%d change notification mismatch", kind, repeat)
			}
		}
		v := h.executionTask(task.TaskID).Execution
		if v.Startup == nil || v.Startup.Code != "readiness_timeout" {
			t.Fatalf("startup missing: %+v", v)
		}
		want := protocol.ExecutionRecovering
		if kind == "worker.startup_started" {
			want = protocol.ExecutionRunning
		}
		if v.State != want {
			t.Fatalf("state=%s want=%s", v.State, want)
		}
	}
	if h.count("events")-before != 3 {
		t.Fatal("startup invalidations missing or duplicated")
	}
}

func TestStartupFailureDoesNotOverrideRetryQueueOrNewRun(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "startup retry", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "worker.startup_failed", Body: json.RawMessage(`{"diagnostic":{"code":"readiness_timeout"}}`)}}); err != nil {
		t.Fatal(err)
	}
	h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_start_failed"}, Release{})
	waiting := h.executionTask(task.TaskID).Execution
	if waiting.State != protocol.ExecutionWaitingOwner || waiting.Startup == nil {
		t.Fatalf("current failure lost: %+v", waiting)
	}
	if op := h.requestAction(task.TaskID, protocol.ActionImplement, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	queued := h.executionTask(task.TaskID).Execution
	if queued.State != protocol.ExecutionQueued || queued.Startup != nil || queued.QueueReason == nil {
		t.Fatalf("historical failure overrides queue: %+v", queued)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("retry not claimable")
	}
	h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	current := h.executionTask(task.TaskID).Execution
	if current.RunID != next.RunID || current.Startup != nil || current.State != protocol.ExecutionRunning {
		t.Fatalf("historical failure overrides current run: %+v", current)
	}
}
