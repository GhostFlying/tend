package protocol

// ExecutionState is a read-only task execution fact, not a plan stage.
type ExecutionState string

const (
	ExecutionIdle         ExecutionState = "idle"
	ExecutionQueued       ExecutionState = "queued"
	ExecutionStarting     ExecutionState = "starting"
	ExecutionRunning      ExecutionState = "running"
	ExecutionRecovering   ExecutionState = "recovering"
	ExecutionAccepting    ExecutionState = "accepting"
	ExecutionWaitingOwner ExecutionState = "waiting_owner"
	ExecutionPaused       ExecutionState = "paused"
	ExecutionUnknown      ExecutionState = "unknown"
	ExecutionCompleted    ExecutionState = "completed"
	ExecutionCancelled    ExecutionState = "cancelled"
)

func ExecutionStateValues() []ExecutionState {
	return []ExecutionState{ExecutionIdle, ExecutionQueued, ExecutionStarting, ExecutionRunning, ExecutionRecovering, ExecutionAccepting, ExecutionWaitingOwner, ExecutionPaused, ExecutionUnknown, ExecutionCompleted, ExecutionCancelled}
}

// QueueWaitCode identifies the scheduling constraint without changing BlockReason.
type QueueWaitCode string

const (
	QueueReady            QueueWaitCode = "ready"
	QueueWorkspaceBusy    QueueWaitCode = "workspace_busy"
	QueueDependency       QueueWaitCode = "dependency"
	QueueHostCapacity     QueueWaitCode = "host_capacity"
	QueueHostUnavailable  QueueWaitCode = "host_unavailable"
	QueueHostRequirements QueueWaitCode = "host_requirements"
	QueueTaskBlocked      QueueWaitCode = "task_blocked"
)

func QueueWaitCodeValues() []QueueWaitCode {
	return []QueueWaitCode{QueueReady, QueueWorkspaceBusy, QueueDependency, QueueHostCapacity, QueueHostUnavailable, QueueHostRequirements, QueueTaskBlocked}
}

// QueueWaitReason explains one current scheduling constraint and its references.
type QueueWaitReason struct {
	Code       QueueWaitCode `json:"code"`
	Message    string        `json:"message"`
	TaskID     string        `json:"task_id,omitempty"`
	ResourceID string        `json:"resource_id,omitempty"`
	HostID     string        `json:"host_id,omitempty"`
}

// TaskExecutionView separates an allocated worker from pending execution.
type TaskExecutionView struct {
	Recovery    *ExecutionRecovery `json:"recovery,omitempty"`
	State       ExecutionState     `json:"state"`
	Role        RunRole            `json:"role,omitempty"`
	RunID       string             `json:"run_id,omitempty"`
	QueueReason *QueueWaitReason   `json:"queue_reason,omitempty"`
}

// ExecutionRecovery exposes safe progress facts, never native transcript paths.
type ExecutionRecovery struct {
	Phase    string `json:"phase"`
	Code     string `json:"code,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
}
