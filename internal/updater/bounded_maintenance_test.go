//go:build linux

package updater

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBoundedMaintenanceForcePreservesUndrainedObservationAndCommits(t *testing.T) {
	h, p, a := committedFixture(t)
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = false
	a.archive.Blockers = []string{"native receipt reads: 3", "summary operations: 14", "hosted operations are active or uncertain"}
	original := h.s.Status()
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: original.Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(p), Role: "server", DataIdentity: boundedServerTestData(h), Source: p.PreviousProcess, Target: p.Successor, Reason: "explicit recovery of unconfirmed hosted work", ExpiresAtMillis: h.s.opts.Now().Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force"); err != nil {
		t.Fatal(err)
	}
	for range 7 {
		if err := h.s.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	st := h.s.Status()
	if st.Format != 4 || st.Maintenance.Phase != "successor_committed" || st.Maintenance.Receipt == nil || st.Bounded.SourceExit == nil {
		t.Fatalf("force did not commit with predecessor exit proof: %+v", st)
	}
	if st.Maintenance.ReceiptRecoveryAuthorized || st.Maintenance.ArchiveAuthorization.HostedDrained || len(st.Maintenance.ArchiveAuthorization.Blockers) != 3 || st.Bounded.Audit[0].Archive.HostedDrained {
		t.Fatal("force fabricated ordinary drain or receipt authorization")
	}
	if st.Bounded.SourceExit.Process != original.Process || h.p.stops != 1 {
		t.Fatal("force did not stop the exact original writer once")
	}
	if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	if a.archive.Preparation.Prepared || a.archive.Hosts[0].Control.Mode != "normal" {
		t.Fatal("force did not finish admission restoration")
	}
}

func TestBoundedMaintenancePollingCannotRenewDeadline(t *testing.T) {
	h, p, _ := committedFixture(t)
	st := h.s.Status()
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: st.Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(p), Role: "server", DataIdentity: boundedServerTestData(h), Source: p.PreviousProcess, Target: p.Successor, Reason: "bounded recovery", ExpiresAtMillis: h.s.opts.Now().Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force"); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status().Bounded
	if err := h.s.boundedEnter("stop", auth.Timeouts.StopMillis); err != nil {
		t.Fatal(err)
	}
	if after := h.s.Status().Bounded; after.PhaseEnteredAtMillis != before.PhaseEnteredAtMillis || after.PhaseDeadlineMillis != before.PhaseDeadlineMillis || after.OverallDeadlineMillis != before.OverallDeadlineMillis {
		t.Fatal("polling renewed the original deadline")
	}
}

type boundedHungStop struct{ *receiptCrashProcess }

func (p *boundedHungStop) Stop(_ context.Context, _ Identity, _ time.Duration) error {
	return ErrUnknown
}

func TestBoundedMaintenanceKillEntersFreshBackupAndHealthBudgets(t *testing.T) {
	h, p, a := committedFixture(t)
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	process := &boundedHungStop{&receiptCrashProcess{fakeProcess: h.p}}
	h.s.opts.Process = process
	st := h.s.Status()
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: st.Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(p), Role: "server", DataIdentity: boundedServerTestData(h), Source: p.PreviousProcess, Target: p.Successor, Reason: "bounded exact termination", ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Step(t.Context()); !errors.Is(err, ErrUnknown) {
		t.Fatal("stop outcome must remain unknown", err)
	}
	if h.p.starts != 0 {
		t.Fatal("candidate started while predecessor still exists")
	}
	h.boot += auth.Timeouts.StopMillis
	h.now = h.now.Add(time.Duration(auth.Timeouts.StopMillis) * time.Millisecond)
	if err := h.s.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if process.crashes != 1 || h.s.Status().Maintenance.Phase != "old_exited" {
		t.Fatal("exact forced stop did not finish")
	}
	// Backup must no longer inherit the kill deadline after predecessor exit.
	h.boot += auth.Timeouts.KillMillis + 1
	h.now = h.now.Add(time.Duration(auth.Timeouts.KillMillis+1) * time.Millisecond)
	if err := h.s.Step(t.Context()); err != nil {
		t.Fatal("backup inherited expired kill budget", err)
	}
	if h.s.Status().Bounded.Phase != "backup" {
		t.Fatal("backup phase was not entered")
	}
	for range 5 {
		if err := h.s.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	st = h.s.Status()
	if st.Maintenance.Phase != "successor_committed" || st.Bounded.Phase != "complete" || a.archive.HostedDrained {
		t.Fatal("force did not reach a distinct completion budget")
	}
}

func TestBoundedMaintenanceCancellationKeepsIndependentRequest(t *testing.T) {
	h, p, a := committedFixture(t)
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: h.s.Status().Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(p), Role: "server", DataIdentity: boundedServerTestData(h), Source: p.PreviousProcess, Target: p.Successor, Reason: "independent cancellation before physical replacement", ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
	if err := h.s.CancelBoundedMaintenance(t.Context(), p, auth); err != nil {
		t.Fatal(err)
	}
	st := h.s.Status()
	if st.Format != 4 || !reflect.DeepEqual(st.Bounded.Authorization, auth) || st.Bounded.Outcome != "cancelled" || st.Maintenance.Phase != "cancelled" || len(st.Bounded.Audit) != 1 || st.Bounded.Audit[0].Action != "cancel" || h.p.stops != 0 || a.archive.Preparation.Prepared {
		t.Fatal("cancellation lost independent authority or changed the original writer")
	}
	if err := h.s.CancelBoundedMaintenance(t.Context(), p, auth); err != nil {
		t.Fatal("original cancellation replay", err)
	}
	if after := h.s.Status(); after.Generation != st.Generation || len(after.Bounded.Audit) != len(st.Bounded.Audit) {
		t.Fatal("original cancellation replay renewed state")
	}
}
