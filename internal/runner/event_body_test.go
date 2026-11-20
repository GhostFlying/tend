package runner

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Exercise the journal with the same nested command output and native identity
// used by the worker's event receiver. Oversized data remains readable after Done.
func TestSettledRunRetainsCompleteEventUntilAcknowledged(t *testing.T) {
	r, as, _, _ := archiveRunner(t)
	as.EventSync = true
	j := r.cfg.Journal
	ctx := t.Context()
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"item_id": "call", "data": map[string]any{"type": "commandExecution", "exitCode": 0, "aggregatedOutput": strings.Repeat("界<", 1<<20)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{body, json.RawMessage(`{"status":"completed"}`)} {
		if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{NativeID: "id", Kind: "item_completed", Body: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Done(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	pending, err := j.SettledEventRuns(ctx)
	if err != nil || len(pending) != 1 || pending[0].RunID != as.RunID {
		t.Fatalf("lost pending execution: %v %v", pending, err)
	}
	batch, err := j.PendingEventBatch(ctx, as.RunID, 200, maxBatchBody)
	if err != nil || len(batch) != 1 || !bytes.Equal(batch[0].Body, body) {
		t.Fatalf("incomplete original or unbounded batch: %d %v", len(batch), err)
	}
	if err := j.AckEvents(ctx, as.RunID, 2); err != nil {
		t.Fatal(err)
	}
	pending, err = j.SettledEventRuns(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("acknowledged queue: %v %v", pending, err)
	}
}
