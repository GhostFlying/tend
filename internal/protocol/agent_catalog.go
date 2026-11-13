package protocol

// AgentCategoryPage lists only the latest revision of each classification.
type AgentCategoryPage struct {
	Items []AgentCategory `json:"items"`
	More  bool            `json:"more"`
	Next  string          `json:"next,omitempty"`
}

// RetainedAgentDefinition keeps a public definition with its immutable source.
// Pin is the latest referenced receipt for that snapshot; all pins remain in
// RunnerAgentCatalog.RetainedExecutions. No private configuration is included.
type RetainedAgentDefinition struct {
	Pin        RunExecutionPin          `json:"pin"`
	Definition AgentInventoryDefinition `json:"definition"`
	Runtimes   []AgentInventoryRuntime  `json:"runtimes"`
}

// RunnerAgentCatalog separates registration from the last executor observation
// and host presence. It grants no execution or configuration authority.
type RunnerAgentCatalog struct {
	TaskActivity          *AgentTaskActivity        `json:"task_activity,omitempty"`
	Capabilities          []AgentCapabilityView     `json:"capabilities,omitempty"`
	ConcurrencyBindings   []ConcurrencyBinding      `json:"concurrency_bindings,omitempty"`
	ContinuationSupported bool                      `json:"continuation_supported,omitempty"`
	RetainedDefinitions   []RetainedAgentDefinition `json:"retained_definitions,omitempty"`
	Continuations         []AgentExecutor           `json:"continuations,omitempty"`
	RetainedExecutions    []RunExecutionPin         `json:"retained_executions,omitempty"`
	HostID                string                    `json:"host_id"`
	Current               *AgentInventoryReceipt    `json:"current,omitempty"`
	Projection            *AgentInventoryProjection `json:"projection,omitempty"`
	Executors             []AgentExecutor           `json:"executors,omitempty"`
	Online                bool                      `json:"online"`
	LastHeartbeatAtMillis int64                     `json:"last_heartbeat_at_ms"`
}

// AgentTaskActivity aggregates distinct Tasks by frozen local Agent identity in
// the containing catalog's environment. Empty maps mean zero; an absent summary
// means the server did not provide this observation. Conversation turns are not Tasks.
type AgentTaskActivity struct {
	RunningTasks   map[string]int `json:"running_tasks"`
	UncertainTasks map[string]int `json:"uncertain_tasks"`
}
