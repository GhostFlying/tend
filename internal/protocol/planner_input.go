package protocol

// PlannerEntryInput contains only editable planning fields. Identity, revision
// and timestamps are supplied by the server, not by the coordinating agent.
type PlannerEntryInput struct {
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
}

type PutPlannerEntryInput struct {
	Entry PlannerEntryInput `json:"entry"`
}

func (p PlannerEntryInput) Entry() PlannerEntry {
	return PlannerEntry{Kind: p.Kind, IssueID: p.IssueID, AttentionID: p.AttentionID,
		Title: p.Title, Responsible: p.Responsible, Rank: p.Rank, Status: p.Status,
		Condition: p.Condition, ExpectedAt: p.ExpectedAt, EstimateSeconds: p.EstimateSeconds,
		SourceMessageID: p.SourceMessageID, Reason: p.Reason}
}
