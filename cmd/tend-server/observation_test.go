package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestObservedDeploymentProofRequiresActualProcess(t *testing.T) {
	hash := strings.Repeat("a", 64)
	manifest := strings.Repeat("b", 64)
	state := updater.State{InstanceID: "server", Current: updater.Version{ReleaseID: "server-original", ManifestSHA256: manifest, Artifact: release.Artifact{SHA256: hash}}, Process: updater.Identity{PID: 123, StartTicks: 42, LaunchID: "actual", SHA256: hash}}
	state.CommittedProof = &updater.CommittedProof{Version: state.Current, Process: state.Process, Binding: protocol.UpgradeBinding{UpgradeID: "batch-original", ReleaseID: state.Current.ReleaseID, ManifestSHA256: manifest, ArtifactSHA256: hash, InstanceID: "server", ProcessInstanceID: "actual"}}
	actual := protocol.ObservedServer{PID: 123, ProcessInstanceID: "actual", BinarySHA256: hash}
	if v := observedDeploymentProof(actual, state); v.ProofStatus != "confirmed" || v.CommittedProof == nil || v.CommittedProof.Binding.ReleaseID != "server-original" {
		t.Fatalf("valid proof: %+v", v)
	}
	for _, kind := range []string{"pid", "hash", "process"} {
		t.Run(kind, func(t *testing.T) {
			changed := actual
			switch kind {
			case "pid":
				changed.PID++
			case "hash":
				changed.BinarySHA256 = strings.Repeat("c", 64)
			case "process":
				changed.ProcessInstanceID = "another"
			}
			v := observedDeploymentProof(changed, state)
			if v.ProofStatus != "mismatch" || v.CommittedProof != nil || *v.ActualServer != changed {
				t.Fatalf("mismatch exposed proof: %+v", v)
			}
		})
	}
	state.CommittedProof = nil
	if v := observedDeploymentProof(actual, state); v.ProofStatus != "unconfirmed" || v.CommittedProof != nil {
		t.Fatal(v)
	}
}

func TestDeploymentObservationUnavailable(t *testing.T) {
	actual := protocol.ObservedServer{PID: 123, BinarySHA256: strings.Repeat("a", 64), ProcessInstanceID: "actual"}
	for _, socket := range []string{"", filepath.Join(t.TempDir(), "absent.sock")} {
		got, err := readDeploymentObservation(context.Background(), actual, socket)
		if err != nil || got.ProofStatus != "unavailable" || got.CommittedProof != nil || *got.ActualServer != actual {
			t.Fatalf("unavailable: %+v %v", got, err)
		}
	}
}
