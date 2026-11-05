package orca

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// recordObservedRoot projects a fact on a local copy. The lifecycle keeps the
// original native API observation, including its uncertainty and authority.
func (w *worker) recordObservedRoot(ctx context.Context, a Agent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.ws.opts.Agent == protocol.AgentTraex {
		native := a
		if err := w.observeTraexRoot(ctx, &a); err != nil {
			// Transcript conflicts cannot become takeover or recovery authority.
			a = native
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	before := w.st
	trackFact := w.ws.opts.Agent == protocol.AgentTraex && a.AgentType == "traex" && a.RootTurn != nil
	if trackFact {
		fact := *a.RootTurn
		fact.ObservedAt = 0 // Sampling time does not change the observed fact.
		body, err := json.Marshal(fact)
		if err != nil {
			return err
		}
		fingerprint := fmt.Sprintf("%x", sha256.Sum256(body))
		if w.st.RootObservationFactHash != fingerprint {
			w.st.RootObservationFactHash = fingerprint
			// The canonical recorder persists the new fingerprint and one
			// revision together before publishing. Its native identity stays
			// unchanged; this key is only its observation deduplication state.
			w.st.RootObservationIdentity = ""
		}
	}
	err := w.recordRootObservation(ctx, a)
	if err != nil && trackFact {
		// A failed save must not make a later attempt skip persistence.
		w.st.RootObservationPhase = before.RootObservationPhase
		w.st.RootObservationIdentity = before.RootObservationIdentity
		w.st.RootObservationRevision = before.RootObservationRevision
		w.st.RootObservationFactHash = before.RootObservationFactHash
	}
	return err
}

// observeTraexRoot supplements only an otherwise bound native history snapshot
// whose ending clock is missing. Its result is for observation reporting only.
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
	*a = native
	return nil
}
