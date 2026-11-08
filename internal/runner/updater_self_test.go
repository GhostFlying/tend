package runner

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestSelfUpdateCapabilityRefreshesThroughExistingStatusRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix control socket fixture")
	}
	r := controlRunner(t)
	r.cfg.UpdaterSocket = "authority.sock"
	r.cfg.UpdaterBundleDir = "bundles"
	r.cfg.UpdaterInstanceID = "host"
	path := filepath.Join(t.TempDir(), "control.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var modern atomic.Bool
	var reads atomic.Int64
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		reads.Add(1)
		if q.Method != http.MethodGet || q.URL.Path != "/status" {
			t.Error("capability probe changed old control protocol", q.Method, q.URL.Path)
			w.WriteHeader(405)
			return
		}
		if modern.Load() {
			w.Header().Set("Tend-Updater-Capabilities", protocol.CapabilityUpdaterSelfUpdate)
		}
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: updater.State{InstanceID: "host"}})
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	r.cfg.UpdaterControlSocket = path
	before, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.Updater != nil || slices.Contains(before.Capabilities, protocol.CapabilityUpdaterSelfUpdate) {
		t.Fatal("legacy supervisor advertised unsupported extension", before)
	}
	modern.Store(true)
	after, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(after.Capabilities, protocol.CapabilityUpdaterSelfUpdate) || after.Updater != nil {
		t.Fatal("bootstrap required a service restart or emitted unselected fields", after)
	}
	if reads.Load() != 2 {
		t.Fatal("capability introduced extra recurring status requests", reads.Load())
	}
}
