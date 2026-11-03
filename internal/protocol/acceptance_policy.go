package protocol

// IndependentAcceptanceRequest is a creator's explicit policy for future tasks.
// Creator identity and request provenance are supplied by the server.
type IndependentAcceptanceRequest struct {
	Required bool   `json:"required"`
	Reason   string `json:"reason"`
}

// AcceptancePolicyCreator is authenticated creation provenance, not an assignee.
type AcceptancePolicyCreator struct {
	Kind          string         `json:"kind"`
	Agent         *AgentIdentity `json:"agent,omitempty"`
	OwnerSubject  string         `json:"owner_subject,omitempty"`
	SourceIssueID string         `json:"source_issue_id,omitempty"`
}

// IssueAcceptancePolicy is frozen in each Issue revision and new task agreement.
// Its absence means the existing requirement for independent acceptance.
type IssueAcceptancePolicy struct {
	Required      bool                    `json:"required"`
	Reason        string                  `json:"reason"`
	Creator       AcceptancePolicyCreator `json:"creator"`
	DecidedBy     AcceptancePolicyCreator `json:"decided_by"`
	RequestID     string                  `json:"request_id"`
	IssueRevision int64                   `json:"issue_revision"`
}

type SetIssueAcceptancePolicyPayload struct {
	ExpectedAssignmentEpoch int64 `json:"expected_assignment_epoch"`
	IndependentAcceptanceRequest
}
