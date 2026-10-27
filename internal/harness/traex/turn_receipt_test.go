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
