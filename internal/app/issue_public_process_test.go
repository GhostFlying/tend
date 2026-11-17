package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestBackgroundPublicProcessReadsActualProducerBeforePublication(t *testing.T) {
	for _, terminal := range []string{store.TurnCompleted, store.TurnFailed, store.TurnInterrupted} {
		t.Run(terminal, func(t *testing.T) {
			h := newHarness(t)
			w := h.createWork(workPlan())
			h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
			d, turn := h.startBackground()
			start := h.clock.Now().UnixMilli()
			record := func(item string, status protocol.ToolStatus, output string) {
				t.Helper()
				op := h.exec(mainAgent, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: item, Tool: "exec_command", Input: `{"cmd":"pwd"}`, Output: output, Status: status}, nil), RecordToolUse{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			// Actual public producer commands populate deferred records, not fixture
			// items inserted into an API block. Prose remains unpublished while frozen.
			h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "draft", Body: "private until batch disposition"}, nil), RecordAgentMessage{})
			for i := 0; i < 3; i++ {
				record(fmt.Sprint(i), protocol.ToolRunning, "")
			}
			record("0", protocol.ToolFailed, "actual command failure")
			record("0", protocol.ToolRunning, "late start")
			read := func(after int64) *protocol.IssueTurnProcess {
				t.Helper()
				var got *protocol.IssueTurnProcess
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					got, err = views.IssuePublicProcess(t.Context(), tx.Q(), turn.MessageID, after, 2)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return got
			}
			first := read(0)
			if first == nil || len(first.Items) != 2 || !first.More || first.Items[0].Tool.Name != "exec_command" || first.Items[0].Tool.Status != protocol.ToolFailed || first.Items[0].Tool.Output != "actual command failure" || first.StartedAt != start || first.EndedAt != 0 {
				t.Fatalf("live process=%+v", first)
			}
			second := read(first.Next)
			if len(second.Items) != 1 || second.More || second.Items[0].MessageID == first.Items[0].MessageID {
				t.Fatal("process cursor duplicated/lost tool", second)
			}
			if h.countWhere(`SELECT COUNT(*) FROM conversation_messages WHERE reply_to=?`, turn.MessageID) != 0 {
				t.Fatal("read published deferred tool or prose")
			}
			// A new App/session reads the same durable source without replaying it.
			h.app = New(h.store, h.ids, h.clock.Now, WithFollowups(true))
			if read(0).Items[0].MessageID != first.Items[0].MessageID {
				t.Fatal("reconnect changed public identity")
			}
			if terminal == store.TurnCompleted {
				if op := h.backgroundCall(d, turn, "sources", map[string]any{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "no user notification required"}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			h.clock.Advance(7 * time.Second)
			turn.Status = terminal
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			final := read(0)
			if final.Items[0].MessageID != first.Items[0].MessageID || final.EndedAt != start+7000 || final.Items[1].Tool.Status != protocol.ToolInterrupted {
				t.Fatalf("settled process=%+v", final)
			}
			record("1", protocol.ToolCompleted, "late result")
			record("1", protocol.ToolRunning, "late start")
			if got := read(0).Items[1]; got.Tool.Status != protocol.ToolCompleted || got.Tool.Output != "late result" {
				t.Fatal(got)
			}
			if terminal == store.TurnCompleted && h.countWhere(`SELECT COUNT(*) FROM conversation_messages WHERE body='private until batch disposition'`) != 0 {
				t.Fatal("silent check-in published prose")
			}
		})
	}
}

func TestManagerCallbackFirstEnrichesOriginalToolWithoutRevivingResult(t *testing.T) {
	h, _, _, as, source := reviewManagerAssignment(t)
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: "native-execution", MessageID: source, ThreadID: "native", TurnID: "turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	payload := mustJSON(t, map[string]any{"thread_id": "native", "turn_id": "turn", "call_id": "call", "tool_result": map[string]any{"Success": false, "Text": "confirmed failure"}})
	for range 2 {
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			return tx.PutManagerToolResult(t.Context(), as.RunID, "callback", string(payload), true, h.clock.Now().UnixMilli())
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []protocol.ToolStatus{protocol.ToolRunning, protocol.ToolCompleted, protocol.ToolRunning} {
		op := h.exec(protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{ReplyTo: source, TurnID: "turn", ItemID: "call", Tool: "issues_read", Input: `{}`, Output: "late event", Status: status}, nil), RecordToolUse{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		u, err := store.GetToolUse(t.Context(), tx.Q(), "turn", "call")
		if err != nil {
			return err
		}
		m, err := store.GetConversationItem(t.Context(), tx.Q(), u.MessageID)
		if err != nil {
			return err
		}
		if u.Tool != "issues_read" || u.Input != "{}" || u.Status != "failed" || u.Output != "confirmed failure" || m.ReplyTo != source || m.Author != "main_agent" {
			t.Fatal("callback metadata/result lost", u, m)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
