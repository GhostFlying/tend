package protocol

// ConversationModel is a model the hosted harness offers to the owner.
// ID is an opaque catalog identity, distinct from the provider's model name.
type ConversationModel struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	SupportedEfforts []string `json:"supported_efforts"`
	DefaultEffort    string   `json:"default_effort"`
}

// ConversationSettingsView describes the shared settings for the next turn.
// A running turn keeps the settings it started with (D73).
type ConversationSettingsView struct {
	Source    string              `json:"source,omitempty"`
	ReadOnly  bool                `json:"read_only,omitempty"`
	Enabled   bool                `json:"enabled"`
	Available bool                `json:"available"`
	Revision  int64               `json:"revision"`
	Model     string              `json:"model"`
	Effort    string              `json:"effort"`
	Models    []ConversationModel `json:"models"`
}

// SetConversationSettingsPayload changes the next hosted turn's settings.
// The envelope carries the expected settings revision.
type SetConversationSettingsPayload struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
}

// AssistantState describes a persisted assistant message, including drafts.
type AssistantState string

const (
	AssistantStreaming   AssistantState = "streaming"
	AssistantCompleted   AssistantState = "completed"
	AssistantInterrupted AssistantState = "interrupted"
	AssistantFailed      AssistantState = "failed"
	AssistantUnknown     AssistantState = "unknown"
)
