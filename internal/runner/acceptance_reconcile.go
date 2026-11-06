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

// reconcileAcceptanceVerdict never enters the ordinary native start path. The
// harness must first prove the original owned session closed and then reconcile
// only that session's complete sealed input and original upload identities.
func (r *Runner) reconcileAcceptanceVerdict(ctx context.Context, as protocol.Assignment) (bool, error) {
	if as.Role != protocol.RoleAccept || !as.AcceptanceEvidenceFiles || assignmentWorker(as, r.cfg.Worker) != protocol.WorkerAppServer {
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
		// This local owned-session receipt grants no permission to bypass
		// the separate retained-native reconciliation authority.
		return true, harness.ErrRetainedResumePending
	}
	execution, err := r.resolveExecution(ctx, as)
	if err != nil {
		return true, err
	}
	reconciler, ok := execution.Workers.(harness.FileVerdictReconciler)
	if !ok {
		return false, nil
	}
	workspace, ok := execution.Workspaces.(interface {
		ReattachAcceptanceVerdict(context.Context, protocol.Assignment) (Prepared, error)
	})
	if !ok {
		return true, fmt.Errorf("%w: original acceptance workspace cannot be read", harness.ErrRetainedResumePending)
	}
	p, err := workspace.ReattachAcceptanceVerdict(ctx, as)
	if err != nil {
		return true, fmt.Errorf("%w: %v", harness.ErrRetainedResumePending, err)
	}
	evidence := &acceptanceEvidence{r: r, as: as, prepared: p}
	result, err := reconciler.ReconcileAcceptanceVerdict(ctx, harness.WorkerSpec{
		RunID: as.RunID, Role: as.Role, Dir: p.Dir, StateDir: p.StateDir,
		Thread: run.Thread, Attempt: run.Attempts, AcceptanceEvidenceFiles: true,
		SealVerdictFiles: evidence.seal, VerdictFilesIdentity: evidence.identity,
	})
	if errors.Is(err, harness.ErrNoAcceptanceVerdict) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if result.Verdict == nil {
		return true, errors.New("runner: original acceptance verdict is unconfirmed")
	}
	body, err := json.Marshal(result)
	if err != nil {
		return true, err
	}
	// Save the result first: a crash before the closure bit is saved resumes
	// completion, rather than reopening the native work phase.
	if err := r.cfg.Journal.WorkerDone(ctx, as.RunID, body); err != nil {
		return true, err
	}
	return true, r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
}
