package protocol

// EventNativeIdentity uses the existing opaque run-event body. Old peers retain
// the fact without interpreting it; no new runner envelope field is required.
const EventNativeIdentity = "worker.native_identity"

// NativeIdentity contains recorded provider navigation facts, never credentials,
// private instructions or authority to control the provider session.
type NativeIdentity struct {
	TurnID                string `json:"turn_id,omitempty"`
	Provider              string `json:"provider"`
	ProviderID            string `json:"provider_id,omitempty"`
	Terminal              string `json:"terminal,omitempty"`
	Pane                  string `json:"pane,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
	RuntimeID             string `json:"runtime_id,omitempty"`
	LaunchID              string `json:"launch_id,omitempty"`
	State                 string `json:"state"`
	Reason                string `json:"reason,omitempty"`
	ControllerObservation string `json:"controller_observation"`
	ObservedSessionID     string `json:"observed_session_id,omitempty"`
}
