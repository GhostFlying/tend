package orca

import (
	"context"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetainedResumeNeverFallsBackToNativeLaunchOrSend(t *testing.T) {
	for _, kind := range []string{"bad-thread", "missing-thread", "terminal-gone", "session-changed", "runtime-changed", "launch-changed", "turn-changed", "pending-send", "cancelled-root", "pending-operations"} {
		t.Run(kind, func(t *testing.T) {
			f, w, a := nativeFixture(t)
			a.RootTurn.State, a.RootTurn.Operations = "running", "unknown"
			a.State, a.MainAgent.State, a.MainAgent.Outcome = stateWorking, stateWorking, ""
			if _, _, err := w.agent(t.Context()); err != nil {
				t.Fatal(err)
			}
			w.st.Worktree = w.wt
			w.st.SessionID = a.ProviderSession.ID
			spec := f.spec(protocol.RoleImplement)
			spec.ProviderID = w.wt
			spec.Attempt = 2
			spec.RetainedResume = &protocol.RetainedRunResume{RequestID: "independent-resolution", Mode: "attach"}
			switch kind {
			case "terminal-gone":
				f.orca.terms = nil
			case "session-changed":
				a.ProviderSession.ID = "another-session"
			case "runtime-changed":
				w.st.RootRuntimeID = "another-runtime"
			case "launch-changed":
				a.RootTurn.LaunchID = "another-launch"
			case "turn-changed":
				a.RootTurn.TurnID = "another-turn"
			case "pending-send":
				w.st.Send = "original-unknown-send"
			case "cancelled-root":
				a.RootTurn.State, a.RootTurn.Operations = "cancelled", "settled"
			case "pending-operations":
				a.RootTurn.State, a.RootTurn.Operations = "completed", "pending"
			}
			spec.Thread = mustState(w.st)
			if kind == "bad-thread" {
				spec.Thread = "{"
			}
			if kind == "missing-thread" {
				spec.Thread = ""
			}
			worker, err := f.ws.Start(t.Context(), spec)
			if worker != nil || !errors.Is(err, harness.ErrRetainedResumePending) {
				t.Fatal("unsafe strict resume was not retained", worker, err)
			}
			if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
				t.Fatal("strict resume started, prompted or closed a native execution")
			}
		})
	}
}

func TestRetainedResumeAttachesSameRunningNativeTurn(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.State, a.RootTurn.Operations = "running", "unknown"
	a.State, a.MainAgent.State, a.MainAgent.Outcome = stateWorking, stateWorking, ""
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.st.Worktree = w.wt
	w.st.SessionID = a.ProviderSession.ID
	terminal, pane, session, turn := w.st.Terminal, w.st.Pane, w.st.SessionID, w.st.RootObservedTurnID
	w.spec.RetainedResume = &protocol.RetainedRunResume{RequestID: "independent-resolution", Mode: "attach"}
	if err := w.beginRetained(t.Context()); err != nil {
		t.Fatal("same running native turn did not attach", err)
	}
	if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("reattachment mutated native execution")
	}
	if w.st.Terminal != terminal || w.st.Pane != pane || w.st.SessionID != session || w.st.RootObservedTurnID != turn {
		t.Fatal("reattachment changed original native identity")
	}
}

func TestRetainedResumeStartedWorkerShutdownDoesNotInjectInput(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.State, a.RootTurn.Operations = "running", "unknown"
	a.State, a.MainAgent.State, a.MainAgent.Outcome = stateWorking, stateWorking, ""
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.st.SessionID = a.ProviderSession.ID
	spec := f.spec(protocol.RoleImplement)
	spec.ProviderID, spec.Thread = w.wt, mustState(w.st)
	spec.RetainedResume = &protocol.RetainedRunResume{RequestID: "independent-resolution", Mode: "attach"}
	ctx, cancel := context.WithCancel(t.Context())
	worker, err := f.ws.Start(ctx, spec)
	if err != nil {
		cancel()
		t.Fatal("complete original native identity could not attach through Start", err)
	}
	cancel()
	for range worker.Events() {
	}
	_, _ = worker.Wait()
	if f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("observation-only attachment injected input or replaced the original execution")
	}
}
