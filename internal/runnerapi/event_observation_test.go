package runnerapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRunnerEventEnvelopeObservationRetainsRequestAndBusinessIdentity(t *testing.T) {
	e := newEnv(t, 0)
	e.createTask()
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", map[string]any{}))
	if as == nil {
		t.Fatal("no assignment")
	}
	payload := protocol.EventsPayload{Events: []protocol.RunEvent{{Seq: 19, Kind: "item.completed"}, {Seq: 7, Kind: "item.completed"}}}
	envelope := e.envelope(protocol.OpRunnerEvents, as.RunID, payload)
	envelope.TraceID = e.ids.New(protocol.PrefixTrace)
	observed := make(chan store.WriteObservation, 4)
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observed <- o })(e.store)
	for _, want := range []int{2, 0} {
		var result protocol.EventsResult
		if err := json.Unmarshal(e.mustOK(asRun(*as), envelope), &result); err != nil || result.Accepted != want {
			t.Fatalf("original event receipt accepted=%d want=%d err=%v", result.Accepted, want, err)
		}
		o := <-observed
		if o.Source.Operation != protocol.OpRunnerEvents || o.Source.RequestID != envelope.RequestID || o.Source.TraceID != envelope.TraceID || o.Source.RunID != as.RunID || o.Source.EventSequence != 19 || o.Outcome != "ok" {
			t.Fatalf("envelope/run attribution changed: %+v", o)
		}
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM run_logs WHERE run_id=? AND seq IN (7,19)`, as.RunID).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatal("diagnostics altered original event deduplication", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
