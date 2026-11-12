package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// executeManager has its own lifecycle because it has no workspace or artifact
// report. Process exit proof and transport receipts remain durable across stop.
func (r *Runner) executeManager(lifetime, runCtx context.Context, a *active) (resultErr error) {
	defer func() {
		if resultErr != nil && lifetime.Err() == nil {
			if rejection, ok := errors.AsType[*protocol.Error](resultErr); ok && rejection.Code != protocol.CodeUnknownOutcome && !isRetryable(resultErr) {
				// A permanent rejection leaves the same durable original pending.
				// Do not immediately hello/re-enter this releasing executor with
				// identical bytes and no new information.
				timer := time.NewTimer(r.cfg.RetryDelay)
				defer timer.Stop()
				select {
				case <-lifetime.Done():
					return
				case <-timer.C:
				}
			}
			r.requestReconcile()
		}
	}()
	as := a.as
	readCtx, cancelRead := context.WithTimeout(lifetime, r.cfg.RequestTimeout)
	run, err := r.cfg.Journal.Run(readCtx, as.RunID)
	cancelRead()
	if errors.Is(err, journal.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Phase == journal.PhaseReleasing {
		if ready, e := r.externalWaitReceiptReady(lifetime, run); e != nil {
			return e
		} else if ready {
			return r.reportExternalWait(lifetime, as)
		}
		if ready, e := r.pauseReceiptReady(lifetime, run); e != nil {
			return e
		} else if ready {
			return r.reportPause(lifetime, as)
		}
		return r.reportManagerStopped(lifetime, as)
	}
	attempt, stop := context.WithCancelCause(runCtx)
	r.mu.Lock()
	a.stopAttempt = stop
	desired := a.desired
	r.mu.Unlock()
	defer func() { stop(nil); r.mu.Lock(); a.stopAttempt = nil; r.mu.Unlock() }()
	if desired == protocol.DesiredPaused {
		stop(errPaused)
	}
	if desired == protocol.DesiredHeld {
		stop(errHeld)
	}
	if desired == protocol.DesiredStopped {
		stop(errStopped)
	}
	err = r.managerNative(attempt, lifetime, as)
	if lifetime.Err() != nil {
		return nil
	}
	if err == nil {
		return nil
	}
	// Only a confirmed local process exit permits releasing its slot. Native
	// business receipts remain unknown until the coordinator reconciles them.
	receiptCtx, cancelReceipts := context.WithTimeout(lifetime, r.cfg.RequestTimeout)
	defer cancelReceipts()
	run, readErr := r.cfg.Journal.Run(receiptCtx, as.RunID)
	if errors.Is(readErr, journal.ErrNotFound) {
		return nil
	}
	if readErr != nil {
		return readErr
	}
	r.mu.Lock()
	retained := a.retained
	r.mu.Unlock()
	if retained {
		return r.markExecutorFenced(receiptCtx, a)
	}
	if !run.StoppedConfirmed {
		if err := r.markExecutorFenced(receiptCtx, a); err != nil {
			return err
		}
		r.requestReconcile()
		return nil
	}
	if v, e := r.cfg.Journal.ExternalWait(receiptCtx, as.RunID); e != nil {
		return e
	} else if v != nil {
		return r.finishExternalWait(receiptCtx, as)
	}
	if p, e := r.cfg.Journal.Pause(receiptCtx, as.RunID); e != nil {
		return e
	} else if p != nil && as.PauseHandoffs && !run.Held {
		return r.finishPause(receiptCtx, as)
	}
	last, readErr := r.cfg.Journal.LastManagerEvent(receiptCtx, as.RunID)
	if readErr != nil {
		return readErr
	}
	env, readErr := r.newEnvelope(protocol.OpManagerNativeStopped, runTarget(as), protocol.ManagerStoppedPayload{Reason: "manager native process stopped; reconcile pending operations", LastSequence: last})
	if readErr != nil {
		return readErr
	}
	if readErr = r.cfg.Journal.Finish(receiptCtx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); readErr != nil {
		return readErr
	}
	return r.reportManagerStopped(receiptCtx, as)
}

func (r *Runner) reportManagerStopped(ctx context.Context, as protocol.Assignment) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	// This independently records an actually observed process close. It grants
	// no Run/business completion and must not wait behind a rejected event.
	// Its existing optional envelope remains durable across an unknown reply.
	_ = r.reportSessionStop(ctx, as)
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	if err = r.flushManagerEvents(ctx, as); err != nil {
		return err
	}
	// Physical exit is independent of business receipt application. Report it
	// after the original events, before a close reply that may await their
	// application. Both original envelopes remain until their own ACK arrives.
	for _, op := range []string{protocol.OpManagerNativeStopped, protocol.OpManagerNativeReply} {
		for _, p := range pending {
			if p.RunID != as.RunID || p.Envelope.Operation != op {
				continue
			}
			if err = r.sendManagerReceipt(ctx, as, &p.Envelope); err != nil {
				return err
			}
			settled, err := r.settleRun(ctx, as.RunID, func() error { return r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID) })
			if err != nil || !settled {
				return err
			}
		}
	}
	return r.finishJournal(ctx, as.RunID)
}

func (r *Runner) managerNative(ctx, lifetime context.Context, as protocol.Assignment) error {
	ctx, cancelNative := context.WithCancelCause(ctx)
	defer cancelNative(nil)
	// Stop cancels new native execution, not recovery of a completed close.
	receiptCtx, cancelReceipts := context.WithTimeout(lifetime, r.cfg.RequestTimeout)
	closed, err := r.replayManagerClose(receiptCtx, as)
	var run journal.Run
	if !closed && err == nil {
		run, err = r.cfg.Journal.Run(receiptCtx, as.RunID)
	}
	cancelReceipts()
	if closed || err != nil {
		return err
	}
	opened := false
	defer func() {
		// Not opening this attempt says nothing about an earlier process.
		if !opened && run.Attempts == 0 {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(lifetime), r.cfg.RequestTimeout)
			defer cancel()
			_ = r.cfg.Journal.ConfirmStopped(stopCtx, as.RunID)
		}
	}()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	resolved, err := r.resolveManagerExecution(ctx, as)
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(as.Manager.ConversationID))
	dir := filepath.Join(r.cfg.Journal.Directory(), "manager-sessions", hex.EncodeToString(key[:]))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if _, err = r.cfg.Journal.NextAttempt(ctx, as.RunID); err != nil {
		return err
	}
	open := resolved.Open
	if as.Manager.NativeOwnerTools && !as.Manager.ReceiptOnly && !as.Manager.RequireReceiptReader {
		if resolved.OpenNative == nil {
			return errors.New("native owner executor is unavailable")
		}
		open = resolved.OpenNative
	}
	var session harness.Session
	if resolved.OpenRun != nil {
		bindingKey := sha256.Sum256([]byte(as.RunID))
		bindingState := filepath.Join(r.cfg.Journal.Directory(), "codex-executions", hex.EncodeToString(bindingKey[:]))
		session, err = resolved.OpenRun(ctx, dir, bindingState, as.RunID, run.Thread != "", as.Manager.NativeOwnerTools && !as.Manager.ReceiptOnly && !as.Manager.RequireReceiptReader)
	} else {
		session, err = open(ctx, dir)
	}
	if err != nil {
		return err
	}
	opened = true
	nativeClosed := false
	var emitMu sync.Mutex
	driver := coordination.Driver{RequireReceiptReader: as.Manager.RequireReceiptReader, NativeOwnerTools: as.Manager.NativeOwnerTools && !as.Manager.ReceiptOnly, OnUsageStopped: func(stopCtx context.Context, thread string) error {
		return r.cfg.Journal.StopUsageSource(stopCtx, as.RunID, thread)
	}, BeforeUsageDispatch: func(sourceCtx context.Context, source nativeusage.Source) error {
		return r.activateUsage(sourceCtx, as, source)
	}, LegacyRawUsage: !r.supportsNativeUsage(), OnUsageSource: func(sourceCtx context.Context, source nativeusage.Source) error {
		return r.registerUsage(sourceCtx, as, source)
	}, OnSource: func(sourceCtx context.Context, source protocol.RolloutPreviewSource) error {
		return r.registerRollout(sourceCtx, as.RunID, source)
	}, Session: session, Directory: dir, Instructions: resolved.Instructions, Profile: resolved.Profile}
	uploadKey := sha256.Sum256([]byte(as.RunID))
	driver.Upload = func(ctx context.Context, path, name string) (protocol.ArtifactFile, error) {
		return r.uploadFile(ctx, as, Prepared{Dir: dir, StateDir: filepath.Join(r.cfg.Journal.Directory(), "manager-uploads", hex.EncodeToString(uploadKey[:]))}, path, name)
	}
	driver.Download = func(ctx context.Context, file protocol.ArtifactFile) (string, error) {
		return r.downloadRetainedFile(ctx, as, filepath.Join(dir, "attachments"), file)
	}
	failureCtx, cancelFailureObserver := context.WithCancel(ctx)
	failureDone := make(chan struct{})
	failures := driver.Failures()
	go func() {
		defer close(failureDone)
		select {
		case err := <-failures:
			r.cfg.Log.ErrorContext(lifetime, "manager native event transport failed", "run_id", as.RunID, "err", err)
			cancelNative(err)
		case <-failureCtx.Done():
		}
	}()
	defer func() { cancelFailureObserver(); <-failureDone }()

	driver.Emit = func(ctx context.Context, event coordination.Event) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		if event.Native != nil {
			native := *event.Native
			if native.ContextUsage != nil || native.Kind == harness.EventTurnCompleted {
				r.wakeNativeUsage()
			}
			var err error
			native.Raw, err = coordination.CompactNativeImages(native.Raw)
			if err != nil {
				return fmt.Errorf("compact manager event: %w", err)
			}
			event.Native = &native
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encode manager event: %w", err)
		}
		env, err := r.newEnvelope(protocol.OpManagerNativeEvent, runTarget(as), nil)
		if err != nil {
			return err
		}
		if err = r.cfg.Journal.SaveManagerEvent(ctx, as.RunID, env, raw); err != nil {
			return fmt.Errorf("persist manager event: %w", err)
		}
		if err = r.flushManagerEvents(ctx, as); err != nil {
			return fmt.Errorf("deliver persisted manager event %s: %w", env.RequestID, err)
		}
		return nil
	}
	defer func() {
		receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(lifetime), r.cfg.RequestTimeout)
		defer cancel()
		_, closeErr := driver.Handle(receiptCtx, "close", json.RawMessage(`{}`))
		if closeErr == nil {
			_ = r.cfg.Journal.ConfirmStopped(receiptCtx, as.RunID)
		}
		if !nativeClosed && lifetime.Err() == nil {
			_ = driver.Emit(receiptCtx, coordination.Event{Disconnected: true})
		}
	}()
	if err = r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		return err
	}
	// Replay prior sequenced facts before emitting reconnect or resume facts.
	if err = r.flushManagerEvents(ctx, as); err != nil {
		return err
	}
	if run.Attempts > 0 {
		// Tell an attached server proxy to reconcile its old native turn. A new
		// proxy begins after this cursor and reattaches using the same identity.
		if err = driver.Emit(ctx, coordination.Event{Disconnected: true}); err != nil {
			return err
		}
		attachments, err := r.cfg.Journal.ManagerAttachments(ctx, as.RunID)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, p := range attachments {
			var req coordination.Request
			if err = json.Unmarshal(p.Payload, &req); err != nil {
				return err
			}
			if seen[req.ThreadID] {
				continue
			}
			seen[req.ThreadID] = true
			if _, err = driver.Handle(ctx, "resume", p.Payload); err != nil {
				return err
			}
		}
	}
	for ctx.Err() == nil {
		select {
		case <-session.Done():
			return errors.New("manager native process exited")
		default:
		}
		var request *protocol.Envelope
		pending, err := r.cfg.Journal.Requests(ctx)
		if err != nil {
			return err
		}
		for _, p := range pending {
			if p.RunID != as.RunID || !strings.HasPrefix(p.Envelope.Operation, "manager.") {
				continue
			}
			if p.Envelope.Operation == protocol.OpManagerNativeClaim {
				saved := p.Envelope
				request = &saved
				continue
			}
			if err = r.sendManagerReceipt(ctx, as, &p.Envelope); err != nil {
				return err
			}
			if err = r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
				return err
			}
			if p.Envelope.Operation == protocol.OpManagerNativeReply {
				var reply protocol.ManagerNativeReply
				if json.Unmarshal(p.Envelope.Payload, &reply) == nil && reply.Error == "" {
					closeReply, err := r.cfg.Journal.IsManagerClose(ctx, reply.ID)
					if err != nil {
						return err
					}
					if closeReply {
						nativeClosed = true
						return r.finishJournal(ctx, as.RunID)
					}
				}
			}
		}
		if request == nil {
			payload, payloadErr := r.managerClaimPayload(ctx, as, false)
			if payloadErr != nil {
				return payloadErr
			}
			request, err = r.newEnvelope(protocol.OpManagerNativeClaim, runTarget(as), payload)
			if err != nil {
				return err
			}
			if err = r.cfg.Journal.PutRequest(ctx, journal.Request{RunID: as.RunID, Envelope: *request}); err != nil {
				return err
			}
		}
		op, err := r.send(ctx, runCredential(as), request)
		if err != nil {
			return err
		}
		if op.Status != protocol.OpSucceeded {
			return errors.New("runner: native claim has not succeeded")
		}
		if err = r.confirmManagerClaim(ctx, as, request); err != nil {
			return err
		}
		var command protocol.ManagerNativeRequest
		if err = json.Unmarshal(op.Result, &command); err != nil {
			return err
		}
		if command.ID == "" {
			if err = r.cfg.Journal.DeleteRequest(ctx, request.RequestID); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		status, reply, err := r.cfg.Journal.ManagerOperation(ctx, as.RunID, command)
		if err != nil {
			return err
		}
		// Retain the claim receipt until a retryable reply is durable. It is
		// the recovery anchor for received, started and completed operations.
		if reply == nil {
			reply = &protocol.ManagerNativeReply{ID: command.ID}
			if r.supportsManagerAcknowledgements() {
				reply.Method = command.Method
			}
			if status == "started" {
				reply.Error = "Native operation outcome requires reconciliation; automatic replay is disabled"
			} else {
				if !journal.ManagerReadOnly(command.Method) {
					if err = r.cfg.Journal.StartManagerOperation(ctx, command.ID); err != nil {
						return err
					}
				}
				result, callErr := driver.Handle(ctx, command.Method, command.Payload)
				if command.Method == "close" && callErr == nil {
					// Delivery can be cancelled after this physical close. No
					// cleanup event may follow its frozen event cursor.
					nativeClosed = true
				}
				if callErr != nil {
					reply.Error = "Native coordinator operation failed"
					reply.Rejected = errors.Is(callErr, harness.ErrRejected)
				} else {
					reply.Result, err = json.Marshal(result)
					if err != nil {
						return err
					}
					if command.Method == "find" {
						reply.Result, err = coordination.CompactNativeImages(reply.Result)
						if err != nil {
							return err
						}
					}
				}
			}
			if command.Method == "close" && reply.Error == "" {
				if err = r.flushManagerEvents(ctx, as); err != nil {
					return err
				}
				reply.LastSequence, err = r.cfg.Journal.LastManagerEvent(ctx, as.RunID)
				if err != nil {
					return err
				}
			}
			var original coordination.Request
			if command.Method == "start" || command.Method == "resume" {
				if err = json.Unmarshal(command.Payload, &original); err != nil {
					return err
				}
			}
			if (command.Method == "start" || command.Method == "resume") && original.Config.Purpose != "internal_summary" {
				if reply.Error == "" {
					var info coordination.ThreadInfo
					if err = json.Unmarshal(reply.Result, &info); err != nil {
						return err
					}
					if err = r.cfg.Journal.SetThread(ctx, as.RunID, info.ID); err != nil {
						return err
					}
				}
			}
			if command.Method == "turn" && reply.Error == "" {
				var request coordination.Request
				var turn string
				if json.Unmarshal(command.Payload, &request) == nil && json.Unmarshal(reply.Result, &turn) == nil && turn != "" {
					if err = r.registerRollout(ctx, as.RunID, protocol.RolloutPreviewSource{SessionID: request.ThreadID, TurnID: turn}); err != nil {
						return err
					}
				}
			}
			if !journal.ManagerReadOnly(command.Method) {
				if err = r.cfg.Journal.CompleteManagerOperation(ctx, *reply); err != nil {
					return err
				}
			}
		}
		if r.supportsManagerAcknowledgements() {
			reply.Method = command.Method
		}
		env, err := r.newEnvelope(protocol.OpManagerNativeReply, runTarget(as), reply)
		if err != nil {
			return err
		}
		if err = r.cfg.Journal.PutRequest(ctx, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
			return err
		}
		if err = r.cfg.Journal.DeleteRequest(ctx, request.RequestID); err != nil {
			return err
		}
		if err = r.sendManagerReceipt(ctx, as, env); err != nil {
			return err
		}
		if err = r.cfg.Journal.DeleteRequest(ctx, env.RequestID); err != nil {
			return err
		}
		if command.Method == "close" && reply.Error == "" {
			nativeClosed = true
			return r.finishJournal(ctx, as.RunID)
		}
	}
	return ctx.Err()
}

// Replay completed close receipts before opening a native process or emitting
// reconnect facts. A new event would invalidate the frozen close cursor.
func (r *Runner) replayManagerClose(ctx context.Context, as protocol.Assignment) (bool, error) {
	reply, err := r.cfg.Journal.CompletedManagerClose(ctx, as.RunID)
	if err != nil || reply == nil {
		return false, err
	}
	if err = r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
		return true, err
	}
	if err = r.flushManagerEvents(ctx, as); err != nil {
		return true, err
	}
	if delivered, err := r.cfg.Journal.ManagerCloseReplyDelivered(ctx, as.RunID, reply.ID); err != nil {
		return true, err
	} else if delivered {
		// The original completed close remains physical proof. Its accepted
		// delivery must not become a new reply after the peer retires it.
		return true, r.finishJournal(ctx, as.RunID)
	}
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return true, err
	}
	var env *protocol.Envelope
	for _, request := range pending {
		if request.RunID != as.RunID || request.Envelope.Operation != protocol.OpManagerNativeReply {
			continue
		}
		var saved protocol.ManagerNativeReply
		if err := json.Unmarshal(request.Envelope.Payload, &saved); err != nil {
			return true, err
		}
		if saved.ID == reply.ID {
			original := request.Envelope
			env = &original
			break
		}
	}
	if env == nil {
		env, err = r.newEnvelope(protocol.OpManagerNativeReply, runTarget(as), reply)
		if err != nil {
			return true, err
		}
		if err = r.cfg.Journal.PutRequest(ctx, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
			return true, err
		}
	}
	if err = r.sendManagerReceipt(ctx, as, env); err != nil {
		return true, err
	}
	return true, r.finishJournal(ctx, as.RunID)
}

// A close proof may reach the server before the final uploaded event has
// finished applying. Retry that same immutable receipt, never the native call.
// Prolonged uncertainty backs off, including HTTP 500 responses wrapped as
// transport failures. The shared send loop would otherwise retry them forever
// at its fixed interval before this receipt loop could observe the error.
func (r *Runner) sendManagerReceipt(ctx context.Context, as protocol.Assignment, env *protocol.Envelope) error {
	if env.Operation != protocol.OpManagerNativeReply {
		_, err := r.send(ctx, runCredential(as), env)
		return err
	}
	// This budget covers delivery of an already completed native operation,
	// not the operation itself. Retries retain its original envelope/deadline.
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	delay := r.cfg.RetryDelay
	limit := max(delay, 30*time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		op, err := r.sendOnce(ctx, runCredential(as), env)
		if err == nil && op.Status != protocol.OpSucceeded {
			err = protocol.Errorf(protocol.CodeUnknownOutcome, "native result delivery is not yet confirmed")
		}
		if err == nil && r.supportsManagerAcknowledgements() {
			var reply protocol.ManagerNativeReply
			if err = json.Unmarshal(env.Payload, &reply); err != nil {
				return err
			}
			if err = r.cfg.Journal.SettleManagerReceipt(ctx, as.RunID, *env); err != nil {
				return err
			}
			if ackErr := r.flushManagerAcknowledgements(ctx, as); ackErr != nil {
				r.cfg.Log.WarnContext(ctx, "native delivery acknowledgement remains pending", "run_id", as.RunID, "command_id", reply.ID, "err", ackErr)
			}
		}
		if err == nil || !isRetryable(err) && protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		r.cfg.Log.WarnContext(ctx, "manager native receipt pending", "run_id", as.RunID, "request_id", env.RequestID, "trace_id", env.TraceID, "retry_delay", delay, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < limit {
			delay = min(2*delay, limit)
		}
	}
}

func (r *Runner) flushManagerEvents(ctx context.Context, as protocol.Assignment) error {
	r.wakeRolloutArchive()
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	events := []journal.Request{}
	sequence := func(p journal.Request) int64 {
		var event protocol.ManagerNativeEvent
		_ = json.Unmarshal(p.Envelope.Payload, &event)
		return event.Sequence
	}
	for _, p := range pending {
		if p.RunID == as.RunID && p.Envelope.Operation == protocol.OpManagerNativeEvent {
			events = append(events, p)
		}
	}
	sort.Slice(events, func(i, j int) bool { return sequence(events[i]) < sequence(events[j]) })
	for _, p := range events {
		if _, err = r.send(ctx, runCredential(as), &p.Envelope); err != nil {
			return fmt.Errorf("deliver original manager event %s sequence %d: %w", p.Envelope.RequestID, sequence(p), err)
		}
		if err = r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
			return err
		}
	}
	return nil
}
