package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type pauseWorkers struct {
	running  atomic.Bool
	starts   atomic.Int64
	releases atomic.Int64
	started  chan struct{}
	unknown  bool
}

func (w *pauseWorkers) Start(ctx context.Context, s harness.WorkerSpec) (harness.Worker, error) {
	w.starts.Add(1)
	w.running.Store(true)
	if err := s.OnThread("retained-thread"); err != nil {
		return nil, err
	}
	events := make(chan harness.WorkerEvent)
	go func() { <-ctx.Done(); w.running.Store(false); close(events) }()
	close(w.started)
	return handoffWorker{ctx: ctx, events: events}, nil
}
func (w *pauseWorkers) Release(context.Context, string, string) error {
	w.releases.Add(1)
	if w.unknown {
		return errors.New("physical stop could not be verified")
	}
	if w.running.Load() {
		return errors.New("process is still running")
	}
	return nil
}

func TestPauseHandoffPersistsStopBeforeReportAndPreservesWorkspace(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "unknown-stop"}[unknown], func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, PauseHandoffs: true}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			p := &protocol.PauseHandoff{RequestID: "original-pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}
			workers := &pauseWorkers{started: make(chan struct{}), unknown: unknown}
			var reports atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunsYieldPause {
					reports.Add(1)
					saved, err := j.Run(req.Context(), as.RunID)
					if err != nil || !saved.StoppedConfirmed || workers.running.Load() {
						t.Error("handoff lacks durable stop proof", err)
					}
					var sent protocol.YieldPausePayload
					if err := json.Unmarshal(env.Payload, &sent); err != nil {
						t.Error(err)
					}
					if sent.PauseHandoff != *p || sent.ThreadID != "retained-thread" {
						t.Error("original pause identity changed", sent)
					}
				}
				if env.Operation == protocol.OpRunsRelease || env.Operation == protocol.OpRunsSubmitArtifact {
					t.Error("pause became a failed or completed result", env.Operation)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			}))
			defer server.Close()
			r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				select {
				case <-workers.started:
				case <-ctx.Done():
					return
				}
				if err := j.RecordPause(ctx, as.RunID, p); err != nil {
					t.Error(err)
					return
				}
				r.applyDesired(ctx, a, protocol.DesiredPaused, clock.Now())
			}()
			err = r.execute(ctx, ctx, as.RunID, a)
			<-stopped
			if unknown {
				if err == nil || reports.Load() != 0 {
					t.Fatalf("unknown stop released slot: %v reports=%d", err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); err != nil {
					t.Fatal("lost retained original", err)
				}
			} else {
				if err != nil || reports.Load() != 1 {
					t.Fatal(err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
					t.Fatal("local slot retained", err)
				}
			}
			if workers.starts.Load() != 1 {
				t.Fatal("pause restarted model", workers.starts.Load())
			}
		})
	}
}

func TestPauseHandoffPreservesAlreadyCompletedResultWithoutRestartingWorker(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "completed", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredPaused, PauseHandoffs: true, Spec: json.RawMessage(`{"goal":"Record findings","deliverables":["report"]}`)}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Already verified findings"}}})
	if err := j.WorkerDone(t.Context(), as.RunID, raw); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordPause(t.Context(), as.RunID, &protocol.PauseHandoff{RequestID: "pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}); err != nil {
		t.Fatal(err)
	}
	var results atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation == protocol.OpRunsSubmitArtifact {
			results.Add(1)
		}
		if env.Operation == protocol.OpRunsYieldPause || env.Operation == protocol.OpRunsRelease {
			t.Error("completed result replaced by a pause or failure")
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	workers := &pauseWorkers{started: make(chan struct{})}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredPaused, resume: make(chan struct{}, 1)}); err != nil {
		t.Fatal(err)
	}
	if results.Load() != 1 || workers.starts.Load() != 0 {
		t.Fatal("known outcome was lost or rerun", results.Load(), workers.starts.Load())
	}
}

func TestPauseHandoffCannotReplaceResultSavedAfterStartSnapshot(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, PauseHandoffs: true}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	started, err := j.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.RecordPause(ctx, as.RunID, &protocol.PauseHandoff{RequestID: "pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"result":{"report":{"body":"verified work"}}}`)
	if err := j.WorkerDone(ctx, as.RunID, result); err != nil {
		t.Fatal(err)
	}
	r := New(Config{Journal: j, Now: clock.Now})
	if err := r.deferOwnedPauseStop(ctx, &active{desired: protocol.DesiredPaused}, started, harness.ErrOwnedWorkerUnconfirmed); err != nil {
		t.Fatalf("completed Run entered pause recovery: %v", err)
	}
	got, err := j.Run(ctx, as.RunID)
	if err != nil || got.Phase != journal.PhaseWorkerDone || string(got.WorkerResult) != string(result) {
		t.Fatalf("completed result changed: phase=%s result=%s err=%v", got.Phase, got.WorkerResult, err)
	}
}

func TestPauseHandoffRetainsHumanControlledExecution(t *testing.T) {
	for _, unknown := range []bool{false} {
		t.Run(map[bool]string{false: "stopped", true: "unknown-stop"}[unknown], func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, PauseHandoffs: true}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			p := &protocol.PauseHandoff{RequestID: "original-pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}
			workers := &pauseWorkers{started: make(chan struct{}), unknown: unknown}
			var reports atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunsYieldPause {
					reports.Add(1)
					saved, err := j.Run(req.Context(), as.RunID)
					if err != nil || !saved.StoppedConfirmed || workers.running.Load() {
						t.Error("handoff lacks durable stop proof", err)
					}
					var sent protocol.YieldPausePayload
					if err := json.Unmarshal(env.Payload, &sent); err != nil {
						t.Error(err)
					}
					if sent.PauseHandoff != *p || sent.ThreadID != "retained-thread" {
						t.Error("original pause identity changed", sent)
					}
				}
				if env.Operation == protocol.OpRunsRelease || env.Operation == protocol.OpRunsSubmitArtifact {
					t.Error("pause became a failed or completed result", env.Operation)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			}))
			defer server.Close()
			r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				select {
				case <-workers.started:
				case <-ctx.Done():
					return
				}
				if err := j.RecordPause(ctx, as.RunID, p); err != nil {
					t.Error(err)
					return
				}
				r.applyDesired(ctx, a, protocol.DesiredHeld, clock.Now())
			}()
			err = r.execute(ctx, ctx, as.RunID, a)
			<-stopped
			if unknown {
				if err == nil || reports.Load() != 0 {
					t.Fatalf("unknown stop released slot: %v reports=%d", err, reports.Load())
				}
				if _, err := j.Run(t.Context(), as.RunID); err != nil {
					t.Fatal("lost retained original", err)
				}
			} else {
				if reports.Load() != 0 {
					t.Fatal(err, reports.Load())
				}
				if saved, err := j.Run(t.Context(), as.RunID); err != nil || !saved.Held {
					t.Fatal("human controlled execution was discarded", saved, err)
				}
			}
			if workers.starts.Load() != 1 {
				t.Fatal("pause restarted model", workers.starts.Load())
			}
		})
	}
}

func TestPauseManagerFlushesRetainedEventsBeforeYield(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "manager", TaskID: "task", Role: protocol.RoleIssueManager, Desired: protocol.DesiredPaused, PauseHandoffs: true, Manager: &protocol.IssueManagerAssignment{IssueID: "issue"}}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordPause(t.Context(), as.RunID, &protocol.PauseHandoff{RequestID: "pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	event := &protocol.Envelope{RequestID: "event", Operation: protocol.OpManagerNativeEvent, Target: runTarget(as)}
	if err := j.SaveManagerEvent(t.Context(), as.RunID, event, json.RawMessage(`{"disconnected":true}`)); err != nil {
		t.Fatal(err)
	}
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		operations = append(operations, env.Operation)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), RetryDelay: time.Millisecond})
	if err := r.finishPause(t.Context(), as, &active{desired: protocol.DesiredPaused}); err != nil {
		t.Fatal(err)
	}
	if len(operations) < 2 || operations[0] != protocol.OpManagerNativeEvent {
		t.Fatalf("manager facts must reach server before ending Run; sent %v", operations)
	}
}

func TestPauseHandoffFinishesAfterExplicitHandback(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "original", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, PauseHandoffs: true}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordPause(t.Context(), as.RunID, &protocol.PauseHandoff{RequestID: "pause", IssueID: "issue", IssueRevision: 3, ControlRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, "original-held-thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	var reports atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(req.Body).Decode(&env)
		if env.Operation == protocol.OpRunsYieldPause {
			saved, err := j.Run(req.Context(), as.RunID)
			if err != nil || saved.Held || !saved.StoppedConfirmed {
				t.Error("handoff preceded confirmed stop after handback", saved, err)
			}
			reports.Add(1)
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	workers := &pauseWorkers{started: make(chan struct{})}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
	a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
	r.applyDesired(t.Context(), a, protocol.DesiredHeld, clock.Now())
	// Owner explicitly gives the Task back; Issue pause still requests stop.
	r.applyDesired(t.Context(), a, protocol.DesiredPaused, clock.Now())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := r.execute(ctx, ctx, as.RunID, a); err != nil {
		t.Fatal(err)
	}
	if workers.starts.Load() != 0 || workers.releases.Load() != 1 {
		t.Fatal("handback restarted work or skipped physical closure", workers.starts.Load(), workers.releases.Load())
	}
	if reports.Load() != 1 {
		saved, _ := j.Run(t.Context(), as.RunID)
		t.Fatalf("no pause handoff after explicit handback; reports=%d phase=%s held=%v", reports.Load(), saved.Phase, saved.Held)
	}
}

func TestRetainedPausingHeartbeatKeepsControlReachable(t *testing.T) {
	ctx := t.Context()
	r, as, _, _ := retainedRunnerFixture(t)
	if err := r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhasePausing); err != nil {
		t.Fatal(err)
	}
	if err := r.retainRun(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var heartbeat protocol.HeartbeatPayload
		if err := json.Unmarshal(env.Payload, &heartbeat); err != nil || len(heartbeat.Runs) != 0 {
			t.Error("heartbeat tried to renew the maintenance-retained Run", heartbeat.Runs, err)
		}
		result, _ := json.Marshal(protocol.HeartbeatResult{LeaseTTLMillis: 60000})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: result}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.heartbeat(ctx)
	select {
	case <-r.reconcile:
	default:
		t.Fatal("retained pause cannot receive later Hello control")
	}
	retained, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil || !retained.Fenced || retained.Phase != journal.PhasePausing {
		t.Fatal("Hello wake released maintenance isolation", retained, err)
	}
}

type blockingPauseObserver struct {
	ownedRecoveryWorker
	entered, proceed chan struct{}
}

func (w *blockingPauseObserver) ObserveOwnedWorkerStop(ctx context.Context, _ harness.WorkerSpec) error {
	close(w.entered)
	select {
	case <-w.proceed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPauseSettlesStopFromPreviousExecutorHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		observing bool
	}{
		{name: "during pause observation", observing: true},
		{name: "between executors"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			r, as, _, _, _ := implementationReconcileFixture(t)
			worker := &blockingPauseObserver{entered: make(chan struct{}), proceed: make(chan struct{})}
			r.cfg.Workers = worker
			pause := &protocol.PauseHandoff{RequestID: "pause", IssueID: "issue", IssueRevision: 2, ControlRevision: 1}
			if err := r.cfg.Journal.RecordPause(ctx, as.RunID, pause); err != nil {
				t.Fatal(err)
			}
			requested, deliver, received := make(chan struct{}), make(chan struct{}), make(chan struct{})
			finals := make(chan protocol.Envelope, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				var result any
				switch env.Operation {
				case protocol.OpRunnerHeartbeat:
					var hb protocol.HeartbeatPayload
					if err := json.Unmarshal(env.Payload, &hb); err != nil || len(hb.Runs) != 1 ||
						hb.Runs[0].RunID != as.RunID || hb.Runs[0].LeaseID != as.LeaseID {
						t.Error("previous executor heartbeat lost the original lease", hb.Runs, err)
						return
					}
					close(requested)
					select {
					case <-deliver:
					case <-q.Context().Done():
						return
					}
					result = protocol.HeartbeatResult{LeaseTTLMillis: 60000, Runs: []protocol.LeaseState{
						{RunID: as.RunID, Desired: protocol.DesiredStopped},
					}}
				case protocol.OpRunnerHello:
					var hello protocol.HelloPayload
					if err := json.Unmarshal(env.Payload, &hello); err != nil || len(hello.Held) != 1 ||
						hello.Held[0].RunID != as.RunID || hello.Held[0].LeaseID != as.LeaseID {
						t.Error("Stop reconciliation lost the original lease", hello.Held, err)
						return
					}
					result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{
						{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped},
					}}
				case protocol.OpRunsYieldPause, protocol.OpRunsRelease, protocol.OpRunsSubmitArtifact:
					finals <- env
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
			}))
			defer func() {
				cancel()
				server.Close()
				r.wg.Wait()
			}()
			r.c.base, r.c.http = server.URL, server.Client()

			previousCtx, previousCancel := context.WithCancelCause(ctx)
			defer previousCancel(nil)
			r.active[as.RunID] = &active{as: as, ctx: previousCtx, cancel: previousCancel, desired: protocol.DesiredPaused}
			go func() { r.heartbeat(ctx); close(received) }()
			select {
			case <-requested:
			case <-ctx.Done():
				t.Fatal("previous executor did not send its heartbeat")
			}
			r.mu.Lock()
			delete(r.active, as.RunID)
			r.mu.Unlock()
			if parked, err := r.cfg.Journal.TryBeginPauseStop(ctx, as.RunID, 1); err != nil || !parked {
				t.Fatal("original attempt did not park", parked, err)
			}
			if tc.observing {
				paused := as
				paused.Desired = protocol.DesiredPaused
				r.start(ctx, paused, r.cfg.Now())
				select {
				case <-worker.entered:
				case <-ctx.Done():
					t.Fatal("pause observer did not start")
				}
			}
			close(deliver)
			select {
			case <-received:
			case <-ctx.Done():
				t.Fatal("Stop did not reach the runner")
			}
			close(worker.proceed)
			r.wg.Wait()
			select {
			case <-r.reconcile:
			default:
				t.Fatal("in-flight Stop did not request authoritative reconciliation")
			}
			if len(finals) != 0 {
				t.Fatal("created a final request before reconciling Stop")
			}
			if err := r.hello(ctx); err != nil {
				t.Fatal(err)
			}
			r.wg.Wait()
			if worker.starts != 0 || len(finals) != 1 {
				t.Fatal("Stop did not settle the original Run exactly once", worker.starts, len(finals))
			}
			env := <-finals
			if env.Operation != protocol.OpRunsRelease || env.Target.ID != as.RunID {
				t.Fatal("Stop did not follow the original Run release path", env)
			}
			wantStops := 1
			if tc.observing {
				wantStops = 0
			}
			if len(worker.stops) != wantStops {
				t.Fatal("Stop did not preserve existing physical proof", worker.stops)
			}
		})
	}
}

type timeoutPauseObserver struct{ ownedRecoveryWorker }

func (*timeoutPauseObserver) ObserveOwnedWorkerStop(context.Context, harness.WorkerSpec) error {
	return context.DeadlineExceeded
}

func TestBlockedPauseObservationAllowsAuthoritativeStop(t *testing.T) {
	ctx := t.Context()
	r, _, _, _ := retainedRunnerFixture(t)
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "paused-run", LeaseID: "lease", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredPaused, PauseHandoffs: true}
	if err := r.cfg.Journal.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cfg.Journal.NextAttempt(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhasePausing); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetThread(ctx, as.RunID, "original-thread"); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.RecordPause(ctx, as.RunID, &protocol.PauseHandoff{RequestID: "pause", IssueID: "issue", IssueRevision: 1, ControlRevision: 1}); err != nil {
		t.Fatal(err)
	}
	worker := &timeoutPauseObserver{}
	r.cfg.Workers = worker
	r.cfg.Workspaces = &ownedRecoveryWorkspace{prepared: Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := r.recoverPauseStop(ctx, ctx, &active{desired: protocol.DesiredPaused}, run)
	pending, ok := errors.AsType[*pauseStopPending](err)
	if recovered || !ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.deferPauseRetry(as.RunID, pending)
	r.mu.Unlock()
	var releases atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		switch env.Operation {
		case protocol.OpRunnerHello:
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}}}
		case protocol.OpRunsRelease:
			releases.Add(1)
		case protocol.OpRunsYieldPause:
			t.Error("authoritative Stop was relabelled as a pause receipt")
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.hello(ctx); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	if len(worker.stops) != 1 || worker.stops[0].RunID != as.RunID || worker.stops[0].Thread != run.Thread ||
		worker.starts != 0 || releases.Load() != 1 {
		t.Fatalf("Stop did not finalize the original attempt once: stops=%+v starts=%d releases=%d", worker.stops, worker.starts, releases.Load())
	}
}
