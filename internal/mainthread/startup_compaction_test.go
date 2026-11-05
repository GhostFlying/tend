package mainthread

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestStartupHoldPreservesOriginalNativeCompactCompletion(t *testing.T) {
	f := newHandoffFixture(t)
	e := f.e
	if err := e.app.RecordAgentUsage(t.Context(), "main", protocol.AgentContextUsage{ThreadID: f.source.ID(), Revision: 1, Measurement: "observed", CompactSupported: true}); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		conversation, err := store.AgentConversation(t.Context(), tx.Q(), "main")
		revision = conversation.Revision
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request := e.ids.New(protocol.PrefixRequest)
	result, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: request, Operation: "conversation.compact", Target: protocol.Target{Type: "conversation", ID: "main"}, ExpectedControlRevision: &revision, Payload: json.RawMessage(`{}`)}, app.CompactConversation{})
	if err != nil || result.Status != protocol.OpAccepted {
		t.Fatal(result, err)
	}
	var compact struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(result.Result, &compact); err != nil || compact.ID == "" {
		t.Fatal(compact, err)
	}
	delivery, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok || delivery.Operation != "conversation.compact" {
		t.Fatal("fixture did not claim original compact", delivery, ok, err)
	}
	request = delivery.RequestID
	for _, state := range []string{"starting", "unknown"} {
		if err := e.app.SetCompactState(t.Context(), compact.ID, f.source.ID(), state); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.app.FinishDelivery(t.Context(), delivery, app.DeliveryResult{Outcome: app.Unknown}); err != nil {
		t.Fatal(err)
	}
	e.say("Keep the later owner input held until its technical action is resolved")
	var input store.OutboxMessage
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		input, err = store.HeadOutboxPrioritized(t.Context(), tx.Q(), app.MainThreadDestination, app.OpHandleEvent)
		return err
	}); err != nil || input.RequestID == request {
		t.Fatal("fixture lost later input", input, err)
	}
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := e.app.DeferAgentStartupAttachment(t.Context(), "main", app.AgentStartupAttachment{RequestID: input.RequestID}); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(31 * time.Second)
	}
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	var opens atomic.Int64
	native := f.source.(*handoffTestThread).Thread.(*testkit.FakeThread)
	m := New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{Instance: "main", Log: quiet(), Now: f.clock.Now, Open: func(context.Context) (harness.Session, error) {
		opens.Add(1)
		native.Compact("original-maintenance")
		return f.session, nil
	}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- m.serve(ctx) }()
	defer func() { cancel(); <-done }()
	eventually(t, "the original unknown compact receives its retained native completion", func() bool {
		state, err := e.app.CompactState(t.Context(), compact.ID)
		return err == nil && state == "completed"
	})
	if opens.Load() != 1 || len(e.h.Starts()) != 0 || e.count(`SELECT COUNT(*) FROM agent_compactions`) != 1 {
		t.Fatal("receipt attachment created a replacement operation or business turn", opens.Load(), e.h.Starts())
	}
	if d, _, ok, err := e.app.NextDelivery(t.Context(), app.MainThreadDestination); err != nil || ok {
		t.Fatal("compact completion admitted the held newer input", d, ok, err)
	}
	if e.count(`SELECT COUNT(*) FROM attentions WHERE status='open'`) != 1 || e.count(`SELECT COUNT(*) FROM main_turns`) != 0 {
		t.Fatal("native compact settlement changed the newer startup action or native intent")
	}
}
