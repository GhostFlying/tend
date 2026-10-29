package mainthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/store"
)

func TestKnownUnsentStartWithCanceledContextPersistsRejection(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		name := "ordinary"
		if recovery {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) {
			var e *env
			var d app.Delivery
			if recovery {
				var th harness.Thread
				var rec store.MainTurn
				e, th, rec, d, _ = setupRetainedRecovery(t, false)
				rec.Status = store.TurnRejected
				if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
					t.Fatal(err)
				}
				if err := e.app.RequeueRejectedAgentRecovery(t.Context(), d.RequestID); err != nil {
					t.Fatal(err)
				}
				state, _, err := e.app.AgentRecovery(t.Context(), d.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				timer := time.NewTimer(max(time.Until(time.UnixMilli(state.NextAttemptAt)), 0) + time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
				session, err := e.h.Open(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				e.m.mu.Lock()
				e.m.session = session
				e.m.thread = &receiptThread{Thread: th, found: true, receipt: harness.TurnReceipt{ID: "original-native", Status: "failed", ContinuationSafe: true}}
				e.m.mu.Unlock()
			} else {
				f := newHandoffFixture(t)
				e = f.e
				e.say("retain original goal on known preflight failure")
				var ok bool
				var err error
				d, _, ok, err = e.app.NextDelivery(t.Context(), app.MainThreadDestination)
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				e.m.mu.Lock()
				e.m.session, e.m.thread = f.session, f.source
				e.m.mu.Unlock()
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			e.h.StartTurnErr = func(harness.TurnInput) error {
				called = true
				cancel()
				return errors.Join(harness.ErrRejected, context.Canceled)
			}
			result := e.m.Deliver(ctx, d)
			if !called {
				t.Fatal("fixture did not reach actual StartTurn boundary", result)
			}
			if result.Outcome != app.Retry {
				t.Fatal("known unsent result", result)
			}
			rec, ok, err := e.app.MainTurn(t.Context(), d.RequestID)
			if err != nil || !ok || rec.Status != store.TurnRejected || rec.TurnID != "" {
				t.Fatalf("canceled admission left unknown/starting: %+v %v", rec, err)
			}
			if pending, err := e.app.NativeReceiptPending(t.Context(), d.RequestID); err != nil || pending {
				t.Fatal("unsent start has receipt fence", pending, err)
			}
			if recovery {
				state, _, err := e.app.AgentRecovery(t.Context(), d.RequestID)
				if err != nil || state.Status != "waiting" {
					t.Fatal("known unsent recovery not requeued", state, err)
				}
			}
		})
	}
}
