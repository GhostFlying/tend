package coordination

import (
	"context"
	"github.com/GhostFlying/tend/internal/harness"
	"testing"
)

type historyTransport struct {
	t     *testing.T
	reads int
}

func (h *historyTransport) Call(_ context.Context, method string, input, output any) error {
	if method == "close" {
		return nil
	}
	if method != "find" {
		h.t.Fatalf("history dispatched native lifecycle method %s", method)
	}
	request := input.(Request)
	if request.ThreadID != "original-thread" || request.MessageID != "original-message" || request.TurnID != "original-turn" {
		h.t.Fatal(request)
	}
	h.reads++
	*output.(*FindResult) = FindResult{Found: true, Receipt: harness.TurnReceipt{ID: request.TurnID, Status: "completed"}}
	return nil
}
func (*historyTransport) Events(ctx context.Context, _ int64) ([]Event, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestHistoryHandleReadsOriginalWithoutResumeOrCallbacks(t *testing.T) {
	transport := &historyTransport{t: t}
	session := New(t.Context(), transport)
	defer session.Close()
	th, err := session.ReadThreadHistory(t.Context(), "original-thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := th.StartTurn(t.Context(), harness.TurnInput{}); err == nil {
		t.Fatal("history allowed a new turn")
	}
	if err := th.InjectDeveloper(t.Context(), "new tools"); err == nil {
		t.Fatal("history changed original instructions")
	}
	receipt, found, err := th.(harness.TurnReader).FindTurn(t.Context(), "original-message", "original-turn")
	if err != nil || !found || receipt.ID != "original-turn" || transport.reads != 1 {
		t.Fatal(receipt, found, err)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.threads) != 0 {
		t.Fatal("history installed event or callback attachments")
	}
}
