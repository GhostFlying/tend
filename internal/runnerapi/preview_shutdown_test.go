package runnerapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestShutdownEndsIdleRolloutPreviewPoll(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	runtime := &protocol.RunnerRuntime{InstanceID: "preview-shutdown", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.MinimumRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRolloutPreview}}
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1}))
	stopping := make(chan struct{})
	close(stopping)
	s := New(e.app, e.peers, secret, Config{Stopping: stopping}, slog.Default())
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/runner/v1/rollout-preview?instance=preview-shutdown", nil)
		req.Header.Set("X-Tend-Host", "linux-1")
		req.Header.Set("Authorization", "Bearer "+hostToken)
		resp, err := server.Client().Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status=%d", resp.StatusCode)
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle preview poll held shutdown until its 25-second timer")
	}
}
