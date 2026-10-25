package orca

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func recoveryTranscript(t *testing.T, extra string) ProviderSession {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"session-root"}}
{"timestamp":"2026-10-26T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-root"}}
` + extra + `{"timestamp":"2026-10-26T00:00:03Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-root","error":{"message":"stream disconnected: code: invalid_encrypted_content; message: encrypted history failed","codex_error_info":"other"}}}
`
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return ProviderSession{ID: "session-root", TranscriptPath: p}
}

func TestFailedTurnTranscriptIdentityAndUnknownOperations(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		wrongID     bool
		want        bool
	}{
		{name: "provider error", want: true},
		{name: "wrong session", wrongID: true},
		{name: "unknown tool result", extra: `{"timestamp":"2026-10-26T00:00:02Z","type":"response_item","payload":{"type":"function_call","call_id":"push"}}` + "\n"},
		{name: "tool result observed", want: true, extra: `{"timestamp":"2026-10-26T00:00:02Z","type":"response_item","payload":{"type":"function_call","call_id":"check"}}` + "\n" + `{"timestamp":"2026-10-26T00:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"check"}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := recoveryTranscript(t, tc.extra)
			if tc.wrongID {
				session.ID = "child"
			}
			fact, err := readFailedTurn(t.Context(), session, 1)
			if (err == nil) != tc.want {
				t.Fatalf("fact=%+v err=%v", fact, err)
			}
			if tc.want && (fact.Code != "invalid_encrypted_content" || fact.TurnID != "turn-root") {
				t.Fatalf("fact=%+v", fact)
			}
		})
	}
}

func failedWorker(t *testing.T) (*workerFixture, *worker, *Agent) {
	t.Helper()
	f := newWorkerFixture(t)
	term := Terminal{Handle: "term_old", WorktreeID: f.orca.wt, TabID: "tab", LeafID: "old"}
	f.orca.terms = []Terminal{term}
	session := recoveryTranscript(t, "")
	a := &Agent{PaneKey: term.PaneKey(), State: stateDone, Prompt: "original prompt", StateStartedAt: 200, MainAgent: &MainAgent{State: stateDone, Outcome: "failure", StateStartedAt: 200}, ProviderSession: &session}
	f.orca.agents[a.PaneKey] = a
	w := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement), st: workerState{Worktree: f.orca.wt, Terminal: term.Handle, Pane: term.PaneKey(), Turn: 100, Said: said(a.Prompt)}}
	return f, w, a
}

func TestRootFailureWaitsForBackgroundAndRecoversOnce(t *testing.T) {
	f, w, a := failedWorker(t)
	a.State = stateWorking
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if f.orca.count("terminal close", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
		t.Fatal("background work was disturbed")
	}
	if len(w.pending) != 2 || w.pending[1].Kind != "worker.turn_failed" {
		t.Fatalf("events=%+v", w.pending)
	}
	a.State = stateDone
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("recovery done=%v err=%v", done, err)
	}
	if w.st.Recovery == nil || w.st.Recovery.Attempts != 1 || w.st.Recovery.Phase != recoveryRunning {
		t.Fatalf("state=%+v", w.st)
	}
	if f.orca.count("terminal close", nil) != 1 || f.orca.count("agent launch", nil) != 1 {
		t.Fatal("expected one fresh session")
	}
	if !strings.Contains(w.prompt(), "session-root") || !strings.Contains(w.prompt(), "never replay") {
		t.Fatal("missing recovery context")
	}
	newAgent := f.orca.agents[w.st.Pane]
	newAgent.State = stateDone
	newAgent.MainAgent = &MainAgent{State: stateDone, Outcome: "failure", StateStartedAt: time.Now().UnixMilli() + 1}
	if _, _, err := w.step(t.Context()); err == nil {
		t.Fatal("second failure must not recover")
	}
	if f.orca.count("agent launch", nil) != 1 {
		t.Fatal("recovery allowance reset")
	}
}

func TestRecoveryClosingSurvivesRestartAfterClose(t *testing.T) {
	f, w, a := failedWorker(t)
	f.orca.failClose = true
	if err := w.recoverFailure(t.Context(), *a); err == nil {
		t.Fatal("wanted unknown close")
	}
	if w.st.Recovery == nil || w.st.Recovery.Phase != recoveryClosing {
		t.Fatal("close intent not journaled")
	}
	// A later read proves that the old terminal closed despite the lost reply.
	f.orca.terms = nil
	f.orca.agents = map[string]*Agent{}
	f.orca.failClose = false
	var persisted workerState
	if err := json.Unmarshal([]byte(f.thread), &persisted); err != nil {
		t.Fatal(err)
	}
	restarted := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement), st: persisted}
	if err := restarted.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	if restarted.st.Recovery.Attempts != 1 || f.orca.count("agent launch", nil) != 1 {
		t.Fatal("restart did not preserve one recovery")
	}
}

func TestRecoveryDisabledAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "cancelled"}[cancelled], func(t *testing.T) {
			f, w, a := failedWorker(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				cancel()
			} else {
				zero := 0
				f.ws.opts.RecoveryAttempts = &zero
			}
			if err := w.recoverFailure(ctx, *a); err == nil {
				t.Fatal("expected no recovery")
			}
			if f.orca.count("terminal close", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
				t.Fatal("changed terminal")
			}
		})
	}
}

func TestRootOutcomeDoesNotConfuseChildrenOrOldTurns(t *testing.T) {
	for _, tc := range []struct {
		outcome          string
		at               int64
		failed, answered bool
	}{
		{"failure", 200, true, false}, {"failure", 50, false, false}, {"success", 200, false, true}, {"cancellation", 200, false, false}, {"", 200, false, true}, {"superseded", 200, false, false}, {"unconfirmed", 200, false, false}, {"future_outcome", 200, false, false},
	} {
		a := Agent{State: stateDone, StateStartedAt: 200, MainAgent: &MainAgent{State: stateDone, Outcome: tc.outcome, StateStartedAt: tc.at}}
		if rootFailed(a, 100) != tc.failed || answered(a, 100) != tc.answered {
			t.Fatalf("incorrect outcome %+v", tc)
		}
	}
}

func TestSettledChildInventoryOverridesStaleAggregate(t *testing.T) {
	f, w, a := failedWorker(t)
	a.State = stateWorking
	a.SubagentObservation = "live"
	a.Children = []ChildWork{{State: "done", Membership: "settled"}}
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.orca.count("agent launch", nil) != 1 {
		t.Fatal("settled background command did not unblock recovery")
	}
	for _, observation := range []string{"", "unverifiable"} {
		a.SubagentObservation = observation
		if backgroundSettled(*a, Wait{}) {
			t.Fatal("unverified child inventory proved completion")
		}
	}
	a.SubagentObservation = "live"
	a.Children[0].State = "working"
	if backgroundSettled(*a, Wait{}) {
		t.Fatal("active child proved completion")
	}
}

func TestTemporaryConnectionFailureContinuesOriginalSession(t *testing.T) {
	f, w, a := failedWorker(t)
	body, err := os.ReadFile(a.ProviderSession.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.ReplaceAll(string(body), `"message":"stream disconnected: code: invalid_encrypted_content; message: encrypted history failed","codex_error_info":"other"`, `"message":"stream failed","codex_error_info":{"response_stream_disconnected":{"http_status_code":503}}`))
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 || f.orca.count("terminal send", nil) != 1 {
		t.Fatal("temporary error did not continue original session")
	}
	if w.st.Recovery.Code != "response_stream_disconnected" || w.st.Recovery.Phase != recoveryRunning {
		t.Fatalf("state=%+v", w.st.Recovery)
	}
}

func TestRecoveryDoesNotRetryClientErrors(t *testing.T) {
	for _, raw := range []string{`"unauthorized"`, `{"http_connection_failed":{"http_status_code":401}}`, `{"response_stream_disconnected":{"http_status_code":400}}`, `"sandbox_error"`} {
		if code := temporaryModelError(json.RawMessage(raw)); code != "" {
			t.Fatalf("%s classified %s", raw, code)
		}
	}
}

func TestRateLimitRecoveryUsesOriginalSessionAndSharedBudget(t *testing.T) {
	for _, tc := range []struct {
		name, errorJSON string
		recover         bool
	}{
		{"backend rate limit", `{"message":"POST responses: 400 Bad Request: code: rate_limit_exceeded; message: too_many_requests","codex_error_info":"other"}`, true},
		{"typed rate limit", `{"message":"rate limited","codex_error_info":"rate_limit_exceeded"}`, true},
		{"generic client error", `{"message":"POST responses: 400 Bad Request","codex_error_info":"other"}`, false},
		{"quota", `{"message":"code: insufficient_quota; message: account quota exhausted","codex_error_info":"other"}`, false},
		{"unauthorized", `{"message":"code: unauthorized; message: sign in","codex_error_info":"unauthorized"}`, false},
		{"similar code", `{"message":"code: rate_limit_exceeded_permanently; message: rejected","codex_error_info":"other"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w, a := failedWorker(t)
			body, err := os.ReadFile(a.ProviderSession.TranscriptPath)
			if err != nil {
				t.Fatal(err)
			}
			body = []byte(strings.ReplaceAll(string(body), `{"message":"stream disconnected: code: invalid_encrypted_content; message: encrypted history failed","codex_error_info":"other"}`, tc.errorJSON))
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err = w.step(t.Context())
			if (err == nil) != tc.recover {
				t.Fatalf("err=%v recover=%v", err, tc.recover)
			}
			if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("changed session for rate limit")
			}
			if !tc.recover {
				if f.orca.count("terminal send", nil) != 0 {
					t.Fatal("retried a nonrecoverable client error")
				}
				return
			}
			if w.st.Recovery.Code != "rate_limit_exceeded" || w.st.Recovery.Attempts != 1 || f.orca.count("terminal send", nil) != 1 {
				t.Fatal("did not recover once in original session")
			}
			// The same per-run allowance covers every recovery classification.
			if err := w.recoverFailure(t.Context(), *a); err == nil {
				t.Fatal("rate limit obtained a second recovery allowance")
			}
			if f.orca.count("terminal send", nil) != 1 {
				t.Fatal("replayed recovery after budget exhaustion")
			}
		})
	}
}

func TestRecoveryDoesNotCloseOwnerReplacement(t *testing.T) {
	f, w, a := failedWorker(t)
	f.orca.failClose = true
	if err := w.recoverFailure(t.Context(), *a); err == nil {
		t.Fatal("wanted unknown close")
	}
	f.orca.failClose = false
	a.ProviderSession = &ProviderSession{ID: "owner-session"}
	before := f.orca.count("terminal close", nil)
	if err := w.begin(t.Context(), "codex"); err == nil {
		t.Fatal("owner replacement accepted")
	}
	if f.orca.count("terminal close", nil) != before || f.orca.count("agent launch", nil) != 0 {
		t.Fatal("owner session changed")
	}
}

func TestRecoveryDoesNotStartBesideReplacementTerminal(t *testing.T) {
	f, w, a := failedWorker(t)
	f.orca.failClose = true
	if err := w.recoverFailure(t.Context(), *a); err == nil {
		t.Fatal("wanted unknown close")
	}
	f.orca.failClose = false
	f.orca.terms[0].Handle = "owner-terminal"
	if err := w.begin(t.Context(), "codex"); err == nil {
		t.Fatal("owner replacement accepted")
	}
	if f.orca.count("agent launch", nil) != 0 {
		t.Fatal("started beside owner replacement")
	}
}

func TestRecoveryResumeBeforeContinuationSubmit(t *testing.T) {
	f, w, a := failedWorker(t)
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, Code: "response_stream_disconnected", SessionID: a.ProviderSession.ID, TranscriptPath: a.ProviderSession.TranscriptPath, TurnAt: a.MainAgent.StateStartedAt}
	save := w.spec.OnThread
	w.spec.OnThread = func(s string) error {
		if err := save(s); err != nil {
			return err
		}
		var st workerState
		_ = json.Unmarshal([]byte(s), &st)
		if st.Turn > a.MainAgent.StateStartedAt {
			return errors.New("crash before terminal send")
		}
		return nil
	}
	if err := w.continueFailedSession(t.Context()); err == nil {
		t.Fatal("crash not reached")
	}
	var persisted workerState
	if err := json.Unmarshal([]byte(f.thread), &persisted); err != nil {
		t.Fatal(err)
	}
	resumed := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement), st: persisted}
	if err := resumed.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	if f.orca.count("terminal send", nil) != 1 {
		t.Fatal("continuation was not submitted once")
	}
}

func TestUnknownRecoverySubmissionIsNotReplayed(t *testing.T) {
	f, w, a := failedWorker(t)
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, Code: "response_stream_disconnected", SessionID: a.ProviderSession.ID, TranscriptPath: a.ProviderSession.TranscriptPath, TurnAt: a.MainAgent.StateStartedAt, OriginalPrompt: w.st.Said, ContinuationIssued: true}
	if err := w.continueFailedSession(t.Context()); err == nil || !strings.Contains(err.Error(), "submission is unknown") {
		t.Fatalf("err=%v", err)
	}
	if f.orca.count("terminal send", nil) != 0 {
		t.Fatal("unknown submission replayed")
	}
}

func TestOwnerPromptDuringRecoveryWaitIsNotMutated(t *testing.T) {
	for _, phase := range []string{recoveryClosing, recoveryContinuing} {
		t.Run(phase, func(t *testing.T) {
			f, w, a := failedWorker(t)
			w.st.Recovery = &recoveryState{Attempts: 1, Phase: phase, SessionID: a.ProviderSession.ID, TranscriptPath: a.ProviderSession.TranscriptPath, TurnAt: a.MainAgent.StateStartedAt, OriginalPrompt: w.st.Said, Terminal: w.st.Terminal, Pane: w.st.Pane}
			original := f.ws.opts.Client.Exec
			f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
				if len(args) > 1 && args[0] == "terminal" && args[1] == "wait" {
					a.Prompt = "owner replacement turn"
					a.State = stateWorking
					a.MainAgent = &MainAgent{State: stateWorking, StateStartedAt: 300}
					return ok(map[string]any{"wait": map[string]any{"satisfied": true}}), nil
				}
				return original(ctx, dir, args)
			}
			var err error
			if phase == recoveryClosing {
				err = w.closeFailedSession(t.Context())
			} else {
				err = w.continueFailedSession(t.Context())
			}
			if err == nil {
				t.Fatal("ownership change was ignored")
			}
			if f.orca.count("terminal close", nil) != 0 || f.orca.count("terminal send", nil) != 0 {
				t.Fatal("owner's new turn was mutated")
			}
		})
	}
}

func TestExplicitHandbackSupersedesAutomaticRecovery(t *testing.T) {
	for _, phase := range []string{recoveryClosing, recoveryContinuing} {
		t.Run(phase, func(t *testing.T) {
			f, w, a := failedWorker(t)
			w.st.Recovery = &recoveryState{Attempts: 1, Phase: phase, Code: "response_stream_disconnected", SessionID: a.ProviderSession.ID, TranscriptPath: a.ProviderSession.TranscriptPath, TurnAt: a.MainAgent.StateStartedAt, OriginalPrompt: w.st.Said}
			w.spec.HandBack = true
			if err := w.begin(t.Context(), "codex"); err != nil {
				t.Fatal(err)
			}
			if f.orca.count("terminal send", nil) != 1 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("handback did not replace automatic recovery")
			}
			if w.st.Recovery.Attempts != 1 || w.st.Recovery.Phase != "handed_back" {
				t.Fatal("handback reset recovery history")
			}
			seen := false
			for _, ev := range w.pending {
				if ev.Kind == "worker.recovery_handed_back" {
					seen = true
				}
			}
			if !seen {
				t.Fatal("successful handback did not clear recovery projection")
			}
		})
	}
}

func TestFailureEventReplaysAfterCheckpointRestart(t *testing.T) {
	f, w, a := failedWorker(t)
	a.State = stateWorking
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	var originalID string
	for _, ev := range w.pending {
		if ev.Kind == "worker.turn_failed" {
			originalID = ev.NativeID
		}
	}
	var st workerState
	if err := json.Unmarshal([]byte(f.thread), &st); err != nil {
		t.Fatal(err)
	}
	restarted := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement), st: st}
	if _, _, err := restarted.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range restarted.pending {
		if ev.Kind == "worker.turn_failed" {
			count++
			if ev.NativeID != originalID || originalID == "" {
				t.Fatal("failure replay changed identity")
			}
		}
	}
	if count != 1 {
		t.Fatalf("failure replay count=%d", count)
	}
}

func TestHandbackAdoptsOwnersReplacementProviderSession(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "pending handback"}[pending], func(t *testing.T) {
			_, w, a := failedWorker(t)
			w.st.SessionID = a.ProviderSession.ID
			w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryClosing}
			a.ProviderSession = &ProviderSession{ID: "owner-session"}
			a.Prompt = "owner's changes"
			w.spec.HandBack = true
			if pending {
				w.st.Send = "req_handback"
				w.st.SendText = harness.HandBack(w.spec)
				w.st.Said = said(w.st.SendText)
				w.st.Turn = time.Now().UnixMilli()
			}
			if err := w.begin(t.Context(), "codex"); err != nil {
				t.Fatal(err)
			}
			if w.st.SessionID != "owner-session" {
				t.Fatalf("session=%q", w.st.SessionID)
			}
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			if w.holding != "" {
				t.Fatalf("legitimate handback held: %s", w.holding)
			}
		})
	}
}

func TestUnconfirmedRootOutcomesWaitForChildrenThenRequireReconciliation(t *testing.T) {
	for _, outcome := range []string{"interruption", "superseded", "unconfirmed", "future_outcome"} {
		t.Run(outcome, func(t *testing.T) {
			f, w, a := failedWorker(t)
			a.MainAgent.Outcome = outcome
			if outcome == "interruption" {
				a.Interrupted = true
			}
			a.State = stateWorking
			if err := w.begin(t.Context(), "codex"); err != nil {
				t.Fatal(err)
			}
			if _, done, err := w.step(t.Context()); err != nil || done {
				t.Fatalf("background terminated: done=%v err=%v", done, err)
			}
			if w.holding != "" {
				t.Fatalf("invented owner takeover: %s", w.holding)
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
				t.Fatal("unconfirmed outcome mutated running background work")
			}
			a.State = stateDone
			if _, done, err := w.step(t.Context()); err == nil || done || !strings.Contains(err.Error(), "reconciliation") {
				t.Fatalf("done=%v err=%v", done, err)
			}
			if w.st.Recovery != nil || w.holding != "" {
				t.Fatal("unconfirmed outcome triggered recovery or owner takeover")
			}
		})
	}
}

func TestOldSendReceiptSettlesBeforeDistinctRecoveryPrompt(t *testing.T) {
	f, w, a := failedWorker(t)
	body, err := os.ReadFile(a.ProviderSession.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.ReplaceAll(string(body), "invalid_encrypted_content", "rate_limit_exceeded"))
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	w.st.Send = "old-request"
	w.st.SendText = a.Prompt
	if err := w.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	if w.st.Send != "" || w.st.Recovery != nil {
		t.Fatal("old receipt was not settled independently")
	}
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.orca.count("terminal send", nil) != 2 {
		t.Fatal("recovery did not send a distinct prompt after settling old receipt")
	}
	if !strings.HasPrefix(a.Prompt, "The previous root model turn failed.") || w.st.Recovery.Phase != recoveryRunning {
		t.Fatal("old prompt was mistaken for recovery")
	}
}

func TestPersistedRecoveryPhaseReplaysStableEvent(t *testing.T) {
	for _, phase := range []string{recoveryRunning, recoveryCompleted} {
		t.Run(phase, func(t *testing.T) {
			f, w, a := failedWorker(t)
			w.st.Recovery = &recoveryState{Attempts: 1, Phase: phase, Code: "rate_limit_exceeded", Terminal: w.st.Terminal, TurnID: "root-turn"}
			w.emitRecoveryPhase(t.Context())
			original := w.pending[len(w.pending)-1]
			if err := w.save(); err != nil {
				t.Fatal(err)
			}
			var st workerState
			if err := json.Unmarshal([]byte(f.thread), &st); err != nil {
				t.Fatal(err)
			}
			a.State = stateWorking
			restarted := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement), st: st}
			if err := restarted.begin(t.Context(), "codex"); err != nil {
				t.Fatal(err)
			}
			if len(restarted.pending) == 0 {
				t.Fatal("lost persisted phase notification")
			}
			replay := restarted.pending[0]
			if replay.Kind != original.Kind || replay.NativeID != original.NativeID || string(replay.Body) != string(original.Body) {
				t.Fatalf("phase fact changed across restart: %+v %+v", original, replay)
			}
		})
	}
}

func TestRecoveryBriefUsesImmutableProvenance(t *testing.T) {
	_, w, a := failedWorker(t)
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, Code: "rate_limit_exceeded", SessionID: a.ProviderSession.ID, TranscriptPath: a.ProviderSession.TranscriptPath, TurnID: "root-turn"}
	before := w.recoveryBrief()
	w.st.Recovery.Phase = recoveryRunning
	w.st.Recovery.ContinuationIssued = true
	w.st.Recovery.OriginalPrompt = "old prompt"
	if after := w.recoveryBrief(); after != before {
		t.Fatal("mutable checkpoint fields changed recovery prompt")
	}
}

func TestRecoveryRegressionHandbackAfterRootFailureBeforeRecovery(t *testing.T) {
	f, w, a := failedWorker(t)
	a.State = stateWorking
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.st.Recovery != nil {
		t.Fatal("unexpected recovery before background settled")
	}
	a.Prompt = "owner reconciles failed turn"
	w.spec.HandBack = true
	w.pending = nil
	if err := w.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	if a.Prompt != harness.HandBack(w.spec) {
		t.Fatal("not handed back")
	}
	if f.orca.count("terminal send", nil) != 1 {
		t.Fatal("not sent")
	}
	for _, ev := range w.pending {
		if ev.Kind == "worker.recovery_handed_back" {
			return
		}
	}
	t.Fatal("successful handback emits no fact to clear the root-failure execution projection")
}

func TestRecoveryRegressionRecoveryOwnershipConflictMustReportTakeover(t *testing.T) {
	f, saved, a := failedWorker(t)
	f.thread = mustState(saved.st)
	original := f.ws.opts.Client.Exec
	waits := 0
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		if len(args) > 1 && args[0] == "terminal" && args[1] == "wait" {
			waits++
			if waits == 2 {
				a.Prompt = "owner replacement turn"
				a.State = stateWorking
				a.MainAgent = &MainAgent{State: stateWorking, StateStartedAt: 300}
				return ok(map[string]any{"wait": map[string]any{"satisfied": true}}), nil
			}
		}
		return original(ctx, dir, args)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	spec := f.spec("implement")
	taken := false
	spec.OnTakeover = func(string) error { taken = true; cancel(); return nil }
	worker, _, drained := f.start(ctx, spec)
	_, err := worker.Wait()
	<-drained
	if !taken && errors.Is(err, harness.ErrHeld) {
		t.Fatal("ownership conflict returned as a worker error without reporting takeover")
	}
	if !taken {
		t.Fatalf("no takeover; err=%v", err)
	}
}

func TestRecoveryStartupOwnershipConflictReportsTakeover(t *testing.T) {
	f, saved, a := failedWorker(t)
	saved.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryClosing, SessionID: a.ProviderSession.ID, TurnAt: a.MainAgent.StateStartedAt, OriginalPrompt: saved.st.Said, Terminal: saved.st.Terminal, Pane: saved.st.Pane}
	f.thread = mustState(saved.st)
	a.ProviderSession.ID = "owner-session"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	spec := f.spec(protocol.RoleImplement)
	taken := false
	spec.OnTakeover = func(string) error { taken = true; cancel(); return nil }
	worker, _, drained := f.start(ctx, spec)
	_, _ = worker.Wait()
	<-drained
	if !taken {
		t.Fatal("startup conflict did not report takeover")
	}
	if f.orca.count("terminal close", nil) != 0 || f.orca.count("terminal send", nil) != 0 {
		t.Fatal("owner terminal mutated")
	}
}

func TestHandbackWithoutRecoveryReplaysCheckpoint(t *testing.T) {
	_, w, a := failedWorker(t)
	a.State = stateWorking
	if _, _, err := w.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.spec.HandBack = true
	w.pending = nil
	if err := w.begin(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	var first harness.WorkerEvent
	for _, ev := range w.pending {
		if ev.Kind == "worker.recovery_handed_back" {
			first = ev
		}
	}
	if first.Kind == "" {
		t.Fatal("missing successful handback checkpoint")
	}
	restarted := &worker{st: w.st}
	restarted.emitRecoveryPhase(t.Context())
	if len(restarted.pending) != 1 || restarted.pending[0].NativeID != first.NativeID || string(restarted.pending[0].Body) != string(first.Body) {
		t.Fatalf("unstable replay: %+v / %+v", first, restarted.pending)
	}
}

func TestNewFailureSupersedesUnpublishedHandback(t *testing.T) {
	for _, phase := range []string{"", "handed_back", recoveryRunning, recoveryCompleted} {
		t.Run(phase, func(t *testing.T) {
			_, w, a := failedWorker(t)
			w.st.HandbackEventID = "previous-handback"
			w.st.FailedTurn = 50
			if phase != "" {
				w.st.Recovery = &recoveryState{Attempts: 1, Phase: phase}
			}
			a.State = stateWorking
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			restarted := &worker{st: w.st}
			restarted.emitRecoveryPhase(t.Context())
			if len(restarted.pending) != 0 {
				t.Fatalf("replayed superseded phase: %+v", restarted.pending)
			}
			if restarted.st.Recovery != nil && restarted.st.Recovery.Attempts != 1 {
				t.Fatal("recovery budget lost")
			}
		})
	}
}

func TestProviderJSONRootFailureClassification(t *testing.T) {
	for _, tc := range []struct{ name, code, message, info, want string }{
		{"encrypted body", "", `{"error":{"code":"invalid_encrypted_content","message":"bad history"}}`, `"other"`, "invalid_encrypted_content"},
		{"codex wrapped body", "", `unexpected status 400 Bad Request: {"error":{"code":"invalid_encrypted_content"}}, url: https://example.invalid/responses, request id: example`, `"other"`, "invalid_encrypted_content"},
		{"rate body", "", `stream disconnected before completion: POST responses: 400 Bad Request {"error":{"code":"rate_limit_exceeded"}}`, `"other"`, "rate_limit_exceeded"},
		{"direct rate body", "", `{"code":"rate_limit_exceeded"}`, `"other"`, "rate_limit_exceeded"},
		{"unknown body", "", `{"error":{"code":"unknown_error"}}`, `"other"`, ""},
		{"unrelated nesting", "", `{"metadata":{"code":"rate_limit_exceeded"}}`, `"other"`, ""},
		{"malformed body", "", `{"error":{"code":"rate_limit_exceeded"`, `"other"`, ""},
		{"auth body overrides prose", "", `{"error":{"code":"unauthorized","message":"code: rate_limit_exceeded; previous error"}}`, `"other"`, "unauthorized"},
		{"quota body overrides typed retry", "", `{"error":{"code":"insufficient_quota"}}`, `"rate_limit_exceeded"`, "insufficient_quota"},
		{"top auth overrides body", "unauthorized", `{"error":{"code":"invalid_encrypted_content"}}`, `"other"`, "unauthorized"},
		{"typed auth overrides body", "", `{"error":{"code":"invalid_encrypted_content"}}`, `"unauthorized"`, "unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := recoveryTranscript(t, "")
			body, err := os.ReadFile(session.TranscriptPath)
			if err != nil {
				t.Fatal(err)
			}
			errorJSON, err := json.Marshal(map[string]any{"code": tc.code, "message": tc.message, "codex_error_info": json.RawMessage(tc.info)})
			if err != nil {
				t.Fatal(err)
			}
			body = []byte(strings.ReplaceAll(string(body), `{"message":"stream disconnected: code: invalid_encrypted_content; message: encrypted history failed","codex_error_info":"other"}`, string(errorJSON)))
			if err := os.WriteFile(session.TranscriptPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			fact, err := readFailedTurn(t.Context(), session, 1)
			if err != nil || fact.Code != tc.want {
				t.Fatalf("fact=%+v err=%v want=%q", fact, err, tc.want)
			}
		})
	}
}
