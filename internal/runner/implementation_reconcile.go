package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// reconcileImplementationResult recovers an explicitly submitted result without
// starting native work or repeating uploads. The harness verifies the original
// result and owned process before returning it to the normal completion path.
func (r *Runner) reconcileImplementationResult(ctx context.Context, as protocol.Assignment) (bool, error) {
	if as.Role != protocol.RoleImplement || assignmentWorker(as, r.cfg.Worker) != protocol.WorkerAppServer {
		return false, nil
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return true, err
	}
	if run.Phase != journal.PhaseStarted || run.Attempts == 0 || run.Thread == "" || run.Held {
		return false, nil
	}
	if err := r.checkRunWorker(run); err != nil {
		return true, err
	}
	strictResume, err := r.cfg.Journal.RetainedResume(ctx, as.RunID)
	if err != nil {
		return true, err
	}
	if strictResume != nil {
		return true, harness.ErrRetainedResumePending
	}
	execution, err := r.resolveExecution(ctx, as)
	if err != nil {
		return true, err
	}
	reconciler, ok := execution.Workers.(harness.FileResultReconciler)
	if !ok {
		return false, nil
	}
	workspace, ok := execution.Workspaces.(interface {
		ReattachRecovery(context.Context, protocol.Assignment) (Prepared, error)
	})
	if !ok {
		return true, fmt.Errorf("%w: original implementation workspace cannot be read", harness.ErrRetainedResumePending)
	}
	p, err := workspace.ReattachRecovery(ctx, as)
	if err != nil {
		return true, fmt.Errorf("%w: original implementation workspace: %w", harness.ErrRetainedResumePending, err)
	}
	result, err := reconciler.ReconcileImplementationResult(ctx, harness.WorkerSpec{
		RunID: as.RunID, Role: as.Role, Dir: p.Dir, StateDir: p.StateDir,
		Thread: run.Thread, Attempt: run.Attempts, Spec: as.Spec,
	})
	if errors.Is(err, harness.ErrNoImplementationResult) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if result.Result == nil {
		return true, errors.New("runner: original implementation result is unconfirmed")
	}
	outcome, err := r.restoreWorkerOutcome(ctx, as, run, p)
	if err != nil {
		return true, err
	}
	if outcome == nil || outcome.Status == "unknown" || outcome.Status == "interrupted" && outcome.Source != "run_control" {
		return true, fmt.Errorf("%w: original implementation terminal outcome is unknown", harness.ErrRetainedResumePending)
	}
	if outcome.Status != "completed" {
		if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
			return true, err
		}
		if outcome.Source == "run_control" {
			// An explicitly paused or held attempt retains its existing resume
			// path; its result file does not turn interruption into completion.
			return false, nil
		}
		message := outcome.Error
		if message == "" {
			message = "the original native turn ended " + outcome.Status
		}
		return true, failed("worker_failed", errors.New(message))
	}
	body, err := json.Marshal(result)
	if err != nil {
		return true, err
	}
	// Save the result before the closure bit so a crash resumes completion,
	// rather than re-entering the ordinary interrupted-attempt recovery.
	if err := r.cfg.Journal.WorkerDone(ctx, as.RunID, body); err != nil {
		return true, err
	}
	return true, r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
}
