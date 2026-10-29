package protocol

// HostedAgentInventory is a safe view of accepted server-local definitions.
// Registration never implies that an adapter or model has been verified ready.
type HostedAgentInventory struct {
	ConfigurationRevision int64                    `json:"configuration_revision"`
	PublicSHA256          string                   `json:"public_sha256"`
	MainAgentID           string                   `json:"main_agent_id"`
	AcceptedRequestID     string                   `json:"accepted_request_id"`
	State                 string                   `json:"state"`
	Projection            AgentInventoryProjection `json:"projection"`
}
