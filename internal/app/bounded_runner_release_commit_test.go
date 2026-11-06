package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBoundedRunnerAdmissionWaitsForCommitWithoutBlockingFencedHealth(t *testing.T) {
	h, as, release := boundedRunnerAdmissionFixture(t)
	var rt protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetHostRuntime(t.Context(), tx.Q(), release.HostID)
		rt = row.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rt.UpdaterReceipt = nil
	rt.Mode = protocol.RunnerMaintenance
	rt.Sequence++
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityRetainedReconciliation)
	if op, err := retainedHello(t, h, &rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	before := upgradeCtl(h, release.HostID)
	op, err := h.app.Execute(t.Context(), owner, admissionReleaseEnvelope(h, release), ReleaseBoundedRunnerAdmission{})
	if code := boundedReleaseCode(op, err); code != protocol.CodeConflictPayload || !reflect.DeepEqual(before, upgradeCtl(h, release.HostID)) {
		t.Fatal("uncommitted initial process released admission", op, err)
	}
	health, err := h.app.RunnerUpgradeHealth(t.Context(), runner(release.HostID), release.Successor)
	if err != nil || !health.ServerConfirmed || !health.Healthy || !health.AdmissionFenced || health.NewWorkAccepted {
		t.Fatal("waiting for commit blocked the fenced installation health", health, err)
	}
	rt.Sequence++
	rt.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: release.Successor, Phase: "committed", CommittedConfirmed: true, CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: rt.Build.BinarySHA256}
	if op, err := retainedHello(t, h, &rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	// The earlier refusal made no release. A separately authorized action can
	// now use the actual committed observation and unchanged current host CAS.
	release.RequestID = h.ids.New(protocol.PrefixRequest)
	if op := h.exec(owner, admissionReleaseEnvelope(h, release), ReleaseBoundedRunnerAdmission{}); op.Status != protocol.OpSucceeded {
		t.Fatal("committed original installation could not release admission", op)
	}
}

func TestBoundedRunnerInitialAdmissionRejectsInvalidCommitReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.UpdaterReceipt)
	}{
		{name: "uncommitted", mutate: func(r *protocol.UpdaterReceipt) { r.CommittedConfirmed = false }},
		{name: "rollback", mutate: func(r *protocol.UpdaterReceipt) { r.RollbackConfirmed = true }},
		{name: "phase", mutate: func(r *protocol.UpdaterReceipt) { r.Phase = "checking" }},
		{name: "binding", mutate: func(r *protocol.UpdaterReceipt) { r.Binding.ProcessInstanceID = "another-installation" }},
		{name: "process", mutate: func(r *protocol.UpdaterReceipt) { r.CurrentProcessInstanceID = "another-process" }},
		{name: "bytes", mutate: func(r *protocol.UpdaterReceipt) { r.CurrentSHA256 = strings.Repeat("d", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, release := boundedRunnerAdmissionFixture(t)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				row, err := store.GetHostRuntime(t.Context(), tx.Q(), release.HostID)
				if err != nil {
					return err
				}
				tc.mutate(row.Runtime.UpdaterReceipt)
				return tx.PutHostRuntime(t.Context(), release.HostID, row)
			}); err != nil {
				t.Fatal(err)
			}
			before := upgradeCtl(h, release.HostID)
			op, err := h.app.Execute(t.Context(), owner, admissionReleaseEnvelope(h, release), ReleaseBoundedRunnerAdmission{})
			if boundedReleaseCode(op, err) != protocol.CodeConflictPayload || !reflect.DeepEqual(before, upgradeCtl(h, release.HostID)) {
				t.Fatal("invalid commit receipt released the initial target", op, err)
			}
		})
	}
}

func TestBoundedRunnerAdmissionReleaseCannotReplacePublishedLineageReceipt(t *testing.T) {
	h, _, release := boundedRunnerAdmissionFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetHostRuntime(t.Context(), tx.Q(), release.HostID)
		if err != nil {
			return err
		}
		row.Runtime.Capabilities = append(row.Runtime.Capabilities, protocol.CapabilityRunnerMaintenance)
		return tx.PutHostRuntime(t.Context(), release.HostID, row)
	}); err != nil {
		t.Fatal(err)
	}
	request := admissionReleaseEnvelope(h, release)
	first := h.exec(owner, request, ReleaseBoundedRunnerAdmission{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	for _, mode := range []string{protocol.RunnerMaintenance, protocol.RunnerNormal} {
		control := upgradeCtl(h, release.HostID)
		op := h.exec(owner, h.env("hosts.maintenance", protocol.Target{Type: "host", ID: release.HostID}, protocol.HostMaintenancePayload{Mode: mode}, rev(control.Revision)), HostMaintenance{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal("published release blocked ordinary maintenance control", op)
		}
	}
	// Control projections may be replaced by later workflows; the immutable
	// original release remains the only authority this journal can consume.
	projection := upgradeCtl(h, release.HostID)
	projection.BoundedRecoveryRelease = nil
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutHostControl(t.Context(), release.HostID, projection, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	before := upgradeCtl(h, release.HostID)
	second := release
	second.RequestID = h.ids.New(protocol.PrefixRequest)
	second.ExpectedControlRevision = before.Revision
	op, err := h.app.Execute(t.Context(), owner, admissionReleaseEnvelope(h, second), ReleaseBoundedRunnerAdmission{})
	if boundedReleaseCode(op, err) != protocol.CodeInvalidTransition || !reflect.DeepEqual(before, upgradeCtl(h, release.HostID)) {
		t.Fatal("a fresh request replaced the original published release", op, err)
	}
	if op.Error == nil || !strings.Contains(op.Error.Message, release.RequestID) {
		t.Fatal("refusal omitted the original release recovery identity", op, err)
	}
	if replay := h.exec(owner, request, ReleaseBoundedRunnerAdmission{}); !reflect.DeepEqual(first, replay) {
		t.Fatal("original release no longer replays after host CAS changes", first, replay)
	}
}

func TestBoundedRunnerNewRecoveryCanReleaseAfterPreviousLineage(t *testing.T) {
	h, _, first := boundedRunnerAdmissionFixture(t)
	if op := h.exec(owner, admissionReleaseEnvelope(h, first), ReleaseBoundedRunnerAdmission{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	previous, err := h.app.RunnerBoundedMaintenance(t.Context(), first.HostID, first.RecoveryAuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	plan := previous.Plan
	plan.ExpectedControlRevision = upgradeCtl(h, first.HostID).Revision
	plan.RunnerInstanceID = first.Successor.ProcessInstanceID
	plan.RunnerGeneration = first.RunnerGeneration
	plan.Source = protocol.RecoveryIdentity{PID: plan.Source.PID, StartTicks: 789, LaunchID: plan.RunnerInstanceID, SHA256: first.Successor.ArtifactSHA256}
	plan.Journal = first.Journal
	plan.Target.UpgradeID = "second-runner-recovery"
	plan.Target.ProcessInstanceID = "second-recovery-process"
	plan.Target.ArtifactSHA256 = strings.Repeat("d", 64)
	plan.Authorization.RequestID = h.ids.New(protocol.PrefixRequest)
	plan.Authorization.Source = plan.Source
	plan.Authorization.Target = plan.Target
	plan.Authorization.DataIdentity = &plan.Journal
	plan.Authorization.ExpectedGeneration++
	plan.Authorization.ExpectedPhase = "committed"
	plan.Authorization.PlanSHA256, err = protocol.RunnerBoundedMaintenancePlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan)
	if err != nil || grant.RecoveryAuthorizationID == first.RecoveryAuthorizationID {
		t.Fatal("independent replacement did not create its own recovery lineage", grant, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetHostRuntime(t.Context(), tx.Q(), first.HostID)
		if err != nil {
			return err
		}
		rt := &row.Runtime
		rt.InstanceID = plan.Target.ProcessInstanceID
		rt.Generation++
		rt.Build.BinarySHA256 = plan.Target.ArtifactSHA256
		rt.BoundedRecoveryAuthorizationID = grant.RecoveryAuthorizationID
		rt.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: plan.Target, Phase: "committed", CommittedConfirmed: true, CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: rt.Build.BinarySHA256}
		return tx.PutHostRuntime(t.Context(), first.HostID, row)
	}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.RequestID = h.ids.New(protocol.PrefixRequest)
	second.RecoveryAuthorizationID = grant.RecoveryAuthorizationID
	second.Successor = plan.Target
	second.RunnerGeneration++
	second.ExpectedControlRevision = grant.ControlRevision
	if op := h.exec(owner, admissionReleaseEnvelope(h, second), ReleaseBoundedRunnerAdmission{}); op.Status != protocol.OpSucceeded {
		t.Fatal("an old published release blocked an independently committed recovery", op)
	}
}

func boundedReleaseCode(op protocol.Operation, err error) protocol.Code {
	if op.Error != nil {
		return op.Error.Code
	}
	return protocol.CodeOf(err)
}
