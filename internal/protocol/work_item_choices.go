package protocol

// WorkItemChoice is a durable planning question, never an execution lease.
type WorkItemChoice struct {
	ChoiceID         string   `json:"choice_id"`
	WorkItemID       string   `json:"work_item_id"`
	Version          int64    `json:"version"`
	Status           string   `json:"status"`
	Question         string   `json:"question"`
	Options          []string `json:"options"`
	Recommendation   string   `json:"recommendation,omitempty"`
	SourceID         string   `json:"source_id"`
	TaskIDs          []string `json:"task_ids,omitempty"`
	DeliveryIDs      []string `json:"delivery_ids,omitempty"`
	Answer           string   `json:"answer,omitempty"`
	AnswerSourceID   string   `json:"answer_source_id,omitempty"`
	AnswerSourceKind string   `json:"answer_source_kind,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	Supersedes       string   `json:"supersedes,omitempty"`
	ReplacedBy       string   `json:"replaced_by,omitempty"`
	MessageID        string   `json:"message_id"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
}

type CreateWorkItemChoicePayload struct {
	Question       string   `json:"question"`
	Options        []string `json:"options"`
	Recommendation string   `json:"recommendation,omitempty"`
	SourceID       string   `json:"source_id"`
	TaskIDs        []string `json:"task_ids,omitempty"`
	DeliveryIDs    []string `json:"delivery_ids,omitempty"`
}

type UpdateWorkItemChoicePayload struct {
	Answer         string                       `json:"answer,omitempty"`
	OwnerMessageID string                       `json:"owner_message_id,omitempty"`
	Replacement    *CreateWorkItemChoicePayload `json:"replacement,omitempty"`
}

type WorkItemChoicePage struct {
	Items []WorkItemChoice `json:"items"`
}
