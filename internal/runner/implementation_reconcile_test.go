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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
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
	onRecover   func() error
	observed    []harness.WorkerSpec
}

func (w *implementationReconcileWorkers) ReconcileImplementationResult(_ context.Context, spec harness.WorkerSpec) (harness.WorkerResult, error) {
	w.seen = append(w.seen, spec)
	if w.onRecover != nil {
		return w.result, w.onRecover()
	}
	return w.result, w.recoveryErr
}

func (w *implementationReconcileWorkers) ObserveOwnedWorkerStop(_ context.Context, spec harness.WorkerSpec) error {
	w.observed = append(w.observed, spec)
	return nil
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

func implementationReconcileFixture(t *testing.T, spec ...json.RawMessage) (*Runner, protocol.Assignment, *implementationReconcileWorkers, *implementationReconcileSpaces, string) {
	t.Helper()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original-result-run", LeaseID: "original-result-lease", TaskID: "original-result-task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, PauseHandoffs: true, Spec: json.RawMessage(`{"deliverables":["report","files"]}`)}
	if len(spec) != 0 {
		as.Spec = spec[0]
	}
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
	if _, err := saveWorkerOutcome(t.Context(), spaces.prepared.StateDir, workerOutcome{Version: 1, RunID: as.RunID, ThreadID: "original-result-thread", TurnID: "original-result-turn", Attempt: 1, Status: "completed", Source: "native_terminal"}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Workers: workers, Workspaces: spaces, Now: clock.Now, IDs: testkit.NewIDGen(clock, 9), Log: slog.New(slog.DiscardHandler), RetryDelay: time.Millisecond})
	return r, as, workers, spaces, path
}

func TestImplementationResultRecoverySubmitsOriginalFilesWithoutNativeWork(t *testing.T) {
	r, as, workers, spaces, _ := implementationReconcileFixture(t)
	before, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// The durable native outcome remains even after all events were acknowledged.
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

func TestPauseSettlesOriginalImplementationResultAfterPhysicalStop(t *testing.T) {
	for _, tc := range []struct {
		name string
		park bool
		err  error
	}{
		{name: "stop verified immediately"},
		{name: "stop verified after parking", park: true},
		{name: "original result cannot be verified", park: true, err: harness.ErrRetainedResumePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			r, as, workers, spaces, _ := implementationReconcileFixture(t)
			a := &active{as: as, desired: protocol.DesiredRun}
			handoff := &protocol.PauseHandoff{RequestID: "original-pause", IssueID: "issue", IssueRevision: 2, ControlRevision: 1}
			pause := func() {
				t.Helper()
				if err := r.cfg.Journal.RecordPause(ctx, as.RunID, handoff); err != nil {
					t.Fatal(err)
				}
				r.applyDesired(ctx, a, protocol.DesiredPaused, r.cfg.Now())
			}
			final := make(chan protocol.Envelope, 4)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
				}
				saved, err := r.cfg.Journal.Run(q.Context(), as.RunID)
				if err != nil || !saved.StoppedConfirmed || saved.Attempts != 1 || saved.Thread != "original-result-thread" {
					t.Error("sent a result without the original physical stop proof", saved, err)
				}
				select {
				case final <- env:
				default:
					t.Error("unexpected repeated business request", env)
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
			}))
			defer srv.Close()
			r.c.base, r.c.http = srv.URL, srv.Client()

			workers.recoveryErr = tc.err
			if tc.park {
				workers.onRecover = func() error {
					pause()
					return harness.ErrOwnedWorkerUnconfirmed
				}
				err := r.execute(ctx, ctx, as.RunID, a)
				saved, readErr := r.cfg.Journal.Run(ctx, as.RunID)
				if _, ok := errors.AsType[*pauseStopPending](err); !ok || readErr != nil ||
					saved.Phase != journal.PhasePausing || saved.StoppedConfirmed || len(final) != 0 {
					t.Fatal("failed closure did not park the original attempt", saved, err, readErr)
				}
				workers.onRecover = nil
			} else {
				pause()
			}

			err := r.execute(ctx, ctx, as.RunID, a)
			if workers.starts != 0 || spaces.mutations != 0 || tc.park && len(workers.observed) != 1 {
				t.Fatal("pause recovery did new work or skipped observation", workers, spaces)
			}
			saved, readErr := r.cfg.Journal.Run(ctx, as.RunID)
			if tc.err != nil {
				if _, ok := errors.AsType[*pauseStopPending](err); !ok || !errors.Is(err, tc.err) || readErr != nil ||
					saved.Phase != journal.PhasePausing || saved.Assignment.RunID != as.RunID || saved.Assignment.LeaseID != as.LeaseID ||
					!saved.StoppedConfirmed || len(final) != 0 || saved.Thread != "original-result-thread" || saved.Attempts != 1 {
					t.Fatal("uncertain result or original stopped attempt was lost", saved, err, readErr)
				}
				return
			}
			if err != nil || !errors.Is(readErr, journal.ErrNotFound) || spaces.finishes != 1 || len(final) != 1 {
				t.Fatal("expected exactly one final request after closure", saved, err, readErr, len(final))
			}
			env := <-final
			var artifact protocol.ArtifactPayload
			if env.Operation != protocol.OpRunsSubmitArtifact || env.Target.ID != as.RunID ||
				json.Unmarshal(env.Payload, &artifact) != nil || !reflect.DeepEqual(&artifact, workers.result.Result) {
				t.Fatal("physical pause discarded or changed the original result", env, artifact)
			}
		})
	}
}

type pauseCollectionSpaces struct {
	*implementationReconcileSpaces
	entered  chan struct{}
	canceled chan error
}

func (s *pauseCollectionSpaces) Collect(ctx context.Context, _ protocol.Assignment) (protocol.ArtifactPayload, error) {
	s.entered <- struct{}{}
	<-ctx.Done()
	s.canceled <- context.Cause(ctx)
	return protocol.ArtifactPayload{}, ctx.Err()
}

func TestRecoveredPauseCollectionReestablishesOriginalLease(t *testing.T) {
	for _, control := range []protocol.RunDesired{protocol.DesiredStopped, protocol.DesiredHeld} {
		t.Run(string(control), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			r, as, workers, spaces, _ := implementationReconcileFixture(t, json.RawMessage(`{"deliverables":["commit"]}`))
			t.Cleanup(func() {
				cancel()
				r.wg.Wait()
			})
			pause := &protocol.PauseHandoff{RequestID: "original-pause", IssueID: "issue", IssueRevision: 2, ControlRevision: 1}
			if err := r.cfg.Journal.RecordPause(ctx, as.RunID, pause); err != nil {
				t.Fatal(err)
			}
			if parked, err := r.cfg.Journal.TryBeginPauseStop(ctx, as.RunID, 1); err != nil || !parked {
				t.Fatal(parked, err)
			}
			mutable := &pauseCollectionSpaces{
				implementationReconcileSpaces: spaces,
				entered:                       make(chan struct{}, 2), canceled: make(chan error, 2),
			}
			r.cfg.Workspaces = mutable
			observations := make(chan protocol.RunObserved, 3)
			finals := make(chan string, 3)
			desired := make(chan protocol.RunDesired, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				var result any
				switch env.Operation {
				case protocol.OpRunnerHello:
					var hello protocol.HelloPayload
					if err := json.Unmarshal(env.Payload, &hello); err != nil || len(hello.Held) != 1 ||
						hello.Held[0].RunID != as.RunID || hello.Held[0].LeaseID != as.LeaseID {
						t.Error("Hello lost original lease identity", hello.Held, err)
						return
					}
					observations <- hello.Held[0].Observed
					result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{
						{RunID: as.RunID, Verdict: protocol.VerdictContinue, Desired: protocol.DesiredPaused, PauseHandoff: pause},
					}}
				case protocol.OpRunnerHeartbeat:
					var hb protocol.HeartbeatPayload
					if err := json.Unmarshal(env.Payload, &hb); err != nil || len(hb.Runs) != 1 ||
						hb.Runs[0] != (protocol.LeaseRef{RunID: as.RunID, LeaseID: as.LeaseID}) {
						t.Error("collection heartbeat did not track the original lease", hb.Runs, err)
						return
					}
					d := <-desired
					result = protocol.HeartbeatResult{LeaseTTLMillis: 60000, Runs: []protocol.LeaseState{
						{RunID: as.RunID, Renewed: d == protocol.DesiredHeld, Desired: d, PauseHandoff: pause},
					}}
				default:
					finals <- env.Operation
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
			saved, readErr := r.cfg.Journal.Run(ctx, as.RunID)
			if <-observations != protocol.RunUnknown || readErr != nil || saved.Phase != journal.PhaseWorkerDone ||
				!saved.StoppedConfirmed || len(saved.WorkerResult) == 0 || len(mutable.entered) != 0 || len(finals) != 0 {
				t.Fatal("pause observation performed workspace work or lost the result", saved, readErr)
			}
			select {
			case <-r.reconcile:
			default:
				t.Fatal("stopped executor did not request fresh control for collection")
			}
			// Simulate a reply in flight from another read-only pause observation.
			paused := as
			paused.Desired = protocol.DesiredPaused
			r.startWithControl(ctx, paused, r.cfg.Now(), true)
			if r.lookup(as.RunID) != nil {
				t.Fatal("read-only pause reply granted workspace execution")
			}

			if err := r.hello(ctx); err != nil {
				t.Fatal(err)
			}
			if <-observations != protocol.RunWaiting {
				t.Fatal("collection renewal did not identify the stopped, waiting Run")
			}
			select {
			case <-mutable.entered:
			case <-ctx.Done():
				t.Fatal("collection did not start under fresh control")
			}
			a := r.lookup(as.RunID)
			r.mu.Lock()
			tracked := a != nil && !a.pauseOnly && !a.reporting && r.cfg.Now().Before(a.deadline)
			r.mu.Unlock()
			if !tracked {
				t.Fatal("collection is outside the normal lease watchdog")
			}
			desired <- control
			r.heartbeat(ctx)
			select {
			case cause := <-mutable.canceled:
				want := errStopped
				if control == protocol.DesiredHeld {
					want = errHeld
				}
				if !errors.Is(cause, want) {
					t.Fatal("collection missed authoritative control", cause)
				}
			case <-ctx.Done():
				t.Fatal("collection survived authoritative control")
			}
			if control == protocol.DesiredHeld {
				desired <- protocol.DesiredStopped
				r.heartbeat(ctx)
			}
			r.wg.Wait()
			if workers.starts != 0 || len(finals) != 1 || <-finals != protocol.OpRunsRelease {
				t.Fatal("collection after pause ran native work or replaced the original stopped path")
			}
		})
	}
}

func TestRecoveredPauseReportObservesHandbackAndStop(t *testing.T) {
	for _, afterHold := range []protocol.RunDesired{protocol.DesiredPaused, protocol.DesiredStopped} {
		t.Run(string(afterHold), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			r, as, workers, _, _ := implementationReconcileFixture(t)
			t.Cleanup(func() {
				cancel()
				r.wg.Wait()
			})
			pause := &protocol.PauseHandoff{RequestID: "original-pause", IssueID: "issue", IssueRevision: 2, ControlRevision: 1}
			if err := r.cfg.Journal.RecordPause(ctx, as.RunID, pause); err != nil {
				t.Fatal(err)
			}
			if parked, err := r.cfg.Journal.TryBeginPauseStop(ctx, as.RunID, 1); err != nil || !parked {
				t.Fatal(parked, err)
			}
			entered, proceed := make(chan struct{}), make(chan struct{})
			workers.onRecover = func() error {
				close(entered)
				select {
				case <-proceed:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			var held atomic.Bool
			finals := make(chan protocol.Envelope, 2)
			observed := make(chan protocol.RunObserved, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				var result any
				switch env.Operation {
				case protocol.OpRunnerHello:
					var hello protocol.HelloPayload
					if err := json.Unmarshal(env.Payload, &hello); err != nil || len(hello.Held) != 1 ||
						hello.Held[0].RunID != as.RunID || hello.Held[0].LeaseID != as.LeaseID {
						t.Error("Hello lost the original Run or lease", hello.Held, err)
						return
					}
					observed <- hello.Held[0].Observed
					desired := protocol.DesiredPaused
					if held.Load() {
						desired = protocol.DesiredHeld
					}
					result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{
						{RunID: as.RunID, Verdict: protocol.VerdictContinue, Desired: desired, PauseHandoff: pause},
					}}
				case protocol.OpRunnerHeartbeat:
					var hb protocol.HeartbeatPayload
					if err := json.Unmarshal(env.Payload, &hb); err != nil || len(hb.Runs) != 1 ||
						hb.Runs[0] != (protocol.LeaseRef{RunID: as.RunID, LeaseID: as.LeaseID}) {
						t.Error("held result missed its original control channel", hb.Runs, err)
						return
					}
					result = protocol.HeartbeatResult{LeaseTTLMillis: 60000, Runs: []protocol.LeaseState{
						{RunID: as.RunID, Renewed: afterHold != protocol.DesiredStopped, Desired: afterHold, PauseHandoff: pause},
					}}
				case protocol.OpRunsSubmitArtifact, protocol.OpRunsRelease, protocol.OpRunsYieldPause:
					finals <- env
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()

			if err := r.hello(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("original result recovery did not start")
			}
			held.Store(true)
			if err := r.hello(ctx); err != nil {
				t.Fatal(err)
			}
			close(proceed)
			r.wg.Wait()
			saved, err := r.cfg.Journal.Run(ctx, as.RunID)
			firstObservation := <-observed
			secondObservation := <-observed
			if firstObservation != protocol.RunUnknown || secondObservation != protocol.RunUnknown ||
				err != nil || !saved.Held || !saved.StoppedConfirmed || saved.Phase != journal.PhaseWorkerDone ||
				len(saved.WorkerResult) == 0 || len(finals) != 0 {
				t.Fatal("pause recovery lost the result or bypassed human control", saved, err)
			}
			select {
			case <-r.reconcile:
			default:
				t.Fatal("held recovered report did not request ordinary control")
			}
			if err := r.hello(ctx); err != nil {
				t.Fatal(err)
			}
			if <-observed != protocol.RunWaiting || r.lookup(as.RunID) == nil || len(finals) != 0 {
				t.Fatal("expected the stopped result to wait for owner control")
			}
			r.heartbeat(ctx)
			r.wg.Wait()
			if workers.starts != 0 || len(finals) != 1 {
				t.Fatal("control did not settle exactly one request for the original result", workers.starts, len(finals))
			}
			env := <-finals
			if env.Target.ID != as.RunID {
				t.Fatal("final request lost the original Run", env)
			}
			if afterHold == protocol.DesiredStopped {
				if env.Operation != protocol.OpRunsRelease {
					t.Fatal("Stop did not use the original release path", env)
				}
				return
			}
			var artifact protocol.ArtifactPayload
			if env.Operation != protocol.OpRunsSubmitArtifact || json.Unmarshal(env.Payload, &artifact) != nil ||
				!reflect.DeepEqual(&artifact, workers.result.Result) {
				t.Fatal("handback did not submit the original result", env, artifact)
			}
		})
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
			if scenario == "held" || scenario == "retained authority" {
				recovered, err := r.reconcileImplementationResult(t.Context(), as)
				if recovered || err != nil || len(workers.seen) != 0 || spaces.reads != 0 {
					t.Fatal("held or explicitly authorized continuation entered result recovery", recovered, err)
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
			if err := os.Remove(workerOutcomePath(spaces.prepared.StateDir, 1)); err != nil {
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
				if outcome != "event append failed" {
					observed, err := readWorkerOutcome(spaces.prepared.StateDir, as.RunID, "owned-thread", 1)
					if err != nil || observed == nil {
						t.Fatal("close preceded durable worker outcome", observed, err)
					}
					if outcome == "worker failed" && (observed.Status != "failed" || observed.Error != "original turn failed") {
						t.Fatal("Wait failure was not retained before closure", observed)
					}
					if outcome == "paused" || outcome == "held" || outcome == "cancelled" {
						if observed.Source != "run_control" || observed.Status != "interrupted" {
							t.Fatal("control interruption was saved as completion", observed)
						}
					}
				}
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
