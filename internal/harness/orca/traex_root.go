package orca

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/harness"
)

// observeTraexRoot supplements only an otherwise bound native history snapshot
// whose ending clock is missing. It never supplies continuation authority.
func (w *worker) observeTraexRoot(ctx context.Context, a *Agent) error {
	r := a.RootTurn
	if a.AgentType != "traex" || r == nil || r.Source != "native-api" || r.State != "unknown" || r.ErrorCode != "ending_unconfirmed" || !validRootTurn(*a, w.st.Turn) || w.spec.HandBack || w.st.RootRebindPending || w.spec.Dir == "" {
		return nil
	}
	if w.st.SessionID == "" || a.ProviderSession.ID != w.st.SessionID || w.st.RootRuntimeID == "" || a.RuntimeID != w.st.RootRuntimeID || w.st.RootLaunchID == "" || r.LaunchID != w.st.RootLaunchID || w.st.RootObservedTurnID != r.TurnID || w.st.RootObservedForTurn != w.st.Turn {
		return nil
	}
	native, err := rootTranscript(ctx, *a.ProviderSession, w.st, w.spec.RunID, w.spec.Dir)
	if errors.Is(err, harness.ErrHeld) {
		return err
	}
	if err != nil || native.RootTurn.TurnID != r.TurnID || native.RootTurn.CompletedAt <= 0 || native.RootTurn.CompletedAt < native.RootTurn.StartedAt || native.RootTurn.StartedAt/1000 != r.StartedAt/1000 {
		return nil
	}
	native.RuntimeID, native.AgentType = a.RuntimeID, a.AgentType
	native.RootTurn.LaunchID = r.LaunchID
	native.RootTurn.HostStartedAt = r.HostStartedAt
	native.Children, native.SubagentObservation = a.Children, a.SubagentObservation
	if a.SubagentObservation == "live" {
		for _, child := range a.Children {
			if child.State != "done" || child.Membership != "settled" {
				native.RootTurn.Operations = "pending"
				break
			}
		}
	}
	native.transcriptSupplement = true
	*a = native
	return nil
}
