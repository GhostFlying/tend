package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type retainedStartupWorkers struct {
	err      error
	releases int
}

func (w *retainedStartupWorkers) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	return nil, w.err
}
func (w *retainedStartupWorkers) Release(context.Context, string, string) error {
	w.releases++
	return nil
}
func (*retainedStartupWorkers) NativeLiveMessages() {}

func TestNativeStartupControlKeepsSessionWithoutFalseStopProof(t *testing.T) {
	for _, desired := range []protocol.RunDesired{protocol.DesiredPaused, protocol.DesiredHeld} {
		t.Run(string(desired), func(t *testing.T) {
			clock := testkit.NewClock()
			ctx := t.Context()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run_native_startup", TaskID: "task_native_startup", Role: protocol.RoleImplement}
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
				t.Fatal(err)
			}
			if err := j.SetThread(ctx, as.RunID, "original"); err != nil {
				t.Fatal(err)
			}
			cause, want := harness.ErrPaused, errPaused
			if desired == protocol.DesiredHeld {
				cause, want = harness.ErrHeld, errHeld
			}
			workers := &retainedStartupWorkers{err: errors.Join(harness.ErrRejected, cause)}
			r := New(Config{Journal: j, Worker: protocol.WorkerAppServer, Now: clock.Now, Workers: workers, Workspaces: completionWorkspaces{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			if err := r.work(ctx, ctx, as, a); !errors.Is(err, want) {
				t.Fatalf("native startup control = %v", err)
			}
			run, err := j.Run(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if workers.releases != 0 || run.StoppedConfirmed || run.Phase != journal.PhaseStarted || len(run.WorkerResult) != 0 || a.desired != desired || run.Held != (desired == protocol.DesiredHeld) {
				t.Fatalf("retained startup session was finalized: releases=%d run=%+v desired=%s", workers.releases, run, a.desired)
			}
		})
	}
}
