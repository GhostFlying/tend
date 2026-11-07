//go:build linux

package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func unfinishedMaintenanceFixture(t *testing.T) (*harness, protocol.CommittedMaintenancePlan, *committedTestAuthority, Operation) {
	t.Helper()
	h, p, a := committedFixture(t)
	op := Operation{ID: p.Previous.UpgradeID, Kind: "upgrade", Phase: "checking", Binding: p.Previous, Candidate: h.s.state.Current, CandidateProcess: p.PreviousProcess, LaunchID: p.PreviousProcess.LaunchID, HealthStartedAt: h.now}
	h.s.state.Operation = &op
	h.s.state.Maintenance = nil
	h.s.state.CommittedProof = nil
	h.s.state.Current = Version{Protocol: 1, StorageVersion: 26}
	p.SourceOperation = &protocol.MaintenanceOperationSource{OperationID: op.ID, Kind: op.Kind, Phase: op.Phase, OperationSHA256: digest(op), LaunchID: op.LaunchID, Protocol: op.Candidate.Protocol, StorageVersion: op.Candidate.StorageVersion}
	p.StateSHA256, p.ExpectedGeneration = digest(h.s.state), h.s.state.Generation
	return h, p, a, op
}

func TestUncommittedOperationCompatibilityUsesMigratedSource(t *testing.T) {
	h, p, _, _ := unfinishedMaintenanceFixture(t)
	h.s.state.Operation.Candidate.Protocol = 2
	h.s.state.Operation.Candidate.StorageVersion = 27
	source, binding, err := OperationMaintenanceSource(h.s.state)
	if err != nil {
		t.Fatal(err)
	}
	p.SourceOperation, p.Previous = source, binding
	path := filepath.Join(p.ReleaseDir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest release.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Protocol = release.Range{Min: 2, Max: 2}
	manifest.Storage.ServerSchema = release.Range{Min: 27, Max: 27}
	manifest.ReleaseID = "migrated-compatible"
	p.Successor.ReleaseID = manifest.ReleaseID
	raw, _ = json.Marshal(manifest)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	bundle, err := release.Load(p.ReleaseDir)
	if err != nil {
		t.Fatal(err)
	}
	p.Successor.ManifestSHA256 = bundle.ManifestSHA256
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal("actual migrated source was incorrectly replaced by old Current schema", err)
	}
}

func TestUncommittedOperationForceCompletesWithoutResumingOldAttempt(t *testing.T) {
	h, p, a, old := unfinishedMaintenanceFixture(t)
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHF1", ExpectedGeneration: h.s.Status().Generation, ExpectedPhase: "updater_adopted", PlanSHA256: digest(p), Role: "server", DataIdentity: boundedServerTestData(h), Source: p.PreviousProcess, Target: p.Successor, Reason: "independent replacement of unconfirmed candidate", ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli(), Timeouts: protocol.DefaultMaintenanceTimeoutPolicy()}
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force"); err != nil {
		t.Fatal(err)
	}
	for range 7 {
		h.step(t)
	}
	st := h.s.Status()
	if st.Format != 5 || st.Operation != nil || digest(st.OperationHistory[old.ID]) != digest(old) || st.Maintenance.Phase != "successor_committed" || st.Bounded.SourceExit == nil || st.Bounded.SourceExit.Process != p.PreviousProcess || st.Maintenance.Receipt == nil || a.archive.HostedDrained || h.p.starts != 1 || h.p.stops != 1 {
		t.Fatal("replacement did not preserve original attempt or stop/start exactly once", st.Maintenance)
	}
	if err := ValidateOperationMaintenanceSource(st, p); err != nil {
		t.Fatal("startup cannot verify transferred ownership", err)
	}
	if err := h.s.LinkCommittedMaintenance(*st.Maintenance.Receipt, st.Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FinishCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Step(t.Context()); err != nil {
		t.Fatal("completed maintenance fell back into old checking", err)
	}
	if h.s.Status().Operation != nil || h.p.starts != 1 || h.p.stops != 1 {
		t.Fatal("old operation restarted after linkage")
	}
}

func TestUncommittedOperationCanBeIndependentlyAdoptedWithoutForgingCommit(t *testing.T) {
	h, p, _, original := unfinishedMaintenanceFixture(t)
	current := h.s.state.Current
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal("unconfirmed migrated candidate cannot enter independent recovery", err)
	}
	st := h.s.Status()
	if st.Operation != nil || digest(st.OperationHistory[original.ID]) != digest(original) || st.Current != current || st.CommittedProof != nil || st.Format != 5 {
		t.Fatal("adoption forged or retained an active predecessor", st)
	}
	if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err != nil {
		t.Fatal("original adoption replay", err)
	}
}

func TestUncommittedOperationRejectsDriftAndMissingPhysicalProof(t *testing.T) {
	for _, change := range []string{"operation", "launch", "process", "schema", "find_zero"} {
		t.Run(change, func(t *testing.T) {
			h, p, _, _ := unfinishedMaintenanceFixture(t)
			switch change {
			case "operation":
				p.SourceOperation.OperationSHA256 = p.Successor.ArtifactSHA256
			case "launch":
				p.SourceOperation.LaunchID = "unrelated-launch"
			case "process":
				p.PreviousProcess.StartTicks++
			case "schema":
				p.SourceOperation.StorageVersion--
			case "find_zero":
				delete(h.p.live, p.PreviousProcess.PID)
				p.PreviousProcess = Identity{}
				p.FailedProcessExited = true
			}
			if err := h.s.AdoptCommittedMaintenance(t.Context(), p); err == nil {
				t.Fatal("unproved source authorized", change)
			}
		})
	}
}
