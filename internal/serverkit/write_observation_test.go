package serverkit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

func TestWriteObservationLogFields(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	logWriteObservation(log, store.WriteObservation{
		Source: store.WriteSource{Operation: "test.operation", RequestID: "test-request", TraceID: "test-trace"},
		Stage:  "callback", Outcome: "error", Total: 2 * time.Second, Callback: time.Second,
		Phases: []store.WritePhaseDuration{{Phase: store.WritePersistOutcome, Duration: time.Second}, {Phase: store.WriteAttention, Duration: 900 * time.Millisecond}},
	})
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"request_id": `"test-request"`, "trace_id": `"test-trace"`, "operation": `"test.operation"`, "callback_inclusive": "1000000000"} {
		if string(decoded[key]) != want {
			t.Errorf("%s = %s, want %s", key, decoded[key], want)
		}
	}
	var phases []struct {
		Phase    string `json:"phase"`
		Duration int64  `json:"duration_ns"`
	}
	if err := json.Unmarshal(decoded["phases_inclusive"], &phases); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[0].Phase != "persist_outcome" || phases[0].Duration != int64(time.Second) || phases[1].Duration != int64(900*time.Millisecond) {
		t.Errorf("phases = %+v", phases)
	}
	t.Log(output.String())
}
