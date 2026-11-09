package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func workerTerminalEvent(thread, turn, status string) harness.WorkerEvent {
	body, _ := json.Marshal(map[string]any{"thread_id": thread, "turn_id": turn,
		"data": map[string]any{"turn": map[string]string{"id": turn, "status": status}}})
	return harness.WorkerEvent{Kind: "native.turn_completed", Body: body}
}

func firstImplementationAttempt(t *testing.T, r *Runner, as protocol.Assignment, spaces *implementationReconcileSpaces, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `UPDATE runs SET attempts=0,thread_id='' WHERE run_id=?`, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workerOutcomePath(spaces.prepared.StateDir, 1)); err != nil {
		t.Fatal(err)
	}
	r.cfg.Workspaces = &normalResultSpaces{implementationReconcileSpaces: spaces}
	r.cfg.EventFlush = time.Hour
}

func reopenOutcomeJournal(t *testing.T, r *Runner, path string) {
	t.Helper()
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(t.Context(), path, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	r.cfg.Journal = j
}

func TestImplementationOutcomeSurvivesAcknowledgedEventsCloseFailureAndRunnerShutdown(t *testing.T) {
	for _, scenario := range []struct {
		status   string
		shutdown bool
	}{
		{"completed", false}, {"failed", false}, {"completed", true}, {"failed", true},
	} {
		t.Run(fmt.Sprintf("%s/runner_shutdown=%t", scenario.status, scenario.shutdown), func(t *testing.T) {
			status := scenario.status
			r, as, recovery, spaces, path := implementationReconcileFixture(t)
			firstImplementationAttempt(t, r, as, spaces, path)
			workers := &normalResultWorkers{result: submittedImplementationResult(),
				events: []harness.WorkerEvent{workerTerminalEvent("owned-thread", "original-turn", status)}}
			if status == "completed" {
				// The native turn succeeded; only its deferred process close is unknown.
				workers.err = fmt.Errorf("%w: process close is unknown", harness.ErrRetainedResumePending)
			} else {
				workers.err = errors.New("traex: the worker's turn ended failed")
			}
			r.cfg.Workers = workers
			var finals []protocol.Envelope
			eventUploads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunnerEvents {
					observed, err := readWorkerOutcome(spaces.prepared.StateDir, as.RunID, "owned-thread", 1)
					if err != nil || observed == nil || observed.Status != status || observed.TurnID != "original-turn" {
						t.Error("event acknowledgement preceded durable terminal outcome", observed, err)
					}
					eventUploads++
				} else {
					finals = append(finals, env)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			lifetime, stopRunner := context.WithCancel(t.Context())
			defer stopRunner()
			runCtx, stopRun := context.WithCancelCause(lifetime)
			defer stopRun(nil)
			// Flush while Wait is returning, before the failed process-close path.
			workers.onWait = func() {
				if err := r.flushEvents(t.Context(), as); err != nil {
					t.Fatal(err)
				}
				if scenario.shutdown {
					stopRunner()
				}
			}
			workers.onStop = func() error { return errors.New("owned process closure is unknown") }
			activeRun := &active{as: as, desired: protocol.DesiredRun}
			var err error
			if scenario.shutdown {
				// Exercise execute's real inherited runner-lifetime cancellation,
				// including its exit without turning that cancellation into a control.
				err = r.execute(lifetime, runCtx, as.RunID, activeRun)
				if err != nil || !errors.Is(context.Cause(runCtx), context.Canceled) {
					t.Fatal("runner shutdown did not propagate", err)
				}
			} else {
				err = r.work(lifetime, runCtx, as, activeRun)
				if !isCompletionPending(err) {
					t.Fatal("close failure was not retained", err)
				}
			}
			saved, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
			pending, pendingErr := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 10)
			if readErr != nil || pendingErr != nil || saved.Phase != journal.PhaseStarted || len(saved.WorkerResult) != 0 || saved.StoppedConfirmed || len(pending) != 0 || eventUploads != 1 || len(finals) != 0 {
				t.Fatal("close failure lost the original unresolved attempt", saved, pending, err, readErr, pendingErr, finals)
			}
			observed, observedErr := readWorkerOutcome(spaces.prepared.StateDir, as.RunID, "owned-thread", 1)
			if observedErr != nil || observed == nil || observed.Source != "native_terminal" || observed.Status != status {
				t.Fatal("runner shutdown overwrote the native terminal", observed, observedErr)
			}
			reopenOutcomeJournal(t, r, path)
			// The original file-backed result and successful close are now available,
			// but there is no event left from which to infer success or failure.
			r.cfg.Workers = recovery
			if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			history, historyErr := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID)
			if historyErr != nil || history.Attempts != 1 || history.Thread != "owned-thread" {
				t.Fatal("recovery replayed an original native attempt", history, historyErr)
			}
			if len(finals) != 1 || recovery.starts != 0 || len(recovery.seen) != 1 || workers.starts != 1 || spaces.mutations != 1 {
				t.Fatal("recovery duplicated work or publication", finals, recovery, workers, spaces)
			}
			if status == "failed" {
				var release protocol.ReleasePayload
				if finals[0].Operation != protocol.OpRunsRelease || json.Unmarshal(finals[0].Payload, &release) != nil || release.Reason != "worker_failed" {
					t.Fatal("failed native turn was promoted to an artifact", finals)
				}
			} else {
				var artifact protocol.ArtifactPayload
				if finals[0].Operation != protocol.OpRunsSubmitArtifact || json.Unmarshal(finals[0].Payload, &artifact) != nil || !reflect.DeepEqual(&artifact, recovery.result.Result) || len(artifact.Files) != 13 {
					t.Fatal("close-only error discarded the original successful result", finals, artifact)
				}
			}
		})
	}
}

func TestImplementationOutcomeUnknownOrForeignRemainsPending(t *testing.T) {
	for _, scenario := range []string{"missing", "foreign thread", "foreign attempt", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			r, as, workers, spaces, _ := implementationReconcileFixture(t)
			if err := os.Remove(workerOutcomePath(spaces.prepared.StateDir, 1)); err != nil {
				t.Fatal(err)
			}
			outcome := workerOutcome{Version: 1, RunID: as.RunID, ThreadID: "original-result-thread", Attempt: 1, Status: "unknown", Source: "worker_wait"}
			switch scenario {
			case "foreign thread":
				outcome.ThreadID = "foreign-thread"
			case "foreign attempt":
				outcome.Attempt = 2
			}
			if scenario != "missing" {
				raw, _ := json.Marshal(outcome)
				// A foreign attempt in the original path must not be accepted either.
				if err := os.WriteFile(workerOutcomePath(spaces.prepared.StateDir, 1), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
			after, afterRequests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			if !isCompletionPending(err) || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, afterRequests) || workers.starts != 0 || spaces.mutations != 0 {
				t.Fatal("unknown or foreign outcome became successful completion", before, after, err)
			}
		})
	}
}

func TestImplementationLegacyOutcomeRecoveredAfterHelloContinue(t *testing.T) {
	r, as, workers, spaces, _ := implementationReconcileFixture(t)
	if err := os.Remove(workerOutcomePath(spaces.prepared.StateDir, 1)); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetFenced(t.Context(), as.RunID, true); err != nil {
		t.Fatal(err)
	}
	event := workerTerminalEvent("original-result-thread", "original-result-turn", "completed")
	if err := r.cfg.Journal.AppendEvent(t.Context(), as.RunID, protocol.RunEvent{Kind: event.Kind, Body: event.Body}); err != nil {
		t.Fatal(err)
	}
	// Match a production held lease: its original terminal fact has not yet
	// been acknowledged, and only the regular Hello verdict may clear fencing.
	var artifact protocol.ArtifactPayload
	events := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		switch env.Operation {
		case protocol.OpRunnerHello:
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictContinue, Desired: protocol.DesiredRun}}}
		case protocol.OpRunnerEvents:
			observed, err := readWorkerOutcome(spaces.prepared.StateDir, as.RunID, "original-result-thread", 1)
			if err != nil || observed == nil || observed.Status != "completed" {
				t.Error("Hello recovery acknowledged its legacy terminal before preserving it", observed, err)
			}
			events++
		case protocol.OpRunsSubmitArtifact:
			if err := json.Unmarshal(env.Payload, &artifact); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected operation %s", env.Operation)
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.hello(ctx); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	history, err := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID)
	if err != nil || history.Phase != journal.PhaseDone || history.Fenced || history.Attempts != 1 || events != 1 || !reflect.DeepEqual(&artifact, workers.result.Result) || workers.starts != 0 || len(workers.seen) != 1 {
		t.Fatal("regular Hello did not recover its original completed result", history, artifact, workers, events, err)
	}
}

func TestWorkerOutcomeKeepsLatestTurnAndOriginalFailure(t *testing.T) {
	dir := t.TempDir()
	outcome := workerOutcome{Version: 1, RunID: "run", ThreadID: "thread", TurnID: "first-turn", Attempt: 1, Status: "failed", Source: "native_terminal"}
	if _, err := saveWorkerOutcome(t.Context(), dir, outcome); err != nil {
		t.Fatal(err)
	}
	outcome.TurnID, outcome.Status = "repair-turn", "completed"
	if _, err := saveWorkerOutcome(t.Context(), dir, outcome); err != nil {
		t.Fatal("later turn was mechanically rejected", err)
	}
	wait := workerOutcome{Version: 1, RunID: "run", ThreadID: "thread", Attempt: 1, Status: "failed", Source: "worker_wait", Error: "only close failed"}
	saved, err := saveWorkerOutcome(t.Context(), dir, wait)
	if err != nil || saved.TurnID != "repair-turn" || saved.Status != "completed" {
		t.Fatal("close error replaced actual native success", saved, err)
	}
	wait.Status, wait.Source, wait.Error = "interrupted", "run_control", "paused after terminal"
	saved, err = saveWorkerOutcome(t.Context(), dir, wait)
	if err != nil || saved.TurnID != "repair-turn" || saved.Status != "completed" {
		t.Fatal("later control erased the actual native terminal", saved, err)
	}
	outcome.Status = "failed"
	if _, err := saveWorkerOutcome(t.Context(), dir, outcome); err == nil {
		t.Fatal("contradictory status for the same terminal turn was accepted")
	}
	outcome.TurnID = "later-turn"
	if _, err := saveWorkerOutcome(t.Context(), dir, outcome); err != nil {
		t.Fatal(err)
	}
	wait.Status, wait.Source, wait.Error = "completed", "worker_wait", ""
	saved, err = saveWorkerOutcome(t.Context(), dir, wait)
	if err != nil || saved.Status != "failed" || saved.TurnID != "later-turn" {
		t.Fatal("Wait nil erased terminal failure", saved, err)
	}
}

func TestImplementationOwnedRecoveryEndsBoundedPass(t *testing.T) {
	r, as, workers, spaces, _ := implementationReconcileFixture(t)
	workers.recoveryErr = fmt.Errorf("%w: original process ownership is unconfirmed", harness.ErrOwnedWorkerUnconfirmed)
	r.cfg.RetryDelay = time.Hour
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun})
	after, afterRequests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	if !isOwnedRecoveryUncertain(err) || ctx.Err() != nil || !after.Fenced || after.StoppedConfirmed || after.Phase != journal.PhaseStarted || len(after.WorkerResult) != 0 || len(workers.seen) != 1 || workers.starts != 0 || spaces.mutations != 0 || !reflect.DeepEqual(requests, afterRequests) {
		t.Fatal("uncertain ownership looped, published, or discarded the attempt", before, after, err, ctx.Err(), workers)
	}
	before.Fenced = true
	if !reflect.DeepEqual(before, after) {
		t.Fatal("bounded ownership recovery changed more than its fence", before, after)
	}
}

// A native interruption records what happened; an independently confirmed
// stop and the existing current control decide whether another attempt may run.
type normalReconciledResultWorkers struct {
	*normalResultWorkers
	recovery *implementationReconcileWorkers
}

func (w normalReconciledResultWorkers) ReconcileImplementationResult(ctx context.Context, spec harness.WorkerSpec) (harness.WorkerResult, error) {
	return w.recovery.ReconcileImplementationResult(ctx, spec)
}

func TestImplementationNativeInterruptionPreservesControlAndResume(t *testing.T) {
	for _, control := range []string{"pause", "stop", "question"} {
		t.Run(control, func(t *testing.T) {
			r, as, recovery, spaces, path := implementationReconcileFixture(t)
			firstImplementationAttempt(t, r, as, spaces, path)
			runCtx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errPaused
			events := []harness.WorkerEvent{workerTerminalEvent("owned-thread", "original-turn", "interrupted")}
			if control == "stop" {
				cause = errStopped
			}
			if control == "question" {
				cause = &questionYield{decision: "original-question"}
				// A provisional submission may precede AskOwner. There need
				// not be a native terminal before the explicit handoff.
				events = nil
			}
			workers := &normalResultWorkers{result: submittedImplementationResult(), events: events, onWait: func() { cancel(cause) }, onStop: func() error { return nil }}
			r.cfg.Workers = workers
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				if env.Operation != protocol.OpRunnerEvents {
					t.Errorf("unexpected request %s", env.Operation)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			err := r.work(t.Context(), runCtx, as, &active{as: as, desired: protocol.DesiredRun})
			saved, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
			if !errors.Is(err, cause) || readErr != nil || !saved.StoppedConfirmed || saved.Attempts != 1 || len(saved.WorkerResult) != 0 {
				t.Fatal("native interruption swallowed explicit control", saved, err, readErr)
			}
			if control == "question" {
				observed, err := readWorkerOutcome(spaces.prepared.StateDir, as.RunID, "owned-thread", 1)
				if err != nil || observed == nil || observed.Source != "run_control" || observed.Status != "interrupted" {
					t.Fatal("explicit question handoff left provisional result unknown", observed, err)
				}
			}
			if control == "stop" {
				return
			}
			reopenOutcomeJournal(t, r, path)
			resumed := &normalResultWorkers{result: submittedImplementationResult(), onStop: func() error { return nil }}
			r.cfg.Workers = normalReconciledResultWorkers{normalResultWorkers: resumed, recovery: recovery}
			// An actual pause already observed physical exit. The original
			// resume path can start its successor without promoting result.json.
			err = r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
			saved, readErr = r.cfg.Journal.Run(t.Context(), as.RunID)
			if err != nil || readErr != nil || saved.Attempts != 2 || saved.Phase != journal.PhaseWorkerDone || resumed.starts != 1 || len(recovery.seen) != 1 || recovery.seen[0].Attempt != 1 {
				t.Fatal("legitimate paused attempt could not resume", saved, err, readErr, resumed.starts, recovery.seen)
			}
		})
	}
}

func TestImplementationUnconfirmedInterruptionDoesNotReplay(t *testing.T) {
	r, as, workers, spaces, _ := implementationReconcileFixture(t)
	outcome := workerOutcome{Version: 1, RunID: as.RunID, ThreadID: "original-result-thread", TurnID: "original-result-turn", Attempt: 1, Status: "interrupted", Source: "native_terminal"}
	raw, _ := json.Marshal(outcome)
	if err := os.WriteFile(workerOutcomePath(spaces.prepared.StateDir, 1), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	saved, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
	failure, ok := errors.AsType[*stepError](err)
	if !ok || failure.reason != "worker_interrupted_after_runner_restart" || readErr != nil || !saved.StoppedConfirmed || saved.Attempts != 1 || saved.Phase != journal.PhaseStarted || workers.starts != 0 || len(workers.stops) != 1 || spaces.mutations != 0 {
		t.Fatal("unconfirmed interruption authorized another attempt", saved, err, readErr, workers)
	}
}

func TestImplementationResultRecoveryBypassesAuthorizedStrictResume(t *testing.T) {
	r, as, _, _, _ := implementationReconcileFixture(t)
	authority := protocol.RetainedRunResume{RequestID: "authorized-original-attachment", Mode: "attach"}
	if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	probe := &retainedStrictWorkerProbe{}
	probe.start = func(_ context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
		if spec.RetainedResume == nil || *spec.RetainedResume != authority || spec.Thread != "original-result-thread" {
			t.Error("strict continuation lost its original authority", spec)
		}
		return nil, harness.ErrRetainedResumePending
	}
	r.cfg.Workers = retainedStrictWorkers{probe}
	spaces := &retainedStrictWorkspaceProbe{}
	r.cfg.Workspaces = spaces
	err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	if !isCompletionPending(err) || probe.starts.Load() != 1 || spaces.reattaches.Load() != 1 || spaces.prepares.Load() != 0 {
		t.Fatal("optional result recovery blocked the original authorized continuation", err, probe.starts.Load(), spaces.reattaches.Load())
	}
}

func TestImplementationUnexpectedNativeInterruptionIsFailure(t *testing.T) {
	for _, closeUnknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("close_unknown=%t", closeUnknown), func(t *testing.T) {
			r, as, recovery, spaces, path := implementationReconcileFixture(t)
			firstImplementationAttempt(t, r, as, spaces, path)
			workers := &normalResultWorkers{result: submittedImplementationResult(), events: []harness.WorkerEvent{workerTerminalEvent("owned-thread", "original-turn", "interrupted")}, err: errors.New("traex: the worker's turn ended interrupted")}
			workers.onStop = func() error {
				if closeUnknown {
					return errors.New("original owned close remains unknown")
				}
				return nil
			}
			r.cfg.Workers = workers
			var finals []protocol.Envelope
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(q.Body).Decode(&env)
				if env.Operation != protocol.OpRunnerEvents {
					finals = append(finals, env)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			expectedReason := "worker_failed"
			if closeUnknown {
				err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
				saved, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
				if !isCompletionPending(err) || readErr != nil || saved.StoppedConfirmed || saved.Attempts != 1 || len(saved.WorkerResult) != 0 || len(finals) != 0 {
					t.Fatal("unconfirmed close was converted to failure or success", saved, err, readErr, finals)
				}
				reopenOutcomeJournal(t, r, path)
				r.cfg.Workers = recovery
				expectedReason = "worker_interrupted_after_runner_restart"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			var release protocol.ReleasePayload
			if ctx.Err() != nil || len(finals) != 1 || finals[0].Operation != protocol.OpRunsRelease || json.Unmarshal(finals[0].Payload, &release) != nil || release.Reason != expectedReason || workers.starts != 1 || recovery.starts != 0 {
				t.Fatal("native interruption did not follow its original failure path", finals, release, ctx.Err(), workers.starts, recovery.starts)
			}
			saved, err := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID)
			if err != nil || saved.Attempts != 1 {
				t.Fatal("unexpected native interruption replayed work", saved, err)
			}
		})
	}
}
