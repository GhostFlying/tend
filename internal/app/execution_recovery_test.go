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
