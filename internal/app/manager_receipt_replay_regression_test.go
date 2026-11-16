package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestFault_CommandBackgroundCompletionRetainsBothOriginals(t *testing.T) {
	h := newHarness(t)
	background := json.RawMessage(`{"id":"command","type":"commandExecution","command":"make check","cwd":"/workspace","processId":"process","status":"backgrounded","durationMs":120000,"exitCode":0,"aggregatedOutput":null}`)
	completed := json.RawMessage(`{"id":"command","type":"commandExecution","command":"make check","cwd":"/workspace","processId":"process","status":"completed","durationMs":151057,"exitCode":0,"aggregatedOutput":"passed"}`)
	for _, body := range []json.RawMessage{background, completed, background, completed} {
		if err := h.app.RecordRolloutEvent(t.Context(), "main", "thread", "turn", "command", "item_completed", body); err != nil {
			t.Fatal("same command background/terminal progression was rejected", err)
		}
	}
	if h.count("agent_rollout_events") != 2 {
		t.Fatal("command stages were overwritten or replayed twice")
	}
	for _, changed := range []json.RawMessage{
		json.RawMessage(strings.Replace(string(completed), `"passed"`, `"different"`, 1)),
		json.RawMessage(strings.Replace(string(completed), `"process"`, `"other"`, 1)),
		json.RawMessage(strings.Replace(string(completed), `"make check"`, `"other command"`, 1)),
	} {
		if err := h.app.RecordRolloutEvent(t.Context(), "main", "thread", "turn", "command", "item_completed", changed); protocol.CodeOf(err) != protocol.CodeConflictPayload {
			t.Fatal("real command identity or terminal conflict was accepted", err)
		}
	}
}

func TestFault_ReceiptRecoveryExhaustionParksOriginalTransport(t *testing.T) {
	h := newHarness(t)
	message, delivery := h.say("Original native work")
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: message, ThreadID: "native", TurnID: "turn", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	attachment := AgentStartupAttachment{RequestID: turn.RequestID, Reconciliation: true}
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := h.app.DeferAgentStartupAttachment(t.Context(), "main", attachment); err != nil {
			t.Fatal(err)
		}
		if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || allowed {
			t.Fatal("failed original receipt attachment ignored its own retry budget", allowed, err)
		}
		h.clock.Advance(31 * time.Second)
	}
	h.app = New(h.store, h.ids, h.clock.Now)
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || allowed {
		t.Fatal("restart discarded exhausted receipt recovery state", allowed, err)
	}
	attention := startupAttention(t, h)
	if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, turn.RequestID) != 1 || h.count("main_turns") != 1 {
		t.Fatal("parking lost the original receipt or created another model turn")
	}
	op := h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: attention.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Original replay conflict repaired; continue reading the same receipt"}, rev(attention.Revision)), AttentionCommand{Action: "confirm"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || !allowed {
		t.Fatal("handled original problem did not permit receipt recovery", allowed, err)
	}
}

func TestFault_ManagerReceiptRecoveryBudgetPreventsNewRun(t *testing.T) {
	h, as, _, _, pending := receiptRecoveryFixture(t)
	// This retained-run fixture presets native execution. Supply the matching
	// original delivery that a real admitted turn already retains.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		turn, err := store.GetMainTurn(t.Context(), tx.Q(), pending)
		if err != nil {
			return err
		}
		body, err := json.Marshal(HandleMessage{MessageID: turn.MessageID})
		if err != nil {
			return err
		}
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: h.ids.New(protocol.PrefixOutbox), RequestID: pending, Destination: store.ConversationDestination(as.Manager.ConversationID), Operation: OpHandleMessage, PayloadJSON: string(body), CreatedAt: h.clock.Now().UnixMilli()})
	}); err != nil {
		t.Fatal(err)
	}
	attachment := AgentStartupAttachment{RequestID: pending, Reconciliation: true}
	if err := h.app.DeferAgentStartupAttachment(t.Context(), as.Manager.ConversationID, attachment); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		m, err := store.GetIssueManagerTask(t.Context(), tx.Q(), as.TaskID)
		if err != nil {
			return err
		}
		due, err := views.ManagerReceiptDue(t.Context(), tx.Q(), m, h.clock.Now().UnixMilli())
		if err == nil && due {
			t.Fatal("scheduler ignored original receipt recovery backoff")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
