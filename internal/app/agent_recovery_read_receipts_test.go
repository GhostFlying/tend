package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestAgentRecoveryPresentationReceiptsDoNotAuthorizeContinuation(t *testing.T) {
	for _, status := range []string{"running", "unknown", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			rec := failedRecoveryTurn(t, h)
			err := h.app.write(t.Context(), func(tx *store.Tx) error {
				if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "read-display", Kind: store.KindToolUse, Author: "main_agent", ReplyTo: rec.MessageID}); err != nil {
					return err
				}
				return tx.PutToolUse(t.Context(), store.ToolUseRow{MessageID: "read-display", ThreadID: rec.ThreadID, TurnID: rec.TurnID, ItemID: "original-read", Tool: "tasks_read", Status: status})
			})
			if err != nil {
				t.Fatal(err)
			}
			r, _, err := h.app.AgentRecovery(t.Context(), rec.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			h.clock.Advance(time.Minute)
			if admitted, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, rec.ThreadID); err != nil || !admitted {
				t.Fatalf("presentation %s blocked the confirmed native predecessor: admitted=%t error=%v", status, admitted, err)
			}
			if admitted, err := h.app.ConfirmAgentRecoveryStart(t.Context(), r.ActiveRequestID, rec.ThreadID, 0); err != nil || !admitted {
				t.Fatalf("presentation %s blocked the final start fence: admitted=%t error=%v", status, admitted, err)
			}
			if blocked, err := h.app.AgentRecoveryUncertain(t.Context(), "main"); err != nil || blocked {
				t.Fatalf("presentation created a conversation-wide unknown: %t %v", blocked, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				use, err := store.GetToolUse(t.Context(), tx.Q(), rec.TurnID, "original-read")
				if err == nil && use.Status != status {
					t.Errorf("changed the original presentation receipt: %+v", use)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentRecoveryPresentationChangeDoesNotSettleBusinessUnknown(t *testing.T) {
	h := newHarness(t)
	rec := failedRecoveryTurn(t, h)
	op, task := h.createTask(owner, createTaskPayload{Title: "independent business operation", Spec: testSpec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok, _ := h.claim("linux-1")
	if !ok || as.TaskID != task.TaskID {
		t.Fatal(as, ok)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The original executor stopped reporting an outcome. Its actual Run and
	// lease remain authoritative, independently of the native reply display.
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='unknown' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	beforeRun, beforeLease, beforeTask := h.run(as.RunID), h.lease(as.RunID), h.task(task.TaskID)
	runs, artifacts := h.count("runs"), h.count("artifacts")
	r, _, err := h.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, rec.ThreadID); err != nil || !admitted {
		t.Fatalf("independent business receipt blocked native recovery: %t %v", admitted, err)
	}
	if got := h.run(as.RunID); got != beforeRun || got.ObservedState != protocol.RunUnknown || h.lease(as.RunID) != beforeLease || h.task(task.TaskID) != beforeTask || h.count("runs") != runs || h.count("artifacts") != artifacts {
		t.Fatal("native recovery replaced or settled an independent unknown Run", beforeRun, got)
	}
}
