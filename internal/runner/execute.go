package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// stepError is a failure of a run's own work, with the reason reported to
// tend when the run is released.
type stepError struct {
	reason string
	err    error
}

func (e *stepError) Error() string { return e.reason + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func failed(reason string, err error) error { return &stepError{reason: reason, err: err} }

// execute drives a journaled run from its current phase to done. ctx is the
// runner's lifetime; runCtx ends when the run is fenced or stopped.
func (r *Runner) execute(ctx, runCtx context.Context, runID string, a *active) error {
	for {
		run, err := r.cfg.Journal.Run(ctx, runID)
		if errors.Is(err, journal.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		as := run.Assignment
		switch run.Phase {
		case journal.PhaseClaimed:
			err = r.prepare(runCtx, as)
		case journal.PhaseStarted:
			err = r.work(ctx, runCtx, as, a)
		case journal.PhaseSubmitting, journal.PhaseReleasing:
			r.mu.Lock()
			a.reporting = true
			r.mu.Unlock()
			return r.report(ctx, as)
		default:
			return fmt.Errorf("runner: run %s has unknown phase %q", runID, run.Phase)
		}
		if err == nil {
			continue
		}
		cause := context.Cause(runCtx)
		switch {
		case ctx.Err() != nil:
			// Shutting down: the journal keeps the phase for the next start.
			return nil
		case errors.Is(cause, errFenced):
			r.cfg.Log.WarnContext(ctx, "run fenced; waiting for reconciliation", "run_id", runID)
			return r.cfg.Journal.SetFenced(ctx, runID, true)
		case errors.Is(err, errPaused):
			// The worker was stopped for a pause; the next attempt
			// waits until tend wants the run again.
			continue
		case errors.Is(cause, errStopped):
			err = r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped by tend"})
		default:
			reason := "runner_error"
			if se, ok := errors.AsType[*stepError](err); ok {
				reason = se.reason
			}
			r.cfg.Log.WarnContext(ctx, "run failed", "run_id", runID, "reason", reason, "err", err)
			err = r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason})
		}
		if err != nil {
			return err
		}
	}
}

// prepare lays out the workspace, reports it and the start, and moves the
// run to started.
func (r *Runner) prepare(ctx context.Context, as protocol.Assignment) error {
	p, err := r.cfg.Workspaces.Prepare(ctx, as)
	if err != nil {
		return failed(reasonFor(err, "workspace_prepare_failed"), err)
	}
	cred := runCredential(as)
	if as.Role == protocol.RoleImplement {
		env, err := r.newEnvelope(protocol.OpRunsReportWorkspace, runTarget(as), p.Report)
		if err != nil {
			return err
		}
		if _, err := r.send(ctx, cred, env); err != nil {
			return failed("workspace_report_rejected", err)
		}
	}
	env, err := r.newEnvelope(protocol.OpRunsReportStarted, runTarget(as), nil)
	if err != nil {
		return err
	}
	// A start already recorded, for example before a restart, is fine.
	if _, err := r.send(ctx, cred, env); err != nil && protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		return failed("start_rejected", err)
	}
	return r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhaseStarted)
}

func runTarget(as protocol.Assignment) protocol.Target {
	return protocol.Target{Type: "run", ID: as.RunID}
}

// work runs the worker and journals the run's final report.
func (r *Runner) work(ctx, runCtx context.Context, as protocol.Assignment, a *active) error {
	if err := r.waitUntilWanted(runCtx, a); err != nil {
		return err
	}
	// Each attempt can be stopped on its own, so a pause interrupts the
	// worker without giving up the run.
	attemptCtx, stopAttempt := context.WithCancelCause(runCtx)
	defer stopAttempt(nil)
	r.mu.Lock()
	a.stopAttempt = stopAttempt
	paused := a.desired == protocol.DesiredPaused
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		a.stopAttempt = nil
		r.mu.Unlock()
	}()
	if paused {
		return errPaused
	}
	runCtx = attemptCtx
	// When the attempt was stopped, its cause explains the failure better
	// than whatever error the interrupted step returned.
	stopped := func(err error) error {
		if cause := context.Cause(runCtx); cause != nil {
			return cause
		}
		return err
	}
	p, err := r.cfg.Workspaces.Prepare(runCtx, as)
	if err != nil {
		return stopped(failed(reasonFor(err, "workspace_prepare_failed"), err))
	}
	attempt, err := r.cfg.Journal.NextAttempt(ctx, as.RunID)
	if err != nil {
		return err
	}
	if attempt > 1 {
		// The run resumes after a fence or a restart.
		if err := r.cfg.Journal.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: protocol.EventRunRunning}); err != nil {
			return err
		}
	}
	w, err := r.cfg.Workers.Start(runCtx, harness.WorkerSpec{
		RunID: as.RunID, Role: as.Role, Dir: p.Dir, ScratchDir: p.ScratchDir, Spec: as.Spec, Artifact: as.Artifact,
		Feedback: as.Feedback, Attempt: attempt,
	})
	if err != nil {
		return stopped(failed("worker_start_failed", err))
	}
	uploaded := make(chan struct{})
	uploaderCtx, stopUploader := context.WithCancel(ctx)
	go func() {
		defer close(uploaded)
		r.uploadEvents(uploaderCtx, as)
	}()
	var appendErr error
	for ev := range w.Events() {
		if appendErr == nil {
			appendErr = r.cfg.Journal.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: ev.NativeID, Kind: ev.Kind, Body: boundedBody(ev.Body)})
		}
	}
	res, werr := w.Wait()
	stopUploader()
	<-uploaded
	if cause := context.Cause(runCtx); cause != nil {
		// errFenced or errStopped from the run, or errPaused from this
		// attempt.
		return cause
	}
	if appendErr != nil {
		return appendErr
	}
	if werr != nil {
		return failed("worker_failed", werr)
	}

	if as.Role == protocol.RoleAccept {
		verdict := res.Verdict
		if verdict == nil {
			evidence, err := json.Marshal(map[string]string{"summary": res.Summary, "note": "the worker gave no verdict"})
			if err != nil {
				return err
			}
			verdict = &protocol.VerdictPayload{Verdict: protocol.VerdictInconclusive, Evidence: evidence}
		}
		return r.finish(ctx, as, journal.PhaseSubmitting, protocol.OpAcceptancesSubmit, verdict)
	}
	artifact, err := r.cfg.Workspaces.Collect(runCtx, as)
	if err != nil {
		return stopped(failed(reasonFor(err, "collect_failed"), err))
	}
	return r.finish(ctx, as, journal.PhaseSubmitting, protocol.OpRunsSubmitArtifact, artifact)
}

// waitUntilWanted holds a run whose task is paused until tend wants it to
// run.
func (r *Runner) waitUntilWanted(ctx context.Context, a *active) error {
	for {
		r.mu.Lock()
		desired := a.desired
		r.mu.Unlock()
		if desired != protocol.DesiredPaused {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-a.resume:
		}
	}
}

// finish journals the run's final report and moves it to phase, so the
// same report, under the same request ID, is resent after a restart.
func (r *Runner) finish(ctx context.Context, as protocol.Assignment, phase, op string, payload any) error {
	env, err := r.newEnvelope(op, runTarget(as), payload)
	if err != nil {
		return err
	}
	return r.cfg.Journal.Finish(ctx, as.RunID, phase, journal.Request{Envelope: *env, RunID: as.RunID})
}

// report sends a run's journaled final report and forgets the run.
// outcome is what a run's final report says, as log attributes.
func outcome(env protocol.Envelope) []any {
	var p struct {
		Verdict protocol.Verdict `json:"verdict"`
		Reason  string           `json:"reason"`
		Mounts  []struct {
			Path    string `json:"path"`
			Changed bool   `json:"changed"`
		} `json:"mounts"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	switch env.Operation {
	case protocol.OpAcceptancesSubmit:
		return []any{"verdict", p.Verdict}
	case protocol.OpRunsRelease:
		return []any{"reason", p.Reason}
	}
	var changed []string
	for _, m := range p.Mounts {
		if m.Changed {
			changed = append(changed, m.Path)
		}
	}
	return []any{"changed", strings.Join(changed, ",")}
}

func (r *Runner) report(ctx context.Context, as protocol.Assignment) error {
	req, ok, err := r.cfg.Journal.RequestForRun(ctx, as.RunID)
	if err != nil {
		return err
	}
	if ok {
		// Facts go out before the result that ends the run's credential.
		if err := r.retry(ctx, func() error { return r.flushEvents(ctx, as) }); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.cfg.Log.ErrorContext(ctx, "tend rejected run events", "run_id", as.RunID, "err", err)
		}
		_, err := r.send(ctx, runCredential(as), &req.Envelope)
		if err == nil {
			r.cfg.Log.InfoContext(ctx, "run reported", append([]any{"run_id", as.RunID, "operation", req.Envelope.Operation}, outcome(req.Envelope)...)...)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.cfg.Log.ErrorContext(ctx, "tend rejected the run's final report", "run_id", as.RunID, "operation", req.Envelope.Operation, "err", err)
			if req.Envelope.Operation != protocol.OpRunsRelease && protocol.CodeOf(err) != protocol.CodeForbidden {
				// The run is still live on tend's side; give it up so the
				// task can move on instead of waiting for a lapse.
				env, err2 := r.newEnvelope(protocol.OpRunsRelease, runTarget(as), protocol.ReleasePayload{Reason: "report_rejected: " + string(protocol.CodeOf(err))})
				if err2 != nil {
					return err2
				}
				if err2 := r.cfg.Journal.Replace(ctx, as.RunID, journal.PhaseReleasing, req.Envelope.RequestID, journal.Request{Envelope: *env, RunID: as.RunID}); err2 != nil {
					return err2
				}
				return r.report(ctx, as)
			}
		}
	}
	if err := r.cfg.Workspaces.Finish(ctx, as); err != nil {
		r.cfg.Log.WarnContext(ctx, "workspace cleanup failed", "run_id", as.RunID, "err", err)
	}
	return r.cfg.Journal.Done(ctx, as.RunID)
}

// uploadEvents uploads a run's events every EventFlush until ctx ends.
func (r *Runner) uploadEvents(ctx context.Context, as protocol.Assignment) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.cfg.EventFlush):
		}
		if err := r.flushEvents(ctx, as); err != nil && ctx.Err() == nil {
			r.cfg.Log.WarnContext(ctx, "run event upload failed", "run_id", as.RunID, "err", err)
		}
	}
}

// Size limits keep every upload well inside the server's request limit.
const (
	maxEventBody = 256 << 10
	maxBatchBody = 1 << 20
)

// boundedBody replaces an event body that is oversized once encoded, which
// may escape characters, with a note of its size.
func boundedBody(body json.RawMessage) json.RawMessage {
	encoded, err := json.Marshal(body)
	if err == nil && len(encoded) <= maxEventBody {
		return body
	}
	note, _ := json.Marshal(map[string]any{"truncated": true, "bytes": len(body)})
	return note
}

// flushEvents uploads every pending event of a run in batches bounded by
// count and size.
func (r *Runner) flushEvents(ctx context.Context, as protocol.Assignment) error {
	for {
		pending, err := r.cfg.Journal.PendingEvents(ctx, as.RunID, 200)
		if err != nil || len(pending) == 0 {
			return err
		}
		events, size := pending[:0:0], 0
		for _, e := range pending {
			encoded, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("runner: encode event: %w", err)
			}
			size += len(encoded)
			if len(events) > 0 && size > maxBatchBody {
				break
			}
			events = append(events, e)
		}
		env, err := r.newEnvelope(protocol.OpRunnerEvents, runTarget(as), protocol.EventsPayload{Events: events})
		if err != nil {
			return err
		}
		if _, err := r.c.do(ctx, runCredential(as), env); err != nil {
			return err
		}
		if err := r.cfg.Journal.AckEvents(ctx, as.RunID, events[len(events)-1].Seq); err != nil {
			return err
		}
	}
}
