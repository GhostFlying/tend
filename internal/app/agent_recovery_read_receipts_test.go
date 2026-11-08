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
	env := h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: workPlan()}, nil)
	env.ParentRequestID = rec.RequestID
	op := h.exec(owner, env, CreateIssue{})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	if err := h.app.write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE request_records SET status='unknown' WHERE domain=? AND request_id=?`, owner.DedupDomain(), env.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r, _, err := h.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), r.ActiveRequestID, rec.ThreadID); err != nil || !admitted {
		t.Fatalf("independent business receipt blocked native recovery: %t %v", admitted, err)
	}
	// The native continuation guard does not rewrite an independently retained
	// business operation. Its original request still deduplicates as unknown.
	again := h.exec(owner, env, CreateIssue{})
	if again.RequestID != op.RequestID || again.Status != protocol.OpUnknown {
		t.Fatalf("business unknown was replaced or settled: original=%+v replay=%+v", op, again)
	}
}
