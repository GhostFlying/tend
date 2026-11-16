package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRunEventQueueObservationPreservesOriginalBatchIdentity(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "actual_first_sequence", true: "empty_batch_clears_cursor"}[empty], func(t *testing.T) {
			h := newHarness(t)
			observed := make(chan store.WriteObservation, 4)
			store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observed <- o })(h.store)
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- h.store.Write(t.Context(), func(*store.Tx) error { close(entered); <-release; return nil })
			}()
			<-entered
			defer func() {
				close(release)
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			outer := store.WithWriteSource(t.Context(), store.WriteSource{Operation: "enclosing.operation", RequestID: "original-request", TraceID: "original-trace"})
			outer = store.WithWriteEvent(outer, "enclosing-run", 999)
			ctx, cancel := context.WithTimeout(outer, 100*time.Millisecond)
			defer cancel()
			events := []protocol.RunEvent{{Seq: 19, Kind: "item.completed", Body: json.RawMessage(`{"text":"private_event_payload"}`)}, {Seq: 7, Kind: "item.completed"}}
			first := int64(19)
			if empty {
				events = nil
				first = 0
			}
			if _, err := h.app.RecordRunEvents(ctx, runPrincipal("original-run", protocol.RoleImplement), "original-run", events); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("queued run events: %v", err)
			}
			o := <-observed
			if o.Source.Operation != protocol.OpRunnerEvents || o.Source.RunID != "original-run" || o.Source.EventSequence != first || o.Source.RequestID != "original-request" || o.Source.TraceID != "original-trace" || o.Stage != "connection_wait" || o.Outcome != "deadline_exceeded" || o.Callback != 0 {
				t.Fatalf("run event queue identity: %+v", o)
			}
			raw, err := json.Marshal(o)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private_event_payload") {
				t.Fatal("event body leaked into diagnostic source")
			}
			if !strings.Contains(strings.Join(o.CallerStack, "\n"), "RecordRunEvents") {
				t.Fatal("run event caller lost", o.CallerStack)
			}
		})
	}
}
