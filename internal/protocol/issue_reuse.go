package protocol

// ReuseIssuePayload explicitly selects one existing responsibility.
type ReuseIssuePayload struct {
	IssueControlPayload
	OriginalRequestID           string `json:"original_request_id"`
	ExpectedAssociationRevision *int64 `json:"expected_association_revision"`
}

// IssueReuseStatus exposes exact origin facts without guessing legacy identity.
type IssueReuseStatus struct {
	Issue             Issue  `json:"issue"`
	OriginalRequestID string `json:"original_request_id,omitempty"`
	OriginObservation string `json:"origin_observation"`
	NextStep          string `json:"next_step"`
}

type IssueReuseResult struct {
	Issue             Issue               `json:"issue"`
	OriginalRequestID string              `json:"original_request_id"`
	Source            MessageAssociations `json:"source"`
	Reused            bool                `json:"reused"`
	NextStep          string              `json:"next_step"`
}
