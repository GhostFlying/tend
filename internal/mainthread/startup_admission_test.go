package mainthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestRejectedInputStartupExhaustionStopsActualOpen(t *testing.T) {
	f := newHandoffFixture(t)
	e := f.e
	e.say("Preserve the input after an explicit native start rejection")
	delivery, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal("fixture did not claim original input", delivery, ok, err)
	}
	e.m.mu.Lock()
	e.m.session, e.m.thread = f.session, f.source
	e.m.mu.Unlock()
	e.h.StartTurnErr = func(harness.TurnInput) error { return harness.ErrRejected }
	result := e.m.Deliver(t.Context(), delivery)
	if result.Outcome != app.Retry {
		t.Fatal("fixture did not reach a definite native rejection", result)
	}
	turn, found, err := e.app.MainTurn(t.Context(), delivery.RequestID)
	if err != nil || !found || turn.Status != store.TurnRejected || turn.TurnID != "" {
		t.Fatal("fixture did not retain the known-unsent rejection", turn, found, err)
	}
	if pending, err := e.app.NativeReceiptPending(t.Context(), delivery.RequestID); err != nil || pending {
		t.Fatal("fixture unexpectedly has an unknown native receipt", pending, err)
	}
	if err := e.app.FinishDelivery(t.Context(), delivery, result); err != nil {
		t.Fatal(err)
	}
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	opens := 0
	m := New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{Instance: "restarted-main", Log: quiet(), Now: f.clock.Now, Open: func(context.Context) (harness.Session, error) {
		opens++
		return nil, errors.New("fixture: known pre-ready Open failure")
	}})
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := m.serve(t.Context()); err == nil {
			t.Fatal("fixture Open unexpectedly succeeded")
		}
		f.clock.Advance(31 * time.Second)
	}
	if err := m.serve(t.Context()); err != nil || opens != int(domain.DefaultAgentRecoveryPolicy().MaxAttempts) {
		t.Fatal("definitely rejected input bypassed the exhausted startup hold", opens, err)
	}
	if e.count(`SELECT COUNT(*) FROM attentions WHERE status='open'`) != 1 {
		t.Fatal("rejected startup did not retain one technical action")
	}
}

func TestReadySessionCannotStartClaimedInputWithStartupHold(t *testing.T) {
	f := newHandoffFixture(t)
	e := f.e
	e.say("Keep the claimed input paused until its technical action is handled")
	delivery, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal("fixture did not claim original input", delivery, ok, err)
	}
	attachment := app.AgentStartupAttachment{RequestID: delivery.RequestID}
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := e.app.DeferAgentStartupAttachment(t.Context(), "main", attachment); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(31 * time.Second)
	}
	e.m.mu.Lock()
	e.m.session, e.m.thread = f.session, f.source
	e.m.mu.Unlock()
	starts := 0
	e.h.StartTurnErr = func(harness.TurnInput) error {
		starts++
		return harness.ErrRejected
	}
	if result := e.m.Deliver(t.Context(), delivery); result.Outcome != app.Retry || starts != 0 {
		t.Fatal("ready session started an input whose technical action is still open", result, starts)
	}
	if e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id=?`, delivery.RequestID) != 0 {
		t.Fatal("held input acquired native intent before technical resolution")
	}
}
