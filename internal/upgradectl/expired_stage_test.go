package upgradectl

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestExpiredLostServerGrantCancelsOnlyStillStagedCandidate(t *testing.T) {
	for _, scenario := range []string{"lost-grant", "cas-loses", "not-expired"} {
		t.Run(scenario, func(t *testing.T) {
			c, f := testController(t)
			c.cfg.Now = func() time.Time { return time.UnixMilli(1000) }
			binding := protocol.UpgradeBinding{UpgradeID: "batch", InstanceID: c.cfg.Instance, ProcessInstanceID: "candidate", ReleaseID: "release", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64)}
			f.view.Status = "attention"
			f.view.Phase = "server_ready"
			deadline := int64(999)
			if scenario == "not-expired" {
				deadline = 1001
			}
			f.batches = map[string]store.UpgradeRow{binding.UpgradeID: {View: f.view, Manifest: release.Manifest{ReleaseID: binding.ReleaseID}, ManifestSHA256: binding.ManifestSHA256, ServerInstance: binding.InstanceID, ServerHash: binding.ArtifactSHA256, ServerProcess: binding.ProcessInstanceID, ServerGrantDeadline: deadline}}
			state := updater.State{InstanceID: c.cfg.Instance, Current: updater.Version{Artifact: release.Artifact{SHA256: c.cfg.BinarySHA256}}, Process: updater.Identity{PID: 12, StartTicks: 2, LaunchID: c.cfg.ProcessInstance, SHA256: c.cfg.BinarySHA256}, Operation: &updater.Operation{ID: binding.UpgradeID, Kind: "upgrade", Phase: "staged", Binding: binding}}
			var mu sync.Mutex
			cancelCalls := 0
			ln, err := net.Listen("unix", c.cfg.UpdaterSocket)
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/cancel":
					cancelCalls++
					var req updater.ControlRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.ID != binding.UpgradeID || req.ExpectedPhase != "staged" {
						t.Errorf("unsafe CAS %+v", req)
					}
					if scenario == "cas-loses" {
						state.Operation.Phase = "stopping"
						w.WriteHeader(409)
						_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state, Error: "phase changed"})
						return
					}
					state.Operation.Phase = "cancelled"
					state.Terminals = map[string]updater.TerminalReceipt{binding.UpgradeID: {Binding: binding, Phase: "cancelled"}}
				case "/status":
				default:
					t.Errorf("unexpected call %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})}
			done := make(chan struct{})
			go func() { defer close(done); _ = srv.Serve(ln) }()
			defer func() { _ = srv.Close(); <-done }()
			err = c.tick(context.Background())
			if (err != nil) != (scenario == "cas-loses") {
				t.Fatalf("unexpected tick result %v", err)
			}
			if scenario == "lost-grant" {
				receipt := f.observation.UpdaterReceipt
				if receipt == nil || receipt.Phase != "cancelled" || receipt.Binding != binding || receipt.RollbackConfirmed || receipt.CommittedConfirmed {
					t.Fatalf("missing current batch no-start proof %+v", receipt)
				}
			} else if f.observation.UpdaterReceipt != nil {
				t.Fatal("unsafe no-start observation")
			}
			mu.Lock()
			if cancelCalls != map[string]int{"lost-grant": 1, "cas-loses": 1, "not-expired": 0}[scenario] {
				t.Fatal("incorrect cancellation boundary")
			}
			mu.Unlock()
			if scenario == "lost-grant" {
				// Controller/process restart after CAS still reconstructs the bound proof.
				mu.Lock()
				state.Operation = &updater.Operation{ID: "restart", Kind: "restart", Phase: "committed"}
				state.Process.LaunchID = "successor"
				mu.Unlock()
				c.cfg.ProcessInstance = "successor"
				if err := c.tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				if receipt := f.observation.UpdaterReceipt; receipt == nil || receipt.Binding != binding || receipt.CurrentProcessInstanceID != "successor" {
					t.Fatal("restart lost no-start proof")
				}
			}
		})
	}
}
