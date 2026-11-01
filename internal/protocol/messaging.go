package protocol

// MessagingChannel describes transport capabilities, not agent authority.
type MessagingChannel struct {
	Health           string `json:"health"`
	LastObservedAt   int64  `json:"last_observed_at"`
	PendingInputs    int64  `json:"pending_inputs"`
	ID               string `json:"channel_id"`
	Status           string `json:"status"`
	Revision         int64  `json:"revision"`
	BotMarkRead      bool   `json:"bot_mark_read"`
	UserReadReceipts bool   `json:"user_read_receipts"`
	Text             bool   `json:"text"`
	Files            bool   `json:"files"`
}

type MessagingInput struct {
	Body         string   `json:"body,omitempty"`
	Attachments  []string `json:"attachments,omitempty"`
	ReplyTo      string   `json:"reply_to,omitempty"`
	CorrectionOf string   `json:"correction_of,omitempty"`
}

// MessagingPublication deliberately has no channel or recipient-address fields.
type MessagingPublication struct {
	SourceMessageIDs []string `json:"source_message_ids,omitempty"`
	IssueIDs         []string `json:"issue_ids,omitempty"`
	Body             string   `json:"body,omitempty"`
	Attachments      []string `json:"attachments,omitempty"`
	ReplyTo          string   `json:"reply_to,omitempty"`
	Purpose          string   `json:"purpose"`
	Reason           string   `json:"reason,omitempty"`
}

type BindTelegram struct {
	UserID int64 `json:"user_id"`
}
type MessagingReadPayload struct {
	ContentRevision int64 `json:"content_revision"`
}

type MessagingMessage struct {
	SourceMessageIDs []string                 `json:"source_message_ids"`
	MessageID        string                   `json:"message_id"`
	Sequence         int64                    `json:"sequence"`
	ConversationID   string                   `json:"conversation_id"`
	Author           string                   `json:"author"`
	Body             string                   `json:"body"`
	ContentRevision  int64                    `json:"content_revision"`
	Direction        string                   `json:"direction"`
	ChannelID        string                   `json:"channel_id"`
	ReplyTo          string                   `json:"reply_to,omitempty"`
	CorrectionOf     string                   `json:"correction_of,omitempty"`
	Purpose          string                   `json:"purpose,omitempty"`
	Reason           string                   `json:"reason,omitempty"`
	ExecutionID      string                   `json:"execution_id,omitempty"`
	CreatedAt        int64                    `json:"created_at"`
	Attachments      []ConversationAttachment `json:"attachments"`
	IssueIDs         []string                 `json:"issue_ids"`
	Processing       string                   `json:"processing"`
	Interaction      *MessagingInteraction    `json:"interaction,omitempty"`
	AgentReadAt      int64                    `json:"agent_read_at,omitempty"`
	DeliveryState    string                   `json:"delivery_state,omitempty"`
}

// MessagingInteraction is a presentation of a versioned business question.
// State is derived from that object; it does not grant separate authority.
type MessagingInteraction struct {
	ID             string            `json:"interaction_id"`
	ObjectType     string            `json:"object_type"`
	ObjectID       string            `json:"object_id"`
	Version        int64             `json:"version"`
	Question       string            `json:"question"`
	Context        string            `json:"context,omitempty"`
	Options        []MessagingOption `json:"options"`
	TextAllowed    bool              `json:"text_allowed"`
	State          string            `json:"state"`
	IssueID        string            `json:"issue_id,omitempty"`
	TaskID         string            `json:"task_id,omitempty"`
	HandledChannel string            `json:"handled_channel,omitempty"`
	HandledAt      int64             `json:"handled_at,omitempty"`
	AnswerSummary  string            `json:"answer_summary,omitempty"`
	Effect         *EffectResult     `json:"effect,omitempty"`
}

type MessagingOption struct {
	ID     string         `json:"option_id"`
	Label  string         `json:"label"`
	Effect DecisionEffect `json:"effect,omitempty"`
}

// RespondInteraction never accepts addresses, channels or arbitrary operations.
type RespondInteraction struct {
	Confirm bool   `json:"confirm,omitempty"`
	Version int64  `json:"version"`
	Option  string `json:"option,omitempty"`
	Text    string `json:"text,omitempty"`
}

type MessagingPage struct {
	Items []MessagingMessage `json:"items"`
	More  bool               `json:"more"`
	Next  int64              `json:"next"`
}

type MessagingRead struct {
	MessageID       string `json:"message_id"`
	ContentRevision int64  `json:"content_revision"`
	Reader          string `json:"reader"`
	Endpoint        string `json:"endpoint"`
	Evidence        string `json:"evidence"`
	ExecutionID     string `json:"execution_id,omitempty"`
	ReadAt          int64  `json:"read_at"`
}

type MessagingDelivery struct {
	ID         string `json:"delivery_id"`
	MessageID  string `json:"message_id"`
	ChannelID  string `json:"channel_id"`
	Part       int64  `json:"part"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	ExternalID int64  `json:"external_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Attempts   int64  `json:"attempts"`
	UpdatedAt  int64  `json:"updated_at"`
}

type MessagingReceipts struct {
	Reads              []MessagingRead     `json:"reads"`
	Deliveries         []MessagingDelivery `json:"deliveries"`
	UserReadCapability string              `json:"user_read_capability"`
}

type MessagingTrace struct {
	Tasks    []MessagingTask   `json:"tasks"`
	Message  MessagingMessage  `json:"message"`
	Receipts MessagingReceipts `json:"receipts"`
	Replies  []string          `json:"replies"`
	Turns    []MessagingTurn   `json:"turns"`
}

type MessagingTurn struct {
	ExecutionID string `json:"execution_id"`
	ThreadID    string `json:"thread_id"`
	TurnID      string `json:"turn_id"`
	Status      string `json:"status"`
}

// MessagingTask reports existing delegated work; it is not a new lifecycle.
type MessagingTask struct {
	TaskID      string        `json:"task_id"`
	RunID       string        `json:"run_id,omitempty"`
	Lifecycle   TaskLifecycle `json:"lifecycle"`
	ControlMode ControlMode   `json:"control_mode"`
}

// MessagingEvent is the communication projection of a durable ledger event.
type MessagingEvent struct {
	EventSeq   int64  `json:"event_seq"`
	Kind       string `json:"kind"`
	ObjectType string `json:"object_type"`
	ObjectID   string `json:"object_id"`
}
