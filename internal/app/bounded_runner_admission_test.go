package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func boundedRunnerAdmissionFixture(t *testing.T) (*harness, protocol.Assignment, protocol.BoundedRunnerAdmissionRelease) {
	t.Helper()
	h, as, plan := boundedRunnerPlanFixture(t)
	grant, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		r, err := store.GetHostRuntime(t.Context(), tx.Q(), plan.HostID)
		if err != nil {
			return err
		}
		r.Runtime.InstanceID = plan.Target.ProcessInstanceID
		r.Runtime.Generation++
		r.Runtime.Build.BinarySHA256 = plan.Target.ArtifactSHA256
		r.Runtime.BoundedRecoveryAuthorizationID = plan.Authorization.RequestID
		r.Runtime.Capabilities = append(r.Runtime.Capabilities, protocol.CapabilityBoundedRunnerRecovery)
		r.Runtime.JournalVersion = 17
		// Unknown operations, the missing maintenance ACK and offline status
		// remain explicit; they are not prerequisites for owner intervention.
		return tx.PutHostRuntime(t.Context(), plan.HostID, r)
	}); err != nil {
		t.Fatal(err)
	}
	journal := plan.Journal
	journal.Version = 17
	return h, as, protocol.BoundedRunnerAdmissionRelease{RequestID: h.ids.New(protocol.PrefixRequest), HostID: plan.HostID, RecoveryAuthorizationID: plan.Authorization.RequestID, Successor: plan.Target, RunnerGeneration: plan.RunnerGeneration + 1, Journal: journal, ExpectedControlRevision: grant.ControlRevision, Reason: "Allow available capacity while original unknown work remains isolated"}
}

func admissionReleaseEnvelope(h *harness, p protocol.BoundedRunnerAdmissionRelease) *protocol.Envelope {
	env := h.env(protocol.OpReleaseBoundedRunnerAdmission, protocol.Target{Type: "host", ID: p.HostID}, p, rev(p.ExpectedControlRevision))
	env.RequestID = p.RequestID
	return env
}

func TestBoundedRunnerAdmissionReleasePreservesUnknownAndOriginalProtection(t *testing.T) {
	h, as, release := boundedRunnerAdmissionFixture(t)
	beforeRun, beforeLease, beforeTask := h.run(as.RunID), h.lease(as.RunID), h.task(as.TaskID)
	request := admissionReleaseEnvelope(h, release)
	op := h.exec(owner, request, ReleaseBoundedRunnerAdmission{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var control protocol.RunnerControl
	if err := json.Unmarshal(op.Result, &control); err != nil {
		t.Fatal(err)
	}
	if control.Mode != protocol.RunnerNormal || control.Revision != release.ExpectedControlRevision+1 || !reflect.DeepEqual(control.BoundedRecoveryRelease, &release) {
		t.Fatal("exact admission authorization was not delivered", control)
	}
	if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) || !reflect.DeepEqual(beforeTask, h.task(as.TaskID)) {
		t.Fatal("global admission release changed retained business work")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		p, err := activeMaintenanceRunProtection(t.Context(), tx.Q(), beforeRun)
		if err == nil && p == nil {
			t.Fatal("admission release removed the original per-run protection")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if replay := h.exec(owner, request, ReleaseBoundedRunnerAdmission{}); !reflect.DeepEqual(op, replay) {
		t.Fatal("admission release did not replay its original receipt", replay)
	}
}

func TestBoundedRunnerAdmissionReleaseRejectsUnboundOrStaleAuthority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*protocol.BoundedRunnerAdmissionRelease)
		principal protocol.Principal
		code      protocol.Code
	}{
		{name: "runner", principal: runner("linux-1"), code: protocol.CodeForbidden},
		{name: "stale control", mutate: func(p *protocol.BoundedRunnerAdmissionRelease) { p.ExpectedControlRevision++ }, code: protocol.CodeStaleRevision},
		{name: "wrong recovery", mutate: func(p *protocol.BoundedRunnerAdmissionRelease) {
			p.RecoveryAuthorizationID = "req_01KDVDNA0048R1ZE6R55WDNW0Z"
		}, code: protocol.CodeConflictPayload},
		{name: "wrong successor", mutate: func(p *protocol.BoundedRunnerAdmissionRelease) { p.Successor.ProcessInstanceID = "other" }, code: protocol.CodeConflictPayload},
		{name: "wrong generation", mutate: func(p *protocol.BoundedRunnerAdmissionRelease) { p.RunnerGeneration++ }, code: protocol.CodeConflictPayload},
		{name: "wrong journal", mutate: func(p *protocol.BoundedRunnerAdmissionRelease) { p.Journal.Inode++ }, code: protocol.CodeConflictPayload},
		{name: "wrong journal incarnation", mutate: func(p *protocol.BoundedRunnerAdmissionRelease) { p.Journal.Incarnation = "other" }, code: protocol.CodeConflictPayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, release := boundedRunnerAdmissionFixture(t)
			if tc.mutate != nil {
				tc.mutate(&release)
			}
			principal := tc.principal
			if principal.Kind == "" {
				principal = owner
			}
			op, err := h.app.Execute(t.Context(), principal, admissionReleaseEnvelope(h, release), ReleaseBoundedRunnerAdmission{})
			code := protocol.CodeOf(err)
			if op.Error != nil {
				code = op.Error.Code
			}
			if code != tc.code {
				t.Fatal("unbound admission release was accepted", op, err)
			}
			if control := upgradeCtl(h, "linux-1"); control.Revision != 1 || control.Mode != protocol.RunnerMaintenance || control.BoundedRecoveryRelease != nil {
				t.Fatal("rejected release changed global admission", control)
			}
		})
	}
}
