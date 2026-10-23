//go:build linux

package updater

import (
	"context"
	"testing"
)

func TestCancelledCandidateProofSurvivesRestartAndDuplicateCancellation(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	binding := h.s.Status().Operation.Binding
	if err := h.s.CancelExpected(binding.UpgradeID, "staged"); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	original := CancelledReceipt(h.s.Status(), binding.UpgradeID)
	if original == nil {
		t.Fatal("cancel proof not durable")
	}
	delete(h.p.live, h.s.Status().Process.PID)
	h.step(t)
	h.until(t, "committed")
	if err := h.s.CancelExpected(binding.UpgradeID, "staged"); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	receipt := CancelledReceipt(h.s.Status(), binding.UpgradeID)
	if receipt == nil || receipt.Binding != binding || receipt.CurrentProcessInstanceID != h.s.Status().Process.LaunchID || receipt.CurrentProcessInstanceID == original.CurrentProcessInstanceID {
		t.Fatal("duplicate cancellation lost full candidate or current restart identity")
	}
	if err := h.s.Upgrade(context.Background(), binding.UpgradeID, h.bundle); err == nil {
		t.Fatal("cancelled candidate restarted")
	}
}
func TestCancellationAfterAuthorizationCannotClaimNoStart(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	binding := h.s.Status().Operation.Binding
	h.until(t, "stopping")
	if h.s.CancelExpected(binding.UpgradeID, "staged") == nil {
		t.Fatal("staged CAS crossed authorization")
	}
	if CancelledReceipt(h.s.Status(), binding.UpgradeID) != nil {
		t.Fatal("stopping represented as no-start")
	}
}
