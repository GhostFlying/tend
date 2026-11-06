//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func boundedRunnerFixture(t *testing.T) (*harness, protocol.BoundedRunnerMaintenanceGrant) {
	t.Helper()
	h := newHarness(t)
	raw, err := os.ReadFile(filepath.Join(h.bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest release.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Storage.RunnerJournal.Max = 17
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.bundle, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	state := h.s.Status()
	bundle, err := release.Load(h.bundle)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := bundle.Artifact("runner", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	device, inode, err := BoundedDataFileIdentity(h.data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.data+".incarnation", []byte("retained generation metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	incDevice, incInode, err := BoundedDataFileIdentity(h.data + ".incarnation")
	if err != nil {
		t.Fatal(err)
	}
	data := protocol.BoundedDataIdentity{Path: h.data, Device: device, Inode: inode, Incarnation: fmt.Sprintf("%d:%d", incDevice, incInode), Version: 8}
	target := protocol.UpgradeBinding{UpgradeID: "forced-runner-upgrade", InstanceID: h.cfg.InstanceID, ProcessInstanceID: "forced-successor", ReleaseID: bundle.Manifest.ReleaseID, ManifestSHA256: bundle.ManifestSHA256, ArtifactSHA256: artifact.SHA256}
	p := protocol.RunnerBoundedMaintenancePlan{HostID: h.cfg.InstanceID, NodeSelector: "configured-node", RunnerInstanceID: "original-runtime", RunnerGeneration: 1, Source: state.Process, Journal: data, ReleaseDir: h.bundle, Target: target, ExpectedControlRevision: 2}
	sha, err := protocol.RunnerBoundedMaintenancePlanSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Authorization = protocol.BoundedMaintenanceAuthorization{RequestID: "req_00000000000000000000000001", ExpectedGeneration: state.Generation, ExpectedPhase: "idle", PlanSHA256: sha, Role: "runner", Source: p.Source, DataIdentity: &data, Target: target, Reason: "bounded replacement retains unknown work", ExpiresAtMillis: h.now.Add(time.Hour).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
	h.opts.Authority = boundedRunnerAuthority{fakeAuthority: h.a, requestID: p.Authorization.RequestID}
	h.s.opts.Authority = h.opts.Authority
	return h, protocol.BoundedRunnerMaintenanceGrant{RecoveryAuthorizationID: p.Authorization.RequestID, Plan: p, ControlRevision: 3, Runs: []protocol.CommittedRetainedRun{}, ProtectionSetSHA256: strings.Repeat("a", 64)}
}

func boundedRunnerTestStep(t *testing.T, h *harness) error {
	t.Helper()
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	handled, err := h.s.boundedRunnerStep(t.Context())
	if !handled {
		t.Fatal("bounded runner did not own its lifecycle")
	}
	return err
}

func TestBoundedRunnerForceInstallsWithoutOrdinaryDrainGrantOrBusinessReady(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	h.a.deny = true
	before, err := os.ReadFile(h.data)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8 && h.s.Status().Operation.Phase != "committed"; i++ {
		if err := boundedRunnerTestStep(t, h); err != nil {
			t.Fatal(err)
		}
	}
	state := h.s.Status()
	if state.Operation.Phase != "committed" || state.Bounded.Outcome != "installed_recovery_pending" || state.Bounded.SourceExit == nil || h.p.starts != 1 || h.p.stops != 1 || state.CommittedProof == nil {
		t.Fatalf("force did not commit the installed process independently from business recovery: %+v starts=%d stops=%d", state, h.p.starts, h.p.stops)
	}
	after, err := os.ReadFile(h.data)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("forced replacement changed predecessor journal: %q %v", after, err)
	}
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatalf("same request did not replay: %v", err)
	}
	if h.p.starts != 1 {
		t.Fatal("same request started a second process")
	}
}

func TestBoundedRunnerLostStartOnlyFindsOriginalLaunch(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	if err := boundedRunnerTestStep(t, h); err != nil {
		t.Fatal(err)
	}
	if err := boundedRunnerTestStep(t, h); err != nil {
		t.Fatal(err)
	}
	h.p.ambiguousStart = true
	if err := boundedRunnerTestStep(t, h); !errors.Is(err, ErrUnknown) {
		t.Fatalf("lost start was not unknown: %v", err)
	}
	if h.s.Status().Operation.Phase != "starting" || h.p.starts != 1 {
		t.Fatal("lost launch was not durably retained")
	}
	if err := boundedRunnerTestStep(t, h); err != nil {
		t.Fatal(err)
	}
	if h.p.starts != 1 || h.s.Status().Operation.Phase != "checking" {
		t.Fatal("lost start launched a second writer")
	}
}

func TestBoundedRunnerAuthorityCoversEveryAcceptedWindow(t *testing.T) {
	for _, action := range []string{"force", "apply", "takeover", "extend"} {
		t.Run(action, func(t *testing.T) {
			h, grant := boundedRunnerFixture(t)
			if action == "extend" {
				if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
					t.Fatal(err)
				}
				grant.Plan.Authorization.RequestID = "req_00000000000000000000000002"
				grant.Plan.Authorization.ExpectedGeneration = h.s.Status().Generation
				grant.Plan.Authorization.ExpectedPhase = h.s.Status().Bounded.Phase
			}
			before := h.s.Status()
			grant.Plan.Authorization.ExpiresAtMillis = h.now.Add(time.Millisecond).UnixMilli()
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, action); err == nil {
				t.Fatal("authority shorter than the full fixed window was accepted")
			}
			if !reflect.DeepEqual(before, h.s.Status()) || h.p.stops != 0 || h.p.starts != 0 {
				t.Fatal("rejected window changed durable recovery or invoked a process action")
			}
		})
	}
}

func TestBoundedRunnerPreviouslySavedShortAuthorityCannotStopAfterExpiry(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	// A previous binary could persist a full window beyond its owner's expiry.
	h.s.state.Bounded.Authorization.ExpiresAtMillis = h.now.Add(time.Millisecond).UnixMilli()
	h.now = h.now.Add(2 * time.Millisecond)
	h.boot += 2
	if err := boundedRunnerTestStep(t, h); err == nil {
		t.Fatal("expired persisted authority allowed another destructive transition")
	}
	if h.p.stops != 0 || h.p.starts != 0 || h.s.Status().Bounded.SourceExit != nil {
		t.Fatal("expired authority stopped a runner or invented an exit")
	}
}

type boundedRunnerExpiringInspect struct {
	*fakeProcess
	expire func()
}

func (p *boundedRunnerExpiringInspect) Inspect(ctx context.Context, pid int) (Identity, error) {
	p.expire()
	return p.fakeProcess.Inspect(ctx, pid)
}

func TestBoundedRunnerRechecksAuthorityAfterInspectBeforeStop(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	h.s.opts.Process = &boundedRunnerExpiringInspect{fakeProcess: h.p, expire: func() {
		h.now = time.UnixMilli(grant.Plan.Authorization.ExpiresAtMillis)
	}}
	if err := boundedRunnerTestStep(t, h); err == nil {
		t.Fatal("inspection crossing the authority expiry still allowed a stop")
	}
	if h.p.stops != 0 || h.p.starts != 0 || h.s.Status().Bounded.SourceExit != nil {
		t.Fatal("expired authority caused a signal or invented predecessor exit")
	}
}

type boundedRunnerUncertainStop struct {
	*fakeProcess
	stopCalls, killCalls int
}

func (p *boundedRunnerUncertainStop) Stop(context.Context, Identity, time.Duration) error {
	p.stopCalls++
	return ErrUnknown
}

func (p *boundedRunnerUncertainStop) Crash(_ context.Context, id Identity, _ time.Duration) error {
	p.killCalls++
	delete(p.live, id.PID)
	return nil
}

func TestBoundedRunnerLostStopReconcilesAcrossRestartBeforeAuthorizedKill(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	p := &boundedRunnerUncertainStop{fakeProcess: h.p}
	h.s.opts.Process, h.opts.Process = p, p
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := boundedRunnerTestStep(t, h); !errors.Is(err, ErrUnknown) {
			t.Fatalf("unconfirmed original stop lost its uncertainty: %v", err)
		}
	}
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := boundedRunnerTestStep(t, h); !errors.Is(err, ErrUnknown) {
		t.Fatalf("reopened original stop lost its uncertainty: %v", err)
	}
	if p.stopCalls != 1 || p.killCalls != 0 || h.p.starts != 0 || h.s.Status().Bounded.SourceExit != nil {
		t.Fatalf("same stop was repeated or its exit invented: stop=%d kill=%d", p.stopCalls, p.killCalls)
	}
	h.now = h.now.Add(time.Duration(grant.Plan.Authorization.Timeouts.StopMillis+1) * time.Millisecond)
	h.boot += grant.Plan.Authorization.Timeouts.StopMillis + 1
	if err := boundedRunnerTestStep(t, h); err != nil {
		t.Fatal(err)
	}
	if p.stopCalls != 1 || p.killCalls != 1 || h.s.Status().Bounded.SourceExit == nil || h.p.starts != 0 {
		t.Fatal("persisted force authority could not advance the original unknown stop to exact termination")
	}
}

func TestBoundedRunnerWrongSourceAndJournalCannotStopOrStart(t *testing.T) {
	for _, scenario := range []string{"source", "journal", "generation", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			h, grant := boundedRunnerFixture(t)
			switch scenario {
			case "source":
				grant.Plan.Source.StartTicks++
				grant.Plan.Authorization.Source = grant.Plan.Source
			case "journal":
				grant.Plan.Journal.Inode++
				grant.Plan.Authorization.DataIdentity = &grant.Plan.Journal
			case "generation":
				grant.Plan.Authorization.ExpectedGeneration++
			case "expired":
				grant.Plan.Authorization.ExpiresAtMillis = h.now.UnixMilli()
			}
			sha, err := protocol.RunnerBoundedMaintenancePlanSHA256(grant.Plan)
			if err != nil {
				t.Fatal(err)
			}
			grant.Plan.Authorization.PlanSHA256 = sha
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err == nil {
				t.Fatal("wrong bounded authority was accepted")
			}
			if h.p.starts != 0 || h.p.stops != 0 {
				t.Fatal("invalid authority changed the original process")
			}
		})
	}
}

func TestBoundedRunnerDrainTimeoutNeverRenewsOrSelfAuthorizes(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	h.a.deny = true
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "apply"); err != nil {
		t.Fatal(err)
	}
	first := h.s.Status().Bounded.PhaseDeadlineMono
	if err := boundedRunnerTestStep(t, h); err == nil {
		t.Fatal("ordinary drain unexpectedly passed")
	}
	h.boot += grant.Plan.Authorization.Timeouts.DrainMillis + 1
	h.now = h.now.Add(time.Duration(grant.Plan.Authorization.Timeouts.DrainMillis+1) * time.Millisecond)
	if err := boundedRunnerTestStep(t, h); err == nil {
		t.Fatal("expired drain did not terminate this wait")
	}
	if h.s.Status().Bounded.PhaseDeadlineMono != first || h.p.starts != 0 || h.p.stops != 0 {
		t.Fatal("poll renewed the drain or manufactured force authority")
	}
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	h.s = reopened
	if err := boundedRunnerTestStep(t, h); err == nil || h.s.Status().Bounded.PhaseDeadlineMono != first {
		t.Fatal("process reopen renewed the expired drain")
	}
}

type boundedRunnerAuthority struct {
	*fakeAuthority
	requestID string
}

func (a boundedRunnerAuthority) Health(ctx context.Context, b Binding) (Health, error) {
	h, err := a.fakeAuthority.Health(ctx, b)
	h.BoundedRecoveryAuthorizationID = a.requestID
	h.JournalVersion = 17
	return h, err
}

func TestBoundedRunnerRejectsAnotherWriterFromTheOriginalLaunch(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	other := grant.Plan.Source
	other.PID = 55
	other.StartTicks = 55
	h.p.live[other.PID] = other
	if err := boundedRunnerTestStep(t, h); err == nil {
		t.Fatal("another predecessor launch writer was ignored")
	}
	if h.s.Status().Bounded.SourceExit != nil || h.p.starts != 0 {
		t.Fatal("second writer was allowed before complete predecessor launch exit")
	}
	delete(h.p.live, other.PID)
	if err := boundedRunnerTestStep(t, h); err != nil {
		t.Fatal(err)
	}
	if h.s.Status().Bounded.SourceExit == nil || h.p.starts != 0 {
		t.Fatal("original exact exit did not converge independently from start")
	}
}

type boundedRunnerLostKill struct {
	*fakeProcess
	kills int
}

func (p *boundedRunnerLostKill) Stop(context.Context, Identity, time.Duration) error {
	return ErrUnknown
}
func (p *boundedRunnerLostKill) Crash(_ context.Context, id Identity, _ time.Duration) error {
	p.kills++
	delete(p.live, id.PID)
	return ErrUnknown
}

type boundedRunnerBudgetKill struct {
	*fakeProcess
	timeout time.Duration
}

func (p *boundedRunnerBudgetKill) Stop(context.Context, Identity, time.Duration) error {
	return ErrUnknown
}
func (p *boundedRunnerBudgetKill) Crash(_ context.Context, _ Identity, timeout time.Duration) error {
	p.timeout = timeout
	return ErrUnknown
}

func TestBoundedRunnerKillUsesOnlyTheRemainingOverallBudget(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	process := &boundedRunnerBudgetKill{fakeProcess: h.p}
	h.s.opts.Process = process
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	h.boot += grant.Plan.Authorization.Timeouts.OverallMillis - 1000
	h.now = h.now.Add(time.Duration(grant.Plan.Authorization.Timeouts.OverallMillis-1000) * time.Millisecond)
	if err := boundedRunnerTestStep(t, h); !errors.Is(err, ErrUnknown) {
		t.Fatalf("last-budget kill uncertainty was lost: %v", err)
	}
	if process.timeout <= 0 || process.timeout > time.Second || h.p.starts != 0 {
		t.Fatalf("kill escaped its remaining overall window: timeout=%v starts=%d", process.timeout, h.p.starts)
	}
}

func TestBoundedRunnerLostKillReplyReconcilesExitWithoutSecondKill(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	process := &boundedRunnerLostKill{fakeProcess: h.p}
	h.s.opts.Process = process
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	if err := boundedRunnerTestStep(t, h); !errors.Is(err, ErrUnknown) {
		t.Fatalf("graceful stop uncertainty was lost: %v", err)
	}
	h.boot += grant.Plan.Authorization.Timeouts.StopMillis + 1
	h.now = h.now.Add(time.Duration(grant.Plan.Authorization.Timeouts.StopMillis+1) * time.Millisecond)
	if err := boundedRunnerTestStep(t, h); !errors.Is(err, ErrUnknown) {
		t.Fatalf("lost kill was not unknown: %v", err)
	}
	if process.kills != 1 || h.s.Status().Bounded.SourceExit != nil || h.p.starts != 0 {
		t.Fatal("unknown kill created false exit or another writer")
	}
	if err := boundedRunnerTestStep(t, h); err != nil {
		t.Fatal(err)
	}
	if process.kills != 1 || h.s.Status().Bounded.SourceExit == nil || h.p.starts != 0 {
		t.Fatal("lost kill was repeated instead of reconciling original exit")
	}
}

func TestBoundedRunnerExtensionKeepsOriginalLaunchAndRecoveryAuthority(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := boundedRunnerTestStep(t, h); err != nil {
			t.Fatal(err)
		}
	}
	before := h.s.Status()
	h.boot += grant.Plan.Authorization.Timeouts.StartHealthMillis + 1
	h.now = h.now.Add(time.Duration(grant.Plan.Authorization.Timeouts.StartHealthMillis+1) * time.Millisecond)
	if err := boundedRunnerTestStep(t, h); err == nil {
		t.Fatal("expired candidate health did not terminate its wait")
	}
	fresh := grant
	fresh.Plan.Authorization.RequestID = "req_00000000000000000000000002"
	fresh.Plan.Authorization.ExpectedGeneration = h.s.Status().Generation
	fresh.Plan.Authorization.ExpectedPhase = h.s.Status().Bounded.Phase
	fresh.Plan.Authorization.ExpiresAtMillis = h.now.Add(time.Hour).UnixMilli()
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), fresh, "extend"); err != nil {
		t.Fatal(err)
	}
	state := h.s.Status()
	if boundedRunnerRecoveryID(state.Bounded) != grant.RecoveryAuthorizationID || state.Operation.CandidateProcess != before.Operation.CandidateProcess || state.Operation.Phase != "checking" || h.p.starts != 1 {
		t.Fatal("new resource window changed original candidate or recovery authority")
	}
	if len(state.Bounded.Audit) == 0 || state.Bounded.Audit[len(state.Bounded.Audit)-1].PreviousWindow == nil {
		t.Fatal("extension erased the previous expired window")
	}
	for i := 0; i < 2; i++ {
		if err := boundedRunnerTestStep(t, h); err != nil {
			t.Fatal(err)
		}
	}
	if h.s.Status().Operation.Phase != "committed" || h.p.starts != 1 {
		t.Fatal("original successor could not commit after independent extension")
	}
}

func TestBoundedRunnerForceArchivesOriginalUncertainOperation(t *testing.T) {
	h, grant := boundedRunnerFixture(t)
	original := &Operation{ID: "original-uncertain-upgrade", Kind: "upgrade", Phase: "starting", LaunchID: grant.Plan.Source.LaunchID, Candidate: h.s.Status().Current, PreviousProcess: grant.Plan.Source, LastError: "start reply was lost", BackupDir: "retained-backup", Backups: []BackupFile{{Name: "original-backup", Existed: true}}}
	h.s.state.Operation = original
	grant.Plan.Authorization.ExpectedPhase = original.Phase
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	archived, ok := h.s.Status().OperationHistory[original.ID]
	if !ok || !reflect.DeepEqual(archived, *original) {
		t.Fatalf("forced forward replacement lost original inflight facts: %+v", archived)
	}
}

func TestBoundedRunnerReconcilesOriginalBackupWithoutOverwritingIt(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete_%t", complete), func(t *testing.T) {
			h, grant := boundedRunnerFixture(t)
			if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
				t.Fatal(err)
			}
			if err := boundedRunnerTestStep(t, h); err != nil {
				t.Fatal(err)
			}
			op := h.s.state.Operation
			op.BackupDir = filepath.Join(h.cfg.StateDir, "backups", op.ID+"-"+grant.Plan.Authorization.RequestID)
			if err := os.MkdirAll(op.BackupDir, 0700); err != nil {
				t.Fatal(err)
			}
			if complete {
				if err := backupBoundedRunnerData(h.cfg, op); err != nil {
					t.Fatal(err)
				}
			} else {
				body, err := os.ReadFile(h.data)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(op.BackupDir, "data-000"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Stat(filepath.Join(op.BackupDir, "data-000"))
			if err != nil {
				t.Fatal(err)
			}
			op.Backups = nil
			if err := boundedRunnerTestStep(t, h); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(filepath.Join(op.BackupDir, "data-000"))
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("backup replay replaced the original backup inode")
			}
			if h.s.Status().Operation.Phase != "backed_up" || len(h.s.Status().Operation.Backups) == 0 || h.p.starts != 0 {
				t.Fatal("backup outcome did not reconcile before candidate launch")
			}
		})
	}
}
