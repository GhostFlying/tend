//go:build linux

package updater

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func restartedMaintenanceFixture(t *testing.T) (*harness, protocol.CommittedMaintenancePlan, State) {
	t.Helper()
	h, p, a := committedFixture(t)
	ctx := t.Context()
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	for range 7 {
		h.step(t)
	}
	committed := h.s.Status()
	if committed.Maintenance.Receipt == nil {
		t.Fatal("missing original committed receipt")
	}
	if err := h.s.LinkCommittedMaintenance(*committed.Maintenance.Receipt, committed.Generation); err != nil {
		t.Fatal(err)
	}
	// Socket authority responses are independent snapshots. Detach the fake's
	// shared slices before finish changes its live host controls.
	a.archive = *h.s.Status().Maintenance.ArchiveAuthorization
	if err := h.s.FinishCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	historical := h.s.Status()
	for range 2 {
		delete(h.p.live, h.s.Status().Process.PID)
		h.step(t)
		h.until(t, "committed")
	}
	h.reopen(t)
	current := h.s.Status()
	if current.Process.LaunchID == historical.Process.LaunchID || current.CommittedProof.Binding != historical.CommittedProof.Binding || current.CommittedProof.Process != current.Process || !reflect.DeepEqual(current.Maintenance, historical.Maintenance) {
		t.Fatalf("verified restart identity: historical=%+v current=%+v proof=%+v binding_preserved=%v maintenance_preserved=%v", historical.Process, current.Process, current.CommittedProof, current.CommittedProof.Binding == historical.CommittedProof.Binding, reflect.DeepEqual(current.Maintenance, historical.Maintenance))
	}
	dir := makeBundle(t, []byte("independent server after two verified restarts"))
	b, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest := b.Manifest
	manifest.Artifacts[0].Role = "server"
	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	b, err = release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p.MaintenanceID = "after-verified-restarts"
	p.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF1"
	p.LinkRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF2"
	p.PreparationRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF3"
	p.PreparationCancelRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF4"
	p.PreparationRevision = a.archive.Preparation.Revision + 1
	p.HostControls = []protocol.MaintenanceHostControl{{HostID: "fixture-host", Revision: a.archive.Hosts[0].Control.Revision, PreviousMode: "normal", RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF5", RestoreRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF6"}}
	// Match the ordinary planner's defaults: the binding is historical, while
	// PreviousProcess identifies the supervisor's current verified incarnation.
	p.Previous = current.CommittedProof.Binding
	p.PreviousProcess = current.Process
	p.ExpectedGeneration = current.Generation
	p.StateSHA256 = digest(current)
	p.ReleaseDir = dir
	p.Successor = Binding{UpgradeID: p.MaintenanceID, InstanceID: "server", ProcessInstanceID: "after-restart-successor", ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.Manifest.Artifacts[0].SHA256}
	return h, p, historical
}

func TestCommittedMaintenanceAfterVerifiedRestarts(t *testing.T) {
	h, p, historical := restartedMaintenanceFixture(t)
	before := h.s.Status()
	starts, stops := h.p.starts, h.p.stops
	if err := h.s.validateCommittedMaintenance(t.Context(), p, true); err != nil {
		t.Fatal("ordinary plan rejected the verified current process", err)
	}
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	after := h.s.Status()
	old := after.MaintenanceHistory[historical.Maintenance.Plan.MaintenanceID]
	if !reflect.DeepEqual(old, *historical.Maintenance) || after.Process != before.Process || after.Current != before.Current || !reflect.DeepEqual(after.CommittedProof, before.CommittedProof) || after.Maintenance.OriginalProof != *before.CommittedProof || h.p.starts != starts || h.p.stops != stops {
		t.Fatal("adoption rewrote historical facts or replaced the running service")
	}
}

func TestCommittedMaintenanceAfterRestartRejectsChangedIdentity(t *testing.T) {
	for _, mode := range []string{"rewritten-binding", "previous-process", "proof-process", "live-process", "duplicate-launch", "old-launch-successor", "current-launch-successor"} {
		t.Run(mode, func(t *testing.T) {
			h, p, _ := restartedMaintenanceFixture(t)
			switch mode {
			case "rewritten-binding":
				p.Previous.ProcessInstanceID = p.PreviousProcess.LaunchID
			case "previous-process":
				p.PreviousProcess.StartTicks++
			case "proof-process":
				h.s.state.CommittedProof.Process.StartTicks++
			case "live-process":
				id := h.p.live[p.PreviousProcess.PID]
				id.StartTicks++
				h.p.live[id.PID] = id
			case "duplicate-launch":
				id := h.p.live[p.PreviousProcess.PID]
				id.PID++
				h.p.live[id.PID] = id
			case "old-launch-successor":
				p.Successor.ProcessInstanceID = p.Previous.ProcessInstanceID
			case "current-launch-successor":
				p.Successor.ProcessInstanceID = p.PreviousProcess.LaunchID
			}
			if err := h.s.validateCommittedMaintenance(t.Context(), p, true); err == nil {
				t.Fatal("changed or ambiguous source identity accepted")
			}
		})
	}
}

func TestCommittedMaintenanceFailedParentRetainsLaunchBinding(t *testing.T) {
	h, p, a := committedFixture(t)
	ctx := context.Background()
	if err := h.s.PrepareCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	if err := h.s.AuthorizeCommittedMaintenance(ctx, p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.unready = true
	for range 7 {
		_ = h.s.Step(ctx)
	}
	parent := h.s.state.Maintenance
	if parent.Phase != "checking_successor" {
		t.Fatal("missing failed parent")
	}
	parent.Process.LaunchID = "not-the-authorized-parent-launch"
	next := p
	next.MaintenanceID = "failed-parent-launch-mismatch"
	next.RequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF7"
	next.LinkRequestID = "req_01M6F01JSS6Q9BFV6DJNSSBHF8"
	next.ParentMaintenanceID = p.MaintenanceID
	next.ParentPlan = &p
	next.ParentPlanSHA256 = digest(p)
	next.ParentArchiveSHA256 = digest(parent.ArchiveAuthorization)
	next.ParentGeneration = h.s.state.Generation
	next.ExpectedGeneration = h.s.state.Generation
	next.StateSHA256 = digest(h.s.state)
	next.Previous = p.Successor
	next.PreviousProcess = parent.Process
	if err := h.s.validateCommittedMaintenance(ctx, next, true); err == nil || !strings.Contains(err.Error(), "exact maintenance process binding required") {
		t.Fatal("failed parent lost its original launch binding", err)
	}
}
