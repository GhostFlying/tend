package app

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A persisted external authority must fence the original lease even when its
// old heartbeat and a normal host control would otherwise permit continuation.
func TestBoundedRunnerGrantPreventsOrdinaryHelloContinuation(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "Preserve uncertain native work", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("run not claimed")
	}
	authority := h.ids.New(protocol.PrefixRequest)
	protection := protocol.RetainedRunProtection{MaintenanceID: "runner-force:" + authority, RunID: as.RunID, TaskID: as.TaskID, ActionID: h.run(as.RunID).ActionID, HostID: "linux-1", LeaseID: as.LeaseID, OriginalRequestsJSON: "[]"}
	grant := protocol.BoundedRunnerMaintenanceGrant{Plan: protocol.RunnerBoundedMaintenancePlan{HostID: "linux-1", Authorization: protocol.BoundedMaintenanceAuthorization{RequestID: authority, Role: "runner"}}}
	body, err := json.Marshal(map[string]any{"grant": grant, "protections": []protocol.RetainedRunProtection{protection}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.UpdateRunObserved(t.Context(), as.RunID, protocol.RunUnknown, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		_, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: h.ids.New(protocol.PrefixEvent), Source: "app", ObjectType: "host", ObjectID: "linux-1", Kind: "host.bounded_maintenance_authorized", BodyJSON: string(body), RequestID: authority, CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rt := testRuntime("bounded-successor")
	rt.Generation = 2
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityRetainedReconciliation, protocol.CapabilityBoundedRunnerRecovery)
	rt.BoundedRecoveryAuthorizationID = authority
	rt.JournalVersion = 17
	op, err := h.app.Execute(t.Context(), runner("linux-1"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: rt, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted}}}, nil), Hello{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	var hello protocol.HelloResult
	if err := json.Unmarshal(op.Result, &hello); err != nil {
		t.Fatal(err)
	}
	if len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictRetain || len(hello.Reassigned) != 0 {
		t.Fatalf("ordinary Hello must retain the exact force-protected lease: %+v", hello)
	}
	if h.run(as.RunID).ObservedState != protocol.RunUnknown {
		t.Fatal("an ordinary observation cleared original business uncertainty")
	}
	if got := h.lease(as.RunID); got.LeaseID != as.LeaseID || got.Status == store.LeaseReleased {
		t.Fatal("original lease was released or replaced", got)
	}
}

func boundedRunnerPlanFixture(t *testing.T) (*harness, protocol.Assignment, protocol.RunnerBoundedMaintenancePlan) {
	t.Helper()
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "Retain a running worker through replacement", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("run not claimed")
	}
	source := protocol.RecoveryIdentity{PID: 123, StartTicks: 456, LaunchID: "old-launch", SHA256: strings.Repeat("a", 64)}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		r, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		r.Runtime.Build.PID = int64(source.PID)
		r.Runtime.Build.BinarySHA256 = source.SHA256
		r.Runtime.PendingClaims = 1
		r.Runtime.PendingOperations = 3
		r.Runtime.Blockers = []string{"native_outcome_unknown"}
		r.Online = false
		return tx.PutHostRuntime(t.Context(), "linux-1", r)
	}); err != nil {
		t.Fatal(err)
	}
	p := protocol.RunnerBoundedMaintenancePlan{HostID: "linux-1", NodeSelector: "tag:tend-runner", RunnerInstanceID: "fixture-linux-1", RunnerGeneration: 1, Source: source, Journal: protocol.BoundedDataIdentity{Device: 42, Inode: 1234, Incarnation: "original-journal", Version: 16}, Target: protocol.UpgradeBinding{UpgradeID: "runner-force-one", InstanceID: "linux-1", ProcessInstanceID: "runner-successor", ReleaseID: "candidate", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64)}}
	p.Authorization = protocol.BoundedMaintenanceAuthorization{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedGeneration: 7, ExpectedPhase: "maintenance", Role: "runner", Source: source, Target: p.Target, Reason: "Replace the exact writer and retain every unresolved native operation", ExpiresAtMillis: h.clock.Now().Add(6 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(), DataIdentity: &p.Journal}
	p.ReleaseDir = filepath.Join(t.TempDir(), "bundle")
	var err error
	p.Authorization.PlanSHA256, err = protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, as, p
}

func TestBoundedRunnerOwnerAuthorityAcceptsUnknownAndPreservesOriginalWork(t *testing.T) {
	h, as, p := boundedRunnerPlanFixture(t)
	beforeRun, beforeLease, beforeTask := h.run(as.RunID), h.lease(as.RunID), h.task(as.TaskID)
	grant, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ControlRevision != 1 || len(grant.Runs) != 1 || grant.Runs[0].Run.RunID != as.RunID || !reflect.DeepEqual(grant.Plan, p) {
		t.Fatal("incomplete independent grant", grant)
	}
	if !reflect.DeepEqual(h.run(as.RunID), beforeRun) || !reflect.DeepEqual(h.lease(as.RunID), beforeLease) || !reflect.DeepEqual(h.task(as.TaskID), beforeTask) {
		t.Fatal("replacement authority changed original business state")
	}
	control := upgradeCtl(h, "linux-1")
	if control.Mode != protocol.RunnerMaintenance || control.Revision != grant.ControlRevision || control.Authorization != nil {
		t.Fatal("force authority leaked into the ordinary grant", control)
	}
	// Lost owner replies use the exact recorded request after both its CAS and
	// expiry have moved; they must not renew or widen its authority.
	h.clock.Advance(10 * time.Minute)
	again, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p)
	if err != nil || !reflect.DeepEqual(grant, again) {
		t.Fatal("original authority receipt was not replayed", again, err)
	}
	read, err := h.app.RunnerBoundedMaintenance(t.Context(), p.HostID, p.Authorization.RequestID)
	if err != nil || !reflect.DeepEqual(read, grant) {
		t.Fatal("immutable authority could not be reconciled", read, err)
	}
	// A runner's release after replacement remains a physical/unclassified
	// fact, without releasing its lease or declaring the native worker dead.
	op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), h.env(protocol.OpRunsRelease, runTarget(as.RunID), protocol.ReleasePayload{Reason: "old transport disappeared"}, nil), Release{})
	if err != nil || op.Status != protocol.OpAccepted {
		t.Fatal("old release escaped retained reconciliation", op, err)
	}
	if !reflect.DeepEqual(h.run(as.RunID), beforeRun) || !reflect.DeepEqual(h.lease(as.RunID), beforeLease) {
		t.Fatal("old release ended or replaced the protected execution")
	}
}

func TestBoundedRunnerAuthorityRejectsPrivilegeAndBindingChanges(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*protocol.RunnerBoundedMaintenancePlan)
		principal protocol.Principal
		code      protocol.Code
	}{
		{name: "runner cannot self grant", principal: runner("linux-1"), code: protocol.CodeForbidden},
		{name: "main agent cannot self grant", principal: mainAgent, code: protocol.CodeForbidden},
		{name: "stale host control", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.ExpectedControlRevision++ }, code: protocol.CodeStaleRevision},
		{name: "wrong node selector", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.NodeSelector = "other-node" }, code: protocol.CodeConflictPayload},
		{name: "wrong instance", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.RunnerInstanceID = "other-instance" }, code: protocol.CodeConflictPayload},
		{name: "wrong generation", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.RunnerGeneration++ }, code: protocol.CodeConflictPayload},
		{name: "wrong PID", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.Source.PID++; p.Authorization.Source = p.Source }, code: protocol.CodeConflictPayload},
		{name: "wrong journal", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.Journal.Inode++ }, code: protocol.CodeInvalidRequest},
		{name: "target mismatch", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) {
			p.Authorization.Target.ProcessInstanceID = "other-target"
		}, code: protocol.CodeInvalidRequest},
		{name: "expired", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.Authorization.ExpiresAtMillis = 1 }, code: protocol.CodeInvalidRequest},
		{name: "unbounded phase", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.Authorization.Timeouts.DrainMillis = 0 }, code: protocol.CodeInvalidRequest},
		{name: "phase total exceeds overall", mutate: func(p *protocol.RunnerBoundedMaintenancePlan) { p.Authorization.Timeouts.OverallMillis = 100000 }, code: protocol.CodeInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, p := boundedRunnerPlanFixture(t)
			if tc.mutate != nil {
				// Isolate the journal from its authorization's pointer so a
				// conflicting caller cannot mutate both copies accidentally.
				journal := p.Journal
				p.Authorization.DataIdentity = &journal
				tc.mutate(&p)
				p.Authorization.PlanSHA256, _ = protocol.RunnerBoundedMaintenancePlanSHA256(p)
			}
			principal := tc.principal
			if principal.Kind == "" {
				principal = owner
			}
			if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), principal, p); protocol.CodeOf(err) != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
			if control := upgradeCtl(h, "linux-1"); control.Revision != 0 || control.Mode != protocol.RunnerNormal {
				t.Fatal("rejected authority partially fenced host", control)
			}
		})
	}
}

func TestBoundedRunnerContinuationRequiresExactIndependentAuthority(t *testing.T) {
	h, as, plan := boundedRunnerPlanFixture(t)
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "linux-1", LocalID: "implementer"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "orca", Revision: 1}, InventoryRevision: 1, SnapshotID: "snapshot", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model", Effort: "high"}, Worker: protocol.WorkerOrca, BackendID: "orca"}
	var runtime protocol.RunnerRuntime
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRunExecution(t.Context(), as.RunID, pin); err != nil {
			return err
		}
		if err := tx.UpdateRunObserved(t.Context(), as.RunID, protocol.RunRunning, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		rt, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		runtime = rt.Runtime
		runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityRetainedReconciliation, protocol.CapabilityStrictRetainedResume, protocol.CapabilityBoundedRunnerRecovery)
		runtime.AgentExecutors = []protocol.AgentExecutor{{Pin: pin, Available: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	grant, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan)
	if err != nil {
		t.Fatal(err)
	}
	runtime.InstanceID = plan.Target.ProcessInstanceID
	runtime.Generation++
	runtime.Sequence = 1
	runtime.Build.BinarySHA256 = plan.Target.ArtifactSHA256
	runtime.Build.PID = 124
	runtime.JournalVersion = 17
	runtime.Mode = protocol.RunnerMaintenance
	runtime.AppliedControlRevision = grant.ControlRevision
	runtime.BoundedRecoveryAuthorizationID = plan.Authorization.RequestID
	op, err := retainedHello(t, h, &runtime, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	s, err := h.app.RetainedRunReconciliation(t.Context(), as.RunID)
	if err != nil || !s.CanContinue || s.RecoveryAuthorizationID != plan.Authorization.RequestID {
		t.Fatal("exact successor must expose a bounded original-run resolution", s, err)
	}
	payload := protocol.ContinueRetainedRunPayload{RetainedRunIdentity: s.Identity, Protections: s.Protections, ProtectionSetSHA256: s.ProtectionSetSHA256, Reason: "Independently verify and reattach only this original native identity"}
	for _, authority := range []string{"", h.ids.New(protocol.PrefixRequest)} {
		payload.RecoveryAuthorizationID = authority
		denied, err := h.app.Execute(t.Context(), owner, h.env(protocol.OpContinueRetainedRun, runTarget(as.RunID), payload, rev(s.TaskRevision)), ContinueRetainedRun{})
		if err != nil || denied.Error == nil || denied.Error.Code != protocol.CodeStaleRevision {
			t.Fatal("ordinary or foreign continuation cleared forced isolation", denied, err)
		}
	}
	payload.RecoveryAuthorizationID = s.RecoveryAuthorizationID
	request := h.env(protocol.OpContinueRetainedRun, runTarget(as.RunID), payload, rev(s.TaskRevision))
	if op := h.exec(owner, request, ContinueRetainedRun{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runtime.Sequence++
	op, err = retainedHello(t, h, &runtime, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	if err != nil {
		t.Fatal(err)
	}
	var hello protocol.HelloResult
	if err := json.Unmarshal(op.Result, &hello); err != nil {
		t.Fatal(err)
	}
	if len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictContinueRetained || hello.Runs[0].RetainedResume == nil || hello.Runs[0].RetainedResume.RequestID != request.RequestID || hello.Runs[0].RetainedResume.RecoveryAuthorizationID != plan.Authorization.RequestID {
		t.Fatal("original journal did not receive the exact independent authority", hello)
	}
	if got := upgradeCtl(h, "linux-1"); got.Mode != protocol.RunnerMaintenance {
		t.Fatal("a single-run resolution reopened global admission", got)
	}
}

func TestBoundedRunnerAuthorityRollsBackFenceWhenAuditCannotCommit(t *testing.T) {
	h, as, p := boundedRunnerPlanFixture(t)
	beforeRun, beforeLease := h.run(as.RunID), h.lease(as.RunID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_bounded_authority BEFORE INSERT ON events WHEN NEW.kind='host.bounded_maintenance_authorized' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p); err == nil {
		t.Fatal("force authorization succeeded without its durable audit")
	}
	if got := upgradeCtl(h, "linux-1"); got.Revision != 0 || got.Mode != protocol.RunnerNormal {
		t.Fatal("uncommitted force left a partial host fence", got)
	}
	if !reflect.DeepEqual(beforeRun, h.run(as.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(as.RunID)) {
		t.Fatal("uncommitted authorization changed retained work")
	}
	if _, err := h.app.RunnerBoundedMaintenance(t.Context(), p.HostID, p.Authorization.RequestID); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatal("uncommitted authority remained readable", err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_bounded_authority`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, p); err != nil {
		t.Fatal("original request could not reconcile after transaction rollback", err)
	}
}
