//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

type boundedOwnedExitProcess struct {
	*fakeProcess
	t              *testing.T
	firstExit      bool
	returnIdentity bool
}

func (p *boundedOwnedExitProcess) Start(ctx context.Context, binary string, cfg Config, extra map[string]string) (Identity, error) {
	if !p.firstExit {
		return p.fakeProcess.Start(ctx, binary, cfg, extra)
	}
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil || state.Format != 5 {
		return Identity{}, errors.New("fixture: legacy writer was not fenced before the first child dispatch")
	}
	p.firstExit = false
	p.starts++
	copy := make(map[string]string, len(extra)+1)
	for k, v := range extra {
		copy[k] = v
	}
	copy["TEND_UPDATER_FAST_EXIT"] = "1"
	id, err := (LinuxProcess{}).Start(ctx, binary, cfg, copy)
	if err != nil && !errors.Is(err, ErrUnknown) {
		return Identity{}, err
	}
	if p.returnIdentity {
		return id, err
	}
	_ = awaitOwnedLaunchExit(p.t, cfg, extra["TEND_UPDATER_LAUNCH_ID"], extra["TEND_RELEASE_SHA256"])
	// The original caller loses its response. Recovery must reload the owned
	// Wait receipt rather than trusting this wrapper or dispatching again.
	return Identity{}, ErrUnknown
}

func boundedRunnerRealOwnedExitFixture(t *testing.T, late bool) (*harness, protocol.BoundedRunnerMaintenanceGrant, State) {
	t.Helper()
	h, grant := boundedRunnerFixture(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	h.bundle = makeBundle(t, raw)
	manifestPath := filepath.Join(h.bundle, "manifest.json")
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest release.Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Storage.RunnerJournal.Max = 17
	manifestRaw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := release.Load(h.bundle)
	if err != nil {
		t.Fatal(err)
	}
	grant.Plan.ReleaseDir = h.bundle
	grant.Plan.Target.ManifestSHA256 = bundle.ManifestSHA256
	grant.Plan.Target.ArtifactSHA256 = manifest.Artifacts[0].SHA256
	grant.Plan.Authorization.Target = grant.Plan.Target
	grant.Plan.Authorization.PlanSHA256, _ = protocol.RunnerBoundedMaintenancePlanSHA256(grant.Plan)
	h.cfg.Args = []string{"-test.run=^TestUpdaterMaintenanceFastExitHelper$"}
	if late {
		h.cfg.Args = []string{"-test.run=^TestBoundedRunnerDelayedExitHelper$"}
	}
	h.s.cfg.Args = h.cfg.Args
	process := &boundedOwnedExitProcess{fakeProcess: h.p, t: t, firstExit: true, returnIdentity: late}
	h.opts.Process, h.s.opts.Process = process, process
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, "force"); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if h.s.Status().Operation.Phase == "launch_exited" || late && h.s.Status().Operation.Phase == "checking" {
			return h, grant, h.s.Status()
		}
		if err := h.s.Step(t.Context()); err != nil && !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	}
	t.Fatal("original real child exit was not reconciled")
	return nil, protocol.BoundedRunnerMaintenanceGrant{}, State{}
}

func boundedRunnerOwnedExitSuccessor(t *testing.T, h *harness, old protocol.BoundedRunnerMaintenanceGrant) protocol.BoundedRunnerMaintenanceGrant {
	t.Helper()
	next := freshBoundedRunnerGrant(h, old, "req_00000000000000000000000002")
	source, binding, err := OperationMaintenanceSource(h.s.Status())
	if err != nil || binding != old.Plan.Target {
		t.Fatal("original source binding was lost", source, binding, err)
	}
	source.OriginalRequestID = old.Plan.Authorization.RequestID
	next.RecoveryAuthorizationID = next.Plan.Authorization.RequestID
	next.Plan.Source, next.Plan.SourceOperation = Identity{}, source
	next.Plan.Target.UpgradeID, next.Plan.Target.ProcessInstanceID = "owned-exit-new-upgrade", "owned-exit-new-launch"
	next.Plan.Journal.Version = source.StorageVersion
	next.Plan.Authorization.Source, next.Plan.Authorization.Target = next.Plan.Source, next.Plan.Target
	next.Plan.Authorization.DataIdentity = &next.Plan.Journal
	next.Plan.Authorization.PlanSHA256, _ = protocol.RunnerBoundedMaintenancePlanSHA256(next.Plan)
	return next
}

func TestBoundedRunnerRealOwnedExitUsesNewGrantAndKeepsOriginalUnknown(t *testing.T) {
	h, old, original := boundedRunnerRealOwnedExitFixture(t, false)
	if original.Operation.LaunchExit == nil || original.Operation.LaunchExit.ExitCode != 17 || h.p.starts != 1 {
		t.Fatal("fixture has no real original owned Wait", original.Operation, h.p.starts)
	}
	next := boundedRunnerOwnedExitSuccessor(t, h, old)
	h.opts.Authority = boundedRunnerAuthority{fakeAuthority: h.a, requestID: next.RecoveryAuthorizationID}
	h.s.opts.Authority = h.opts.Authority
	before, err := os.ReadFile(h.data)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), next, "force"); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if h.s.Status().Operation.Phase == "committed" {
			break
		}
		if err := h.s.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	state := h.s.Status()
	if state.Operation.Phase != "committed" || state.Operation.LaunchID != next.Plan.Target.ProcessInstanceID || state.Bounded.Outcome != "installed_recovery_pending" || h.p.starts != 2 || h.p.stops != 1 {
		t.Fatalf("independent replacement failed or touched the wrong child: %+v starts=%d stops=%d", state.Operation, h.p.starts, h.p.stops)
	}
	if !reflect.DeepEqual(state.OperationHistory[original.Operation.ID], *original.Operation) || !reflect.DeepEqual(state.BoundedHistory[old.RecoveryAuthorizationID], *original.Bounded) {
		t.Fatal("replacement changed the original failed launch or expired authority history")
	}
	after, err := os.ReadFile(h.data)
	if err != nil || string(before) != string(after) {
		t.Fatal("forward recovery changed or rolled back the journal", err)
	}
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), next, "force"); err != nil || h.p.starts != 2 {
		t.Fatal("replayed original grant started another launch", err)
	}
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.s.Status().OperationHistory[original.Operation.ID], *original.Operation) {
		t.Fatal("updater restart lost original owned-exit history")
	}
}

func TestBoundedRunnerOwnedExitCannotReplaceMissingDurableProof(t *testing.T) {
	h, old, _ := boundedRunnerRealOwnedExitFixture(t, false)
	next := boundedRunnerOwnedExitSuccessor(t, h, old)
	launch, sha := next.Plan.SourceOperation.LaunchID, next.Plan.SourceOperation.LaunchExit.ArtifactSHA256
	dir, err := ownedLaunchDirectory(h.s.cfg, launch, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "exit.json")); err != nil {
		t.Fatal(err)
	}
	before := h.s.Status()
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), next, "force"); err == nil || !reflect.DeepEqual(before, h.s.Status()) || h.p.starts != 1 {
		t.Fatal("Find=0 replaced missing owned exit proof", err)
	}
	if proof, err := LoadOwnedLaunchExit(h.s.cfg, launch, sha); proof != nil || err != nil {
		t.Fatal("missing exit fact was recreated", proof, err)
	}
	// A fresh finite window is an independent action; it never extends this
	// retained original deadline merely because the root child is absent.
	if h.s.Status().Bounded.OverallDeadlineMillis != before.Bounded.OverallDeadlineMillis {
		t.Fatal("rejected replacement renewed the old deadline")
	}
}

func TestBoundedRunnerDelayedExitHelper(t *testing.T) {
	if os.Getenv("TEND_UPDATER_FAST_EXIT") == "1" {
		time.Sleep(250 * time.Millisecond)
		os.Exit(17)
	}
}

func TestBoundedRunnerOwnedExitObservedAfterDeadlineKeepsOriginalWindow(t *testing.T) {
	h, _, before := boundedRunnerRealOwnedExitFixture(t, true)
	if before.Operation.Phase != "checking" || before.Operation.CandidateProcess.PID <= 1 {
		t.Fatal("fixture did not observe the real initial child identity", before.Operation)
	}
	op := before.Operation
	_ = awaitOwnedLaunchExit(t, h.s.cfg, op.LaunchID, op.Candidate.Artifact.SHA256)
	h.now = time.UnixMilli(before.Bounded.Authorization.ExpiresAtMillis + 1)
	h.boot += before.Bounded.Authorization.Timeouts.OverallMillis + 1
	if err := h.s.Step(t.Context()); err != nil {
		t.Fatal("passive original Wait evidence was blocked by an expired action grant", err)
	}
	after := h.s.Status()
	if after.Operation.Phase != "launch_exited" || after.Operation.LaunchExit == nil || after.Bounded.Outcome != "timed_out" || after.Bounded.OverallDeadlineMillis != before.Bounded.OverallDeadlineMillis || after.Bounded.PhaseDeadlineMillis != before.Bounded.PhaseDeadlineMillis || h.p.starts != 1 {
		t.Fatal("late original evidence restarted work or renewed its deadline", after.Operation, after.Bounded)
	}
}
