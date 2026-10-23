package updater

import (
	"context"
	"os"
	"testing"
)

func TestExpiredGrantAfterStopRecoversOldProcessWithoutRestoringData(t *testing.T) {
	for _, phase := range []string{"stopped", "backed_up"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			h.upgrade(t)
			h.until(t, phase)
			h.boot += 31 * 60 * 1000
			// A backup never authorizes overwriting newer bytes. This recovery path only
			// launches the previous executable; candidate Start has not occurred.
			if err := os.WriteFile(h.data, []byte("unchanged by updater"), 0600); err != nil {
				t.Fatal(err)
			}
			h.reopen(t)
			h.until(t, "rolled_back")
			if h.p.starts != 1 || RollbackReceipt(h.s.Status()) == nil {
				t.Fatal("old process not recovered with terminal proof")
			}
			raw, _ := os.ReadFile(h.data)
			if string(raw) != "unchanged by updater" {
				t.Fatal("prelaunch recovery rewrote business data")
			}
		})
	}
}
func TestCancelExpectedDoesNotRacePastStaged(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	id := h.s.Status().Operation.ID
	h.step(t) // authorization has crossed the safely staged boundary
	if err := h.s.CancelExpected(id, "staged"); err == nil {
		t.Fatal("stale phase accepted")
	}
	if h.s.Status().Operation.CancelRequested {
		t.Fatal("stale cancellation changed durable intent")
	}
	h2 := newHarness(t)
	h2.upgrade(t)
	id2 := h2.s.Status().Operation.ID
	if err := h2.s.CancelExpected(id2, "staged"); err != nil {
		t.Fatal(err)
	}
	if h2.s.Status().Operation.Phase != "cancelled" {
		t.Fatal("staged operation not cancelled")
	}
	if err := h2.s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h2.s.Upgrade(context.Background(), "next-batch", h2.bundle); err != nil {
		t.Fatalf("cancel blocks next upgrade: %v", err)
	}
}

func TestExpiredStoppingReconcilesOriginalProcessWithoutGuessing(t *testing.T) {
	for _, which := range []string{"alive", "already-stopped", "reused-pid"} {
		t.Run(which, func(t *testing.T) {
			h := newHarness(t)
			h.upgrade(t)
			h.until(t, "stopping")
			old := h.s.Status().Operation.PreviousProcess
			if which == "already-stopped" {
				delete(h.p.live, old.PID)
			}
			if which == "reused-pid" {
				other := old
				other.StartTicks++
				h.p.live[old.PID] = other
			}
			h.boot += 31 * 60 * 1000
			h.reopen(t)
			if which == "reused-pid" {
				if err := h.s.Step(context.Background()); err == nil {
					t.Fatal("reused PID accepted")
				}
				if h.p.starts != 0 || h.p.stops != 0 {
					t.Fatal("uncertain process modified")
				}
				return
			}
			h.until(t, "rolled_back")
			receipt := RollbackReceipt(h.s.Status())
			if receipt == nil {
				t.Fatal("no terminal process identity proof")
			}
			if which == "alive" && (h.p.starts != 0 || h.p.stops != 0 || receipt.CurrentProcessInstanceID != old.LaunchID) {
				t.Fatal("live original disturbed")
			}
			if which == "already-stopped" && h.p.starts != 1 {
				t.Fatal("confirmed stopped original not recovered")
			}
		})
	}
}

func TestExpiredGrantRecoveryCanRetryDefiniteOldLaunchFailure(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "stopped")
	h.boot += 31 * 60 * 1000
	h.p.rejectStart = true
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("definite launch failure not reported")
	}
	if h.s.Status().Operation.Phase != "rollback_ready" {
		t.Fatal("known no-launch proof not retained")
	}
	h.reopen(t)
	h.until(t, "rolled_back")
	if h.p.starts != 1 || RollbackReceipt(h.s.Status()) == nil {
		t.Fatal("recoverable old-version start stranded")
	}
}
