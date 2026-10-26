package protocol

// WorkItemView combines the plan with current execution facts. Column is a
// projection, not an editable business stage or proof of completion.
type WorkItemView struct {
	Choices        []WorkItemChoice           `json:"choices,omitempty"`
	PendingReviews []WorkItemCompletionReview `json:"pending_reviews,omitempty"`
	WorkItem
	Execution      *ExecutionCounts      `json:"execution,omitempty"`
	StageExecution []StageExecutionView  `json:"stage_execution,omitempty"`
	Column         string                `json:"column"`
	Tasks          []TaskSummary         `json:"tasks,omitempty"`
	Observations   []DeliveryObservation `json:"observations,omitempty"`
}

type WorkItemPage struct {
	Items []WorkItemView `json:"items"`
}

// StageExecutionView counts task facts independently of the plan stage status.
type StageExecutionView struct {
	StageID   string          `json:"stage_id"`
	Execution ExecutionCounts `json:"execution"`
}

// ExecutionCounts counts distinct linked tasks, including mixed execution states.
type ExecutionCounts struct {
	Starting     int `json:"starting,omitempty"`
	Recovering   int `json:"recovering,omitempty"`
	Queued       int `json:"queued"`
	Running      int `json:"running"`
	Accepting    int `json:"accepting"`
	WaitingOwner int `json:"waiting_owner"`
	Paused       int `json:"paused"`
	Unknown      int `json:"unknown"`
	Completed    int `json:"completed"`
	Cancelled    int `json:"cancelled"`
	Idle         int `json:"idle"`
}
