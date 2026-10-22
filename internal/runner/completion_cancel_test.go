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

func completedCancelAssignment(t *testing.T, j *journal.Journal, kind ...protocol.WorkerKind) (protocol.Assignment, json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	as := protocol.Assignment{RunID: "run_cancel_completed", TaskID: "task_cancel_completed", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
	origin := protocol.WorkerAppServer
	if len(kind) > 0 {
		origin = kind[0]
	}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", origin); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "already completed"}}})
	if err := j.WorkerDone(ctx, as.RunID, body); err != nil {
		t.Fatal(err)
	}
	return as, body
}

type cancelCompletedWorkers struct {
	completionWorkers
	release func(context.Context) error
}

func (w *cancelCompletedWorkers) Release(ctx context.Context, _ string, _ string) error {
	return w.release(ctx)
}

func TestCancelledCompletedWorkerReleasesOnceWithoutReplayingResult(t *testing.T) {
	for _, during := range []bool{false, true} {
		name := "before recovery"
		if during {
			name = "during completion check"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as, body := completedCancelAssignment(t, j)
			runCtx, stop := context.WithCancelCause(ctx)
			defer stop(nil)
			if !during {
				stop(errStopped)
			}
			attempts := 0
			var finalRequest string
			workers := &cancelCompletedWorkers{release: func(stopCtx context.Context) error {
				attempts++
				if during && attempts == 1 {
					stop(errStopped)
					return stopCtx.Err()
				}
				if err := stopCtx.Err(); err != nil {
					t.Errorf("final stop used cancelled run context: %v", err)
					return err
				}
				run, err := j.Run(ctx, as.RunID)
				if err != nil || run.Phase != journal.PhaseReleasing || string(run.WorkerResult) != string(body) {
					t.Fatalf("lost saved result during cancellation: %+v %v", run, err)
				}
				req, found, err := j.RequestForRun(ctx, as.RunID)
				if err != nil || !found || req.Envelope.Operation != protocol.OpRunsRelease {
					t.Fatalf("missing release intent: %+v %v", req, err)
				}
				if finalRequest == "" {
					finalRequest = req.Envelope.RequestID
				} else if finalRequest != req.Envelope.RequestID {
					t.Fatal("stop retry replaced the release request")
				}
				if attempts == 1 || during && attempts == 2 {
					return errors.New("temporary stop verification failure")
				}
				return nil
			}}
			var reported protocol.Envelope
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				_ = json.NewDecoder(q.Body).Decode(&reported)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer srv.Close()
			spaces := &completionSnapshots{}
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: spaces, RetryDelay: time.Millisecond})
			if err := r.execute(ctx, runCtx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			wantAttempts := 2
			if during {
				wantAttempts = 3
			}
			if reported.Operation != protocol.OpRunsRelease || reported.RequestID != finalRequest || attempts != wantAttempts || workers.starts != 0 || spaces.calls != 0 {
				t.Fatalf("cancelled result replayed: report=%+v attempts=%d starts=%d snapshots=%d", reported, attempts, workers.starts, spaces.calls)
			}
		})
	}
}

func TestHelloStoppedCompletedWorkerReleasesCancelledOrSupersededSpec(t *testing.T) {
	for _, reason := range []string{"cancelled", "spec_superseded"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as, body := completedCancelAssignment(t, j)
			stops := 0
			workers := &cancelCompletedWorkers{release: func(stopCtx context.Context) error {
				stops++
				if stopCtx.Err() != nil {
					t.Errorf("stop context cancelled: %v", stopCtx.Err())
					return stopCtx.Err()
				}
				run, err := j.Run(ctx, as.RunID)
				if err != nil || run.Phase != journal.PhaseReleasing || string(run.WorkerResult) != string(body) {
					t.Errorf("hello lost completed result: %+v %v", run, err)
				}
				return nil
			}}
			var final protocol.Envelope
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				result := json.RawMessage(`{}`)
				if env.Operation == protocol.OpRunnerHello {
					result, _ = json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped, Reason: reason}}})
				} else {
					final = env
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
			}))
			defer srv.Close()
			spaces := &completionSnapshots{}
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: spaces, RetryDelay: time.Millisecond})
			if err := r.hello(ctx); err != nil {
				t.Fatal(err)
			}
			r.wg.Wait()
			if stops != 1 || workers.starts != 0 || spaces.calls != 0 || final.Operation != protocol.OpRunsRelease {
				t.Fatalf("stopped hello collected or replayed work: stops=%d starts=%d snapshots=%d final=%+v", stops, workers.starts, spaces.calls, final)
			}
		})
	}
}

func TestCancelledCompletedHeldOrcaTerminalWaitsForOwnerToClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as, body := completedCancelAssignment(t, j, protocol.WorkerOrca)
	as.Desired = protocol.DesiredStopped
	if err := j.SetThread(ctx, as.RunID, `{"terminal":"term_owner","pane":"tab:1","worktree":"wt_owner","turn":100,"said":"tend brief"}`); err != nil {
		t.Fatal(err)
	}
	if err := j.SetHeld(ctx, as.RunID, true); err != nil {
		t.Fatal(err)
	}
	var ownerClosed atomic.Bool
	var queries atomic.Int32
	checked := make(chan struct{}, 1)
	oc := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] != "terminal" || args[1] != "list" {
			t.Errorf("cancel treated held terminal as handback: %v", args)
			return nil, errors.New("must not close or touch owner turn")
		}
		queries.Add(1)
		terms := []orca.Terminal{}
		if !ownerClosed.Load() {
			terms = append(terms, orca.Terminal{Handle: "term_owner", WorktreeID: "wt_owner", TabID: "tab", LeafID: "1"})
		}
		select {
		case checked <- struct{}{}:
		default:
		}
		return json.Marshal(map[string]any{"ok": true, "result": map[string]any{"terminals": terms}})
	}}
	var reported protocol.Envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		_ = json.NewDecoder(q.Body).Decode(&reported)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Worker: protocol.WorkerOrca, Workers: orca.NewWorkers(orca.WorkerOptions{Client: oc, Now: clock.Now}), Workspaces: completionWorkspaces{}, RetryDelay: time.Millisecond})
	r.start(ctx, as, clock.Now())
	defer r.wg.Wait()
	defer cancel()
	select {
	case <-checked:
	case <-ctx.Done():
		t.Fatal("held terminal never reconciled")
	}
	run, err := j.Run(ctx, as.RunID)
	if err != nil || run.Phase != journal.PhaseReleasing || !run.Held || string(run.WorkerResult) != string(body) {
		t.Fatalf("held cancellation lost result or ownership: %+v %v", run, err)
	}
	req, found, err := j.RequestForRun(ctx, as.RunID)
	if err != nil || !found {
		t.Fatalf("missing held release: %v", err)
	}
	if req.Envelope.Operation != protocol.OpRunsRelease {
		t.Fatalf("cancel became takeover: %+v", req)
	}
	a := r.lookup(as.RunID)
	if a == nil {
		t.Fatal("held finalization abandoned")
	}
	r.mu.Lock()
	desired := a.desired
	r.mu.Unlock()
	if desired != protocol.DesiredStopped {
		t.Fatalf("cancel became desired %s", desired)
	}
	ownerClosed.Store(true)
	r.wg.Wait()
	if reported.Operation != protocol.OpRunsRelease || reported.RequestID != req.Envelope.RequestID || queries.Load() < 2 {
		t.Fatalf("owner close did not release original intent: %+v queries=%d", reported, queries.Load())
	}
	stopped, err := j.HistoricalRun(ctx, as.RunID)
	if err != nil || !stopped.StoppedConfirmed || stopped.Phase != journal.PhaseDone {
		t.Fatalf("owner close not confirmed: %+v %v", stopped, err)
	}
}

type cancelledHeldWorkers struct {
	completionWorkers
	checks  int
	onCheck func()
}

func (w *cancelledHeldWorkers) ReleaseHistorical(ctx context.Context, id, thread string, held bool, at int64) error {
	w.checks++
	if w.onCheck != nil {
		w.onCheck()
	}
	if !held {
		return errors.New("cancel was treated as handback")
	}
	return harness.ErrHeld
}

func TestCancelledHeldCompletionIgnoresStaleDesiredWithoutHandback(t *testing.T) {
	for _, desired := range []protocol.RunDesired{protocol.DesiredRun, protocol.DesiredHeld} {
		t.Run(string(desired), func(t *testing.T) {
			base := context.Background()
			clock := testkit.NewClock()
			j, err := journal.Open(base, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as, _ := completedCancelAssignment(t, j)
			if err := j.SetHeld(base, as.RunID, true); err != nil {
				t.Fatal(err)
			}
			workers := &cancelledHeldWorkers{}
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Workers: workers, Workspaces: completionWorkspaces{}, RetryDelay: time.Millisecond})
			ctx, cancel := context.WithTimeout(base, 2*time.Second)
			defer cancel()
			workers.onCheck = func() {
				if workers.checks == 2 {
					cancel()
				}
			}
			runCtx, stop := context.WithCancelCause(ctx)
			stop(errStopped)
			if err := r.execute(ctx, runCtx, as.RunID, &active{as: as, desired: desired, resume: make(chan struct{}, 1)}); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			run, err := j.Run(base, as.RunID)
			if err != nil || run.Phase != journal.PhaseReleasing || !run.Held || run.StoppedConfirmed || workers.checks == 0 || workers.starts != 0 {
				t.Fatalf("cancel stalled in old control state or closed owned agent: %+v checks=%d starts=%d err=%v", run, workers.checks, workers.starts, err)
			}
			req, found, err := j.RequestForRun(base, as.RunID)
			if err != nil || !found || req.Envelope.Operation != protocol.OpRunsRelease {
				t.Fatalf("lost cancelled final request: %+v %v", req, err)
			}
		})
	}
}

func TestCancelledCompletionSettlesLateTakeoverWithoutUndoingStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as, _ := completedCancelAssignment(t, j, protocol.WorkerOrca)
	if err := j.SetThread(ctx, as.RunID, `{"terminal":"term_closed","pane":"tab:1","worktree":"wt_owner","turn":100,"said":"tend brief"}`); err != nil {
		t.Fatal(err)
	}
	oc := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] != "terminal" || args[1] != "list" {
			t.Errorf("stopped task launched or touched terminal: %v", args)
		}
		return []byte(`{"ok":true,"result":{"terminals":[]}}`), nil
	}}
	var ops []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		ops = append(ops, env.Operation)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Worker: protocol.WorkerOrca, Workers: orca.NewWorkers(orca.WorkerOptions{Client: oc, Now: clock.Now}), Workspaces: completionWorkspaces{}, RetryDelay: time.Millisecond})
	env, err := r.newEnvelope(protocol.OpRunsReportTakeover, runTarget(as), protocol.TakeoverPayload{Reason: "takeover acknowledged before cancellation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.PutRequest(ctx, journal.Request{Envelope: *env, RunID: as.RunID}); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancelCause(ctx)
	stop(errStopped)
	a := &active{as: as, ctx: runCtx, cancel: stop, desired: protocol.DesiredStopped, resume: make(chan struct{}, 1)}
	if err := r.execute(ctx, runCtx, as.RunID, a); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0] != protocol.OpRunsReportTakeover || ops[1] != protocol.OpRunsRelease || a.desired != protocol.DesiredStopped {
		t.Fatalf("late takeover reversed stop or stranded report: ops=%v desired=%s", ops, a.desired)
	}
	if _, err := j.Run(ctx, as.RunID); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("stopped completed run not finalized: %v", err)
	}
}
