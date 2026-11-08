package mainthread

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRecoveryContinuesAfterReadDisplayReceiptLoss(t *testing.T) {
	h := testkit.NewHarness()
	var reads, continued atomic.Int32
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if in.Continue {
			continued.Add(1)
			if in.Text != "" || in.UserMessageID != "" {
				t.Error("recovery replayed the original input")
			}
			th.Say(turn, "answer", "retained work completed")
			return "completed"
		}
		reads.Add(1)
		th.UseTool(turn, "original-read", harness.ToolUse{Name: "tasks_read", Status: protocol.ToolRunning})
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: turn, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}})
		return "failed"
	}
	e := newEnv(t, h)
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 2})(e.app)
	id := e.say("finish the retained investigation")
	eventually(t, "native-safe recovery completes despite missing read display completion", func() bool {
		return continued.Load() == 1 && e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND status='completed'`, id) == 1
	})
	if reads.Load() != 1 || len(businessStarts(h)) != 2 {
		t.Fatalf("lost display caused tool or input replay: reads=%d starts=%d", reads.Load(), len(businessStarts(h)))
	}
	if got := e.count(`SELECT COUNT(*) FROM conversation_tool_uses WHERE item_id='original-read' AND status IN ('running','interrupted')`); got != 1 {
		t.Fatalf("recovery fabricated successful completion of the historical presentation: %d", got)
	}
}

func TestRecoveryStillRequiresSafeNativeReceipt(t *testing.T) {
	e, th, rec, delivery, _ := setupRetainedRecovery(t, false)
	if err := e.app.RequeueRejectedAgentRecovery(t.Context(), delivery.RequestID); err != nil {
		t.Fatal(err)
	}
	e.m.mu.Lock()
	e.m.thread = &receiptThread{Thread: th, found: true, receipt: harness.TurnReceipt{ID: rec.TurnID, Status: "failed", ContinuationSafe: false}}
	e.m.mu.Unlock()
	result := e.m.Deliver(t.Context(), delivery)
	recovery, _, err := e.app.AgentRecovery(t.Context(), rec.RequestID)
	if err != nil || result.Outcome != app.Retry || recovery.Status != "waiting" {
		t.Fatalf("unsafe native receipt allowed continuation: result=%+v recovery=%+v error=%v", result, recovery, err)
	}
}
