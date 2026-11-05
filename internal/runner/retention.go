package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/runner/journal"
)

// retainRun serializes retention with irreversible local settlement without
// holding the host-wide mutex across workspace cleanup. A settlement already
// completed first must not be relabelled as a retained journal.
func (r *Runner) retainRun(ctx context.Context, id string) error {
	for {
		r.mu.Lock()
		a := r.active[id]
		if a == nil {
			err := r.fenceRetainedJournal(ctx, id)
			r.mu.Unlock()
			return err
		}
		r.mu.Unlock()
		unlock, lockErr := r.lockSettlement(ctx, a)
		if lockErr != nil {
			return lockErr
		}
		r.mu.Lock()
		if r.active[id] != a {
			r.mu.Unlock()
			unlock()
			continue
		}
		err := r.fenceRetainedJournal(ctx, id)
		if err == nil {
			a.retained = true
			a.cancel(errFenced)
		}
		r.mu.Unlock()
		unlock()
		return err
	}
}

// settleRun keeps original requests and local ownership until an authoritative
// control response permits settlement. Business facts may still be reported.
func (r *Runner) settleRun(ctx context.Context, id string, mutate func() error) (bool, error) {
	a := r.lookup(id)
	if a == nil {
		return true, mutate()
	}
	unlock, err := r.lockSettlement(ctx, a)
	if err != nil {
		return false, err
	}
	defer unlock()
	r.mu.Lock()
	retained := a.retained
	r.mu.Unlock()
	if retained {
		return false, nil
	}
	return true, mutate()
}

func (r *Runner) finishJournal(ctx context.Context, id string) error {
	_, err := r.settleRun(ctx, id, func() error { return r.cfg.Journal.Done(ctx, id) })
	return err
}

func (r *Runner) fenceRetainedJournal(ctx context.Context, id string) error {
	if _, err := r.cfg.Journal.Run(ctx, id); err != nil {
		return err
	}
	if err := r.cfg.Journal.ClearRetainedStop(ctx, id); err != nil {
		return err
	}
	return r.cfg.Journal.SetFenced(ctx, id, true)
}

func (r *Runner) lockSettlement(ctx context.Context, a *active) (func(), error) {
	r.mu.Lock()
	if a.settlement == nil {
		a.settlement = make(chan struct{}, 1)
	}
	gate := a.settlement
	r.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// authorizeRetainedSettlement orders later Continue/Stop with retention and final
// settlement. A cancelled executor must exit before any fresh attempt starts.
func (r *Runner) authorizeRetainedSettlement(ctx context.Context, id string, clearFence bool) error {
	if clearFence {
		return r.authorizeRetainedControl(ctx, id, true, func() error {
			return r.cfg.Journal.ClearRetainedStop(ctx, id)
		})
	}
	return r.authorizeRetainedControl(ctx, id, clearFence, nil)
}

func (r *Runner) authorizeRetainedControl(ctx context.Context, id string, clearFence bool, record func() error) error {
	for {
		r.mu.Lock()
		a := r.active[id]
		r.mu.Unlock()
		if a == nil {
			if record != nil {
				if err := record(); err != nil {
					return err
				}
			}
			if clearFence {
				return r.cfg.Journal.SetFenced(ctx, id, false)
			}
			return nil
		}
		unlock, err := r.lockSettlement(ctx, a)
		if err != nil {
			return err
		}
		r.mu.Lock()
		if r.active[id] != a {
			r.mu.Unlock()
			unlock()
			continue
		}
		if record != nil {
			err = record()
		}
		if err == nil && clearFence {
			err = r.cfg.Journal.SetFenced(ctx, id, false)
		}
		if err == nil && a.retained {
			a.retained = false
			a.reconcileOnExit = true
		}
		if err == nil && record != nil && (!clearFence || a.reporting) {
			// A control can reach a reporting executor just before it exits.
			// Stop's receipt or a Continue revoking that receipt must not lose
			// its successor when start still sees the old reporting executor.
			a.reconcileOnExit = true
		}
		r.mu.Unlock()
		unlock()
		return err
	}
}

// settledHeldRun only accepts the original completed journal as a local fact.
func (r *Runner) settledHeldRun(ctx context.Context, held journal.Run) bool {
	saved, err := r.cfg.Journal.HistoricalRun(ctx, held.Assignment.RunID)
	return err == nil && saved.Phase == journal.PhaseDone &&
		saved.Assignment.RunID == held.Assignment.RunID && saved.Assignment.LeaseID == held.Assignment.LeaseID &&
		saved.Assignment.TaskID == held.Assignment.TaskID && saved.Assignment.Role == held.Assignment.Role &&
		saved.Assignment.Generation == held.Assignment.Generation && saved.Assignment.SpecVersion == held.Assignment.SpecVersion
}

// markExecutorFenced cannot overwrite newer authoritative Continue/Stop.
func (r *Runner) markExecutorFenced(ctx context.Context, a *active) error {
	unlock, err := r.lockSettlement(ctx, a)
	if err != nil {
		return err
	}
	defer unlock()
	r.mu.Lock()
	superseded := a.reconcileOnExit && !a.retained
	r.mu.Unlock()
	if superseded {
		return nil
	}
	return r.cfg.Journal.SetFenced(ctx, a.as.RunID, true)
}

// A rejected attachment is a local Run outcome, not failure of host recovery.
// Direct Hello callers can inspect it; the host startup and reconciliation loops
// keep the rejected journals fenced while continuing other work.
type retainedContinuationsRejected struct{ runs []string }

func (e *retainedContinuationsRejected) Error() string {
	return fmt.Sprintf("runner: strict retained continuations remain fenced: %v", e.runs)
}

func (r *Runner) reconcileHello(ctx context.Context) error {
	err := r.freshHello(ctx)
	if _, rejected := errors.AsType[*retainedContinuationsRejected](err); rejected {
		return nil
	}
	return err
}

func (r *Runner) rejectRetainedContinuation(ctx context.Context, held journal.Run, id, reason string) error {
	if held.Assignment.RunID != "" {
		if err := r.retainRun(ctx, id); err != nil {
			if !errors.Is(err, journal.ErrNotFound) || !r.settledHeldRun(ctx, held) {
				return err
			}
		}
	}
	r.cfg.Log.WarnContext(ctx, "strict continuation rejected; preserving original run", "run_id", id, "reason", reason)
	return nil
}
