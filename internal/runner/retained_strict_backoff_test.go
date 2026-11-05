package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestRetainedStrictRetryBackoffStillAllowsStop(t *testing.T) {
	r, as, _, authority, _ := retainedAuthorityStopFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.cfg.Journal.ClearRetainedStop(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	delete(r.active, as.RunID)
	r.cfg.RetryDelay = time.Second
	var requests atomic.Int64
	var stop atomic.Bool
	first := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunnerEvents {
			_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			return
		}
		if env.Operation == protocol.OpRunsRelease {
			requests.Add(1)
			once.Do(func() { close(first) })
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "immutable original accepted request")})
			return
		}
		if env.Operation != protocol.OpRunnerHello {
			t.Errorf("unexpected reporting operation %s", env.Operation)
			return
		}
		v := protocol.RunVerdict{RunID: as.RunID, Verdict: protocol.VerdictContinueRetained, Desired: protocol.DesiredRun, RetainedResume: &authority}
		if stop.Load() {
			v = protocol.RunVerdict{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}
		}
		raw, _ := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{v}})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	done := make(chan struct{})
	go func() { defer close(done); r.reconciler(ctx) }()
	defer func() { cancel(); <-done; r.wg.Wait() }()
	r.start(ctx, as, r.cfg.Now())
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("original strict request was never attempted")
	}
	// A successful Hello is not a fresh business result and must not bypass
	// the configured retry spacing by repeatedly creating reporting executors.
	time.Sleep(250 * time.Millisecond)
	if n := requests.Load(); n != 1 {
		t.Errorf("strict unknown report bypassed configured retry delay: %d POSTs", n)
	}
	stop.Store(true)
	r.requestReconcile()
	for {
		saved, err := r.cfg.Journal.HistoricalRun(ctx, as.RunID)
		if err == nil && saved.Phase == journal.PhaseDone {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reporting backoff blocked authoritative Stop")
		case <-time.After(time.Millisecond):
		}
	}
}
