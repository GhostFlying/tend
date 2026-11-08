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

func TestUncertainCompactResumeRejectionCannotReplaceThread(t *testing.T) {
	f := newHandoffFixture(t)
	f.e.m.cfg.MaintenanceEnabled = false
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,before_json,created_at,updated_at) VALUES('original-compact','main',?,'unknown','manual','{}',0,0)`, f.source.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.session.resumeErr = map[string]error{f.source.ID(): harness.ErrRejected}
	_, err := f.e.m.openThread(t.Context(), f.session)
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("uncertain compact allowed a replacement thread", err)
	}
	if state, err := f.e.app.CompactState(t.Context(), "original-compact"); err != nil || state != "unknown" {
		t.Fatal(state, err)
	}
}

func TestUncertainCompactResumesOriginalThreadInsteadOfCurrentBinding(t *testing.T) {
	f := newHandoffFixture(t)
	f.e.m.cfg.MaintenanceEnabled = false
	original := f.source.ID()
	replacement, err := f.session.StartThread(t.Context(), f.e.m.threadConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.SetMainThread(t.Context(), "main", replacement.ID()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,before_json,created_at,updated_at) VALUES('original-compact','main',?,'unknown','manual','{}',0,0)`, original)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := f.e.m.openThread(t.Context(), f.session)
	if err != nil || restored.ID() != original {
		t.Fatal("compact read a newer binding instead of its original native thread", restored, err)
	}
}

func seedUnknownCompact(t *testing.T, f *handoffFixture) {
	t.Helper()
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,before_json,created_at,updated_at) VALUES('original-compact','main',?,'unknown','manual','{}',0,0)`, f.source.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompactRecoverySessionEndsAfterOriginalCompletion(t *testing.T) {
	for _, event := range []bool{true, false} {
		t.Run(map[bool]string{true: "native_event", false: "already_durable"}[event], func(t *testing.T) {
			f := newHandoffFixture(t)
			seedUnknownCompact(t, f)
			var compactAck atomic.Bool
			f.session.mu.Lock()
			f.session.threads[f.source.ID()] = &acknowledgedCompactThread{handoffTestThread: f.source.(*handoffTestThread), acked: &compactAck}
			f.session.mu.Unlock()
			retained := &retainedCompactSession{f.session}
			f.e.m.cfg.Open = func(context.Context) (harness.Session, error) { return retained, nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- f.e.m.serve(ctx) }()
			eventually(t, "original compact recovery session attaches", func() bool { f.e.m.mu.Lock(); defer f.e.m.mu.Unlock(); return f.e.m.thread != nil })
			if event {
				f.source.(*handoffTestThread).Thread.(*testkit.FakeThread).Compact("original-completion")
			} else if err := f.e.app.CompleteNativeCompact(t.Context(), "main", f.source.ID(), "original-completion"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				cancel()
				<-done
				t.Fatal("settled compact session remained ready with its old toolset")
			}
			if event && !compactAck.Load() {
				t.Fatal("compact attachment exited before acknowledging its durable completion")
			}
			if state, err := f.e.app.CompactState(t.Context(), "original-compact"); err != nil || state != "completed" {
				t.Fatal(state, err)
			}
			if len(f.e.h.Starts()) != 0 {
				t.Fatal("recovery attachment started business work", f.e.h.Starts())
			}
			f.saveSummary(t)
			next, err := f.e.m.openThread(t.Context(), retained)
			if err != nil {
				t.Fatal("settled compact did not re-enter normal toolset startup", err)
			}
			if next.ID() == f.source.ID() {
				t.Fatal("normal startup retained the old toolset thread after compact settlement")
			}
			binding, known, err := f.e.app.MainThreadBinding(t.Context(), "main")
			hash, hashErr := f.e.m.currentToolsetHash()
			if err != nil || hashErr != nil || !known || binding.ThreadID != next.ID() || binding.ToolsetHash != hash {
				t.Fatal("toolset handoff did not commit the new binding", binding, err, hashErr)
			}

		})
	}
}

func TestSettledCompactAttachmentStillRejectsFreshBusiness(t *testing.T) {
	f := newHandoffFixture(t)
	seedUnknownCompact(t, f)
	f.e.say("Do not start this input on the old recovery toolset")
	delivery, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(delivery, ok, err)
	}
	th, err := f.e.m.openThread(t.Context(), f.session)
	if err != nil {
		t.Fatal(err)
	}
	f.e.m.mu.Lock()
	f.e.m.session, f.e.m.thread = f.session, th
	close(f.e.m.ready)
	f.e.m.mu.Unlock()
	if err := f.e.app.CompleteNativeCompact(t.Context(), "main", f.source.ID(), "durable-completion"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := f.e.m.Deliver(ctx, delivery)
	if result.Outcome != app.Retry || len(f.e.h.Starts()) != 0 {
		t.Fatal("fresh business escaped a compact recovery attachment before normal startup", result, f.e.h.Starts())
	}
}

type retainedCompactSession struct{ *handoffTestSession }

func (s *retainedCompactSession) Close() error { return nil }

type acknowledgedCompactThread struct {
	*handoffTestThread
	acked *atomic.Bool
}

func (t *acknowledgedCompactThread) AcknowledgeEvent(_ context.Context, event harness.Event) error {
	if event.Kind == harness.EventItemCompleted && event.ItemType == harness.ItemContextCompaction {
		t.acked.Store(true)
	}
	return nil
}

func TestCompactResumeQuarantinesSynchronousToolCallback(t *testing.T) {
	f := newHandoffFixture(t)
	f.e.m.cfg.MaintenanceEnabled = false
	message := f.e.say("Retain the original callback identity")
	delivery, _, ok, err := f.e.app.NextDelivery(t.Context(), app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(delivery, ok, err)
	}
	rec := store.MainTurn{RequestID: delivery.RequestID, MessageID: message, ThreadID: f.source.ID(), TurnID: "retained-turn"}
	for _, state := range []string{store.TurnStarting, store.TurnStarted, store.TurnCompleted} {
		rec.Status = state
		if err := f.e.app.PutMainTurn(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.e.app.FinishDelivery(t.Context(), delivery, app.DeliveryResult{Outcome: app.Delivered}); err != nil {
		t.Fatal(err)
	}
	call := harness.ToolCall{ThreadID: f.source.ID(), TurnID: rec.TurnID, CallID: "buffered-call", Tool: "agent_conversations_list", Arguments: json.RawMessage(`{}`)}
	if result := f.e.m.onToolCall(t.Context(), call); !result.Success {
		t.Fatal("fixture did not reach the ordinary tool service", result)
	}

	seedUnknownCompact(t, f)
	called := false
	var result harness.ToolResult
	f.session.resumeHook = func(ctx context.Context, id string, cfg harness.ThreadConfig) {
		called = true
		call.ThreadID = id
		result = cfg.OnToolCall(ctx, call)
	}
	if _, err := f.e.m.openThread(t.Context(), f.session); err != nil {
		t.Fatal(err)
	}
	if !called || result.Success {
		t.Fatal("buffered resume callback reached the tool service before quarantine", called, result)
	}
}
