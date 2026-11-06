package app

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func boundedRunnerMonitorFixture(t *testing.T) (*harness, protocol.Assignment, protocol.RunnerBoundedMaintenancePlan, protocol.BoundedRunnerAdmissionRelease, protocol.RunnerRuntime) {
	t.Helper()
	h, as, release := boundedRunnerAdmissionFixture(t)
	grant, err := h.app.RunnerBoundedMaintenance(t.Context(), release.HostID, release.RecoveryAuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "linux-1", LocalID: "implementer"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "orca", Revision: 1}, InventoryRevision: 1, SnapshotID: "snapshot", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model", Effort: "high"}, Worker: protocol.WorkerOrca, BackendID: "orca"}
	var rt protocol.RunnerRuntime
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRunExecution(t.Context(), as.RunID, pin); err != nil {
			return err
		}
		if err := tx.UpdateRunObserved(t.Context(), as.RunID, protocol.RunRunning, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		row, err := store.GetHostRuntime(t.Context(), tx.Q(), release.HostID)
		if err == nil {
			rt = row.Runtime
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rt.Sequence++
	rt.Mode = protocol.RunnerMaintenance
	rt.AppliedControlRevision = grant.ControlRevision
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityRetainedReconciliation, protocol.CapabilityStrictRetainedResume)
	rt.AgentExecutors = []protocol.AgentExecutor{{Pin: pin, Available: true}}
	if op, err := retainedHello(t, h, &rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	// A normal supervisor restart has a new process and higher generation,
	// while its committed receipt retains the original immutable upgrade.
	rt.InstanceID = "monitored-successor"
	rt.Generation++
	rt.Sequence = 1
	rt.Build.PID++
	rt.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: release.Successor, Phase: "committed", CommittedConfirmed: true, CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: rt.Build.BinarySHA256}
	return h, as, grant.Plan, release, rt
}

func TestBoundedRunnerMonitoredRestartRetainsAuthorityAndAllowsExplicitRecovery(t *testing.T) {
	h, as, plan, release, rt := boundedRunnerMonitorFixture(t)
	if op, err := retainedHello(t, h, &rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	health, err := h.app.RunnerUpgradeHealth(t.Context(), runner(release.HostID), release.Successor)
	if err != nil || !health.ServerConfirmed || !health.Healthy || !health.AdmissionFenced || health.NewWorkAccepted {
		t.Fatal("committed monitored successor lost bounded recovery health", health, err)
	}
	s, err := h.app.RetainedRunReconciliation(t.Context(), as.RunID)
	if err != nil || !s.CanContinue || s.RecoveryAuthorizationID != release.RecoveryAuthorizationID {
		t.Fatal("monitored successor lost its original strict recovery scope", s, err)
	}
	payload := protocol.ContinueRetainedRunPayload{RetainedRunIdentity: s.Identity, Protections: s.Protections, ProtectionSetSHA256: s.ProtectionSetSHA256, RecoveryAuthorizationID: s.RecoveryAuthorizationID, Reason: "Independently retain the original execution on the observed supervised process"}
	if op := h.exec(owner, h.env(protocol.OpContinueRetainedRun, runTarget(as.RunID), payload, rev(s.TaskRevision)), ContinueRetainedRun{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A fresh budget request reuses the original source plan and quarantine,
	// even though the first installed process is no longer the live process.
	plan.ExpectedControlRevision = upgradeCtl(h, release.HostID).Revision
	plan.Authorization.RequestID = h.ids.New(protocol.PrefixRequest)
	plan.Authorization.ExpectedGeneration++
	plan.Authorization.ExpectedPhase = "committed"
	grant, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan)
	if err != nil || grant.RecoveryAuthorizationID != release.RecoveryAuthorizationID {
		t.Fatal("monitored successor could not obtain a fresh bounded window", grant, err)
	}
	release.RequestID = h.ids.New(protocol.PrefixRequest)
	release.ExpectedControlRevision = grant.ControlRevision
	release.RunnerGeneration = rt.Generation
	if op := h.exec(owner, admissionReleaseEnvelope(h, release), ReleaseBoundedRunnerAdmission{}); op.Status != protocol.OpSucceeded {
		t.Fatal("explicit admission release lost the immutable target binding", op)
	}
	if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
		t.Fatal("process recovery or global release settled retained business work")
	}
}

func TestBoundedRunnerMonitoredRestartRejectsUnprovenReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.RunnerRuntime)
	}{
		{name: "missing", mutate: func(rt *protocol.RunnerRuntime) { rt.UpdaterReceipt = nil }},
		{name: "uncommitted", mutate: func(rt *protocol.RunnerRuntime) { rt.UpdaterReceipt.CommittedConfirmed = false }},
		{name: "rollback", mutate: func(rt *protocol.RunnerRuntime) { rt.UpdaterReceipt.RollbackConfirmed = true }},
		{name: "phase", mutate: func(rt *protocol.RunnerRuntime) { rt.UpdaterReceipt.Phase = "starting" }},
		{name: "binding", mutate: func(rt *protocol.RunnerRuntime) { rt.UpdaterReceipt.Binding.ProcessInstanceID = "another-target" }},
		{name: "current process", mutate: func(rt *protocol.RunnerRuntime) { rt.UpdaterReceipt.CurrentProcessInstanceID = "another-process" }},
		{name: "current bytes", mutate: func(rt *protocol.RunnerRuntime) { rt.UpdaterReceipt.CurrentSHA256 = strings.Repeat("d", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, as, plan, release, rt := boundedRunnerMonitorFixture(t)
			tc.mutate(&rt)
			if op, err := retainedHello(t, h, &rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}); err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal("an observation may retain uncertain work without granting recovery", op, err)
			}
			health, err := h.app.RunnerUpgradeHealth(t.Context(), runner(release.HostID), release.Successor)
			if err != nil || health.ServerConfirmed || health.Healthy {
				t.Fatal("unproven replacement was declared healthy", health, err)
			}
			s, err := h.app.RetainedRunReconciliation(t.Context(), as.RunID)
			if err != nil || s.CanContinue || !slices.Contains(s.Blockers, "bounded_recovery_runtime_mismatch") {
				t.Fatal("unproven replacement could continue an original run", s, err)
			}
			plan.Authorization.RequestID = h.ids.New(protocol.PrefixRequest)
			plan.ExpectedControlRevision = upgradeCtl(h, release.HostID).Revision
			if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan); protocol.CodeOf(err) != protocol.CodeConflictPayload {
				t.Fatal("unproven replacement minted a fresh force authority", err)
			}
			release.RunnerGeneration = rt.Generation
			op, err := h.app.Execute(t.Context(), owner, admissionReleaseEnvelope(h, release), ReleaseBoundedRunnerAdmission{})
			code := protocol.CodeOf(err)
			if op.Error != nil {
				code = op.Error.Code
			}
			if code != protocol.CodeConflictPayload {
				t.Fatal("unproven replacement released global admission", op, err)
			}
			if control := upgradeCtl(h, release.HostID); control.Mode != protocol.RunnerMaintenance || control.Revision != release.ExpectedControlRevision {
				t.Fatal("unproven replacement changed host authority", control)
			}
		})
	}
}

func TestBoundedRunnerMonitoredRestartStillRejectsStaleRuntimeGeneration(t *testing.T) {
	h, as, _, _, rt := boundedRunnerMonitorFixture(t)
	rt.Generation--
	op, err := retainedHello(t, h, &rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	code := protocol.CodeOf(err)
	if op.Error != nil {
		code = op.Error.Code
	}
	if code != protocol.CodeConflictPayload {
		t.Fatal("committed receipt bypassed the ordinary runtime generation fence", op, err)
	}
}
