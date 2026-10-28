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

func TestRateLimitContinuesWithoutRepeatingInputOrTool(t *testing.T) {
	var tools, continued atomic.Int32
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if in.Continue {
			continued.Add(1)
			if in.Text != "" || in.UserMessageID != "" || len(in.ImageURLs) != 0 {
				t.Errorf("replayed original input: %+v", in)
			}
			th.Say(turn, "answer", "retained work completed")
			return "completed"
		}
		tools.Add(1)
		th.UseTool(turn, "operation", harness.ToolUse{Name: "record_once", Status: protocol.ToolCompleted, Output: "already done"})
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: turn, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}})
		return "failed"
	}
	e := newEnv(t, h)
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 2})(e.app)
	id := e.say("perform this operation once")
	eventually(t, "empty continuation completes", func() bool {
		return continued.Load() == 1 && e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND status='completed'`, id) > 0
	})
	if tools.Load() != 1 {
		t.Fatalf("tool executed %d times", tools.Load())
	}
	if got := businessStarts(h); len(got) != 2 || !got[1].Continue {
		t.Fatalf("starts: %+v", got)
	}
}

func TestNativeInternalRetryDoesNotCreateTendContinuation(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: turn, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429, WillRetry: true}})
		th.Say(turn, "done", "native recovered")
		return "completed"
	}
	e := newEnv(t, h)
	id := e.say("wait for native recovery")
	eventually(t, "native recovery settles", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND status='completed'`, id) > 0
	})
	if n := e.count(`SELECT COUNT(*) FROM agent_recoveries`); n != 0 {
		t.Fatalf("created %d Tend recoveries", n)
	}
}

func TestRecoveryUnknownNativeStartNeverResends(t *testing.T) {
	h := testkit.NewHarness()
	var attempts atomic.Int32
	h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: turn, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}})
		return "failed"
	}
	h.StartTurnErr = func(in harness.TurnInput) error {
		if in.Continue {
			attempts.Add(1)
			return contextLostError{}
		}
		return nil
	}
	e := newEnv(t, h)
	app.WithAgentRecoveryPolicy(domain.AgentRecoveryPolicy{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, MaxElapsed: time.Minute, MaxAttempts: 2})(e.app)
	e.say("keep uncertain starts fenced")
	eventually(t, "uncertain recovery is retained", func() bool { return e.count(`SELECT COUNT(*) FROM agent_recoveries WHERE status='unknown'`) == 1 })
	e.stop()
	e.start()
	eventually(t, "manager reconnects", func() bool { return h.Sessions() >= 2 })
	e.say("new input must wait for reconciliation")
	eventually(t, "new input remains fenced", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE operation='main_thread.handle_message' AND status='pending' AND attempts>0`) > 0
	})
	if attempts.Load() != 1 {
		t.Fatalf("uncertain start sent %d times", attempts.Load())
	}
}

type contextLostError struct{}

func (contextLostError) Error() string { return "synthetic response loss" }
