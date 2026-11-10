package mainthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRunnerSessionShutdownRetainsUnconfirmedClose(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	seedClosingReceiptMetadata(t, e, "completed", true)
	ctx, cancel := context.WithCancel(t.Context())
	session := RunnerSession(t.Context(), e.app, "receipt-reader-run", ctx)
	cancel()
	done := make(chan error, 1)
	go func() { done <- session.Close() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for a runner that has already stopped")
	}
	if e.count(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id='receipt-reader-run' AND method='close' AND request_id='close_cached' AND status='pending'`) != 1 {
		t.Fatal("shutdown changed or replaced the original unconfirmed close")
	}
	if e.count(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id='receipt-reader-run' AND method='close'`) != 1 {
		t.Fatal("shutdown queued a duplicate close")
	}
	if e.count(`SELECT COUNT(*) FROM runs WHERE run_id='receipt-reader-run' AND observed_state='unknown'`) != 1 {
		t.Fatal("shutdown invented physical stop evidence")
	}
}
