package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedFrozenOrdinaryRetryWaitsForOtherNativeReceipt(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	event, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || event.Operation != OpHandleEvent {
		t.Fatal("expected normal assignment event", event, ok, err)
	}
	selected, named, err := h.app.NamedHostedSelection(t.Context(), conversation)
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	var payload HandleMessage
	if err = json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	rec := store.MainTurn{RequestID: event.RequestID, MessageID: payload.MessageID, ThreadID: "original-event", Status: store.TurnStarting, Hosted: &selected, Model: selected.Definition.Model, Effort: selected.Definition.Effort}
	if err = h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	rec.Status = store.TurnRejected
	if err = h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err = h.app.FinishDelivery(t.Context(), event, DeliveryResult{Outcome: Retry, RetryAfter: time.Second}); err != nil {
		t.Fatal(err)
	}
	ownerTurn, _ := startHostedIssueReview(t, h, issue)
	if err = h.app.AwaitNativeReceipts(t.Context(), ownerTurn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err = h.app.SettleOpenTurns(t.Context(), ownerTurn.ThreadID); err != nil {
		t.Fatal(err)
	}
	inflight, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range inflight {
		if d.RequestID == ownerTurn.RequestID {
			if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.clock.Advance(time.Second)
	if d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || ok {
		t.Fatal("frozen ordinary event bypassed Issue receipt fence", d, ok, err)
	}
	if _, err = h.app.AgentContext(t.Context(), conversation, event.RequestID); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("frozen event context bypassed receipt gate", err)
	}
	ownerTurn.Status = store.TurnInterrupted
	ownerTurn.NativeReconciled = true
	if err = h.app.PutMainTurn(t.Context(), ownerTurn); err != nil {
		t.Fatal(err)
	}
	resumed, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || resumed.RequestID != event.RequestID {
		t.Fatal("original frozen event did not resume", resumed, ok, err)
	}
	pinned, named, err := h.app.HostedSelectionForAdmission(t.Context(), event.RequestID, conversation)
	if err != nil || !named || pinned.ConfigurationRevision != selected.ConfigurationRevision {
		t.Fatal("retry lost original pin", pinned, named, err)
	}
}
func TestHostedTerminalCanonicalPendingBatchRemainsDiscoverable(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureDeclined)))
	// Reconstruct the App as startup does; no in-memory pool/session selection.
	_, p := acceptHostedFixture(t, h, hostedConfigurationFixture(), 1)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(p))
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("closed canonical pending batch stranded after restart", ids, err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || d.Operation != OpHandleEvent {
		t.Fatal("terminal batch not claimable", d, ok, err)
	}
}

func TestHostedOrdinaryStartupBackoffDoesNotAdmitLaterBusiness(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	send := func(body string) {
		t.Helper()
		op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: body}, nil), SubmitMessage{})
		if op.Status != protocol.OpAccepted {
			t.Fatal(op)
		}
	}
	send("Original known-unsent startup input")
	if err := h.app.DeferAgentStartup(t.Context(), conversation); err != nil {
		t.Fatal(err)
	}
	send("Later business input cannot jump the original")
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(ids, conversation) {
		t.Fatal("ordinary later input bypassed head", ids, err)
	}
	if d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || ok {
		t.Fatal("claimed later ordinary input", d, ok, err)
	}
	h.clock.Advance(30 * time.Second)
	if d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || !ok || d.Operation != OpHandleMessage {
		t.Fatal("original owner did not become claimable", d, ok, err)
	}
}
