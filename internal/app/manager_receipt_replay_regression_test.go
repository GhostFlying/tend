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

func receiptStartupFixture(t *testing.T) (*harness, protocol.Assignment, string) {
	t.Helper()
	h, as, pending := stoppedRetainedManager(t)
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
	return h, as, pending
}

func TestFault_ManagerReceiptRecoveryBudgetPreventsNewRun(t *testing.T) {
	h, as, pending := receiptStartupFixture(t)
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

func TestFault_QueuedReceiptHonorsOriginalRecoveryBudget(t *testing.T) {
	h, as, pending := receiptStartupFixture(t)
	action := h.ids.New(protocol.PrefixAction)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertAction(t.Context(), store.ActionRow{ActionID: action, TaskID: as.TaskID, Kind: protocol.ActionCoordinate, EffectKey: "fixture-queued-receipt", TriggerJSON: `{"receipt_only":true}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: 2, Status: protocol.ActionPending, CreatedRequestID: h.ids.New(protocol.PrefixRequest), CreatedAt: h.clock.Now().UnixMilli()})
	}); err != nil {
		t.Fatal(err)
	}
	contains := func() bool {
		t.Helper()
		found := false
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			cs, err := views.IssueManagerCandidatesObservedAt(t.Context(), tx.Q(), nil, h.clock.Now().UnixMilli())
			for _, c := range cs {
				if c.ActionID == action {
					found = true
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return found
	}
	attachment := AgentStartupAttachment{RequestID: pending, Reconciliation: true}
	if !contains() {
		t.Fatal("fixture original receipt was not discoverable")
	}
	for attempt := int64(0); attempt < domain.DefaultAgentRecoveryPolicy().MaxAttempts; attempt++ {
		if err := h.app.DeferAgentStartupAttachment(t.Context(), as.Manager.ConversationID, attachment); err != nil {
			t.Fatal(err)
		}
		if contains() {
			t.Fatal("queued receipt bypassed original backoff or Attention")
		}
		h.clock.Advance(31 * time.Second)
		if attempt+1 < domain.DefaultAgentRecoveryPolicy().MaxAttempts && !contains() {
			t.Fatal("due original receipt did not recover after backoff")
		}
	}
	if contains() {
		t.Fatal("exhausted original receipt remained automatically claimable")
	}
	attention := startupAttention(t, h)
	op := h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: attention.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Original technical fault repaired; continue the same receipt"}, rev(attention.Revision)), AttentionCommand{Action: "confirm"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if !contains() {
		t.Fatal("formal original recovery did not release the same queued action")
	}
	if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, pending) != 1 {
		t.Fatal("admission fabricated original receipt success")
	}
}

func TestFault_ReceiptStartupBackoffRetainsWakeDeadline(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	turn, _ := startHostedIssueReview(t, h, issue)
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if pending, err := h.app.DeferAgentReceiptRead(t.Context(), conversation); err != nil || !pending {
		t.Fatal(pending, err)
	}
	h.clock.Advance(2 * time.Second)
	_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), conversation)
	if err != nil || !attachment.Reconciliation {
		t.Fatal(attachment, err)
	}
	if err := h.app.DeferAgentStartupAttachment(t.Context(), conversation, attachment); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(28 * time.Second)
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || allowed {
		t.Fatal(allowed, err)
	}
	scan := h.clock.Now()
	delay, err := h.app.BackgroundDelay(t.Context(), scan)
	if err != nil || delay != 2*time.Second {
		t.Fatalf("receipt backoff wake=%s, want 2s: %v", delay, err)
	}
	h.clock.Advance(delay)
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || !allowed {
		t.Fatal("original receipt did not become serviceable at its timer", allowed, err)
	}
	h.clock.Advance(time.Millisecond)
	if delay, err := h.app.BackgroundDelay(t.Context(), scan); err != nil || delay == 0 {
		t.Fatal("deadline crossed during scan lost its timer wake", delay, err)
	}
}

func TestFault_CompactStartupBackoffRetainsWakeDeadline(t *testing.T) {
	h := hostedManagerHarness(t)
	c := createConsultationFixture(t, h)
	selected, named, err := h.app.NamedHostedSelection(t.Context(), c.ID)
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	request, compact, thread := startupCompact(t, h, c.ID, "unknown", &selected)
	// Restore the persisted delivery left by a process that stopped after
	// native admission, before it could settle the compact's outbox receipt.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='sending',next_attempt_at=0 WHERE request_id=?`, request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.app = New(h.store, h.ids, h.clock.Now)
	_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), c.ID)
	if err != nil || !attachment.Reconciliation || attachment.RequestID != request || attachment.NativeCompactThread() != thread {
		t.Fatal("restart lost the original compact attachment", attachment, err)
	}
	if err := h.app.DeferAgentStartupAttachment(t.Context(), c.ID, attachment); err != nil {
		t.Fatal(err)
	}
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || allowed {
		t.Fatal("compact attachment ignored its own startup backoff", allowed, err)
	}
	delay, err := h.app.BackgroundDelay(t.Context(), h.clock.Now())
	if err != nil || delay != 30*time.Second {
		t.Fatalf("compact backoff wake=%s, want 30s: %v", delay, err)
	}
	h.clock.Advance(delay)
	if allowed, err := h.app.AgentStartupAttachmentAllowed(t.Context(), attachment); err != nil || !allowed {
		t.Fatal("original compact did not become serviceable at its timer", allowed, err)
	}
	if state, err := h.app.CompactState(t.Context(), compact); err != nil || state != "unknown" {
		t.Fatal("timer fabricated compact completion", state, err)
	}
	if h.count("agent_compactions") != 1 || h.count("main_turns") != 0 || h.count("native_turn_receipts_pending") != 0 {
		t.Fatal("compact attachment replayed work or fabricated a native turn")
	}
	if err := h.app.DeferAgentStartupAttachment(t.Context(), c.ID, attachment); err != nil {
		t.Fatal(err)
	}
	if err := h.app.CompleteNativeCompact(t.Context(), c.ID, thread, "original-compact-item"); err != nil {
		t.Fatal(err)
	}
	if delay, err := h.app.BackgroundDelay(t.Context(), h.clock.Now()); err != nil || delay != 0 {
		t.Fatal("completed compact retained an obsolete startup timer", delay, err)
	}
}
