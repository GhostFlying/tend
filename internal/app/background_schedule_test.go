package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBackgroundDeadlinePreservesDueWorkAndFutureRetry(t *testing.T) {
	h := newHarness(t)
	var original string
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for i, offset := range []time.Duration{0, 8 * time.Second, 30 * time.Second} {
			id := h.ids.New(protocol.PrefixOutbox)
			if i == 0 {
				original = id
			}
			if err := tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: id, RequestID: h.ids.New(protocol.PrefixRequest), Destination: "background_thread:original", Operation: OpHandleEvent, PayloadJSON: `{}`, CreatedAt: h.clock.Now().Add(offset).UnixMilli()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []time.Duration{8 * time.Second, 22 * time.Second, 0} {
		delay, err := h.app.BackgroundDelay(t.Context(), h.clock.Now())
		if err != nil || delay != expected {
			t.Fatalf("next deadline = %v (%v), want %v", delay, err, expected)
		}
		h.clock.Advance(delay)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var status string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM outbox WHERE outbox_id=?`, original).Scan(&status); err != nil {
			return err
		}
		if status != store.OutboxPending {
			t.Fatal("scheduling fabricated progress for due work", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundDeadlineCrossedDuringScan(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: c.ID, Body: "original input"}, nil), SubmitMessage{})
	if op.Error != nil {
		t.Fatal(op)
	}
	if err := h.app.DeferAgentStartup(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(30*time.Second - time.Millisecond)
	wake := h.app.Changes().Wait()
	scanStart := h.clock.Now()
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(ids, c.ID) {
		t.Fatal("input should not be due yet", ids, err)
	}
	// Another conversation or DB work consumes the last millisecond of the scan.
	h.clock.Advance(time.Millisecond)
	delay, err := h.app.BackgroundDelay(t.Context(), scanStart)
	if err != nil {
		t.Fatal(err)
	}
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, c.ID) {
		t.Fatal("fixture did not become runnable", ids, err)
	}
	select {
	case <-wake:
		t.Fatal("time passage should not publish a ledger change")
	default:
	}
	if delay == 0 {
		t.Fatal("runnable original input has no timer or fact wake after scan crossed its deadline")
	}
}

func TestCancelledRecoverySuccessorReceiptDeadline(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	send := func(body string) {
		op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: c.ID, Body: body}, nil), SubmitMessage{})
		if op.Error != nil {
			t.Fatal(op)
		}
	}
	dest := "background_thread:" + c.ID
	send("original question")
	d, _, ok, err := h.app.NextDelivery(t.Context(), dest)
	if err != nil || !ok {
		t.Fatal(d, ok, err)
	}
	old := startHostedReview(t, h, c.ID, d)
	old.Status = store.TurnFailed
	old.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	send("new question supersedes waiting recovery")
	r, exists, err := h.app.AgentRecovery(t.Context(), old.RequestID)
	if err != nil || !exists || r.Status != "cancelled" {
		t.Fatal("not a cancelled recovery", r, err)
	}
	d, _, ok, err = h.app.NextDelivery(t.Context(), dest)
	if err != nil || !ok {
		t.Fatal(d, ok, err)
	}
	successor := startHostedReview(t, h, c.ID, d)
	if err := h.app.AwaitNativeReceipts(t.Context(), successor.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), successor.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, c.ID) {
		t.Fatal("missing original receipt", ids, err)
	}
	pending, err := h.app.DeferAgentReceiptRead(t.Context(), c.ID)
	if err != nil || !pending {
		t.Fatal(pending, err)
	}
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(ids, c.ID) {
		t.Fatal("receipt did not enter backoff", ids, err)
	}
	delay, err := h.app.BackgroundDelay(t.Context(), h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(30 * time.Second)
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, c.ID) {
		t.Fatal("receipt not eligible after delay", ids, err)
	}
	if delay != 30*time.Second {
		t.Fatalf("cancelled recovery's successor still needs a receipt wake in 30s, got %v", delay)
	}
}

func TestClaimedBackgroundStartupRetainsOriginalDeadline(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	startupInput(t, h, c.ID)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+c.ID)
	if err != nil || !ok {
		t.Fatal(delivery, ok, err)
	}
	if err := h.app.DeferAgentStartup(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	delay, err := h.app.BackgroundDelay(t.Context(), h.clock.Now())
	if err != nil || delay != 30*time.Second {
		t.Fatal("claimed startup lost original wake", delay, err)
	}
	before, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(before, c.ID) {
		t.Fatal("backoff ignored", before, err)
	}
	h.clock.Advance(delay)
	after, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(after, c.ID) {
		t.Fatal("original input did not resume", after, err)
	}
}

func TestActiveManagerStartupHoldHasExplicitAdmissionAndDeadline(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Original input"}, rev(issue.Revision)), RequestIssueManager{})
	if op.Error != nil {
		t.Fatal(op)
	}
	claimed := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
	var result struct{ Assignment *protocol.Assignment }
	if err := json.Unmarshal(claimed.Result, &result); err != nil || result.Assignment == nil || result.Assignment.Manager == nil {
		t.Fatal(claimed, err)
	}
	conversation := result.Assignment.Manager.ConversationID
	if err := h.app.DeferAgentStartup(t.Context(), conversation); err != nil {
		t.Fatal(err)
	}
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("fixture lost active responsibility", ids, err)
	}
	_, named, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), conversation)
	if err != nil || !named || attachment.Reconciliation {
		t.Fatal(attachment, named, err)
	}
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || allowed {
		t.Fatal("paused manager allowed another attachment", allowed, err)
	}
	if delay, err := h.app.BackgroundDelay(t.Context(), h.clock.Now()); err != nil || delay != 30*time.Second {
		t.Fatal("paused manager lost wake", delay, err)
	}
	h.clock.Advance(30 * time.Second)
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || !allowed {
		t.Fatal("original attachment never resumed", allowed, err)
	}
}
