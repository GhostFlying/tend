package protocol

const OpIssueOwnerSteer = "issues.steer_owner"

// ManagerSteerUnsupported is the fixed reason carried in the existing native
// reply. Old runners may report the same condition as a generic rejection.
const ManagerSteerUnsupported = "harness: current-turn steer is unsupported"

// IssueOwnerSteerPayload routes the exact owner original to a specific active
// manager. The envelope revision fences the Issue; the source revision fences
// the original text. A revocation references an earlier original in that Issue.
type IssueOwnerSteerPayload struct {
	MessageID               string `json:"message_id"`
	RunID                   string `json:"run_id"`
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	SourceMessageID         string `json:"source_message_id"`
	ExpectedSourceRevision  int64  `json:"expected_source_revision"`
	RevokesSourceMessageID  string `json:"revokes_source_message_id,omitempty"`
}

type IssueOwnerSteerView struct {
	MessageID              string    `json:"message_id"`
	IssueID                string    `json:"issue_id"`
	AssignmentEpoch        int64     `json:"assignment_epoch"`
	TaskID                 string    `json:"task_id"`
	RunID                  string    `json:"run_id"`
	ThreadID               string    `json:"thread_id"`
	TurnID                 string    `json:"turn_id"`
	SourceMessageID        string    `json:"source_message_id"`
	SourceRevision         int64     `json:"source_revision"`
	RevokesSourceMessageID string    `json:"revokes_source_message_id,omitempty"`
	Sender                 Principal `json:"sender"`
	Status                 string    `json:"status"`
	Reason                 string    `json:"reason,omitempty"`
	AcceptedAt             int64     `json:"accepted_at,omitempty"`
	NativeRequestID        string    `json:"native_request_id,omitempty"`
	NativeItemID           string    `json:"native_item_id,omitempty"`
}
