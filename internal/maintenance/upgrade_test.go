package maintenance

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestFault_MaintenanceUpgradeRetainsNativeAndFencesAdmission(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	launch(t, c)
	active := c.Status().Active
	b := protocol.UpgradeBinding{UpgradeID: "install", InstanceID: "maintenance", ProcessInstanceID: "candidate", ReleaseID: "release", ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64)}
	c.cfg.InstanceID = b.InstanceID
	c.cfg.UpdaterSocket = filepath.Join(t.TempDir(), "control.sock")
	c.state.Upgrade = &protocol.MaintenanceUpgrade{ID: b.UpgradeID, ReleaseID: b.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.ArtifactSHA256, Phase: "submitting"}
	grant, err := c.authorizeUpgrade(context.Background(), b)
	if err != nil || !grant.Allowed {
		t.Fatal(grant, err)
	}
	if _, err := c.Command(context.Background(), "audit", protocol.MaintenanceRequest{RequestID: "late", ExpectedRevision: c.Status().Revision}); err == nil {
		t.Fatal("accepted new work while installation owns state")
	}
	if _, err := c.upgradeHealth(b); err == nil {
		t.Fatal("old process reported candidate health")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	cfg.InstanceID = b.InstanceID
	cfg.UpdaterSocket = c.cfg.UpdaterSocket
	opts.RuntimeID = b.ProcessInstanceID
	opts.ArtifactSHA256 = b.ArtifactSHA256
	d, err := Open(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	health, err := d.upgradeHealth(b)
	if err != nil || !health.Healthy || !health.AdmissionFenced || health.NewWorkAccepted {
		t.Fatal(health, err)
	}
	wrong := b
	wrong.ArtifactSHA256 = strings.Repeat("c", 64)
	if _, err := d.upgradeHealth(wrong); err == nil {
		t.Fatal("wrong binding accepted")
	}
	listener, err := net.Listen("unix", cfg.UpdaterSocket)
	if err != nil {
		t.Fatal(err)
	}
	state := updater.State{InstanceID: b.InstanceID, Process: updater.Identity{LaunchID: b.ProcessInstanceID, SHA256: b.ArtifactSHA256}, Operation: &updater.Operation{ID: b.UpgradeID, Binding: b, Phase: "checking", Candidate: updater.Version{ManifestSHA256: b.ManifestSHA256, Artifact: release.Artifact{SHA256: b.ArtifactSHA256}}}}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close(); <-done }()
	step(t, d)
	if !d.frozen() {
		t.Fatal("candidate admitted work before commit")
	}
	// Stop the handler before changing its snapshot to keep the test race-free.
	_ = srv.Close()
	<-done
	state.Operation.Phase = "committed"
	listener, err = net.Listen("unix", cfg.UpdaterSocket)
	if err != nil {
		t.Fatal(err)
	}
	srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})}
	done = make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(listener) }()
	step(t, d)
	if d.frozen() {
		t.Fatal("committed candidate did not reopen admission")
	}
	if d.Status().Active.SessionID != active.SessionID || n.started != 1 || n.created != 1 {
		t.Fatal("maintenance update duplicated native work")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "maintenance.db")); err != nil {
		t.Fatal(err)
	}
}
