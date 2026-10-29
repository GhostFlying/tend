package protocol

// IssueContextReview records one manager's source review, not a global execution baseline.
type IssueContextReview struct {
	ConfirmedFactIDs        []string            `json:"confirmed_fact_ids,omitempty"`
	RetiredFactIDs          []string            `json:"retired_fact_ids,omitempty"`
	ExpectedAssignmentEpoch int64               `json:"expected_assignment_epoch"`
	ContextRevision         int64               `json:"context_revision"`
	Sources                 []LogicalTurnReview `json:"sources"`
	Summary                 string              `json:"summary"`
}
