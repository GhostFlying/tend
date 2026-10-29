package protocol

// NativeEntry exposes only recorded owner navigation and control facts.
// Workspace navigation is separate from provider session/controller observation.
type NativeEntry struct {
	Identity              *NativeIdentity  `json:"identity,omitempty"`
	IdentitySequence      int64            `json:"identity_sequence,omitempty"`
	IdentityRecordedAt    int64            `json:"identity_recorded_at,omitempty"`
	RunID                 string           `json:"run_id"`
	TaskID                string           `json:"task_id"`
	HostID                string           `json:"host_id"`
	Provider              string           `json:"provider"`
	AdapterImplemented    bool             `json:"adapter_implemented"`
	WorkspaceRecorded     bool             `json:"workspace_recorded"`
	WorkspaceID           string           `json:"workspace_id,omitempty"`
	WorkspacePath         string           `json:"workspace_path,omitempty"`
	ProviderID            string           `json:"provider_id,omitempty"`
	ProviderInstanceID    string           `json:"provider_instance_id,omitempty"`
	Worker                WorkerKind       `json:"worker,omitempty"`
	Execution             *RunExecutionPin `json:"execution,omitempty"`
	RunObserved           RunObserved      `json:"run_observed"`
	ControlMode           ControlMode      `json:"control_mode"`
	ControlRevision       int64            `json:"control_revision"`
	ControllerObservation string           `json:"controller_observation"`
	SessionRecorded       bool             `json:"session_recorded"`
	Reason                string           `json:"reason"`
}
