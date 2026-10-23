package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

type cancelProofProcess struct{ identity updater.Identity }

func (p cancelProofProcess) Inspect(context.Context, int) (updater.Identity, error) {
	return p.identity, nil
}
func (p cancelProofProcess) Find(context.Context, string, string) ([]updater.Identity, error) {
	return nil, nil
}
func (p cancelProofProcess) Start(context.Context, string, updater.Config, map[string]string) (updater.Identity, error) {
	return updater.Identity{}, errors.New("test must never start process")
}
func (p cancelProofProcess) Stop(context.Context, updater.Identity, time.Duration) error {
	return errors.New("test must never stop process")
}

type cancelProofAuthority struct{}

func (cancelProofAuthority) Authorize(context.Context, updater.Binding) (updater.Grant, error) {
	return updater.Grant{}, nil
}
func (cancelProofAuthority) Health(context.Context, updater.Binding) (updater.Health, error) {
	return updater.Health{}, nil
}

func TestCancelledTombstoneFencesLateUpgradePOSTAndRetainsNextBatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("supervisor is Linux-only")
	}
	root := t.TempDir()
	old := []byte("old executable fixture")
	oldSHA := sha256.Sum256(old)
	binary := filepath.Join(root, "runner")
	if err := os.WriteFile(binary, old, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := updater.Config{InstanceID: "host", Role: "runner", StateDir: filepath.Join(root, "supervisor"), InitialBinary: binary, LogPath: filepath.Join(root, "runner.log"), Protocol: 1, StorageVersion: 9}
	supervisor, err := updater.Open(cfg, updater.Options{Process: cancelProofProcess{identity: updater.Identity{PID: 7, StartTicks: 1, LaunchID: "old", SHA256: hex.EncodeToString(oldSHA[:])}}, Authority: cancelProofAuthority{}})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	if err := supervisor.Bootstrap(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "release")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("new executable fixture")
	sum := sha256.Sum256(data)
	artifact := release.Artifact{Role: "runner", OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "runner", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	manifest := release.Manifest{Format: 1, ReleaseID: "new", Protocol: release.Range{Min: 0, Max: 1}, Storage: release.Storage{ServerSchema: release.Range{Min: 26, Max: 29}, RunnerJournal: release.Range{Min: 9, Max: 9}}, Artifacts: []release.Artifact{artifact}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(bundle, "runner"), data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	entered, releaseOld := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(releaseOld) })
	socket := filepath.Join(root, "control.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix socket unavailable: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method == http.MethodGet && q.URL.Path == "/status" {
			json.NewEncoder(w).Encode(updater.ControlResponse{State: supervisor.Status()})
			return
		}
		var request updater.ControlRequest
		if err := json.NewDecoder(q.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		var callErr error
		switch q.URL.Path {
		case "/upgrade":
			if request.ID == "cancelled-old" {
				close(entered)
				select {
				case <-releaseOld:
				case <-q.Context().Done():
					return
				}
			}
			callErr = supervisor.Upgrade(q.Context(), request.ID, request.Release)
		case "/cancel":
			callErr = supervisor.CancelExpected(request.ID, request.ExpectedPhase)
		default:
			w.WriteHeader(404)
			return
		}
		result := updater.ControlResponse{State: supervisor.Status()}
		if callErr != nil {
			w.WriteHeader(409)
			result.Error = callErr.Error()
		}
		json.NewEncoder(w).Encode(result)
	})}
	go server.Serve(ln)
	defer server.Close()
	lateDone := make(chan error, 1)
	go func() {
		_, err := updater.CallSocket(t.Context(), socket, "upgrade", updater.ControlRequest{ID: "cancelled-old", Release: bundle})
		lateDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old POST did not arrive")
	}
	r := controlRunner(t)
	r.cfg.UpdaterInstanceID = "host"
	r.cfg.UpdaterControlSocket = socket
	control := r.control
	control.Revision = 4
	control.Mode = "normal"
	control.CancelledUpgradeID = "cancelled-old"
	if err := r.applyControl(t.Context(), &control, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	view, err := r.runtimeObservation(t.Context())
	r.wg.Wait()
	if err != nil || view.ConfirmedCancelledUpgradeID != "cancelled-old" {
		t.Fatal("missing durable tombstone confirmation", view, err)
	}
	once.Do(func() { close(releaseOld) })
	select {
	case err := <-lateDone:
		if err == nil {
			t.Fatal("late cancelled POST was accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late POST did not finish")
	}
	if op := supervisor.Status().Operation; op != nil {
		t.Fatal("late old POST resurrected operation", op)
	}
	if err := r.submitUpdater(t.Context(), "next-batch", bundle, "unused"); err != nil {
		t.Fatal("tombstone blocked new batch", err)
	}
	restarted := New(r.cfg)
	if _, err := restarted.runtimeObservation(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted.wg.Wait()
	view, err = restarted.runtimeObservation(t.Context())
	restarted.wg.Wait()
	if err != nil || view.ConfirmedCancelledUpgradeID != "cancelled-old" {
		t.Fatal("terminal history lost across runner restart", view, err)
	}
	state := supervisor.Status()
	if state.Operation == nil || state.Operation.ID != "next-batch" || state.Operation.Phase != "staged" || !updater.Cancelled(state, "cancelled-old") {
		t.Fatal("next batch or old cancellation lost", state)
	}
}
