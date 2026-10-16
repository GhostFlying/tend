package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestClientLogsLostAndRegainedLink(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeNotFound, "no such run")})
	}))
	defer up.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer stalled.Close()
	defer close(release)

	var logs bytes.Buffer
	now := time.Unix(0, 0)
	c := &client{base: down.URL, http: up.Client(), hostID: "linux-1", log: slog.New(slog.NewTextHandler(&logs, nil)),
		now: func() time.Time { return now }}
	env := &protocol.Envelope{RequestID: "req_1", Operation: protocol.OpRunnerClaim}
	// A cancelled request says nothing about tend.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.do(cancelled, c.host(), env); !isRetryable(err) || logs.Len() != 0 {
		t.Fatalf("cancelled do = %v, logged %q", err, logs.String())
	}
	for range 3 {
		if _, err := c.do(context.Background(), c.host(), env); !isRetryable(err) {
			t.Fatalf("do while tend is down = %v", err)
		}
		now = now.Add(10 * time.Second)
	}
	// A timed-out request, as when the network drops packets, is as
	// unreachable.
	c.base = stalled.URL
	timeout, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if _, err := c.do(timeout, c.host(), env); !isRetryable(err) {
		t.Fatalf("do while tend stalls = %v", err)
	}
	c.base = up.URL
	for range 2 {
		// A rejection is an answer: tend is reachable.
		if _, err := c.do(context.Background(), c.host(), env); protocol.CodeOf(err) != protocol.CodeNotFound {
			t.Fatalf("do after tend is back = %v", err)
		}
	}
	got := logs.String()
	if n := strings.Count(got, "tend is unreachable"); n != 1 {
		t.Errorf("unreachable logged %d times:\n%s", n, got)
	}
	if n := strings.Count(got, "reconnected to tend"); n != 1 || !strings.Contains(got, "down_for=30s") {
		t.Errorf("reconnection logged %d times:\n%s", n, got)
	}
}
