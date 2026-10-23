package upgradectl

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestCancelledOriginalBatchClearsLocalStagingEvenAfterNewBatch(t *testing.T) {
	c, f := testController(t)
	binding := protocol.UpgradeBinding{UpgradeID: "old-batch", InstanceID: c.cfg.Instance, ProcessInstanceID: "candidate", ReleaseID: "release", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: strings.Repeat("c", 64)}
	f.view.UpgradeID = "new-batch"
	f.batches = map[string]store.UpgradeRow{binding.UpgradeID: {View: protocol.UpgradeView{UpgradeID: binding.UpgradeID, Status: "cancelled"}, Manifest: release.Manifest{ReleaseID: binding.ReleaseID}, ManifestSHA256: binding.ManifestSHA256, ServerInstance: binding.InstanceID, ServerHash: binding.ArtifactSHA256}}
	state := updater.State{InstanceID: c.cfg.Instance, Current: updater.Version{Artifact: release.Artifact{SHA256: c.cfg.BinarySHA256}}, Process: updater.Identity{LaunchID: c.cfg.ProcessInstance, SHA256: c.cfg.BinarySHA256}, Operation: &updater.Operation{ID: binding.UpgradeID, Kind: "upgrade", Phase: "staged", Binding: binding}}
	var mu sync.Mutex
	attempts := 0
	ln, err := net.Listen("unix", c.cfg.UpdaterSocket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/cancel" {
			var req updater.ControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.ID != binding.UpgradeID || req.ExpectedPhase != "staged" {
				t.Errorf("unsafe cancellation: %+v", req)
			}
			attempts++
			if attempts == 1 {
				w.WriteHeader(503)
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{Error: "unavailable"})
				return
			}
			state.Operation.Phase = "cancelled"
			state.Terminals = map[string]updater.TerminalReceipt{binding.UpgradeID: {Binding: binding, Phase: "cancelled"}}
		} else if r.URL.Path != "/status" {
			t.Errorf("unexpected mutation %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); <-done }()
	if c.tick(context.Background()) == nil {
		t.Fatal("failed cancellation not surfaced")
	}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 || state.Operation.Phase != "cancelled" {
		t.Fatal(errors.New("staged intent not converged"))
	}
}

func TestCancelledHistoricalBatchGetsTombstoneWithoutLocalOperation(t *testing.T) {
	for _, which := range []string{"absent", "different-operation"} {
		t.Run(which, func(t *testing.T) {
			c, f := testController(t)
			f.batches = map[string]store.UpgradeRow{"old": {View: protocol.UpgradeView{UpgradeID: "old", Status: "cancelled"}}}
			state := updater.State{InstanceID: c.cfg.Instance}
			if which == "different-operation" {
				state.Operation = &updater.Operation{ID: "new", Kind: "upgrade", Phase: "checking"}
			}
			calls := 0
			var mu sync.Mutex
			ln, err := net.Listen("unix", c.cfg.UpdaterSocket)
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/cancel" {
					calls++
					var req updater.ControlRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.ID != "old" || req.ExpectedPhase != "staged" {
						t.Errorf("unsafe request %+v", req)
					}
					state.Terminals = map[string]updater.TerminalReceipt{"old": {Binding: updater.Binding{UpgradeID: "old", InstanceID: c.cfg.Instance}, Phase: "cancelled"}}
				} else if r.URL.Path != "/status" {
					t.Errorf("unexpected action %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})}
			done := make(chan struct{})
			go func() { defer close(done); _ = srv.Serve(ln) }()
			defer func() { _ = srv.Close(); <-done }()
			for i := 0; i < 2; i++ {
				if err := c.tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 1 {
				t.Fatalf("absence acknowledged or terminal repeatedly cancelled: %d", calls)
			}
			if which == "different-operation" && (state.Operation.ID != "new" || state.Operation.Phase != "checking") {
				t.Fatal("new operation touched")
			}
		})
	}
}
