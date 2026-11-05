package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func rejectedStartupInput(t *testing.T, h *harness, pendingReceipt bool) (Delivery, store.MainTurn) {
	t.Helper()
	message, delivery := h.say("Retain the original definitely unsent input")
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: message, ThreadID: "original-thread", Status: store.TurnStarting}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if pendingReceipt {
		if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
			t.Fatal(err)
		}
	}
	turn.Status = store.TurnRejected
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Retry}); err != nil {
		t.Fatal(err)
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), delivery.RequestID); err != nil || pending != pendingReceipt {
		t.Fatal("fixture receipt identity changed", pending, err)
	}
	return delivery, turn
}

func TestAgentStartupRejectedInputRetainsOrdinaryBudget(t *testing.T) {
	h := newHarness(t)
	delivery, _ := rejectedStartupInput(t, h, false)
	_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
	if err != nil || attachment.RequestID != delivery.RequestID || attachment.Reconciliation {
		t.Fatal("definitely rejected input received a native reconciliation exemption", attachment, err)
	}
	before := h.clock.Now()
	if err := h.app.DeferAgentStartupAttachment(t.Context(), "main", attachment); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		input, err := store.GetOutboxByRequest(t.Context(), tx.Q(), delivery.RequestID)
		if err == nil && (input.NextAttemptAt < before.Add(30*time.Second).UnixMilli() || input.Status != store.OutboxPending || input.Attempts != delivery.Attempt) {
			t.Fatal("known-unsent startup lost its original input backoff", input)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(31 * time.Second)
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts - 1 {
		if err := h.app.DeferAgentStartup(t.Context(), "main"); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(31 * time.Second)
	}
	startupAttention(t, h)
	if allowed, err := h.app.AgentStartupAllowed(t.Context(), "main"); err != nil || allowed {
		t.Fatal("known-unsent rejection bypassed the exhausted startup hold", allowed, err)
	}
	turn, found, err := h.app.MainTurn(t.Context(), delivery.RequestID)
	if err != nil || !found || turn.Status != store.TurnRejected || turn.TurnID != "" {
		t.Fatal("startup accounting changed the original rejected native fact", turn, found, err)
	}
}

func TestAgentStartupRejectedInputPreservesExplicitPendingReceipt(t *testing.T) {
	h := newHarness(t)
	delivery, turn := rejectedStartupInput(t, h, true)
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := h.app.DeferAgentStartup(t.Context(), "main"); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(31 * time.Second)
	}
	attention := startupAttention(t, h)
	_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
	if err != nil || attachment.RequestID != delivery.RequestID || !attachment.Reconciliation {
		t.Fatal("explicit pending receipt lost the original read-only attachment", attachment, err)
	}
	if allowed, err := h.app.AgentStartupAllowed(t.Context(), "main"); err != nil || !allowed {
		t.Fatal("startup hold suppressed explicit native receipt reconciliation", allowed, err)
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), delivery.RequestID); err != nil || !pending {
		t.Fatal("startup classification discarded the pending receipt", pending, err)
	}
	turn.Status, turn.TurnID = store.TurnStarted, "original-native-turn"
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal("open startup hold blocked an existing native fact", err)
	}
	turn.Status, turn.NativeReconciled = store.TurnCompleted, true
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal("open startup hold blocked the original receipt settlement", err)
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), delivery.RequestID); err != nil || pending {
		t.Fatal("original native receipt did not settle", pending, err)
	}
	if current := readAttentionForTest(t, h, attention.AttentionID); current.Status != "open" {
		t.Fatal("receipt settlement silently resolved the technical startup action", current)
	}
}

func TestAgentStartupHeldRepairRemainsUnclaimedAfterForegroundStartup(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	prepareHostedAdmissionRepair(t, h, issue)
	conversation := "issue:" + issue.IssueID
	_, _, held, err := h.app.HostedRuntimeStartupSelection(t.Context(), conversation)
	if err != nil || held.RequestID == "" || held.Reconciliation {
		t.Fatal("fixture did not select the unadmitted repair", held, err)
	}
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts {
		if err := h.app.DeferAgentStartupAttachment(t.Context(), conversation, held); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(31 * time.Second)
	}
	attention := startupAttention(t, h)
	submitHostedAdmissionInput(t, h, conversation)
	_, _, foreground, err := h.app.HostedRuntimeStartupSelection(t.Context(), conversation)
	if err != nil || foreground.RequestID == held.RequestID || foreground.Reconciliation {
		t.Fatal("fixture did not select the newer foreground input", foreground, err)
	}
	if err := h.app.AgentStartupAttachmentSucceeded(t.Context(), foreground); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !ok || delivery.RequestID != foreground.RequestID {
		t.Fatal("old repair hold blocked the newer foreground input", delivery, ok, err)
	}
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	if next, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation)); err != nil || ok {
		t.Fatal("foreground session bypassed the older input's startup hold", next, ok, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		original, err := store.GetOutboxByRequest(t.Context(), tx.Q(), held.RequestID)
		if err == nil && (original.Status != store.OutboxPending || original.Attempts != 0) {
			t.Fatal("held repair acquired a dispatch attempt", original)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: attention.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Original startup configuration repaired"}, rev(attention.Revision)), AttentionCommand{Action: "confirm"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if next, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation)); err != nil || !ok || next.RequestID != held.RequestID {
		t.Fatal("resolved hold did not release the original repair identity", next, ok, err)
	}
}

func TestAgentStartupClaimedInputCannotAdmitAfterHold(t *testing.T) {
	for _, previous := range []string{"none", "rejected"} {
		t.Run(previous, func(t *testing.T) {
			h := newHarness(t)
			message, delivery := h.say("Keep this claimed input paused until its technical action is handled")
			turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: message, ThreadID: "already-open-thread", Status: store.TurnStarting}
			if previous == "rejected" {
				if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
				turn.Status = store.TurnRejected
				if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
				turn.Status = store.TurnStarting
			}
			exhaustStartup(t, h, "main")
			attention := startupAttention(t, h)
			if err := h.app.PutMainTurn(t.Context(), turn); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
				t.Fatal("claimed input bypassed its startup hold at native admission", err)
			}
			retained, found, err := h.app.MainTurn(t.Context(), delivery.RequestID)
			if err != nil || found != (previous == "rejected") || found && retained.Status != store.TurnRejected {
				t.Fatal("held admission changed the original native facts", retained, found, err)
			}
			op := h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: attention.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Original startup configuration repaired"}, rev(attention.Revision)), AttentionCommand{Action: "confirm"})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal("resolved hold did not admit the same original request", err)
			}
		})
	}
}
