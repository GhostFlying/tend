package protocol

// TaskInput is an immutable original explicitly attached to a task spec.
// Callers identify FileID; the server freezes the other fields from storage.
type TaskInput struct {
	FileID          string `json:"file_id"`
	Name            string `json:"name,omitempty"`
	MediaType       string `json:"media_type,omitempty"`
	Size            int64  `json:"size,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
}

// CapabilityTaskInputs means the runner exposes original inputs to both roles.
const CapabilityTaskInputs = "task-inputs-v1"
