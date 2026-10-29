package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEndToEnd_WebStopsBackgroundEventTurn(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	agent.Turn = func(_ *testkit.FakeThread, _ string, in harness.TurnInput) string {
		if strings.HasPrefix(in.Text, "[Tend ledger event ") {
			return "" // The owner must stop this background reply.
		}
		return "completed"
	}
	srv.startHistoricalMainThread(agent, agent.Turn)
	web := srv.webLogin(srv.startWeb().URL)
	stream := web.stream("")
	n := newNode(t, srv, passing)
	n.ws.noChanges = true
	n.start()
	srv.createTask()
	events := stream.until("background reply starts", func(e protocol.LedgerEvent) bool {
		return e.Item != nil && e.Item.Author == "system" && e.Item.State == protocol.MessageProcessing
	})
	message := events[len(events)-1].Item.MessageID
	conversation, err := srv.app.MessageConversation(t.Context(), message)
	if err != nil || conversation == "main" {
		t.Fatal(conversation, err)
	}
	assertState := func(state protocol.MessageState) {
		t.Helper()
		var page protocol.ConversationPage
		if st := web.do("GET", "/api/v1/conversation?conversation_id="+conversation, nil, &page); st != 200 {
			t.Fatal(st)
		}
		for _, item := range page.Items {
			if item.MessageID == message && item.Author == "system" && item.State == state {
				return
			}
		}
		t.Fatalf("owner history omitted background message %s state %s", message, state)
	}
	assertState(protocol.MessageProcessing)
	web.op(srv.ids.New(protocol.PrefixRequest), "conversation.interrupt_reply", protocol.Target{Type: "message", ID: message},
		protocol.InterruptReplyPayload{MessageID: message})
	stream.until("background reply stops", itemIs(message, protocol.MessageInterrupted))
	assertState(protocol.MessageInterrupted)
	ownerMessage := web.send(protocol.SubmitMessagePayload{Body: "Continue with my clarification"})
	stream.until("owner turn follows stopped event", itemIs(ownerMessage, protocol.MessageReplied))
}
