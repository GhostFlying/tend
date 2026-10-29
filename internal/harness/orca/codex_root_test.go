package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/protocol"
)

// These records preserve the two observed orderings without machine identities,
// provider payloads, credentials or a special-case background prompt.
func rootRecord(at int64, kind string, payload map[string]any) string {
	b, _ := json.Marshal(map[string]any{"timestamp": time.UnixMilli(at).UTC().Format(time.RFC3339Nano), "type": kind, "payload": payload})
	return string(b) + "\n"
}
func rootStart(at int64, turn, prompt string) string {
	return rootRecord(at, "event_msg", map[string]any{"type": "task_started", "turn_id": turn}) + rootRecord(at+1, "response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": prompt}}})
}
func rootEnd(at int64, turn string) string {
	return rootRecord(at, "event_msg", map[string]any{"type": "task_complete", "turn_id": turn, "error": nil, "last_agent_message": "original native answer"})
}
func writeRoot(t *testing.T, session ProviderSession, body string) {
	t.Helper()
	if err := os.WriteFile(session.TranscriptPath, []byte(rootRecord(900, "session_meta", map[string]any{"id": session.ID})+body), 0600); err != nil {
		t.Fatal(err)
	}
}
func managedCodexFixture(t *testing.T) (*workerFixture, *worker, *Agent) {
	t.Helper()
	f := newWorkerFixture(t)
	f.ws.opts.Agent = protocol.AgentCodex
	term := Terminal{Handle: "term_root", WorktreeID: f.orca.wt, TabID: "tab", LeafID: "root"}
	f.orca.terms = []Terminal{term}
	session := ProviderSession{ID: "session-root", TranscriptPath: filepath.Join(t.TempDir(), "rollout.jsonl")}
	a := &Agent{RuntimeID: "runtime-root", AgentType: "codex", PaneKey: term.PaneKey(), ProviderSession: &session, State: stateDone, StateStartedAt: 6000, UpdatedAt: 6000, Prompt: "a background prompt", MainAgent: &MainAgent{State: stateDone, StateStartedAt: 6000}, LastAssistantMessage: "original answer"}
	f.orca.agents[a.PaneKey] = a
	previous := f.ws.opts.Client.Exec
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		body, err := previous(ctx, dir, args)
		if err != nil {
			return body, err
		}
		var response map[string]any
		if err = json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		response["_meta"] = map[string]string{"runtimeId": "runtime-root"}
		return json.Marshal(response)
	}
	w := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement), st: workerState{Worktree: f.orca.wt, Terminal: term.Handle, Pane: term.PaneKey(), Marker: "launch-root", Asked: 1000, Turn: 1000, Said: "[tend run run_1, launch launch-root] original task", RootLegacyRuntimeID: "runtime-root"}}
	return f, w, a
}
func TestCodexRootBindsOriginalLaunchBeforeAggregateTakeover(t *testing.T) {
	f, w, a := managedCodexFixture(t)
	writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+rootEnd(5000, "turn-original")+rootRecord(7000, "event_msg", map[string]any{"type": "token_count"}))
	got, done, err := w.step(t.Context())
	if err != nil || !done || w.holding != "" || w.st.SessionID != "session-root" || w.st.RootObservedTurnID != "turn-original" || got.Prompt != w.st.Said || got.LastAssistantMessage != "original native answer" {
		t.Fatalf("done=%v err=%v held=%s state=%+v", done, err, w.holding, w.st)
	}
	if f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
		t.Fatal("observation replayed provider input")
	}
	// A restart consumes the journaled identity, not the background pane prompt.
	var restored workerState
	if err := json.Unmarshal([]byte(f.thread), &restored); err != nil {
		t.Fatal(err)
	}
	w.st = restored
	if _, done, err := w.step(t.Context()); err != nil || !done {
		t.Fatalf("restart done=%v err=%v", done, err)
	}
	if err := f.ws.ReleaseHistorical(t.Context(), "run_1", mustState(w.st), false, 5500); err != nil {
		t.Fatal(err)
	}
	if len(f.orca.terms) != 0 {
		t.Fatal("completed managed terminal was not released")
	}
}
func TestCodexRootEarlyAggregateDonePreservesBridgeAndResult(t *testing.T) {
	f, w, a := managedCodexFixture(t)
	writeRoot(t, *a.ProviderSession, "")
	if err := w.openBridge(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer w.closeBridge(t.Context())
	if _, done, err := w.step(t.Context()); err != nil || done || w.holding != "" {
		t.Fatalf("early done=%v err=%v held=%s", done, err, w.holding)
	}
	if err := f.ws.ReleaseHistorical(t.Context(), "run_1", mustState(w.st), false, 1500); err == nil || errors.Is(err, harness.ErrHeld) {
		t.Fatalf("unknown root must preserve terminal without takeover: %v", err)
	}
	if len(f.orca.terms) != 1 || f.orca.count("terminal send", nil) != 0 {
		t.Fatal("unknown observation changed the terminal")
	}
	// The original native turn appears later. Its original MCP bridge still
	// accepts the result, rather than a replacement launch or synthetic result.
	writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said))
	if _, done, err := w.step(t.Context()); err != nil || done {
		t.Fatalf("running done=%v err=%v", done, err)
	}
	state, err := bridge.StatePath(t.Context(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_result","arguments":{"report":{"body":"original result"}}}}` + "\n"
	var output bytes.Buffer
	if err := bridge.ServeMCP(t.Context(), state, "run_1", strings.NewReader(input), &output); err != nil || strings.Contains(output.String(), "isError") {
		t.Fatalf("result bridge: %v %s", err, output.String())
	}
	writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+rootEnd(5000, "turn-original"))
	if _, done, err := w.step(t.Context()); err != nil || !done {
		t.Fatalf("completed done=%v err=%v", done, err)
	}
	result := w.results.Result()
	if result == nil || result.Report == nil || result.Report.Body != "original result" {
		t.Fatalf("original result not retained: %+v", result)
	}
	if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal send", nil) != 0 {
		t.Fatal("observation replayed provider input")
	}
}
func TestCodexRootRejectsOwnerInputIncludingRepeatedPrompt(t *testing.T) {
	for _, prompt := range []string{"owner investigation", "[tend run run_1, launch launch-root] original task"} {
		t.Run(prompt, func(t *testing.T) {
			f, w, a := managedCodexFixture(t)
			writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+rootEnd(3000, "turn-original")+rootStart(4000, "turn-owner", prompt)+rootEnd(5000, "turn-owner"))
			if _, _, err := w.agent(t.Context()); !errors.Is(err, harness.ErrHeld) {
				t.Fatalf("unmanaged root turn adopted: %v", err)
			}
			if err := f.ws.ReleaseHistorical(t.Context(), "run_1", mustState(w.st), false, 5500); !errors.Is(err, harness.ErrHeld) {
				t.Fatalf("owner terminal release=%v", err)
			}
			if len(f.orca.terms) != 1 {
				t.Fatal("owner terminal closed")
			}
		})
	}
}
func TestCodexRootAcceptsManagedSendAsNextTurn(t *testing.T) {
	f, w, a := managedCodexFixture(t)
	original := w.st.Said
	writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", original)+rootEnd(3000, "turn-original"))
	if err := w.observeCodexRoot(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := w.bindRootObservation(a); err != nil {
		t.Fatal(err)
	}
	f.ws.opts.Now = func() time.Time { return time.UnixMilli(3500) }
	if err := w.send(t.Context(), "continue the managed task"); err != nil {
		t.Fatal(err)
	}
	if f.orca.count("terminal send", nil) != 1 || w.st.Send != "" {
		t.Fatal("managed send receipt was not settled exactly once")
	}
	a.RootTurn = nil
	writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", original)+rootEnd(3000, "turn-original")+rootStart(4000, "turn-next", w.st.Said)+rootEnd(5000, "turn-next"))
	if err := w.observeCodexRoot(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := w.bindRootObservation(a); err != nil {
		t.Fatal(err)
	}
	if !answered(*a, w.st.Turn) || w.st.RootObservedTurnID != "turn-next" {
		t.Fatal("managed continuation was not bound")
	}
}
func TestCodexRootUnknownAndPendingNeverComplete(t *testing.T) {
	for _, kind := range []string{"missing", "partial", "wrong-session", "wrong-launch", "before-turn", "no-end", "pending-tool", "settled-tool"} {
		t.Run(kind, func(t *testing.T) {
			_, w, a := managedCodexFixture(t)
			session := *a.ProviderSession
			body := rootStart(2000, "turn-original", w.st.Said)
			switch kind {
			case "wrong-launch":
				body = rootStart(2000, "turn-original", "[tend run run_1, launch foreign] other task")
			case "before-turn":
				w.st.Turn = 7000
			}
			if kind == "pending-tool" || kind == "settled-tool" {
				body += rootRecord(2500, "response_item", map[string]any{"type": "custom_tool_call", "call_id": "operation"})
			}
			if kind == "settled-tool" {
				body += rootRecord(2600, "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "operation"})
			}
			if kind != "no-end" {
				body += rootEnd(5000, "turn-original")
			}
			writeRoot(t, session, body)
			switch kind {
			case "missing":
				if err := os.Remove(session.TranscriptPath); err != nil {
					t.Fatal(err)
				}
			case "partial":
				file, err := os.OpenFile(session.TranscriptPath, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString(`{"type":`)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "wrong-session":
				a.ProviderSession.ID = "different-session"
			}
			if err := w.observeCodexRoot(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if answered(*a, w.st.Turn) != (kind == "settled-tool") {
				t.Fatalf("completion from %s: %+v", kind, a.RootTurn)
			}
		})
	}
}
func TestCodexRootRuntimeAndSessionChangesHold(t *testing.T) {
	for _, kind := range []string{"runtime", "session"} {
		t.Run(kind, func(t *testing.T) {
			_, w, a := managedCodexFixture(t)
			a.RuntimeID = "runtime-root"
			if kind == "runtime" {
				a.RuntimeID = "different-runtime"
			} else {
				w.st.SessionID = "different-session"
			}
			if err := w.observeCodexRoot(t.Context(), a); !errors.Is(err, harness.ErrHeld) {
				t.Fatalf("changed %s adopted: %v", kind, err)
			}
		})
	}
}

func TestCodexRootHandbackBindsOnlyItsNewManagedInput(t *testing.T) {
	_, w, a := managedCodexFixture(t)
	w.st.SessionID = "old-session"
	a.ProviderSession.ID = "adopted-session"
	a.RuntimeID = "adopted-runtime"
	w.spec.HandBack = true
	writeRoot(t, *a.ProviderSession, rootStart(2000, "owner-turn", "owner investigation")+rootEnd(3000, "owner-turn"))
	if err := w.observeCodexRoot(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := w.bindRootObservation(a); err != nil {
		t.Fatal(err)
	}
	if answered(*a, w.st.Turn) || !w.st.RootRebindPending {
		t.Fatal("handback adopted an old owner turn as managed completion")
	}
	// begin() adopts the exact returned session and sends one new handback prompt.
	w.st.SessionID = a.ProviderSession.ID
	w.st.Turn, w.st.Said = 3500, "hand back to managed execution"
	w.spec.HandBack = false
	a.RootTurn = nil
	writeRoot(t, *a.ProviderSession, rootStart(2000, "owner-turn", "owner investigation")+rootEnd(3000, "owner-turn")+rootStart(4000, "managed-handback", w.st.Said)+rootEnd(5000, "managed-handback"))
	if err := w.observeCodexRoot(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := w.bindRootObservation(a); err != nil {
		t.Fatal(err)
	}
	if !answered(*a, w.st.Turn) || w.st.RootRebindPending || w.st.RootRuntimeID != "adopted-runtime" {
		t.Fatal("exact handback send was not bound")
	}
	a.RootTurn = nil
	if err := w.observeCodexRoot(t.Context(), a); err != nil {
		t.Fatal("adopted session was lost on the next observation", err)
	}
}

func TestCodexRootTerminalAndPaneChangesHold(t *testing.T) {
	for _, kind := range []string{"pane", "handle", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			f, w, _ := managedCodexFixture(t)
			switch kind {
			case "pane":
				f.orca.terms[0].LeafID = "replacement"
			case "handle":
				f.orca.terms[0].Handle = "replacement"
			case "worktree":
				f.orca.terms[0].WorktreeID = "replacement"
			}
			if _, _, err := w.agent(t.Context()); !errors.Is(err, harness.ErrHeld) {
				t.Fatalf("changed %s adopted: %v", kind, err)
			}
		})
	}
}

func TestCodexRootMissingRowTypeStillRequiresNativeEvidence(t *testing.T) {
	for _, kind := range []string{"known-root", "missing-session", "missing-runtime", "missing-transcript"} {
		t.Run(kind, func(t *testing.T) {
			_, w, a := managedCodexFixture(t)
			a.AgentType = ""
			writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+rootEnd(5000, "turn-original"))
			switch kind {
			case "missing-session":
				a.ProviderSession = nil
			case "missing-runtime":
				a.RuntimeID = ""
			case "missing-transcript":
				a.ProviderSession.TranscriptPath = ""
			}
			if err := w.observeCodexRoot(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if answered(*a, w.st.Turn) != (kind == "known-root") {
				t.Fatalf("configured Codex used aggregate completion for %s", kind)
			}
			if kind != "known-root" && (a.RootTurn == nil || a.RootTurn.State != "unknown" || w.takenOver(*a) != "") {
				t.Fatal("missing root did not preserve unknown managed execution")
			}
		})
	}
}

func TestCodexRootPreservesKnownLiveChildrenUntilSettled(t *testing.T) {
	for _, child := range []ChildWork{{State: "working", Membership: "active"}, {State: "done", Membership: "active"}} {
		t.Run(child.State, func(t *testing.T) {
			f, w, a := managedCodexFixture(t)
			a.Children = []ChildWork{child}
			a.SubagentObservation = "live"
			writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+rootRecord(3000, "response_item", map[string]any{"type": "custom_tool_call", "call_id": "command"})+rootRecord(4000, "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "command"})+rootEnd(5000, "turn-original"))
			got, done, err := w.step(t.Context())
			if err != nil || done || got.RootTurn == nil || got.RootTurn.Operations != "pending" || got.SubagentObservation != "live" || len(got.Children) != 1 || got.Children[0] != child || backgroundSettled(got, Wait{Satisfied: true}) {
				t.Fatalf("known live child was lost or completed early: done=%v err=%v children=%+v observation=%q root=%+v", done, err, got.Children, got.SubagentObservation, got.RootTurn)
			}
			if err := f.ws.ReleaseHistorical(t.Context(), "run_1", mustState(w.st), false, 5500); err == nil || errors.Is(err, harness.ErrHeld) {
				t.Fatalf("live child must preserve original terminal without reporting takeover: %v", err)
			}
			if len(f.orca.terms) != 1 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("root completion stopped its live child")
			}
			a.Children[0] = ChildWork{State: "done", Membership: "settled"}
			got, done, err = w.step(t.Context())
			if err != nil || !done || got.RootTurn.Operations != "settled" || len(got.Children) != 1 {
				t.Fatalf("settled live inventory did not complete: done=%v err=%v root=%+v", done, err, got.RootTurn)
			}
			if err := f.ws.ReleaseHistorical(t.Context(), "run_1", mustState(w.st), false, 5500); err != nil {
				t.Fatal(err)
			}
			if len(f.orca.terms) != 0 {
				t.Fatal("settled original terminal was not released")
			}
		})
	}
}

func TestCodexRootHostContextIsNotOwnerInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kinds    []string
		wantHeld bool
	}{
		{name: "subagent context", kinds: []string{"multi_agent.subagent_notification"}},
		{name: "host context", kinds: []string{"context.environment"}},
		{name: "real user", kinds: []string{"user.text"}, wantHeld: true},
		{name: "unknown classification", kinds: []string{"unknown"}, wantHeld: true},
		{name: "missing classification", wantHeld: true},
		{name: "incomplete classification", kinds: []string{"multi_agent.subagent_notification", "context.environment"}, wantHeld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, w, a := managedCodexFixture(t)
			fragment := rootRecord(2500, "response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "arbitrary context or user text"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": tc.kinds}})
			writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+fragment+rootEnd(5000, "turn-original"))
			_, _, err := w.agent(t.Context())
			if errors.Is(err, harness.ErrHeld) != tc.wantHeld || err != nil && !errors.Is(err, harness.ErrHeld) {
				t.Fatalf("classification held=%v want=%v error=%v", errors.Is(err, harness.ErrHeld), tc.wantHeld, err)
			}
		})
	}
}

func TestCodexRootSavedRecoveryReceiptWaitsForNewNativeTurn(t *testing.T) {
	f, w, a := managedCodexFixture(t)
	original := w.st.Said
	oldRoot := rootStart(2000, "turn-original", original) + rootRecord(3000, "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-original", "error": map[string]any{"code": "response_stream_disconnected"}})
	writeRoot(t, *a.ProviderSession, oldRoot)
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.st.Recovery = &recoveryState{Attempts: 1, Phase: recoveryContinuing, Code: "response_stream_disconnected", SessionID: a.ProviderSession.ID, TurnID: "turn-original", TurnAt: 3000, OriginalPrompt: original, LaunchID: w.st.Marker, RuntimeID: "runtime-root", Terminal: w.st.Terminal, Pane: w.st.Pane, ContinuationIssued: true}
	w.st.Turn, w.st.Said = 3500, said(w.recoveryBrief())
	w.st.Send, w.st.SendText = "saved-request", w.recoveryBrief()
	if err := w.continueFailedSession(t.Context()); err != nil {
		t.Fatalf("original receipt was not reconciled: %v", err)
	}
	f.orca.mu.Lock()
	count := 0
	for _, args := range f.orca.calls {
		if len(args) > 1 && args[0] == "terminal" && args[1] == "send" {
			count++
			if flag(args, "--retry-request") != "saved-request" || flag(args, "--text") != w.recoveryBrief() {
				t.Error("receipt replay lost original identity/text")
			}
		}
	}
	f.orca.mu.Unlock()
	if count != 1 || w.st.Send != "" || w.st.Recovery.Attempts != 1 || w.st.Recovery.Phase != recoveryRunning {
		t.Fatalf("wrong receipt settlement count=%d state=%+v", count, w.st.Recovery)
	}
	got, done, err := w.step(t.Context())
	if err != nil || done || validRootTurn(got, w.st.Turn) {
		t.Fatalf("old root became completion of the new send: done=%v err=%v", done, err)
	}
	writeRoot(t, *a.ProviderSession, oldRoot+rootStart(4000, "turn-continuation", w.recoveryBrief())+rootEnd(5000, "turn-continuation"))
	if _, done, err := w.step(t.Context()); err != nil || !done {
		t.Fatalf("new managed native turn did not complete: done=%v err=%v", done, err)
	}
	if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal send", nil) != 1 {
		t.Fatal("receipt reconciliation created new provider work")
	}
}
