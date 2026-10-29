package protocol

// Attention is a request for a person's attention, never an assignment or lease.
// Decision answers stay in their original decision/choice, not in this record.
type Attention struct {
	QuestionVersion    int64     `json:"question_version,omitempty"`
	QuestionOptions    []string  `json:"question_options,omitempty"`
	AttentionID        string    `json:"attention_id"`
	IssueID            string    `json:"issue_id,omitempty"`
	TaskID             string    `json:"task_id,omitempty"`
	Kind               string    `json:"kind"`
	Title              string    `json:"title"`
	Revision           int64     `json:"revision"`
	Status             string    `json:"status"`
	DecisionID         string    `json:"decision_id,omitempty"`
	ChoiceID           string    `json:"choice_id,omitempty"`
	SourceMessageID    string    `json:"source_message_id,omitempty"`
	RaisedBy           Principal `json:"raised_by"`
	BlocksClose        bool      `json:"blocks_close,omitempty"`
	Resolution         string    `json:"resolution,omitempty"`
	ResolutionSourceID string    `json:"resolution_source_id,omitempty"`
	ReadAt             int64     `json:"read_at,omitempty"`
	ArchivedAt         int64     `json:"archived_at,omitempty"`
	CreatedAt          int64     `json:"created_at"`
	UpdatedAt          int64     `json:"updated_at"`
}
type AttentionView struct {
	Attention
	Decision *DecisionView   `json:"decision,omitempty"`
	Choice   *WorkItemChoice `json:"choice,omitempty"`
}
type AttentionPage struct {
	Items []AttentionView `json:"items"`
	More  bool            `json:"more"`
	Next  string          `json:"next,omitempty"`
}
type CreateAttentionPayload struct {
	IssueID         string   `json:"issue_id,omitempty"`
	TaskID          string   `json:"task_id,omitempty"`
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	Options         []string `json:"options,omitempty"`
	DecisionID      string   `json:"decision_id,omitempty"`
	ChoiceID        string   `json:"choice_id,omitempty"`
	SourceMessageID string   `json:"source_message_id,omitempty"`
	BlocksClose     bool     `json:"blocks_close,omitempty"`
}
type UpdateAttentionPayload struct {
	DecisionVersion int64    `json:"decision_version,omitempty"`
	Options         []string `json:"options,omitempty"`
	Title           string   `json:"title"`
	BlocksClose     bool     `json:"blocks_close,omitempty"`
}
type RespondAttentionPayload struct {
	Version         int64  `json:"version"`
	Option          string `json:"option,omitempty"`
	Text            string `json:"text,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
}
type ConfirmAttentionPayload struct {
	Summary         string `json:"summary"`
	SourceMessageID string `json:"source_message_id,omitempty"`
}
type PresentAttentionPayload struct {
	Read     *bool `json:"read,omitempty"`
	Archived *bool `json:"archived,omitempty"`
}

// IssueDeadline retains the meaning and provenance of a time independently of
// reminder delivery. Unknown due time/estimate are represented by nil.
type IssueDeadline struct {
	SourceNeedsReview bool   `json:"source_needs_review,omitempty"`
	LatestStartAt     *int64 `json:"latest_start_at,omitempty"`
	DeadlineID        string `json:"deadline_id"`
	IssueID           string `json:"issue_id"`
	Revision          int64  `json:"revision"`
	Status            string `json:"status"`
	Title             string `json:"title"`
	Nature            string `json:"nature"`
	Strength          string `json:"strength"`
	DueAt             *int64 `json:"due_at,omitempty"`
	Timezone          string `json:"timezone"`
	EstimateSeconds   *int64 `json:"estimate_seconds,omitempty"`
	SourceMessageID   string `json:"source_message_id,omitempty"`
	SourceRevision    int64  `json:"source_revision,omitempty"`
	Reminders         bool   `json:"reminders"`
	NearRuleID        string `json:"near_rule_id,omitempty"`
	DueRuleID         string `json:"due_rule_id,omitempty"`
	Reason            string `json:"reason"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
}

// CancelIssueDeadlinePayload carries optional source material for a planner relay.
// The authenticated owner may cancel directly without a source message.
type CancelIssueDeadlinePayload struct {
	IssueSourceReference
	Reason string `json:"reason,omitempty"`
}

type PutIssueDeadlinePayload struct {
	IssueID                string `json:"issue_id"`
	Title                  string `json:"title"`
	Nature                 string `json:"nature"`
	Strength               string `json:"strength"`
	DueAt                  *int64 `json:"due_at,omitempty"`
	Timezone               string `json:"timezone"`
	EstimateSeconds        *int64 `json:"estimate_seconds,omitempty"`
	SourceMessageID        string `json:"source_message_id,omitempty"`
	ExpectedSourceRevision *int64 `json:"expected_source_revision,omitempty"`
	Reminders              *bool  `json:"reminders,omitempty"`
	Reason                 string `json:"reason"`
}

// PlannerEntry explicitly orders personal actions or Issue planning attention.
// It does not replace the task scheduler's rating/weight/waiting rules.
type PlannerEntry struct {
	EntryID         string `json:"entry_id"`
	Revision        int64  `json:"revision"`
	Kind            string `json:"kind"`
	IssueID         string `json:"issue_id,omitempty"`
	AttentionID     string `json:"attention_id,omitempty"`
	Title           string `json:"title"`
	Responsible     string `json:"responsible"`
	Rank            int64  `json:"rank"`
	Status          string `json:"status"`
	Condition       string `json:"condition,omitempty"`
	ExpectedAt      *int64 `json:"expected_at,omitempty"`
	EstimateSeconds *int64 `json:"estimate_seconds,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	Reason          string `json:"reason"`
	UpdatedAt       int64  `json:"updated_at"`
}
type PutPlannerEntryPayload struct {
	Entry PlannerEntry `json:"entry"`
}
type NextActionPage struct {
	Items     []PlannerEntry  `json:"items"`
	Deadlines []IssueDeadline `json:"deadlines"`
}
