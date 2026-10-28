package traex

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/harness"
	"testing"
)

func TestNativeReceiptMatchesOnlyOriginalUserIdentity(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]string{"id": "thread"}} })
	f.on("thread/read", func(json.RawMessage) any {
		return map[string]any{"thread": map[string]any{"turns": []any{
			map[string]any{"id": "first", "status": "completed", "items": []any{map[string]string{"type": "userMessage", "id": "original"}, map[string]string{"type": "agentMessage", "id": "reply", "text": "preserved"}}},
			map[string]any{"id": "later", "status": "completed", "items": []any{map[string]string{"type": "userMessage", "id": "different"}}},
		}}}
	})
	th, err := s.StartThread(context.Background(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	reader := th.(harness.TurnReader)
	receipt, found, err := reader.FindTurn(t.Context(), "original", "")
	if err != nil || !found || receipt.ID != "first" || len(receipt.Messages) != 1 || receipt.Messages[0].Text != "preserved" {
		t.Fatal(receipt, found, err)
	}
	if _, found, err := reader.FindTurn(t.Context(), "missing", ""); err != nil || found {
		t.Fatal("absence invented a receipt", found, err)
	}
}

func TestNativeReceiptPreservesSupplementAndReplyOrder(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]string{"id": "thread"}} })
	f.on("thread/read", func(json.RawMessage) any {
		return map[string]any{"thread": map[string]any{"turns": []any{
			map[string]any{"id": "turn", "status": "completed", "items": []any{
				map[string]string{"id": "before", "type": "agentMessage", "text": "before supplement"},
				map[string]any{"id": "supplement", "type": "userMessage", "content": []any{map[string]string{"type": "text", "text": "one "}, map[string]string{"type": "text", "text": "two"}}},
				map[string]string{"id": "after", "type": "agentMessage", "text": "after supplement"},
			}},
		}}}
	})
	th, err := s.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	r, ok, err := th.(harness.TurnReader).FindTurn(t.Context(), "", "turn")
	if err != nil || !ok || len(r.Items) != 3 || len(r.Messages) != 2 {
		t.Fatal(r, ok, err)
	}
	if r.Items[0].ItemID != "before" || r.Items[1].ItemID != "supplement" || r.Items[1].Text != "one two" || r.Items[2].ItemID != "after" {
		t.Fatal(r.Items)
	}
}

func TestCodexReceiptMatchesClientIdentity(t *testing.T) {
	f, s := newFake(t)
	s.codex = true
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]string{"id": "thread"}} })
	f.on("thread/read", func(json.RawMessage) any {
		return map[string]any{"thread": map[string]any{"turns": []any{
			map[string]any{"id": "wanted", "status": "failed", "items": []any{map[string]string{"type": "userMessage", "id": "native-generated-id", "clientId": "owner-id"}}},
			map[string]any{"id": "different", "status": "completed", "items": []any{map[string]string{"type": "userMessage", "id": "owner-id", "clientId": "another-owner"}}},
		}}}
	})
	th, err := s.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	r, found, err := th.(harness.TurnReader).FindTurn(t.Context(), "owner-id", "")
	if err != nil || !found || r.ID != "wanted" {
		t.Fatal(r, found, err)
	}
}
