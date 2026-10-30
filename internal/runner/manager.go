package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// executeManager has its own lifecycle because it has no workspace or artifact
// report. Process exit proof and transport receipts remain durable across stop.
func (r *Runner) executeManager(lifetime, runCtx context.Context, a *active) (resultErr error) {
	defer func() {
		if resultErr != nil && lifetime.Err() == nil {
			r.requestReconcile()
		}
	}()
	as := a.as
	run, err := r.cfg.Journal.Run(lifetime, as.RunID)
	if errors.Is(err, journal.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Phase == journal.PhaseReleasing {
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
	run, readErr := r.cfg.Journal.Run(lifetime, as.RunID)
	if errors.Is(readErr, journal.ErrNotFound) {
		return nil
	}
	if readErr != nil {
		return readErr
	}
	if !run.StoppedConfirmed {
		if err := r.cfg.Journal.SetFenced(lifetime, as.RunID, true); err != nil {
			return err
		}
		r.requestReconcile()
		return nil
	}
	last, readErr := r.cfg.Journal.LastManagerEvent(lifetime, as.RunID)
	if readErr != nil {
		return readErr
	}
	env, readErr := r.newEnvelope(protocol.OpManagerNativeStopped, runTarget(as), protocol.ManagerStoppedPayload{Reason: "manager native process stopped; reconcile pending operations", LastSequence: last})
	if readErr != nil {
		return readErr
	}
	if readErr = r.cfg.Journal.Finish(lifetime, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); readErr != nil {
		return readErr
	}
	return r.reportManagerStopped(lifetime, as)
}

func (r *Runner) reportManagerStopped(ctx context.Context, as protocol.Assignment) error {
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	if err = r.flushManagerEvents(ctx, as); err != nil {
		return err
	}
	// Events precede stop even if the final request sorts earlier by ID.
	for _, op := range []string{protocol.OpManagerNativeReply, protocol.OpManagerNativeStopped} {
		for _, p := range pending {
			if p.RunID != as.RunID || p.Envelope.Operation != op {
				continue
			}
			if err = r.sendManagerReceipt(ctx, as, &p.Envelope); err != nil {
				return err
			}
			if err = r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
				return err
			}
		}
	}
	return r.cfg.Journal.Done(ctx, as.RunID)
}

func (r *Runner) managerNative(ctx, lifetime context.Context, as protocol.Assignment) error {
	if closed, err := r.replayManagerClose(ctx, as); closed || err != nil {
		return err
	}
	opened := false
	defer func() {
		if !opened {
			_ = r.cfg.Journal.ConfirmStopped(context.WithoutCancel(lifetime), as.RunID)
		}
	}()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	resolver, ok := r.cfg.Executions.(ManagerExecutionResolver)
	if !ok || as.Execution == nil || as.Manager == nil {
		return errors.New("runner: manager executor is unavailable")
	}
	resolved, err := resolver.ResolveManager(ctx, *as.Execution)
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(as.Manager.ConversationID))
	dir := filepath.Join(r.cfg.Journal.Directory(), "manager-sessions", hex.EncodeToString(key[:]))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if _, err = r.cfg.Journal.NextAttempt(ctx, as.RunID); err != nil {
		return err
	}
	session, err := resolved.Open(ctx, dir)
	if err != nil {
		return err
	}
	opened = true
	nativeClosed := false
	var emitMu sync.Mutex
	driver := coordination.Driver{Session: session, Directory: dir, Instructions: resolved.Instructions, Profile: resolved.Profile}
	driver.Download = func(ctx context.Context, file protocol.ArtifactFile) (string, error) {
		return r.downloadRetainedFile(ctx, as, filepath.Join(dir, "attachments"), file)
	}
	driver.Emit = func(ctx context.Context, event coordination.Event) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		if event.Native != nil {
			native := *event.Native
			var err error
			native.Raw, err = coordination.CompactNativeImages(native.Raw)
			if err != nil {
				return err
			}
			event.Native = &native
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		env, err := r.newEnvelope(protocol.OpManagerNativeEvent, runTarget(as), nil)
		if err != nil {
			return err
		}
		if err = r.flushManagerEvents(ctx, as); err != nil {
			return err
		}
		if err = r.cfg.Journal.SaveManagerEvent(ctx, as.RunID, env, raw); err != nil {
			return err
		}
		_, err = r.send(ctx, runCredential(as), env)
		if err != nil {
			return err
		}
		return r.cfg.Journal.DeleteRequest(ctx, env.RequestID)
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
						return r.cfg.Journal.Done(ctx, as.RunID)
					}
				}
			}
		}
		if request == nil {
			request, err = r.newEnvelope(protocol.OpManagerNativeClaim, runTarget(as), nil)
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
			if status == "started" {
				reply.Error = "Native operation outcome requires reconciliation; automatic replay is disabled"
			} else {
				if err = r.cfg.Journal.StartManagerOperation(ctx, command.ID); err != nil {
					return err
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
			if command.Method == "start" || command.Method == "resume" {
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
			if err = r.cfg.Journal.CompleteManagerOperation(ctx, *reply); err != nil {
				return err
			}
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
			return r.cfg.Journal.Done(ctx, as.RunID)
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
	return true, r.cfg.Journal.Done(ctx, as.RunID)
}

// A close proof may reach the server before the final uploaded event has
// finished applying. Retry that same immutable receipt, never the native call.
func (r *Runner) sendManagerReceipt(ctx context.Context, as protocol.Assignment, env *protocol.Envelope) error {
	for {
		_, err := r.send(ctx, runCredential(as), env)
		if env.Operation != protocol.OpManagerNativeReply || protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.cfg.RetryDelay):
		}
	}
}

func (r *Runner) flushManagerEvents(ctx context.Context, as protocol.Assignment) error {
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
			return err
		}
		if err = r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
			return err
		}
	}
	return nil
}
