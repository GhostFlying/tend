package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
	"time"
)

func TestConsultationLaterInputDoesNotBypassStartupHeadBackoff(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	send := func(body string) {
		t.Helper()
		op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: c.ID, Body: body}, nil), SubmitMessage{})
		if op.Status != protocol.OpAccepted {
			t.Fatal(op)
		}
	}
	send("Original ordinary consultation question")
	if err := h.app.DeferAgentStartup(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	send("A second ordinary question while the model remains unavailable")
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(ids, c.ID) {
		t.Fatal("later input bypassed deferred actual head", ids, err)
	}
	// A reconstructed App must honor the same persisted deadline.
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 1)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(ids, c.ID) {
		t.Fatal("restart lost head backoff", ids, err)
	}
	h.clock.Advance(30 * time.Second)
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, c.ID) {
		t.Fatal("due consultation not discoverable", ids, err)
	}
	if h.count("main_turns") != 0 || h.count("issue_controls") != 0 || h.count("tasks") != 0 {
		t.Fatal("discovery manufactured execution")
	}
}
