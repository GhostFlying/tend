//go:build linux

package updater

import (
	"context"
	"testing"
)

func TestCancellationTombstoneFencesLateUpgradeAcrossRestartAndNextBatch(t *testing.T) {
	h := newHarness(t)
	if err := h.s.CancelExpected("delayed", "staged"); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	if !Cancelled(h.s.Status(), "delayed") {
		t.Fatal("cancellation not durable")
	}
	if err := h.s.Upgrade(context.Background(), "delayed", h.bundle); err == nil {
		t.Fatal("late request revived cancelled identity")
	}
	h.upgrade(t)
	if err := h.s.CancelExpected("another-delayed", "staged"); err != nil {
		t.Fatal(err)
	}
	if h.s.Status().Operation.ID != "upgrade-1" || h.s.Status().Operation.Phase != "staged" {
		t.Fatal("unrelated active operation modified")
	}
	h.until(t, "committed")
	h.reopen(t)
	if err := h.s.Upgrade(context.Background(), "next", h.bundle); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Upgrade(context.Background(), "delayed", h.bundle); err == nil {
		t.Fatal("next operation discarded cancellation fence")
	}
	snapshot := h.s.Status()
	delete(snapshot.Terminals, "delayed")
	if !Cancelled(h.s.Status(), "delayed") {
		t.Fatal("status exposes mutable terminal receipts")
	}
}
func TestHistoricalTerminalUpgradeDoesNotExecuteAgain(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "committed")
	h.reopen(t)
	original := h.s.Status().Operation.Binding
	starts := h.p.starts
	if err := h.s.Upgrade(context.Background(), "next", h.bundle); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Upgrade(context.Background(), "upgrade-1", h.bundle); err != nil {
		t.Fatal(err)
	}
	if h.s.Status().Operation.ID != "next" || h.p.starts != starts {
		t.Fatal("historical upgrade ran again")
	}
	if h.s.Status().Terminals["upgrade-1"].Binding != original {
		t.Fatal("historical binding lost")
	}
	altered := makeBundle(t, []byte("other executable"))
	if err := h.s.Upgrade(context.Background(), "upgrade-1", altered); err == nil {
		t.Fatal("different historical payload accepted")
	}
}
