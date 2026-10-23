//go:build linux

package updater

import (
	"context"
	"os"
	"testing"
)

func TestCommitProofRequiresSuccessfulDurableWrite(t *testing.T) {
	for _, which := range []string{"before-write", "after-rename"} {
		t.Run(which, func(t *testing.T) {
			h := newHarness(t)
			h.upgrade(t)
			h.until(t, "checking")
			h.step(t)
			h.s.writeState = func(path string, state State) error {
				if which == "after-rename" {
					if err := writeJSON(path, state); err != nil {
						return err
					}
				}
				return os.ErrPermission
			}
			if err := h.s.Step(context.Background()); err == nil {
				t.Fatal("failed durable commit reported success")
			}
			if ProcessReceipt(h.s.Status()) != nil || h.s.Status().Operation.Phase != "checking" {
				t.Fatal("unpersisted commit published")
			}
			if h.s.Retry("upgrade-1") == nil || h.s.CancelExpected("late", "staged") == nil || h.s.Step(context.Background()) == nil {
				t.Fatal("mutations continued after durability became unknown")
			}
			h.reopen(t)
			h.until(t, "committed")
			if CommittedReceipt(h.s.Status()) == nil {
				t.Fatal("reopened durable state did not reconcile")
			}
		})
	}
}
func TestFailedCancellationWriteCannotBeAcknowledgedByStatus(t *testing.T) {
	h := newHarness(t)
	h.s.writeState = func(string, State) error { return os.ErrPermission }
	if h.s.CancelExpected("cancelled-id", "staged") == nil {
		t.Fatal("failed tombstone write accepted")
	}
	if Cancelled(h.s.Status(), "cancelled-id") {
		t.Fatal("status published unpersisted cancellation")
	}
	h.reopen(t)
	if Cancelled(h.s.Status(), "cancelled-id") {
		t.Fatal("failed tombstone unexpectedly persisted")
	}
	if err := h.s.CancelExpected("cancelled-id", "staged"); err != nil {
		t.Fatal(err)
	}
	if !Cancelled(h.s.Status(), "cancelled-id") {
		t.Fatal("fresh durable cancellation missing")
	}
}
func TestRollbackProofWriteFailureReconcilesKnownOldProcessWithoutDuplicateLaunch(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "stopped")
	h.boot += 31 * 60 * 1000
	h.s.writeState = func(path string, state State) error {
		if state.Operation != nil && state.Operation.Phase == "rolled_back" {
			return os.ErrPermission
		}
		return writeJSON(path, state)
	}
	if h.s.Step(context.Background()) == nil {
		t.Fatal("failed rollback proof reported success")
	}
	if RollbackReceipt(h.s.Status()) != nil {
		t.Fatal("unpersisted rollback proof published")
	}
	starts := h.p.starts
	h.reopen(t)
	h.until(t, "rolled_back")
	if h.p.starts != starts || RollbackReceipt(h.s.Status()) == nil {
		t.Fatal("rollback recovery duplicated the old process or lost proof")
	}
}
