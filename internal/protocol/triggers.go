package protocol

// TriggerQuery selects stable ID pages. After is exclusive; time filters use UTC milliseconds.
type TriggerQuery struct {
	ScheduleID string `json:"schedule_id,omitempty"`
	IssueID    string `json:"issue_id,omitempty"`
	Source     string `json:"source,omitempty"`
	Status     string `json:"status,omitempty"`
	Search     string `json:"search,omitempty"`
	After      string `json:"after,omitempty"`
	Since      int64  `json:"since,omitempty"`
	Until      int64  `json:"until,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type TriggerStep struct {
	Kind   string `json:"kind"`
	At     int64  `json:"at,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type TriggerEvent struct {
	ID         string        `json:"id"`
	IssueID    string        `json:"issue_id"`
	IssueTitle string        `json:"issue_title"`
	IssueState string        `json:"issue_state"`
	ScheduleID string        `json:"schedule_id,omitempty"`
	Title      string        `json:"title"`
	Source     string        `json:"source"`
	Status     string        `json:"status"`
	Reason     string        `json:"reason,omitempty"`
	RequestID  string        `json:"request_id"`
	MessageID  string        `json:"message_id,omitempty"`
	BatchID    string        `json:"batch_id,omitempty"`
	PlannedAt  int64         `json:"planned_at,omitempty"`
	CreatedAt  int64         `json:"created_at"`
	UpdatedAt  int64         `json:"updated_at"`
	Steps      []TriggerStep `json:"steps"`
}

type TriggerEventPage struct {
	Items    []TriggerEvent `json:"items"`
	More     bool           `json:"more"`
	Next     string         `json:"next,omitempty"`
	EventSeq int64          `json:"event_seq"`
	DataAt   int64          `json:"data_at"`
}

type TriggerSchedule struct {
	ID         string             `json:"id"`
	IssueID    string             `json:"issue_id"`
	IssueTitle string             `json:"issue_title"`
	Title      string             `json:"title"`
	Source     string             `json:"source"`
	Kind       string             `json:"kind"`
	Status     string             `json:"status"`
	Reason     string             `json:"reason,omitempty"`
	NextAt     int64              `json:"next_at,omitempty"`
	UpdatedAt  int64              `json:"updated_at"`
	Revision   int64              `json:"revision"`
	Rule       *IssueFollowupRule `json:"rule,omitempty"`
}

type TriggerSchedulePage struct {
	Items    []TriggerSchedule `json:"items"`
	More     bool              `json:"more"`
	Next     string            `json:"next,omitempty"`
	EventSeq int64             `json:"event_seq"`
	DataAt   int64             `json:"data_at"`
}

// WebhookSubscription contains only public metadata; credentials never enter the ledger.
type WebhookSubscription struct {
	ID          string `json:"id"`
	IssueID     string `json:"issue_id"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
	KeyRef      string `json:"key_ref"`
	Revision    int64  `json:"revision"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type WebhookSubscriptionPage struct {
	Items []WebhookSubscription `json:"items"`
	More  bool                  `json:"more"`
	Next  string                `json:"next,omitempty"`
}

type WebhookSubscriptionPayload struct {
	IssueID                 string `json:"issue_id,omitempty"`
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	Title                   string `json:"title,omitempty"`
	Instruction             string `json:"instruction,omitempty"`
	KeyRef                  string `json:"key_ref,omitempty"`
}

type WebhookReceipt struct {
	SubscriptionID string `json:"subscription_id"`
	DeliveryID     string `json:"delivery_id"`
	EventID        string `json:"event_id"`
	Status         string `json:"status"`
}
