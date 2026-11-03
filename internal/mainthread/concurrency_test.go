package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type unknownConcurrencySummary struct {
	harness.Thread
	interruptErr error
}

func (*unknownConcurrencySummary) ID() string { return "unknown-summary" }
func (*unknownConcurrencySummary) StartTurn(context.Context, harness.TurnInput) (string, error) {
	return "", context.DeadlineExceeded
}
func (s *unknownConcurrencySummary) Interrupt(context.Context, string) error {
	return s.interruptErr
}

type unsubscribeSummary struct{}

func (unsubscribeSummary) ForkThread(context.Context, string, harness.ThreadConfig) (harness.Thread, error) {
	panic("unused")
}
func (unsubscribeSummary) ReleaseThread(context.Context, string) error { return nil }
func TestSummaryUnconfirmedInterruptRetainsConcurrency(t *testing.T) {
	for name, interruptErr := range map[string]error{"response_lost": errors.New("interrupt response lost"), "acknowledged_without_terminal_receipt": nil} {
		t.Run(name, func(t *testing.T) {
			f := newHandoffFixture(t)
			sel := hostedconfig.Selection{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "env", LocalID: "agent"}, Runtime: agentconfig.RuntimeBinding{ID: "runtime"}}
			id, err := f.e.app.AcquireAuxiliaryConcurrency(t.Context(), "main", &sel)
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
			forker := concurrencyForker{ThreadForker: unsubscribeSummary{}, permit: id}
			_, err = f.e.m.collectSummary(t.Context(), &unknownConcurrencySummary{interruptErr: interruptErr}, forker, store.HandoffSnapshot{}, "summary", harness.ModelSelection{})
			if err == nil {
				t.Fatal("expected unknown start")
			}
			next, err := f.e.app.AcquireAuxiliaryConcurrency(t.Context(), "main", &sel)
			if !errors.Is(err, app.ErrConcurrencyCapacity) {
				t.Fatalf("unknown summary was released solely by unsubscribe, allowing %q: %v", next, err)
			}
		})
	}
}

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
