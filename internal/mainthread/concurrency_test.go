package mainthread

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type concurrencyCompactThread struct {
	harness.Thread
	called bool
}

func (t *concurrencyCompactThread) ContextUsage(context.Context) (protocol.AgentContextUsage, error) {
	return protocol.AgentContextUsage{Available: true, CompactSupported: true}, nil
}
func (t *concurrencyCompactThread) Compact(context.Context) error {
	t.called = true
	return harness.ErrRejected
}
func TestManualCompactionHonorsGroupCapacity(t *testing.T) {
	f := newHandoffFixture(t)
	sel := hostedconfig.Selection{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "env", LocalID: "agent"}, Runtime: agentconfig.RuntimeBinding{ID: "runtime"}}
	_, err := f.e.app.AcquireAuxiliaryConcurrency(t.Context(), "other-conversation", &sel)
	if err != nil {
		t.Fatal(err)
	}
	configure := func(action string, revision int64, p protocol.ConfigureConcurrencyPayload) {
		raw, _ := json.Marshal(p)
		op, err := f.e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: f.e.ids.New(protocol.PrefixRequest), Operation: "concurrency." + action, Target: protocol.Target{Type: "concurrency", ID: "shared"}, ExpectedControlRevision: &revision, Payload: raw}, app.ConfigureConcurrency{Action: action})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	configure("create", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
	configure("bind", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "env", RuntimeID: "runtime"})
	f.e.m.namedMu.Lock()
	f.e.m.named = &sel
	f.e.m.namedMu.Unlock()
	th := &concurrencyCompactThread{Thread: f.source}
	f.e.m.mu.Lock()
	f.e.m.thread = th
	f.e.m.mu.Unlock()
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,status,source,before_json,created_at,updated_at) VALUES('compact-test','main','queued','manual','{}',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result := f.e.m.deliverCompact(t.Context(), app.Delivery{Payload: json.RawMessage(`{"operation_id":"compact-test"}`)})
	if th.called {
		t.Fatalf("native compact was sent while its shared group was full: %+v", result)
	}
}
