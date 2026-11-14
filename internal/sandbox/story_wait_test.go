package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestStoryWaitUsesPreparationBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
		defer cancel()
		started := time.Now()
		reads := 0
		if err := storyWait(ctx, "slow healthy turn", func() (bool, error) {
			reads++
			return time.Since(started) >= 91*time.Second, nil
		}); err != nil {
			t.Fatal("healthy preparation was cut off by a hidden per-round limit", err)
		}
		if reads < 2 || ctx.Err() != nil {
			t.Fatal("wait did not observe actual readiness within its parent budget", reads, ctx.Err())
		}
	})
}

func TestStoryWaitRetainsDeadlineCancellationAndReadFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		err := storyWait(ctx, "never ready", func() (bool, error) { return false, nil })
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "never ready") {
			t.Fatal("missing finite named failure", err)
		}
		ctx, stop := context.WithCancelCause(t.Context())
		cause := errors.New("service stopped")
		stop(cause)
		err = storyWait(ctx, "cancelled", func() (bool, error) {
			t.Fatal("read after service cancellation")
			return true, nil
		})
		if !errors.Is(err, cause) {
			t.Fatal("cancellation lost its original cause", err)
		}
		readErr := errors.New("read failed")
		err = storyWait(t.Context(), "read", func() (bool, error) { return false, readErr })
		if !errors.Is(err, readErr) {
			t.Fatal("actual read failure was hidden", err)
		}
	})
}
