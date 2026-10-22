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
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type completionWorkers struct {
	starts     int
	result     harness.WorkerResult
	onRelease  func()
	onWait     func()
	releaseErr func() error
}

func (w *completionWorkers) Start(_ context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	if spec.OnThread != nil {
		if err := spec.OnThread("owned-thread"); err != nil {
			return nil, err
		}
	}
	events := make(chan harness.WorkerEvent)
	close(events)
	return completionWorker{events: events, result: w.result, onWait: w.onWait}, nil
}

func (w *completionWorkers) Release(_ context.Context, _ string, thread string) error {
	if thread != "owned-thread" {
		return errors.New("unexpected terminal identity")
	}
	if w.onRelease != nil {
		w.onRelease()
	}
	if w.releaseErr != nil {
		return w.releaseErr()
	}
	return nil
}

func TestTransientStopErrorRetainsCompletedArtifact(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_stop_retry", TaskID: "task_stop_retry", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "operation already completed"}}})
	if err := j.WorkerDone(ctx, as.RunID, body); err != nil {
		t.Fatal(err)
	}
	var operations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		operations = append(operations, env.Operation)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	calls := 0
	workers := &completionWorkers{releaseErr: func() error {
		calls++
		if calls == 1 {
			return errors.New("temporary provider read failure")
		}
		return nil
	}}
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: completionWorkspaces{}, RetryDelay: time.Nanosecond})
	if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0] != protocol.OpRunsSubmitArtifact || workers.starts != 0 || calls < 2 {
		t.Fatalf("completed outcome lost: operations=%v starts=%d stops=%d", operations, workers.starts, calls)
	}
}

type completionSnapshots struct {
	completionWorkspaces
	calls int
}

func (w *completionSnapshots) Snapshot(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	w.calls++
	return protocol.ArtifactPayload{}, nil
}

func TestRecoveredCompletedOrcaTurnYieldsToManualOwnerAndUsesExplicitHandback(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_owner", TaskID: "task_owner", Role: protocol.RoleImplement, Spec: json.RawMessage(`{"deliverables":["report"]}`), Workspace: protocol.AssignmentWorkspace{Mounts: []protocol.AssignedMount{{Path: "repo"}}}}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, `{"terminal":"term_owner","pane":"tab:1","worktree":"wt_original","turn":100,"said":"tend brief"}`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "completed before downtime"}}})
	if err := j.WorkerDone(ctx, as.RunID, body); err != nil {
		t.Fatal(err)
	}
	before, err := j.Run(ctx, as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	alive := true
	inspected := false
	authorizedHandback := false
	oc := &orca.Client{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		result := map[string]any{}
		switch args[0] + " " + args[1] {
		case "terminal list":
			terms := []orca.Terminal{}
			if alive {
				terms = append(terms, orca.Terminal{Handle: "term_owner", WorktreeID: "wt_original", TabID: "tab", LeafID: "1"})
			}
			result["terminals"] = terms
		case "worktree ps":
			inspected = true
			result["worktrees"] = []any{map[string]any{"worktreeId": "wt_original", "agents": []orca.Agent{{PaneKey: "tab:1", State: "working", Prompt: "owner investigation while runner was down", StateStartedAt: 300}}}}
		case "terminal close":
			if !authorizedHandback {
				t.Fatal("closed owner's terminal before handback")
			}
			alive = false
		default:
			t.Fatalf("completed task started another turn: %v", args)
		}
		return json.Marshal(map[string]any{"ok": true, "result": result})
	}}
	takeovers := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		if env.Operation == protocol.OpRunsReportTakeover {
			takeovers++
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	workers := orca.NewWorkers(orca.WorkerOptions{Client: oc, Now: clock.Now})
	spaces := &completionSnapshots{}
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Worker: protocol.WorkerOrca, Workers: workers, Workspaces: spaces})
	a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
	clock.Advance(time.Hour)
	if err := r.completeWork(ctx, ctx, as, a, body); !errors.Is(err, errHeld) {
		t.Fatalf("manual recovery not held: %v", err)
	}
	if !alive || !inspected || spaces.calls != 0 {
		t.Fatal("manual work was closed or collected into completed result")
	}
	held, err := j.Run(ctx, as.RunID)
	if err != nil || !held.Held || held.Phase != journal.PhaseWorkerDone || held.WorkerDoneAtMillis != before.WorkerDoneAtMillis {
		t.Fatalf("completion/hold state = %+v %v", held, err)
	}
	if err := r.settleTakeover(ctx, as, a); err != nil {
		t.Fatal(err)
	}
	if takeovers != 1 {
		t.Fatalf("takeover reports = %d", takeovers)
	}
	authorizedHandback = true
	r.applyDesired(ctx, a, protocol.DesiredRun, clock.Now())
	if err := r.completeWork(ctx, ctx, as, a, body); err != nil {
		t.Fatal(err)
	}
	if alive || spaces.calls != 1 {
		t.Fatal("explicit handback did not finalize exactly once")
	}
	finished, err := j.Run(ctx, as.RunID)
	if err != nil || finished.Phase != journal.PhaseSubmitting || finished.Held {
		t.Fatalf("finalized state = %+v %v", finished, err)
	}
}

type completionWorker struct {
	events chan harness.WorkerEvent
	result harness.WorkerResult
	onWait func()
}

func (w completionWorker) Events() <-chan harness.WorkerEvent { return w.events }
func (w completionWorker) Wait() (harness.WorkerResult, error) {
	if w.onWait != nil {
		w.onWait()
	}
	return w.result, nil
}

type completionWorkspaces struct{}

func (completionWorkspaces) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	return Prepared{}, nil
}
func (completionWorkspaces) Collect(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	return protocol.ArtifactPayload{}, errors.New("report task must not collect Git changes")
}
func (completionWorkspaces) Finish(context.Context, protocol.Assignment) error { return nil }

func TestCompletedWorkerResultSurvivesRestartAfterAgentStop(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			clock := testkit.NewClock()
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := journal.Open(context.Background(), path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			as := protocol.Assignment{RunID: "run_done", TaskID: "task_done", Role: role, RunToken: "credential", Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
			if err := j.AcceptAssignmentFromWorker(context.Background(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(context.Background(), as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			workers := &completionWorkers{result: harness.WorkerResult{Summary: "operation executed exactly once", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "operation completed"}, Files: []protocol.ArtifactFile{{FileID: "file_evidence", Name: "evidence.txt", Size: 8, SHA256: strings.Repeat("a", 64)}}}, Verdict: &protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"checked":"external state"}`)}}}
			var submitted protocol.Envelope
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunsSubmitArtifact || env.Operation == protocol.OpAcceptancesSubmit {
					submitted = env
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
			}))
			defer srv.Close()
			cfg := Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Workspaces: completionWorkspaces{}}
			r := New(cfg)
			active := &active{as: as, desired: protocol.DesiredRun}
			if err := r.work(context.Background(), context.Background(), as, active); err != nil {
				t.Fatal(err)
			}
			crashCtx, cancel := context.WithCancel(context.Background())
			workers.onRelease = func() {
				run, err := j.Run(context.Background(), as.RunID)
				if err != nil || run.Phase != journal.PhaseWorkerDone || len(run.WorkerResult) == 0 {
					t.Fatalf("agent stopped before result persisted: %+v %v", run, err)
				}
				var recorded harness.WorkerResult
				if err := json.Unmarshal(run.WorkerResult, &recorded); err != nil || recorded.Summary != workers.result.Summary {
					t.Fatalf("completed summary was not persisted: %+v %v", recorded, err)
				}
				cancel()
			}
			if err := r.execute(crashCtx, crashCtx, as.RunID, active); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = journal.Open(context.Background(), path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			cfg.Journal = j
			workers.onRelease = nil
			r = New(cfg)
			if err := r.execute(context.Background(), context.Background(), as.RunID, active); err != nil {
				t.Fatal(err)
			}
			if workers.starts != 1 {
				t.Fatalf("completed operation replayed %d times", workers.starts)
			}
			if _, err := j.Run(context.Background(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
				t.Fatalf("run not finished: %v", err)
			}
			if role == protocol.RoleImplement {
				var result protocol.ArtifactPayload
				if err := json.Unmarshal(submitted.Payload, &result); err != nil {
					t.Fatal(err)
				}
				if result.Report == nil || result.Report.Body != "operation completed" || len(result.Files) != 1 || result.Files[0].FileID != "file_evidence" {
					t.Fatalf("restored implementation result = %+v", result)
				}
			} else {
				var result protocol.VerdictPayload
				if err := json.Unmarshal(submitted.Payload, &result); err != nil {
					t.Fatal(err)
				}
				if result.Verdict != protocol.VerdictPassed || !strings.Contains(string(result.Evidence), "external state") {
					t.Fatalf("restored acceptance = %+v", result)
				}
			}
		})
	}
}

func TestInterruptedWorkerDoesNotPersistFalseCompletion(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_held", TaskID: "task_held", Role: protocol.RoleImplement}
	if err := j.AcceptAssignmentFromWorker(context.Background(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(context.Background(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	heldCtx, cancel := context.WithCancelCause(context.Background())
	workers := &completionWorkers{onWait: func() { cancel(errHeld) }}
	r := New(Config{Journal: j, Now: clock.Now, Workers: workers, Workspaces: completionWorkspaces{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := r.work(context.Background(), heldCtx, as, &active{as: as, desired: protocol.DesiredRun}); !errors.Is(err, errHeld) {
		t.Fatalf("held worker = %v", err)
	}
	run, err := j.Run(context.Background(), as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Phase != journal.PhaseStarted || len(run.WorkerResult) != 0 {
		t.Fatalf("held worker recorded as completed: %+v", run)
	}
}

func TestCompletedResultWaitsForOwnerHandbackBeforeStoppingAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_complete_held", TaskID: "task_held", Role: protocol.RoleImplement, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "already complete"}}})
	if err := j.WorkerDone(ctx, as.RunID, result); err != nil {
		t.Fatal(err)
	}
	var stopped atomic.Bool
	workers := &completionWorkers{onRelease: func() { stopped.Store(true) }}
	r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Workers: workers, Workspaces: completionWorkspaces{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	a := &active{as: as, desired: protocol.DesiredHeld, resume: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- r.completeWork(ctx, ctx, as, a, result) }()
	for {
		run, err := j.Run(ctx, as.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Held {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("completion did not wait for owner: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
			runtime.Gosched()
		}
	}
	if stopped.Load() {
		t.Fatal("closed the owner's held terminal")
	}
	r.applyDesired(ctx, a, protocol.DesiredRun, clock.Now())
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() || workers.starts != 0 {
		t.Fatalf("handback must finalize without a new turn: stopped=%v starts=%d", stopped.Load(), workers.starts)
	}
	run, err := j.Run(ctx, as.RunID)
	if err != nil || run.Phase != journal.PhaseSubmitting {
		t.Fatalf("result not retained for submission: %+v %v", run, err)
	}
}
