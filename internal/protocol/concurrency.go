package protocol

// CapabilityAdaptiveConcurrency enables durable execution feedback, not retries.
const CapabilityAdaptiveConcurrency = "runner-adaptive-concurrency-v1"

type ConcurrencyBinding struct {
	EnvironmentID string `json:"environment_id"`
	RuntimeID     string `json:"runtime_id"`
	GroupID       string `json:"group_id"`
}

type ConcurrencyGroup struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Revision         int64                `json:"revision"`
	Min              int64                `json:"min"`
	Initial          int64                `json:"initial"`
	Max              int64                `json:"max"`
	Current          int64                `json:"current"`
	Occupied         int64                `json:"occupied"`
	Running          int64                `json:"running"`
	Waiting          int64                `json:"waiting"`
	CooldownUntil    int64                `json:"cooldown_until"`
	ObservationSince int64                `json:"observation_since"`
	SaturatedMillis  int64                `json:"saturated_ms"`
	SuccessfulTurns  int64                `json:"successful_turns"`
	FeedbackHealthy  bool                 `json:"feedback_healthy"`
	Bindings         []ConcurrencyBinding `json:"bindings"`
}

type ConcurrencyGroupPage struct {
	Items []ConcurrencyGroup `json:"items"`
	Next  string             `json:"next,omitempty"`
	More  bool               `json:"more"`
}

// ConcurrencySource preserves identity and typed evidence without error prose.
type ConcurrencySource struct {
	Purpose           string `json:"purpose,omitempty"`
	ParentExecutionID string `json:"parent_execution_id,omitempty"`
	ExecutionID       string `json:"execution_id"`
	EnvironmentID     string `json:"environment_id"`
	RuntimeID         string `json:"runtime_id"`
	AgentID           string `json:"agent_id"`
	IssueID           string `json:"issue_id,omitempty"`
	IssueTitle        string `json:"issue_title,omitempty"`
	TaskID            string `json:"task_id,omitempty"`
	RunID             string `json:"run_id,omitempty"`
	ConversationID    string `json:"conversation_id,omitempty"`
	SessionID         string `json:"session_id"`
	NativeSessionID   string `json:"native_session_id,omitempty"`
	TurnID            string `json:"turn_id,omitempty"`
	Attempt           string `json:"attempt"`
	Kind              string `json:"kind"`
	Code              string `json:"code,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	OccurredAt        int64  `json:"occurred_at"`
	ReceivedAt        int64  `json:"received_at"`
	EventID           string `json:"event_id"`
}

type ConcurrencyChange struct {
	Bindings        []ConcurrencyBinding `json:"bindings"`
	ID              int64                `json:"id"`
	GroupID         string               `json:"group_id"`
	GroupName       string               `json:"group_name"`
	At              int64                `json:"at"`
	Reason          string               `json:"reason"`
	Before          int64                `json:"before"`
	After           int64                `json:"after"`
	Min             int64                `json:"min"`
	Initial         int64                `json:"initial"`
	Max             int64                `json:"max"`
	Occupied        int64                `json:"occupied"`
	CooldownUntil   int64                `json:"cooldown_until"`
	WindowStart     int64                `json:"window_start"`
	WindowEnd       int64                `json:"window_end"`
	SaturatedMillis int64                `json:"saturated_ms"`
	SuccessfulTurns int64                `json:"successful_turns"`
	CongestionCount int64                `json:"congestion_count"`
	FeedbackHealthy bool                 `json:"feedback_healthy"`
	Waiting         int64                `json:"waiting"`
	Actor           string               `json:"actor,omitempty"`
	RequestID       string               `json:"request_id,omitempty"`
	Sources         []ConcurrencySource  `json:"sources"`
}

type ConcurrencyHistoryPage struct {
	Items []ConcurrencyChange `json:"items"`
	Since int64               `json:"since"`
	Until int64               `json:"until"`
	Next  string              `json:"next,omitempty"`
	More  bool                `json:"more"`
}

type ConcurrencyQuery struct {
	GroupID       string `json:"group_id,omitempty"`
	Search        string `json:"search,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	RuntimeID     string `json:"runtime_id,omitempty"`
	IssueID       string `json:"issue_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Since         int64  `json:"since,omitempty"`
	Until         int64  `json:"until,omitempty"`
	After         string `json:"after,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type ConfigureConcurrencyPayload struct {
	Name          string `json:"name"`
	Min           int64  `json:"min"`
	Initial       int64  `json:"initial"`
	Max           int64  `json:"max"`
	EnvironmentID string `json:"environment_id,omitempty"`
	RuntimeID     string `json:"runtime_id,omitempty"`
}

// ConcurrencyFeedback is carried over existing durable run-event envelopes.
type ConcurrencyFeedback struct {
	Kind       string        `json:"kind"`
	SessionID  string        `json:"session_id"`
	TurnID     string        `json:"turn_id"`
	Attempt    string        `json:"attempt,omitempty"`
	Failure    *AgentFailure `json:"failure,omitempty"`
	OccurredAt int64         `json:"occurred_at,omitempty"`
}
