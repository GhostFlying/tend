package traex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkerCancellationUnderEventBackpressure(t *testing.T) {
	for _, retained := range []bool{false, true} {
		for _, cause := range []error{harness.ErrPaused, harness.ErrHeld} {
			for _, event := range []harness.Event{
				{Kind: harness.EventRequestUsage, RequestUsage: &protocol.TokenUsageSample{RequestID: "request"}},
				{Kind: harness.EventChildRollout},
				{Kind: harness.EventTurnStarted, TurnID: "tu_w"},
				{Kind: harness.EventItemStarted, TurnID: "tu_w"},
				{Kind: harness.EventItemCompleted, TurnID: "tu_w"},
				{Kind: harness.EventTurnCompleted, TurnID: "tu_w", TurnStatus: "completed"},
			} {
				name := "local/"
				if retained {
					name = "retained/"
				}
				t.Run(name+cause.Error()+"/"+string(event.Kind), func(t *testing.T) {
					ctx, cancel := context.WithCancelCause(t.Context())
					defer cancel(context.Canceled)
					ws, servers := fakeWorkers(t)
					released := make(chan error, 1)
					if retained {
						ws.opts.Acquire = func(ctx context.Context, _ harness.WorkerSpec) (*Session, func(error), error) {
							s, err := ws.start(ctx, Options{Env: []string{protocol.AddonEnv + "=off"}})
							return s, func(err error) { released <- err }, err
						}
					}
					w, err := ws.Start(ctx, harness.WorkerSpec{RunID: "run", Role: protocol.RoleImplement, Dir: "/w", Spec: []byte(`{}`)})
					if err != nil {
						t.Fatal(err)
					}
					f := <-servers
					// Deliver directly to the worker and leave its output unread. Once
					// received, the only way out of its pending output is cancellation.
					select {
					case w.(*worker).thread.events <- event:
					case <-time.After(5 * time.Second):
						t.Fatal("worker did not receive the event")
					}
					cancel(cause)
					done := make(chan error, 1)
					go func() { _, err := w.Wait(); done <- err }()
					select {
					case err := <-done:
						if !errors.Is(err, cause) {
							t.Fatalf("cancellation cause lost under backpressure: %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("cancellation blocked on unread output")
					}
					wantInterrupt := !retained || !errors.Is(cause, harness.ErrHeld)
					if interrupted := f.params(methodTurnInterrupt) != nil; interrupted != wantInterrupt {
						t.Fatalf("interrupted=%v, want %v", interrupted, wantInterrupt)
					}
					if retained {
						if err := <-released; !errors.Is(err, cause) {
							t.Fatalf("retained release lost cancellation: %v", err)
						}
					}
				})
			}
		}
	}
}
