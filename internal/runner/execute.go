package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
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

// completionPending keeps a successful result while external stop proof is unknown.
type completionPending struct{ err error }

func (e *completionPending) Error() string {
	return "completed result awaits reconciliation: " + e.err.Error()
}
func (e *completionPending) Unwrap() error { return e.err }
func isCompletionPending(err error) bool   { _, ok := errors.AsType[*completionPending](err); return ok }

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
		case journal.PhaseWorkerDone:
			if r.stopRequested(runCtx, a) {
				// The saved outcome remains available for reconciliation, but
				// cancellation must not restart work or collect a stale spec.
				err = r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped by tend"})
			} else {
				err = r.completeWork(ctx, runCtx, as, a, run.WorkerResult)
			}
		case journal.PhaseSubmitting, journal.PhaseReleasing:
			err = r.awaitCompletedStop(ctx, runCtx, as, a)
			if err == nil {
				r.mu.Lock()
				a.reporting = true
				r.mu.Unlock()
				return r.report(ctx, as)
			}
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
		case errors.Is(cause, errStopped) && run.Phase == journal.PhaseWorkerDone:
			// A stop arriving during completion takes precedence over a
			// pending stop check. Final phases retain their existing request.
			err = r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped by tend"})
		case errors.Is(err, errPaused), errors.Is(err, errHeld):
			// The worker was stopped for a pause or a takeover; the next
			// attempt waits until tend wants the run again.
			continue
		case isCompletionPending(err):
			retryCtx := runCtx
			if errors.Is(cause, errStopped) {
				retryCtx = ctx
			}
			r.cfg.Log.WarnContext(ctx, "completed result retained for reconciliation", "run_id", runID, "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-retryCtx.Done():
				return nil
			case <-time.After(r.cfg.RetryDelay):
				continue
			}
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
	execution, err := r.resolveExecution(ctx, as)
	if err != nil {
		return &completionPending{err}
	}
	if err := r.requireRunWorker(ctx, as.RunID); err != nil {
		return &completionPending{err}
	}
	p, err := execution.Workspaces.Prepare(ctx, as)
	if err != nil {
		return failed(reasonFor(err, "workspace_prepare_failed"), err)
	}
	cred := runCredential(as)
	if as.Role == protocol.RoleImplement {
		report := p.Report
		if !as.MountDirectories {
			// Older servers decode reports strictly. Keep the local facts,
			// but omit the extension unless this assignment negotiated it.
			report.Mounts = slices.Clone(report.Mounts)
			for i := range report.Mounts {
				report.Mounts[i].Directory = ""
			}
		}
		env, err := r.newEnvelope(protocol.OpRunsReportWorkspace, runTarget(as), report)
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
	if err := r.settleTakeover(runCtx, as, a); err != nil {
		return err
	}
	if err := r.waitUntilWanted(runCtx, as, a); err != nil {
		return err
	}
	// Each attempt can be stopped on its own, so a pause interrupts the
	// worker without giving up the run.
	attemptCtx, stopAttempt := context.WithCancelCause(runCtx)
	defer stopAttempt(nil)
	r.mu.Lock()
	a.stopAttempt = stopAttempt
	desired := a.desired
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		a.stopAttempt = nil
		r.mu.Unlock()
	}()
	switch desired {
	case protocol.DesiredPaused:
		return errPaused
	case protocol.DesiredHeld:
		return errHeld
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
	if err := r.requireRunWorker(runCtx, as.RunID); err != nil {
		return &completionPending{err}
	}
	execution, err := r.resolveExecution(runCtx, as)
	if err != nil {
		return &completionPending{err}
	}
	p, err := execution.Workspaces.Prepare(runCtx, as)
	if err != nil {
		return stopped(failed(reasonFor(err, "workspace_prepare_failed"), err))
	}
	attempt, err := r.cfg.Journal.NextAttempt(ctx, as.RunID)
	if err != nil {
		return err
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if attempt > 1 {
		// The run resumes after a fence or a restart.
		if err := r.cfg.Journal.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: protocol.EventRunRunning}); err != nil {
			return err
		}
	}
	var askOwner func(context.Context, protocol.AskOwnerPayload) (protocol.WorkerQuestion, error)
	var questions []protocol.WorkerQuestion
	var receiveMessages func(context.Context) (*protocol.RunMessage, error)
	var ackMessage func(context.Context, string) error
	if as.RunMessages && assignmentWorker(as, r.cfg.Worker) == protocol.WorkerOrca {
		messages := &workerMessages{r: r, as: as, receiver: r.cfg.IDs.New(protocol.PrefixRequest)}
		withRun := func(call context.Context) (context.Context, func()) {
			callCtx, cancel := context.WithCancel(call)
			stop := context.AfterFunc(runCtx, cancel)
			return callCtx, func() { stop(); cancel() }
		}
		receiveMessages = func(ctx context.Context) (*protocol.RunMessage, error) {
			callCtx, done := withRun(ctx)
			defer done()
			return messages.receive(callCtx)
		}
		ackMessage = func(ctx context.Context, id string) error {
			callCtx, done := withRun(ctx)
			defer done()
			return messages.ack(callCtx, id)
		}
	}
	if as.WorkerQuestions {
		owner := &ownerQuestions{r: r, as: as, prepared: p}
		questions, err = owner.recover(runCtx)
		if err != nil {
			return stopped(failed("owner_question_recovery_failed", err))
		}
		askOwner = func(ctx context.Context, payload protocol.AskOwnerPayload) (protocol.WorkerQuestion, error) {
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(runCtx, cancel)
			defer stop()
			return owner.ask(callCtx, payload)
		}
	}
	w, err := execution.Workers.Start(runCtx, harness.WorkerSpec{
		FrozenExecution: execution.Profile, RoleInstructions: execution.Instructions,
		Execution: as.AcceptanceExecution, QuestionContext: as.QuestionContext, RunID: as.RunID, Role: as.Role, Dir: p.Dir, ProviderID: p.ProviderID, StateDir: p.StateDir, Baseline: p.Baseline,
		AskOwner: askOwner, Questions: questions,
		ReceiveMessages: receiveMessages, AckMessage: ackMessage,
		ReadObservation: r.observationReader(runCtx, as),
		UploadFile: func(ctx context.Context, path, name string) (protocol.ArtifactFile, error) {
			return r.uploadFile(ctx, as, p, path, name)
		},
		DownloadFile: func(ctx context.Context, id string) (string, error) { return r.downloadFile(ctx, as, p, id) }, Guide: p.Guide, ScratchDir: p.ScratchDir, Spec: as.Spec, Artifact: as.Artifact, Relations: as.Relations,
		Feedback: as.Feedback, Attempt: attempt, Thread: run.Thread,
		OnThread: func(thread string) error {
			if thread == run.Thread {
				return nil
			}
			// Recorded before the thread's first turn, and even while
			// shutting down, so the next attempt continues it (D61). The
			// harness says when it could not continue the old one.
			run.Thread = thread
			return r.cfg.Journal.SetThread(context.WithoutCancel(ctx), as.RunID, thread)
		},
		OnStartupEvent: func(eventCtx context.Context, ev harness.WorkerEvent) error {
			if err := r.cfg.Journal.AppendEvent(eventCtx, as.RunID, protocol.RunEvent{NativeID: ev.NativeID, Kind: ev.Kind, Body: boundedBody(ev.Body)}); err != nil {
				return err
			}
			flushCtx, cancel := context.WithTimeout(eventCtx, stopFlush)
			defer cancel()
			// The durable outbox survives transport failure. Startup must not be replayed
			// merely because a diagnostic upload failed.
			_ = r.flushEvents(flushCtx, as)
			return nil
		},
		OnTakeover: func(reason string) error { return r.reportTakeover(runCtx, as, a, reason) },
		HandBack:   run.Held,
	})
	if err != nil {
		if native, ok := errors.AsType[*orca.Error](err); ok && as.Execution != nil && (native.Code == "invalid_argument" || native.Code == "capability_unsupported" || native.Code == "incompatible_runtime") {
			if rejected, ok := r.cfg.Executions.(interface {
				Reject(protocol.RunExecutionPin, string)
			}); ok {
				rejected.Reject(*as.Execution, "adapter_rejected_configuration")
			}
		}
		proofCtx := context.WithoutCancel(ctx)
		var proofErr error
		if errors.Is(err, harness.ErrWorkerNotStarted) && attempt == 1 && run.Thread == "" {
			// This proof comes from the harness before any external launch,
			// never from an empty thread or a failed request alone.
			proofErr = r.cfg.Journal.ConfirmStopped(proofCtx, as.RunID)
		} else {
			proofErr = r.recordClosedSession(proofCtx, as)
		}
		if proofErr != nil {
			return &completionPending{proofErr}
		}
		return stopped(failed("worker_start_failed", err))
	}
	if run.Held {
		// The worker has told the agent it is tend's again.
		if err := r.cfg.Journal.SetHeld(ctx, as.RunID, false); err != nil {
			return err
		}
	}
	uploaded := make(chan struct{})
	uploaderCtx, stopUploader := context.WithCancel(ctx)
	go func() {
		defer close(uploaded)
		r.uploadEvents(uploaderCtx, as)
	}()
	var appendErr error
	messageCtx, stopMessages := context.WithCancel(runCtx)
	messagesDone := make(chan struct{})
	go func() {
		defer close(messagesDone)
		if as.RunMessages {
			if receiveMessages == nil {
				r.runMessageLoop(messageCtx, as, w)
			}
		}
	}()
	for ev := range w.Events() {
		if appendErr == nil {
			appendErr = r.cfg.Journal.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: ev.NativeID, Kind: ev.Kind, Body: boundedBody(ev.Body)})
		}
	}
	res, werr := w.Wait()
	stopMessages()
	<-messagesDone
	stopUploader()
	<-uploaded
	if err := r.recordClosedSession(context.WithoutCancel(ctx), as); err != nil {
		return &completionPending{err}
	}
	if cause := context.Cause(runCtx); cause != nil {
		// errFenced or errStopped from the run, or errPaused or errHeld
		// from this attempt. What the worker said as it stopped, such as
		// that the owner took over, would otherwise wait for the run's
		// next attempt or its report.
		if ctx.Err() == nil && appendErr == nil {
			flushCtx, cancel := context.WithTimeout(ctx, stopFlush)
			_ = r.flushEvents(flushCtx, as)
			cancel()
		}
		return cause
	}
	if appendErr != nil {
		return appendErr
	}
	if werr != nil {
		return failed("worker_failed", werr)
	}
	body, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return r.cfg.Journal.WorkerDone(ctx, as.RunID, body)
}

// completeWork resumes a known outcome after the owner returns control.
func (r *Runner) completeWork(ctx, runCtx context.Context, as protocol.Assignment, a *active, body json.RawMessage) error {
	execution, resolveErr := r.resolveExecution(ctx, as)
	if resolveErr != nil {
		return &completionPending{resolveErr}
	}
	if err := r.awaitCompletedStop(ctx, runCtx, as, a); err != nil {
		return err
	}
	var res harness.WorkerResult
	if err := json.Unmarshal(body, &res); err != nil {
		return failed("invalid_worker_result", err)
	}
	stopped := func(err error) error {
		if cause := context.Cause(runCtx); cause != nil {
			return cause
		}
		return err
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
	artifact := protocol.ArtifactPayload{}
	if res.Result != nil {
		artifact = *res.Result
	}
	if requiresDelivery(as, "commit") {
		git, err := execution.Workspaces.Collect(runCtx, as)
		if err != nil {
			return stopped(failed(reasonFor(err, "collect_failed"), err))
		}
		artifact.Mounts = git.Mounts
	} else if len(as.Workspace.Mounts) > 0 {
		if snapshots, ok := execution.Workspaces.(interface {
			Snapshot(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error)
		}); ok {
			git, err := snapshots.Snapshot(runCtx, as)
			if err != nil {
				return stopped(failed(reasonFor(err, "collect_failed"), err))
			}
			artifact.Mounts = git.Mounts
		}
	}
	if requiresDelivery(as, "report") && (artifact.Report == nil || strings.TrimSpace(artifact.Report.Body) == "") {
		return failed("missing_report", errors.New("worker did not submit the required report"))
	}
	if requiresDelivery(as, "files") && len(artifact.Files) == 0 {
		return failed("missing_files", errors.New("worker did not submit required files"))
	}
	return r.finish(ctx, as, journal.PhaseSubmitting, protocol.OpRunsSubmitArtifact, artifact)
}

// waitUntilWanted holds a run whose task is paused or under human control
// until tend wants it to run. A takeover seen meanwhile is journaled, so
// the next worker hands the agent back even after a restart.
func (r *Runner) waitUntilWanted(ctx context.Context, as protocol.Assignment, a *active) error {
	for {
		r.mu.Lock()
		desired := a.desired
		r.mu.Unlock()
		if desired == protocol.DesiredHeld {
			if err := r.cfg.Journal.SetHeld(ctx, as.RunID, true); err != nil {
				return err
			}
		}
		if desired != protocol.DesiredPaused && desired != protocol.DesiredHeld {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-a.resume:
		}
	}
}

// reportTakeover tells tend the owner took over the run's agent (D67). The
// request is journaled first: one tend may have recorded is resent under
// its ID by the next attempt, never noticed and reported afresh, which
// could undo a hand-back that came meanwhile.
func (r *Runner) reportTakeover(ctx context.Context, as protocol.Assignment, a *active, reason string) error {
	r.cfg.Log.InfoContext(ctx, "the owner took over the run's agent", "run_id", as.RunID, "reason", reason)
	env, err := r.newEnvelope(protocol.OpRunsReportTakeover, runTarget(as), protocol.TakeoverPayload{Reason: reason})
	if err != nil {
		return err
	}
	if err := r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: *env, RunID: as.RunID}); err != nil {
		return err
	}
	return r.sendTakeover(ctx, as, a, env)
}

// settleTakeover resends a takeover report an earlier attempt or process
// journaled but did not see answered.
func (r *Runner) settleTakeover(ctx context.Context, as protocol.Assignment, a *active) error {
	reqs, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	for _, req := range reqs {
		if req.RunID == as.RunID && req.Envelope.Operation == protocol.OpRunsReportTakeover {
			env := req.Envelope
			if err := r.sendTakeover(ctx, as, a, &env); err != nil {
				return err
			}
		}
	}
	return nil
}

// sendTakeover sends a journaled takeover report. Once tend has it, the
// run is held here without waiting for a heartbeat to say so, which may
// never come if the owner hands it back first; heartbeats sent before
// then cannot undo it.
func (r *Runner) sendTakeover(ctx context.Context, as protocol.Assignment, a *active, env *protocol.Envelope) error {
	_, err := r.send(ctx, runCredential(as), env)
	if err != nil && !isRetryable(err) {
		// tend refused it, as for a run that has ended: nothing to hold.
		r.cfg.Log.WarnContext(ctx, "tend refused the takeover report", "run_id", as.RunID, "err", err)
		return r.cfg.Journal.DeleteRequest(ctx, env.RequestID)
	}
	if err != nil {
		return err
	}
	if err := r.cfg.Journal.SetHeld(context.WithoutCancel(ctx), as.RunID, true); err != nil {
		return err
	}
	if err := r.cfg.Journal.DeleteRequest(context.WithoutCancel(ctx), env.RequestID); err != nil {
		return err
	}
	now := r.cfg.Now()
	r.mu.Lock()
	a.heldAt = now
	r.mu.Unlock()
	r.applyDesired(ctx, a, protocol.DesiredHeld, now)
	return nil
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
	// Dedicated receipts are not in RequestForRun and must be drained before
	// final reporting. Failure leaves both the receipt and final request intact.
	if err := r.recoverRunMessages(ctx, as); err != nil {
		return err
	}
	req, ok, err := r.cfg.Journal.RequestForRun(ctx, as.RunID)
	if err != nil {
		return err
	}
	// execute has confirmed the process stopped before entering reporting.
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
	execution, err := r.resolveExecution(ctx, as)
	if err != nil {
		return &completionPending{err}
	}
	if err := execution.Workspaces.Finish(ctx, as); err != nil {
		r.cfg.Log.WarnContext(ctx, "workspace cleanup failed", "run_id", as.RunID, "err", err)
	}
	if as.Workspace.ID != "" {
		if err := r.cfg.Journal.QueueWorkspaceRelease(ctx, protocol.WorkspaceRelease{WorkspaceID: as.Workspace.ID, TaskID: as.TaskID}); err != nil {
			return err
		}
	}
	return r.cfg.Journal.Done(ctx, as.RunID)
}

// stopAgent confirms that an external agent can no longer write before reporting.
func (r *Runner) stopAgent(ctx context.Context, as protocol.Assignment) error {
	execution, resolveErr := r.resolveExecution(ctx, as)
	if resolveErr != nil {
		return resolveErr
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if run.StoppedConfirmed {
		return nil
	}
	if run.Thread == "" && run.Attempts == 0 {
		return r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
	}
	if err := r.checkRunWorker(run); err != nil {
		return err
	}
	if rel, ok := execution.Workers.(harness.Releaser); ok {
		if err := rel.Release(ctx, as.RunID, run.Thread); err != nil {
			if ctx.Err() == nil {
				r.cfg.Log.WarnContext(ctx, "could not stop the agent of a released run", "run_id", as.RunID, "err", err)
			}
			return err
		}
	} else if assignmentWorker(as, r.cfg.Worker) != protocol.WorkerAppServer || len(run.WorkerResult) == 0 || run.WorkerDoneAtMillis <= 0 {
		return errors.New("runner: original process closure is unconfirmed")
	}
	return r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
}

// recordClosedSession is called only after this process observes app-server
// Start fail or Wait return. That harness closes its per-run session first;
// absence of a Releaser alone never establishes closure after a restart.
func (r *Runner) recordClosedSession(ctx context.Context, as protocol.Assignment) error {
	execution, resolveErr := r.resolveExecution(ctx, as)
	if resolveErr != nil {
		return resolveErr
	}
	if assignmentWorker(as, r.cfg.Worker) == protocol.WorkerAppServer {
		if _, external := execution.Workers.(harness.Releaser); !external {
			return r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
		}
	}
	return nil
}

// checkRunWorker never treats the current harness as evidence about a process
// started by a different harness, or one whose origin cannot be established.
func (r *Runner) checkRunWorker(run journal.Run) error {
	origin, err := recordedWorkerOrigin(run)
	if err != nil {
		return err
	}
	if origin != assignmentWorker(run.Assignment, r.cfg.Worker) {
		return errors.New("runner: run requires its original worker harness")
	}
	return nil
}

func (r *Runner) requireRunWorker(ctx context.Context, runID string) error {
	// Only a record that never attempted a launch may acquire the configured
	// harness. Restarting an old process cannot change its recorded origin.
	if err := r.cfg.Journal.BindUnstartedWorkerOrigin(ctx, runID, r.cfg.Worker); err != nil {
		return err
	}
	run, err := r.cfg.Journal.Run(ctx, runID)
	if err != nil {
		return err
	}
	if err := r.checkRunWorker(run); err != nil {
		return err
	}
	if assignmentWorker(run.Assignment, r.cfg.Worker) == protocol.WorkerAppServer && run.Attempts > 0 && !run.StoppedConfirmed {
		return errors.New("runner: original app-server session closure is unconfirmed")
	}
	return nil
}

// stopFlush bounds the upload of a run's last events when its attempt
// stops; whatever it misses goes with the next attempt or the report.
const stopFlush = 5 * time.Second

// uploadEvents uploads a run's events every EventFlush until ctx ends.
func (r *Runner) uploadEvents(ctx context.Context, as protocol.Assignment) {
	// failing is set from the first failure until an upload succeeds, so
	// an outage is logged once rather than on every attempt.
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.cfg.EventFlush):
		}
		err := r.flushEvents(ctx, as)
		switch {
		case ctx.Err() != nil:
		case err == nil:
			failing = false
		case !failing:
			failing = true
			r.cfg.Log.WarnContext(ctx, "run event upload failed", "run_id", as.RunID, "err", err)
		}
	}
}

// Size limits keep every upload well inside the server's request limit.
const (
	maxEventBody = 256 << 10
	maxBatchBody = 1 << 20
)

// boundedBody replaces oversized content while retaining small lifecycle
// identities and statuses. A clipped result must still settle its start.
func boundedBody(body json.RawMessage) json.RawMessage {
	encoded, err := json.Marshal(body)
	if err == nil && len(encoded) <= maxEventBody {
		return body
	}
	note, _ := json.Marshal(map[string]any{"truncated": true, "bytes": len(body)})
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) == nil && envelope != nil {
		kept := boundedEventMetadata(envelope)
		kept["truncated"] = json.RawMessage(`true`)
		kept["bytes"], _ = json.Marshal(len(body))
		var data map[string]json.RawMessage
		if json.Unmarshal(envelope["data"], &data) == nil && data != nil {
			inner := boundedEventMetadata(data)
			inner["truncated"] = json.RawMessage(`true`)
			kept["data"], _ = json.Marshal(inner)
		}
		if raw, err := json.Marshal(kept); err == nil && len(raw) <= maxEventBody {
			return raw
		}
	}
	return note
}

func boundedEventMetadata(body map[string]json.RawMessage) map[string]json.RawMessage {
	kept := make(map[string]json.RawMessage)
	for _, key := range []string{"thread_id", "turn_id", "item_id", "id", "kind", "type", "status", "exitCode", "success"} {
		if raw, ok := body[key]; ok {
			kept[key] = raw
		}
	}
	return kept
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

func (r *Runner) stopRequested(ctx context.Context, a *active) bool {
	if errors.Is(context.Cause(ctx), errStopped) {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return a.desired == protocol.DesiredStopped
}

// awaitCompletedStop applies the same ownership and retry rules to both a saved
// worker result and a final request recovered from an earlier runner process.
func (r *Runner) awaitCompletedStop(ctx, runCtx context.Context, as protocol.Assignment, a *active) error {
	stopCtx := runCtx
	if errors.Is(context.Cause(runCtx), errStopped) {
		// A stop request must not cancel its own final process verification.
		// Keep the original context below to distinguish it from a handback.
		stopCtx = ctx
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return &completionPending{err}
	}
	if err := r.settleTakeover(stopCtx, as, a); err != nil {
		return &completionPending{err}
	}
	if !r.stopRequested(runCtx, a) {
		if err := r.waitUntilWanted(stopCtx, as, a); err != nil {
			return err
		}
	}
	// NextAttempt invalidates this proof before launching another worker.
	// Control still gates collection and finalization even when closing the
	// original terminal is no longer necessary.
	if run.StoppedConfirmed {
		return nil
	}
	r.mu.Lock()
	handBack := a.desired == protocol.DesiredRun && !errors.Is(context.Cause(runCtx), errStopped)
	r.mu.Unlock()
	if err := r.stopCompletedAgent(stopCtx, as, handBack); err != nil {
		if errors.Is(err, harness.ErrHeld) && !r.stopRequested(runCtx, a) {
			return r.holdCompleted(ctx, as, a, err.Error())
		}
		return &completionPending{err}
	}
	return nil
}

func (r *Runner) stopCompletedAgent(ctx context.Context, as protocol.Assignment, handBack bool) error {
	execution, resolveErr := r.resolveExecution(ctx, as)
	if resolveErr != nil {
		return resolveErr
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if run.StoppedConfirmed {
		return nil
	}
	var stopErr error
	if run.Thread == "" && run.Attempts == 0 {
		// A failure before Start has no external worker to close.
		return r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
	}
	if err := r.checkRunWorker(run); err != nil {
		return err
	}
	verified, hasCompletedGuard := execution.Workers.(harness.CompletedReleaser)
	if run.Held && !handBack {
		// Cancellation ends tend's work; it does not hand an owner's live
		// terminal back. Only its independently confirmed absence releases it.
		historical, ok := execution.Workers.(harness.HistoricalReleaser)
		if !ok {
			return harness.ErrHeld
		}
		stopErr = historical.ReleaseHistorical(ctx, as.RunID, run.Thread, true, run.WorkerDoneAtMillis)
	} else if !hasCompletedGuard {
		return r.stopAgent(ctx, as)
	} else if finalizing, ok := execution.Workers.(harness.FinalizingReleaser); ok && len(run.WorkerResult) == 0 {
		req, found, err := r.cfg.Journal.RequestForRun(ctx, as.RunID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("runner: final request ownership timestamp is missing")
		}
		stopErr = finalizing.ReleaseFinalizing(ctx, as.RunID, run.Thread, req.CreatedAtMillis, run.Held && handBack, func(thread string) error {
			return r.cfg.Journal.SetThread(ctx, as.RunID, thread)
		})
	} else {
		stopErr = verified.ReleaseCompleted(ctx, as.RunID, run.Thread, run.WorkerDoneAtMillis, run.Held && handBack)
	}
	if stopErr != nil {
		return stopErr
	}
	if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
		return err
	}
	if run.Held {
		return r.cfg.Journal.SetHeld(ctx, as.RunID, false)
	}
	return nil
}

func (r *Runner) holdCompleted(ctx context.Context, as protocol.Assignment, a *active, reason string) error {
	env, err := r.newEnvelope(protocol.OpRunsReportTakeover, runTarget(as), protocol.TakeoverPayload{Reason: reason})
	if err != nil {
		return &completionPending{err}
	}
	if err := r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: *env, RunID: as.RunID}); err != nil {
		return &completionPending{err}
	}
	if err := r.cfg.Journal.SetHeld(ctx, as.RunID, true); err != nil {
		return &completionPending{err}
	}
	now := r.cfg.Now()
	r.mu.Lock()
	a.heldAt = now
	r.mu.Unlock()
	r.applyDesired(ctx, a, protocol.DesiredHeld, now)
	return errHeld
}
