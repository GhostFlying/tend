package protocol

// IssueSummary describes canonical responsibility, never a legacy facade row.
type IssueSummary struct {
	IssueControl
	Revision int64  `json:"revision"`
	Title    string `json:"title"`
	Project  string `json:"project"`
}

type IssuePage struct {
	Items []IssueSummary `json:"items"`
	Next  string         `json:"next,omitempty"`
	More  bool           `json:"more"`
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
	Issue
	Historical      bool                       `json:"historical,omitempty"`
	ContextRevision int64                      `json:"context_revision,omitempty"`
	Tasks           []IssueTaskSummary         `json:"tasks,omitempty"`
	PendingReviews  []WorkItemCompletionReview `json:"pending_reviews,omitempty"`
	SourceReviews   []IssueSourceReview        `json:"source_reviews,omitempty"`
}
