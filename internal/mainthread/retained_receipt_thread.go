package mainthread

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

// A failed message presentation can finish entirely from previously observed
// originals. This handle has no provider connection or execution operations.
type retainedReceiptThread struct {
	id       string
	receipts map[string]harness.TurnReceipt
}

func (t *retainedReceiptThread) ID() string                 { return t.id }
func (*retainedReceiptThread) Events() <-chan harness.Event { return nil }
func (*retainedReceiptThread) InjectDeveloper(context.Context, ...string) error {
	return fmt.Errorf("%w: retained receipts cannot inject instructions", harness.ErrRejected)
}
func (*retainedReceiptThread) StartTurn(context.Context, harness.TurnInput) (string, error) {
	return "", fmt.Errorf("%w: retained receipts cannot start turns", harness.ErrRejected)
}
func (*retainedReceiptThread) Interrupt(context.Context, string) error {
	return fmt.Errorf("%w: retained receipts cannot interrupt turns", harness.ErrRejected)
}
func (t *retainedReceiptThread) FindTurn(_ context.Context, _ string, turn string) (harness.TurnReceipt, bool, error) {
	receipt, found := t.receipts[turn]
	return receipt, found, nil
}

func (m *Manager) retainedReceiptThread(ctx context.Context) (harness.Thread, error) {
	if m.cfg.DecodeRetainedTurnReceipt == nil {
		return nil, nil
	}
	id, exists, err := m.app.MainThread(ctx, m.bindingInstance())
	if err != nil || !exists {
		return nil, err
	}
	turns, err := m.app.UncertainNativeTurns(ctx, id)
	if err != nil || len(turns) == 0 {
		return nil, err
	}
	thread := &retainedReceiptThread{id: id, receipts: map[string]harness.TurnReceipt{}}
	for _, rec := range turns {
		raw, err := m.app.RetainedNativeReceipt(ctx, m.cfg.ConversationID, rec.RequestID)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, nil
		}
		receipt, err := m.cfg.DecodeRetainedTurnReceipt(raw)
		if err != nil {
			return nil, err
		}
		if receipt.ID != rec.TurnID || receipt.ID == "" {
			return nil, fmt.Errorf("%w: retained receipt has a different original turn", harness.ErrRejected)
		}
		covered, err := m.nativeReceiptSourcesCovered(ctx, id, receipt)
		if err != nil {
			return nil, err
		}
		if !covered {
			return nil, nil
		}
		receipt.Raw = raw
		thread.receipts[rec.TurnID] = receipt
	}
	return thread, nil
}
