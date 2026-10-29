package protocol

// AgentCategoryPage lists only the latest revision of each classification.
type AgentCategoryPage struct {
	Items []AgentCategory `json:"items"`
	More  bool            `json:"more"`
	Next  string          `json:"next,omitempty"`
}

// RunnerAgentCatalog separates registration from the last executor observation
// and host presence. It grants no execution or configuration authority.
type RunnerAgentCatalog struct {
	HostID                string                    `json:"host_id"`
	Current               *AgentInventoryReceipt    `json:"current,omitempty"`
	Projection            *AgentInventoryProjection `json:"projection,omitempty"`
	Executors             []AgentExecutor           `json:"executors,omitempty"`
	Online                bool                      `json:"online"`
	LastHeartbeatAtMillis int64                     `json:"last_heartbeat_at_ms"`
}
