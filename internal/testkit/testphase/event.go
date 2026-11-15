package testphase

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Await starts a named watchdog for this event, independently of preparation
// and earlier events. A producer failure or parent cancellation remains fatal.
func Await[T any](t *testing.T, parent context.Context, name string, watchdog time.Duration, event <-chan T, failures <-chan string) (T, error) {
	t.Helper()
	ctx, finish := Context(t, parent, name, watchdog)
	defer finish()
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("%s: %w", name, context.Cause(ctx))
	}
	select {
	case value, ok := <-event:
		if !ok {
			return zero, fmt.Errorf("%s: event source closed before its result", name)
		}
		return value, nil
	case failure := <-failures:
		return zero, fmt.Errorf("%s: producer failed: %s", name, failure)
	case <-ctx.Done():
		return zero, fmt.Errorf("%s: %w", name, context.Cause(ctx))
	}
}
