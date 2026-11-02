package remote

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (ex *execution) reportNativeIdentity(ctx context.Context) error {
	ex.mu.Lock()
	state, publish := ex.state, ex.spec.OnStartupEvent
	ex.mu.Unlock()
	if publish == nil {
		return nil
	}
	identity := protocol.NativeIdentity{Provider: "orca", ProviderID: state.ProviderID, Terminal: state.Terminal,
		Pane: state.Pane, SessionID: state.Thread, State: "known", ControllerObservation: "unknown"}
	if identity.Terminal == "" || identity.Pane == "" || identity.SessionID == "" {
		identity.State, identity.Reason = "partial", "native_locator_incomplete"
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return publish(ctx, harness.WorkerEvent{NativeID: state.Terminal + "@" + state.Thread, Kind: protocol.EventNativeIdentity, Body: body})
}
