package runner

import (
	"context"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// ownedRecoveryUncertain ends this bounded recovery pass. The original run is
// fenced instead of keeping an executor in a stop/retry loop; subsequent
// explicit reconciliation or bounded external isolation retains its identity.
type ownedRecoveryUncertain struct{ err error }

func (e *ownedRecoveryUncertain) Error() string { return e.err.Error() }
func (e *ownedRecoveryUncertain) Unwrap() error { return e.err }

func isOwnedRecoveryUncertain(err error) bool {
	_, ok := errors.AsType[*ownedRecoveryUncertain](err)
	return ok || errors.Is(err, harness.ErrOwnedWorkerUnconfirmed)
}

// stopOwnedAppServer never prepares a workspace or starts native execution.
// The existing run, lease, workspace and worker origin remain authoritative.
// Missing historical ownership leaves this run retained; independent bounded
// quarantine still permits unrelated work without asserting native closure.
func (r *Runner) stopOwnedAppServer(ctx context.Context, as protocol.Assignment, run journal.Run) (bool, error) {
	if assignmentWorker(as, r.cfg.Worker) != protocol.WorkerAppServer {
		return false, nil
	}
	execution, err := r.resolveExecution(ctx, as)
	if err != nil {
		return true, err
	}
	stopper, ok := execution.Workers.(harness.OwnedWorkerStopper)
	if !ok || !stopper.SupportsOwnedWorkerStop() {
		return false, nil
	}
	workspace, ok := execution.Workspaces.(interface {
		ReattachRecovery(context.Context, protocol.Assignment) (Prepared, error)
	})
	if !ok {
		return true, errors.New("runner: original app-server workspace recovery is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := workspace.ReattachRecovery(ctx, as)
	if err != nil {
		return true, err
	}
	return true, stopper.StopOwnedWorker(ctx, harness.WorkerSpec{RunID: as.RunID, Dir: p.Dir, StateDir: p.StateDir, Thread: run.Thread})
}

// recoverAppServerAttempt settles an interrupted local attempt as a failure,
// allowing the current owner to choose the remaining work from Issue context.
// It never resumes the old turn or silently allocates another native attempt.
func (r *Runner) recoverAppServerAttempt(ctx context.Context, as protocol.Assignment) (bool, error) {
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return false, err
	}
	if run.Attempts == 0 || run.StoppedConfirmed || assignmentWorker(as, r.cfg.Worker) != protocol.WorkerAppServer {
		return false, nil
	}
	if err := r.checkRunWorker(run); err != nil {
		return true, err
	}
	supported, err := r.stopOwnedAppServer(ctx, as, run)
	if !supported || err != nil {
		return supported, err
	}
	return true, r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
}
