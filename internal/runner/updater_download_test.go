package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReleaseDownloadRequiresSameOriginExactBytesAndDigest(t *testing.T) {
	r := controlRunner(t)
	body := []byte("release bytes")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		requests++
		if q.Header.Get("Authorization") != "Bearer secret" || q.Header.Get(headerHost) != "host" {
			t.Error("missing host authorization")
		}
		if q.URL.Path == "/redirect" {
			http.Redirect(w, q, "/artifact", http.StatusFound)
			return
		}
		w.Write(body)
	}))
	defer server.Close()
	r.cfg.ServerURL = server.URL
	r.cfg.HostToken = "secret"
	r.cfg.HostID = "host"
	r.c.http = server.Client()
	for _, path := range []string{"https://example.invalid/artifact", "//example.invalid/artifact", "/redirect"} {
		dest := filepath.Join(t.TempDir(), "bad")
		if err := r.downloadReleaseFile(t.Context(), path, dest, int64(len(body)), digest); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	if requests != 1 {
		t.Fatalf("foreign URL requested: %d", requests)
	}
	dest := filepath.Join(t.TempDir(), "valid")
	if err := r.downloadReleaseFile(t.Context(), "/artifact", dest, int64(len(body)), digest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(body) {
		t.Fatal("bytes changed")
	}
	for _, size := range []int64{int64(len(body)) - 1, int64(len(body)) + 1} {
		if err := r.downloadReleaseFile(t.Context(), "/artifact", filepath.Join(t.TempDir(), "bad"), size, digest); err == nil {
			t.Fatal("bad length accepted")
		}
	}
	if err := r.downloadReleaseFile(t.Context(), "/artifact", filepath.Join(t.TempDir(), "bad"), int64(len(body)), string(make([]byte, 64))); err == nil {
		t.Fatal("bad digest accepted")
	}
}

func TestReleaseBundleVerifiedBeforeIdempotentUpdaterSubmission(t *testing.T) {
	r := controlRunner(t)
	r.cfg.UpdaterBundleDir = t.TempDir()
	artifact := []byte("runner binary fixture")
	sum := sha256.Sum256(artifact)
	artifactSHA := hex.EncodeToString(sum[:])
	manifest := release.Manifest{Format: 1, ReleaseID: "release", Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: 0, Max: 99}, RunnerJournal: release.Range{Min: journal.CurrentVersion, Max: journal.CurrentVersion}}, Artifacts: []release.Artifact{{Role: "runner", OS: "linux", Arch: "amd64", Path: "bin/runner", Size: int64(len(artifact)), SHA256: artifactSHA}}}
	raw, _ := json.Marshal(manifest)
	manifestHash := sha256.Sum256(raw)
	manifestSHA := hex.EncodeToString(manifestHash[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/manifest" {
			w.Write(raw)
		} else if q.URL.Path == "/artifacts/"+artifactSHA {
			w.Write(artifact)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	r.cfg.ServerURL = server.URL
	r.c.http = server.Client()
	socket := filepath.Join(t.TempDir(), "updater.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix socket: %v", err)
	}
	defer listener.Close()
	r.cfg.UpdaterControlSocket = socket
	var mu sync.Mutex
	submitted := 0
	supervisor := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.Method == http.MethodGet {
			if submitted == 0 {
				json.NewEncoder(w).Encode(map[string]any{"state": map[string]any{}})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"state": map[string]any{"operation": map[string]any{"id": "batch", "candidate": map[string]any{"manifest_sha256": manifestSHA}}}})
			}
			return
		}
		var req struct{ ID, Release string }
		if err := json.NewDecoder(q.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		bundle, err := release.Load(req.Release)
		if err != nil || bundle.ManifestSHA256 != manifestSHA {
			t.Errorf("unverified bundle submitted: %v", err)
		}
		if req.ID != "batch" {
			t.Errorf("wrong batch: %s", req.ID)
		}
		submitted++
		w.Write([]byte(`{}`))
	})}
	go supervisor.Serve(listener)
	defer supervisor.Close()
	control := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "batch", ReleaseID: "release", Upgrade: &protocol.RunnerUpgradeTarget{ManifestPath: "/manifest", ManifestSHA256: manifestSHA, ManifestSize: int64(len(raw)), ArtifactBasePath: "/artifacts/"}}
	if err := r.applyControl(t.Context(), &control, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.downloadAndSubmit(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	if err := r.downloadAndSubmit(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if submitted != 1 {
		t.Fatalf("duplicate updater submissions %d", submitted)
	}
}

func TestReleaseBundleRejectsPreviousJournalOnlyBeforeArtifacts(t *testing.T) {
	r := controlRunner(t)
	r.cfg.UpdaterBundleDir = t.TempDir()
	body := []byte("candidate")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	manifest := release.Manifest{Format: 1, ReleaseID: "previous-only", Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: 0, Max: 99}, RunnerJournal: release.Range{Min: journal.CurrentVersion - 1, Max: journal.CurrentVersion - 1}}, Artifacts: []release.Artifact{{Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: int64(len(body)), SHA256: digest}}}
	raw, _ := json.Marshal(manifest)
	hash := sha256.Sum256(raw)
	var artifacts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/manifest" {
			w.Write(raw)
			return
		}
		artifacts.Add(1)
		w.Write(body)
	}))
	defer server.Close()
	r.cfg.ServerURL = server.URL
	r.c.http = server.Client()
	control := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "batch", ReleaseID: manifest.ReleaseID, Upgrade: &protocol.RunnerUpgradeTarget{ManifestPath: "/manifest", ManifestSHA256: hex.EncodeToString(hash[:]), ManifestSize: int64(len(raw)), ArtifactBasePath: "/artifacts/"}}
	if err := r.applyControl(t.Context(), &control, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.downloadAndSubmit(t.Context(), control); err == nil {
		t.Fatal("a candidate excluding the actual journal version was accepted")
	}
	if artifacts.Load() != 0 {
		t.Fatalf("journal compatibility was checked after downloading %d artifacts", artifacts.Load())
	}
}

func TestNewMaintenanceAttemptClearsErrorsAndIgnoresOldDownloadFailure(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint("late=", late), func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.Log = slog.Default()
			r.cfg.UpdaterSocket = "unused"
			r.cfg.UpdaterControlSocket = filepath.Join(t.TempDir(), "absent.sock")
			r.cfg.UpdaterBundleDir = t.TempDir()
			started, finish := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				close(started)
				<-finish
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			r.cfg.ServerURL = server.URL
			r.c.http = server.Client()
			c := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "batch", ReleaseID: "release", Upgrade: &protocol.RunnerUpgradeTarget{ManifestPath: "/manifest", ManifestSHA256: strings.Repeat("a", 64), ManifestSize: 1}}
			if err := r.applyControl(t.Context(), &c, r.cfg.Now(), true); err != nil {
				t.Fatal(err)
			}
			r.resourceReconciled = true
			r.confirmedRuntime = protocol.RunnerRuntime{Mode: protocol.RunnerReady, AppliedControlRevision: 1, UpgradeID: "batch"}
			r.maybeUpgrade(t.Context())
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("download did not start")
			}
			if !late {
				close(finish)
				r.wg.Wait()
				view, err := r.runtimeObservation(t.Context())
				if err != nil || view.UpgradeError == "" {
					t.Fatal("failed download not observed", view, err)
				}
			}
			for _, next := range []protocol.RunnerControl{{Revision: 2, Mode: protocol.RunnerNormal}, {Revision: 3, Mode: protocol.RunnerMaintenance, UpgradeID: "batch", ReleaseID: "release"}} {
				if err := r.applyControl(t.Context(), &next, r.cfg.Now(), false); err != nil {
					t.Fatal(err)
				}
			}
			if late {
				close(finish)
				r.wg.Wait()
			}
			view, err := r.runtimeObservation(t.Context())
			if err != nil || view.UpgradeError != "" || view.UpgradePhase != "" || view.DownloadedBytes != 0 {
				t.Fatal("old attempt poisoned new control", view, err)
			}
		})
	}
}
