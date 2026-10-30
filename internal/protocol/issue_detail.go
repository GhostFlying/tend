package protocol

// IssueProgress is a manager's assessment, separate from execution facts.
type IssueProgress struct {
	Summary       string        `json:"summary"`
	NextStep      string        `json:"next_step"`
	IssueRevision int64         `json:"issue_revision"`
	UpdatedAt     int64         `json:"updated_at"`
	UpdatedBy     IssueAssignee `json:"updated_by"`
}

type UpdateIssueProgressPayload struct {
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	Summary                 string `json:"summary"`
	NextStep                string `json:"next_step"`
}

// IssueConversationBinding is read-only; reading it never opens a conversation.
type IssueConversationBinding struct {
	ConversationID string `json:"conversation_id,omitempty"`
	CanSend        bool   `json:"can_send"`
	Reason         string `json:"reason,omitempty"`
}

type IssueAncestor struct {
	IssueID string     `json:"issue_id"`
	Title   string     `json:"title"`
	State   IssueState `json:"state"`
}

// IssueActivityEntry keeps stable ledger order while its transcript streams.
type IssueActivityEntry struct {
	ID         string                `json:"id"`
	Position   int64                 `json:"position"`
	Kind       string                `json:"kind"`
	CreatedAt  int64                 `json:"created_at"`
	Author     string                `json:"author"`
	Title      string                `json:"title,omitempty"`
	IssueID    string                `json:"issue_id,omitempty"`
	TaskID     string                `json:"task_id,omitempty"`
	ArtifactID string                `json:"artifact_id,omitempty"`
	Block      *IssueTranscriptBlock `json:"block,omitempty"`
}

type IssueActivityPage struct {
	Items      []IssueActivityEntry `json:"items"`
	More       bool                 `json:"more"`
	NextBefore int64                `json:"next_before,omitempty"`
	EventSeq   int64                `json:"event_seq"`
}

// IssueResult identifies one immutable artifact and only its matching verdicts.
type IssueResult struct {
	ArtifactDetail
	Title       string `json:"title"`
	RunID       string `json:"run_id"`
	SpecVersion int64  `json:"spec_version"`
	Generation  int64  `json:"generation"`
	CreatedAt   int64  `json:"created_at"`
}

type IssueResultPage struct {
	Items []IssueResult `json:"items"`
	More  bool          `json:"more"`
	Next  string        `json:"next,omitempty"`
}
