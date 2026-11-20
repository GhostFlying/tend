package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type pauseYield struct{}

func (*pauseYield) Error() string { return "yield execution for Issue pause" }

// pauseStopPending retains the original Run while stop evidence or a durably
// recorded pause receipt awaits reconciliation.
type pauseStopPending struct{ err error }

func (e *pauseStopPending) Error() string { return e.err.Error() }
func (e *pauseStopPending) Unwrap() error { return e.err }

var errPauseResultTimeout = errors.New("runner: paused result reconciliation timed out")

type pauseResultActivityKey struct{}

type pauseResultActivity struct {
	mu   sync.Mutex
	last time.Time
}

func (a *pauseResultActivity) record() {
	a.mu.Lock()
	a.last = time.Now()
	a.mu.Unlock()
}

// boundPauseResultIdle limits time without forward progress. The same attempt
// may take longer when evidence is still being read or transmitted.
func boundPauseResultIdle(ctx context.Context, timeout time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	activity := &pauseResultActivity{last: time.Now()}
	var timer *time.Timer
	activity.mu.Lock()
	timer = time.AfterFunc(timeout, func() {
		activity.mu.Lock()
		defer activity.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if remaining := timeout - time.Since(activity.last); remaining > 0 {
			timer.Reset(remaining)
			return
		}
		cancel(errPauseResultTimeout)
	})
	activity.mu.Unlock()
	return context.WithValue(ctx, pauseResultActivityKey{}, activity), func() {
		cancel(nil)
		activity.mu.Lock()
		timer.Stop()
		activity.mu.Unlock()
	}
}

func (r *Runner) deferOwnedPauseStop(ctx context.Context, a *active, run journal.Run, stopErr error) error {
	as := run.Assignment
	if !as.PauseHandoffs || run.Held || run.Phase != journal.PhaseStarted || run.Attempts == 0 ||
		!errors.Is(stopErr, harness.ErrOwnedWorkerUnconfirmed) || r.pauseStopDesired(a) != protocol.DesiredPaused {
		return nil
	}
	if p, err := r.cfg.Journal.Pause(ctx, as.RunID); err != nil {
		return errors.Join(stopErr, err)
	} else if p == nil {
		return nil
	}
	parked, err := r.cfg.Journal.TryBeginPauseStop(ctx, as.RunID, run.Attempts)
	if err != nil {
		return errors.Join(stopErr, err)
	}
	if !parked {
		return nil
	}
	return &pauseStopPending{stopErr}
}

func (r *Runner) recoverPauseStop(ctx, runCtx context.Context, a *active, run journal.Run) (bool, error) {
	as := run.Assignment
	desired := r.pauseStopDesired(a)
	if desired == protocol.DesiredHeld {
		return false, &pauseStopPending{errHeld}
	}
	if desired != protocol.DesiredPaused || !as.PauseHandoffs || as.Role == protocol.RoleIssueManager ||
		run.Attempts == 0 || assignmentWorker(as, r.cfg.Worker) != protocol.WorkerAppServer {
		return false, &pauseStopPending{errors.New("runner: original app-server pause authority is unavailable")}
	}
	if err := r.checkRunWorker(run); err != nil {
		return false, &pauseStopPending{err}
	}
	if !run.StoppedConfirmed {
		execution, err := r.resolveExecution(ctx, as)
		if err != nil {
			return false, &pauseStopPending{err}
		}
		observer, ok := execution.Workers.(harness.OwnedWorkerStopObserver)
		if !ok {
			return false, &pauseStopPending{errors.New("runner: original app-server stop observer is unavailable")}
		}
		if err := r.withOwnedAppServer(ctx, as, run, execution, observer.ObserveOwnedWorkerStop); err != nil {
			return false, &pauseStopPending{err}
		}
		if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
			return false, &pauseStopPending{err}
		}
	}
	switch r.pauseStopDesired(a) {
	case protocol.DesiredPaused:
	case protocol.DesiredHeld:
		return false, &pauseStopPending{errHeld}
	default:
		return false, nil
	}
	if run.Held {
		// A fresh DesiredPaused after owner control is the explicit handback.
		// Clear the local hold only once the original process is proven stopped.
		if err := r.cfg.Journal.SetHeld(ctx, as.RunID, false); err != nil {
			return false, err
		}
	}
	if wait, err := r.cfg.Journal.ExternalWait(ctx, as.RunID); err != nil {
		return false, err
	} else if wait != nil {
		return false, r.finishExternalWait(ctx, as)
	}
	// A native result may already be durable even though closure previously
	// prevented it from entering worker_done. Settle it before yielding.
	// Stop can interrupt result recovery. Takeover also stops acceptance
	// uploads. Local implementation recovery may still checkpoint its
	// result for finalization after handback.
	controlCtx := runCtx
	if as.Role == protocol.RoleAccept {
		var release func()
		controlCtx, release = r.withResultControl(runCtx, a)
		defer release()
	}
	resultCtx, stopIdle := boundPauseResultIdle(controlCtx, r.cfg.RequestTimeout)
	defer stopIdle()
	recovered, err := r.reconcileAcceptanceVerdict(resultCtx, as)
	if !recovered {
		recovered, err = r.reconcileImplementationResult(resultCtx, as)
	}
	if recovered && err == nil {
		return true, nil
	}
	if r.stopRequested(runCtx, a) {
		return false, nil
	}
	if cause := context.Cause(resultCtx); errors.Is(cause, errPauseResultTimeout) || errors.Is(cause, errHeld) {
		return false, &pauseStopPending{cause}
	}
	if err != nil {
		if _, failedTurn := errors.AsType[*stepError](err); !failedTurn {
			return false, &pauseStopPending{err}
		}
		// A submitted file from a known failed native turn is not a
		// completed result. The physical pause can still be acknowledged.
	}
	return false, r.finishPause(ctx, as, a)
}

func (r *Runner) pauseStopDesired(a *active) protocol.RunDesired {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.retained {
		return ""
	}
	return a.desired
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
	if errors.Is(pending, harness.ErrOwnedWorkerStillPresent) || errors.Is(pending, errHeld) ||
		errors.Is(pending, errPauseResultTimeout) || isRetryable(pending) {
		r.schedulePauseRetry(runID)
	} else {
		r.pauseRetries[runID] = pauseRetry{blocked: true}
	}
}

// schedulePauseRetry runs with r.mu held. A Hello observing owner control
// must preserve a future check, even if its timer was just consumed.
func (r *Runner) schedulePauseRetry(runID string) {
	retry := r.pauseRetries[runID]
	now := r.cfg.Now()
	if retry.blocked || retry.due.After(now) {
		return
	}
	retry.delay = min(max(r.cfg.RetryDelay, 2*retry.delay), 30*time.Second)
	retry.due = now.Add(retry.delay)
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

func (r *Runner) finishPause(ctx context.Context, as protocol.Assignment, a *active) error {
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
	return r.reportPause(ctx, as, a)
}

// reportPause retains every original receipt until it has its own ACK. A pause
// never runs workspace completion/cleanup and never replaces unknown work.
func (r *Runner) reportPause(ctx context.Context, as protocol.Assignment, a *active) error {
	r.mu.Lock()
	a.pauseOnly, a.reporting = true, true
	r.mu.Unlock()
	if r.pauseStopDesired(a) == protocol.DesiredHeld {
		return &pauseStopPending{errHeld}
	}
	requests, err := r.cfg.Journal.ManagerControlRequests(ctx, as.RunID)
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
	flush := r.flushEvents
	if as.Manager != nil {
		flush = r.flushManagerEvents
	}
	if err := flush(ctx, as); err != nil {
		if isRetryable(err) {
			return &pauseStopPending{err}
		}
		return &completionPending{err}
	}
	var op protocol.Operation
	err = r.retry(ctx, func() error {
		switch r.pauseStopDesired(a) {
		case "":
			return errFenced
		case protocol.DesiredHeld:
			return errHeld
		}
		var sendErr error
		op, sendErr = r.sendOnce(ctx, runCredential(as), &pause.Envelope)
		return sendErr
	})
	if errors.Is(err, errHeld) || pauseReceiptAwaitingHandback(err) {
		return &pauseStopPending{errHeld}
	}
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

// A takeover can race the network request even after the last local control
// check. The server can defer the first receipt without consuming its identity.
func pauseReceiptAwaitingHandback(err error) bool {
	var details struct {
		Desired protocol.RunDesired `json:"desired_state"`
	}
	pe, ok := errors.AsType[*protocol.Error](err)
	return ok && pe.Code == protocol.CodeInvalidTransition &&
		json.Unmarshal(pe.Details, &details) == nil && details.Desired == protocol.DesiredHeld
}

func (r *Runner) pauseReceiptReady(ctx context.Context, run journal.Run) (bool, error) {
	if !run.Assignment.PauseHandoffs || run.Phase != journal.PhaseReleasing || !run.StoppedConfirmed {
		return false, nil
	}
	p, err := r.cfg.Journal.Pause(ctx, run.Assignment.RunID)
	if err != nil || p == nil {
		return false, err
	}
	reqs, err := r.cfg.Journal.RequestsByOperation(ctx, protocol.OpRunsYieldPause)
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
