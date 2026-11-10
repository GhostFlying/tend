package protocol

import "encoding/json"

// OwnerToolSpec describes one concrete owner capability, not a free-form HTTP
// endpoint. The server derives this catalog from its actual registrations.
type OwnerToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Capability  string          `json:"capability"`
}

// OwnerToolCall carries server-established identity separately from arguments.
type OwnerToolCall struct {
	Actor       Principal
	ExecutionID string
	RequestID   string
	Name        string
	Arguments   json.RawMessage
}

type OwnerToolArguments struct {
	// OriginalEnvelope is only for an exact replay of an existing owner request.
	OriginalEnvelope        *Envelope         `json:"original_envelope,omitempty"`
	Reason                  string            `json:"reason,omitempty"`
	EffectSeq               int64             `json:"effect_seq,omitempty"`
	Target                  Target            `json:"target,omitempty"`
	ExpectedControlRevision *int64            `json:"expected_control_revision,omitempty"`
	Payload                 json.RawMessage   `json:"payload,omitempty"`
	Path                    map[string]string `json:"path,omitempty"`
	Query                   map[string]string `json:"query,omitempty"`
	Body                    json.RawMessage   `json:"body,omitempty"`
	ContentBase64           string            `json:"content_base64,omitempty"`
	ContentType             string            `json:"content_type,omitempty"`
	// Range permits bounded complete reads of large originals across calls.
	Range string `json:"range,omitempty"`
}
