package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/harness/remote"
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

// questionYield is emitted only after the question's durable receipt is read.
type questionYield struct{ decision string }

func (e *questionYield) Error() string { return "yield execution for question " + e.decision }

// completionPending keeps a successful result while external stop proof is unknown.
type completionPending struct{ err error }

func (e *completionPending) Error() string {
	return "completed result awaits reconciliation: " + e.err.Error()
}
func (e *completionPending) Unwrap() error { return e.err }
func isCompletionPending(err error) bool   { _, ok := errors.AsType[*completionPending](err); return ok }

func isQuestionYield(err error) bool { _, ok := errors.AsType[*questionYield](err); return ok }

func failed(reason string, err error) error { return &stepError{reason: reason, err: err} }

// execute drives a journaled run from its current phase to done. ctx is the
// runner's lifetime; runCtx ends when the run is fenced or stopped.
func (r *Runner) execute(ctx, runCtx context.Context, runID string, a *active) error {
	if errors.Is(context.Cause(runCtx), errFenced) {
		return r.markExecutorFenced(ctx, a)
	}
	if a.as.Role == protocol.RoleIssueManager {
		return r.executeManager(ctx, runCtx, a)
	}
	for {
		if errors.Is(context.Cause(runCtx), errFenced) {
			return r.markExecutorFenced(ctx, a)
		}
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
			r.flushFinalEvents(ctx, as)
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
				reportErr := r.report(ctx, as)
				if isCompletionPending(reportErr) {
					strict, err := r.cfg.Journal.RetainedResume(ctx, as.RunID)
					if err != nil {
						return &completionPending{errors.Join(reportErr, err)}
					}
					if strict != nil {
						// Hello can record Stop while this executor is present.
						// Preserve retry spacing without blocking that control.
						select {
						case <-ctx.Done():
						case <-runCtx.Done():
						case <-time.After(r.cfg.RetryDelay):
						}
					}
				}
				return reportErr
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
			return r.markExecutorFenced(ctx, a)
		case errors.Is(cause, errStopped) && run.Phase == journal.PhaseWorkerDone:
			// A stop arriving during completion takes precedence over a
			// pending stop check. Final phases retain their existing request.
			err = r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped by tend"})
		case isQuestionYield(err):
			question, _ := errors.AsType[*questionYield](err)
			if err := r.stopAgent(ctx, as); err != nil {
				return &completionPending{err}
			}
			saved, readErr := r.cfg.Journal.Run(ctx, as.RunID)
			if readErr != nil {
				return readErr
			}
			err = r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: question.decision, ThreadID: saved.Thread})
		case errors.Is(err, errPaused), errors.Is(err, errHeld):
			// The worker was stopped for a pause or a takeover; the next
			// attempt waits until tend wants the run again.
			continue
		case isOwnedRecoveryUncertain(err):
			r.cfg.Log.WarnContext(ctx, "original app-server recovery remains unconfirmed; run retained for explicit reconciliation or bounded isolation", "run_id", runID, "err", err)
			if fenceErr := r.markExecutorFenced(ctx, a); fenceErr != nil {
				return errors.Join(err, fenceErr)
			}
			return err
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
			payload := protocol.ReleasePayload{Reason: reason}
			if diagnosis, ok := errors.AsType[*PreparationError](err); ok && as.PreparationDiagnostics && run.Phase == journal.PhaseClaimed && run.Attempts == 0 && run.Thread == "" {
				payload.Preparation = &diagnosis.Failure
			}
			err = r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, payload)
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
func (r *Runner) work(ctx, runCtx context.Context, as protocol.Assignment, a *active) (workErr error) {
	if err := r.settleTakeover(runCtx, as, a); err != nil {
		return err
	}
	if err := r.waitUntilWanted(runCtx, as, a); err != nil {
		return err
	}
	// Each attempt can be stopped on its own, so a pause interrupts the
	// worker without giving up the run.
	nativeLifetime := runCtx
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
	if recovered, recoveryErr := r.reconcileAcceptanceVerdict(runCtx, as); recovered {
		if recoveryErr != nil {
			return &completionPending{recoveryErr}
		}
		return nil
	}
	if recovered, recoveryErr := r.reconcileImplementationResult(runCtx, as); recovered {
		if recoveryErr != nil {
			if _, terminalFailure := errors.AsType[*stepError](recoveryErr); terminalFailure {
				return recoveryErr
			}
			return &ownedRecoveryUncertain{&completionPending{recoveryErr}}
		}
		return nil
	}
	if recovered, err := r.recoverAppServerAttempt(runCtx, as); recovered {
		if err != nil {
			return &ownedRecoveryUncertain{&completionPending{err}}
		}
		return failed("worker_interrupted_after_runner_restart", errors.New("runner: original app-server attempt was stopped; owner must reconcile its effects before choosing follow-up work"))
	} else if err != nil {
		return &completionPending{err}
	}
	if err := r.requireRunWorker(runCtx, as.RunID); err != nil {
		return &completionPending{err}
	}
	execution, err := r.resolveExecution(runCtx, as)
	if err != nil {
		return &completionPending{err}
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	strictResume, err := r.cfg.Journal.RetainedResume(ctx, as.RunID)
	if err != nil {
		return &completionPending{err}
	}
	defer func() {
		// Setup, observation and local persistence failures do not prove the
		// original worker stopped. Preserve it instead of fabricating a new
		// business failure and entering the ordinary terminal finalizer.
		if strictResume != nil && workErr != nil && context.Cause(runCtx) == nil && !isCompletionPending(workErr) && !isQuestionYield(workErr) && !errors.Is(workErr, errPaused) && !errors.Is(workErr, errHeld) {
			workErr = &completionPending{workErr}
		}
	}()
	var p Prepared
	if strictResume != nil {
		if run.Held || run.Thread == "" {
			return &completionPending{harness.ErrRetainedResumePending}
		}
		if _, supported := execution.Workers.(interface{ StrictRetainedResume() }); !supported {
			return &completionPending{harness.ErrRetainedResumePending}
		}
		workspace, supported := execution.Workspaces.(interface {
			Reattach(context.Context, protocol.Assignment) (Prepared, error)
		})
		if !supported {
			return &completionPending{harness.ErrRetainedResumePending}
		}
		p, err = workspace.Reattach(runCtx, as)
		if err != nil {
			return &completionPending{fmt.Errorf("%w: %v", harness.ErrRetainedResumePending, err)}
		}
	} else {
		p, err = execution.Workspaces.Prepare(runCtx, as)
		if err != nil {
			return stopped(failed(reasonFor(err, "workspace_prepare_failed"), err))
		}
	}
	var askOwner func(context.Context, protocol.AskOwnerPayload) (protocol.WorkerQuestion, error)
	var questions []protocol.WorkerQuestion
	var receiveMessages func(context.Context) (*protocol.RunMessage, error)
	var ackMessage func(context.Context, string) error
	_, nativeMessages := execution.Workers.(interface{ NativeLiveMessages() })
	toolLifetime := runCtx
	if nativeMessages {
		toolLifetime = nativeLifetime
	}
	if as.RunMessages && assignmentWorker(as, r.cfg.Worker) == protocol.WorkerOrca && !nativeMessages {
		messages := &workerMessages{r: r, as: as, receiver: r.cfg.IDs.New(protocol.PrefixRequest)}
		withRun := func(call context.Context) (context.Context, func()) {
			callCtx, cancel := context.WithCancel(call)
			stop := context.AfterFunc(toolLifetime, cancel)
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
		if as.QuestionHandoffs && len(questions) > 0 {
			// A prior create may have committed before its receipt or local stop.
			// Never restart the source model merely to recover that same question.
			return &questionYield{decision: questions[len(questions)-1].DecisionID}
		}
		questions = append(append([]protocol.WorkerQuestion{}, as.Questions...), questions...)
		askOwner = func(ctx context.Context, payload protocol.AskOwnerPayload) (protocol.WorkerQuestion, error) {
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			stop := context.AfterFunc(toolLifetime, cancel)
			defer stop()
			question, err := owner.ask(callCtx, payload)
			if err == nil && as.QuestionHandoffs {
				cause := &questionYield{decision: question.DecisionID}
				stopAttempt(cause)
				return question, cause
			}
			return question, err
		}
	}
	if as.Responsibility != nil {
		p.Guide += fmt.Sprintf("\nYou are executing work for Issue %s at its publishing owner's assignment epoch %d, role %s. Your execution identity remains the selected Agent. Fulfill the frozen task agreement and submit its result through the existing role-specific tools. Choose short investigation or collaboration within the existing goal using this device's configured tools. An ordinary same-Issue collaboration need not create a child Issue; independent continuing responsibility may use one. Preserve independent acceptance isolation and submit actual results with their original execution identity.\n", as.Responsibility.IssueID, as.Responsibility.AssignmentEpoch, as.Role)
	}
	persistEvent := func(eventCtx context.Context, ev harness.WorkerEvent) error {

		if err := r.recordRolloutEvent(eventCtx, as.RunID, ev); err != nil {
			return err
		}
		if err := r.cfg.Journal.AppendEvent(eventCtx, as.RunID, protocol.RunEvent{NativeID: ev.NativeID, Kind: ev.Kind, Body: boundedBody(ev.Body)}); err != nil {
			return err
		}
		flushCtx, cancel := context.WithTimeout(eventCtx, stopFlush)
		defer cancel()
		// The durable outbox survives transport failure. Persisted facts must
		// not cause startup or owner input to be replayed.
		_ = r.flushEvents(flushCtx, as)
		return nil
	}
	startWorker, err := r.capabilityWorkerStart(runCtx, as, execution)
	if err != nil {
		return err
	}
	// Reset stop proof only when another native attempt is actually about to
	// start. Recovering a durable question must preserve the predecessor exit.
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
	var startupObserved time.Time
	if nativeMessages {
		startupObserved = r.cfg.Now()
	}
	if run.Thread == "" && as.QuestionContinuation != nil && as.Role == protocol.RoleImplement && (assignmentWorker(as, r.cfg.Worker) == protocol.WorkerAppServer || nativeMessages) {
		run.Thread = as.QuestionContinuation.ThreadID
		if err := r.cfg.Journal.SetThread(ctx, as.RunID, run.Thread); err != nil {
			return err
		}
	}

	predecessorState := ""
	if as.QuestionContinuation != nil {
		if locator, ok := execution.Workspaces.(interface{ RunStateDirectory(string) (string, error) }); ok {
			predecessorState, err = locator.RunStateDirectory(as.QuestionContinuation.RunID)
			if err != nil {
				return err
			}
		}
	}

	evidence := &acceptanceEvidence{r: r, as: as, prepared: p}
	w, err := startWorker(runCtx, harness.WorkerSpec{PredecessorStateDir: predecessorState, RetainedResume: strictResume, ConcurrencyFeedback: as.ConcurrencyFeedback, AcceptanceEvidenceFiles: as.AcceptanceEvidenceFiles, ValidateVerdictFiles: evidence.validate, SealVerdictFiles: evidence.seal, VerdictFilesIdentity: evidence.identity, LegacyRawUsage: !r.supportsNativeUsage(),
		Lifetime: nativeLifetime, NativeDial: func(ctx context.Context, channel string) (io.ReadWriteCloser, error) {
			return remote.Dial(ctx, r.cfg.HTTPClient, r.cfg.ServerURL, as.RunID, as.RunToken, channel)
		},
		FrozenExecution: execution.Profile, RoleInstructions: execution.Instructions,
		OnUsageSource: func(sourceCtx context.Context, source nativeusage.Source) error {
			return r.registerUsage(sourceCtx, as, source)
		},
		Execution: as.AcceptanceExecution, QuestionContext: as.QuestionContext, QuestionHandoffs: as.QuestionHandoffs, QuestionContinuation: as.QuestionContinuation, SupervisorQuestions: as.SupervisorQuestions, RunID: as.RunID, Role: as.Role, Dir: p.Dir, ProviderID: p.ProviderID, StateDir: p.StateDir, Baseline: p.Baseline,
		AskOwner: askOwner, Questions: questions,
		ReceiveMessages: receiveMessages, AckMessage: ackMessage,
		ReadObservation: r.observationReader(toolLifetime, as),
		ReadContext:     r.contextReader(toolLifetime, as),
		UploadFile: func(ctx context.Context, path, name string) (protocol.ArtifactFile, error) {
			return r.uploadFile(ctx, as, p, path, name)
		},
		DownloadFile: func(ctx context.Context, id string) (string, error) { return r.downloadFile(ctx, as, p, id) }, Guide: p.Guide, ScratchDir: p.ScratchDir, Spec: as.Spec, Artifact: as.Artifact, Relations: as.Relations,
		Feedback: as.Feedback, Attempt: attempt, Thread: run.Thread,
		OnNativeSource: func(sourceCtx context.Context, source protocol.RolloutPreviewSource) error {
			return r.registerRollout(sourceCtx, as.RunID, source)
		},
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
		OnStartupEvent: persistEvent, OnRetainedEvent: persistEvent,
		OnTakeover: func(reason string) error { return r.reportTakeover(nativeLifetime, as, a, reason) },
		HandBack:   run.Held,
	})
	if err != nil {
		if errors.Is(err, harness.ErrRetainedResumePending) {
			return &completionPending{err}
		}
		if nativeMessages {
			switch {
			case errors.Is(err, harness.ErrHeld):
				r.applyDesired(ctx, a, protocol.DesiredHeld, startupObserved)
				return stopped(errHeld)
			case errors.Is(err, harness.ErrPaused):
				r.applyDesired(ctx, a, protocol.DesiredPaused, startupObserved)
				return stopped(errPaused)
			}
		}
		if strictResume != nil && context.Cause(runCtx) == nil {
			return &completionPending{err}
		}
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
		if p.StateDir != "" && as.Role == protocol.RoleImplement && assignmentWorker(as, r.cfg.Worker) == protocol.WorkerAppServer {
			outcome, outcomeErr := nativeWorkerOutcome(as.RunID, run.Thread, attempt, ev)
			if outcomeErr == nil && outcome != nil {
				_, outcomeErr = saveWorkerOutcome(context.WithoutCancel(ctx), p.StateDir, *outcome)
			}
			if outcomeErr != nil {
				appendErr = errors.Join(appendErr, outcomeErr)
			}
		}
		if appendErr == nil {
			ev, appendErr = concurrencyWorkerEvent(ev, r.cfg.Now().UnixMilli(), attempt)
		}

		if appendErr == nil {
			appendErr = r.recordRolloutEvent(ctx, as.RunID, ev)
			if appendErr == nil {
				appendErr = r.cfg.Journal.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: ev.NativeID, Kind: ev.Kind, Body: boundedBody(ev.Body)})
			}
		}
	}
	res, werr := w.Wait()
	stopMessages()
	<-messagesDone
	stopUploader()
	<-uploaded
	r.flushFinalEvents(ctx, as)
	if p.StateDir != "" && as.Role == protocol.RoleImplement && assignmentWorker(as, r.cfg.Worker) == protocol.WorkerAppServer && appendErr == nil {
		outcome := workerOutcome{Version: 1, RunID: as.RunID, ThreadID: run.Thread, Attempt: attempt, Status: "completed", Source: "worker_wait"}
		if cause := context.Cause(runCtx); cause != nil {
			outcome.Status, outcome.Error = "unknown", cause.Error()
			if errors.Is(cause, errPaused) || errors.Is(cause, errHeld) || errors.Is(cause, errStopped) || isQuestionYield(cause) {
				outcome.Status, outcome.Source = "interrupted", "run_control"
			}
		} else if werr != nil {
			outcome.Status, outcome.Error = "failed", werr.Error()
			if errors.Is(werr, harness.ErrRetainedResumePending) {
				outcome.Status = "unknown"
			}
		}
		observed, err := saveWorkerOutcome(context.WithoutCancel(ctx), p.StateDir, outcome)
		if err != nil {
			return &completionPending{err}
		}
		if observed.Status == "failed" || observed.Status == "interrupted" {
			werr = errors.Join(fmt.Errorf("the original native turn ended %s", observed.Status), werr)
		} else if observed.Status != "completed" && observed.Source != "run_control" && context.Cause(runCtx) == nil {
			return &completionPending{fmt.Errorf("%w: original native turn ended %s", harness.ErrRetainedResumePending, observed.Status)}
		}
	}
	if errors.Is(werr, harness.ErrRetainedResumePending) {
		return &completionPending{werr}
	}
	if strictResume != nil && context.Cause(runCtx) == nil {
		if appendErr != nil {
			return &completionPending{appendErr}
		}
		if werr != nil {
			return &completionPending{werr}
		}
	}
	if context.Cause(runCtx) == nil && appendErr == nil && werr == nil {
		body, err := json.Marshal(res)
		if err != nil {
			return err
		}
		// A failed close must not discard an already observed successful result.
		// Completion still verifies physical exit and current control below.
		if err := r.cfg.Journal.WorkerDone(ctx, as.RunID, body); err != nil {
			return &completionPending{err}
		}
	}
	if err := r.recordClosedSession(context.WithoutCancel(ctx), as); err != nil {
		return &completionPending{err}
	}
	if cause := context.Cause(runCtx); cause != nil {
		// errFenced or errStopped from the run, or errPaused or errHeld
		// from this attempt. What the worker said as it stopped, such as
		// that the owner took over, would otherwise wait for the run's
		// next attempt or its report.
		return cause
	}
	if appendErr != nil {
		return appendErr
	}
	if werr != nil {
		return failed("worker_failed", werr)
	}
	return nil
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
	strictResume, err := r.cfg.Journal.RetainedResume(ctx, as.RunID)
	if err != nil {
		return &completionPending{err}
	}
	// Dedicated receipts retain their separate post-stop credential. Drain
	// these facts before ending local ownership, without repeating invocation.
	if err := r.recoverRunMessages(ctx, as); err != nil {
		return err
	}
	if strictResume != nil {
		stop, err := r.cfg.Journal.RetainedStop(ctx, as.RunID)
		if err != nil {
			return &completionPending{err}
		}
		if stop != nil {
			run, err := r.cfg.Journal.Run(ctx, as.RunID)
			if err != nil || !run.StoppedConfirmed {
				return &completionPending{errors.New("runner: authoritative Stop still awaits physical stop proof")}
			}
			// The host's Stop independently ends local responsibility. Run
			// credentials may already be revoked; preserve outstanding facts
			// as history instead of interpreting Forbidden as business success.
			return r.cleanupReportedRunWithStop(ctx, as, stop)
		}
	}
	req, ok, err := r.cfg.Journal.RequestForRun(ctx, as.RunID)
	if err != nil {
		return err
	}
	if strictResume != nil && !ok {
		return &completionPending{errors.New("runner: strict final report is unavailable")}
	}
	// execute has confirmed the process stopped before entering reporting.
	if ok {
		// Facts go out before the result that ends the run's credential.
		flush := func() error { return r.flushEvents(ctx, as) }
		var eventErr error
		if strictResume != nil {
			// A durable Stop may arrive while the credential still works.
			// Yield uncertain uploads to Hello instead of retrying forever
			// inside a report that cannot reread that independent control.
			eventErr = flush()
		} else {
			eventErr = r.retry(ctx, flush)
		}
		if err := eventErr; err != nil {
			if strictResume != nil {
				r.requestReconcile()
				return &completionPending{err}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.cfg.Log.ErrorContext(ctx, "tend rejected run events", "run_id", as.RunID, "err", err)
		}
		send := r.send
		if strictResume != nil {
			send = r.sendOnce
		}
		op, err := send(ctx, runCredential(as), &req.Envelope)
		if strictResume != nil {
			if err != nil {
				r.requestReconcile()
				return &completionPending{err}
			}
			if op.Status != protocol.OpSucceeded {
				// Accepted acknowledges the original request identity, not
				// business settlement. Keep its exact request and ownership.
				r.requestReconcile()
				return &completionPending{errors.New("runner: strict final report awaits business settlement")}
			}
		}
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
				settled, err2 := r.settleRun(ctx, as.RunID, func() error {
					return r.cfg.Journal.Replace(ctx, as.RunID, journal.PhaseReleasing, req.Envelope.RequestID, journal.Request{Envelope: *env, RunID: as.RunID})
				})
				if err2 != nil || !settled {
					return err2
				}
				return r.report(ctx, as)
			}
		}
	}
	return r.cleanupReportedRun(ctx, as)
}

func (r *Runner) cleanupReportedRun(ctx context.Context, as protocol.Assignment) error {
	return r.cleanupReportedRunWithStop(ctx, as, nil)
}

func (r *Runner) cleanupReportedRunWithStop(ctx context.Context, as protocol.Assignment, stop *journal.RetainedStop) error {
	_, settleErr := r.settleRun(ctx, as.RunID, func() error {
		if stop != nil {
			// Continue or a new attachment can revoke a previously read Stop.
			// Recheck only after acquiring the same settlement gate they use,
			// before performing any workspace cleanup.
			current, err := r.cfg.Journal.RetainedStop(ctx, as.RunID)
			run, readErr := r.cfg.Journal.Run(ctx, as.RunID)
			if err != nil || readErr != nil || current == nil || *current != *stop || !run.StoppedConfirmed {
				return &completionPending{errors.New("runner: original Stop no longer authorizes cleanup")}
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
		if stop != nil {
			if err := r.cfg.Journal.DoneRetainedStop(ctx, as.RunID, *stop); err != nil {
				return &completionPending{err}
			}
			return nil
		}
		return r.cfg.Journal.Done(ctx, as.RunID)
	})
	return settleErr
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
	if supported, err := r.stopOwnedAppServer(ctx, as, run); supported {
		if err != nil {
			return err
		}
		return r.cfg.Journal.ConfirmStopped(ctx, as.RunID)
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
			run, err := r.cfg.Journal.Run(ctx, as.RunID)
			if err != nil {
				return err
			}
			if supported, err := r.stopOwnedAppServer(ctx, as, run); supported && err != nil {
				return err
			}
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

// flushFinalEvents sends already journaled facts independently of physical
// closure. A shutdown may cancel execution, but cannot erase received facts;
// failed uploads retain the original event identities for the next recovery.
func (r *Runner) flushFinalEvents(ctx context.Context, as protocol.Assignment) {
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopFlush)
	defer cancel()
	if err := r.flushEvents(flushCtx, as); err != nil {
		r.cfg.Log.WarnContext(flushCtx, "final run events remain pending", "run_id", as.RunID, "err", err)
	}
}

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
	req, final, err := r.cfg.Journal.RequestForRun(ctx, as.RunID)
	if err != nil {
		return &completionPending{err}
	}
	yielding := final && req.Envelope.Operation == protocol.OpRunsYieldQuestion
	if !yielding && !r.stopRequested(runCtx, a) {
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

func concurrencyWorkerEvent(ev harness.WorkerEvent, now int64, attempt int) (harness.WorkerEvent, error) {
	if ev.Kind != "worker.concurrency" {
		return ev, nil
	}
	var f protocol.ConcurrencyFeedback
	if err := json.Unmarshal(ev.Body, &f); err != nil {
		return ev, err
	}
	if f.OccurredAt == 0 {
		f.OccurredAt = now
	}
	if f.Attempt == "" {
		f.Attempt = fmt.Sprint(attempt)
	}
	raw, err := json.Marshal(f)
	ev.Body = raw
	return ev, err
}
