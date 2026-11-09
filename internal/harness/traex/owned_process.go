package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
)

// This is local recovery evidence, not a native start receipt or permission to
// repeat input. Old runs without it retain their existing reconciliation path.
type ownedWorkerProcess struct {
	Version              int                    `json:"version"`
	RunID                string                 `json:"run_id"`
	Dir                  string                 `json:"directory"`
	ThreadID             string                 `json:"thread_id,omitempty"`
	Process              ownedProcessIdentity   `json:"process"`
	CloseObserved        bool                   `json:"close_observed,omitempty"`
	Detached             []ownedProcessIdentity `json:"detached_processes,omitempty"`
	ReconciledCloseScope string                 `json:"reconciled_close_scope,omitempty"`
}

const ownedProcessFile = "owned-appserver-process.json"

const knownOwnedProcessSet = "known_owned_process_set"

var errOwnedProcessExited = errors.New("traex: original app-server exited without an independent session close receipt")

func (r ownedWorkerProcess) closed() bool {
	return r.CloseObserved || r.ReconciledCloseScope == knownOwnedProcessSet
}

func (ws *Workers) SupportsOwnedWorkerStop() bool {
	return ownedProcessSupported() && ws.opts.Acquire == nil
}

func (ws *Workers) StopOwnedWorker(ctx context.Context, spec harness.WorkerSpec) (err error) {
	defer func() {
		if err == nil {
			err = codexenv.ConfirmExecutionClosed(ctx, spec.StateDir, "run:"+spec.RunID)
		}
		if err != nil {
			err = fmt.Errorf("%w: %w", harness.ErrOwnedWorkerUnconfirmed, err)
		}
	}()
	record, err := ws.ownedWorkerProcess(spec)
	if err != nil {
		return err
	}
	if err := confirmDetachedProcessesStopped(record.Detached); err != nil {
		return err
	}
	if record.closed() {
		// A recorded close is a historical fact. An unrelated later process
		// reusing the PID must neither invalidate it nor receive a signal.
		return nil
	}
	checkDetached := func() error {
		detached, err := ownedDetachedDescendants(record.Process)
		if err != nil {
			return err
		}
		if len(detached) != 0 {
			record.Detached = detached
			if err := saveOwnedWorkerProcess(spec, record); err != nil {
				return err
			}
			return errors.New("traex: detached native execution remains active; retain original workspace and use bounded isolation")
		}
		return nil
	}
	if err := stopOwnedProcess(ctx, record.Process, checkDetached); err != nil {
		if !errors.Is(err, errOwnedProcessExited) {
			return err
		}
		// Close can reject a detached child, then reap the original leader.
		// Reconcile the original known process set instead of requiring that
		// already-exited leader to supply another historical Close receipt.
		if err := confirmKnownOwnedProcessSetStopped(ctx, record.Process, record.Detached); err != nil {
			return err
		}
		record.ReconciledCloseScope = knownOwnedProcessSet
		return saveOwnedWorkerProcess(spec, record)
	}
	record.CloseObserved = true
	return saveOwnedWorkerProcess(spec, record)
}

func (ws *Workers) ownedWorkerProcess(spec harness.WorkerSpec) (ownedWorkerProcess, error) {
	var record ownedWorkerProcess
	if !ws.SupportsOwnedWorkerStop() || spec.RunID == "" || spec.StateDir == "" {
		return record, errors.New("traex: original app-server ownership is unavailable")
	}
	body, err := os.ReadFile(filepath.Join(spec.StateDir, ownedProcessFile))
	if err != nil {
		return record, fmt.Errorf("traex: read original app-server ownership: %w", err)
	}
	if err := json.Unmarshal(body, &record); err != nil {
		return record, fmt.Errorf("traex: decode original app-server ownership: %w", err)
	}
	if record.Version != 1 || record.RunID != spec.RunID || filepath.Clean(record.Dir) != filepath.Clean(spec.Dir) || spec.Thread != "" && record.ThreadID != "" && record.ThreadID != spec.Thread {
		return record, errors.New("traex: original app-server run or thread identity changed")
	}
	if record.ReconciledCloseScope != "" && record.ReconciledCloseScope != knownOwnedProcessSet {
		return record, errors.New("traex: original app-server close reconciliation scope is unsupported")
	}
	return record, nil
}

func saveOwnedWorkerProcess(spec harness.WorkerSpec, record ownedWorkerProcess) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(spec.StateDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(spec.StateDir, ".owned-appserver-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(spec.StateDir, ownedProcessFile)); err != nil {
		return err
	}
	dir, err := os.Open(spec.StateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (ws *Workers) recordOwnedWorkerProcess(spec harness.WorkerSpec, opts *Options) (*ownedWorkerProcess, error) {
	if !ws.SupportsOwnedWorkerStop() || spec.RunID == "" || spec.StateDir == "" {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(spec.StateDir, ownedProcessFile)); err == nil {
		original, err := ws.ownedWorkerProcess(spec)
		if err == nil {
			if !original.closed() {
				err = errors.New("traex: original native close has not been independently recorded")
			} else {
				err = confirmDetachedProcessesStopped(original.Detached)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%w: original native process must be reconciled before another launch: %w", harness.ErrRetainedResumePending, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	record := &ownedWorkerProcess{Version: 1, RunID: spec.RunID, Dir: spec.Dir}
	opts.onStartProcess = func(cmd *exec.Cmd) error {
		identity, err := inspectOwnedProcess(cmd.Process.Pid)
		if err != nil {
			return err
		}
		record.Process = identity
		return saveOwnedWorkerProcess(spec, *record)
	}
	opts.onBeforeCloseProcess = func() error {
		detached, err := ownedDetachedDescendants(record.Process)
		if err != nil {
			return err
		}
		record.Detached = detached
		if len(detached) != 0 {
			if err := saveOwnedWorkerProcess(spec, *record); err != nil {
				return err
			}
			return errors.New("traex: detached native execution prevents confirmed owned session closure")
		}
		return nil
	}
	opts.onClosedProcess = func() error {
		if err := awaitOwnedProcessStopped(context.Background(), record.Process); err != nil {
			return err
		}
		record.CloseObserved = true
		return saveOwnedWorkerProcess(spec, *record)
	}
	return record, nil
}
