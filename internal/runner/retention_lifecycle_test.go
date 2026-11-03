package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestRetainedReconciliationReportingLifecycle(t *testing.T) {
	cases := []struct {
		name             string
		role             protocol.RunRole
		phase, operation string
		status           protocol.OpStatus
	}{
		{"implement-artifact", protocol.RoleImplement, journal.PhaseSubmitting, protocol.OpRunsSubmitArtifact, protocol.OpSucceeded},
		{"accept-verdict", protocol.RoleAccept, journal.PhaseSubmitting, protocol.OpAcceptancesSubmit, protocol.OpSucceeded},
		{"implement-release", protocol.RoleImplement, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.OpAccepted},
		{"accept-release", protocol.RoleAccept, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.OpAccepted},
		{"manager-stopped", protocol.RoleIssueManager, journal.PhaseReleasing, protocol.OpManagerNativeStopped, protocol.OpSucceeded},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r, as, probe, path := retainedRunnerFixture(t, tt.role)
			r.cfg.Workers = &completionWorkers{}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
				t.Fatal(err)
			}
			env, err := r.newEnvelope(tt.operation, runTarget(as), map[string]any{"original": "business-or-physical-fact"})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.cfg.Journal.Finish(ctx, as.RunID, tt.phase, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var reports, renewals atomic.Int64
			var stop atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var request protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				var result any = map[string]any{}
				status := protocol.OpSucceeded
				switch request.Operation {
				case protocol.OpRunnerHello:
					verdict, desired := protocol.VerdictRetain, protocol.DesiredRun
					if stop.Load() {
						verdict, desired = protocol.VerdictStop, protocol.DesiredStopped
					}
					result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: verdict, Desired: desired}}}
				case protocol.OpRunnerHeartbeat:
					var p protocol.HeartbeatPayload
					if err := json.Unmarshal(request.Payload, &p); err != nil {
						t.Error(err)
					}
					renewals.Add(int64(len(p.Runs)))
					result = protocol.HeartbeatResult{LeaseTTLMillis: 60000}
				default:
					if request.Operation != tt.operation || !reflect.DeepEqual(request, *env) {
						t.Error("original fact/request replaced", request)
					}
					if reports.Add(1) == 1 {
						close(entered)
						<-release
					}
					status = tt.status
					result = map[string]any{"business_outcome": "pending_reconciliation"}
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: status, Result: raw}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			r.start(ctx, as, r.cfg.Now())
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("original report never started")
			}
			if err := r.freshHello(ctx); err != nil {
				close(release)
				t.Fatal(err)
			}
			r.heartbeat(ctx)
			if renewals.Load() != 0 {
				t.Fatal("retained active lease renewed", renewals.Load())
			}
			close(release)
			r.wg.Wait()
			pending, err := r.cfg.Journal.Requests(ctx)
			saved, readErr := r.cfg.Journal.Run(ctx, as.RunID)
			if err != nil || readErr != nil || !saved.Fenced || saved.Phase != tt.phase || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, *env) || probe.calls.Load() != 0 {
				t.Fatal("late report discarded retained identity/ownership", saved, pending, probe.calls.Load(), err, readErr)
			}
			if err := r.cfg.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := journal.Open(ctx, filepath.Clean(path), r.cfg.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			cfg := r.cfg
			cfg.Journal = j
			restarted := New(cfg)
			restarted.c.base, restarted.c.http = server.URL, server.Client()
			if err := restarted.freshHello(ctx); err != nil {
				t.Fatal(err)
			}
			restarted.start(ctx, as, cfg.Now())
			restarted.wg.Wait()
			if reports.Load() != 1 || probe.calls.Load() != 0 {
				t.Fatal("restart replayed work/facts", reports.Load(), probe.calls.Load())
			}
			done := make(chan struct{})
			go func() { defer close(done); restarted.reconciler(ctx) }()
			defer func() { cancel(); <-done; restarted.wg.Wait() }()
			stop.Store(true)
			restarted.heartbeat(ctx)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				held, err := j.Runs(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(held) == 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("later authoritative Stop did not settle", tt.name)
				case <-ticker.C:
				}
			}
			wantCleanup := int64(1)
			if tt.role == protocol.RoleIssueManager {
				wantCleanup = 0
			}
			if reports.Load() != 2 || probe.calls.Load() != wantCleanup {
				t.Fatal("explicit Stop changed business work or request", reports.Load(), probe.calls.Load())
			}
		})
	}
}

func TestRetainedReconciliationDoesNotRelabelDoneJournal(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t)
	if err := r.cfg.Journal.Done(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.retainRun(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatal("completed journal became retained", err)
	}
}

func TestRetainedReconciliationSettlementWaitHonorsCancellation(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t)
	runCtx, cancelAttempt := context.WithCancelCause(t.Context())
	defer cancelAttempt(nil)
	a := &active{as: as, ctx: runCtx, cancel: cancelAttempt}
	r.active[as.RunID] = a
	unlock, err := r.lockSettlement(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.retainRun(ctx, as.RunID); !errors.Is(err, context.Canceled) {
		unlock()
		t.Fatal("retention wait ignored cancellation", err)
	}
	called := false
	if _, err := r.settleRun(ctx, as.RunID, func() error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		unlock()
		t.Fatal("settlement wait ignored cancellation", err, called)
	}
	unlock()
	if err := r.retainRun(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if applied, err := r.settleRun(t.Context(), as.RunID, func() error { called = true; return nil }); err != nil || applied || called {
		t.Fatal("retained settlement mutated state", applied, err, called)
	}
}

// A newer authority event wins even if the cancelled executor persists its fence late.
func TestRetainedReconciliationAuthorityTransitions(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "continue"}[clear], func(t *testing.T) {
			r, as, _, _ := retainedRunnerFixture(t)
			ctx := t.Context()
			runCtx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			a := &active{as: as, ctx: runCtx, cancel: cancel}
			r.active[as.RunID] = a
			if err := r.retainRun(ctx, as.RunID); err != nil {
				t.Fatal(err)
			}
			if err := r.authorizeRetainedSettlement(ctx, as.RunID, clear); err != nil {
				t.Fatal(err)
			}
			if err := r.markExecutorFenced(ctx, a); err != nil {
				t.Fatal(err)
			}
			saved, err := r.cfg.Journal.Run(ctx, as.RunID)
			if err != nil || a.retained || !a.reconcileOnExit || saved.Fenced == clear {
				t.Fatal(saved, a, err)
			}
			called := false
			settled, err := r.settleRun(ctx, as.RunID, func() error { called = true; return nil })
			if err != nil || !settled || !called {
				t.Fatal(settled, called, err)
			}
			// A subsequent retain is a newer event again and must restore both guards.
			if err := r.retainRun(ctx, as.RunID); err != nil {
				t.Fatal(err)
			}
			saved, err = r.cfg.Journal.Run(ctx, as.RunID)
			settled, err2 := r.settleRun(ctx, as.RunID, func() error { t.Fatal("retained settlement"); return nil })
			if err != nil || err2 != nil || !saved.Fenced || !a.retained || settled {
				t.Fatal(saved, a, settled, err, err2)
			}
		})
	}
}
