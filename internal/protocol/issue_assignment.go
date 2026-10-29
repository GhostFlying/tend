package protocol

// IssueAssignmentPayload names a concrete registered agent or the owner.
// Questions and attention never use assignment as a routing mechanism.
type IssueAssignmentPayload struct {
	ExpectedAssignmentEpoch int64         `json:"expected_assignment_epoch"`
	Assignee                IssueAssignee `json:"assignee"`
	Reason                  string        `json:"reason"`
}
type FinishIssueAssignmentPayload struct {
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	HandoffID               string `json:"handoff_id"`
	Reason                  string `json:"reason"`
}
