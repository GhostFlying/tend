package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func selfControlFixture(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket fixture")
	}
	path := filepath.Join(t.TempDir(), "control.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	return path
}
func TestSelfUpdateCapabilityProbeUsesOnlyLegacyReadRoute(t *testing.T) {
	var calls atomic.Int64
	socket := selfControlFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/status" {
			t.Error("unknown legacy operation", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(ControlResponse{})
	})
	supported, err := SelfUpdateSupported(t.Context(), socket)
	if err != nil || supported || calls.Load() != 1 {
		t.Fatal(supported, calls.Load(), err)
	}
}
func TestSelfUpdateLostResponseOnlyObservesOriginalAttempt(t *testing.T) {
	var posts, reads atomic.Int64
	request := SelfUpdateRequest{Target: protocol.UpdaterSelfTarget{RequestID: "original"}, UpgradeID: "batch", ReleaseSetSHA256: "set"}
	socket := selfControlFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			hijacker := w.(http.Hijacker)
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		n := reads.Add(1)
		status := SelfUpdateStatus{Supported: true, Journal: &SelfUpdateJournal{Format: 1, Plan: SelfUpdatePlan{Request: request}, Phase: "starting"}}
		if n >= 2 {
			status.Observation = protocol.UpdaterSelfObservation{InstanceID: "host", Process: Identity{PID: 21, SHA256: "target"}, SupervisedProcess: Identity{PID: 20}, Receipt: &protocol.UpdaterSelfReceipt{RequestID: "original", UpgradeID: "batch", ReleaseSetSHA256: "set", InstanceID: "host", Installed: Identity{PID: 21, SHA256: "target"}}}
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	observation, err := EnsureSelfUpdate(ctx, socket, request)
	if err != nil || posts.Load() != 1 || reads.Load() != 2 || observation.Receipt == nil {
		t.Fatal("unknown dispatch repeated", posts.Load(), reads.Load(), observation, err)
	}
}
func TestSelfUpdateMissingReceiptNeverCompletesOrResubmits(t *testing.T) {
	var posts atomic.Int64
	socket := selfControlFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		_ = json.NewEncoder(w).Encode(SelfUpdateStatus{Supported: true})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()
	_, err := EnsureSelfUpdate(ctx, socket, SelfUpdateRequest{})
	if !errors.Is(err, ErrUnknown) || posts.Load() != 1 {
		t.Fatal("missing receipt became success or another attempt", posts.Load(), err)
	}
}
