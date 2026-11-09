package mainthread

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type collectionFaultThread struct {
	*handoffTestThread
	source   nativeusage.Source
	compacts int
}

func (t *collectionFaultThread) UsageSource() nativeusage.Source { return t.source }
func (t *collectionFaultThread) Compact(context.Context) error {
	t.compacts++
	return harness.ErrRejected
}
func collectionFaultFixtureThread(f *handoffFixture) *collectionFaultThread {
	return &collectionFaultThread{handoffTestThread: f.source.(*handoffTestThread), source: nativeusage.Source{Path: "missing.jsonl", Provider: "codex", ThreadID: f.source.ID(), NoDispatchProven: true, WriterID: "writer"}}
}
func TestUsageRegistrationFailureRecoversBeforeDispatch(t *testing.T) {
	f := newHandoffFixture(t)
	m := f.e.m
	th := collectionFaultFixtureThread(f)
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), "CREATE TRIGGER fail_register BEFORE INSERT ON token_usage_local_sources BEGIN SELECT RAISE(ABORT,'injected'); END")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	m.registerNativeUsage(t.Context(), th, "")
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error { _, e := tx.Exec(t.Context(), "DROP TRIGGER fail_register"); return e }); err != nil {
		t.Fatal(err)
	}
	if err := m.beginNativeUsage(t.Context(), th, "next-input"); err != nil {
		t.Fatalf("dispatch still blocked after registration storage recovered: %v", err)
	}
}
func TestUnsentCompactUsageFailureRemainsRetryable(t *testing.T) {
	f := newHandoffFixture(t)
	e := f.e
	m := e.m
	th := collectionFaultFixtureThread(f)
	m.thread = th
	m.registerNativeUsage(t.Context(), th, "")
	if err := e.app.RecordAgentUsage(t.Context(), "main", protocol.AgentContextUsage{ThreadID: th.ID(), Revision: 1, Measurement: "observed", CompactSupported: true}); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		c, err := store.AgentConversation(t.Context(), tx.Q(), "main")
		revision = c.Revision
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "conversation.compact", Target: protocol.Target{Type: "conversation", ID: "main"}, ExpectedControlRevision: &revision, Payload: json.RawMessage("{}")}, app.CompactConversation{})
	if err != nil || result.Status != protocol.OpAccepted {
		t.Fatal(result, err)
	}
	d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok || d.Operation != "conversation.compact" {
		t.Fatal(d, ok, err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), "CREATE TRIGGER fail_activate BEFORE UPDATE ON token_usage_local_sources BEGIN SELECT RAISE(ABORT,'injected'); END")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	first := m.deliverCompact(t.Context(), d)
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error { _, e := tx.Exec(t.Context(), "DROP TRIGGER fail_activate"); return e }); err != nil {
		t.Fatal(err)
	}
	second := m.deliverCompact(t.Context(), d)
	var p struct {
		ID string `json:"operation_id"`
	}
	_ = json.Unmarshal(d.Payload, &p)
	state, _ := e.app.CompactState(t.Context(), p.ID)
	if second.Outcome == app.Unknown && th.compacts == 0 {
		t.Fatalf("never-sent compact became irreconcilable: first=%+v second=%+v state=%s native calls=%d", first, second, state, th.compacts)
	}
}
