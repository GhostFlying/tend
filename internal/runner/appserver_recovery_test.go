package runner

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type ownedRecoveryWorkspace struct {
	prepared  Prepared
	reads     int
	mutations int
}

func (w *ownedRecoveryWorkspace) ReattachRecovery(context.Context, protocol.Assignment) (Prepared, error) {
	w.reads++
	return w.prepared, nil
}
func (w *ownedRecoveryWorkspace) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	w.mutations++
	return Prepared{}, errors.New("recovery must not prepare")
}
func (w *ownedRecoveryWorkspace) Collect(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	w.mutations++
	return protocol.ArtifactPayload{}, errors.New("recovery must not collect")
}
func (w *ownedRecoveryWorkspace) Finish(context.Context, protocol.Assignment) error {
	w.mutations++
	return errors.New("recovery must not clean up before settlement")
}

type ownedRecoveryWorker struct {
	stops  []harness.WorkerSpec
	starts int
	err    error
}

func (w *ownedRecoveryWorker) SupportsOwnedWorkerStop() bool { return true }
func (w *ownedRecoveryWorker) StopOwnedWorker(ctx context.Context, spec harness.WorkerSpec) error {
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("native stop has no deadline")
	}
	w.stops = append(w.stops, spec)
	return w.err
}
func (w *ownedRecoveryWorker) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	return nil, errors.New("recovery must not start another native turn")
}

func appserverRecoveryFixture(t *testing.T) (*Runner, protocol.Assignment, *ownedRecoveryWorker, *ownedRecoveryWorkspace) {
	t.Helper()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{RunID: "original-run", LeaseID: "original-lease", TaskID: "original-task", Role: protocol.RoleImplement, Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, "original-thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	worker := &ownedRecoveryWorker{}
	workspace := &ownedRecoveryWorkspace{prepared: Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}}
	r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Workers: worker, Workspaces: workspace, Now: clock.Now, IDs: testkit.NewIDGen(clock, 8), Log: slog.New(slog.DiscardHandler), RetryDelay: time.Millisecond})
	return r, as, worker, workspace
}

func TestAppServerRestartReconcilesOriginalProcessWithoutNativeReplay(t *testing.T) {
	r, as, worker, workspace := appserverRecoveryFixture(t)
	before, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	a := &active{as: as, desired: protocol.DesiredRun}
	err = r.work(t.Context(), t.Context(), as, a)
	failure, ok := errors.AsType[*stepError](err)
	if !ok || failure.reason != "worker_interrupted_after_runner_restart" {
		t.Fatalf("recovered attempt did not return an explicit owner-visible failure: %v", err)
	}
	after, err := r.cfg.Journal.Run(t.Context(), as.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.StoppedConfirmed || after.Attempts != before.Attempts || after.Thread != before.Thread || !reflect.DeepEqual(after.Assignment, before.Assignment) || worker.starts != 0 || workspace.mutations != 0 || workspace.reads != 1 || len(worker.stops) != 1 {
		t.Fatalf("recovery changed identity or performed new work: before=%+v after=%+v stops=%d starts=%d reads=%d mutations=%d", before, after, len(worker.stops), worker.starts, workspace.reads, workspace.mutations)
	}
	stop := worker.stops[0]
	if stop.RunID != as.RunID || stop.Thread != before.Thread || stop.Dir != workspace.prepared.Dir || stop.StateDir != workspace.prepared.StateDir {
		t.Fatalf("stop did not carry the original run binding: %+v", stop)
	}
}

func TestAppServerRecoveryFailureKeepsOriginalIdentityAndResources(t *testing.T) {
	r, as, worker, workspace := appserverRecoveryFixture(t)
	worker.err = errors.New("original native process identity cannot be verified")
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	if !isCompletionPending(err) {
		t.Fatalf("unknown closure was not retained: %v", err)
	}
	after, afterRequests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, afterRequests) || worker.starts != 0 || workspace.mutations != 0 {
		t.Fatalf("unknown closure altered original work: before=%+v after=%+v", before, after)
	}
}

func TestAppServerRecoveryFailureEndsBoundedPassInsteadOfRetryingForever(t *testing.T) {
	r, as, worker, workspace := appserverRecoveryFixture(t)
	worker.err = context.DeadlineExceeded
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun})
	if _, ok := errors.AsType[*ownedRecoveryUncertain](err); !ok || ctx.Err() != nil {
		t.Fatalf("uncertain recovery did not end its bounded pass: %v", err)
	}
	after, afterRequests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	before.Fenced = true
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, afterRequests) || worker.starts != 0 || len(worker.stops) != 1 || workspace.mutations != 0 {
		t.Fatalf("bounded recovery looped or lost its original work: before=%+v after=%+v stops=%d", before, after, len(worker.stops))
	}
}
