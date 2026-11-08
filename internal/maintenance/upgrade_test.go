package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

func upgradeServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tend-maint-upgrade-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })
	return socket
}

func TestFault_MaintenanceUpgradeRejectedVersusUnknown(t *testing.T) {
	for _, mode := range []string{"rejected", "lost-reply", "accepted-error"} {
		t.Run(mode, func(t *testing.T) {
			c, n, cfg, opts := fixture(t)
			launch(t, c)
			original := c.Status().Active.SessionID
			var posts atomic.Int64
			cfg.InstanceID = "maintainer"
			cfg.UpdaterSocket = upgradeServer(t, func(w http.ResponseWriter, r *http.Request) {
				state := updater.State{InstanceID: cfg.InstanceID}
				if mode == "accepted-error" {
					state.Operation = &updater.Operation{ID: "install", Phase: "staged"}
				}
				if r.Method == http.MethodPost {
					posts.Add(1)
					if mode == "lost-reply" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
						return
					}
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state, Error: "installation not accepted"})
					return
				}
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})
			c.cfg = cfg
			c.state.Upgrade = &protocol.MaintenanceUpgrade{ID: "install", Phase: "requested"}
			if err := c.Step(context.Background()); err == nil {
				t.Fatal("failure was hidden")
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			c.lock = nil
			d, err := Open(context.Background(), cfg, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if mode == "rejected" {
				if d.frozen() || d.Status().Upgrade.Phase != "rejected" {
					t.Fatal("definite rejection remained frozen")
				}
				command(t, d, "audit", protocol.MaintenanceRequest{})
			} else {
				if !d.frozen() || d.Status().Upgrade.Phase != "submitting" {
					t.Fatal("unknown acceptance was reset")
				}
				_ = d.Step(context.Background())
				if !d.frozen() {
					t.Fatal("unknown installation reopened work")
				}
			}
			if posts.Load() != 1 || d.Status().Upgrade.ID != "install" || d.Status().Active.SessionID != original || n.started != 1 {
				t.Fatal("recovery changed original installation or native execution")
			}
		})
	}
}

func TestFault_MaintenanceUpgradeRetainedCommitAfterSupervisedRestart(t *testing.T) {
	for _, mismatch := range []string{"", "binding", "process", "artifact"} {
		t.Run("mismatch="+mismatch, func(t *testing.T) {
			c, _, _, _ := fixture(t)
			b := protocol.UpgradeBinding{UpgradeID: "install", InstanceID: "maintainer", ProcessInstanceID: "original-candidate", ReleaseID: "release", ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64)}
			version := updater.Version{ReleaseID: b.ReleaseID, ManifestSHA256: b.ManifestSHA256, Artifact: release.Artifact{SHA256: b.ArtifactSHA256}}
			process := updater.Identity{LaunchID: "supervised-successor", SHA256: b.ArtifactSHA256}
			proof := updater.CommittedProof{Binding: b, Version: version, Process: process}
			switch mismatch {
			case "binding":
				proof.Binding.UpgradeID = "different"
			case "process":
				proof.Process.LaunchID = "stale-process"
			case "artifact":
				proof.Version.Artifact.SHA256 = strings.Repeat("c", 64)
			}
			state := updater.State{InstanceID: b.InstanceID, Current: version, Process: process, Operation: &updater.Operation{ID: "restart-successor", Kind: "restart", Phase: "committed"}, Terminals: map[string]updater.TerminalReceipt{b.UpgradeID: {Binding: b, Phase: "committed"}}, CommittedProof: &proof}
			c.cfg.InstanceID = b.InstanceID
			c.cfg.UpdaterSocket = upgradeServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})
			c.opts.RuntimeID, c.opts.ArtifactSHA256 = process.LaunchID, process.SHA256
			c.state.Upgrade = &protocol.MaintenanceUpgrade{ID: b.UpgradeID, ReleaseID: b.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.ArtifactSHA256, Phase: "submitting", Binding: &b}
			err := c.Step(context.Background())
			if mismatch == "" {
				if err != nil || c.frozen() || c.Status().Upgrade.Phase != "completed" {
					t.Fatal("matching retained receipt did not restore admission", err)
				}
			} else if err == nil || !c.frozen() {
				t.Fatal("mismatched retained proof reopened admission", err)
			}
		})
	}
}

func TestFault_MaintenanceUnknownUpgradeAcceptsOriginalCancellationTombstone(t *testing.T) {
	c, _, _, _ := fixture(t)
	c.cfg.InstanceID = "maintainer"
	c.opts.RuntimeID, c.opts.ArtifactSHA256 = "running-original", strings.Repeat("b", 64)
	b := protocol.UpgradeBinding{InstanceID: c.cfg.InstanceID, UpgradeID: "install"}
	state := updater.State{InstanceID: c.cfg.InstanceID, Process: updater.Identity{LaunchID: c.opts.RuntimeID, SHA256: c.opts.ArtifactSHA256}, Current: updater.Version{Artifact: release.Artifact{SHA256: c.opts.ArtifactSHA256}}, Terminals: map[string]updater.TerminalReceipt{"install": {Binding: b, Phase: "cancelled"}}}
	c.cfg.UpdaterSocket = upgradeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})
	c.state.Upgrade = &protocol.MaintenanceUpgrade{ID: "install", Phase: "submitting"}
	step(t, c)
	if c.frozen() || c.Status().Upgrade.Phase != "cancelled" {
		t.Fatal("durable cancellation of the original unknown submission was ignored")
	}
}

func TestFault_MaintenanceRetainedRollbackAfterSupervisedRestart(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint("stale=", stale), func(t *testing.T) {
			c, _, _, _ := fixture(t)
			b := protocol.UpgradeBinding{UpgradeID: "install", InstanceID: "maintainer", ProcessInstanceID: "candidate", ReleaseID: "candidate-release", ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64)}
			previous := updater.Version{ReleaseID: "previous", ManifestSHA256: strings.Repeat("d", 64), Artifact: release.Artifact{SHA256: strings.Repeat("c", 64)}}
			process := updater.Identity{LaunchID: "supervised-rollback-successor", SHA256: previous.Artifact.SHA256}
			proof := updater.RollbackProof{Binding: b, Version: previous, Process: process}
			if stale {
				proof.Process.LaunchID = "stale-rollback"
			}
			state := updater.State{InstanceID: b.InstanceID, Current: previous, Process: process, Operation: &updater.Operation{ID: "restart-successor", Kind: "restart", Phase: "committed"}, Terminals: map[string]updater.TerminalReceipt{b.UpgradeID: {Binding: b, Phase: "rolled_back"}}, RollbackProof: &proof}
			c.cfg.InstanceID = b.InstanceID
			c.cfg.UpdaterSocket = upgradeServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
			})
			c.opts.RuntimeID, c.opts.ArtifactSHA256 = process.LaunchID, process.SHA256
			c.state.Upgrade = &protocol.MaintenanceUpgrade{ID: b.UpgradeID, ReleaseID: b.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.ArtifactSHA256, Phase: "submitting", Binding: &b}
			err := c.Step(context.Background())
			if stale {
				if err == nil || !c.frozen() {
					t.Fatal("stale rollback proof admitted a different process")
				}
			} else if err != nil || c.frozen() || c.Status().Upgrade.Phase != "rolled_back" {
				t.Fatal("matching retained rollback remained frozen", err)
			}
		})
	}
}

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
