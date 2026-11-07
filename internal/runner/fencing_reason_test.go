package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_FencingReportsHeartbeatRejectionAndLeaseExpiry(t *testing.T) {
	for _, cause := range []string{"heartbeat_not_renewed", "lease_deadline_elapsed"} {
		t.Run(cause, func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
			var logs bytes.Buffer
			r.cfg.Log = slog.New(slog.NewJSONHandler(&logs, nil))
			r.c.log = r.cfg.Log
			runCtx, stop := context.WithCancelCause(t.Context())
			defer stop(nil)
			deadline := r.cfg.Now().Add(time.Minute)
			if cause == "lease_deadline_elapsed" {
				deadline = r.cfg.Now().Add(-time.Nanosecond)
			}
			r.active["original-run"] = &active{as: protocol.Assignment{RunID: "original-run", LeaseID: "original-lease"}, ctx: runCtx, cancel: stop, deadline: deadline}
			if cause == "heartbeat_not_renewed" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					result, _ := json.Marshal(protocol.HeartbeatResult{Runs: []protocol.LeaseState{{RunID: "original-run", Desired: protocol.DesiredRun, Renewed: false}}})
					_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: result}})
				}))
				defer server.Close()
				r.c.base = server.URL
				r.c.http = server.Client()
				r.heartbeat(t.Context())
			} else {
				r.setTTL(100)
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); r.watchdog(ctx) }()
				select {
				case <-runCtx.Done():
				case <-time.After(5 * time.Second):
					cancel()
					<-done
					t.Fatal("watchdog did not fence expired lease")
				}
				cancel()
				<-done
			}
			if !errors.Is(context.Cause(runCtx), errFenced) {
				t.Fatal("original run not fenced", context.Cause(runCtx))
			}
			if !strings.Contains(logs.String(), `"cause":"`+cause+`"`) || !strings.Contains(logs.String(), `"run_id":"original-run"`) || !strings.Contains(logs.String(), `"lease_deadline":`) {
				t.Fatal(logs.String())
			}
		})
	}
}
