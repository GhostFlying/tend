package protocol

// TaskWorkContext is a frozen selection of shared facts for one task spec.
// It contains neither stage designs nor an implementation author's assessment.
type TaskWorkContext struct {
	WorkItemID  string       `json:"work_item_id"`
	Revision    int64        `json:"revision"`
	Intent      string       `json:"intent"`
	Constraints []string     `json:"constraints,omitempty"`
	Facts       []SharedFact `json:"facts,omitempty"`
}

// SharedFact records the provenance of background given to every worker role.
type SharedFact struct {
	Kind   SharedFactKind `json:"kind"`
	Text   string         `json:"text"`
	Source string         `json:"source"`
}
