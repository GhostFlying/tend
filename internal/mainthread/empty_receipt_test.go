package mainthread

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestReconnectSettlesEmptyNativeReplyWithoutCreatingMessage(t *testing.T) {
	for _, mode := range []string{"empty only", "progress then empty", "clear streamed reply"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			source := e.say("Continue the original responsibility")
			session, err := e.h.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			base, err := session.StartThread(t.Context(), harness.ThreadConfig{})
			if err != nil {
				t.Fatal(err)
			}
			th := &receiptThread{Thread: base, found: true}
			if err := e.app.RegisterAgentThread(t.Context(), "main", th.ID()); err != nil {
				t.Fatal(err)
			}
			rec := store.MainTurn{RequestID: e.ids.New(protocol.PrefixRequest), MessageID: source, ThreadID: th.ID(), TurnID: "original-turn", Status: store.TurnStarted}
			if err := e.app.PutMainTurn(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			if mode == "clear streamed reply" {
				partial := harness.Event{TurnID: rec.TurnID, ItemID: "final", ItemType: harness.ItemAgentMessage, Text: "An unfinished prefix"}
				if err := e.m.recordReplyState(t.Context(), th, partial, protocol.AssistantStreaming); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.app.SettleOpenTurns(t.Context(), th.ID()); err != nil {
				t.Fatal(err)
			}
			app.WithForwardRecoveryStartup("empty-reply-recovery")(e.app)
			items := []harness.Event{}
			if mode == "progress then empty" {
				items = append(items, harness.Event{TurnID: rec.TurnID, ItemID: "progress", ItemType: harness.ItemAgentMessage, Text: "Existing review is ready"})
			}
			items = append(items, harness.Event{TurnID: rec.TurnID, ItemID: "final", ItemType: harness.ItemAgentMessage})
			raw := json.RawMessage(`{"id":"original-turn","status":"completed","items":[{"type":"agentMessage","id":"final","text":""}]}`)
			th.receipt = harness.TurnReceipt{ID: rec.TurnID, Status: "completed", Raw: raw, Items: items}
			for range 2 {
				if err := e.m.reconcileNativeReceipts(t.Context(), th); err != nil {
					t.Fatal(err)
				}
			}
			state, err := e.app.LogicalTurn(t.Context(), rec.RequestID)
			if err != nil || state.Status != "completed" || state.RepairAttempts != 0 {
				t.Fatal(state, err)
			}
			want := 0
			if mode != "empty only" {
				want = 1
			}
			if got := e.count(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id=? AND turn_id=?`, th.ID(), rec.TurnID); got != want {
				t.Fatalf("published replies = %d, want %d", got, want)
			}
			if mode == "clear streamed reply" && e.count(`SELECT COUNT(*) FROM conversation_agent_messages a JOIN conversation_messages m USING(message_id) WHERE a.thread_id=? AND a.turn_id=? AND a.item_id='final' AND a.status='completed' AND m.body=''`, th.ID(), rec.TurnID) != 1 {
				t.Fatal("authoritative empty terminal did not replace the streamed reply")
			}
			if e.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, rec.RequestID) != 0 || e.count(`SELECT COUNT(*) FROM agent_rollout_events WHERE thread_id=? AND turn_id=? AND kind='native_receipt'`, th.ID(), rec.TurnID) != 1 {
				t.Fatal("original receipt was not retained and settled exactly once")
			}
			if len(e.h.Starts()) != 0 || e.count(`SELECT COUNT(*) FROM logical_turn_executions WHERE repair=1`) != 0 {
				t.Fatal("receipt reconciliation replayed model execution")
			}
		})
	}
}
