package maintenance

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
	"github.com/gorilla/websocket"
)

func TestFault_MaintenanceOfflineAuditDrivesCompletedBackendSwitch(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	launch(t, c)
	n.complete, n.busy = true, false
	backend := cfg.Backend
	backend.Agent = "traex"
	command(t, c, "config", protocol.MaintenanceRequest{Backend: &backend, Mode: "when-idle"})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	if err := Audit(context.Background(), cfg, opts); err != nil {
		t.Fatal(err)
	}
	d, err := Open(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := d.Status()
	if s.Active == nil || s.Active.Backend.Agent != "traex" || s.Active.InputID == "" {
		t.Fatal("audit did not dispatch on the adopted backend", s.Active)
	}
	if n.started != 2 {
		t.Errorf("audit returned success without dispatching its inspection")
	}
}
func TestFault_MaintenanceCachedCreationConnectionReconnect(t *testing.T) {
	dir, err := os.MkdirTemp("", "maint-reconnect-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "n.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var connections atomic.Int64
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections.Add(1)
		for {
			var req struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
			}
			if conn.ReadJSON(&req) != nil {
				return
			}
			if req.ID == nil {
				continue
			}
			var result any = map[string]any{}
			if req.Method == "thread/start" {
				result = map[string]any{"thread": map[string]any{"id": "original"}, "sandbox": map[string]any{"type": "workspaceWrite"}}
			}
			if req.Method == "thread/read" {
				result = map[string]any{"thread": map[string]any{"status": map[string]string{"type": "idle"}}}
			}
			if conn.WriteJSON(map[string]any{"id": req.ID, "result": result}) != nil {
				return
			}
			if req.Method == "thread/start" {
				return
			}
		}
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); <-done }()
	b := protocol.MaintenanceBackend{Agent: "codex", Socket: socket, Sandbox: "workspace-write", ApprovalPolicy: "never"}
	n := &NativeClient{}
	defer n.Close()
	thread, err := n.NewThread(context.Background(), b, dir, "bounded inspection")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = n.Read(context.Background(), b, thread.ID, ReadMetadata)
	if _, err = n.Read(context.Background(), b, thread.ID, ReadMetadata); err != nil {
		t.Fatal("read did not reconnect to original native thread", err)
	}
	if connections.Load() == 1 {
		t.Errorf("all future reads keep the dead creation connection instead of reconnecting to the healthy daemon")
	}
}

type gatedMaintenanceAuthority struct {
	c              *Controller
	entered, allow chan struct{}
}

func (a *gatedMaintenanceAuthority) Authorize(ctx context.Context, b updater.Binding) (updater.Grant, error) {
	close(a.entered)
	select {
	case <-a.allow:
	case <-ctx.Done():
		return updater.Grant{}, ctx.Err()
	}
	return a.c.authorizeUpgrade(ctx, b)
}
func (a *gatedMaintenanceAuthority) Health(_ context.Context, b updater.Binding) (updater.Health, error) {
	return a.c.upgradeHealth(b)
}
func TestFault_MaintenanceUpdaterCallbackCannotDeadlockStatus(t *testing.T) {
	c, _, _, _ := fixture(t)
	b := protocol.UpgradeBinding{UpgradeID: "install", InstanceID: "maintainer", ProcessInstanceID: "candidate", ReleaseID: "candidate-release", ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64)}
	c.cfg.InstanceID = b.InstanceID
	c.state.Upgrade = &protocol.MaintenanceUpgrade{ID: b.UpgradeID, ReleaseID: b.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.ArtifactSHA256, Phase: "submitting"}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	initial := filepath.Join(root, "old")
	if err := os.WriteFile(initial, []byte("old executable"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := updater.Version{Artifact: release.Artifact{SHA256: strings.Repeat("c", 64)}}
	candidate := updater.Version{ReleaseID: b.ReleaseID, ManifestSHA256: b.ManifestSHA256, Artifact: release.Artifact{SHA256: b.ArtifactSHA256}}
	state := updater.State{Format: 1, InstanceID: b.InstanceID, Current: previous, Operation: &updater.Operation{ID: b.UpgradeID, Kind: "upgrade", Phase: "staged", Binding: b, Previous: previous, Candidate: candidate}}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(root, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	a := &gatedMaintenanceAuthority{c: c, entered: make(chan struct{}), allow: make(chan struct{})}
	s, err := updater.Open(updater.Config{InstanceID: b.InstanceID, Role: "maintainer", StateDir: root, InitialBinary: initial, LogPath: filepath.Join(root, "log")}, updater.Options{Authority: a, Now: c.opts.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	statusEntered := make(chan struct{})
	c.cfg.UpdaterSocket = upgradeServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(statusEntered)
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: s.Status()})
	})
	updDone := make(chan error, 1)
	go func() { updDone <- s.Step(context.Background()) }()
	<-a.entered
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	maintDone := make(chan error, 1)
	go func() { maintDone <- c.Step(ctx) }()
	<-statusEntered
	close(a.allow)
	select {
	case err := <-maintDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("updater status and authorization callback waited on each other", ctx.Err())
	}
	if err := <-updDone; err != nil {
		t.Fatal(err)
	}
	if c.Status().Upgrade.Binding == nil {
		t.Fatal("callback did not persist the original binding")
	}
}
