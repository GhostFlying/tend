//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GhostFlying/tend/internal/release"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type committedTestAuthority struct {
	fakeAuthority
	p                     *fakeProcess
	archive               protocol.ArchiveOnlineStatus
	sequence              uint64
	unready, blockArchive bool
}

func (a *committedTestAuthority) MaintenanceArchiveStatus(context.Context) (protocol.ArchiveOnlineStatus, error) {
	if a.blockArchive {
		return protocol.ArchiveOnlineStatus{}, errors.New("failed service API unavailable")
	}
	return a.archive, nil
}
func (a *committedTestAuthority) MaintenanceArchiveControl(_ context.Context, path string, input any) error {
	switch v := input.(type) {
	case protocol.ArchiveControlRequest:
		if v.ExpectedControlRevision != a.archive.Preparation.Revision {
			return errors.New("stale preparation")
		}
		a.archive.Preparation.Revision++
		a.archive.Preparation.Prepared = path == "prepare"
	case protocol.ArchiveHostControlRequest:
		found := false
		for i := range a.archive.Hosts {
			h := &a.archive.Hosts[i]
			if h.HostID != v.HostID {
				continue
			}
			found = true
			if h.Control.Revision != v.ExpectedControlRevision {
				return errors.New("stale host")
			}
			h.Control.Revision++
			h.Control.Mode = v.Mode
			h.Runtime.AppliedControlRevision = h.Control.Revision
			h.Runtime.Mode = v.Mode
		}
		if !found {
			return errors.New("unknown host")
		}
	}
	return nil
}
func (a *committedTestAuthority) MaintenanceHealth(_ context.Context, p protocol.CommittedMaintenancePlan) (protocol.CommittedMaintenanceHealth, error) {
	a.sequence++
	var process Identity
	for _, v := range a.p.live {
		if v.LaunchID == p.Successor.ProcessInstanceID {
			process = v
		}
	}
	return protocol.CommittedMaintenanceHealth{MaintenanceID: p.MaintenanceID, Binding: p.Successor, Process: process, Epoch: "successor-epoch", Sequence: a.sequence, Protocol: 2, Schema: 27, Registered: true, Fenced: true, Ready: !a.unready}, nil
}
func committedFixture(t *testing.T) (*harness, protocol.CommittedMaintenancePlan, *committedTestAuthority) {
	t.Helper()
	h, old, _ := forwardFixture(t)
	v := h.s.state.Operation.Candidate
	h.s.state.Current = v
	h.s.state.CommittedProof = &CommittedProof{Binding: old.Original, Version: v, Process: old.OriginalProcess}
	h.s.state.Operation = nil
	a := &committedTestAuthority{p: h.p, archive: protocol.ArchiveOnlineStatus{HostedDrained: false, Hosts: []protocol.ArchiveHostStatus{{HostID: "fixture-host", Fresh: true, Control: protocol.RunnerControl{Revision: 5, Mode: "normal"}, Runtime: &protocol.RunnerRuntime{Mode: "normal", AppliedControlRevision: 5}, OpenRuns: 4}}}}
	h.opts.Authority = a
	h.s.opts.Authority = a
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "committed-fixture", RequestID: old.RequestID, LinkRequestID: old.LinkRequestID, PreparationRequestID: old.OriginalRequestID, PreparationCancelRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", HostControls: []protocol.MaintenanceHostControl{{HostID: "fixture-host", Revision: 5, PreviousMode: "normal", RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEZ", RestoreRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF0"}}, Previous: old.Original, PreviousProcess: old.OriginalProcess, ExpectedGeneration: h.s.state.Generation, StateSHA256: digest(h.s.state), ConfigSHA256: old.ConfigSHA256, ReleaseDir: old.ReleaseDir, Successor: old.Successor, OperatorUID: old.OperatorUID, WindowClaim: old.WindowClaim, ResourceEvidence: old.ResourceEvidence, ResourceSHA256: old.ResourceSHA256, ExpiresAtMillis: old.ExpiresAtMillis, PreparationRevision: 1}
	p.Successor.UpgradeID = p.MaintenanceID
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	return h, p, a
}
func TestCommittedMaintenanceNotDrainedCanCancelWithoutReplacingServer(t *testing.T) {
	h, p, a := committedFixture(t)
	original := h.s.Status()
	ctx := t.Context()
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err == nil {
		t.Fatal("undrained hosted work authorized replacement")
	}
	if h.p.stops != 0 {
		t.Fatal("undrained service stopped")
	}
	if err := h.s.CancelCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	now := h.s.Status()
	if now.Maintenance.Phase != "cancelled" || now.Current != original.Current || now.Process != original.Process || !reflect.DeepEqual(now.CommittedProof, original.CommittedProof) || a.archive.Preparation.Prepared || a.archive.Hosts[0].Control.Mode != "normal" || h.p.stops != 0 {
		t.Fatal("cancel changed committed service or retained host maintenance")
	}
	if err := h.s.CancelCommittedMaintenance(ctx, p, 0); err != nil {
		t.Fatal("cancel replay", err)
	}
}
func TestCommittedMaintenanceRetainsOpenRunsAndLinksIndependentReceipt(t *testing.T) {
	h, p, a := committedFixture(t)
	ctx := t.Context()
	old := h.s.Status()
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	a.archive.Hosts[0].Runtime.Mode = "ready"
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		h.step(t)
	}
	st := h.s.Status()
	if st.Maintenance.Phase != "successor_committed" || st.Maintenance.Receipt == nil || st.Current == old.Current || len(st.ProofHistory) != 1 || a.archive.Hosts[0].OpenRuns != 4 {
		t.Fatal("missing independent successor proof", st.Maintenance)
	}
	if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	if a.archive.Preparation.Prepared || a.archive.Hosts[0].Control.Mode != "normal" {
		t.Fatal("owned admission controls not restored")
	}
}
func TestCommittedMaintenanceRechecksHostedDrainBeforeStop(t *testing.T) {
	h, p, a := committedFixture(t)
	ctx := t.Context()
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = false
	if err := h.s.Step(ctx); err == nil || h.p.stops != 0 {
		t.Fatal("drain changed after authorization but server stopped")
	}
}

func TestCommittedMaintenanceCanDeployAgainAfterLinkedReceipt(t *testing.T) {
	h, p, a := committedFixture(t)
	ctx := t.Context()
	complete := func(p protocol.CommittedMaintenancePlan) {
		t.Helper()
		if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
			t.Fatal(err)
		}
		a.archive.HostedDrained = true
		if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 7; i++ {
			h.step(t)
		}
		st := h.s.Status()
		if st.Maintenance.Receipt == nil {
			t.Fatal("missing receipt")
		}
		if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
			t.Fatal(err)
		}
		if err := h.s.FinishCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
			t.Fatal(err)
		}
	}
	complete(p)
	first := h.s.Status()
	previous := *first.Maintenance.Receipt
	dir := makeBundle(t, []byte("second independent maintenance server"))
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest release.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Artifacts[0].Role = "server"
	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	b, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	next := p
	next.MaintenanceID = "second-committed-window"
	next.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF1"
	next.LinkRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
	next.PreparationRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF3"
	next.PreparationCancelRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF4"
	next.PreparationRevision = 3
	next.HostControls = []protocol.MaintenanceHostControl{{HostID: "fixture-host", Revision: 7, PreviousMode: "normal", RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF5", RestoreRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF6"}}
	next.Previous = first.CommittedProof.Binding
	next.PreviousProcess = first.Process
	next.ExpectedGeneration = first.Generation
	next.StateSHA256 = digest(first)
	next.ReleaseDir = dir
	next.Successor = Binding{UpgradeID: next.MaintenanceID, InstanceID: "server", ProcessInstanceID: "second-successor-launch", ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.Manifest.Artifacts[0].SHA256}
	if err := h.s.AdoptCommittedMaintenance(ctx, next); err != nil {
		t.Fatal("second window cannot adopt", err)
	}
	complete(next)
	now := h.s.Status()
	history, ok := now.MaintenanceHistory[p.MaintenanceID]
	if !ok || history.Receipt == nil || *history.Receipt != previous || history.Phase != "ledger_linked" || len(now.ProofHistory) != 2 {
		t.Fatal("previous receipt or committed history overwritten")
	}
}

type committedUncertainStop struct {
	*fakeProcess
	failed bool
}

func (p *committedUncertainStop) Stop(ctx context.Context, id Identity, timeout time.Duration) error {
	if err := p.fakeProcess.Stop(ctx, id, timeout); err != nil {
		return err
	}
	if !p.failed {
		p.failed = true
		return ErrUnknown
	}
	return nil
}
func TestCommittedMaintenanceUnknownStopAndStartReconcileSameIdentity(t *testing.T) {
	h, p, a := committedFixture(t)
	ctx := t.Context()
	uncertain := &committedUncertainStop{fakeProcess: h.p}
	h.s.opts.Process = uncertain
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Step(ctx); !errors.Is(err, ErrUnknown) {
		t.Fatal("stop must retain unknown fact", err)
	}
	if h.s.Status().Maintenance.Phase != "stopping_old" {
		t.Fatal("unknown stop did not retain durable intent")
	}
	h.p.ambiguousStart = true
	for i := 0; i < 8; i++ {
		err := h.s.Step(ctx)
		if err != nil && !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	}
	st := h.s.Status()
	if st.Maintenance.Phase != "successor_committed" || h.p.starts != 1 || h.p.stops != 1 || st.Process.LaunchID != p.Successor.ProcessInstanceID {
		t.Fatal("unknown side effect was repeated or rebound", st.Maintenance, h.p.starts, h.p.stops)
	}
}

func TestCommittedMaintenanceFailedCandidateCanForwardWithoutItsAPI(t *testing.T) {
	for _, mode := range []string{"checking-live", "checking-gone", "committed-unlinked", "launch-exited", "force-checking-live", "force-checking-unobserved"} {
		t.Run(mode, func(t *testing.T) {
			forced := strings.HasPrefix(mode, "force-")
			mode = strings.TrimPrefix(mode, "force-")
			h, p, a := committedFixture(t)
			ctx := t.Context()
			if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
				t.Fatal(err)
			}
			a.archive.HostedDrained = !forced
			if forced {
				a.archive.Blockers = []string{"native receipt reads: 3", "summary operations: 14", "hosted operations are active or uncertain"}
				a.blockArchive = mode == "checking-unobserved"
				auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: h.s.Status().Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(p), Role: "server", DataIdentity: boundedServerTestData(h), Source: p.PreviousProcess, Target: p.Successor, Reason: "explicit unknown predecessor recovery", ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
				if err := h.s.AuthorizeBoundedMaintenance(ctx, p, auth, "force"); err != nil {
					t.Fatal(err)
				}
			} else if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
				t.Fatal(err)
			}
			a.unready = mode != "committed-unlinked"
			if mode == "launch-exited" {
				h.s.opts.Process = &earlyExitedProcess{fakeProcess: h.p}
			}
			for i := 0; i < 7; i++ {
				_ = h.s.Step(ctx)
			}
			before := h.s.Status()
			parent := before.Maintenance
			if parent.Phase != "checking_successor" && parent.Phase != "successor_committed" && parent.Phase != "launch_exited" {
				t.Fatal("failed parent shape not reached", parent.Phase)
			}
			if mode == "checking-unobserved" && parent.ArchiveAuthorization != nil {
				t.Fatal("failed observation was replaced by a synthetic archive")
			}
			next := p
			next.MaintenanceID = "forward-failed-maintenance"
			next.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF7"
			next.LinkRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF8"
			next.ParentMaintenanceID = p.MaintenanceID
			copy := p
			next.ParentPlan = &copy
			next.ParentPlanSHA256 = digest(p)
			next.ParentReceipt = parent.Receipt
			next.ParentArchiveSHA256 = digest(parent.ArchiveAuthorization)
			next.ParentGeneration = before.Generation
			next.ExpectedGeneration = before.Generation
			next.Previous = p.Successor
			next.PreviousProcess = parent.Process
			if mode == "launch-exited" {
				next.FailedProcessExited = true
				proof := *parent.LaunchExit
				next.ParentLaunchExit = &proof
				h.s.opts.Process = h.p
			}
			if mode == "checking-gone" {
				delete(h.p.live, parent.Process.PID)
				next.FailedProcessExited = true
			}
			next.StateSHA256 = digest(h.s.state)
			dir := makeBundle(t, []byte("repaired candidate after failed maintenance"))
			raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
			var manifest release.Manifest
			_ = json.Unmarshal(raw, &manifest)
			manifest.Artifacts[0].Role = "server"
			if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
				t.Fatal(err)
			}
			b, err := release.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			next.ReleaseDir = dir
			next.Successor = Binding{UpgradeID: next.MaintenanceID, InstanceID: "server", ProcessInstanceID: "repaired-launch", ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.Manifest.Artifacts[0].SHA256}
			if err := h.s.AdoptCommittedMaintenance(ctx, next); err != nil {
				t.Fatal("cannot adopt exact failed parent", err)
			}
			a.blockArchive = true
			a.unready = false
			if forced {
				if mode == "checking-unobserved" {
					beforePrepare := h.s.Status()
					if err := h.s.PrepareCommittedMaintenance(ctx, next, beforePrepare.Generation); err == nil || !reflect.DeepEqual(beforePrepare, h.s.Status()) {
						t.Fatal("ordinary preparation changed an unobserved forced parent", err)
					}
				}
				if err := h.s.AuthorizeCommittedMaintenance(ctx, next, h.s.Status().Generation); err == nil {
					t.Fatal("ordinary forward converted the old forced observation into drain proof")
				}
				auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF2", ExpectedGeneration: h.s.Status().Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(next), Role: "server", DataIdentity: boundedServerTestData(h), Source: next.PreviousProcess, Target: next.Successor, Reason: "independently authorize the repaired candidate", ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
				if mode == "checking-unobserved" {
					root := t.TempDir()
					planFile, authFile := filepath.Join(root, "plan.json"), filepath.Join(root, "authorization.json")
					if err := writeJSON(planFile, CommittedHandoffPlan{Maintenance: next}); err != nil {
						t.Fatal(err)
					}
					if err := writeJSON(authFile, auth); err != nil {
						t.Fatal(err)
					}
					stop := boundedCancelServe(t, h)
					_, err := BoundedCommand(ctx, h.cfg, "force", planFile, authFile, auth.RequestID, auth.ExpectedGeneration, auth.ExpectedPhase)
					stop()
					if err != nil {
						t.Fatal("bounded installer could not independently authorize forward recovery", err)
					}
				} else if err := h.s.AuthorizeBoundedMaintenance(ctx, next, auth, "force"); err != nil {
					t.Fatal("cannot explicitly forward from forced parent", err)
				}
				if err := ValidateBoundedMaintenanceParent(h.s.Status(), *parent); err != nil {
					t.Fatal("lost original unknown parent authority", err)
				}
			} else {
				if err := h.s.PrepareCommittedMaintenance(ctx, next, h.s.Status().Generation); err != nil {
					t.Fatal("child depended on broken API", err)
				}
				if err := h.s.AuthorizeCommittedMaintenance(ctx, next, h.s.Status().Generation); err != nil {
					t.Fatal("cannot authorize forward without broken API", err)
				}
			}
			for i := 0; i < 7; i++ {
				h.step(t)
			}
			st := h.s.Status()
			if st.Maintenance.Phase != "successor_committed" || st.Maintenance.Receipt == nil {
				t.Fatal("repaired successor did not commit", st.Maintenance)
			}
			historical := st.MaintenanceHistory[p.MaintenanceID]
			if !reflect.DeepEqual(historical, *parent) {
				t.Fatal("failed parent history rewritten")
			}
			if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
				t.Fatal(err)
			}
			fork := next
			fork.MaintenanceID = "fork-from-old-parent"
			fork.ExpectedGeneration = h.s.Status().Generation
			fork.ParentGeneration = fork.ExpectedGeneration
			fork.StateSHA256 = digest(h.s.state)
			if err := h.s.AdoptCommittedMaintenance(ctx, fork); err == nil {
				t.Fatal("superseded failed parent authorized a fork")
			}
		})
	}
}

func TestCommittedMaintenancePrelaunchContinuationKeepsCandidateAndRequest(t *testing.T) {
	for _, phase := range []string{"authorized", "stopping_old", "old_exited", "backed_up", "launch_not_started"} {
		t.Run(phase, func(t *testing.T) {
			h, p, a := committedFixture(t)
			ctx := t.Context()
			if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
				t.Fatal(err)
			}
			a.archive.HostedDrained = true
			if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
				t.Fatal(err)
			}
			if phase == "authorized" {
				delete(h.p.live, p.PreviousProcess.PID)
			} else if phase == "stopping_old" {
				h.s.opts.Process = &committedUncertainStop{fakeProcess: h.p}
				if err := h.s.Step(ctx); !errors.Is(err, ErrUnknown) {
					t.Fatal(err)
				}
			} else {
				h.step(t)
				if phase != "old_exited" {
					h.step(t)
				}
			}
			if phase == "launch_not_started" {
				h.p.rejectStart = true
				if err := h.s.Step(ctx); !errors.Is(err, ErrNotStarted) {
					t.Fatal(err)
				}
			}
			if got := h.s.Status().Maintenance.Phase; got != phase {
				t.Fatal("prelaunch fixture", got)
			}
			h.now = h.now.Add(11 * time.Minute)
			if err := h.s.Step(ctx); err == nil && !(phase == "stopping_old" && h.s.Status().Maintenance.Phase == "old_exited") {
				t.Fatal("expired/definitely-not-started operation advanced beyond exit observation without new action")
			}
			if h.p.starts != 0 {
				t.Fatal("candidate launched without current authority")
			}
			resourceFile := filepath.Join(t.TempDir(), "fresh-resources.json")
			raw, _ := os.ReadFile(p.ResourceEvidence)
			var resources ResourceHandoff
			_ = json.Unmarshal(raw, &resources)
			resources.WindowClaim = "fresh-prelaunch-continuation"
			resources.ExpiresAtMillis = h.now.Add(10 * time.Minute).UnixMilli()
			resources.RunnerCoverageUntilMillis = resources.ExpiresAtMillis
			if err := writeJSON(resourceFile, resources); err != nil {
				t.Fatal(err)
			}
			sum, _ := hashFile(resourceFile)
			c := protocol.MaintenanceContinuation{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF9", ExpectedGeneration: h.s.Status().Generation, WindowClaim: resources.WindowClaim, ExpiresAtMillis: resources.ExpiresAtMillis, ResourceEvidence: resourceFile, ResourceSHA256: sum}
			stale := c
			stale.ExpectedGeneration--
			if err := h.s.ContinueCommittedMaintenance(ctx, p, stale); err == nil {
				t.Fatal("stale continuation accepted")
			}
			if err := h.s.ContinueCommittedMaintenance(ctx, p, c); err != nil {
				t.Fatal("fresh original-plan continuation rejected", err)
			}
			if err := h.s.ContinueCommittedMaintenance(ctx, p, c); err != nil {
				t.Fatal("same continuation replay failed", err)
			}
			for i := 0; i < 7; i++ {
				h.step(t)
			}
			st := h.s.Status()
			if st.Maintenance.Phase != "successor_committed" || h.p.starts != 1 || st.Maintenance.Plan.RequestID != p.RequestID || st.Current.Artifact.SHA256 != p.Successor.ArtifactSHA256 {
				t.Fatal("continuation changed candidate or repeated launch")
			}
			if err := h.s.ContinueCommittedMaintenance(ctx, p, c); err != nil {
				t.Fatal("completed continuation replay failed", err)
			}
		})
	}
}

func TestCommittedMaintenanceDoesNotReauthorizeAmbiguousLaunch(t *testing.T) {
	h, p, a := committedFixture(t)
	ctx := t.Context()
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	h.step(t)
	h.step(t)
	h.p.ambiguousStart = true
	if err := h.s.Step(ctx); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	c := protocol.MaintenanceContinuation{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF9", ExpectedGeneration: h.s.Status().Generation, WindowClaim: "not-a-new-launch", ExpiresAtMillis: h.now.Add(time.Minute).UnixMilli()}
	if err := h.s.ContinueCommittedMaintenance(ctx, p, c); err == nil {
		t.Fatal("ambiguous launch got a second authorization")
	}
	h.step(t)
	if h.p.starts != 1 {
		t.Fatal("ambiguous launch repeated")
	}
}

func TestCommittedMaintenanceHistoryRemainsBoundedAcrossTwentyWindows(t *testing.T) {
	h, p, _ := committedFixture(t)
	ctx := t.Context()
	originalSource := h.s.Status().Maintenance.OriginalStateSHA256
	for n := 0; n < 20; n++ {
		if n > 0 {
			p.MaintenanceID = fmt.Sprintf("bounded-window-%d", n)
			p.RequestID = fmt.Sprintf("req_%026d", 100+n*4)
			p.LinkRequestID = fmt.Sprintf("req_%026d", 101+n*4)
			p.PreparationRequestID = fmt.Sprintf("req_%026d", 102+n*4)
			p.PreparationCancelRequestID = fmt.Sprintf("req_%026d", 103+n*4)
			p.Successor.UpgradeID = p.MaintenanceID
			p.Successor.ProcessInstanceID = fmt.Sprintf("bounded-launch-%d", n)
			p.ExpectedGeneration = h.s.Status().Generation
			p.StateSHA256 = digest(h.s.state)
			if err := h.s.AdoptCommittedMaintenance(ctx, p); err != nil {
				t.Fatal(n, err)
			}
		}
		if err := h.s.CancelCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
			t.Fatal(n, err)
		}
		state := h.s.Status()
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 64*1024+(n+1)*16*1024 {
			t.Fatalf("window %d state grew beyond linear metadata budget: %d", n, len(raw))
		}
		if state.Maintenance.OriginalStateSHA256 != p.StateSHA256 {
			t.Fatal("lost immutable source snapshot reference")
		}
	}
	state := h.s.Status()
	if len(state.MaintenanceHistory) != 19 || state.MaintenanceHistory["committed-fixture"].OriginalStateSHA256 != originalSource {
		t.Fatal("bounded history lost original facts")
	}
}

func TestCommittedMaintenancePrelaunchPhaseClassification(t *testing.T) {
	for _, test := range []struct {
		phase    string
		eligible bool
	}{
		{"updater_adopted", false}, {"authorized", true}, {"stopping_old", true}, {"old_exited", true}, {"backed_up", true}, {"launch_not_started", true}, {"starting_successor", false}, {"checking_successor", false}, {"successor_committed", false}, {"ledger_linked", false}, {"cancelling", false}, {"cancelled", false}, {"unknown", false},
	} {
		t.Run(test.phase, func(t *testing.T) {
			if got := maintenancePrelaunchPhase(test.phase); got != test.eligible {
				t.Fatalf("phase eligibility=%v want%v", got, test.eligible)
			}
		})
	}
}
func TestCommittedMaintenanceContinuationRejectsLiveOriginal(t *testing.T) {
	h, p, a := committedFixture(t)
	ctx := t.Context()
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	c := protocol.MaintenanceContinuation{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF9", ExpectedGeneration: h.s.Status().Generation, WindowClaim: "fresh-continuation", ExpiresAtMillis: h.now.Add(time.Minute).UnixMilli()}
	if err := h.s.ContinueCommittedMaintenance(ctx, p, c); !errors.Is(err, ErrUnknown) {
		t.Fatal("live original must prevent prelaunch recovery", err)
	}
	if h.p.starts != 0 || h.p.stops != 0 || h.s.Status().Maintenance.Phase != "authorized" {
		t.Fatal("recovery changed live original")
	}
}

// earlyExitedProcess reports only the same explicit owned-wait proof as Linux;
// arbitrary Start errors and empty launch scans retain the unknown branch.
type earlyExitedProcess struct{ *fakeProcess }

func (p *earlyExitedProcess) StartMaintenance(_ context.Context, _ string, _ Config, extra map[string]string) (Identity, error) {
	return Identity{}, &launchExitedError{proof: LaunchExitProof{PID: 2345, LaunchID: extra["TEND_UPDATER_LAUNCH_ID"], ArtifactSHA256: extra["TEND_RELEASE_SHA256"], ExitCode: 17}}
}
