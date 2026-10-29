package protocol

// SetIssueMessageAssociationsPayload classifies a versioned source against
// canonical responsibility identities. It does not authorize task execution.
type SetIssueMessageAssociationsPayload struct {
	Disposition     string   `json:"disposition"`
	ContentRevision int64    `json:"content_revision"`
	IssueIDs        []string `json:"issue_ids"`
}
