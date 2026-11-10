package mainthread

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type prefixThread struct {
	harness.Thread
	ack func(context.Context, harness.Event, []int64) error
}

func TestNativeTerminalReplyFlushDoesNotRequirePrefixAcknowledgement(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	thread := &prefixThread{ack: func(context.Context, harness.Event, []int64) error {
		t.Fatal("real native terminal handling was intercepted by the closing-prefix hook")
		return nil
	}}
	event := harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: "original-turn", ItemID: "original-item", Text: "terminal text", CoordinatorSequence: 19}
	drafts := replyDrafts{}
	drafts.begin(event, e.m)
	drafts.consume(event)
	if err := e.m.flushReplies(t.Context(), thread, drafts, event.TurnID, protocol.AssistantUnknown); err != nil {
		t.Fatal("real terminal boundary could not proceed to ordinary acknowledgement", err)
	}
}

func TestClosingReplyPrefixDoesNotPublishSuppressedRepairOutput(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	source := e.say("original")
	request := e.ids.New(protocol.PrefixRequest)
	rec := store.MainTurn{RequestID: request, MessageID: source, ThreadID: "native-original", TurnID: "business-turn", Status: store.TurnStarted}
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnCompleted
	if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	repair := retainHistoricalRepairForTest(t, e, request)
	e.m.active = &turn{requestID: repair, messageID: source}
	thread := &prefixThread{ack: func(context.Context, harness.Event, []int64) error {
		t.Fatal("suppressed repair prose was treated as a visible durable reply")
		return nil
	}}
	event := harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: "repair-turn", ItemID: "repair-item", Text: "private repair prose", CoordinatorSequence: 19}
	drafts := replyDrafts{}
	drafts.begin(event, e.m)
	drafts.consume(event)
	if err := e.m.flushReplies(t.Context(), thread, drafts, event.TurnID, protocol.AssistantUnknown); err != nil {
		t.Fatal("suppressed repair blocked real native terminal handling", err)
	}
	if err := e.m.flushClosingReplies(t.Context(), thread, drafts, protocol.AssistantInterrupted); err != nil {
		t.Fatal("closing repair invented a visible output requirement", err)
	}
	if rows := assistantItems(t, e); len(rows) != 0 {
		t.Fatal("repair prose was published", rows)
	}
}

type boundaryPrefixSession struct{ *testkit.FakeSession }
type boundaryPrefixThread struct{ *testkit.FakeThread }

func (s boundaryPrefixSession) StartThread(ctx context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	thread, err := s.FakeSession.StartThread(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return boundaryPrefixThread{thread.(*testkit.FakeThread)}, nil
}

func (s boundaryPrefixSession) ResumeThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	thread, err := s.FakeSession.ResumeThread(ctx, id, cfg)
	if err != nil {
		return nil, err
	}
	return boundaryPrefixThread{thread.(*testkit.FakeThread)}, nil
}

func (boundaryPrefixThread) AcknowledgeReplyPrefix(context.Context, harness.Event, []int64) error {
	return errors.New("buffered output has no visible terminal message yet")
}

func TestBackgroundNativeTerminalSettlesBeforeVisiblePrefixIsRequired(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		if result := th.CallTool(t.Context(), turn, "sources", "background_sources", map[string]any{}); !result.Success {
			t.Error(result.Text)
			return "failed"
		}
		if result := th.CallTool(t.Context(), turn, "silent", "background_checkin", map[string]any{"note": "Waiting for the existing owner decision"}); !result.Success {
			t.Error(result.Text)
			return "failed"
		}
		for i, text := range []string{"retained", " background", " reply"} {
			th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: "background-item", Text: text, CoordinatorSequence: int64(17 + i)})
		}
		return "completed"
	}
	e := newEnv(t, h, func(cfg *Config) {
		cfg.Open = func(ctx context.Context) (harness.Session, error) {
			session, err := h.Open(ctx)
			if err != nil {
				return nil, err
			}
			return boundaryPrefixSession{session.(*testkit.FakeSession)}, nil
		}
	})
	openWakeDecision(e)
	eventually(t, "background native terminal settles normally", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1) AND status='completed'`) == 1
	})
	if e.count(`SELECT COUNT(*) FROM background_batches WHERE status='completed' AND checkin_json<>''`) != 1 || e.count(`SELECT COUNT(*) FROM background_replies WHERE body='retained background reply'`) != 1 {
		t.Fatal("real terminal failed to settle retained background text")
	}
	if rows := assistantItems(t, e); len(rows) != 0 {
		t.Fatal("silent background text was made visible", rows)
	}
}

func (*prefixThread) ID() string { return "native-original" }
func (t *prefixThread) AcknowledgeReplyPrefix(ctx context.Context, event harness.Event, consumed []int64) error {
	return t.ack(ctx, event, consumed)
}

func TestFault_ManagerTerminalReplyAcknowledgesOnlyDurableDeltaPrefix(t *testing.T) {
	for _, state := range []protocol.AssistantState{protocol.AssistantInterrupted, protocol.AssistantUnknown} {
		t.Run(string(state), func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			drafts := replyDrafts{}
			for i, text := range []string{"original", "尾部", "🙂"} {
				event := harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: "original-turn", ItemID: "original-item", Text: text, CoordinatorSequence: int64(17 + i)}
				drafts.begin(event, e.m)
				drafts.consume(event)
			}
			attempts := 0
			ackFailure := errors.New("original prefix acknowledgement unavailable")
			thread := &prefixThread{ack: func(_ context.Context, event harness.Event, consumed []int64) error {
				attempts++
				rows := assistantItems(t, e)
				if len(rows) != 1 || rows[0].Body != "original尾部🙂" || rows[0].AssistantState != state {
					t.Fatalf("ack preceded durable terminal text: %+v", rows)
				}
				if !slices.Equal(consumed, []int64{17, 18, 19}) || event.CoordinatorSequence != 19 || event.Kind != harness.EventAgentMessageDelta || event.TurnID != "original-turn" || event.ItemID != "original-item" {
					t.Fatalf("prefix lost original last event identity: %+v", event)
				}
				if attempts == 1 {
					return ackFailure
				}
				return nil
			}}
			if err := e.m.flushReplies(t.Context(), thread, drafts, "", ""); err != nil || attempts != 0 {
				t.Fatalf("ordinary streaming flush acknowledged replayable prefix: attempts=%d err=%v", attempts, err)
			}
			failedCtx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := e.m.flushClosingReplies(failedCtx, thread, drafts, state); err == nil || attempts != 0 {
				t.Fatalf("failed terminal save acknowledged prefix: attempts=%d err=%v", attempts, err)
			}
			if err := e.m.flushClosingReplies(t.Context(), thread, drafts, state); !errors.Is(err, ackFailure) {
				t.Fatalf("terminal save did not preserve acknowledgement failure: %v", err)
			}
			if err := e.m.flushClosingReplies(t.Context(), thread, drafts, state); err != nil || attempts != 2 {
				t.Fatalf("same durable prefix could not retry acknowledgement: attempts=%d err=%v", attempts, err)
			}
		})
	}
}
