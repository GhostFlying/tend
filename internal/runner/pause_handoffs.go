package runner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type pauseYield struct{}

func (*pauseYield) Error() string { return "yield execution for Issue pause" }

// pauseStopPending keeps the original attempt in PhasePausing. Only a
// positively observed, still-present process set earns an automatic retry.
type pauseStopPending struct{ err error }

func (e *pauseStopPending) Error() string { return e.err.Error() }
func (e *pauseStopPending) Unwrap() error { return e.err }

func (r *Runner) deferOwnedPauseStop(ctx context.Context, a *active, run journal.Run, stopErr error) error {
	as := run.Assignment
	if !as.PauseHandoffs || run.Held || run.Phase != journal.PhaseStarted ||
		!errors.Is(stopErr, harness.ErrOwnedWorkerUnconfirmed) || !r.pauseStopAuthorized(a) {
		return nil
	}
	if p, err := r.cfg.Journal.Pause(ctx, as.RunID); err != nil {
		return errors.Join(stopErr, err)
	} else if p == nil {
		return nil
	}
	if err := r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhasePausing); err != nil {
		return errors.Join(stopErr, err)
	}
	return &pauseStopPending{stopErr}
}

func (r *Runner) recoverPauseStop(ctx context.Context, a *active, run journal.Run) error {
	as := run.Assignment
	if !r.pauseStopAuthorized(a) || run.Held || !as.PauseHandoffs || as.Role == protocol.RoleIssueManager ||
		run.Attempts == 0 || assignmentWorker(as, r.cfg.Worker) != protocol.WorkerAppServer {
		return &pauseStopPending{errors.New("runner: original app-server pause authority is unavailable")}
	}
	if err := r.checkRunWorker(run); err != nil {
		return &pauseStopPending{err}
	}
	if !run.StoppedConfirmed {
		execution, err := r.resolveExecution(ctx, as)
		if err != nil {
			return &pauseStopPending{err}
		}
		observer, ok := execution.Workers.(harness.OwnedWorkerStopObserver)
		if !ok {
			return &pauseStopPending{errors.New("runner: original app-server stop observer is unavailable")}
		}
		if err := r.withOwnedAppServer(ctx, as, run, execution, observer.ObserveOwnedWorkerStop); err != nil {
			return &pauseStopPending{err}
		}
		if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
			return &pauseStopPending{err}
		}
	}
	if !r.pauseStopAuthorized(a) {
		return nil
	}
	if wait, err := r.cfg.Journal.ExternalWait(ctx, as.RunID); err != nil {
		return err
	} else if wait != nil {
		return r.finishExternalWait(ctx, as)
	}
	return r.finishPause(ctx, as)
}

func (r *Runner) pauseStopAuthorized(a *active) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return a.desired == protocol.DesiredPaused && !a.retained
}

type pauseRetry struct {
	delay   time.Duration
	due     time.Time
	blocked bool
}

// Called after the executor is removed, while holding the same lock as start.
func (r *Runner) deferPauseRetry(runID string, pending *pauseStopPending) {
	if pending == nil {
		delete(r.pauseRetries, runID)
		return
	}
	retry := r.pauseRetries[runID]
	if errors.Is(pending, harness.ErrOwnedWorkerStillPresent) {
		retry.delay = min(max(r.cfg.RetryDelay, 2*retry.delay), 30*time.Second)
		retry.due = r.cfg.Now().Add(retry.delay)
	} else {
		retry = pauseRetry{blocked: true}
	}
	r.pauseRetries[runID] = retry
	select {
	case r.pauseWake <- struct{}{}:
	default:
	}
}

func (r *Runner) nextPauseRetry() <-chan time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	var next time.Time
	for _, retry := range r.pauseRetries {
		if !retry.due.IsZero() && (next.IsZero() || retry.due.Before(next)) {
			next = retry.due
		}
	}
	if next.IsZero() {
		return nil
	}
	return time.After(next.Sub(r.cfg.Now()))
}

func (r *Runner) consumePauseRetries() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.cfg.Now()
	for id, retry := range r.pauseRetries {
		if !retry.due.IsZero() && !retry.due.After(now) {
			retry.due = time.Time{}
			r.pauseRetries[id] = retry
		}
	}
}

func (r *Runner) finishPause(ctx context.Context, as protocol.Assignment) error {
	p, err := r.cfg.Journal.Pause(ctx, as.RunID)
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("runner: pause identity unavailable")
	}
	saved, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if !saved.StoppedConfirmed {
		return &completionPending{errors.New("runner: pause awaits physical stop proof")}
	}
	env, err := r.newEnvelope(protocol.OpRunsYieldPause, runTarget(as), protocol.YieldPausePayload{PauseHandoff: *p, ThreadID: saved.Thread})
	if err != nil {
		return err
	}
	if err := r.cfg.Journal.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
		return err
	}
	return r.reportPause(ctx, as)
}

// reportPause retains every original receipt until it has its own ACK. A pause
// never runs workspace completion/cleanup and never replaces unknown work.
func (r *Runner) reportPause(ctx context.Context, as protocol.Assignment) error {
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	var pause *journal.Request
	for i := range requests {
		if requests[i].RunID == as.RunID && requests[i].Envelope.Operation == protocol.OpRunsYieldPause {
			pause = &requests[i]
			break
		}
	}
	if pause == nil {
		return errors.New("runner: original pause receipt unavailable")
	}
	if as.Manager != nil {
		if err := r.flushManagerEvents(ctx, as); err != nil {
			return &completionPending{err}
		}
	} else {
		if err := r.flushEvents(ctx, as); err != nil {
			return &completionPending{err}
		}
	}
	op, err := r.send(ctx, runCredential(as), &pause.Envelope)
	if err != nil || op.Status != protocol.OpSucceeded {
		return &completionPending{errors.Join(err, errors.New("runner: pause receipt awaits settlement"))}
	}
	if as.Manager != nil {
		for _, p := range requests {
			if p.RunID != as.RunID || p.Envelope.Operation != protocol.OpManagerNativeReply {
				continue
			}
			if err := r.sendManagerReceipt(ctx, as, &p.Envelope); err != nil {
				return &completionPending{err}
			}
			if err := r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
				return err
			}
		}
	}
	if err := r.recoverRunMessages(ctx, as); err != nil {
		return &completionPending{err}
	}
	return r.finishJournal(ctx, as.RunID)
}

func (r *Runner) pauseReceiptReady(ctx context.Context, run journal.Run) (bool, error) {
	if !run.Assignment.PauseHandoffs || run.Phase != journal.PhaseReleasing || !run.StoppedConfirmed || run.Held {
		return false, nil
	}
	p, err := r.cfg.Journal.Pause(ctx, run.Assignment.RunID)
	if err != nil || p == nil {
		return false, err
	}
	reqs, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return false, err
	}
	for _, req := range reqs {
		if req.RunID != run.Assignment.RunID || req.Envelope.Operation != protocol.OpRunsYieldPause {
			continue
		}
		var payload protocol.YieldPausePayload
		if json.Unmarshal(req.Envelope.Payload, &payload) != nil {
			return false, nil
		}
		return payload.PauseHandoff == *p && payload.ThreadID == run.Thread, nil
	}
	return false, nil
}
