package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type retainedWorkspaceProbe struct{ calls atomic.Int64 }

func (p *retainedWorkspaceProbe) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	p.calls.Add(1)
	return Prepared{}, ErrFetchFailed
}
func (p *retainedWorkspaceProbe) Collect(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	p.calls.Add(1)
	return protocol.ArtifactPayload{}, ErrNoChanges
}
func (p *retainedWorkspaceProbe) Finish(context.Context, protocol.Assignment) error {
	p.calls.Add(1)
	return nil
}

func retainedRunnerFixture(t *testing.T, roles ...protocol.RunRole) (*Runner, protocol.Assignment, *retainedWorkspaceProbe, string) {
	t.Helper()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	probe := &retainedWorkspaceProbe{}
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 7), Workspaces: probe, RetryDelay: time.Millisecond, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original-run", LeaseID: "original-lease", TaskID: "original-task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun}
	if len(roles) > 0 {
		as.Role = roles[0]
	}
	if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, "original-thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	return r, as, probe, path
}

func TestRetainedReconciliationSurvivesRestartWithoutReplayingWork(t *testing.T) {
	ctx := t.Context()
	r, as, probe, path := retainedRunnerFixture(t)
	original := journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-final-request", Operation: protocol.OpRunsRelease, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"reason":"unclassified transport failure"}`)}}
	if err := r.cfg.Journal.PutRequest(ctx, original); err != nil {
		t.Fatal(err)
	}
	before, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	requestsBefore, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, env)
		n := len(requests)
		mu.Unlock()
		if env.Operation != protocol.OpRunnerHello {
			t.Errorf("retained run sent new business: %s", env.Operation)
		}
		var payload protocol.HelloPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.Runtime == nil || !slices.Contains(payload.Runtime.Capabilities, protocol.CapabilityRetainedReconciliation) {
			t.Error("retain was not explicitly negotiated", payload, err)
		}
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		result, _ := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictRetain, Desired: protocol.DesiredRun}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: result}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	activeCtx, cancelAttempt := context.WithCancelCause(ctx)
	r.active[as.RunID] = &active{as: as, ctx: activeCtx, cancel: cancelAttempt}
	if err := r.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(activeCtx), errFenced) {
		t.Fatal("existing attempt was not fenced")
	}
	delete(r.active, as.RunID)
	mu.Lock()
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal("transport uncertainty did not retry the original Hello envelope", requests)
	}
	mu.Unlock()
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := journal.Open(ctx, path, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cfg := r.cfg
	cfg.Journal = reopened
	cfg.ServerURL, cfg.HTTPClient = server.URL, server.Client()
	restarted := New(cfg)
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() { restarted.start(ctx, as, cfg.Now()) })
	}
	workers.Wait()
	if err := restarted.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	workers.Go(func() { restarted.start(ctx, as, cfg.Now()) })
	workers.Wait()
	saved, err := reopened.Run(ctx, as.RunID)
	if err != nil || !saved.Fenced || saved.Thread != before.Thread || saved.Phase != before.Phase || saved.Attempts != before.Attempts || !reflect.DeepEqual(saved.Assignment, before.Assignment) {
		t.Fatal("retained journal identity changed", saved, err)
	}
	requestsAfter, err := reopened.Requests(ctx)
	if err != nil || !reflect.DeepEqual(requestsAfter, requestsBefore) || probe.calls.Load() != 0 || restarted.lookup(as.RunID) != nil {
		t.Fatal("retention resumed work, released resources, or replaced an original request", requestsAfter, err, probe.calls.Load())
	}
}

func TestRetainedReconciliationSerializesHelloAndCancelsWaiters(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		result, _ := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictRetain, Desired: protocol.DesiredRun}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	first := make(chan error, 1)
	go func() { first <- r.freshHello(t.Context()) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.freshHello(ctx); !errors.Is(err, context.Canceled) {
		t.Error("waiting Hello ignored cancellation", err)
	}
	close(release)
	if err := <-first; err != nil || calls.Load() != 1 {
		t.Fatal("overlapping Hello could reorder a prior Continue after retain", err, calls.Load())
	}
}

func TestRetainedReconciliationRejectsUnheldRetainVerdict(t *testing.T) {
	r, _, probe, _ := retainedRunnerFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		result, _ := json.Marshal(protocol.HelloResult{Runs: []protocol.RunVerdict{{RunID: "not-held", Verdict: protocol.VerdictRetain}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.freshHello(t.Context()); err == nil || probe.calls.Load() != 0 {
		t.Fatal("invalid retain response created or released work", err)
	}
}

func TestRetainedReconciliationStillHonorsExplicitContinueAndStop(t *testing.T) {
	for _, verdict := range []string{protocol.VerdictContinue, protocol.VerdictStop} {
		t.Run(verdict, func(t *testing.T) {
			r, as, probe, _ := retainedRunnerFixture(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			if err := r.cfg.Journal.SetFenced(ctx, as.RunID, true); err != nil {
				t.Fatal(err)
			}
			a := &active{as: as, ctx: ctx, cancel: cancel, desired: protocol.DesiredPaused, resume: make(chan struct{}, 1)}
			r.active[as.RunID] = a
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				desired := protocol.DesiredRun
				if verdict == protocol.VerdictStop {
					desired = protocol.DesiredStopped
				}
				result, _ := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: verdict, Desired: desired}}})
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			if err := r.freshHello(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err := r.cfg.Journal.Run(t.Context(), as.RunID)
			if err != nil || probe.calls.Load() != 0 {
				t.Fatal("business control lost the original journal", saved, err)
			}
			if verdict == protocol.VerdictContinue {
				if saved.Fenced || a.deadline.IsZero() || a.desired != protocol.DesiredRun || context.Cause(ctx) != nil {
					t.Fatal("explicit Continue no longer resumes a normal fence", saved, a)
				}
			} else if !errors.Is(context.Cause(ctx), errStopped) || !saved.Fenced {
				t.Fatal("explicit Stop was hidden by a retain fence", saved, context.Cause(ctx))
			}
		})
	}
}

func TestRetainedReconciliationLateExecutorCannotOpenWork(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleIssueManager} {
		t.Run(string(role), func(t *testing.T) {
			r, as, probe, _ := retainedRunnerFixture(t)
			as.Role = role
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(errFenced)
			if err := r.execute(t.Context(), ctx, as.RunID, &active{as: as}); err != nil {
				t.Fatal(err)
			}
			saved, err := r.cfg.Journal.Run(t.Context(), as.RunID)
			if err != nil || !saved.Fenced || saved.Phase != journal.PhaseStarted || saved.Thread != "original-thread" || saved.Attempts != 0 || probe.calls.Load() != 0 {
				t.Fatal("late executor entered native/workspace lifecycle after the fence", saved, err, probe.calls.Load())
			}
			requests, err := r.cfg.Journal.Requests(t.Context())
			if err != nil || len(requests) != 0 {
				t.Fatal("late executor created a new outcome request", requests, err)
			}
		})
	}
}

func TestRetainedReconciliationRunningManagerPreservesJournal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original-run", LeaseID: "original-lease", Role: protocol.RoleIssueManager, Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{ConversationID: "original-issue"}, Desired: protocol.DesiredRun}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{}, 1)
	var stops atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		switch env.Operation {
		case protocol.OpRunnerHello:
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictRetain, Desired: protocol.DesiredRun}}}
		case protocol.OpManagerNativeClaim:
			select {
			case claimed <- struct{}{}:
			default:
			}
		case protocol.OpManagerNativeStopped:
			stops.Add(1)
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	resolver := &closeRecoveryResolver{native: testkit.NewHarness()}
	r := New(Config{Journal: j, Executions: resolver, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, RetryDelay: time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	r.start(ctx, as, clock.Now())
	select {
	case <-claimed:
	case <-ctx.Done():
		t.Fatal("manager never became active")
	}
	if err := r.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	saved, err := j.Run(ctx, as.RunID)
	if err != nil || !saved.Fenced {
		t.Fatalf("retain deleted original manager journal: run=%+v err=%v stopped_receipts=%d", saved, err, stops.Load())
	}
}

func TestRetainedReconciliationHeartbeatDeliversLaterStop(t *testing.T) {
	r, as, probe, _ := retainedRunnerFixture(t)
	r.cfg.Workers = &completionWorkers{}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var stop atomic.Bool
	var helloCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		switch env.Operation {
		case protocol.OpRunnerHello:
			helloCalls.Add(1)
			v := protocol.RunVerdict{RunID: as.RunID, Verdict: protocol.VerdictRetain, Desired: protocol.DesiredRun}
			if stop.Load() {
				v.Verdict = protocol.VerdictStop
				v.Desired = protocol.DesiredStopped
			}
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{v}}
		case protocol.OpRunnerHeartbeat:
			var hb protocol.HeartbeatPayload
			if err := json.Unmarshal(env.Payload, &hb); err != nil {
				t.Error(err)
			}
			if len(hb.Runs) != 0 {
				t.Error("retained journal was renewed as active", hb.Runs)
			}
			result = protocol.HeartbeatResult{LeaseTTLMillis: 60000}
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	stop.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); r.reconciler(ctx) }()
	defer func() { cancel(); <-done; r.wg.Wait() }()
	r.heartbeat(ctx)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		held, err := r.cfg.Journal.Runs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(held) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("heartbeat did not deliver owner stop", helloCalls.Load())
		case <-ticker.C:
		}
	}
	if helloCalls.Load() < 2 {
		t.Fatal("owner stop did not use fresh Hello")
	}
	if probe.calls.Load() > 1 {
		t.Fatal("stop reentered workspace lifecycle", probe.calls.Load())
	}
}

func TestRetainedReconciliationLateManagerReportKeepsOriginalRequest(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t, protocol.RoleIssueManager)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	env, err := r.newEnvelope(protocol.OpManagerNativeStopped, runTarget(as), protocol.ManagerStoppedPayload{Reason: "original physical exit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var reports atomic.Int64
	var stop atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var request protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		if request.Operation == protocol.OpManagerNativeStopped {
			if !reflect.DeepEqual(request, *env) {
				t.Error("physical stop was replaced", request)
			}
			if reports.Add(1) == 1 {
				close(entered)
				<-release
			}
		}
		if request.Operation == protocol.OpRunnerHello {
			verdict := protocol.VerdictRetain
			desired := protocol.DesiredRun
			if stop.Load() {
				verdict = protocol.VerdictStop
				desired = protocol.DesiredStopped
			}
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: verdict, Desired: desired}}}
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.start(ctx, as, r.cfg.Now())
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("report did not start")
	}
	if err := r.freshHello(ctx); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	r.wg.Wait()
	saved, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil || !saved.Fenced || saved.Phase != journal.PhaseReleasing {
		t.Fatal("late report discarded retained journal", saved, err)
	}
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, *env) {
		t.Fatal("late report discarded original recovery request", pending, err)
	}
	restarted := New(r.cfg)
	restarted.c.base, restarted.c.http = server.URL, server.Client()
	if err := restarted.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.wg.Wait()
	if reports.Load() != 1 {
		t.Fatal("restart replayed retained request")
	}
	stop.Store(true)
	if err := restarted.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.wg.Wait()
	held, err := r.cfg.Journal.Runs(ctx)
	if err != nil || len(held) != 0 || reports.Load() != 2 {
		t.Fatal("explicit stop failed to settle original request", held, reports.Load(), err)
	}
}

func TestRetainedReconciliationContinueDuringReport(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t)
	r.cfg.Workers = &completionWorkers{}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	env, err := r.newEnvelope(protocol.OpRunsRelease, runTarget(as), protocol.ReleasePayload{Reason: "original physical exit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var helloCalls atomic.Int64
	var reports atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var request protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		if request.Operation == protocol.OpRunsRelease {
			if reports.Add(1) == 1 {
				close(entered)
				<-release
			}
		}
		if request.Operation == protocol.OpRunnerHello {
			verdict, desired := protocol.VerdictRetain, protocol.DesiredRun
			if helloCalls.Add(1) > 1 {
				verdict, desired = protocol.VerdictContinue, protocol.DesiredPaused
			}
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: verdict, Desired: desired}}}
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.start(ctx, as, r.cfg.Now())
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("report did not start")
	}
	if err := r.freshHello(ctx); err != nil {
		close(release)
		t.Fatal(err)
	}
	// Reporting executors are deliberately not awaited by waitIdleFenced.
	r.waitIdleFenced(ctx)
	if err := r.freshHello(ctx); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	r.wg.Wait()
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return
	}
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Fenced && r.lookup(as.RunID) == nil && len(requests) != 0 {
		t.Fatalf("Continue left orphaned pending report: phase=%s fenced=%v active=%v requests=%d", run.Phase, run.Fenced, r.lookup(as.RunID), len(requests))
	}
}

func TestRetainedReconciliationDoneDuringHello(t *testing.T) {
	r, as, _, _ := retainedRunnerFixture(t)
	r.cfg.Workers = &completionWorkers{}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	env, err := r.newEnvelope(protocol.OpRunsRelease, runTarget(as), protocol.ReleasePayload{Reason: "original physical exit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
		t.Fatal(err)
	}
	other := as
	other.RunID, other.LeaseID = "unrelated", "unrelated-lease"
	if err := r.cfg.Journal.AcceptAssignment(ctx, other, ""); err != nil {
		t.Fatal(err)
	}
	otherCtx, otherCancel := context.WithCancelCause(ctx)
	defer otherCancel(nil)
	otherActive := &active{as: other, ctx: otherCtx, cancel: otherCancel, desired: protocol.DesiredPaused, resume: make(chan struct{}, 1)}
	r.active[other.RunID] = otherActive
	reportEntered, reportRelease := make(chan struct{}), make(chan struct{})
	helloEntered, helloRelease := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var request protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		if request.Operation == protocol.OpRunsRelease {
			close(reportEntered)
			<-reportRelease
		}
		if request.Operation == protocol.OpRunnerHello {
			close(helloEntered)
			<-helloRelease
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{
				{RunID: as.RunID, Verdict: protocol.VerdictRetain, Desired: protocol.DesiredRun},
				{RunID: other.RunID, Verdict: protocol.VerdictContinue, Desired: protocol.DesiredRun},
			}}
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.start(ctx, as, r.cfg.Now())
	select {
	case <-reportEntered:
	case <-ctx.Done():
		t.Fatal("report did not start")
	}
	helloDone := make(chan error, 1)
	go func() { helloDone <- r.freshHello(ctx) }()
	select {
	case <-helloEntered:
	case <-ctx.Done():
		close(reportRelease)
		t.Fatal("Hello did not start")
	}
	close(reportRelease)
	r.wg.Wait()
	close(helloRelease)
	if err := <-helloDone; err != nil {
		t.Fatalf("completed retained entry aborted unrelated reconciliation: hello=%v unrelated desired=%s deadline=%v", err, otherActive.desired, otherActive.deadline)
	}
}
