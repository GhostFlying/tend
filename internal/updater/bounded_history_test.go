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

type boundedHistoryControl struct {
	path, request, payload string
}

type boundedHistoryAuthority struct {
	*committedTestAuthority
	controls            []boundedHistoryControl
	loseRestoreResponse bool
}

func (a *boundedHistoryAuthority) MaintenanceArchiveStatus(ctx context.Context) (protocol.ArchiveOnlineStatus, error) {
	v, err := a.committedTestAuthority.MaintenanceArchiveStatus(ctx)
	if err != nil {
		return v, err
	}
	// A real authority response owns its snapshot bytes. The original fixture
	// returns slices shared with subsequent control writes; isolate that fixture
	// alias so these tests exercise the durable audit rather than shared memory.
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	var snapshot protocol.ArchiveOnlineStatus
	err = json.Unmarshal(raw, &snapshot)
	return snapshot, err
}

func (a *boundedHistoryAuthority) MaintenanceArchiveControl(ctx context.Context, path string, input any) error {
	var request string
	var restore bool
	switch v := input.(type) {
	case protocol.ArchiveControlRequest:
		request = v.RequestID
	case protocol.ArchiveHostControlRequest:
		request, restore = v.RequestID, v.Mode == protocol.RunnerNormal
	}
	a.controls = append(a.controls, boundedHistoryControl{path: path, request: request, payload: digest(input)})
	if err := a.committedTestAuthority.MaintenanceArchiveControl(ctx, path, input); err != nil {
		return err
	}
	if restore && a.loseRestoreResponse {
		a.loseRestoreResponse = false
		return ErrUnknown
	}
	return nil
}

func boundedHistoryFixture(t *testing.T) (*harness, protocol.CommittedMaintenancePlan, *boundedHistoryAuthority) {
	t.Helper()
	h, p, original := committedFixture(t)
	a := &boundedHistoryAuthority{committedTestAuthority: original}
	a.archive.HostedDrained = false
	a.archive.Blockers = []string{"original native work remains unknown"}
	h.opts.Authority, h.s.opts.Authority = a, a
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	return h, p, a
}

func boundedHistoryAuthorization(h *harness, p protocol.CommittedMaintenancePlan, request string) protocol.BoundedMaintenanceAuthorization {
	state := h.s.Status()
	return protocol.BoundedMaintenanceAuthorization{
		RequestID: request, ExpectedGeneration: state.Generation, ExpectedPhase: state.Maintenance.Phase,
		PlanSHA256: digest(p), Role: "server", Source: p.PreviousProcess, Target: p.Successor,
		Reason:          "Independently recover while retaining original unknown work",
		ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(),
	}
}

func boundedHistoryAdvance(h *harness, milliseconds int64) {
	h.now = h.now.Add(time.Duration(milliseconds) * time.Millisecond)
	h.boot += milliseconds
}

func boundedHistoryCommit(t *testing.T, h *harness) {
	t.Helper()
	for i := 0; i < 10; i++ {
		if h.s.Status().Maintenance.Phase == "successor_committed" {
			return
		}
		if err := h.s.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("successor did not reach committed receipt", h.s.Status().Maintenance)
}

func boundedHistoryNextPlan(t *testing.T, h *harness, p protocol.CommittedMaintenancePlan, a *boundedHistoryAuthority) protocol.CommittedMaintenancePlan {
	t.Helper()
	state := h.s.Status()
	dir := makeBundle(t, []byte("independent successor after the bounded window"))
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
	bundle, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	next := p
	next.MaintenanceID = "bounded-history-next-window"
	next.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF3"
	next.LinkRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF4"
	next.PreparationRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF5"
	next.PreparationCancelRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF6"
	next.PreparationRevision = a.archive.Preparation.Revision + 1
	next.HostControls = []protocol.MaintenanceHostControl{{HostID: a.archive.Hosts[0].HostID, Revision: a.archive.Hosts[0].Control.Revision, PreviousMode: protocol.RunnerNormal, RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF7", RestoreRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF8"}}
	next.Previous, next.PreviousProcess = state.CommittedProof.Binding, state.Process
	next.ExpectedGeneration, next.StateSHA256 = state.Generation, digest(state)
	next.ReleaseDir = dir
	next.Successor = Binding{UpgradeID: next.MaintenanceID, InstanceID: h.cfg.InstanceID, ProcessInstanceID: "next-bounded-successor", ReleaseID: bundle.Manifest.ReleaseID, ManifestSHA256: bundle.ManifestSHA256, ArtifactSHA256: bundle.Manifest.Artifacts[0].SHA256}
	return next
}

func TestBoundedHistoryOriginalAuthoritiesSurviveExtensionAndNextAdoption(t *testing.T) {
	h, p, a := boundedHistoryFixture(t)
	first := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, first, "force"); err != nil {
		t.Fatal(err)
	}
	original := h.s.Status().Bounded
	boundedHistoryAdvance(h, first.Timeouts.StopMillis)
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("expired force window allowed the original stop")
	}
	expired := h.s.Status().Bounded
	if expired.Outcome != "timed_out" || h.p.stops != 0 || h.p.starts != 0 {
		t.Fatal("unstarted expired window lost its actual outcome", expired, h.p.stops, h.p.starts)
	}
	second := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF2")
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, second, "extend"); err != nil {
		t.Fatal(err)
	}
	extended := h.s.Status().Bounded
	last := extended.Audit[len(extended.Audit)-1]
	previous := last.PreviousWindow
	if previous == nil || previous.Phase != original.Phase || previous.PhaseEnteredAtMillis != original.PhaseEnteredAtMillis || previous.PhaseDeadlineMillis != original.PhaseDeadlineMillis || previous.OverallDeadlineMillis != original.OverallDeadlineMillis || previous.PhaseDeadlineMono != original.PhaseDeadlineMono || previous.OverallDeadlineMono != original.OverallDeadlineMono || previous.Outcome != "timed_out" {
		t.Fatal("extension erased or renewed its expired predecessor window", previous, original)
	}
	if !reflect.DeepEqual(expired.Audit, extended.Audit[:len(expired.Audit)]) {
		t.Fatal("extension rewrote original timeout or unknown-work audit")
	}
	boundedHistoryCommit(t, h)
	committed := h.s.Status()
	if err := h.s.LinkCommittedMaintenance(*committed.Maintenance.Receipt, committed.Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	finished := h.s.Status()
	if finished.Bounded.Outcome != "completed" || h.p.stops != 1 || h.p.starts != 1 || a.archive.Hosts[0].OpenRuns != 4 || finished.Bounded.Audit[0].Archive.HostedDrained {
		t.Fatal("completion did not retain original undrained work or repeated native effects", finished.Bounded, h.p.stops, h.p.starts)
	}
	next := boundedHistoryNextPlan(t, h, p, a)
	if err := h.s.AdoptCommittedMaintenance(t.Context(), next); err != nil {
		t.Fatal("fresh independent maintenance could not adopt", err)
	}
	h.reopen(t)
	adopted := h.s.Status()
	if adopted.Format != 4 || adopted.Bounded != nil || len(adopted.BoundedHistory) == 0 {
		t.Fatal("new adoption downgraded or erased bounded history", adopted)
	}
	for _, action := range []string{"status", "reconcile"} {
		for _, originalAuthority := range []protocol.BoundedMaintenanceAuthorization{first, second} {
			value, err := BoundedCommand(t.Context(), h.cfg, action, "", "", originalAuthority.RequestID, 0, "")
			if err != nil {
				t.Fatal("original request history is no longer queryable", action, originalAuthority.RequestID, err)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Bounded protocol.BoundedMaintenanceState `json:"bounded"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if got, ok := response.Bounded.Authorizations[originalAuthority.RequestID]; !ok || !reflect.DeepEqual(got, originalAuthority) || !reflect.DeepEqual(response.Bounded, *finished.Bounded) {
				t.Fatal("historical query changed original authority or terminal window", action, originalAuthority.RequestID, string(raw))
			}
		}
	}
	for _, request := range []string{first.RequestID, second.RequestID} {
		reused := boundedHistoryAuthorization(h, next, request)
		before := h.s.Status()
		if err := h.s.AuthorizeBoundedMaintenance(t.Context(), next, reused, "force"); err == nil {
			t.Fatal("historical request authorized a different plan", request)
		}
		if after := h.s.Status(); !reflect.DeepEqual(after, before) || h.p.stops != 1 || h.p.starts != 1 {
			t.Fatal("rejected historical identity changed state or native effects", request)
		}
	}
}

func TestBoundedHistoryCompletionDeadlineBlocksEffectsUntilFreshExtension(t *testing.T) {
	for _, deadline := range []string{"phase", "overall"} {
		for _, operation := range []string{"link", "finish"} {
			t.Run(deadline+"/"+operation, func(t *testing.T) {
				h, p, a := boundedHistoryFixture(t)
				first := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
				if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, first, "force"); err != nil {
					t.Fatal(err)
				}
				boundedHistoryCommit(t, h)
				committed := h.s.Status()
				if committed.Bounded.Phase != "complete" || committed.Maintenance.Receipt == nil {
					t.Fatal("successor did not enter its separate completion budget", committed)
				}
				if operation == "finish" {
					if err := h.s.LinkCommittedMaintenance(*committed.Maintenance.Receipt, committed.Generation); err != nil {
						t.Fatal(err)
					}
					a.loseRestoreResponse = true
					if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); !errors.Is(err, ErrUnknown) {
						t.Fatal("lost original restoration response was not retained", err)
					}
				}
				beforeTimeout := h.s.Status()
				controls := append([]boundedHistoryControl(nil), a.controls...)
				if deadline == "phase" {
					boundedHistoryAdvance(h, beforeTimeout.Bounded.PhaseDeadlineMillis-h.now.UnixMilli())
				} else {
					boundedHistoryAdvance(h, beforeTimeout.Bounded.OverallDeadlineMillis-h.now.UnixMilli())
				}
				execute := func() error {
					state := h.s.Status()
					if operation == "link" {
						return h.s.LinkCommittedMaintenance(*state.Maintenance.Receipt, state.Generation)
					}
					return h.s.FinishCommittedMaintenance(t.Context(), p, state.Generation)
				}
				if err := execute(); err == nil {
					t.Fatal("expired completion window admitted a new side effect")
				}
				expired := h.s.Status()
				if expired.Maintenance.Phase != beforeTimeout.Maintenance.Phase || !reflect.DeepEqual(a.controls, controls) || h.p.starts != 1 || h.p.stops != 1 || !reflect.DeepEqual(expired.CommittedProof, committed.CommittedProof) {
					t.Fatal("expired completion repeated a native/control effect or changed installed proof", expired)
				}
				durable, err := readState(filepath.Join(h.cfg.StateDir, "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				if durable.Bounded.Outcome != "installed_recovery_pending" || durable.Bounded.PhaseDeadlineMillis != beforeTimeout.Bounded.PhaseDeadlineMillis || durable.Bounded.OverallDeadlineMillis != beforeTimeout.Bounded.OverallDeadlineMillis {
					t.Errorf("durable completion outcome %q, phase deadline %d (want %d), overall deadline %d (want %d)", durable.Bounded.Outcome, durable.Bounded.PhaseDeadlineMillis, beforeTimeout.Bounded.PhaseDeadlineMillis, durable.Bounded.OverallDeadlineMillis, beforeTimeout.Bounded.OverallDeadlineMillis)
				}
				assertStableWindow := func(want State) {
					t.Helper()
					after := h.s.Status()
					if after.Generation != want.Generation || !reflect.DeepEqual(after.Bounded, want.Bounded) || !reflect.DeepEqual(a.controls, controls) || h.p.starts != 1 || h.p.stops != 1 {
						t.Fatalf("repeated expired-window observation changed generation %d -> %d, audit length %d -> %d, outcome %q -> %q or effects", want.Generation, after.Generation, len(want.Bounded.Audit), len(after.Bounded.Audit), want.Bounded.Outcome, after.Bounded.Outcome)
					}
					onDisk, err := readState(filepath.Join(h.cfg.StateDir, "state.json"))
					if err != nil || onDisk.Generation != want.Generation || !reflect.DeepEqual(onDisk.Bounded, want.Bounded) {
						t.Fatal("repeated expired-window observation changed its durable window", err)
					}
				}
				// Every retry supplies the freshly observed generation. Rejection must
				// come from the same expired window, not a stale-generation shortcut.
				if err := execute(); err == nil {
					t.Fatal("fresh-generation retry renewed expired completion authority")
				}
				assertStableWindow(expired)
				state := h.s.Status()
				_ = h.s.LinkCommittedMaintenance(*state.Maintenance.Receipt, state.Generation)
				assertStableWindow(expired)
				_ = h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation)
				assertStableWindow(expired)
				// The first Step may add its first LastError diagnostic. Further Step
				// calls must not repeatedly save the same deadline or oscillate outcome.
				_ = h.s.Step(t.Context())
				observed := h.s.Status()
				if !reflect.DeepEqual(observed.Bounded, expired.Bounded) || !reflect.DeepEqual(a.controls, controls) || h.p.starts != 1 || h.p.stops != 1 {
					t.Fatal("observing an expired window renewed its budget, audit or effects")
				}
				for range 2 {
					boundedHistoryAdvance(h, 1)
					_ = h.s.Step(t.Context())
					assertStableWindow(observed)
					for _, action := range []string{"status", "reconcile"} {
						if _, err := BoundedCommand(t.Context(), h.cfg, action, "", "", first.RequestID, 0, ""); err != nil {
							t.Fatal("expired original authority could not be read", action, err)
						}
						assertStableWindow(observed)
					}
				}
				if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, first, "force"); err != nil {
					t.Fatal("original request was not an idempotent readback", err)
				}
				if err := execute(); err == nil || !reflect.DeepEqual(a.controls, controls) {
					t.Fatal("original request replay renewed the expired completion authority", err)
				}
				assertStableWindow(observed)
				prior := h.s.Status().Bounded
				second := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF2")
				stale := second
				stale.ExpectedGeneration--
				if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, stale, "extend"); err == nil {
					t.Fatal("stale external extension reopened completion")
				}
				if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, second, "extend"); err != nil {
					t.Fatal("fresh independent extension could not continue completion", err)
				}
				extended := h.s.Status().Bounded
				if !reflect.DeepEqual(prior.Audit, extended.Audit[:len(prior.Audit)]) || extended.Audit[len(extended.Audit)-1].PreviousWindow == nil || extended.Audit[len(extended.Audit)-1].PreviousWindow.PhaseDeadlineMillis != prior.PhaseDeadlineMillis || extended.Audit[len(extended.Audit)-1].PreviousWindow.OverallDeadlineMillis != prior.OverallDeadlineMillis {
					t.Fatal("fresh completion extension rewrote expired history", extended)
				}
				if operation == "link" {
					state := h.s.Status()
					if err := h.s.LinkCommittedMaintenance(*state.Maintenance.Receipt, state.Generation); err != nil {
						t.Fatal(err)
					}
				}
				if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
					t.Fatal("fresh extension did not reconcile original restoration requests", err)
				}
				finished := h.s.Status()
				if finished.Bounded.Outcome != "completed" || a.archive.Preparation.Prepared || a.archive.Hosts[0].Control.Mode != protocol.RunnerNormal || a.archive.Hosts[0].OpenRuns != 4 || h.p.starts != 1 || h.p.stops != 1 {
					t.Fatal("completion did not restore admission while retaining original work", finished)
				}
				seen := map[string]boundedHistoryControl{}
				for _, control := range a.controls {
					if old, ok := seen[control.request]; ok && old != control {
						t.Fatal("restoration replaced the original request or CAS", old, control)
					}
					seen[control.request] = control
				}
				if _, ok := seen[p.HostControls[0].RestoreRequestID]; !ok {
					t.Fatal("original host restoration was not reconciled")
				}
				if _, ok := seen[p.PreparationCancelRequestID]; !ok {
					t.Fatal("original preparation release was not reconciled")
				}
			})
		}
	}
}
