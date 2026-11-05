package orca

import (
	"context"
	"errors"
	"time"

	"encoding/json"
	"github.com/GhostFlying/tend/internal/harness"
	"os"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func traexEndingFixture(t *testing.T) (*workerFixture, *worker, *Agent, string) {
	t.Helper()
	f, w, a := managedCodexFixture(t)
	f.ws.opts.Agent = protocol.AgentTraex
	previous := f.ws.opts.Client.Exec
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		raw, err := previous(ctx, dir, args)
		if err != nil {
			return raw, err
		}
		var reply map[string]any
		if json.Unmarshal(raw, &reply) == nil && len(args) > 0 && args[0] == "status" {
			reply["result"].(map[string]any)["runtime"].(map[string]any)["runtimeId"] = "runtime-root"
			return json.Marshal(reply)
		}
		return raw, err
	}

	a.AgentType = "traex"
	w.st.SessionID = a.ProviderSession.ID
	w.st.RootRuntimeID = "runtime-root"
	w.st.RootLaunchID = "native-launch"
	w.st.RootObservedTurnID = "turn-original"
	w.st.RootObservedForTurn = w.st.Turn
	w.st.RootObservationRequired = true
	a.RootTurn = &RootTurn{Version: 1, Source: "native-api", PaneKey: w.st.Pane, LaunchID: w.st.RootLaunchID, SessionID: w.st.SessionID, TurnID: "turn-original", StartedAt: 2000, ObservedAt: 6000, State: "unknown", Operations: "unknown", ErrorCode: "ending_unconfirmed"}
	head := rootRecord(900, "session_meta", map[string]any{"id": w.st.SessionID, "originator": "traex-tui", "cli_version": "0.213.1", "cwd": f.dir})
	body := head + rootStart(2000, "turn-original", w.st.Said)
	return f, w, a, body
}

func TestTraexRootSupplementsExactAbortWithoutProviderAction(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	body += rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.observeCodexRoot(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if a.RootTurn.State != "cancelled" || a.RootTurn.CompletedAt != 5000 || a.RootTurn.Source != transcriptRootSource || a.RootTurn.LaunchID != "native-launch" || a.RootTurn.Operations != "settled" {
		t.Fatalf("root=%+v", a.RootTurn)
	}
	if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("observation mutated native execution")
	}
}

func TestTraexRootSupplementPreservesPendingOperations(t *testing.T) {
	_, w, a, body := traexEndingFixture(t)
	body += rootRecord(3000, "response_item", map[string]any{"type": "function_call", "call_id": "pending", "name": "exec_command"}) + rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.observeCodexRoot(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if a.RootTurn.State != "cancelled" || a.RootTurn.Operations != "pending" {
		t.Fatalf("root=%+v", a.RootTurn)
	}
}

func TestTraexRootSupplementRejectsIncompleteOrDifferentEvidence(t *testing.T) {
	for _, name := range []string{"eof", "other session", "other turn", "no turn id", "other cwd", "other provider", "other input", "bad clock", "other launch", "api terminal"} {
		t.Run(name, func(t *testing.T) {
			_, w, a, body := traexEndingFixture(t)
			end := rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
			switch name {
			case "eof":
				end = ""
			case "other session":
				body = strings.Replace(body, `"id":"session-root"`, `"id":"other"`, 1)
			case "other turn":
				end = strings.Replace(end, "turn-original", "other", 1)
			case "no turn id":
				end = rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted"})
			case "other cwd":
				body = strings.Replace(body, w.spec.Dir, "/different", 1)
			case "other provider":
				body = strings.Replace(body, "traex-tui", "codex-tui", 1)
			case "other input":
				body = strings.Replace(body, w.st.Said, "unmanaged input", 1)
			case "bad clock":
				end = rootRecord(1000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
			case "other launch":
				a.RootTurn.LaunchID = "different"
			case "api terminal":
				a.RootTurn.State = "completed"
				a.RootTurn.CompletedAt = 4000
			}
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body+end), 0600); err != nil {
				t.Fatal(err)
			}
			before := *a.RootTurn
			_ = w.observeCodexRoot(t.Context(), a)
			if *a.RootTurn != before {
				t.Fatalf("unsafe replacement before=%+v after=%+v", before, a.RootTurn)
			}
		})
	}
}

func TestTraexRootSupplementDoesNotResumeCancelledTurnAcrossRestart(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	w.st.Interrupted = true
	body += rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, done, err := w.step(t.Context())
		if err != nil || done || got.RootTurn.State != "cancelled" || got.RootTurn.CompletedAt != 5000 {
			t.Fatalf("done=%v err=%v root=%+v", done, err, got.RootTurn)
		}
		if err = w.save(); err != nil {
			t.Fatal(err)
		}
		var saved workerState
		if err = json.Unmarshal([]byte(f.thread), &saved); err != nil {
			t.Fatal(err)
		}
		w.st = saved
	}
	if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("terminal observation authorized continuation")
	}
}

func TestTraexRootSupplementKeepsUnknownOperationsAndLiveChildren(t *testing.T) {
	for _, kind := range []string{"future operation", "live child", "partial record"} {
		t.Run(kind, func(t *testing.T) {
			_, w, a, body := traexEndingFixture(t)
			body += rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
			want := "unknown"
			switch kind {
			case "future operation":
				body += rootRecord(5001, "response_item", map[string]any{"type": "future_operation"})
			case "live child":
				a.SubagentObservation = "live"
				a.Children = []ChildWork{{State: "running", Membership: "live"}}
				want = "pending"
			case "partial record":
				body += `{"type":`
			}
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := w.observeCodexRoot(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if a.RootTurn.Operations != want {
				t.Fatalf("root=%+v", a.RootTurn)
			}
			if kind == "partial record" && a.RootTurn.State != "unknown" {
				t.Fatal("partial record certified terminal")
			}
		})
	}
}

func TestTraexRootSupplementCompletedAndFailedRequireRealTerminal(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "completed"
		if failure {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			f, w, a, body := traexEndingFixture(t)
			payload := map[string]any{"type": "task_complete", "turn_id": "turn-original"}
			if failure {
				payload["error"] = map[string]any{"codex_error_info": "response_stream_disconnected"}
			}
			body += rootRecord(5000, "event_msg", payload)
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			got, done, err := w.step(t.Context())
			if err != nil || done == failure || got.RootTurn.State != name || got.RootTurn.CompletedAt != 5000 {
				t.Fatalf("done=%v err=%v root=%+v", done, err, got.RootTurn)
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("supplement replayed native work")
			}
		})
	}
}

func rootAppend(at int64, items ...any) string {
	return rootRecord(at, "history_mutation", map[string]any{"version": 1, "turn_id": "turn-original", "operation": "append", "items": items})
}
func TestTraexRootSupplementReadsHistoryMutationInputAndOperations(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "input", true: "pending tool"}[pending], func(t *testing.T) {
			_, w, a, body := traexEndingFixture(t)
			if !pending {
				body = strings.SplitAfterN(body, "\n", 2)[0] + rootRecord(2000, "event_msg", map[string]any{"type": "task_started", "turn_id": "turn-original"}) + rootAppend(2001, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": w.st.Said}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"user.text"}}})
			} else {
				body += rootAppend(3000, map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "outer", "input": "command()"})
			}
			body += rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := w.observeCodexRoot(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			want := "settled"
			if pending {
				want = "pending"
			}
			if !a.transcriptSupplement || a.RootTurn.State != "cancelled" || a.RootTurn.Operations != want {
				t.Fatalf("root=%+v supplemented=%v", a.RootTurn, a.transcriptSupplement)
			}
		})
	}
}
func TestTraexRootSupplementUnknownToolOutputIsNotSettlement(t *testing.T) {
	for _, kind := range []string{"function_call", "custom_tool_call"} {
		t.Run(kind, func(t *testing.T) {
			_, w, a, body := traexEndingFixture(t)
			body += rootRecord(3000, "response_item", map[string]any{"type": kind, "name": "exec", "call_id": "opaque"}) + rootRecord(3100, "response_item", map[string]any{"type": kind + "_output", "call_id": "opaque", "output": "opaque receipt"}) + rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := w.observeCodexRoot(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if a.RootTurn.Operations != "unknown" {
				t.Fatalf("opaque output settled operations: %+v", a.RootTurn)
			}
		})
	}
}
func waitTraexWorker(t *testing.T, w harness.Worker, cancel context.CancelCauseFunc) {
	t.Helper()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range w.Events() {
		}
	}()
	finished := make(chan error, 1)
	go func() { _, err := w.Wait(); finished <- err }()
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("worker: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("supplemented completed worker did not finish without a new prompt")
		cancel(harness.ErrHeld)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("worker failed to stop")
		}
	}
	<-drained
}
func TestTraexRootSupplementRealStartAcceptNeverSends(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	body += rootEnd(5000, "turn-original")
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	original := mustState(w.st)
	for range 2 {
		ctx, cancel := context.WithCancelCause(t.Context())
		spec := w.spec
		spec.Role = protocol.RoleAccept
		spec.Thread = original
		started, err := f.ws.Start(ctx, spec)
		if err != nil {
			cancel(harness.ErrHeld)
			t.Fatal(err)
		}
		waitTraexWorker(t, started, cancel)
		cancel(harness.ErrHeld)
		if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
			t.Fatal("Start/begin or missing-verdict path sent native input")
		}
	}
}
func TestTraexRootSupplementRunMissingVerdictNeverSends(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	body += rootEnd(5000, "turn-original")
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	w.spec.Role = protocol.RoleAccept
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(harness.ErrHeld)
	if err := w.openBridge(ctx); err != nil {
		t.Fatal(err)
	}
	w.events = make(chan harness.WorkerEvent)
	w.done = make(chan struct{})
	go w.run(ctx)
	waitTraexWorker(t, w, cancel)
	if f.orca.count("terminal send", nil) != 0 {
		t.Fatal("missing verdict sent a reminder from transcript evidence")
	}
}
func TestTraexRootSupplementFailedPollWaitsForCancellation(t *testing.T) {
	f, w, a, body := traexEndingFixture(t)
	body += rootRecord(5000, "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-original", "error": map[string]any{"codex_error_info": "other"}})
	if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	f.ws.opts.Poll = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{}, 1)
	prev := f.ws.opts.Client.Exec
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		if len(args) > 1 && args[0] == "terminal" && args[1] == "wait" {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		return prev(ctx, dir, args)
	}
	result := make(chan error, 1)
	go func() { _, _, err := w.step(ctx); result <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no native wait observed")
	}
	select {
	case <-result:
		t.Fatal("failed supplement returned immediately instead of respecting Poll")
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Poll did not cancel")
	}
}

func TestTraexRootSupplementRejectsRewritesAndAppendedOwnerInput(t *testing.T) {
	for _, kind := range []string{"rewrite", "unknown mutation", "new user", "bookkeeping"} {
		t.Run(kind, func(t *testing.T) {
			_, w, a, body := traexEndingFixture(t)
			switch kind {
			case "rewrite":
				body += rootRecord(3000, "history_mutation", map[string]any{"operation": "replace", "items": []any{}})
			case "unknown mutation":
				body += rootRecord(3000, "history_mutation", map[string]any{"operation": "future", "items": []any{}})
			case "new user":
				body += rootAppend(3000, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "new owner input"}}})
			case "bookkeeping":
				body += rootAppend(3000, map[string]any{"type": "trae_extra_info"})
			}
			body += rootRecord(5000, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn-original"})
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			err := w.observeCodexRoot(t.Context(), a)
			if kind == "new user" {
				if !errors.Is(err, harness.ErrHeld) {
					t.Fatal("new input not rejected", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "unknown"
			if kind == "bookkeeping" {
				want = "settled"
			}
			if a.RootTurn.Operations != want {
				t.Fatalf("root=%+v", a.RootTurn)
			}
		})
	}
}
func TestTraexRootSupplementDoesNotAuthorizeRecoveryEntrypoints(t *testing.T) {
	for _, kind := range []string{"recover", "continue", "close", "recheck"} {
		t.Run(kind, func(t *testing.T) {
			f, w, a, body := traexEndingFixture(t)
			// Keep the daemon fixture's weak API row unchanged; supplementation is
			// local to the decoded observation, never written back to Orca.
			observed := *a
			root := *a.RootTurn
			observed.RootTurn = &root
			a = &observed
			body += rootRecord(5000, "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-original", "error": map[string]any{"codex_error_info": "response_stream_disconnected"}})
			if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := w.observeCodexRoot(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			w.st.Recovery = &recoveryState{Phase: recoveryContinuing, SessionID: w.st.SessionID, TurnID: w.st.RootObservedTurnID, TurnAt: 5000, OriginalPrompt: w.st.Said, Terminal: w.st.Terminal, Pane: w.st.Pane}
			var err error
			switch kind {
			case "recover":
				err = w.recoverFailure(t.Context(), *a)
			case "continue":
				err = w.continueObservedSession(t.Context(), *a, true)
			case "close":
				w.st.Recovery.Phase = recoveryClosing
				err = w.closeFailedSession(t.Context())
			case "recheck":
				err = w.recheckFailedTurn(t.Context(), Wait{Satisfied: true})
			}
			if !errors.Is(err, errRecoveryUncertain) {
				t.Fatal("recovery did not retain uncertainty", err)
			}
			if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("transcript evidence authorized recovery effects")
			}
		})
	}
}

func TestTraexRootSupplementRecoveryPhaseSurvivesWeakToStrongStartup(t *testing.T) {
	for _, phase := range []string{recoveryClosing, recoveryContinuing} {
		for _, lostClose := range []bool{false, true} {
			if phase == recoveryContinuing && lostClose {
				continue
			}
			name := phase
			if lostClose {
				name += "_lost_close_reply"
			}
			t.Run(name, func(t *testing.T) {
				f, saved, a, body := traexEndingFixture(t)
				a.Prompt = saved.st.Said
				a.MainAgent = &MainAgent{State: stateDone, Outcome: "failure", StateStartedAt: 5000}
				a.StateStartedAt = 5000
				body += rootRecord(5000, "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-original", "error": map[string]any{"codex_error_info": "response_stream_disconnected"}})
				if err := os.WriteFile(a.ProviderSession.TranscriptPath, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				code := "response_stream_disconnected"
				if phase == recoveryClosing {
					code = "invalid_encrypted_content"
				}
				saved.st.Recovery = &recoveryState{Attempts: 1, Phase: phase, Code: code, SessionID: saved.st.SessionID, TranscriptPath: a.ProviderSession.TranscriptPath, TurnID: "turn-original", Terminal: saved.st.Terminal, Pane: saved.st.Pane, TurnAt: 5000, OriginalPrompt: saved.st.Said, LaunchID: saved.st.RootLaunchID, RuntimeID: saved.st.RootRuntimeID}
				spec := saved.spec
				spec.Thread = mustState(saved.st)
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(harness.ErrHeld)
				previous := f.ws.opts.Client.Exec
				reads, closes := 0, 0
				effect := make(chan struct{}, 1)
				f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
					if len(args) > 1 && args[0] == "worktree" && args[1] == "ps" {
						reads++
						if reads == 2 {
							f.orca.mu.Lock()
							a.RootTurn.State, a.RootTurn.Operations, a.RootTurn.ErrorCode = "failed", "settled", code
							a.RootTurn.CompletedAt = 5000
							f.orca.mu.Unlock()
						}
					}
					raw, err := previous(ctx, dir, args)
					if len(args) > 1 && args[0] == "terminal" && args[1] == "close" {
						closes++
						if lostClose && closes == 1 && err == nil {
							return nil, errors.New("original close reply lost after completion")
						}
					}
					if len(args) > 1 && (args[0] == "agent" && args[1] == "launch" || args[0] == "terminal" && args[1] == "send") {
						select {
						case effect <- struct{}{}:
						default:
						}
					}
					return raw, err
				}
				started, err := f.ws.Start(ctx, spec)
				if err != nil {
					t.Fatal(err)
				}
				drained := make(chan struct{})
				go func() {
					defer close(drained)
					for range started.Events() {
					}
				}()
				select {
				case <-effect:
				case <-time.After(time.Second):
					t.Error("original recovery phase never resumed")
				}
				cancel(harness.ErrHeld)
				_, _ = started.Wait()
				<-drained
				sends, launches := f.orca.count("terminal send", nil), f.orca.count("agent launch", nil)
				if phase == recoveryClosing {
					if closes != 1 || sends != 0 || launches != 1 {
						t.Fatalf("closing changed strategy: closes=%d sends=%d launches=%d", closes, sends, launches)
					}
					if f.orca.count("terminal close", func(c []string) bool { return flag(c, "--terminal") == saved.st.Terminal }) != 1 {
						t.Fatal("close lost original terminal identity")
					}
				} else if closes != 0 || sends != 1 || launches != 0 {
					t.Fatalf("continuing changed strategy: closes=%d sends=%d launches=%d", closes, sends, launches)
				}
			})
		}
	}
}
