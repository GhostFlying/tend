package mainthread

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

func assistantItems(t *testing.T, e *env) []protocol.ConversationItem {
	t.Helper()
	var page protocol.ConversationPage
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		page, err = views.Conversation(context.Background(), tx.Q(), 0, 0, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var out []protocol.ConversationItem
	for _, it := range page.Items {
		if it.Author == "main_agent" && it.Tool == nil {
			out = append(out, it)
		}
	}
	return out
}

func TestStreamingReplyPersistsBeforeCompletion(t *testing.T) {
	h := testkit.NewHarness()
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		for _, fragment := range []string{"中文", "🙂", strings.Repeat("a", 200)} {
			th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: "reply", Text: fragment})
		}
		<-release
		th.Say(turn, "reply", "最终正文")
		return "completed"
	}
	e := newEnv(t, h)
	msg := e.say("hello")
	eventually(t, "partial body is durable while the turn is running", func() bool {
		rows := assistantItems(t, e)
		return len(rows) == 1 && rows[0].Body == "中文🙂"+strings.Repeat("a", 200) && rows[0].AssistantState == protocol.AssistantStreaming
	})
	first := assistantItems(t, e)[0]
	if first.ReplyTo != msg || first.AssistantRevision != 1 {
		t.Fatalf("first draft: %+v", first)
	}
	// Event views carry the cumulative durable item, including after a
	// client reconnects at an older cursor.
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		events, err := views.Events(context.Background(), tx.Q(), 0, 100)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if ev.ObjectID == first.MessageID && ev.Item != nil && ev.Item.Body == first.Body {
				return nil
			}
		}
		return fmt.Errorf("missing durable streaming event")
	}); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	eventually(t, "final authoritative body replaces draft", func() bool {
		rows := assistantItems(t, e)
		return len(rows) == 1 && rows[0].AssistantState == protocol.AssistantCompleted
	})
	final := assistantItems(t, e)[0]
	if final.MessageID != first.MessageID || final.Seq != first.Seq || final.CreatedAt != first.CreatedAt || final.Body != "最终正文" || final.AssistantRevision != 2 {
		t.Fatalf("final = %+v; first = %+v", final, first)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE object_id = ? AND kind = 'conversation.message'`, first.MessageID); n != 2 {
		t.Errorf("writes = %d, want 2 batched writes", n)
	}
}

func TestStreamingReplySurvivesStopFailureAndRestart(t *testing.T) {
	for _, ending := range []string{"interrupted", "failed", "restart", "completed"} {
		t.Run(ending, func(t *testing.T) {
			h := testkit.NewHarness()
			release := make(chan struct{})
			defer close(release)
			h.Turn = func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
				th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: "reply", Text: "keep partial🙂"})
				<-release
				return ending
			}
			e := newEnv(t, h)
			msg := e.say("long reply")
			eventually(t, "draft", func() bool { return len(assistantItems(t, e)) == 1 })
			first := assistantItems(t, e)[0]
			expected := protocol.AssistantState(ending)
			switch ending {
			case "interrupted":
				e.exec(owner, "conversation.interrupt_reply", protocol.Target{Type: "message", ID: msg}, protocol.InterruptReplyPayload{MessageID: msg}, app.InterruptReply{})
			case "restart":
				e.stop()
				expected = protocol.AssistantInterrupted
				e.start()
			case "completed":
				expected = protocol.AssistantUnknown
				release <- struct{}{}
			default:
				release <- struct{}{}
			}
			eventually(t, "partial settled", func() bool { rows := assistantItems(t, e); return len(rows) == 1 && rows[0].AssistantState == expected })
			final := assistantItems(t, e)[0]
			if final.Body != first.Body || final.MessageID != first.MessageID || final.AssistantRevision != first.AssistantRevision+1 {
				t.Fatalf("settled=%+v, first=%+v", final, first)
			}
		})
	}
}

func TestStreamingItemRejectsStaleFrames(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	p := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "thread"}
	record := func(body string, state protocol.AssistantState) {
		e.exec(p, "conversation.record_agent_message", protocol.Target{Type: "message"}, map[string]any{"body": body, "turn_id": "turn", "item_id": "item", "status": state}, app.RecordAgentMessage{})
	}
	record("prefix", protocol.AssistantStreaming)
	record("prefix plus", protocol.AssistantStreaming)
	record("prefix", protocol.AssistantStreaming)
	row := assistantItems(t, e)[0]
	if row.Body != "prefix plus" || row.AssistantRevision != 2 {
		t.Fatalf("stale frame replaced text: %+v", row)
	}
	record("final", protocol.AssistantCompleted)
	record("prefix plus stale", protocol.AssistantStreaming)
	record("different late final", protocol.AssistantCompleted)
	row = assistantItems(t, e)[0]
	if row.Body != "final" || row.AssistantRevision != 3 || row.AssistantState != protocol.AssistantCompleted {
		t.Fatalf("terminal revived: %+v", row)
	}
}

func TestStreamingBeforeStartResponseKeepsOrder(t *testing.T) {
	h := testkit.NewHarness()
	h.BeforeAnswer = func(th *testkit.FakeThread, turn string) {
		th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: "first", Text: "first draft"})
		th.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: turn, ItemID: "second", Text: "second draft"})
		th.UseTool(turn, "tool", harness.ToolUse{Name: "shell", Status: protocol.ToolRunning})
		th.Say(turn, "first", "first final")
		th.Say(turn, "second", "")
	}
	e := newEnv(t, h)
	msg := e.say("hello")
	eventually(t, "turn completed", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1) AND status = 'completed'`) == 1
	})
	rows := assistantItems(t, e)
	if len(rows) != 2 || rows[0].Body != "first final" || rows[1].Body != "" || rows[0].AssistantState != protocol.AssistantCompleted || rows[1].AssistantState != protocol.AssistantCompleted {
		t.Fatalf("rows: %+v", rows)
	}
	for _, row := range rows {
		if row.ReplyTo != msg {
			t.Fatalf("early draft lost owner: %+v", row)
		}
	}
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE kind = 'tool_use' AND seq > ?`, rows[1].Seq) != 1 {
		t.Fatal("tool must follow preceding draft messages")
	}
}
