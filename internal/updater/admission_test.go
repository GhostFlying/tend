//go:build linux

package updater

import (
	"context"
	"os"
	"testing"
)

func TestUnhealthyCandidateResetSurvivesUpdaterRestart(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	h.step(t)
	h.a.unhealthy = true
	h.step(t)
	h.reopen(t)
	if h.s.Status().Operation.HealthCount != 0 {
		t.Fatal("unhealthy observation did not persist the health counter reset")
	}
	h.a.unhealthy = false
	h.step(t)
	if h.s.Status().Operation.Phase == "committed" {
		t.Fatal("candidate committed after only one healthy observation following reset")
	}
	h.step(t)
	if h.s.Status().Operation.Phase != "committed" {
		t.Fatal("candidate did not commit after two consecutive healthy observations")
	}
}

func plannedRollback(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	h.a.unhealthy = true
	h.a.rollback = true
	h.now = h.now.Add(11e9)
	h.until(t, "rollback_stopping")
	return h
}
func TestRollbackRefreshesFenceBeforeStoppingLiveCandidate(t *testing.T) {
	h := plannedRollback(t)
	stops := h.p.stops
	h.reopen(t)
	h.a.accepted = true
	if err := os.WriteFile(h.data, []byte("newly admitted data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("stale rollback permission accepted")
	}
	if h.p.stops != stops || !h.s.Status().Operation.Irreversible {
		t.Fatal("admission did not latch before stop")
	}
	h.reopen(t)
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("irreversible rollback resumed")
	}
	body, _ := os.ReadFile(h.data)
	if string(body) != "newly admitted data" {
		t.Fatal("new business data overwritten")
	}
}
func TestStoppedCandidateUsesPersistedAdmissionFenceWithoutDeadBridge(t *testing.T) {
	h := plannedRollback(t)
	h.a.failHealth = true
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("live candidate stopped without fresh authority")
	}
	delete(h.p.live, h.s.Status().Operation.CandidateProcess.PID)
	h.reopen(t)
	h.until(t, "rolled_back")
	if RollbackReceipt(h.s.Status()) == nil {
		t.Fatal("persistent fence could not complete after bridge exit")
	}
}
func TestRestoreRejectsMissingOrInvalidPersistentFence(t *testing.T) {
	for _, which := range []string{"missing", "unfenced", "control-revision"} {
		t.Run(which, func(t *testing.T) {
			h := plannedRollback(t)
			h.step(t)
			if h.s.Status().Operation.Phase != "restoring" {
				t.Fatal("candidate not stopped")
			}
			op := h.s.state.Operation
			switch which {
			case "missing":
				op.RollbackAuthorization = nil
			case "unfenced":
				op.RollbackAuthorization.AdmissionFenced = false
			case "control-revision":
				op.RollbackAuthorization.ControlRevision = 0
			}
			if err := h.s.save(); err != nil {
				t.Fatal(err)
			}
			h.reopen(t)
			if err := os.WriteFile(h.data, []byte("preserve data"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := h.s.Step(context.Background()); err == nil {
				t.Fatal("restore allowed without persistent fence")
			}
			body, _ := os.ReadFile(h.data)
			if string(body) != "preserve data" {
				t.Fatal("unproved restore modified data")
			}
		})
	}
}
func TestCommittedProofFollowsVerifiedSuccessorAndDisablesRollback(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	binding := h.s.Status().Operation.Binding
	h.until(t, "committed")
	if !h.s.Status().Operation.Irreversible {
		t.Fatal("local commit did not latch irreversible")
	}
	for i := 0; i < 2; i++ {
		old := h.s.Status().Process
		delete(h.p.live, old.PID)
		h.step(t)
		h.p.ambiguousStart = true
		if err := h.s.Step(context.Background()); err == nil {
			t.Fatal("unknown restart not reported")
		}
		if receipt := CommittedReceipt(h.s.Status()); receipt == nil || receipt.CurrentProcessInstanceID != old.LaunchID {
			t.Fatal("unproved successor advertised")
		}
		h.reopen(t)
		h.until(t, "committed")
		receipt := ProcessReceipt(h.s.Status())
		if receipt == nil || receipt.Phase != "committed" || !receipt.CommittedConfirmed || receipt.RollbackConfirmed || receipt.Binding != binding || receipt.CurrentProcessInstanceID != h.s.Status().Process.LaunchID || receipt.CurrentProcessInstanceID == old.LaunchID {
			t.Fatal("original candidate identity lost across verified restart")
		}
	}
	if RollbackReceipt(h.s.Status()) != nil {
		t.Fatal("committed version reported rollback")
	}
	snapshot := h.s.Status()
	snapshot.CommittedProof.Process.LaunchID = "forged"
	if CommittedReceipt(snapshot) != nil {
		t.Fatal("unmatched current process accepted")
	}
	if ProcessReceipt(h.s.Status()) == nil {
		t.Fatal("status proof aliased")
	}
}
