package protocol

// AppliedTransitionAuthorizationScope marks a program-verified receipt for an
// already applied control transition, never continuing task authority.
const AppliedTransitionAuthorizationScope = "Only the transition applied by this request; no authority for subsequent actions."

// TaskWorkContext is a frozen selection of shared facts for one task spec.
// It contains neither stage designs nor an implementation author's assessment.
type TaskWorkContext struct {
	ContextDigest string       `json:"context_digest,omitempty"`
	WorkItemID    string       `json:"work_item_id"`
	Revision      int64        `json:"revision"`
	Intent        string       `json:"intent"`
	Constraints   []string     `json:"constraints,omitempty"`
	Facts         []SharedFact `json:"facts,omitempty"`
}

// SharedFact records the provenance of background given to every worker role.
type SharedFact struct {
	Kind   SharedFactKind `json:"kind"`
	Text   string         `json:"text"`
	Source string         `json:"source"`
}
