package traex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/harness"
)

var _ harness.FileResultReconciler = (*Workers)(nil)

// ReconcileImplementationResult restores an original submit_result without
// attaching a native session or repeating any upload. The submitted result and
// independently confirmed process closure remain separate facts.
func (ws *Workers) ReconcileImplementationResult(ctx context.Context, spec harness.WorkerSpec) (harness.WorkerResult, error) {
	if err := ctx.Err(); err != nil {
		return harness.WorkerResult{}, fmt.Errorf("%w: result recovery cancelled: %w", harness.ErrRetainedResumePending, err)
	}
	if spec.StateDir == "" {
		return harness.WorkerResult{}, harness.ErrNoImplementationResult
	}
	if _, err := os.Stat(filepath.Join(spec.StateDir, "result.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return harness.WorkerResult{}, harness.ErrNoImplementationResult
		}
		return harness.WorkerResult{}, fmt.Errorf("%w: inspect original submitted result: %w", harness.ErrRetainedResumePending, err)
	}
	original, err := ws.ownedWorkerProcess(spec)
	if err != nil {
		return harness.WorkerResult{}, fmt.Errorf("%w: original result ownership: %w", harness.ErrRetainedResumePending, err)
	}
	if original.ThreadID == "" {
		return harness.WorkerResult{}, fmt.Errorf("%w: original submitted result has no native thread binding", harness.ErrRetainedResumePending)
	}
	results := harness.Results{Spec: spec}
	if err := results.Restore(); err != nil {
		return harness.WorkerResult{}, fmt.Errorf("%w: restore original submitted result: %w", harness.ErrRetainedResumePending, err)
	}
	if results.Result() == nil {
		return harness.WorkerResult{}, fmt.Errorf("%w: original submitted result disappeared during recovery", harness.ErrRetainedResumePending)
	}
	if err := ws.StopOwnedWorker(ctx, spec); err != nil {
		return harness.WorkerResult{}, fmt.Errorf("%w: original submitted result closure: %w", harness.ErrRetainedResumePending, err)
	}
	// An original submission may finish while its session is closing. Read the
	// final original payload after closure without replaying the submission.
	final := harness.Results{Spec: spec}
	if err := final.Restore(); err != nil {
		return harness.WorkerResult{}, fmt.Errorf("%w: restore closed original result: %w", harness.ErrRetainedResumePending, err)
	}
	if final.Result() == nil {
		return harness.WorkerResult{}, fmt.Errorf("%w: closed original result is unavailable", harness.ErrRetainedResumePending)
	}
	return harness.WorkerResult{Result: final.Result()}, nil
}
