// Package testphase names finite test watchdogs independently of business time.
package testphase

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Context starts a phase now, without spending its budget on earlier fixtures.
// Use the service/test parent, not an earlier phase, for successive phases.
// It never extends a parent deadline or replaces application clock semantics.
// Finish logs elapsed time and the actual cancellation cause, then cancels only
// this phase. Cleanup also calls Finish so failures retain the active phase.
func Context(t *testing.T, parent context.Context, name string, watchdog time.Duration) (context.Context, func()) {
	t.Helper()
	started := time.Now()
	cause := fmt.Errorf("test phase %s exceeded %s watchdog: %w", name, watchdog, context.DeadlineExceeded)
	ctx, cancel := context.WithTimeoutCause(parent, watchdog, cause)
	deadline, _ := ctx.Deadline()
	t.Logf("test phase=%q started watchdog=%s deadline=%s", name, watchdog, deadline.UTC().Format(time.RFC3339Nano))
	var once sync.Once
	finish := func() {
		once.Do(func() {
			t.Logf("test phase=%q finished elapsed=%s cause=%v", name, time.Since(started), context.Cause(ctx))
			cancel()
		})
	}
	t.Cleanup(finish)
	return ctx, finish
}
