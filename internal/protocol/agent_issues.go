package protocol

// AgentIssueRun is an execution-time association, never inferred from a display name.
type AgentIssueRun struct {
	TaskID          string          `json:"task_id"`
	Run             RunView         `json:"run"`
	Pin             RunExecutionPin `json:"pin"`
	Source          string          `json:"source"`
	PendingDecision bool            `json:"pending_decision"`
	// TaskOpenQuestions preserves task-level records without asserting Run applicability.
	TaskOpenQuestions int `json:"task_open_questions"`
}

// AgentIssue groups all recorded executions for this identity and Issue.
type AgentIssue struct {
	IssueID string          `json:"issue_id"`
	Title   string          `json:"title"`
	State   IssueState      `json:"state"`
	Current bool            `json:"current"`
	Runs    []AgentIssueRun `json:"runs"`
}

// AgentIssuePage covers canonical Issues with frozen Run identity evidence only.
// Empty results do not prove a hosted main/consultation Agent has never worked.
type AgentIssuePage struct {
	Identity AgentIdentity `json:"identity"`
	Coverage string        `json:"coverage"`
	Items    []AgentIssue  `json:"items"`
	More     bool          `json:"more"`
	Next     string        `json:"next,omitempty"`
}
