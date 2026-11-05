package orca

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// StrictRetainedResume marks the factory that enforces retained native identity.
func (*Workers) StrictRetainedResume() {}

func (w *worker) beginRetained(ctx context.Context) error {
	saved := w.st
	if w.spec.RetainedResume == nil || w.spec.RetainedResume.RequestID == "" || w.spec.RetainedResume.Mode != "attach" || w.spec.HandBack || saved.Terminal == "" || saved.Pane == "" || saved.Worktree == "" || saved.Worktree != w.wt || saved.SessionID == "" || saved.RootRuntimeID == "" || saved.RootLaunchID == "" || saved.RootObservedTurnID == "" || !saved.RootObservationRequired || saved.RootRebindPending || saved.Send != "" || saved.Launch != "" {
		return fmt.Errorf("%w: complete original native identity is unavailable", harness.ErrRetainedResumePending)
	}
	a, alive, err := w.agent(ctx)
	if err != nil || !alive {
		return fmt.Errorf("%w: original terminal observation is unavailable", harness.ErrRetainedResumePending)
	}
	if a.ProviderSession == nil || a.ProviderSession.ID != saved.SessionID || a.RuntimeID != saved.RootRuntimeID || !validRootTurn(a, saved.Turn) || a.RootTurn.LaunchID != saved.RootLaunchID || a.RootTurn.TurnID != saved.RootObservedTurnID || a.PaneKey != saved.Pane {
		return fmt.Errorf("%w: original terminal/session/root identity changed", harness.ErrRetainedResumePending)
	}
	if reason := w.takenOver(a); reason != "" {
		return fmt.Errorf("%w: original native control changed", harness.ErrRetainedResumePending)
	}
	if a.RootTurn.State != "running" && (a.RootTurn.State != "completed" || a.RootTurn.Operations != "settled") {
		return fmt.Errorf("%w: original root cannot be attached without an independent continuation", harness.ErrRetainedResumePending)
	}
	w.retainedAttached = true
	return nil
}
