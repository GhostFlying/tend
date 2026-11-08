package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/updater"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func selfConfigurationSocket(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket fixture")
	}
	p := filepath.Join(t.TempDir(), "u.sock")
	l, e := net.Listen("unix", p)
	if e != nil {
		t.Fatal(e)
	}
	s := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close(); <-done })
	return p
}
func TestSelfUpdateSameBinaryStillSubmitsConfiguration(t *testing.T) {
	r := controlRunner(t)
	r.cfg.UpdaterBundleDir = t.TempDir()
	r.cfg.Build = protocol.RunnerBuild{PID: 99, GOOS: "linux", GOARCH: "amd64"}
	data := []byte("same fixture bytes")
	hash := sha256.Sum256(data)
	sha := hex.EncodeToString(hash[:])
	r.cfg.Build.BinarySHA256 = sha
	m := release.Manifest{Format: 1, ReleaseID: "release", Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: 1, Max: 999}, RunnerJournal: release.Range{Min: journal.CurrentVersion, Max: journal.CurrentVersion}}}
	for _, role := range []string{"server", "runner"} {
		m.Artifacts = append(m.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: int64(len(data)), SHA256: sha})
	}
	raw, _ := json.Marshal(m)
	mh := sha256.Sum256(raw)
	msha := hex.EncodeToString(mh[:])
	set := release.Set{Format: 2, ReleaseID: m.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", ManifestSHA256: msha, Artifacts: append([]release.Artifact{}, m.Artifacts...)}
	for _, role := range []string{"updater", "cli"} {
		set.Artifacts = append(set.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: int64(len(data)), SHA256: sha})
	}
	sr, _ := json.Marshal(set)
	sh := sha256.Sum256(sr)
	ssha := hex.EncodeToString(sh[:])
	dir := filepath.Join(r.cfg.UpdaterBundleDir, msha+"-"+ssha)
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600)
	os.WriteFile(filepath.Join(dir, "release-set.json"), sr, 0600)
	for _, a := range set.Artifacts {
		os.WriteFile(filepath.Join(dir, a.Path), data, 0600)
	}
	var submitted atomic.Int64
	r.cfg.UpdaterControlSocket = selfConfigurationSocket(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.HasPrefix(q.URL.Path, "/self-update/") {
			json.NewEncoder(w).Encode(updater.SelfUpdateStatus{Supported: true, Observation: protocol.UpdaterSelfObservation{InstanceID: "host", Process: protocol.RecoveryIdentity{PID: 98, SHA256: sha}, SupervisedProcess: protocol.RecoveryIdentity{PID: 99, SHA256: sha, LaunchID: "instance"}, Receipt: &protocol.UpdaterSelfReceipt{RequestID: "original", UpgradeID: "batch", ReleaseSetSHA256: ssha, InstanceID: "host", Installed: protocol.RecoveryIdentity{PID: 98, SHA256: sha}}}})
			return
		}
		if q.URL.Path == "/upgrade" {
			submitted.Add(1)
		}
		json.NewEncoder(w).Encode(updater.ControlResponse{})
	})
	ctl := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "batch", ReleaseID: "release", Upgrade: &protocol.RunnerUpgradeTarget{ManifestSHA256: msha, ManifestSize: int64(len(raw)), ReleaseSet: &protocol.ReleaseSetTarget{SHA256: ssha, Size: int64(len(sr))}, Updater: &protocol.UpdaterSelfTarget{RequestID: "original"}, Configuration: &protocol.RunnerConfiguration{SHA256: strings.Repeat("e", 64), PreviousSHA256: strings.Repeat("f", 64)}}}
	if e := r.applyControl(t.Context(), &ctl, r.cfg.Now(), true); e != nil {
		t.Fatal(e)
	}
	if e := r.downloadAndSubmit(context.Background(), ctl); e != nil {
		t.Fatal(e)
	}
	if submitted.Load() != 1 {
		t.Fatalf("configuration upgrade never submitted: calls=%d phase=%s", submitted.Load(), r.upgradePhase)
	}
}
