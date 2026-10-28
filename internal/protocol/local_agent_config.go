package protocol

// LocalAgentConfiguration is an observation of the runner's accepted registry.
// Acceptance is distinct from server registration or executable availability.
type LocalAgentConfiguration struct {
	State            string                    `json:"state"`
	Revision         uint64                    `json:"revision"`
	SnapshotID       string                    `json:"snapshot_id,omitempty"`
	PublicSHA256     string                    `json:"public_sha256,omitempty"`
	Projection       *AgentInventoryProjection `json:"projection,omitempty"`
	PendingRequestID string                    `json:"pending_request_id,omitempty"`
}

type ReloadLocalAgentsRequest struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

type ReconcileLocalAgentsRequest struct {
	RequestID string `json:"request_id"`
}

type LocalAgentConfigurationReceipt struct {
	RequestID    string `json:"request_id"`
	Revision     uint64 `json:"revision"`
	SnapshotID   string `json:"snapshot_id"`
	PublicSHA256 string `json:"public_sha256"`
}

type LocalAgentRegistration struct {
	State            string                `json:"state"`
	PendingRequestID string                `json:"pending_request_id,omitempty"`
	Server           *AgentInventoryStatus `json:"server,omitempty"`
}

type LocalAgentConfigurationResult struct {
	Registration *LocalAgentRegistration         `json:"registration,omitempty"`
	Current      *LocalAgentConfiguration        `json:"current,omitempty"`
	Candidate    *AgentInventoryProjection       `json:"candidate,omitempty"`
	Receipt      *LocalAgentConfigurationReceipt `json:"receipt,omitempty"`
	RequestState string                          `json:"request_state,omitempty"`
	ErrorCode    string                          `json:"error_code,omitempty"`
}
