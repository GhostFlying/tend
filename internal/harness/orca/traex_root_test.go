package orca

import (
	"context"
	"encoding/json"
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
