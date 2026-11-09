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

func TestImplementationOutcomeSurvivesAcknowledgedEventsAndCloseFailure(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
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
			// Flush while Wait is returning, before the failed process-close path.
			workers.onWait = func() {
				if err := r.flushEvents(t.Context(), as); err != nil {
					t.Fatal(err)
				}
			}
			workers.onStop = func() error { return errors.New("owned process closure is unknown") }
			err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
			saved, readErr := r.cfg.Journal.Run(t.Context(), as.RunID)
			pending, pendingErr := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 10)
			if !isCompletionPending(err) || readErr != nil || pendingErr != nil || saved.Phase != journal.PhaseStarted || len(saved.WorkerResult) != 0 || saved.StoppedConfirmed || len(pending) != 0 || eventUploads != 1 || len(finals) != 0 {
				t.Fatal("close failure lost the original unresolved attempt", saved, pending, err, readErr, pendingErr, finals)
			}
			reopenOutcomeJournal(t, r, path)
			// The original file-backed result and successful close are now available,
			// but there is no event left from which to infer success or failure.
			r.cfg.Workers = recovery
			if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
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
	for _, scenario := range []string{"missing", "foreign thread", "foreign attempt", "unknown", "interrupted"} {
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
			case "interrupted":
				outcome.Status, outcome.Source, outcome.TurnID = "interrupted", "native_terminal", "original-turn"
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
	outcome.Status = "failed"
	if _, err := saveWorkerOutcome(t.Context(), dir, outcome); err == nil {
		t.Fatal("contradictory status for the same terminal turn was accepted")
	}
	outcome.TurnID = "later-turn"
	if _, err := saveWorkerOutcome(t.Context(), dir, outcome); err != nil {
		t.Fatal(err)
	}
	wait.Status, wait.Error = "completed", ""
	saved, err = saveWorkerOutcome(t.Context(), dir, wait)
	if err != nil || saved.Status != "failed" || saved.TurnID != "later-turn" {
		t.Fatal("Wait nil erased terminal failure", saved, err)
	}
}
