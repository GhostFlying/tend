package protocol

// QuestionInputPayload grants selected originals without rewriting an answer.
type QuestionInputPayload struct {
	Version int64  `json:"version"`
	RunID   string `json:"run_id"`
	LeaseID string `json:"lease_id"`
	IssueSourceReference
	FileIDs       []string `json:"file_ids,omitempty"`
	GrantRevision int64    `json:"grant_revision,omitempty"`
	Reason        string   `json:"reason"`
}

// RevokeQuestionInputsPayload needs no live execution identity: an owner can
// revoke historical grants without reviving the old question or run.
type RevokeQuestionInputsPayload struct {
	IssueSourceReference
	GrantRevision int64  `json:"grant_revision"`
	Reason        string `json:"reason"`
}

// QuestionInputGrant retains original execution and owner-file provenance.
type QuestionInputGrant struct {
	Format          int              `json:"format"`
	Revision        int64            `json:"revision"`
	Status          string           `json:"status"`
	DecisionID      string           `json:"decision_id"`
	TaskID          string           `json:"task_id"`
	RunID           string           `json:"run_id"`
	LeaseID         string           `json:"lease_id"`
	ActionID        string           `json:"action_id"`
	Role            RunRole          `json:"role"`
	SpecVersion     int64            `json:"spec_version"`
	Generation      int64            `json:"generation"`
	Execution       *RunExecutionPin `json:"execution,omitempty"`
	SourceMessageID string           `json:"source_message_id"`
	SourceRevision  int64            `json:"source_revision"`
	Files           []TaskInput      `json:"files"`
	Reason          string           `json:"reason"`
}

// QuestionInputScope is the original question agreement, not an authorization
// for a successor or an observation of the executor's physical state.
type QuestionInputScope struct {
	TaskID      string  `json:"task_id"`
	RunID       string  `json:"run_id"`
	LeaseID     string  `json:"lease_id"`
	ActionID    string  `json:"action_id"`
	SpecVersion int64   `json:"spec_version"`
	Role        RunRole `json:"role"`
	Generation  int64   `json:"generation"`
}

type QuestionInputPage struct {
	QuestionVersion int64                `json:"question_version"`
	Original        *QuestionInputScope  `json:"original,omitempty"`
	DecisionID      string               `json:"decision_id"`
	Revision        int64                `json:"revision"`
	Grants          []QuestionInputGrant `json:"grants"`
	NextStep        string               `json:"next_step"`
}
