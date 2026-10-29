package protocol

// A named consultation is an ordinary logical conversation, never a worker.
type CreateConsultationPayload struct {
	AgentRef string `json:"agent_ref"`
	Title    string `json:"title"`
}
type PromoteConsultationPayload struct {
	IssueSourceReference
	Sources                 []IssueSourceReference `json:"sources,omitempty"`
	Plan                    WorkItemPlan           `json:"plan,omitzero"`
	Assignee                IssueAssignee          `json:"assignee,omitzero"`
	ExistingIssueID         string                 `json:"existing_issue_id,omitempty"`
	ExpectedIssueRevision   *int64                 `json:"expected_issue_revision,omitempty"`
	ExpectedAssignmentEpoch int64                  `json:"expected_assignment_epoch,omitempty"`
}
