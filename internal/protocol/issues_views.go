package protocol

// IssueSummary describes canonical responsibility, never a legacy facade row.
type IssueSummary struct {
	Board     *IssueBoardCard  `json:"board,omitempty"`
	Execution *ExecutionCounts `json:"execution,omitempty"`
	IssueControl
	Revision int64  `json:"revision"`
	Title    string `json:"title"`
	Project  string `json:"project"`
}

type IssuePage struct {
	Counts    map[IssueWorkStatus]int `json:"counts,omitempty"`
	Assignees []IssueAssignee         `json:"assignees,omitempty"`
	FactsSeq  int64                   `json:"facts_seq,omitempty"`
	Items     []IssueSummary          `json:"items"`
	Next      string                  `json:"next,omitempty"`
	More      bool                    `json:"more"`
}

// IssueTaskSummary keeps the new Issue surface independent of work_item fields.
// Task/run detail remains available from its existing independent task API.
type IssueTaskSummary struct {
	TaskID          string             `json:"task_id"`
	Title           string             `json:"title"`
	Lifecycle       TaskLifecycle      `json:"lifecycle"`
	ControlMode     ControlMode        `json:"control_mode"`
	ControlRevision int64              `json:"control_revision"`
	SpecVersion     int64              `json:"spec_version"`
	Rating          Rating             `json:"rating"`
	BlockReason     BlockReason        `json:"block_reason"`
	Execution       *TaskExecutionView `json:"execution,omitempty"`
}

// IssueSourceReview is an actual pending source share. Reading it does not
// resolve it or grant the source author additional execution authority.
type IssueSourceReview struct {
	MessageID           string `json:"message_id"`
	Author              string `json:"author"`
	Body                string `json:"body"`
	ContentRevision     int64  `json:"content_revision"`
	AssociationRevision int64  `json:"association_revision"`
	Withdrawn           bool   `json:"withdrawn"`
}

type IssueView struct {
	QueueReason        *QueueWaitReason          `json:"queue_reason,omitempty"`
	Pause              *IssuePauseProgress       `json:"pause,omitempty"`
	Children           *IssueChildProgress       `json:"children,omitempty"`
	DeliveryDuties     []IssueDeliveryDuty       `json:"delivery_duties,omitempty"`
	OwnershipPolicy    string                    `json:"ownership_policy,omitempty"`
	ResponsibilityRuns []ExecutionResponsibility `json:"responsibility_runs,omitempty"`
	Manager            *IssueManagerView         `json:"manager,omitempty"`
	Progress           *IssueProgress            `json:"progress,omitempty"`
	Conversation       *IssueConversationBinding `json:"conversation,omitempty"`
	Ancestors          []IssueAncestor           `json:"ancestors,omitempty"`
	Attentions         *AttentionPage            `json:"attentions,omitempty"`
	Deadlines          []IssueDeadline           `json:"deadlines,omitempty"`
	Issue
	Historical      bool                       `json:"historical,omitempty"`
	ContextRevision int64                      `json:"context_revision,omitempty"`
	Tasks           []IssueTaskSummary         `json:"tasks,omitempty"`
	PendingReviews  []WorkItemCompletionReview `json:"pending_reviews,omitempty"`
	SourceReviews   []IssueSourceReview        `json:"source_reviews,omitempty"`
}
