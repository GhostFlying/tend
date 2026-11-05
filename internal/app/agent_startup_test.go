package app

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func agentStartupHead(ctx context.Context, q store.Querier, conversation string) (store.OutboxMessage, error) {
	return store.HeadOutboxPrioritized(ctx, q, store.ConversationDestination(store.ConversationID(conversation)), OpHandleEvent)
}

func startupInput(t *testing.T, h *harness, conversation string) store.OutboxMessage {
	t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Investigate the original authorized goal"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	var head store.OutboxMessage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		head, err = store.HeadOutboxPrioritized(t.Context(), tx.Q(), store.ConversationDestination(conversation), OpHandleEvent)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return head
}

func exhaustStartup(t *testing.T, h *harness, conversation string) {
	t.Helper()
	before := h.count("attentions")
	for attempt := int64(0); attempt < domain.DefaultAgentRecoveryPolicy().MaxAttempts; attempt++ {
		if err := h.app.DeferAgentStartup(t.Context(), conversation); err != nil {
			t.Fatal(err)
		}
		if attempt == 0 && h.count("attentions") != before {
			t.Fatal("one transient startup failure escalated")
		}
		if attempt == 2 {
			h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration))
		}
		h.clock.Advance(31 * time.Second)
	}
}

func startupAttention(t *testing.T, h *harness) protocol.Attention {
	t.Helper()
	if h.count("attentions") != 1 {
		t.Fatalf("persistent startup failure did not create exactly one technical problem: %d", h.count("attentions"))
	}
	var id string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions`).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return readAttentionForTest(t, h, id)
}

func TestAgentStartupFailureEscalatesToParentWithoutLosingInput(t *testing.T) {
	h := hostedManagerHarness(t)
	parent := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	child := childProblemIssue(t, h, parent)
	conversation := "issue:" + child.IssueID
	before := startupInput(t, h, conversation)
	exhaustStartup(t, h, conversation)
	a := startupAttention(t, h)
	var input HandleMessage
	if err := json.Unmarshal([]byte(before.PayloadJSON), &input); err != nil {
		t.Fatal(err)
	}
	if a.Kind != "action" || a.IssueID != child.IssueID || a.SourceMessageID != input.MessageID || a.Responsibility == nil || a.Responsibility.IssueID != parent.IssueID || len(a.Responsibility.History) != 1 {
		t.Fatal("startup problem did not retain its original source and parent responsibility", a)
	}
	assertHumanProblemCount(t, h, 0)
	if allowed, err := h.app.AgentStartupAllowed(t.Context(), conversation); err != nil || allowed {
		t.Fatal("exhausted startup continued before its original technical problem was handled", allowed, err)
	}
	if pending, err := h.app.PendingAgentConversations(t.Context()); err != nil || slices.Contains(pending, conversation) {
		t.Fatal("exhausted startup remained automatically runnable", pending, err)
	}
	for range 2 {
		if err := h.app.DeferAgentStartup(t.Context(), conversation); err != nil {
			t.Fatal(err)
		}
	}
	if h.count("attentions") != 1 || h.count("main_turns") != 0 {
		t.Fatal("repeated startup manufactured a second problem or native turn")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		after, err := store.HeadOutboxPrioritized(t.Context(), tx.Q(), before.Destination, OpHandleEvent)
		if err == nil && (after.RequestID != before.RequestID || after.PayloadJSON != before.PayloadJSON || after.Status != store.OutboxPending || after.Attempts != 0) {
			t.Fatal("startup changed the original input or claimed a native attempt", after)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Verified the original startup configuration was repaired"}, rev(a.Revision)), AttentionCommand{Action: "confirm"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if allowed, err := h.app.AgentStartupAllowed(t.Context(), conversation); err != nil || !allowed {
		t.Fatal("handled technical fault did not release the original pending input", allowed, err)
	}
	if err := h.app.DeferAgentStartup(t.Context(), conversation); err != nil {
		t.Fatal(err)
	}
	if h.count("attentions") != 1 {
		t.Fatal("new transient startup failure inherited the exhausted budget")
	}
}

func TestAgentStartupFailureAtRootEscalatesToMain(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	conversation := "issue:" + issue.IssueID
	startupInput(t, h, conversation)
	exhaustStartup(t, h, conversation)
	a := startupAttention(t, h)
	if a.Responsibility == nil || a.Responsibility.RecipientKind != "main" || a.Responsibility.IssueID != "" {
		t.Fatal("root startup failure did not reach the main coordinator", a)
	}
	assertHumanProblemCount(t, h, 0)
}

func TestAgentStartupFailureOfMainReachesOwnerWithoutNativeTurn(t *testing.T) {
	h := newHarness(t)
	before := startupInput(t, h, "main")
	exhaustStartup(t, h, "main")
	a := startupAttention(t, h)
	if a.Kind != "action" || a.IssueID != "" || a.Responsibility != nil {
		t.Fatal("main startup failure recursed into the unavailable coordinator", a)
	}
	assertHumanProblemCount(t, h, 1)
	if h.count("main_turns") != 0 {
		t.Fatal("main startup failure fabricated native execution")
	}
	if n := h.countWhere(`SELECT COUNT(*) FROM outbox WHERE destination=?`, MainThreadDestination); n != 1 {
		t.Fatalf("owner fallback recursively queued unavailable main input: %d", n)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		after, err := store.HeadOutboxPrioritized(t.Context(), tx.Q(), MainThreadDestination, OpHandleEvent)
		if err == nil && (after.RequestID != before.RequestID || after.Status != store.OutboxPending) {
			t.Fatal("owner fallback swallowed the original main input", after)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentStartupSuccessResetsTransientFailureWindow(t *testing.T) {
	h := newHarness(t)
	startupInput(t, h, "main")
	for range domain.DefaultAgentRecoveryPolicy().MaxAttempts - 1 {
		if err := h.app.DeferAgentStartup(t.Context(), "main"); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(31 * time.Second)
	}
	if err := h.app.AgentStartupSucceeded(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.DeferAgentStartup(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	if h.count("attentions") != 0 {
		t.Fatal("successful attachment did not end its prior failure window")
	}
	if allowed, err := h.app.AgentStartupAllowed(t.Context(), "main"); err != nil || allowed {
		t.Fatal("transient startup retry did not retain its backoff", allowed, err)
	}
}

func TestAgentStartupElapsedBudgetSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	startupInput(t, h, "main")
	if err := h.app.DeferAgentStartup(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(domain.DefaultAgentRecoveryPolicy().MaxElapsed + time.Second)
	h.app = New(h.store, h.ids, h.clock.Now)
	if err := h.app.DeferAgentStartup(t.Context(), "main"); err != nil {
		t.Fatal(err)
	}
	startupAttention(t, h)
	assertHumanProblemCount(t, h, 1)
}
