package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type implementationReconcileWorkers struct {
	ownedRecoveryWorker
	result      harness.WorkerResult
	recoveryErr error
	seen        []harness.WorkerSpec
}

func (w *implementationReconcileWorkers) ReconcileImplementationResult(_ context.Context, spec harness.WorkerSpec) (harness.WorkerResult, error) {
	w.seen = append(w.seen, spec)
	return w.result, w.recoveryErr
}

type implementationReconcileSpaces struct {
	ownedRecoveryWorkspace
	finishes int
}

func (w *implementationReconcileSpaces) Finish(context.Context, protocol.Assignment) error {
	w.finishes++
	return nil
}

func submittedImplementationResult() harness.WorkerResult {
	artifact := &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "original completed investigation"}}
	for i := range 13 {
		artifact.Files = append(artifact.Files, protocol.ArtifactFile{
			FileID: fmt.Sprintf("original-file-%d", i), Name: fmt.Sprintf("evidence-%d.txt", i),
			Size: int64(i + 1), SHA256: strings.Repeat(fmt.Sprintf("%x", i), 64),
		})
	}
	return harness.WorkerResult{Result: artifact, Summary: "original final summary"}
}

func implementationReconcileFixture(t *testing.T) (*Runner, protocol.Assignment, *implementationReconcileWorkers, *implementationReconcileSpaces, string) {
	t.Helper()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	as := protocol.Assignment{RunID: "original-result-run", LeaseID: "original-result-lease", TaskID: "original-result-task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report","files"]}`)}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, "original-result-thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	workers := &implementationReconcileWorkers{result: submittedImplementationResult()}
	spaces := &implementationReconcileSpaces{ownedRecoveryWorkspace: ownedRecoveryWorkspace{prepared: Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}}}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Workers: workers, Workspaces: spaces, Now: clock.Now, IDs: testkit.NewIDGen(clock, 9), Log: slog.New(slog.DiscardHandler), RetryDelay: time.Millisecond})
	return r, as, workers, spaces, path
}

func TestImplementationResultRecoverySubmitsOriginalFilesWithoutNativeWork(t *testing.T) {
	r, as, workers, spaces, _ := implementationReconcileFixture(t)
	before, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// There are no pending native events: successful acknowledgements must not
	// turn an explicitly submitted result into another model execution.
	err = r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	saved, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
	var result harness.WorkerResult
	if err != nil || readErr != nil || saved.Phase != journal.PhaseWorkerDone || !saved.StoppedConfirmed || json.Unmarshal(saved.WorkerResult, &result) != nil || !reflect.DeepEqual(result, workers.result) {
		t.Fatalf("original submitted result was not recovered: %+v err=%v read=%v", saved, err, readErr)
	}
	if saved.Attempts != before.Attempts || saved.Thread != before.Thread || !reflect.DeepEqual(saved.Assignment, before.Assignment) || workers.starts != 0 || len(workers.stops) != 0 || spaces.mutations != 0 || spaces.reads != 1 || len(workers.seen) != 1 {
		t.Fatal("result recovery changed the original execution or performed new work", saved, workers, spaces)
	}
	spec := workers.seen[0]
	if spec.RunID != as.RunID || spec.Thread != before.Thread || spec.Attempt != 1 || spec.Dir != spaces.prepared.Dir || spec.StateDir != spaces.prepared.StateDir || !reflect.DeepEqual(spec.Spec, as.Spec) || spec.UploadFile != nil {
		t.Fatalf("result recovery lost frozen identity or offered new uploads: %+v", spec)
	}
	var final []protocol.Envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
		}
		final = append(final, env)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	r.cfg.ServerURL, r.cfg.HTTPClient = srv.URL, srv.Client()
	r.c.base, r.c.http = srv.URL, srv.Client()
	if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
	var artifact protocol.ArtifactPayload
	if len(final) != 1 || final[0].Operation != protocol.OpRunsSubmitArtifact || json.Unmarshal(final[0].Payload, &artifact) != nil || !reflect.DeepEqual(&artifact, workers.result.Result) || len(artifact.Files) != 13 || workers.starts != 0 || len(workers.seen) != 1 || spaces.finishes != 1 {
		t.Fatal("recovery failed to submit exactly the original artifact once", final, artifact, workers, spaces)
	}
}

func TestImplementationResultRecoveryPreservesUncertaintyAndMissingFallback(t *testing.T) {
	for _, scenario := range []string{"missing", "malformed", "changed identity", "active detached", "missing payload", "retained authority", "held"} {
		t.Run(scenario, func(t *testing.T) {
			r, as, workers, spaces, _ := implementationReconcileFixture(t)
			switch scenario {
			case "missing":
				workers.recoveryErr = harness.ErrNoImplementationResult
			case "malformed", "changed identity", "active detached":
				workers.recoveryErr = fmt.Errorf("original result retained: %s", scenario)
			case "missing payload":
				workers.result.Result = nil
			case "retained authority":
				if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, protocol.RetainedRunResume{RequestID: "original-authority", Mode: "attach"}); err != nil {
					t.Fatal(err)
				}
			case "held":
				if err := r.cfg.Journal.SetHeld(t.Context(), as.RunID, true); err != nil {
					t.Fatal(err)
				}
			}
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			if scenario == "held" {
				recovered, err := r.reconcileImplementationResult(t.Context(), as)
				if recovered || err != nil || len(workers.seen) != 0 || spaces.reads != 0 {
					t.Fatal("held worker entered result recovery", recovered, err)
				}
				return
			}
			err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
			after, afterRequests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			if scenario == "missing" {
				failure, ok := errors.AsType[*stepError](err)
				if !ok || failure.reason != "worker_interrupted_after_runner_restart" || !after.StoppedConfirmed || len(after.WorkerResult) != 0 || len(workers.stops) != 1 {
					t.Fatal("missing result changed original interruption behavior", after, err)
				}
			} else if !isCompletionPending(err) || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, afterRequests) || len(workers.stops) != 0 {
				t.Fatal("uncertain original submission was promoted or discarded", before, after, err)
			}
			if workers.starts != 0 || spaces.mutations != 0 {
				t.Fatal("recovery launched or prepared new work", workers, spaces)
			}
		})
	}
}

func TestImplementationResultCheckpointPrecedesStoppedConfirmation(t *testing.T) {
	r, as, workers, _, path := implementationReconcileFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_result_close BEFORE UPDATE OF stopped_confirmed ON runs WHEN NEW.stopped_confirmed=1 BEGIN SELECT RAISE(ABORT,'injected close persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	err = r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	saved, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
	if !isCompletionPending(err) || readErr != nil || saved.Phase != journal.PhaseWorkerDone || saved.StoppedConfirmed || len(saved.WorkerResult) == 0 || saved.Attempts != 1 || workers.starts != 0 {
		t.Fatal("failed closure checkpoint lost the original result", saved, err, readErr)
	}
	if _, err := db.ExecContext(t.Context(), `DROP TRIGGER fail_result_close`); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := journal.Open(t.Context(), path, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	r.cfg.Journal = reopened
	restored, err := reopened.Run(t.Context(), as.RunID)
	if err != nil || restored.WorkerDoneAtMillis != saved.WorkerDoneAtMillis || string(restored.WorkerResult) != string(saved.WorkerResult) {
		t.Fatal("reopening changed the saved original result", restored, err)
	}
	var final []protocol.Envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		final = append(final, env)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()
	r.c.base, r.c.http = srv.URL, srv.Client()
	if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
	var artifact protocol.ArtifactPayload
	if len(final) != 1 || final[0].Operation != protocol.OpRunsSubmitArtifact || json.Unmarshal(final[0].Payload, &artifact) != nil || !reflect.DeepEqual(&artifact, workers.result.Result) || workers.starts != 0 || len(workers.seen) != 1 {
		t.Fatal("reopened checkpoint did not submit its original result once", final, workers)
	}
	history, err := reopened.HistoricalRun(t.Context(), as.RunID)
	if err != nil || history.Phase != journal.PhaseDone || history.Attempts != 1 || history.Thread != saved.Thread || !reflect.DeepEqual(history.Assignment, saved.Assignment) {
		t.Fatal("reopened completion changed the original execution", history, err)
	}
}

type normalResultWorkers struct {
	result harness.WorkerResult
	events []harness.WorkerEvent
	err    error
	starts int
	onWait func()
	onStop func() error
}

func (w *normalResultWorkers) SupportsOwnedWorkerStop() bool { return true }
func (w *normalResultWorkers) StopOwnedWorker(context.Context, harness.WorkerSpec) error {
	return w.onStop()
}
func (w *normalResultWorkers) Start(_ context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	if err := spec.OnThread("owned-thread"); err != nil {
		return nil, err
	}
	events := make(chan harness.WorkerEvent, len(w.events))
	for _, event := range w.events {
		events <- event
	}
	close(events)
	return normalResultWorker{completionWorker: completionWorker{events: events, result: w.result, onWait: w.onWait}, err: w.err}, nil
}

type normalResultWorker struct {
	completionWorker
	err error
}

func (w normalResultWorker) Wait() (harness.WorkerResult, error) {
	result, _ := w.completionWorker.Wait()
	return result, w.err
}

func TestNormalWorkerCheckpointsSuccessfulResultBeforeClose(t *testing.T) {
	for _, outcome := range []string{"completed", "worker failed", "paused", "held", "cancelled", "event append failed"} {
		t.Run(outcome, func(t *testing.T) {
			r, as, _, spaces, path := implementationReconcileFixture(t)
			// This is the first attempt, not recovery of a previously started worker.
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(t.Context(), `UPDATE runs SET attempts=0,thread_id='' WHERE run_id=?`, as.RunID); err != nil {
				t.Fatal(err)
			}
			workers := &normalResultWorkers{result: submittedImplementationResult()}
			r.cfg.Workers = workers
			runCtx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			if outcome == "worker failed" {
				workers.err = errors.New("original turn failed")
			}
			if outcome == "paused" || outcome == "held" || outcome == "cancelled" {
				cause := map[string]error{"paused": errPaused, "held": errHeld, "cancelled": errStopped}[outcome]
				workers.onWait = func() { cancel(cause) }
			}
			if outcome == "event append failed" {
				workers.events = []harness.WorkerEvent{{Kind: "item.completed", Body: json.RawMessage(`{"type":"agentMessage","text":"original final message"}`)}}
				if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_result_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'injected event append failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			closed := false
			workers.onStop = func() error {
				closed = true
				saved, err := r.cfg.Journal.Run(t.Context(), as.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "completed" {
					var result harness.WorkerResult
					if saved.Phase != journal.PhaseWorkerDone || json.Unmarshal(saved.WorkerResult, &result) != nil || !reflect.DeepEqual(result, workers.result) {
						t.Fatal("native close preceded the successful result checkpoint", saved)
					}
				} else if len(saved.WorkerResult) != 0 || saved.Phase == journal.PhaseWorkerDone {
					t.Fatal("control/error outcome was recorded as successful completion", saved, outcome)
				}
				return errors.New("original process close is still unknown")
			}
			// Preparation is independent of result collection and must occur once.
			spaces.prepared = Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}
			spaces.ownedRecoveryWorkspace.mutations = 0
			r.cfg.Workspaces = &normalResultSpaces{implementationReconcileSpaces: spaces}
			err = r.work(t.Context(), runCtx, as, &active{as: as, desired: protocol.DesiredRun})
			if !closed || !isCompletionPending(err) || workers.starts != 1 {
				t.Fatal("normal completion did not retain the failed close", closed, err, workers.starts)
			}
		})
	}
}

type normalResultSpaces struct{ *implementationReconcileSpaces }

func (w *normalResultSpaces) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	w.mutations++
	return w.prepared, nil
}
