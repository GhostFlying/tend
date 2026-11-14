package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestUpgradeProgressHeartbeatsBeforePeriodicIntervalWithoutLoop(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log, r.c.log = slog.Default(), slog.Default()
	r.cfg.HeartbeatInterval = time.Hour
	control := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "batch"}
	seen := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var payload protocol.HeartbeatPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Error(err)
			return
		}
		result, _ := json.Marshal(protocol.HeartbeatResult{Control: &control})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: result}})
		seen <- payload.Runtime.UpgradePhase
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.applyControl(t.Context(), &control, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.heartbeats(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("control ACK waited for periodic heartbeat")
	}
	if !r.updateUpgradeAttempt(control, func() { r.upgradePhase = "updater_ready" }) {
		t.Fatal("wrong active attempt")
	}
	select {
	case phase := <-seen:
		if phase != "updater_ready" {
			t.Fatal(phase)
		}
	case <-time.After(time.Second):
		t.Fatal("actual progress waited for periodic heartbeat")
	}
	// Replaying the same control or reporting byte progress must not self-wake.
	if err := r.applyControl(t.Context(), &control, r.cfg.Now(), false); err != nil {
		t.Fatal(err)
	}
	r.updateUpgradeAttempt(control, func() { r.downloadedBytes++ })
	select {
	case phase := <-seen:
		t.Fatal("unchanged state formed a heartbeat loop", phase)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUpgradeProgressCannotRedispatchFailedDownload(t *testing.T) {
	r := controlRunner(t)
	clock := testkit.NewClock()
	r.cfg.Now, r.c.now = clock.Now, clock.Now
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	r.c.log = r.cfg.Log
	r.cfg.HeartbeatInterval, r.cfg.RetryDelay = time.Hour, time.Hour
	r.cfg.UpdaterSocket = filepath.Join(t.TempDir(), "service.sock")
	r.cfg.UpdaterControlSocket = filepath.Join(t.TempDir(), "supervisor.sock")
	r.cfg.UpdaterBundleDir = t.TempDir()
	control := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "original", ReleaseID: "release", Upgrade: &protocol.RunnerUpgradeTarget{ManifestPath: "/manifest", ManifestSHA256: strings.Repeat("a", 64), ManifestSize: 1}}
	var downloads atomic.Int32
	repeated, reported := make(chan struct{}, 1), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/manifest" {
			if downloads.Add(1) > 1 {
				select {
				case repeated <- struct{}{}:
				default:
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		var payload protocol.HeartbeatPayload
		_ = json.Unmarshal(env.Payload, &payload)
		if payload.Runtime != nil && payload.Runtime.UpgradePhase == "download_failed" {
			select {
			case reported <- struct{}{}:
			default:
			}
		}
		result, _ := json.Marshal(protocol.HeartbeatResult{Control: &control})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: result}})
	}))
	defer server.Close()
	r.cfg.ServerURL, r.c.base, r.c.http = server.URL, server.URL, server.Client()
	if err := r.applyControl(t.Context(), &control, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	r.resourceReconciled = true
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.heartbeats(ctx) }()
	defer func() { cancel(); <-done; r.wg.Wait() }()
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("failed download was not promptly reported")
	}
	select {
	case <-repeated:
		t.Fatal("progress ACK redispatched failed download before retry delay")
	case <-time.After(50 * time.Millisecond):
	}
	if downloads.Load() != 1 {
		t.Fatal("wrong original attempt count", downloads.Load())
	}
	// A later ordinary observation may retry the safe failed download after
	// the original delay. Progress reporting must not disable recovery either.
	clock.Advance(time.Hour)
	r.requestHeartbeat()
	select {
	case <-repeated:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not recover after its finite delay")
	}
	select {
	case <-repeated:
		t.Fatal("retry formed another progress loop")
	case <-time.After(50 * time.Millisecond):
	}
	if downloads.Load() != 2 {
		t.Fatal("retry was duplicated", downloads.Load())
	}
}
