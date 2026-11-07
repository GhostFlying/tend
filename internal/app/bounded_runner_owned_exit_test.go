package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func boundedRunnerOwnedExitPlan(t *testing.T) (*harness, protocol.Assignment, protocol.BoundedRunnerMaintenanceGrant, protocol.RunnerBoundedMaintenancePlan) {
	t.Helper()
	h, as, first := boundedRunnerPlanFixture(t)
	old, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, first)
	if err != nil {
		t.Fatal(err)
	}
	p := first
	p.Source = protocol.RecoveryIdentity{}
	p.SourceOperation = &protocol.MaintenanceOperationSource{OperationID: first.Target.UpgradeID, Kind: "upgrade", Phase: "launch_exited", OperationSHA256: strings.Repeat("d", 64), LaunchID: first.Target.ProcessInstanceID, Protocol: 2, StorageVersion: 17, OriginalRequestID: first.Authorization.RequestID, LaunchExit: &protocol.MaintenanceLaunchExitProof{PID: 4321, LaunchID: first.Target.ProcessInstanceID, ArtifactSHA256: first.Target.ArtifactSHA256, ExitCode: 17}}
	p.Journal.Version = 17
	p.Target.UpgradeID, p.Target.ProcessInstanceID = "runner-owned-exit-replacement", "runner-owned-exit-successor"
	p.ExpectedControlRevision = old.ControlRevision
	p.Authorization.RequestID = h.ids.New(protocol.PrefixRequest)
	p.Authorization.Source, p.Authorization.Target = p.Source, p.Target
	p.Authorization.DataIdentity = &p.Journal
	p.Authorization.PlanSHA256, err = protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, as, old, p
}

func TestBoundedRunnerOwnedExitGrantPreservesOriginalProtectionWithoutHello(t *testing.T) {
	h, as, old, p := boundedRunnerOwnedExitPlan(t)
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	grant, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p)
	if err != nil {
		t.Fatal(err)
	}
	if grant.RecoveryAuthorizationID != p.Authorization.RequestID || grant.RecoveryAuthorizationID == old.RecoveryAuthorizationID || !reflect.DeepEqual(grant.Runs, old.Runs) || grant.ProtectionSetSHA256 != old.ProtectionSetSHA256 {
		t.Fatalf("replacement recaptured work or reused the old request: %+v", grant)
	}
	if !reflect.DeepEqual(h.run(as.RunID), beforeRun) || !reflect.DeepEqual(h.lease(as.RunID), beforeLease) {
		t.Fatal("owned root exit changed unresolved business work")
	}
	again, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p)
	if err != nil || !reflect.DeepEqual(again, grant) {
		t.Fatal("original replacement request was not replayed", err)
	}
}

func TestBoundedRunnerOwnedExitGrantRejectsUnboundSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*protocol.RunnerBoundedMaintenancePlan)
	}{
		{"no source operation", func(p *protocol.RunnerBoundedMaintenancePlan) { p.SourceOperation = nil }},
		{"no wait proof", func(p *protocol.RunnerBoundedMaintenancePlan) { p.SourceOperation.LaunchExit = nil }},
		{"unknown original grant", func(p *protocol.RunnerBoundedMaintenancePlan) {
			p.SourceOperation.OriginalRequestID = "req_00000000000000000000000099"
		}},
		{"wrong candidate", func(p *protocol.RunnerBoundedMaintenancePlan) {
			p.SourceOperation.LaunchExit.ArtifactSHA256 = strings.Repeat("f", 64)
		}},
		{"wrong original operation", func(p *protocol.RunnerBoundedMaintenancePlan) { p.SourceOperation.OperationID = "unrelated-upgrade" }},
		{"same launch", func(p *protocol.RunnerBoundedMaintenancePlan) {
			p.Target.ProcessInstanceID = p.SourceOperation.LaunchID
			p.Authorization.Target = p.Target
		}},
		{"changed journal", func(p *protocol.RunnerBoundedMaintenancePlan) {
			p.Journal.Inode++
			p.Authorization.DataIdentity = &p.Journal
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, old, p := boundedRunnerOwnedExitPlan(t)
			tc.edit(&p)
			p.Authorization.PlanSHA256, _ = protocol.RunnerBoundedMaintenancePlanSHA256(p)
			if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p); err == nil {
				t.Fatal("unbound owned-exit replacement was authorized")
			}
			if got := upgradeCtl(h, p.HostID); got.Revision != old.ControlRevision {
				t.Fatal("rejected source changed host control", got)
			}
		})
	}
}
