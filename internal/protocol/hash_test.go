package protocol

import (
	"encoding/json"
	"testing"
)

func hashOf(t *testing.T, e Envelope) string {
	t.Helper()
	h, err := PayloadHash(&e)
	if err != nil {
		t.Fatalf("PayloadHash() error = %v", err)
	}
	return h
}

func revision(n int64) *int64 { return &n }

func baseHashEnvelope() Envelope {
	return Envelope{
		RequestID:               "req_01K7BQ5XJ8000000000000000A",
		Operation:               "tasks.update_intent",
		Target:                  Target{Type: "task", ID: "task_01K7BQ5XJ8000000000000000B"},
		ExpectedControlRevision: revision(12),
		Payload:                 json.RawMessage(`{"priority":2,"constraints":["no preemption"],"note":"<a&b>"}`),
	}
}

// TestPayloadHashGolden pins the canonical form so the hash of an existing
// request never changes silently; changing it would break dedup of recorded
// requests.
func TestPayloadHashGolden(t *testing.T) {
	// Canonical form: {"expected_control_revision":12,"operation":"tasks.update_intent",
	// "payload":{"constraints":["no preemption"],"note":"<a&b>","priority":2},
	// "target":{"id":"task_01K7BQ5XJ8000000000000000B","type":"task"}}
	const want = "d4ad21175df098764d7c07511cddb67c8808ed67af1308ae131d50a577f35e79"
	if got := hashOf(t, baseHashEnvelope()); got != want {
		t.Errorf("PayloadHash() = %s, want %s", got, want)
	}
}

func TestPayloadHashIgnoresFormattingAndTransportFields(t *testing.T) {
	base := hashOf(t, baseHashEnvelope())
	tests := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"key order and whitespace", func(e *Envelope) {
			e.Payload = json.RawMessage("{ \"note\" : \"<a&b>\",\n \"constraints\": [\"no preemption\"], \"priority\": 2 }")
		}},
		{"different request id", func(e *Envelope) { e.RequestID = "req_01K7BQ5XJ8000000000000000C" }},
		{"parent request id", func(e *Envelope) { e.ParentRequestID = "req_01K7BQ5XJ8000000000000000D" }},
		{"trace id", func(e *Envelope) { e.TraceID = "trc_01K7BQ5XJ8000000000000000E" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := baseHashEnvelope()
			tt.mutate(&e)
			if got := hashOf(t, e); got != base {
				t.Errorf("PayloadHash() = %s, want unchanged %s", got, base)
			}
		})
	}
}

func TestPayloadHashDetectsIntentChanges(t *testing.T) {
	base := hashOf(t, baseHashEnvelope())
	tests := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"operation", func(e *Envelope) { e.Operation = "tasks.create" }},
		{"target type", func(e *Envelope) { e.Target.Type = "action" }},
		{"target id", func(e *Envelope) { e.Target.ID = "task_01K7BQ5XJ8000000000000000F" }},
		{"revision", func(e *Envelope) { e.ExpectedControlRevision = revision(13) }},
		{"no revision", func(e *Envelope) { e.ExpectedControlRevision = nil }},
		{"payload value", func(e *Envelope) {
			e.Payload = json.RawMessage(`{"priority":3,"constraints":["no preemption"],"note":"<a&b>"}`)
		}},
		{"number spelling", func(e *Envelope) {
			e.Payload = json.RawMessage(`{"priority":2.0,"constraints":["no preemption"],"note":"<a&b>"}`)
		}},
		{"array order", func(e *Envelope) {
			e.Payload = json.RawMessage(`{"priority":2,"constraints":["no preemption","x"],"note":"<a&b>"}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := baseHashEnvelope()
			tt.mutate(&e)
			if got := hashOf(t, e); got == base {
				t.Errorf("PayloadHash() unchanged after changing %s", tt.name)
			}
		})
	}
}

func TestPayloadHashPreservesLargeIntegers(t *testing.T) {
	a, b := baseHashEnvelope(), baseHashEnvelope()
	a.Payload = json.RawMessage(`{"n":9007199254740993}`)
	b.Payload = json.RawMessage(`{"n":9007199254740992}`)
	if hashOf(t, a) == hashOf(t, b) {
		t.Error("integers beyond float64 precision hash the same")
	}
}

func TestPayloadHashRejectsInvalidPayload(t *testing.T) {
	e := baseHashEnvelope()
	e.Payload = json.RawMessage(`{"a":`)
	if _, err := PayloadHash(&e); err == nil {
		t.Error("PayloadHash() error = nil for invalid JSON")
	}
}
