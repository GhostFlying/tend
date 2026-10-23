package runner

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFreshHelloRetriesOnlyConfirmedStaleObservation(t *testing.T) {
	for _, stale := range []bool{true, false} {
		t.Run(map[bool]string{true: "stale", false: "retired"}[stale], func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
			r.cfg.RetryDelay = time.Millisecond
			r.c.log = slog.Default()
			var requests []protocol.Envelope
			var sequences []int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				json.NewDecoder(q.Body).Decode(&env)
				requests = append(requests, env)
				var payload protocol.HelloPayload
				json.Unmarshal(env.Payload, &payload)
				sequences = append(sequences, payload.Runtime.Sequence)
				if len(requests) == 1 {
					e := &protocol.Error{Code: protocol.CodeConflictPayload, Message: "rejected"}
					if stale {
						e.Details = json.RawMessage(`{"reason":"stale_sequence"}`)
					}
					json.NewEncoder(w).Encode(rpcResponse{Error: e})
					return
				}
				result, _ := json.Marshal(protocol.HelloResult{})
				json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: result}})
			}))
			defer server.Close()
			r.c.base = server.URL
			r.c.http = server.Client()
			err := r.freshHello(t.Context())
			if stale {
				if err != nil {
					t.Fatal(err)
				}
				if len(requests) != 2 || requests[0].RequestID == requests[1].RequestID || sequences[1] <= sequences[0] {
					t.Fatalf("did not refresh: %+v %v", requests, sequences)
				}
			} else if err == nil || len(requests) != 1 {
				t.Fatal("retired incarnation retried")
			}
		})
	}
}
