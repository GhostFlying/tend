package orca

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRetainedControlsExplicitPauseInterruptsAttachedOriginal(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.State, a.RootTurn.Operations = "running", "unknown"
	a.State, a.MainAgent.State, a.MainAgent.Outcome = stateWorking, stateWorking, ""
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.st.Worktree, w.st.SessionID = w.wt, a.ProviderSession.ID
	spec := f.spec(protocol.RoleImplement)
	spec.ProviderID, spec.Thread = w.wt, mustState(w.st)
	spec.RetainedResume = &protocol.RetainedRunResume{RequestID: "explicit-authority", Mode: "attach"}
	ctx, pause := context.WithCancelCause(t.Context())
	defer pause(nil)
	attached, err := f.ws.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	pause(harness.ErrPaused)
	for range attached.Events() {
	}
	_, _ = attached.Wait()
	if f.orca.count("terminal send", func(args []string) bool { return flag(args, "--text") == esc }) != 1 || f.orca.count("agent launch", nil) != 0 || f.orca.count("terminal close", nil) != 0 {
		t.Fatal("explicit pause failed to interrupt exactly the original terminal")
	}
	saved := attached.(*worker).st
	if !saved.Interrupted || saved.Terminal != w.st.Terminal || saved.SessionID != w.st.SessionID || saved.RootObservedTurnID != w.st.RootObservedTurnID {
		t.Fatal("pause lost original identity or interruption fact", saved)
	}
}

func TestRetainedControlsUnconfirmedAttachmentDoesNotInterrupt(t *testing.T) {
	f, w, a := nativeFixture(t)
	a.RootTurn.State, a.RootTurn.Operations = "unknown", "unknown"
	a.State, a.MainAgent.State, a.MainAgent.Outcome = stateDone, stateDone, ""
	if _, _, err := w.agent(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.st.Worktree, w.st.SessionID = w.wt, a.ProviderSession.ID
	w.spec.RetainedResume = &protocol.RetainedRunResume{RequestID: "explicit-authority", Mode: "attach"}
	if err := w.beginRetained(t.Context()); err == nil {
		t.Fatal("unknown original root attached")
	}
	w.stop(t.Context(), harness.ErrPaused)
	if f.orca.count("terminal send", nil) != 0 || f.orca.count("terminal close", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
		t.Fatal("unconfirmed strict attachment injected input")
	}
}
