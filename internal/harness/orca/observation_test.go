package orca

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/protocol"
)

func observationTranscript(t *testing.T, id, originator string) *ProviderSession {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	b, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]string{"id": id, "originator": originator}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return &ProviderSession{ID: id, TranscriptPath: p}
}

func TestHistoricalHandbackReplaysOnlyOriginalRequest(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			f, w, a := failedWorker(t)
			w.spec.Role, w.spec.HandBack = role, true
			old := "The owner worked with you in this terminal and has handed the task back to tend. " +
				"Check the state of the working trees and anything the owner changed or asked for, then finish the same task."
			if role == protocol.RoleAccept {
				old += " End by calling submit_verdict with your verdict, even if you called it before."
			}
			old += "\n"
			w.st.Send, w.st.SendText = "req_original_handback", old
			a.State, a.MainAgent = stateWorking, nil
			if err := w.begin(t.Context(), "trae"); err != nil {
				t.Fatal(err)
			}
			if n := f.orca.count("terminal send", nil); n != 1 {
				t.Fatalf("got %d sends, want only the original replay", n)
			}
			if a.Prompt != old || w.spec.HandBack || w.st.Send != "" {
				t.Fatalf("old handback not settled: prompt=%q state=%+v", a.Prompt, w.st)
			}
			if harness.IsHandBack(w.spec, old+"extra instruction") {
				t.Fatal("unrelated pending turn recognized as handback")
			}
		})
	}
}

func TestResumedAppServerInTUIStillTriggersOwnerTakeover(t *testing.T) {
	f, w, a := failedWorker(t)
	f.ws.opts.Agent = protocol.AgentTraex
	w.st.SessionID = "managed-root"
	a.ProviderSession = observationTranscript(t, "owner-resumed-root", "tend")
	a.Prompt, a.MainAgent = "owner's new instruction", nil
	if _, done, err := w.step(t.Context()); err != nil || done || w.holding == "" || w.st.SessionID != "managed-root" {
		t.Fatalf("owner takeover hidden: done=%v err=%v holding=%q id=%q", done, err, w.holding, w.st.SessionID)
	}
}

func TestHandbackAdoptsResumedAppServerAsRoot(t *testing.T) {
	f, w, a := failedWorker(t)
	f.ws.opts.Agent = protocol.AgentTraex
	a.ProviderSession = observationTranscript(t, "resumed-root", "tend")
	a.State, a.MainAgent = stateWorking, nil
	w.spec.HandBack = true
	if err := w.begin(t.Context(), "trae"); err != nil {
		t.Fatal(err)
	}
	if w.st.SessionID != "resumed-root" {
		t.Fatalf("handback did not adopt root: %q", w.st.SessionID)
	}
	f.orca.finish(w.st.Terminal, "finished")
	if _, done, err := w.step(t.Context()); err != nil || !done || w.holding != "" {
		t.Fatalf("resumed root ignored: done=%v err=%v held=%q", done, err, w.holding)
	}
}

func TestHandbackRebuildsPrivateVerdictBridgeForOriginalRun(t *testing.T) {
	f, w, a := failedWorker(t)
	w.spec.Role = protocol.RoleAccept
	w.spec.StateDir = t.TempDir()
	w.st.SessionID = "misbound-test-session"
	a.ProviderSession = observationTranscript(t, "original-root", "traex-tui")
	if err := w.openBridge(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.closeBridge(t.Context())
	state := filepath.Join(w.spec.StateDir, protocol.WorkerMarker)
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("held bridge still exists: %v", err)
	}
	if err := w.openBridge(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer w.closeBridge(t.Context())
	w.spec.HandBack = true
	if err := w.begin(t.Context(), "traex"); err != nil {
		t.Fatal(err)
	}
	if w.st.SessionID != "original-root" || f.orca.count("agent launch", nil) != 0 {
		t.Fatalf("did not preserve root: %+v", w.st)
	}
	var out bytes.Buffer
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_verdict","arguments":{"verdict":"changes_required","evidence":{"finding":"saved independent finding"}}}}` + "\n"
	if err := bridge.ServeMCP(t.Context(), state, w.spec.RunID, strings.NewReader(in), &out); err != nil || strings.Contains(out.String(), "isError") {
		t.Fatalf("restored verdict bridge: %s err=%v", out.String(), err)
	}
	if v := w.verdicts.Verdict(); v == nil || v.Verdict != protocol.VerdictChangesRequired {
		t.Fatalf("verdict not received: %+v", v)
	}
}

func TestFirstSessionRequiresFreshMatchingPrompt(t *testing.T) {
	for _, change := range []string{"owner input", "empty prompt", "old state", "empty state", "matching"} {
		t.Run(change, func(t *testing.T) {
			_, w, a := failedWorker(t)
			w.st.SessionID = ""
			a.MainAgent = nil
			a.State = stateWorking
			switch change {
			case "owner input":
				a.Prompt = "owner's new instruction"
			case "empty prompt":
				a.Prompt = ""
			case "old state":
				a.StateStartedAt = w.st.Turn - 1
			case "empty state":
				a.State = ""
			}
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			if (w.st.SessionID != "") != (change == "matching") {
				t.Fatalf("unexpected identity: %q", w.st.SessionID)
			}
			if (w.holding != "") != (change == "owner input") {
				t.Fatalf("takeover=%q", w.holding)
			}
			if change == "owner input" {
				b, err := json.Marshal(w.holdingEvidence)
				if err != nil || !strings.Contains(string(b), "observed_prompt_sha256") || strings.Contains(string(b), a.Prompt) || strings.Contains(string(b), a.ProviderSession.TranscriptPath) {
					t.Fatalf("unsafe or absent evidence: %s err=%v", b, err)
				}
			}
		})
	}
}
