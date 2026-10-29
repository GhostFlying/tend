package orca

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// reportNativeIdentity reuses the run's durable event channel. Recording a
// locator never opens a provider session, sends input or changes its controller.
func (w *worker) reportNativeIdentity(ctx context.Context, uncertain bool) error {
	identity := protocol.NativeIdentity{Provider: "orca", ProviderID: w.wt, Terminal: w.st.Terminal, Pane: w.st.Pane, SessionID: w.st.SessionID, RuntimeID: w.st.RootRuntimeID, LaunchID: w.st.RootLaunchID, State: "known", ControllerObservation: "unknown"}
	if identity.RuntimeID == "" {
		identity.RuntimeID = w.st.RootLegacyRuntimeID
	}
	if identity.Terminal == "" || identity.Pane == "" {
		identity.State, identity.Reason = "unknown", "terminal_not_recorded"
	} else if identity.SessionID == "" {
		identity.State, identity.Reason = "partial", "session_not_observed"
	}
	if uncertain || w.reconciling || w.holding != "" {
		identity.State, identity.Reason = "unknown", "native_observation_unconfirmed"
	}
	if w.holdingEvidence != nil {
		identity.State, identity.Reason = "unknown", "controller_changed"
		identity.ControllerObservation = "takeover_observed"
		identity.ObservedSessionID, _ = w.holdingEvidence["observed_session_id"].(string)
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	if string(raw) == w.lastNativeIdentity {
		return nil
	}
	if !w.running && w.spec.OnStartupEvent != nil {
		w.seq++
		if err := w.spec.OnStartupEvent(ctx, harness.WorkerEvent{NativeID: fmt.Sprintf("%s@%d#%d", w.st.Terminal, w.st.Turn, w.seq), Kind: protocol.EventNativeIdentity, Body: raw}); err != nil {
			return err
		}
	} else {
		w.emit(ctx, protocol.EventNativeIdentity, identity)
	}
	w.lastNativeIdentity = string(raw)
	return nil
}
