package protocol

// FollowupSpec is an explicit, bounded wait for existing owner-authorized work.
// Times use UTC milliseconds; Timezone only controls owner presentation.
type FollowupSpec struct {
	Purpose      string             `json:"purpose"`
	Instruction  string             `json:"instruction"`
	SourceID     string             `json:"source_id,omitempty"`
	FollowOnID   string             `json:"follow_on_id,omitempty"`
	Kind         string             `json:"kind"`
	At           int64              `json:"at,omitempty"`
	EverySeconds int64              `json:"every_seconds,omitempty"`
	ExpiresAt    int64              `json:"expires_at,omitempty"`
	MaxTriggers  int                `json:"max_triggers,omitempty"`
	Timezone     string             `json:"timezone"`
	ExpirySilent bool               `json:"expiry_silent,omitempty"`
	Condition    *FollowupCondition `json:"condition,omitempty"`
}

type FollowupCondition struct {
	Kind          string   `json:"kind"`
	TaskIDs       []string `json:"task_ids,omitempty"`
	DeliveryID    string   `json:"delivery_id,omitempty"`
	DeliveryState string   `json:"delivery_state,omitempty"`
	DecisionID    string   `json:"decision_id,omitempty"`
	ChoiceID      string   `json:"choice_id,omitempty"`
}

type FollowupRule struct {
	FollowupSpec
	RuleID       string `json:"rule_id"`
	WorkItemID   string `json:"work_item_id"`
	Revision     int64  `json:"revision"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	NextAt       int64  `json:"next_at,omitempty"`
	TriggerCount int    `json:"trigger_count"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type FollowupOccurrence struct {
	OccurrenceID string `json:"occurrence_id"`
	RuleID       string `json:"rule_id"`
	Revision     int64  `json:"revision"`
	WorkItemID   string `json:"work_item_id"`
	MessageID    string `json:"message_id"`
	Reason       string `json:"reason"`
	Status       string `json:"status"`
	Evidence     string `json:"evidence"`
	CreatedAt    int64  `json:"created_at"`
}

type FollowupPage struct {
	Enabled  bool           `json:"enabled"`
	Items    []FollowupRule `json:"items"`
	More     bool           `json:"more"`
	Next     string         `json:"next,omitempty"`
	EventSeq int64          `json:"event_seq"`
}

type FollowupHistoryPage struct {
	Rule           FollowupRule         `json:"rule"`
	Revisions      []FollowupRule       `json:"revisions"`
	Occurrences    []FollowupOccurrence `json:"occurrences"`
	More           bool                 `json:"more"`
	NextRevision   int64                `json:"next_revision,omitempty"`
	NextOccurrence string               `json:"next_occurrence,omitempty"`
}

type FollowupSummary struct {
	Active  int    `json:"active"`
	Paused  int    `json:"paused"`
	NextAt  int64  `json:"next_at,omitempty"`
	Purpose string `json:"purpose,omitempty"`
	RuleID  string `json:"rule_id,omitempty"`
}

type UpdateFollowupPayload struct {
	Spec *FollowupSpec `json:"spec,omitempty"`
}
