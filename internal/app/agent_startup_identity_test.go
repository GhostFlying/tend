package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/store"
)

func TestAgentStartupFrozenAttachmentRetainsSourceAfterQueuePriorityChanges(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	prepareHostedAdmissionRepair(t, h, issue)
	conversation := "issue:" + issue.IssueID
	_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), conversation)
	if err != nil || attachment.RequestID == "" || attachment.Reconciliation {
		t.Fatal("fixture did not select an unadmitted frozen repair", attachment, err)
	}
	submitHostedAdmissionInput(t, h, conversation)
	var current store.OutboxMessage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		current, err = h.app.hostedServiceableHead(t.Context(), tx.Q(), store.ConversationDestination(conversation))
		return err
	}); err != nil || current.RequestID == attachment.RequestID {
		t.Fatal("fixture did not change the currently serviceable input", current, err)
	}
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := h.app.DeferAgentStartupAttachment(t.Context(), conversation, attachment); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(31 * time.Second)
	}
	a := startupAttention(t, h)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		original, err := store.GetOutboxByRequest(t.Context(), tx.Q(), attachment.RequestID)
		if err != nil {
			return err
		}
		var source HandleMessage
		if err := json.Unmarshal([]byte(original.PayloadJSON), &source); err != nil {
			return err
		}
		wrong, err := readAgentStartupState(t.Context(), tx.Q(), current.RequestID)
		if err == nil && (a.SourceMessageID != source.MessageID || wrong.Attempts != 0) {
			t.Fatal("attachment fault moved to the later foreground input", a, source, wrong)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := h.app.AgentStartupAllowed(t.Context(), conversation); err != nil || !allowed {
		t.Fatal("old attachment budget blocked the newer serviceable input", allowed, err)
	}
}

func TestAgentStartupLegacyReceiptAttachmentIgnoresNewInputBudget(t *testing.T) {
	h := newHarness(t)
	message, delivery := h.say("Original native input")
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: message, ThreadID: "legacy-native", TurnID: "original-turn", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	newInput := startupInput(t, h, "main")
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := h.app.DeferAgentStartupAttachment(t.Context(), "main", AgentStartupAttachment{RequestID: newInput.RequestID}); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(31 * time.Second)
	}
	_, named, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
	if err != nil || named || !attachment.Reconciliation || attachment.RequestID != turn.RequestID {
		t.Fatal("legacy native receipt did not retain its original attachment", named, attachment, err)
	}
	if allowed, err := h.app.AgentStartupAllowed(t.Context(), "main"); err != nil || !allowed {
		t.Fatal("new input budget suppressed the original legacy receipt attachment", allowed, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, turn.RequestID) != 1 || h.countWhere(`SELECT COUNT(*) FROM main_turns WHERE request_id=? AND status=?`, turn.RequestID, store.TurnInterrupted) != 1 {
		t.Fatal("startup discovery changed the original unknown native receipt")
	}
}

func TestReviewStartupBudgetTracksServiceableFrozenRepair(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	prepareHostedAdmissionRepair(t, h, issue)
	conversation := "issue:" + issue.IssueID
	submitHostedAdmissionInput(t, h, conversation)
	reloadHostedAdmissionConfig(t, h, true)
	var rawHead, actual store.OutboxMessage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		rawHead, err = agentStartupHead(t.Context(), tx.Q(), conversation)
		if err != nil {
			return err
		}
		actual, err = h.app.hostedServiceableHead(t.Context(), tx.Q(), store.ConversationDestination(conversation))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if actual.Operation != "conversation.repair_turn" || rawHead.RequestID == actual.RequestID {
		t.Fatal("fixture lacks skipped unavailable foreground", rawHead, actual)
	}
	selected, named, err := h.app.HostedRuntimeSelection(t.Context(), conversation)
	if err != nil || !named || selected.ConfigurationRevision != 1 {
		t.Fatal("fixture does not select original repair runtime", selected, named, err)
	}
	if err := h.app.DeferAgentStartup(t.Context(), conversation); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		correct, err := readAgentStartupState(t.Context(), tx.Q(), actual.RequestID)
		if err != nil {
			return err
		}
		wrong, err := readAgentStartupState(t.Context(), tx.Q(), rawHead.RequestID)
		if err == nil && (correct.Attempts != 1 || wrong.Attempts != 0) {
			t.Fatalf("frozen repair startup charged unavailable owner input: actual=%s/%s correct_attempts=%d raw=%s/%s wrong_attempts=%d", actual.RequestID, actual.Operation, correct.Attempts, rawHead.RequestID, rawHead.Operation, wrong.Attempts)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewStartupExhaustionPreservesUnknownReceiptDiscovery(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	inflight, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range inflight {
		if d.RequestID == turn.RequestID {
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
				t.Fatal(err)
			}
		}
	}
	conversation := "issue:" + issue.IssueID
	submitHostedAdmissionInput(t, h, conversation)
	before, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(before, conversation) {
		t.Fatal("fixture missing receipt-only discovery", before, err)
	}
	exhaustStartup(t, h, conversation)
	var count int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, turn.RequestID).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatal("fixture lost original native receipt", count, err)
	}
	after, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(after, conversation) {
		t.Fatalf("pending new-input startup gate suppressed original unknown receipt discovery: before=%v after=%v err=%v", before, after, err)
	}
}
