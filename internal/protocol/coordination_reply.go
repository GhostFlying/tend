package protocol

// CoordinationReply is a formal assistant message with explicit semantic scope.
// Contexts name the versions before this write; the response returns new versions.
type CoordinationReply struct {
	Body          string                 `json:"body"`
	Purpose       string                 `json:"purpose"`
	Disposition   string                 `json:"disposition"`
	WorkItemIDs   []string               `json:"work_item_ids"`
	Contexts      []LogicalContextReview `json:"contexts"`
	ReviewedKinds []string               `json:"reviewed_kinds"`
	Facts         []ContextFact          `json:"facts"`
}
