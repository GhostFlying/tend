package orca

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// Use the native terminal format, including the lost error code, rather than
// synthesizing a typed rate_limit_exceeded that the provider never supplied.
func traexRateTranscript(t *testing.T) ProviderSession {
	t.Helper()
	session := recoveryTranscript(t, "")
	body, err := os.ReadFile(session.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(body), `"id":"session-root"`, `"id":"session-root","originator":"traex-tui","model_provider":"trae"`, 1)
	text = strings.Replace(text, "stream disconnected: code: invalid_encrypted_content; message: encrypted history failed", traexTerminalRateLimit, 1)
	if err := os.WriteFile(session.TranscriptPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestTraexTerminalRateLimitRequiresTrustedCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		want                   bool
	}{
		{name: "native terminal error", want: true},
		{name: "other provider", old: `"model_provider":"trae"`, replacement: `"model_provider":"gateway"`},
		{name: "other client", old: `"originator":"traex-tui"`, replacement: `"originator":"codex"`},
		{name: "assistant prose", old: `"type":"task_complete"`, replacement: `"type":"agent_message"`},
		{name: "generic error", old: traexTerminalRateLimit, replacement: "requests exceeded the rate limit"},
		{name: "diagnostic suffix", old: traexTerminalRateLimit, replacement: traexTerminalRateLimit + " unauthorized"},
		{name: "explicit auth", old: `"codex_error_info":"other"`, replacement: `"codex_error_info":"unauthorized"`},
		{name: "explicit quota", old: `"error":{"message"`, replacement: `"error":{"code":"insufficient_quota","message"`},
		{name: "unmatched end", old: `"type":"task_complete","turn_id":"turn-root"`, replacement: `"type":"task_complete","turn_id":"old-turn"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := traexRateTranscript(t)
			body, err := os.ReadFile(session.TranscriptPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.old != "" {
				body = []byte(strings.Replace(string(body), tc.old, tc.replacement, 1))
			}
			if err := os.WriteFile(session.TranscriptPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			fact, err := readFailedTurn(t.Context(), session, 1)
			got := err == nil && fact.Code == "rate_limit_exceeded" && fact.TraexRateLimit
			if got != tc.want {
				t.Fatalf("fact=%+v err=%v want rate=%v", fact, err, tc.want)
			}
		})
	}
}

func TestFault_TraexRateLimitWaitsAcrossRestartAndContinuesSameRunOnce(t *testing.T) {
	f, w, a := nativeFixture(t)
	session := traexRateTranscript(t)
	a.ProviderSession = &session
	a.RootTurn.ErrorCode, a.RootTurn.Retryable = "other", false
	f.ws.opts.Poll = time.Nanosecond
	now := time.UnixMilli(1000)
	f.ws.opts.Now = func() time.Time { return now }
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if w.st.Recovery == nil || w.st.Recovery.NotBefore != now.Add(30*time.Second).UnixMilli() || w.st.Recovery.Attempts != 1 {
		t.Fatalf("recovery=%+v", w.st.Recovery)
	}
	if f.orca.count("terminal send", nil) != 0 {
		t.Fatal("continued before cooldown")
	}
	var saved workerState
	if err := json.Unmarshal([]byte(f.thread), &saved); err != nil {
		t.Fatal(err)
	}
	now = now.Add(29 * time.Second)
	restarted := &worker{ws: f.ws, wt: w.wt, spec: w.spec, st: saved}
	if err := restarted.begin(t.Context(), "traex"); err != nil {
		t.Fatal(err)
	}
	if f.orca.count("terminal send", nil) != 0 || restarted.st.Recovery.NotBefore != saved.Recovery.NotBefore {
		t.Fatal("restart reset the cooldown or sent early")
	}
	now = now.Add(time.Second)
	if _, done, err := restarted.step(t.Context()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if restarted.st.Recovery.Phase != recoveryRunning || f.orca.count("terminal send", nil) != 1 ||
		f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 ||
		restarted.st.Terminal != w.st.Terminal || a.ProviderSession.ID != session.ID || restarted.spec.RunID != w.spec.RunID {
		t.Fatal("did not continue exactly once in the original run/session")
	}
	if err := restarted.recoverFailure(t.Context(), *a); err == nil || f.orca.count("terminal send", nil) != 1 {
		t.Fatal("restart renewed the recovery budget")
	}
}

func TestFault_TraexRateFallbackRetainsNativeAndToolFences(t *testing.T) {
	for _, change := range []string{"wrong turn", "wrong session", "auth", "unknown operations", "pending operations", "pending tool", "incomplete transcript", "other agent"} {
		t.Run(change, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			session := traexRateTranscript(t)
			a.ProviderSession = &session
			a.RootTurn.ErrorCode, a.RootTurn.Retryable = "other", false
			switch change {
			case "wrong turn":
				a.RootTurn.TurnID = "another-turn"
			case "wrong session":
				a.RootTurn.SessionID = "another-session"
			case "auth":
				a.RootTurn.ErrorCode = "unauthorized"
			case "unknown operations":
				a.RootTurn.Operations = "unknown"
			case "pending operations":
				a.RootTurn.Operations = "pending"
			case "other agent":
				a.AgentType = "codex"
			case "pending tool", "incomplete transcript":
				file, err := os.OpenFile(session.TranscriptPath, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				data := `{"timestamp":"2026-10-26T00:00:04Z","type":"response_item","payload":{"type":"function_call","call_id":"unknown-write"}}` + "\n"
				if change == "incomplete transcript" {
					data = `{"type":`
				}
				_, writeErr := file.WriteString(data)
				closeErr := file.Close()
				if err := errors.Join(writeErr, closeErr); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.recoverFailure(t.Context(), *a); err == nil || w.st.Recovery != nil || f.orca.count("terminal send", nil) != 0 {
				t.Fatalf("unsafe fallback: err=%v recovery=%+v", err, w.st.Recovery)
			}
		})
	}
}

func TestFault_RateCooldownCancellationAndOwnerReplacement(t *testing.T) {
	for _, action := range []string{"cancel", "replacement", "unknown operations"} {
		t.Run(action, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			a.RootTurn.ErrorCode = "rate_limit_exceeded"
			f.ws.opts.Poll = time.Nanosecond
			now := time.UnixMilli(1000)
			f.ws.opts.Now = func() time.Time { return now }
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch action {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "replacement":
				a.Prompt = "owner replacement instruction"
			case "unknown operations":
				a.RootTurn.Operations = "unknown"
			}
			now = now.Add(30 * time.Second)
			_, _, err := w.step(ctx)
			if err != nil || f.orca.count("terminal send", nil) != 0 || w.st.Recovery.Attempts != 1 {
				t.Fatalf("action=%s err=%v", action, err)
			}
			if action == "replacement" && w.holding == "" {
				t.Fatal("replacement should retain owner control")
			}
		})
	}
}

func TestFault_RateCooldownKeepsUnsettledOperationsAcrossRestart(t *testing.T) {
	for _, operations := range []string{"pending", "unknown"} {
		t.Run(operations, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			a.RootTurn.ErrorCode = "rate_limit_exceeded"
			f.ws.opts.Poll = time.Nanosecond
			now := time.UnixMilli(1000)
			f.ws.opts.Now = func() time.Time { return now }
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			a.RootTurn.Operations = operations
			now = now.Add(30 * time.Second)
			if _, done, err := w.step(t.Context()); err != nil || done {
				t.Fatalf("unsettled root terminated: done=%v err=%v", done, err)
			}
			var saved workerState
			if err := json.Unmarshal([]byte(f.thread), &saved); err != nil {
				t.Fatal(err)
			}
			restarted := &worker{ws: f.ws, wt: w.wt, spec: w.spec, st: saved}
			if err := restarted.begin(t.Context(), "traex"); !errors.Is(err, errRecoveryUncertain) {
				t.Fatalf("restart should reconcile the original root: %v", err)
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("unsettled operations replayed or closed")
			}
			a.RootTurn.Operations = "settled"
			if _, done, err := restarted.step(t.Context()); err != nil || done {
				t.Fatalf("settled root failed to continue: done=%v err=%v", done, err)
			}
			if f.orca.count("terminal send", nil) != 1 || restarted.st.Recovery.Attempts != 1 {
				t.Fatal("settlement did not retain the original recovery allowance")
			}
		})
	}
}

func TestFault_RateRecoveryRechecksOperationsBeforeSend(t *testing.T) {
	for _, operations := range []string{"pending", "unknown"} {
		t.Run(operations, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			a.RootTurn.ErrorCode = "rate_limit_exceeded"
			f.ws.opts.Poll = time.Nanosecond
			now := time.UnixMilli(1000)
			f.ws.opts.Now = func() time.Time { return now }
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			previous := f.ws.opts.Client.Exec
			waits := 0
			f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
				if len(args) > 1 && args[0] == "terminal" && args[1] == "wait" {
					waits++
					if waits == 2 {
						a.RootTurn.Operations = operations
					}
				}
				return previous(ctx, dir, args)
			}
			now = now.Add(30 * time.Second)
			if _, _, err := w.step(t.Context()); !errors.Is(err, errRecoveryUncertain) {
				t.Fatalf("late unsettled operation must reconcile, got %v", err)
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("late unsettled operation triggered a mutation")
			}
		})
	}
}

func TestFault_RateCooldownRecordsTakeoverBeforePause(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.ErrorCode = "rate_limit_exceeded"
	f.ws.opts.Poll = time.Nanosecond
	now := time.UnixMilli(1000)
	f.ws.opts.Now = func() time.Time { return now }
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	a.Prompt, a.State, a.StateStartedAt = "owner replacement instruction", stateWorking, now.UnixMilli()
	if _, _, err := w.step(t.Context()); err != nil || w.holding == "" || w.holdingEvidence == nil {
		t.Fatalf("takeover not recorded during cooldown: err=%v holding=%q", err, w.holding)
	}
	w.stop(t.Context(), harness.ErrPaused)
	if f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("pause interrupted the observed owner's turn")
	}
}

func TestFault_RateCooldownRetainsBoundedObservationRetries(t *testing.T) {
	for _, failures := range []int{1, failuresAllowed} {
		t.Run(map[int]string{1: "transient", failuresAllowed: "persistent"}[failures], func(t *testing.T) {
			f, w, a := nativeFixture(t)
			a.RootTurn.ErrorCode = "rate_limit_exceeded"
			f.ws.opts.Poll = time.Nanosecond
			now := time.UnixMilli(1000)
			f.ws.opts.Now = func() time.Time { return now }
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			deadline := w.st.Recovery.NotBefore
			previous := f.ws.opts.Client.Exec
			failedReads := 0
			f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
				if len(args) > 1 && args[0] == "worktree" && args[1] == "ps" && failedReads < failures {
					failedReads++
					return nil, errors.New("observation temporarily unavailable")
				}
				return previous(ctx, dir, args)
			}
			_, done, err := w.step(t.Context())
			if done || (err != nil) != (failures == failuresAllowed) || failedReads != failures {
				t.Fatalf("done=%v err=%v reads=%d", done, err, failedReads)
			}
			if w.st.Recovery.NotBefore != deadline || w.st.Recovery.Attempts != 1 || f.orca.count("terminal send", nil) != 0 {
				t.Fatal("observation retry altered the recovery budget or sent a prompt")
			}
		})
	}
}
