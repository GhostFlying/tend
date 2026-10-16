package protocol

import (
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

// Target names the object a request acts on. ID may be empty for operations
// that create the object.
type Target struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// Envelope wraps every application request. RequestID identifies one logical
// request: transport retries keep it, a new business intent gets a new one.
// The caller's identity is not part of the envelope; it comes from the
// authenticated connection as a Principal.
type Envelope struct {
	RequestID               string          `json:"request_id"`
	ParentRequestID         string          `json:"parent_request_id,omitempty"`
	TraceID                 string          `json:"trace_id,omitempty"`
	Operation               string          `json:"operation"`
	Target                  Target          `json:"target"`
	ExpectedControlRevision *int64          `json:"expected_control_revision,omitempty"`
	Payload                 json.RawMessage `json:"payload,omitempty"`
	// Reason is the caller's short note on why it made the request, kept
	// with the request record as its provenance. It is not part of the
	// payload hash.
	Reason string `json:"reason,omitempty"`
}

// MaxReason bounds Envelope.Reason in characters.
const MaxReason = 500

var operationPattern = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)+$`)

// Validate checks the envelope's shape before any side effect happens.
func (e *Envelope) Validate() error {
	switch {
	case e.RequestID == "":
		return Errorf(CodeMissingRequestID, "request_id is required")
	case !ValidID(PrefixRequest, e.RequestID):
		return Errorf(CodeInvalidRequest, "request_id is malformed")
	case e.ParentRequestID != "" && !ValidID(PrefixRequest, e.ParentRequestID):
		return Errorf(CodeInvalidRequest, "parent_request_id is malformed")
	case e.TraceID != "" && !ValidID(PrefixTrace, e.TraceID):
		return Errorf(CodeInvalidRequest, "trace_id is malformed")
	case !operationPattern.MatchString(e.Operation):
		return Errorf(CodeInvalidRequest, "operation is malformed")
	case e.Target.Type == "":
		return Errorf(CodeInvalidRequest, "target.type is required")
	case len(e.Payload) > 0 && !json.Valid(e.Payload):
		return Errorf(CodeInvalidRequest, "payload is not valid JSON")
	case utf8.RuneCountInString(e.Reason) > MaxReason:
		return Errorf(CodeInvalidRequest, "reason is longer than %d characters", MaxReason)
	}
	return nil
}
