package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestUpgradePreservesQueuedOwnerMessagesAndFinishesExistingTurn(t *testing.T) {
	h, c := upgradeHarness(t)
	first, delivery := h.say("finish the current turn")
	running := store.MainTurn{RequestID: delivery.RequestID, MessageID: first, ThreadID: "thread-original", TurnID: "turn-original", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	if op := startTestUpgrade(h, "upgrade-messages", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	request := h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "preserve this queued owner message"}, nil)
	op := h.exec(owner, request, SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	next := store.MainTurn{RequestID: h.ids.New(protocol.PrefixRequest), MessageID: result.MessageID, ThreadID: "thread-original", Status: store.TurnStarting}
	if err := h.app.PutMainTurn(t.Context(), next); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("new turn during drain: %v", err)
	}
	if _, exists, err := h.app.MainTurn(t.Context(), next.RequestID); err != nil || exists {
		t.Fatalf("rejected turn persisted: %v %v", exists, err)
	}
	running.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), running); err != nil {
		t.Fatalf("current turn could not finish: %v", err)
	}
	if msg, err := h.app.Message(t.Context(), result.MessageID); err != nil || msg.Body != "preserve this queued owner message" {
		t.Fatalf("queued message lost: %+v %v", msg, err)
	}
	batch := upgradeRead(h)
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.PutMainTurn(t.Context(), next); err != nil {
		t.Fatalf("queued turn could not resume: %v", err)
	}
}
