package serverkit

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestDeploymentExecutionEntryUsesOnlyConfiguredRemoteRoute(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("controlled local upgrade configuration requires Linux")
	}
	root := t.TempDir()
	cfg := &UpgradeConfig{Instance: "logical-server-identity", UpdaterSocket: filepath.Join(root, "updater.sock"), ExecutionEntry: &protocol.DeploymentExecutionEntry{Transport: "tailscale_ssh", Target: "service.example.invalid", User: "operator", CLIPath: filepath.Join(root, "controlled-cli")}}
	if err := prepareUpgradeConfig(cfg, root); err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutionEntry.ControlSocket != cfg.ControlSocket || cfg.ExecutionEntry.Target == cfg.Instance {
		t.Fatal("operator entry inferred the logical host or a separate control socket", cfg)
	}
	if strings.Contains(cfg.ExecutionEntry.CLIPath, cfg.UpdaterSocket) {
		t.Fatal("operator CLI exposed updater-private state")
	}
	for _, entry := range []protocol.DeploymentExecutionEntry{
		{Transport: "tailscale_ssh", CLIPath: cfg.ExecutionEntry.CLIPath},
		{Transport: "ssh", Target: "service.example.invalid", CLIPath: cfg.ExecutionEntry.CLIPath},
		{Transport: "guess", Target: cfg.Instance, User: "operator", CLIPath: cfg.ExecutionEntry.CLIPath},
		{Transport: "local", CLIPath: ""},
		{Transport: "local", Target: cfg.Instance, CLIPath: cfg.ExecutionEntry.CLIPath},
	} {
		invalid := *cfg
		invalid.ExecutionEntry = &entry
		if err := prepareUpgradeConfig(&invalid, root); err == nil {
			t.Fatal("ambiguous execution entry was accepted", entry)
		}
	}
}

func TestConfiguredDeploymentObservationMatchesFixedInstanceAndOmitsPrivateState(t *testing.T) {
	dir, err := os.MkdirTemp("", "tend-observe-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "supervisor.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	hash, manifest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	state := updater.State{InstanceID: "fixed-service", Current: updater.Version{ReleaseID: "original-release", ManifestSHA256: manifest, Artifact: release.Artifact{SHA256: hash, Path: "PRIVATE_INSTALLATION_PATH"}}, Process: updater.Identity{PID: 123, StartTicks: 42, LaunchID: "current-process", SHA256: hash}}
	state.CommittedProof = &updater.CommittedProof{Version: state.Current, Process: state.Process, Binding: protocol.UpgradeBinding{UpgradeID: "original-batch", ReleaseID: "original-release", ManifestSHA256: manifest, ArtifactSHA256: hash, InstanceID: "fixed-service", ProcessInstanceID: "original-process"}}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/status" {
			t.Error("observation performed an updater mutation", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: state})
	})}
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = server.Serve(ln) }()
	defer func() { _ = server.Close(); <-stopped }()
	actual := protocol.ObservedServer{PID: 123, ProcessInstanceID: "current-process", BinarySHA256: hash}
	for _, instance := range []string{"fixed-service", "another-service"} {
		d, err := configuredDeploymentObservation(t.Context(), actual, socket, instance)
		if err != nil || d.Preparation == nil || d.Preparation.ServerInstance != instance {
			t.Fatalf("fixed configured target unavailable: %+v %v", d, err)
		}
		if instance == "fixed-service" {
			if d.ProofStatus != "confirmed" || d.CommittedProof == nil || d.CommittedProof.CurrentProcessInstanceID != "current-process" {
				t.Fatal("actual installed successor proof lost", d)
			}
		} else if d.ProofStatus != "mismatch" || d.CommittedProof != nil {
			t.Fatal("another updater instance became the configured target proof", d)
		}
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), "PRIVATE_") || strings.Contains(string(raw), socket) {
			t.Fatalf("private updater state leaked: %s", raw)
		}
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
