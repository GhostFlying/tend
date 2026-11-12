//go:build linux

package updater

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"reflect"
	"testing"
)

func TestBoundedCompletionTimeoutForceRejectsForeignCancelledFence(t *testing.T) {
	h, p, base := committedFixture(t)
	if err := h.s.CancelCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	base.archive.Preparation = protocol.ArchivePreparation{Revision: 1}
	base.archive.Hosts[0].Control.Mode = protocol.RunnerMaintenance
	base.archive.Hosts[0].Runtime.Mode = protocol.RunnerMaintenance
	a := &boundedHistoryAuthority{committedTestAuthority: base}
	h.opts.Authority, h.s.opts.Authority = a, a
	p.MaintenanceID = "timeout-force-review"
	p.RequestID, p.LinkRequestID = "req_00000000000000000000000011", "req_00000000000000000000000012"
	p.PreparationRequestID, p.PreparationCancelRequestID = "req_00000000000000000000000013", "req_00000000000000000000000014"
	p.HostControls = []protocol.MaintenanceHostControl{{HostID: base.archive.Hosts[0].HostID, Revision: 5, PreviousMode: protocol.RunnerMaintenance, RequestID: "req_00000000000000000000000015", RestoreRequestID: "req_00000000000000000000000016"}}
	p.Successor.UpgradeID, p.Successor.ProcessInstanceID = p.MaintenanceID, "timeout-review-successor"
	p.ExpectedGeneration, p.StateSHA256 = h.s.Status().Generation, digest(h.s.Status())
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err == nil {
		t.Fatal("incorrect prepare CAS accepted")
	}
	auth := boundedHistoryAuthorization(h, p, "req_00000000000000000000000019")
	auth.ForceOnDrainTimeout = true
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "apply"); err != nil {
		t.Fatal(err)
	}
	boundedHistoryAdvance(h, auth.Timeouts.DrainMillis)
	boundedHistoryCommit(t, h)
	st := h.s.Status()
	if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
		t.Fatal(err)
	}
	// A separate valid archive cancel advances false/P to false/P+1 without
	// ever establishing the original plan's prepared=true/P fence.
	if err := a.MaintenanceArchiveControl(t.Context(), "cancel", protocol.ArchiveControlRequest{RequestID: "req_00000000000000000000000020", ExpectedControlRevision: 1}); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status()
	controls, starts, stops := len(a.controls), h.p.starts, h.p.stops
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err == nil {
		t.Fatalf("foreign cancel was accepted as this window's restored fence; outcome=%s audits=%+v", h.s.Status().Bounded.Outcome, h.s.Status().Bounded.Audit)
	}
	after := h.s.Status()
	if after.Bounded.Outcome != "installed_recovery_pending" || !reflect.DeepEqual(before.Bounded.Audit, after.Bounded.Audit) || !reflect.DeepEqual(before.Maintenance, after.Maintenance) || len(a.controls) != controls || h.p.starts != starts || h.p.stops != stops {
		t.Fatal("rejected foreign restoration changed the original audit or external controls")
	}
}

func TestBoundedCompletionTimeoutForceAcceptsOriginallyPreparedRestoredFence(t *testing.T) {
	h, p, a := boundedHistoryFixture(t)
	auth := boundedHistoryAuthorization(h, p, "req_00000000000000000000000019")
	auth.ForceOnDrainTimeout = true
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "apply"); err != nil {
		t.Fatal(err)
	}
	boundedHistoryAdvance(h, auth.Timeouts.DrainMillis)
	boundedHistoryCommit(t, h)
	state := h.s.Status()
	if err := h.s.LinkCommittedMaintenance(*state.Maintenance.Receipt, state.Generation); err != nil {
		t.Fatal(err)
	}
	forced := false
	for _, entry := range h.s.Status().Bounded.Audit {
		if entry.Action == "force_on_drain_timeout" && entry.RequestID == auth.RequestID {
			forced = true
		}
	}
	if !forced {
		t.Fatal("fixture did not use the real timeout-triggered force path")
	}
	// Official controls can be restored before the updater finishes. Their exact
	// poststates satisfy the original plan without repeating any external write.
	for _, host := range p.HostControls {
		if host.PreviousMode != protocol.RunnerNormal {
			continue
		}
		if err := a.MaintenanceArchiveControl(t.Context(), "host", protocol.ArchiveHostControlRequest{RequestID: host.RestoreRequestID, ExpectedControlRevision: host.Revision + 1, HostID: host.HostID, Mode: host.PreviousMode}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.MaintenanceArchiveControl(t.Context(), "cancel", protocol.ArchiveControlRequest{RequestID: p.PreparationCancelRequestID, ExpectedControlRevision: p.PreparationRevision}); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status()
	controls, starts, stops := len(a.controls), h.p.starts, h.p.stops
	archive, err := a.MaintenanceArchiveStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, before.Generation); err != nil {
		t.Fatal("originally established fence could not finish", err)
	}
	after := h.s.Status()
	actual, err := a.MaintenanceArchiveStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Bounded.Outcome != "completed" || !reflect.DeepEqual(before.Bounded.Audit, after.Bounded.Audit) || !reflect.DeepEqual(before.Maintenance, after.Maintenance) || !reflect.DeepEqual(archive, actual) || before.Process != after.Process || before.Current != after.Current || len(a.controls) != controls || h.p.starts != starts || h.p.stops != stops {
		t.Fatal("completion changed restored controls, original audit or installed service")
	}
	h.s.Close()
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil || len(a.controls) != controls || h.p.starts != starts || h.p.stops != stops {
		t.Fatal("reopened completion repeated an external effect", err)
	}
}
