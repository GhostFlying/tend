package upgradectl

import (
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestCancelledServerObservationRequiresActualSocketTerminalACK(t *testing.T) {
	for _, scenario := range []string{"staged_cancelled", "already_terminal", "cas_loses", "wrong_original"} {
		t.Run(scenario, func(t *testing.T) {
			c, backend := testController(t)
			binding := updater.Binding{UpgradeID: "original", InstanceID: c.cfg.Instance, ProcessInstanceID: "candidate", ReleaseID: "original-release", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64)}
			backend.view.UpgradeID, backend.view.Status = binding.UpgradeID, "cancelled"
			backend.batches = map[string]store.UpgradeRow{binding.UpgradeID: {View: backend.view, Manifest: release.Manifest{ReleaseID: binding.ReleaseID}, ManifestSHA256: binding.ManifestSHA256, ServerHash: binding.ArtifactSHA256, ServerInstance: binding.InstanceID, ServerProcess: binding.ProcessInstanceID}}
			row := backend.batches[binding.UpgradeID]
			row.View.ReleaseID = binding.ReleaseID
			backend.batches[binding.UpgradeID] = row
			state := updater.State{InstanceID: c.cfg.Instance, Current: updater.Version{Artifact: release.Artifact{SHA256: c.cfg.BinarySHA256}}, Process: updater.Identity{PID: 11, StartTicks: 1, LaunchID: c.cfg.ProcessInstance, SHA256: c.cfg.BinarySHA256}, Operation: &updater.Operation{ID: binding.UpgradeID, Kind: "upgrade", Phase: "staged", Binding: binding}}
			if scenario == "already_terminal" || scenario == "wrong_original" {
				terminal := binding
				if scenario == "wrong_original" {
					terminal.ManifestSHA256 = strings.Repeat("d", 64)
				}
				state.Operation = nil
				state.Terminals = map[string]updater.TerminalReceipt{binding.UpgradeID: {Binding: terminal, Phase: "cancelled"}}
			}
			listener, err := net.Listen("unix", c.cfg.UpdaterSocket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cancel" {
					var request updater.ControlRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.ID != binding.UpgradeID || request.ExpectedPhase != "staged" {
						t.Error("wrong original cancellation", request)
					}
					if scenario == "cas_loses" {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state, Error: "operation already started"})
						return
					}
					state.Operation = nil
					state.Terminals = map[string]updater.TerminalReceipt{binding.UpgradeID: {Binding: binding, Phase: "cancelled"}}
				} else if r.URL.Path != "/status" {
					t.Error("unexpected call", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})}
			done := make(chan struct{})
			go func() { defer close(done); _ = server.Serve(listener) }()
			defer func() { _ = server.Close(); <-done }()
			err = c.tick(t.Context())
			passed := scenario == "staged_cancelled" || scenario == "already_terminal"
			if (err == nil) != passed || slices.Contains(backend.observation.ServerCancellations, binding.UpgradeID) != passed {
				t.Fatal("server cancellation intent became an unproved ACK", scenario, backend.observation.ServerCancellations, err)
			}
		})
	}
}
