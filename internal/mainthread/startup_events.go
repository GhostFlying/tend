package mainthread

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/harness"
)

// A closing runner cannot answer history/usage RPCs until final events are
// applied. Keep consuming those facts while startup reads wait on the runner.
func drainStartupEvents(ctx context.Context, events <-chan harness.Event, apply func(context.Context, harness.Event) error, read func(context.Context) error) error {
	readCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- read(readCtx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	for {
		select {
		case err := <-done:
			joined = true
			return err
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return errors.New("mainthread: runner ended during startup reconciliation")
			}
			if err := apply(readCtx, event); err != nil {
				return err
			}
		}
	}
}
