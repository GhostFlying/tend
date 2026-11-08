package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestConversationIndexPagesReferencesWithoutProseOrReviewWrites(t *testing.T) {
	h := newHarness(t)
	secret := strings.Repeat("ORIGINAL_CONSTRAINT_原件", 10000)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for i := range 55 {
			id := fmt.Sprintf("source-%03d", i)
			if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Author: "system", Body: secret}); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,scope,kind,body,source_message_id,attribution,created_at) VALUES(?,'global','constraint',?,?,'owner',0)`, fmt.Sprintf("fact-%03d", i), secret, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"state", "sources", "facts", "threads"} {
		next := ""
		seen := map[string]bool{}
		for {
			v, err := h.app.ConversationContextIndex(t.Context(), "main", protocol.ConversationContextQuery{Section: section, After: next})
			if err != nil {
				t.Fatal(section, err)
			}
			raw, _ := json.Marshal(v)
			if strings.Contains(string(raw), "ORIGINAL_CONSTRAINT") || len(v.Items) > 20 {
				t.Fatal("unbounded original body", section)
			}
			for _, ref := range v.Items {
				if section == "facts" && (ref.Seq <= 0 || ref.ConversationID != "main") {
					t.Fatal("fact cannot locate its exact original", ref)
				}
				if seen[ref.ID] {
					t.Fatal("repeated reference", ref.ID)
				}
				seen[ref.ID] = true
			}
			text, err := protocol.ContextIndexText(v)
			if err != nil || len(text) > protocol.ContextIndexBudget || strings.Contains(text, "ORIGINAL_CONSTRAINT") {
				t.Fatal("automatic injection includes prose", len(text), err)
			}
			if !v.More {
				break
			}
			if v.Next == next {
				t.Fatal("nonadvancing cursor")
			}
			next = v.Next
		}
		if (section == "facts" || section == "sources") && len(seen) != 55 {
			t.Fatal("lost references", section, len(seen))
		}
	}
	if h.count("message_context_obligations WHERE needs_review=1") != 0 || h.count("agent_turn_contexts") != 0 {
		t.Fatal("index changed review or admission")
	}
	for _, q := range []protocol.ConversationContextQuery{{Section: "bad"}, {Limit: 51}, {Limit: -1}} {
		if _, err := h.app.ConversationContextIndex(t.Context(), "main", q); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatal("invalid query accepted", q, err)
		}
	}
}

func TestConversationIndexHidesUnadmittedOwnerAndFiltersNativeThread(t *testing.T) {
	h := newHarness(t)
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "QUEUED_OWNER_SECRET"}, nil), SubmitMessage{})
	if op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	page, err := h.app.ConversationContextIndex(t.Context(), "main", protocol.ConversationContextQuery{Section: "sources"})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("queued source escaped", page, err)
	}
	for _, thread := range []string{"old-thread", "new-thread"} {
		if err := h.app.RegisterAgentThread(t.Context(), "main", thread); err != nil {
			t.Fatal(err)
		}
		if err := h.app.RecordRolloutEvent(t.Context(), "main", thread, "turn", "item", "tool", json.RawMessage(`{"original":"retained"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		v, err := store.AgentRollout(t.Context(), tx.Q(), "main", "", 0, "old-thread")
		if err == nil && (len(v.Records) != 1 || v.Records[0].ThreadID != "old-thread" || len(v.Threads) != 1) {
			t.Fatal("thread filter escaped", v)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestContextIndexCorrectionsAndWithdrawalsKeepExactSourceIdentity(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "original-owner", Author: "owner", Body: "ORIGINAL_DO_NOT_PUBLISH"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.ConversationContextIndex(t.Context(), "main", protocol.ConversationContextQuery{Section: "sources"})
	if err != nil || len(before.Items) != 1 {
		t.Fatal(before, err)
	}
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET body='CORRECTED_PAUSE' WHERE message_id='original-owner'`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE conversation_message_controls SET withdrawn_at=1 WHERE message_id='original-owner'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after, err := h.app.ConversationContextIndex(t.Context(), "main", protocol.ConversationContextQuery{Section: "sources"})
	if err != nil || len(after.Items) != 1 || after.Items[0].ID != before.Items[0].ID || !after.Items[0].Withdrawn || after.Items[0].ContentRevision <= before.Items[0].ContentRevision {
		t.Fatal("lost source correction", before, after, err)
	}
	raw, _ := json.Marshal(after)
	if strings.Contains(string(raw), "CORRECTED_PAUSE") || strings.Contains(string(raw), "ORIGINAL_DO_NOT_PUBLISH") {
		t.Fatal("index contains source text")
	}
}
