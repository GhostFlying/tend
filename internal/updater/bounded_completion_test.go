//go:build linux

package updater

import (
	"context"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func unpreparedBoundedCompletionFixture(t *testing.T, variant ...string) (*harness, protocol.CommittedMaintenancePlan, *boundedHistoryAuthority) {
	t.Helper()
	h, original, base := committedFixture(t)
	if err := h.s.CancelCommittedMaintenance(t.Context(), original, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	base.archive.Preparation = protocol.ArchivePreparation{Revision: 1, PreparedAtMillis: 10, UpdatedAtMillis: 20}
	base.archive.Hosts[0].Control.Mode = "maintenance"
	base.archive.Hosts[0].Runtime.Mode = "maintenance"
	base.archive.Hosts = append(base.archive.Hosts, protocol.ArchiveHostStatus{HostID: "another-host", Control: protocol.RunnerControl{Mode: "maintenance", Revision: 7}, OpenRuns: 2})
	a := &boundedHistoryAuthority{committedTestAuthority: base}
	h.opts.Authority, h.s.opts.Authority = a, a
	p := original
	p.MaintenanceID = "unprepared-completion"
	p.RequestID, p.LinkRequestID = "req_00000000000000000000000011", "req_00000000000000000000000012"
	p.PreparationRequestID, p.PreparationCancelRequestID = "req_00000000000000000000000013", "req_00000000000000000000000014"
	p.HostControls = []protocol.MaintenanceHostControl{
		{HostID: base.archive.Hosts[0].HostID, Revision: 5, PreviousMode: "maintenance", RequestID: "req_00000000000000000000000015", RestoreRequestID: "req_00000000000000000000000016"},
		{HostID: "another-host", Revision: 7, PreviousMode: "maintenance", RequestID: "req_00000000000000000000000017", RestoreRequestID: "req_00000000000000000000000018"},
	}
	pendingPrepare := len(variant) != 0 && variant[0] == "pending_prepare"
	if pendingPrepare {
		base.archive.Preparation.Revision = p.PreparationRevision - 1
	}
	if len(variant) != 0 && variant[0] == "previous_normal" {
		p.HostControls[0].PreviousMode = "normal"
	}
	p.Successor.UpgradeID, p.Successor.ProcessInstanceID = p.MaintenanceID, "unprepared-successor"
	p.ExpectedGeneration, p.StateSHA256 = h.s.Status().Generation, digest(h.s.Status())
	// PreparationRevision is the expected post-prepare revision. This frozen
	// input is deliberately one behind the correct value; CAS 0 cannot win at 1.
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if !pendingPrepare {
		if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err == nil {
			t.Fatal("incorrect original prepare CAS unexpectedly succeeded")
		}
	}
	auth := boundedHistoryAuthorization(h, p, "req_00000000000000000000000019")
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force"); err != nil {
		t.Fatal(err)
	}
	boundedHistoryCommit(t, h)
	state := h.s.Status()
	if err := h.s.LinkCommittedMaintenance(*state.Maintenance.Receipt, state.Generation); err != nil {
		t.Fatal(err)
	}
	return h, p, a
}

func TestBoundedCompletionLeavesUnestablishedFenceUntouched(t *testing.T) {
	h, p, a := unpreparedBoundedCompletionFixture(t)
	// Retain the original expired completion outcome, then grant a separate
	// fresh window. The new completion must not erase either old failure.
	boundedHistoryAdvance(h, h.s.Status().Bounded.Authorization.ExpiresAtMillis-h.now.UnixMilli()+1)
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err == nil {
		t.Fatal("expired completion window was accepted")
	}
	auth := boundedHistoryAuthorization(h, p, "req_00000000000000000000000020")
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "extend"); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status()
	controls, starts, stops := len(a.controls), h.p.starts, h.p.stops
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, before.Generation-1); err == nil || !reflect.DeepEqual(before, h.s.Status()) {
		t.Fatal("completion bypassed its fresh generation CAS", err)
	}
	// Runtime observations can change without changing archive/host ownership.
	a.archive.HostedDrained = true
	a.archive.Hosts[0].OpenRuns++
	a.archive.Hosts[0].Runtime.AppliedControlRevision++
	archiveBefore, err := a.MaintenanceArchiveStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, before.Generation); err != nil {
		t.Fatal("installed force could not finish without changing the untouched baseline", err)
	}
	after := h.s.Status()
	if after.Bounded.Outcome != "completed" || len(after.Bounded.Audit) != len(before.Bounded.Audit)+1 || !reflect.DeepEqual(before.Bounded.Audit, after.Bounded.Audit[:len(before.Bounded.Audit)]) {
		t.Fatal("completion lost the old force/timeout audit")
	}
	if !reflect.DeepEqual(before.Maintenance, after.Maintenance) || before.Current != after.Current || before.Process != after.Process || !reflect.DeepEqual(before.CommittedProof, after.CommittedProof) || !reflect.DeepEqual(a.archive, archiveBefore) || len(a.controls) != controls || h.p.starts != starts || h.p.stops != stops {
		t.Fatal("completion changed frozen facts, external controls or the installed process")
	}
	h.s.Close()
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	reopened := h.s.Status()
	boundedHistoryAdvance(h, auth.Timeouts.OverallMillis+1)
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, reopened.Generation); err != nil || !reflect.DeepEqual(reopened, h.s.Status()) || len(a.controls) != controls || h.p.starts != starts || h.p.stops != stops {
		t.Fatal("reopened completion replay changed original facts or side effects", err)
	}
}

func TestBoundedCompletionRejectsChangedOrUnprovenBaseline(t *testing.T) {
	for _, change := range []string{"ordinary", "previous_normal", "no_force", "expired", "missing_audit", "wrong_source", "wrong_target", "wrong_plan", "missing_exit", "wrong_exit", "missing_receipt", "wrong_receipt", "missing_proof", "wrong_proof", "wrong_process", "prepared", "revision", "timestamp", "host_revision", "host_mode", "host_upgrade", "host_extra", "host_missing", "host_duplicate"} {
		t.Run(change, func(t *testing.T) {
			h, p, a := unpreparedBoundedCompletionFixture(t, change)
			fresh := boundedHistoryAuthorization(h, p, "req_00000000000000000000000020")
			if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, fresh, "extend"); err != nil {
				t.Fatal(err)
			}
			b, m := h.s.state.Bounded, h.s.state.Maintenance
			key := b.RecoveryAuthorizationID
			switch change {
			case "ordinary":
				h.s.state.Bounded = nil
			case "no_force":
				b.Audit[0].Action = "apply"
			case "expired":
				b.Authorization.ExpiresAtMillis = h.now.UnixMilli() - 1
				b.Authorizations[b.Authorization.RequestID] = b.Authorization
			case "missing_audit":
				b.Audit[0].Archive = nil
			case "wrong_source", "wrong_target", "wrong_plan":
				auth := b.Authorizations[key]
				if change == "wrong_source" {
					auth.Source.StartTicks++
				}
				if change == "wrong_target" {
					auth.Target.ProcessInstanceID = "another-target"
				}
				if change == "wrong_plan" {
					auth.PlanSHA256 = "different-plan"
				}
				b.Authorizations[key] = auth
			case "missing_exit":
				b.SourceExit = nil
			case "wrong_exit":
				b.SourceExit.Process.StartTicks++
			case "missing_receipt":
				m.Receipt = nil
			case "wrong_receipt":
				m.Receipt.Process.StartTicks++
			case "missing_proof":
				h.s.state.CommittedProof = nil
			case "wrong_proof":
				h.s.state.CommittedProof.Binding.ProcessInstanceID = "another-target"
			case "wrong_process":
				h.s.state.Process.StartTicks++
			case "prepared":
				a.archive.Preparation.Prepared = true
			case "revision":
				a.archive.Preparation.Revision++
			case "timestamp":
				a.archive.Preparation.UpdatedAtMillis++
			case "host_revision":
				a.archive.Hosts[0].Control.Revision++
			case "host_mode":
				a.archive.Hosts[0].Control.Mode = "normal"
			case "host_upgrade":
				a.archive.Hosts[0].Control.UpgradeID = "another-upgrade"
			case "host_extra":
				a.archive.Hosts = append(a.archive.Hosts, protocol.ArchiveHostStatus{HostID: "foreign-host"})
			case "host_missing":
				a.archive.Hosts = a.archive.Hosts[:1]
			case "host_duplicate":
				a.archive.Hosts[1] = a.archive.Hosts[0]
			}
			if err := h.s.save(); err != nil {
				t.Fatal(err)
			}
			starts, stops := h.p.starts, h.p.stops
			if err := h.s.FinishCommittedMaintenance(context.Background(), p, h.s.Status().Generation); err == nil || h.p.starts != starts || h.p.stops != stops {
				t.Fatal("unproven or changed baseline was completed", err)
			}
			if b := h.s.Status().Bounded; b != nil && b.Outcome == "completed" {
				t.Fatal("foreign baseline marked completed")
			}
		})
	}
}

func TestBoundedCompletionDoesNotClassifyViableLatePrepareAsImpossible(t *testing.T) {
	h, p, a := unpreparedBoundedCompletionFixture(t, "pending_prepare")
	before := h.s.Status()
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, before.Generation); err != nil {
		t.Fatal("ordinary baseline restoration changed", err)
	}
	if !reflect.DeepEqual(before.Bounded.Audit, h.s.Status().Bounded.Audit) {
		t.Fatal("a still-viable original prepare was classified as unestablished")
	}
	if err := a.MaintenanceArchiveControl(t.Context(), "prepare", protocol.ArchiveControlRequest{RequestID: p.PreparationRequestID, ExpectedControlRevision: p.PreparationRevision - 1}); err != nil {
		t.Fatal("fixture did not preserve the original delayed prepare's valid CAS", err)
	}
}

func restartUnfinishedBoundedCompletion(t *testing.T, h *harness) State {
	t.Helper()
	before := h.s.Status()
	if before.Maintenance.Phase != "ledger_linked" || before.Bounded.Outcome == "completed" {
		t.Fatal("fixture must retain its unfinished linked maintenance")
	}
	delete(h.p.live, before.Process.PID)
	for range 4 {
		if err := h.s.Step(t.Context()); err != nil {
			t.Fatal("existing same-version supervision failed", err)
		}
	}
	after := h.s.Status()
	if after.Operation == nil || after.Operation.Kind != "restart" || after.Operation.Phase != "committed" || after.Current != before.Current || after.Process == before.Process || after.Process.SHA256 != before.Process.SHA256 || after.CommittedProof.Binding != before.CommittedProof.Binding || after.CommittedProof.Process != after.Process || !reflect.DeepEqual(before.Maintenance, after.Maintenance) || !reflect.DeepEqual(before.Bounded, after.Bounded) {
		t.Fatal("supervision changed historical maintenance or failed to prove its current writer")
	}
	return before
}

func TestBoundedCompletionAcceptsExistingSupervisedWriter(t *testing.T) {
	h, p, a := unpreparedBoundedCompletionFixture(t)
	original := restartUnfinishedBoundedCompletion(t, h)
	before := h.s.Status()
	controls, starts, stops := len(a.controls), h.p.starts, h.p.stops
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, before.Generation); err != nil {
		t.Fatal("legitimately supervised current writer could not finish", err)
	}
	after := h.s.Status()
	if after.Bounded.Outcome != "completed" || !reflect.DeepEqual(original.Maintenance, after.Maintenance) || !reflect.DeepEqual(before.CommittedProof, after.CommittedProof) || after.Process != before.Process || after.Current != before.Current || len(a.controls) != controls || h.p.starts != starts || h.p.stops != stops {
		t.Fatal("completion replaced historical proof or performed another service action")
	}
}

func TestBoundedCompletionRejectsUnprovenSupervisedWriter(t *testing.T) {
	for _, change := range []string{"historical_process", "historical_receipt", "current_binding", "current_proof", "current_process", "kernel_process", "unknown_operation"} {
		t.Run(change, func(t *testing.T) {
			h, p, _ := unpreparedBoundedCompletionFixture(t)
			restartUnfinishedBoundedCompletion(t, h)
			s := &h.s.state
			switch change {
			case "historical_process":
				s.Maintenance.Process = s.Process
			case "historical_receipt":
				s.Maintenance.Receipt.Process = s.Process
			case "current_binding":
				s.CommittedProof.Binding.ProcessInstanceID = s.Process.LaunchID
			case "current_proof":
				s.CommittedProof.Process.StartTicks++
			case "current_process":
				s.Process.StartTicks++
			case "kernel_process":
				delete(h.p.live, s.Process.PID)
			case "unknown_operation":
				s.Operation.Phase = "starting"
			}
			if err := h.s.save(); err != nil {
				t.Fatal(err)
			}
			starts, stops := h.p.starts, h.p.stops
			if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err == nil || h.p.starts != starts || h.p.stops != stops || h.s.Status().Bounded.Outcome == "completed" {
				t.Fatal("unproven current/historical writer was accepted", err)
			}
		})
	}
}
