package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

const (
	recoveryClosing    = "closing"
	recoveryContinuing = "continuing"
	recoveryLaunching  = "launching"
	recoveryRunning    = "running"
	recoveryCompleted  = "completed"
)

// recoveryState is part of WorkerSpec.Thread, saved synchronously in the runner
// journal before any external mutation. A restart cannot reset its allowance.
type recoveryState struct {
	Attempts           int    `json:"attempts"`
	Phase              string `json:"phase"`
	Code               string `json:"code"`
	SessionID          string `json:"session_id"`
	TranscriptPath     string `json:"transcript_path"`
	TurnID             string `json:"turn_id"`
	LaunchID           string `json:"launch_id,omitempty"`
	RuntimeID          string `json:"runtime_id,omitempty"`
	Terminal           string `json:"terminal"`
	Pane               string `json:"pane"`
	TurnAt             int64  `json:"turn_at"`
	OriginalPrompt     string `json:"original_prompt,omitempty"`
	ContinuationIssued bool   `json:"continuation_issued,omitempty"`
}

func (w *worker) resetNative() {
	w.st = workerState{Recovery: w.st.Recovery, Startup: w.st.Startup, RootObservationRequired: w.st.RootObservationRequired}
}

func (w *worker) recoveryBrief() string {
	r := w.st.Recovery
	// The complete immutable spec, role, accepted inputs, baseline and previously
	// submitted results are still supplied by the ordinary brief/bridge.
	b, _ := json.Marshal(struct {
		Attempt        int    `json:"attempt"`
		Code           string `json:"code"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		TurnID         string `json:"turn_id"`
	}{r.Attempts, r.Code, r.SessionID, r.TranscriptPath, r.TurnID})
	return "The previous root model turn failed. Continue this SAME run and workspace; do not start the task again. Recovery provenance: " + string(b) +
		"\nUse the preserved native conversation history, and the recorded transcript path when available, to recover completed commands, reports and pending operations. The original history is preserved. Inspect current files and previously submitted results before continuing. First reconcile every previously initiated deployment, push or other external operation by its original request identity and actual state. If an outcome remains unknown, ask the owner through the provided tool; never replay the operation. Do not reset, clean, stash or switch the workspace branch."
}

func (w *worker) recoverFailure(ctx context.Context, a Agent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.st.Send != "" {
		// Settle the previous prompt under its own identity before creating a
		// distinct recovery action. send deliberately replays saved text.
		if err := w.send(ctx, ""); err != nil {
			return err
		}
		fresh, alive, err := w.agent(ctx)
		if err != nil {
			return err
		}
		if !alive {
			return errors.New("orca: original terminal disappeared while settling its prompt")
		}
		if reason := w.takenOver(fresh); reason != "" {
			w.holding = reason
			return nil
		}
		if !rootFailed(fresh, w.st.Turn) {
			return nil
		}
		a = fresh
	}
	limit := 1
	if w.ws.opts.RecoveryAttempts != nil {
		limit = min(limit, *w.ws.opts.RecoveryAttempts)
	}
	attempts := 0
	if w.st.Recovery != nil {
		attempts = w.st.Recovery.Attempts
	}
	if attempts >= limit || a.ProviderSession == nil || a.ProviderSession.ID == "" {
		return errors.New("orca: the worker's root turn failed; automatic recovery unavailable or exhausted")
	}
	var fact failedTurn
	var err error
	if a.RootTurn != nil {
		fact, err = nativeFailedTurn(a, w.st.Turn)
		// Context reconstruction still requires the original complete JSONL proof.
		// Native metadata alone never authorizes closing/rebuilding a conversation.
		if err != nil && validRootTurn(a, w.st.Turn) && a.RootTurn.State == "failed" && a.RootTurn.Operations == "settled" && a.ProviderSession.TranscriptPath != "" {
			legacy, legacyErr := readFailedTurn(ctx, *a.ProviderSession, w.st.Turn)
			if legacyErr == nil && legacy.Code == "invalid_encrypted_content" && legacy.TurnID == a.RootTurn.TurnID {
				fact, err = legacy, nil
			}
		}
	} else {
		fact, err = readFailedTurn(ctx, *a.ProviderSession, w.st.Turn)
	}
	if err != nil {
		return fmt.Errorf("orca: root turn failed; recovery evidence unavailable: %w", err)
	}
	if fact.Code != "invalid_encrypted_content" && fact.Code != "http_connection_failed" && fact.Code != "response_stream_connection_failed" && fact.Code != "response_stream_disconnected" && fact.Code != "rate_limit_exceeded" {
		return fmt.Errorf("orca: root turn failed (%s); not an automatically recoverable error", fact.Code)
	}
	w.st.Recovery = &recoveryState{Attempts: attempts + 1, Phase: recoveryClosing, Code: fact.Code,
		SessionID: a.ProviderSession.ID, TranscriptPath: a.ProviderSession.TranscriptPath, TurnID: fact.TurnID,
		Terminal: w.st.Terminal, Pane: w.st.Pane, TurnAt: a.MainAgent.StateStartedAt, OriginalPrompt: w.st.Said}
	if a.RootTurn != nil {
		w.st.Recovery.LaunchID, w.st.Recovery.RuntimeID = a.RootTurn.LaunchID, a.RuntimeID
	}
	if fact.Code != "invalid_encrypted_content" {
		w.st.Recovery.Phase = recoveryContinuing
	}
	if err := w.save(); err != nil {
		return err
	}
	w.emitRecoveryPhase(ctx)
	if w.st.Recovery.Phase == recoveryContinuing {
		return w.continueFailedSession(ctx)
	}
	if err := w.closeFailedSession(ctx); err != nil {
		return err
	}
	return w.launch(ctx, orcaAgents[w.ws.opts.Agent])
}

// closeFailedSession can be resumed after a crash on either side of terminal
// close. It never closes a terminal now answering an owner's different prompt.
func (w *worker) closeFailedSession(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := w.st.Recovery
	terms, err := w.ws.opts.Client.Terminals(ctx, workspaceSelector(w.wt))
	if err != nil {
		return err
	}
	for _, term := range terms {
		if term.PaneKey() == r.Pane && term.Handle != r.Terminal {
			return fmt.Errorf("%w: the failed terminal pane belongs to another terminal", harness.ErrHeld)
		}
	}
	a, alive, err := w.agent(ctx)
	if err != nil {
		return err
	}
	if alive {
		if w.spec.HandBack || a.Prompt == "" || w.st.Said == "" || w.takenOver(a) != "" || a.ProviderSession == nil || a.ProviderSession.ID != r.SessionID ||
			!rootFailed(a, w.st.Turn) || a.MainAgent.StateStartedAt != r.TurnAt {
			return fmt.Errorf("%w: failed turn ownership or background completion changed before recovery", harness.ErrHeld)
		}
		wait, err := w.ws.opts.Client.WaitFor(ctx, w.st.Terminal, WaitTUIIdle, w.ws.opts.WaitChunk)
		if err != nil && !isWaitTimeout(err) {
			return errors.New("orca: failed turn background work is not confirmed idle")
		}
		if err := w.recheckFailedTurn(ctx, wait); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.ws.Release(ctx, w.spec.RunID, mustState(w.st)); err != nil {
			return err
		}
	}
	w.resetNative()
	w.st.Recovery.Phase = recoveryLaunching
	return w.save()
}

func mustState(st workerState) string {
	b, _ := json.Marshal(st)
	return string(b)
}

// continueFailedSession settles a saved send before considering another one.
func (w *worker) continueFailedSession(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a, alive, err := w.agent(ctx)
	if err != nil {
		return err
	}
	r := w.st.Recovery
	if !alive || a.ProviderSession == nil || a.ProviderSession.ID != r.SessionID || w.spec.HandBack {
		return errors.New("orca: original failed session is unavailable for continuation")
	}
	if r.LaunchID != "" {
		if a.RootTurn == nil || a.RootTurn.LaunchID == "" || a.RuntimeID == "" {
			return errRecoveryUncertain
		}
		if a.RootTurn.LaunchID != r.LaunchID || a.RuntimeID != r.RuntimeID {
			return fmt.Errorf("%w: recovery runtime or terminal launch changed", harness.ErrHeld)
		}
	}
	if w.st.Send != "" {
		prompt := w.recoveryBrief()
		if a.Prompt == "" || (!(r.OriginalPrompt != "" && samePrompt(said(a.Prompt), r.OriginalPrompt)) && !samePrompt(said(a.Prompt), said(prompt))) {
			return fmt.Errorf("%w: recovery receipt belongs to a superseded prompt", harness.ErrHeld)
		}
		if err := w.send(ctx, ""); err != nil {
			return err
		}
		r.Phase = recoveryRunning
		if err := w.save(); err != nil {
			return err
		}
		w.emitRecoveryPhase(ctx)
		return nil
	}
	if r.OriginalPrompt == "" {
		r.OriginalPrompt = w.st.Said
	}
	if a.RootTurn != nil && (!validRootTurn(a, w.st.Turn) || a.RootTurn.State == "unknown") {
		return errRecoveryUncertain
	}
	prompt := w.recoveryBrief()
	alreadySent := samePrompt(said(a.Prompt), said(prompt)) && a.Prompt != "" && a.StateStartedAt >= w.st.Turn
	if !alreadySent {
		if a.Prompt == "" || r.OriginalPrompt == "" || !samePrompt(said(a.Prompt), r.OriginalPrompt) || a.Interrupted || !rootFailed(a, r.TurnAt) || a.MainAgent.StateStartedAt != r.TurnAt {
			return fmt.Errorf("%w: original failed turn changed before continuation", harness.ErrHeld)
		}
	}
	if !alreadySent && r.ContinuationIssued && w.st.Send == "" {
		return errRecoveryUncertain
	}
	if !alreadySent {
		wait, err := w.ws.opts.Client.WaitFor(ctx, w.st.Terminal, WaitTUIIdle, w.ws.opts.WaitChunk)
		if err != nil && !isWaitTimeout(err) {
			return errors.New("orca: failed turn background work is not confirmed idle")
		}
		if err := w.recheckFailedTurn(ctx, wait); err != nil {
			return err
		}
	}
	if !alreadySent || w.st.Send != "" {
		if err := w.send(ctx, prompt); err != nil {
			return err
		}
	}
	r.Phase = recoveryRunning
	if err := w.save(); err != nil {
		return err
	}
	w.emitRecoveryPhase(ctx)
	return nil
}

// A root error can leave the legacy aggregate row working after its last child
// has settled. Only a live, explicitly present child inventory overrides that
// aggregate; missing or restored/unverifiable rows are not completion proof.
func backgroundSettled(a Agent, wait Wait) bool {
	if a.RootTurn != nil && (!validRootTurn(a, 0) || a.RootTurn.Operations != "settled" || (a.RootTurn.State != "completed" && a.RootTurn.State != "failed" && a.RootTurn.State != "cancelled")) {
		return false
	}
	if a.RootTurn != nil && a.UpdatedAt > 0 && a.UpdatedAt <= a.RootTurn.ObservedAt {
		// Native item/child history covers the operation inventory. A stale
		// aggregate or missing JSONL is not newer evidence of background activity.
		return true
	}
	if a.State == stateDone && wait.Satisfied {
		return true
	}
	if a.Children == nil || a.SubagentObservation != "live" {
		return false
	}
	for _, child := range a.Children {
		if child.State != "done" || child.Membership != "settled" {
			return false
		}
	}
	return true
}

func isWaitTimeout(err error) bool {
	oe, ok := errors.AsType[*Error](err)
	return ok && oe.Code == codeTimeout
}

// WaitFor may observe idle just before the owner submits another turn. Read the
// exact root identity again after that blocking observation, immediately before
// the bounded close/send operation. An idle reply is not an ownership token.
func (w *worker) recheckFailedTurn(ctx context.Context, wait Wait) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a, alive, err := w.agent(ctx)
	if err != nil {
		return err
	}
	r := w.st.Recovery
	expected := r.OriginalPrompt
	if expected == "" {
		expected = w.st.Said
	}
	if r.Phase == recoveryContinuing && a.RootTurn != nil && (!validRootTurn(a, w.st.Turn) || a.RootTurn.State == "unknown") {
		return errRecoveryUncertain
	}
	if !alive || w.spec.HandBack || a.ProviderSession == nil || a.ProviderSession.ID != r.SessionID ||
		a.Prompt == "" || expected == "" || !samePrompt(said(a.Prompt), expected) || a.Interrupted ||
		!rootFailed(a, r.TurnAt) || a.MainAgent.StateStartedAt != r.TurnAt {
		return fmt.Errorf("%w: failed turn changed while waiting for recovery", harness.ErrHeld)
	}
	if r.LaunchID != "" && (!validRootTurn(a, w.st.Turn) || a.RootTurn.LaunchID != r.LaunchID ||
		a.RootTurn.TurnID != r.TurnID || a.RuntimeID != r.RuntimeID) {
		return fmt.Errorf("%w: native recovery identity changed", harness.ErrHeld)
	}
	if !backgroundSettled(a, wait) {
		return errors.New("orca: failed turn background work changed while waiting for recovery")
	}
	return nil
}

func (w *worker) finishHandback(ctx context.Context) error {
	if w.st.Recovery == nil && w.st.FailedTurn == 0 {
		return nil
	}
	if w.st.Recovery != nil {
		w.st.Recovery.Phase = "handed_back"
	}
	w.st.HandbackEventID = fmt.Sprintf("%s@%d/recovery-handed-back", w.st.Terminal, w.st.Turn)
	if err := w.save(); err != nil {
		return err
	}
	w.emitHandback(ctx)
	return nil
}

func (w *worker) emitHandback(ctx context.Context) {
	id := w.st.HandbackEventID
	if id == "" {
		id = fmt.Sprintf("%s@%d/recovery-handed-back", w.st.Terminal, w.st.Turn)
	}
	attempts := 0
	if w.st.Recovery != nil {
		attempts = w.st.Recovery.Attempts
	}
	w.emitID(ctx, id, "worker.recovery_handed_back", map[string]int{"attempts": attempts})
}

// Phase checkpoints replay facts, not provider mutations. Identity and payload
// are stable across native-terminal changes and runner restarts.
func (w *worker) emitRecoveryPhase(ctx context.Context) {
	r := w.st.Recovery
	if r == nil {
		if w.st.HandbackEventID != "" {
			w.emitHandback(ctx)
		}
		return
	}
	kind, phase := "", ""
	switch r.Phase {
	case recoveryClosing, recoveryContinuing, recoveryLaunching:
		kind, phase = "worker.recovery_pending", "pending"
	case recoveryRunning:
		kind, phase = "worker.recovery_started", "running"
	case recoveryCompleted:
		kind, phase = "worker.recovery_completed", "completed"
	case "handed_back":
		w.emitHandback(ctx)
		return
	default:
		return
	}
	id := fmt.Sprintf("%s/%s/recovery-%d/%s", r.Terminal, r.TurnID, r.Attempts, phase)
	w.emitID(ctx, id, kind, map[string]any{"attempts": r.Attempts, "code": r.Code, "phase": phase})
}

// An unknown send is not worker completion or a human takeover. Keep its
// terminal and execution slot until the original receipt can be reconciled.
var errRecoveryUncertain = errors.New("orca: recovery prompt submission is unknown; reconciliation is required")

func (w *worker) awaitRecoveryReconciliation(ctx context.Context) bool {
	identity := fmt.Sprintf("%s/recovery-uncertain/%d", w.st.Terminal, w.st.Recovery.Attempts)
	code := "recovery_submission_unknown"
	if !w.st.Recovery.ContinuationIssued && w.st.Send == "" {
		code = "observation_unavailable"
	}
	w.emitID(ctx, identity, "worker.root_observation_unknown", map[string]any{
		"turn_at": w.st.Turn, "managed_turn_at": w.st.Turn, "code": code, "background_pending": true, "root_state": "unknown",
	})
	for ctx.Err() == nil {
		// A saved receipt is queried under its original identity. Without one,
		// continueFailedSession only accepts an observed matching prompt after issue.
		var err error
		if w.spec.HandBack {
			err = w.begin(ctx, orcaAgents[w.ws.opts.Agent])
		} else {
			err = w.continueFailedSession(ctx)
		}
		if err == nil {
			w.emitID(ctx, identity+"/resolved", "worker.root_observation_resolved", map[string]any{"managed_turn_at": w.st.Turn})
			return true
		}
		if errors.Is(err, harness.ErrHeld) {
			w.holding = err.Error()
			w.hold(ctx)
			return false
		}
		delay := w.ws.opts.Poll
		if delay <= 0 {
			delay = time.Second
		}
		if sleep(ctx, delay) != nil {
			break
		}
	}
	// Shutdown or lost connectivity cannot authorize mutating an uncertain turn.
	if errors.Is(context.Cause(ctx), harness.ErrStopped) {
		w.stop(ctx, context.Cause(ctx))
	}
	w.err = context.Cause(ctx)
	return false
}
