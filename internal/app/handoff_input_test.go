package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestSummaryInputIncludesServerOnlyNoticesAndCompleteDeltas(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		for _, m := range []store.MessageRow{{MessageID: "notice", Author: "system", Body: "task finished; artifact report"}, {MessageID: "removed", Author: "system", Body: "old notice"}, {MessageID: "unchanged", Author: "system", Body: "unchanged notice"}, {MessageID: "tool", Kind: store.KindToolUse, Author: "main_agent"}} {
			if _, err := tx.InsertMessage(ctx, m); err != nil {
				return err
			}
		}
		return tx.PutToolUse(ctx, store.ToolUseRow{MessageID: "tool", ThreadID: "source", TurnID: "turn", ItemID: "call", Tool: "tasks_read", Output: "old", Status: "running"})
	}); err != nil {
		t.Fatal(err)
	}
	first, err := h.app.HandoffSummaryInput(ctx, "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(first.Observations, &rows); err != nil {
		t.Fatal(err)
	}
	if first.PreviousSummary != nil || len(rows) != 4 || !strings.Contains(string(first.Observations), "task finished; artifact report") {
		t.Fatalf("server-only observations missing: %+v %s", first, first.Observations)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: first.Snapshot, Body: "prior complete summary", FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE conversation_messages SET body='notice edited in place' WHERE message_id='notice'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM conversation_messages WHERE message_id='removed'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_tool_uses SET output='complete report',status='completed' WHERE message_id='tool'`); err != nil {
			return err
		}
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "new", Author: "system", Body: "new task completed"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	delta, err := h.app.HandoffSummaryInput(ctx, "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if delta.PreviousSummary == nil || delta.PreviousSummary.Body != "prior complete summary" || delta.Snapshot.Version <= first.Snapshot.Version {
		t.Fatalf("missing previous coverage: %+v", delta)
	}
	if err := json.Unmarshal(delta.Observations, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || strings.Contains(string(delta.Observations), "unchanged notice") || !strings.Contains(string(delta.Observations), `"deleted":true`) || !strings.Contains(string(delta.Observations), "complete report") || !strings.Contains(string(delta.Observations), "notice edited in place") {
		t.Fatalf("incomplete delta: %s", delta.Observations)
	}
	saved, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: delta.Snapshot, Body: "new complete summary", FormatVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "late", Author: "system", Body: "late activity"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Snapshot.Covers(latest) {
		t.Fatal("late notice falsely covered")
	}
}

func TestSummaryInputBudgetNeverTruncatesOrClaimsCoverage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "large", Author: "system", Body: strings.Repeat("x", 1024)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out, err := h.app.HandoffSummaryInput(ctx, "main", 128)
	if protocol.CodeOf(err) != protocol.CodeInvalidRequest || len(out.Observations) != 0 || out.Snapshot.ThreadID != "" {
		t.Fatalf("truncated success: %+v %v", out, err)
	}
	if h.count("conversation_summaries") != 0 {
		t.Fatal("failed input saved summary")
	}
	full, err := h.app.HandoffSummaryInput(ctx, "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: full.Snapshot, Body: strings.Repeat("\"<", 512), FormatVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.HandoffSummaryInput(ctx, "main", len(encoded)+2); err != nil {
		t.Fatalf("exact serialized budget rejected: %v", err)
	}
	if _, err := h.app.HandoffSummaryInput(ctx, "main", len(encoded)+1); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("previous metadata/escaping not budgeted: %v", err)
	}
	if _, err := h.app.HandoffSummaryInput(ctx, "main", 128); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("previous summary excluded from budget: %v", err)
	}
}

func TestSummaryInputIgnoresIncompleteOldFormatAndRejectsPending(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "old", Author: "system", Body: "previously missed notice"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: snapshot, Body: "incomplete old format", FormatVersion: 1}); err != nil {
		t.Fatal(err)
	}
	out, err := h.app.HandoffSummaryInput(ctx, "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if out.PreviousSummary != nil || !strings.Contains(string(out.Observations), "previously missed notice") {
		t.Fatalf("incomplete format reused: %+v", out)
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "pending owner input"}, nil), SubmitMessage{})
	if _, err := h.app.HandoffSummaryInput(ctx, "main", 4096); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("pending accepted: %v", err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(ctx, `UPDATE outbox SET status='delivered'`); return err }); err != nil {
		t.Fatal(err)
	}
	var messageID string
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT message_id FROM conversation_messages WHERE author='owner'`).Scan(&messageID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(ctx, store.MainTurn{RequestID: "active", MessageID: messageID, ThreadID: "source", Status: store.TurnStarting}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.HandoffSummaryInput(ctx, "main", 4096); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("active accepted: %v", err)
	}
}

func TestSummaryInputDeltaIncludesAssistantStateOwnerStateAndDecision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "assistant", Author: "main_agent", Body: "partial"}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO conversation_agent_messages(message_id,thread_id,turn_id,item_id,status,revision) VALUES('assistant','source','turn','reply','streaming',1)`); err != nil {
			return err
		}
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "owner", Author: "owner", Body: "approve", DecisionJSON: `{"decision_id":"decision","option_id":"yes"}`})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input, err := h.app.HandoffSummaryInput(ctx, "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: input.Snapshot, Body: "prefix", FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE conversation_agent_messages SET status='completed',revision=2 WHERE message_id='assistant'`); err != nil {
			return err
		}
		return h.app.insertMessageState(ctx, tx, "owner", protocol.MessageReplied, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	delta, err := h.app.HandoffSummaryInput(ctx, "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	text := string(delta.Observations)
	for _, want := range []string{`"assistant_state":"completed"`, `"assistant_revision":2`, `"state":"replied"`, `"decision":`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
}
