package runner

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
