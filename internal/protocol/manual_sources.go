package protocol

// ManualSourceMetadata separates authenticated submission from external authorship.
// Missing author or original time means unknown, not an inferred owner/time.
type ManualSourceMetadata struct {
	Author       string `json:"author,omitempty"`
	URL          string `json:"url,omitempty"`
	AtMillis     *int64 `json:"at_ms,omitempty"`
	CorrectionOf string `json:"correction_of,omitempty"`
}
type InjectSourcePayload struct {
	ConversationID string               `json:"conversation_id,omitempty"`
	Text           string               `json:"text,omitempty"`
	Attachments    []string             `json:"attachments,omitempty"`
	Metadata       ManualSourceMetadata `json:"metadata"`
}
type ManualSource struct {
	MessageID      string                   `json:"message_id"`
	ConversationID string                   `json:"conversation_id"`
	Text           string                   `json:"text"`
	Attachments    []ConversationAttachment `json:"attachments"`
	Metadata       ManualSourceMetadata     `json:"metadata"`
	SuppliedBy     Principal                `json:"supplied_by"`
	CreatedAt      int64                    `json:"created_at"`
}
