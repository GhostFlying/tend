//go:build linux

package updater

import (
	"context"
	"testing"
)

func TestRestoredSupervisorPersistsUsableRollbackReceipt(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	candidate := h.s.Status().Operation.Binding
	h.until(t, "backed_up")
	h.p.rejectStart = true
	_ = h.s.Step(context.Background())
	h.until(t, "rolled_back")
	h.reopen(t)
	receipt := RollbackReceipt(h.s.Status())
	if receipt == nil || receipt.Binding != candidate || receipt.CurrentSHA256 != h.s.Status().Current.Artifact.SHA256 {
		t.Fatalf("actual rollback lost terminal proof: %+v", receipt)
	}
}

func TestRollbackProofFollowsConfirmedSameVersionCrashRecovery(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	original := h.s.Status().Operation.Binding
	h.until(t, "stopped")
	h.boot += 31 * 60 * 1000
	h.until(t, "rolled_back")
	previous := h.s.Status().Process
	for i := 0; i < 2; i++ {
		delete(h.p.live, previous.PID)
		h.step(t) // persist automatic restart intent
		if h.s.Status().Operation.Kind != "restart" {
			t.Fatal("crash did not enter restart")
		}
		h.p.ambiguousStart = true
		if err := h.s.Step(context.Background()); err == nil {
			t.Fatal("ambiguous new process not reported")
		}
		if receipt := RollbackReceipt(h.s.Status()); receipt == nil || receipt.CurrentProcessInstanceID != previous.LaunchID {
			t.Fatal("unconfirmed launch changed proof")
		}
		h.reopen(t)
		h.until(t, "committed")
		receipt := RollbackReceipt(h.s.Status())
		current := h.s.Status().Process
		if receipt == nil || receipt.Binding != original || receipt.CurrentProcessInstanceID != current.LaunchID || current.LaunchID == previous.LaunchID {
			t.Fatal("original rollback provenance lost after restart")
		}
		previous = current
	}
	h.reopen(t)
	snapshot := h.s.Status()
	snapshot.RollbackProof.Process.LaunchID = "forged"
	if RollbackReceipt(snapshot) != nil {
		t.Fatal("wrong process proof accepted")
	}
	if RollbackReceipt(h.s.Status()) == nil {
		t.Fatal("status exposed mutable proof")
	}
	// Changing even same-hash version metadata must not reuse old provenance.
	snapshot = h.s.Status()
	snapshot.Current.ReleaseID = "different-version"
	if RollbackReceipt(snapshot) != nil {
		t.Fatal("different committed version inherited proof")
	}
}
