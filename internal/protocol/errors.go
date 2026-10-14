package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Code is a stable, caller-visible error code.
type Code string

// Error codes.
const (
	CodeInvalidRequest    Code = "invalid_request"
	CodeMissingRequestID  Code = "missing_request_id"
	CodeConflictPayload   Code = "conflict_payload"
	CodeStaleRevision     Code = "stale_revision"
	CodeForbidden         Code = "forbidden"
	CodeNotFound          Code = "not_found"
	CodeInvalidTransition Code = "invalid_transition"
	CodeUnknownOutcome    Code = "unknown_outcome"
	CodeInternal          Code = "internal"
)

// Error is the error returned across protocol boundaries. Message is written
// for the caller and never carries internal error text.
type Error struct {
	Code    Code            `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf returns an *Error with a formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the protocol code carried by err, or CodeInternal when err
// is not a protocol error.
func CodeOf(err error) Code {
	if pe, ok := errors.AsType[*Error](err); ok {
		return pe.Code
	}
	return CodeInternal
}

// OpStatus is the state of a side-effecting request.
type OpStatus string

// Operation statuses.
const (
	// OpSucceeded means the request committed and has no pending external
	// phase.
	OpSucceeded OpStatus = "succeeded"
	// OpFailed means the request was rejected for a deterministic business
	// reason; retrying the same request ID returns the same failure.
	OpFailed OpStatus = "failed"
	// OpAccepted means the local transaction committed but an external phase,
	// such as an outbox delivery, is still in progress.
	OpAccepted OpStatus = "accepted"
	// OpUnknown means an external effect may or may not have happened and
	// must be reconciled rather than retried.
	OpUnknown OpStatus = "unknown"
)

// Operation is the result of a side-effecting request.
type Operation struct {
	RequestID string          `json:"request_id"`
	Status    OpStatus        `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}
