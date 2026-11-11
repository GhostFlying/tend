package protocol

// CoordinationReply publishes a formal assistant message. Issue responsibility
// comes from the actual execution; historical review metadata is optional and
// never a prerequisite for publication or proof of understanding/authorization.
type CoordinationReply struct {
	MessageID     string                 `json:"message_id"`
	ReplyTo       string                 `json:"reply_to,omitempty"`
	Attachments   []string               `json:"attachments,omitempty"`
	Body          string                 `json:"body"`
	Purpose       string                 `json:"purpose"`
	Disposition   string                 `json:"disposition,omitempty"`
	WorkItemIDs   []string               `json:"work_item_ids,omitempty"`
	Contexts      []LogicalContextReview `json:"contexts,omitempty"`
	ReviewedKinds []string               `json:"reviewed_kinds,omitempty"`
	Facts         []ContextFact          `json:"facts,omitempty"`
}
