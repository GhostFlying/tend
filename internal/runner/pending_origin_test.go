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
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type originProbeWorkers struct {
	starts    int
	failStart bool
	failWait  bool
}

func (w *originProbeWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	if err := spec.OnThread("native_thread"); err != nil {
		return nil, err
	}
	if w.failStart {
		return nil, errors.New("closed app-server failed to start turn")
	}
	events := make(chan harness.WorkerEvent)
	close(events)
	return originProbeWorker{events: events, fail: w.failWait}, nil
}

type originProbeWorker struct {
	events chan harness.WorkerEvent
	fail   bool
}

func (w originProbeWorker) Events() <-chan harness.WorkerEvent { return w.events }
func (w originProbeWorker) Wait() (harness.WorkerResult, error) {
	if w.fail {
		return harness.WorkerResult{}, errors.New("closed app-server failed during turn")
	}
	return harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "finished with closed session"}}}, nil
}

type originProbeSpaces struct {
	completionWorkspaces
	prepares int
}

func (w *originProbeSpaces) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	w.prepares++
	return Prepared{}, nil
}

func TestPendingOriginMismatchNeverStartsOrConfirmsDifferentHarness(t *testing.T) {
	for _, phase := range []string{journal.PhaseClaimed, journal.PhaseStarted, journal.PhaseWorkerDone, journal.PhaseSubmitting, journal.PhaseReleasing} {
		for _, explicit := range []bool{false, true} {
			name := phase + " legacy"
			if explicit {
				name = phase + " explicit"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				clock := testkit.NewClock()
				j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
				if err != nil {
					t.Fatal(err)
				}
				defer j.Close()
				as := protocol.Assignment{RunID: "run_old_orca", TaskID: "task_old_orca", Desired: protocol.DesiredRun}
				origin := protocol.WorkerKind("")
				if explicit {
					origin = protocol.WorkerOrca
				}
				if err := j.AcceptAssignmentFromWorker(ctx, as, "", origin); err != nil {
					t.Fatal(err)
				}
				if _, err := j.NextAttempt(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
				if err := j.SetThread(ctx, as.RunID, `{"worktree":"wt","terminal":"term","pane":"tab:1","turn":100,"said":"tend brief"}`); err != nil {
					t.Fatal(err)
				}
				if phase == journal.PhaseWorkerDone {
					if err := j.WorkerDone(ctx, as.RunID, json.RawMessage(`{"summary":"already completed"}`)); err != nil {
						t.Fatal(err)
					}
				} else if err := j.SetPhase(ctx, as.RunID, phase); err != nil {
					t.Fatal(err)
				}
				if phase == journal.PhaseSubmitting || phase == journal.PhaseReleasing {
					if err := j.Finish(ctx, as.RunID, phase, journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "req_existing", Operation: protocol.OpRunsRelease, Target: runTarget(as), Payload: json.RawMessage(`{"reason":"existing"}`)}}); err != nil {
						t.Fatal(err)
					}
				}
				workers := &originProbeWorkers{}
				spaces := &originProbeSpaces{}
				r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Worker: protocol.WorkerAppServer, Workers: workers, Workspaces: spaces, RetryDelay: time.Millisecond})
				if err := r.stopAgent(ctx, as); err == nil {
					t.Fatal("wrong harness confirmed an external terminal stopped")
				}
				if err := r.stopCompletedAgent(ctx, as, false); err == nil {
					t.Fatal("guarded stop used the wrong harness")
				}
				if phase == journal.PhaseClaimed {
					err = r.prepare(ctx, as)
				} else if phase == journal.PhaseStarted {
					err = r.work(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun})
				} else {
					err = r.awaitCompletedStop(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun})
				}
				if err == nil {
					t.Fatal("pending original harness was replaced")
				}
				run, err := j.Run(ctx, as.RunID)
				if err != nil || run.StoppedConfirmed || run.Phase != phase || workers.starts != 0 || spaces.prepares != 0 {
					t.Fatalf("wrong-harness side effect: run=%+v starts=%d prepares=%d err=%v", run, workers.starts, spaces.prepares, err)
				}
				if explicit && run.WorkerKind != protocol.WorkerOrca || !explicit && run.WorkerKind != "" {
					t.Fatalf("origin was relabeled: %s", run.WorkerKind)
				}
			})
		}
	}
}

func TestRecoveredAppserverFailureWithoutClosureProofStaysPending(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_old_appserver", TaskID: "task", Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "native_thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "req_old_failed", Operation: protocol.OpRunsRelease, Target: runTarget(as)}}); err != nil {
		t.Fatal(err)
	}
	workers := &originProbeWorkers{}
	r := New(Config{Journal: j, Now: clock.Now, Workers: workers, Worker: protocol.WorkerAppServer, Workspaces: completionWorkspaces{}})
	if err := r.stopAgent(ctx, as); err == nil {
		t.Fatal("same harness without a releaser fabricated process closure")
	}
	if err := r.awaitCompletedStop(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun}); !isCompletionPending(err) {
		t.Fatalf("old failure not retained: %v", err)
	}
	run, err := j.Run(ctx, as.RunID)
	if err != nil || run.StoppedConfirmed || run.Phase != journal.PhaseReleasing || workers.starts != 0 {
		t.Fatalf("old session silently released: %+v %v", run, err)
	}
}

func TestCurrentAppserverClosureProofSurvivesFailedOrSuccessfulWorker(t *testing.T) {
	for _, mode := range []string{"start failure", "wait failure", "success"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run_live_appserver", TaskID: "task", Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["report"]}`)}
			// A genuinely unstarted pre-upgrade row may safely bind the first harness.
			if err := j.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			workers := &originProbeWorkers{failStart: mode == "start failure", failWait: mode == "wait failure"}
			var reported protocol.Envelope
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				_ = json.NewDecoder(q.Body).Decode(&reported)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer srv.Close()
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client(), Workers: workers, Worker: protocol.WorkerAppServer, Workspaces: completionWorkspaces{}})
			if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			want := protocol.OpRunsRelease
			if mode == "success" {
				want = protocol.OpRunsSubmitArtifact
			}
			saved, err := j.HistoricalRun(ctx, as.RunID)
			if err != nil || !saved.StoppedConfirmed || saved.WorkerKind != protocol.WorkerAppServer || workers.starts != 1 || reported.Operation != want {
				t.Fatalf("live closure proof lost: %+v starts=%d report=%+v err=%v", saved, workers.starts, reported, err)
			}
		})
	}
}

func TestRestartedAppserverUnknownSessionCannotStartAnotherAttempt(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_old_session", TaskID: "task", Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(ctx, as.RunID, "native_thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	workers := &originProbeWorkers{}
	spaces := &originProbeSpaces{}
	r := New(Config{Journal: j, Now: clock.Now, Workers: workers, Worker: protocol.WorkerAppServer, Workspaces: spaces})
	if err := r.work(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun}); !isCompletionPending(err) {
		t.Fatalf("unknown previous session not retained: %v", err)
	}
	if workers.starts != 0 || spaces.prepares != 0 {
		t.Fatalf("unknown prior writer overlapped: starts=%d prepares=%d", workers.starts, spaces.prepares)
	}
	if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.requireRunWorker(ctx, as.RunID); err != nil {
		t.Fatalf("independently confirmed session could not resume: %v", err)
	}
}
