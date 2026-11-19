package serverkit

import (
	"bytes"
	"database/sql"
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
		ReaderPool: sql.DBStats{MaxOpenConnections: 8, OpenConnections: 6, InUse: 4, Idle: 2, WaitCount: 3, WaitDuration: time.Second, MaxIdleClosed: 5},
		Phases: []store.WritePhaseDuration{
			{Phase: store.WritePersistOutcome, Duration: time.Second},
			{Phase: store.WriteAttention, Duration: 900 * time.Millisecond},
			{Phase: store.WriteExternalWaitRefresh, Duration: 125 * time.Millisecond, Items: new(0)},
		},
	})
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"request_id": `"test-request"`, "trace_id": `"test-trace"`, "operation": `"test.operation"`, "callback_inclusive": "1000000000", "reader_pool_max_open": "8", "reader_pool_open": "6", "reader_pool_in_use": "4", "reader_pool_idle": "2", "reader_pool_wait_count_total": "3", "reader_pool_wait_duration_total": "1000000000", "reader_pool_max_idle_closed_total": "5"} {
		if string(decoded[key]) != want {
			t.Errorf("%s = %s, want %s", key, decoded[key], want)
		}
	}
	var phases []struct {
		Phase    string `json:"phase"`
		Duration int64  `json:"duration_ns"`
		Items    *int   `json:"items"`
	}
	if err := json.Unmarshal(decoded["phases_inclusive"], &phases); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 3 || phases[0].Phase != "persist_outcome" || phases[0].Duration != int64(time.Second) || phases[1].Duration != int64(900*time.Millisecond) {
		t.Fatalf("phases = %+v", phases)
	}
	if phases[0].Items != nil || phases[2].Phase != "external_wait_refresh" || phases[2].Items == nil || *phases[2].Items != 0 {
		t.Errorf("phase item counts = %+v", phases)
	}
	t.Log(output.String())
}

func TestWriteObservationLogTimeoutSnapshot(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	source := store.WriteSource{Operation: "manager.event.ack", RequestID: "original-request", TraceID: "original-trace", RunID: "original-run", EventSequence: 14}
	logWriteObservation(log, store.WriteObservation{ID: 22, Source: source, StartedAt: started, DeadlineAt: started.Add(time.Second), FinishedAt: started.Add(time.Second), WaitFailureSampledAt: started.Add(time.Second), Stage: "connection_wait", Outcome: "deadline_exceeded", ConnectionWait: time.Second, CallerStack: []string{"app.write", "app.AcknowledgeManagerEvent"}, PoolOpenConnections: 1, PoolInUse: 1, ActiveWriter: &store.ActiveWriteSnapshot{ID: 21, Source: store.WriteSource{Operation: "runner.claim"}, AcquiredAt: started.Add(500 * time.Millisecond), SampledAt: started.Add(time.Second), HeldFor: 500 * time.Millisecond, Stage: "callback"}})
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"write_id": "22", "run_id": `"original-run"`, "event_sequence": "14", "request_id": `"original-request"`, "trace_id": `"original-trace"`, "connection_wait": "1000000000", "pool_in_use": "1", "write_deadline_at": `"2026-01-02T03:04:06Z"`} {
		if string(decoded[key]) != want {
			t.Errorf("%s=%s want %s", key, decoded[key], want)
		}
	}
	var active struct {
		ID      uint64    `json:"write_id"`
		Held    int64     `json:"held_ns"`
		Sampled time.Time `json:"sampled_at"`
	}
	if err := json.Unmarshal(decoded["active_writer_at_wait_failure"], &active); err != nil {
		t.Fatal(err)
	}
	if active.ID != 21 || active.Held != int64(500*time.Millisecond) || !active.Sampled.Equal(started.Add(time.Second)) {
		t.Fatalf("snapshot units/identity=%+v", active)
	}
}
