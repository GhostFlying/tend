package protocol

import (
	"encoding/json"
	"testing"
)

func validEnvelope(t *testing.T) Envelope {
	t.Helper()
	g := NewULIDGen(fixedNow(1), nil)
	return Envelope{
		RequestID: g.New(PrefixRequest),
		Operation: "tasks.create",
		Target:    Target{Type: "task"},
		Payload:   json.RawMessage(`{"title":"x"}`),
	}
}

func TestEnvelopeValidate(t *testing.T) {
	g := NewULIDGen(fixedNow(2), nil)
	tests := []struct {
		name   string
		mutate func(*Envelope)
		want   Code
	}{
		{"valid", func(*Envelope) {}, ""},
		{"missing request id", func(e *Envelope) { e.RequestID = "" }, CodeMissingRequestID},
		{"malformed request id", func(e *Envelope) { e.RequestID = "req_bad" }, CodeInvalidRequest},
		{"request id with other prefix", func(e *Envelope) { e.RequestID = g.New(PrefixTask) }, CodeInvalidRequest},
		{"valid parent and trace", func(e *Envelope) {
			e.ParentRequestID = g.New(PrefixRequest)
			e.TraceID = g.New(PrefixTrace)
		}, ""},
		{"malformed parent", func(e *Envelope) { e.ParentRequestID = "nope" }, CodeInvalidRequest},
		{"malformed trace", func(e *Envelope) { e.TraceID = g.New(PrefixRequest) }, CodeInvalidRequest},
		{"operation without dot", func(e *Envelope) { e.Operation = "tasks" }, CodeInvalidRequest},
		{"operation with uppercase", func(e *Envelope) { e.Operation = "Tasks.create" }, CodeInvalidRequest},
		{"nested operation", func(e *Envelope) { e.Operation = "executions.control.stop" }, ""},
		{"missing target type", func(e *Envelope) { e.Target = Target{} }, CodeInvalidRequest},
		{"invalid payload", func(e *Envelope) { e.Payload = json.RawMessage(`{`) }, CodeInvalidRequest},
		{"empty payload", func(e *Envelope) { e.Payload = nil }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEnvelope(t)
			tt.mutate(&e)
			err := e.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if got := CodeOf(err); got != tt.want {
				t.Errorf("Validate() code = %q (err %v), want %q", got, err, tt.want)
			}
		})
	}
}
