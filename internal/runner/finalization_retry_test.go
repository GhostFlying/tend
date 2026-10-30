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
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFinalRequestStopRetryKeepsExecutorAndHeartbeat(t *testing.T) {
	for _, phase := range []string{journal.PhaseSubmitting, journal.PhaseReleasing} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "run_retry", TaskID: "task_retry", LeaseID: "lease_retry", Desired: protocol.DesiredRun}
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
				t.Fatal(err)
			}
			op := protocol.OpRunsSubmitArtifact
			if phase == journal.PhaseReleasing {
				op = protocol.OpRunsRelease
			}
			original := protocol.Envelope{RequestID: "req_original", Operation: op, Target: runTarget(as), Payload: json.RawMessage(`{"reason":"original failure","report":{"body":"saved result"}}`)}
			if err := j.Finish(ctx, as.RunID, phase, journal.Request{RunID: as.RunID, Envelope: original}); err != nil {
				t.Fatal(err)
			}
			retrying := make(chan struct{})
			allow := make(chan struct{})
			var stops atomic.Int32
			workers := &completionWorkers{releaseErr: func() error {
				n := stops.Add(1)
				if n == 1 {
					return errors.New("temporary terminal-list failure")
				}
				if n == 2 {
					close(retrying)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-allow:
					return nil
				}
			}}
			reported := make(chan protocol.Envelope, 1)
			heartbeats := make(chan protocol.HeartbeatPayload, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				result := json.RawMessage(`{}`)
				if env.Operation == protocol.OpRunnerHeartbeat {
					var p protocol.HeartbeatPayload
					_ = json.Unmarshal(env.Payload, &p)
					heartbeats <- p
					result, _ = json.Marshal(protocol.HeartbeatResult{LeaseTTLMillis: 60000, Runs: []protocol.LeaseState{{RunID: as.RunID, Renewed: true, Desired: protocol.DesiredRun}}})
				} else {
					reported <- env
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
			}))
			defer srv.Close()
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: completionWorkspaces{}, RetryDelay: time.Millisecond})
			defer r.wg.Wait()
			defer cancel()
			r.start(ctx, as, clock.Now())
			select {
			case <-retrying:
			case <-time.After(2 * time.Second):
				t.Fatal("finalizing executor did not retry stop verification")
			}
			a := r.lookup(as.RunID)
			if a == nil {
				t.Fatal("executor abandoned pending final request")
			}
			if r.isReporting(a) {
				t.Fatal("unverified agent skipped fencing during retry")
			}
			req, found, err := j.RequestForRun(ctx, as.RunID)
			if err != nil || !found || req.Envelope.RequestID != original.RequestID {
				t.Fatalf("lost final request: %+v %v", req, err)
			}
			r.heartbeat(ctx)
			select {
			case hb := <-heartbeats:
				if len(hb.Runs) != 1 || hb.Runs[0].RunID != as.RunID {
					t.Fatalf("pending run not renewed: %+v", hb)
				}
			case <-time.After(time.Second):
				t.Fatal("no heartbeat")
			}
			close(allow)
			select {
			case got := <-reported:
				if got.RequestID != original.RequestID || got.Operation != op || string(got.Payload) != string(original.Payload) {
					t.Fatalf("changed final intent: %+v", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("original final report not sent")
			}
			r.wg.Wait()
			if workers.starts != 0 || stops.Load() != 2 {
				t.Fatalf("replayed worker or redundant stop: starts=%d stops=%d", workers.starts, stops.Load())
			}
			if _, err := j.Run(ctx, as.RunID); !errors.Is(err, journal.ErrNotFound) {
				t.Fatalf("run not completed: %v", err)
			}
		})
	}
}

func TestFinalRequestConfirmedStopSkipsExternalReverification(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "run_confirmed", Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	workers := &completionWorkers{onRelease: func() { t.Fatal("queried an already confirmed stopped terminal") }}
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: completionWorkspaces{}})
	if err := r.finish(ctx, as, journal.PhaseSubmitting, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{}); err != nil {
		t.Fatal(err)
	}
	if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
}

// guardedFinalWorkers rejects manual activity until the owner explicitly hands
// back the saved thread; its ordinary Release must never bypass that check.
type guardedFinalWorkers struct {
	completionWorkers
	manual bool
	closes int
	cutoff int64
}

func (w *guardedFinalWorkers) Release(context.Context, string, string) error {
	return errors.New("unguarded release")
}
func (w *guardedFinalWorkers) ReleaseCompleted(ctx context.Context, id, thread string, at int64, handBack bool) error {
	return w.guard(at, handBack)
}
func (w *guardedFinalWorkers) ReleaseFinalizing(ctx context.Context, id, thread string, at int64, handBack bool, saveThread func(string) error) error {
	return w.guard(at, handBack)
}
func (w *guardedFinalWorkers) guard(at int64, handBack bool) error {
	w.cutoff = at
	if w.manual && !handBack {
		return harness.ErrHeld
	}
	w.closes++
	return nil
}

func TestRecoveredFinalRequestPreservesIntentThroughOwnerHandback(t *testing.T) {
	for _, phase := range []string{journal.PhaseSubmitting, journal.PhaseReleasing} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "run_legacy", Desired: protocol.DesiredRun}
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
				t.Fatal(err)
			}
			op := protocol.OpRunsSubmitArtifact
			if phase == journal.PhaseReleasing {
				op = protocol.OpRunsRelease
			}
			env := protocol.Envelope{RequestID: "req_saved", Operation: op, Target: runTarget(as), Payload: json.RawMessage(`{"reason":"saved"}`)}
			if err := j.Finish(ctx, as.RunID, phase, journal.Request{RunID: as.RunID, Envelope: env}); err != nil {
				t.Fatal(err)
			}
			original, _, _ := j.RequestForRun(ctx, as.RunID)
			clock.Advance(time.Hour)
			workers := &guardedFinalWorkers{manual: true}
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Workers: workers, Workspaces: completionWorkspaces{}})
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			if err := r.awaitCompletedStop(ctx, ctx, as, a); !errors.Is(err, errHeld) {
				t.Fatalf("manual agent not held: %v", err)
			}
			if workers.closes != 0 || workers.cutoff != original.CreatedAtMillis {
				t.Fatalf("unsafe recovery: %+v", workers)
			}
			var sent []protocol.Envelope
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				sent = append(sent, env)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer srv.Close()
			r.c.base = srv.URL
			r.c.http = srv.Client()
			if err := r.settleTakeover(ctx, as, a); err != nil {
				t.Fatal(err)
			}
			r.applyDesired(ctx, a, protocol.DesiredRun, clock.Now())
			if err := r.execute(ctx, ctx, as.RunID, a); err != nil {
				t.Fatal(err)
			}
			if workers.closes != 1 || workers.starts != 0 || len(sent) != 2 || sent[1].RequestID != env.RequestID || sent[1].Operation != op {
				t.Fatalf("handback changed operation: closed=%d starts=%d sent=%+v", workers.closes, workers.starts, sent)
			}
		})
	}
}

type failedFinalWorkers struct{ guardedFinalWorkers }

func (w *failedFinalWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	if err := spec.OnThread("owned-thread"); err != nil {
		return nil, err
	}
	return nil, errors.New("provider failed after creating the original turn")
}

func TestCurrentWorkerFailureFinalizesWithoutOwnerHandback(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "run_failed", Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	var got protocol.Envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_ = json.NewDecoder(q.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	workers := &failedFinalWorkers{}
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: completionWorkspaces{}})
	if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
	if workers.starts != 1 || workers.closes != 1 || workers.cutoff != clock.Now().UnixMilli() || got.Operation != protocol.OpRunsRelease {
		t.Fatalf("failure was held or replayed: workers=%+v report=%+v", workers, got)
	}
	run, err := j.HistoricalRun(ctx, as.RunID)
	if err != nil || len(run.WorkerResult) != 0 || run.Phase != journal.PhaseDone {
		t.Fatalf("failure became successful completion: %+v %v", run, err)
	}
}

type unknownLaunchFinalWorkers struct {
	*orca.Workers
	thread string
	starts int
}

func (w *unknownLaunchFinalWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	if err := spec.OnThread(w.thread); err != nil {
		return nil, err
	}
	return nil, errors.New("launch receipt lost")
}

func TestFailedUnknownLaunchFinalizationResolvesAndJournalsExactTerminal(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "run_unknown_launch", Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	thread := `{"worktree":"wt_own","marker":"marker_own","launch":"launch_uncertain","asked":100,"turn":100,"said":"tend brief marker_own"}`
	alive := true
	queries := 0
	closes := 0
	oc := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		result := map[string]any{}
		switch args[0] + " " + args[1] {
		case "terminal list":
			terms := []orca.Terminal{}
			if alive {
				terms = append(terms, orca.Terminal{Handle: "term_own", WorktreeID: "wt_own", TabID: "tab", LeafID: "1"})
			}
			result["terminals"] = terms
		case "worktree ps":
			queries++
			agents := []orca.Agent{}
			if queries > 1 {
				agents = append(agents, orca.Agent{PaneKey: "tab:1", Prompt: "tend brief marker_own", State: "working", StateStartedAt: 100})
			}
			result["worktrees"] = []any{map[string]any{"worktreeId": "wt_own", "agents": agents}}
		case "terminal close":
			run, err := j.Run(ctx, as.RunID)
			if err != nil || !json.Valid([]byte(run.Thread)) {
				t.Fatalf("missing resolved journal: %+v %v", run, err)
			}
			var saved struct{ Terminal, Pane, Launch string }
			_ = json.Unmarshal([]byte(run.Thread), &saved)
			if saved.Terminal != "term_own" || saved.Pane != "tab:1" || saved.Launch != "" {
				t.Fatalf("closed before identity persisted: %s", run.Thread)
			}
			if args[3] != "term_own" {
				t.Fatalf("wrong close: %v", args)
			}
			closes++
			alive = false
		default:
			t.Fatalf("replayed launch or prompt: %v", args)
		}
		return json.Marshal(map[string]any{"ok": true, "result": result})
	}}
	workers := &unknownLaunchFinalWorkers{Workers: orca.NewWorkers(orca.WorkerOptions{Client: oc, Now: clock.Now}), thread: thread}
	var reported protocol.Envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_ = json.NewDecoder(q.Body).Decode(&reported)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Worker: protocol.WorkerOrca, Workers: workers, Workspaces: completionWorkspaces{}, RetryDelay: time.Millisecond})
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.execute(bounded, bounded, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
	if alive || closes != 1 || workers.starts != 1 || queries < 3 || reported.Operation != protocol.OpRunsRelease {
		t.Fatalf("unknown launch not safely finalized: alive=%t closes=%d starts=%d queries=%d report=%+v", alive, closes, workers.starts, queries, reported)
	}
}

func TestStoppedRunRetriesItsJournaledReleaseUntilProcessProof(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "run_stopped", Desired: protocol.DesiredStopped}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	workers := &completionWorkers{releaseErr: func() error {
		calls++
		if calls == 1 {
			return errors.New("temporary stop failure")
		}
		return nil
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: completionWorkspaces{}, RetryDelay: time.Millisecond})
	if err := r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped by tend"}); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancelCause(ctx)
	stop(errStopped)
	if err := r.execute(ctx, runCtx, as.RunID, &active{as: as, desired: protocol.DesiredStopped}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || workers.starts != 0 {
		t.Fatalf("stop aborted its own finalization: calls=%d starts=%d", calls, workers.starts)
	}
	if _, err := j.Run(ctx, as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("stopped release left pending: %v", err)
	}
}
