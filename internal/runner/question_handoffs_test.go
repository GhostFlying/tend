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

type handoffWorkers struct {
	running atomic.Bool
	starts  atomic.Int64
}

func (w *handoffWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.starts.Add(1)
	w.running.Store(true)
	if err := spec.OnThread("original-thread"); err != nil {
		return nil, err
	}
	events := make(chan harness.WorkerEvent)
	go func() {
		defer close(events)
		defer w.running.Store(false)
		_, _ = spec.AskOwner(ctx, protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Need a decision", Evidence: "Investigated original scope"})
		<-ctx.Done()
	}()
	return handoffWorker{ctx: ctx, events: events}, nil
}
func (w *handoffWorkers) Release(context.Context, string, string) error {
	if w.running.Load() {
		return errors.New("execution has not stopped")
	}
	return nil
}

type handoffWorker struct {
	ctx    context.Context
	events chan harness.WorkerEvent
}

func (w handoffWorker) Events() <-chan harness.WorkerEvent { return w.events }
func (w handoffWorker) Wait() (harness.WorkerResult, error) {
	return harness.WorkerResult{}, context.Cause(w.ctx)
}

func TestQuestionHandoffStopsBeforeReportAndRecoversWithoutModel(t *testing.T) {
	for _, recoverQuestion := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-question", true: "lost-create-receipt"}[recoverQuestion], func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run-handoff", TaskID: "task-handoff", Role: protocol.RoleImplement, WorkerQuestions: true, QuestionHandoffs: true, Desired: protocol.DesiredRun}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			workers := &handoffWorkers{}
			var final atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				question := protocol.WorkerQuestion{DecisionID: "decision-original", RunID: as.RunID, Status: "open", QuestionKey: "scope", Question: "Need a decision", Evidence: "Investigated original scope"}
				if req.Method == http.MethodGet {
					questions := []protocol.WorkerQuestion{}
					if recoverQuestion {
						questions = append(questions, question)
					}
					_ = json.NewEncoder(w).Encode(questions)
					return
				}
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				result := json.RawMessage(`{}`)
				switch env.Operation {
				case protocol.OpRunsAskOwner:
					result, _ = json.Marshal(question)
				case protocol.OpRunsYieldQuestion:
					if workers.running.Load() {
						t.Error("reported handoff while model was running")
					}
					saved, err := j.Run(req.Context(), as.RunID)
					if err != nil || !saved.StoppedConfirmed {
						t.Error("reported without durable process stop proof", err)
					}
					final.Add(1)
				case protocol.OpRunsRelease, protocol.OpRunsSubmitArtifact, protocol.OpAcceptancesSubmit:
					t.Error("question was reported as failure or delivery", env.Operation)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
			}))
			defer server.Close()
			r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			if final.Load() != 1 {
				t.Fatal("missing final handoff receipt", final.Load())
			}
			if recoverQuestion && workers.starts.Load() != 0 {
				t.Fatal("receipt recovery restarted the model")
			}
			if _, err := j.Run(t.Context(), as.RunID); !errors.Is(err, journal.ErrNotFound) {
				t.Fatal("handoff did not release the local execution", err)
			}
		})
	}
}

type handoffCaptureWorker struct{ thread string }

func (*handoffCaptureWorker) NativeLiveMessages() {}
func (w *handoffCaptureWorker) Start(_ context.Context, s harness.WorkerSpec) (harness.Worker, error) {
	w.thread = s.Thread
	return nil, harness.ErrWorkerNotStarted
}
func TestQuestionHandoffCentralThreadPropagation(t *testing.T) {
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	const thread = `{"kind":"central-traex","run_id":"old","thread":"original-session","executor_stopped":true}`
	as := protocol.Assignment{RunID: "new", TaskID: "task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, QuestionHandoffs: true, QuestionContinuation: &protocol.QuestionContinuation{RunID: "old", ThreadID: thread}}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	workers := &handoffCaptureWorker{}
	r := New(Config{Journal: j, Worker: protocol.WorkerOrca, Workers: workers, Now: clock.Now, IDs: testkit.NewIDGen(clock, 2), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}})
	_ = r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	if workers.thread != thread {
		t.Fatalf("central adapter received thread %q; want predecessor binding", workers.thread)
	}
}

func TestFault_QuestionHandoffRecoveryPreservesStopProof(t *testing.T) {
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{RunID: "run-recovered", TaskID: "task", Role: protocol.RoleImplement, WorkerQuestions: true, QuestionHandoffs: true, Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, "stopped-native-thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	final := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]protocol.WorkerQuestion{{DecisionID: "original", RunID: as.RunID, Status: "open"}})
			return
		}
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
		}
		if env.Operation == protocol.OpRunsYieldQuestion {
			final++
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	}))
	defer server.Close()
	workers := &originProbeWorkers{}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: server.URL, HTTPClient: server.Client(), Workers: workers, Workspaces: prelaunchSpaces{dir: t.TempDir(), state: t.TempDir()}, RetryDelay: time.Millisecond})
	if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal("handoff recovery lost durable stop proof", err)
	}
	if workers.starts != 0 || final != 1 {
		t.Fatal("handoff restarted the model or lost its final receipt", workers.starts, final)
	}
}
