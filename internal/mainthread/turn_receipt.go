package mainthread

import (
	"context"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"time"
)

// Reconcile retained native history before starting more work after reconnect.
// Missing or ambiguous history remains unknown; it never authorizes replay.
func (m *Manager) reconcileNativeReceipts(ctx context.Context, th harness.Thread) error {
	reader, ok := th.(harness.TurnReader)
	if !ok {
		return nil
	}
	turns, err := m.app.UncertainNativeTurns(ctx, th.ID())
	if err != nil {
		return err
	}
	for _, rec := range turns {
		identity := rec.MessageID
		repair, err := m.app.IsRepairExecution(ctx, rec.RequestID)
		if err != nil {
			return err
		}
		if repair {
			identity = rec.RequestID
		}
		receipt, found, err := reader.FindTurn(ctx, identity, rec.TurnID)
		if err != nil {
			return err
		}
		if !found || (receipt.Status != "completed" && receipt.Status != "interrupted" && receipt.Status != "failed") {
			continue
		}
		rec.TurnID = receipt.ID
		rec.Status = store.TurnStarted
		if err := m.app.PutMainTurn(ctx, rec); err != nil {
			return err
		}
		if err := m.app.RecordRolloutEvent(ctx, m.cfg.ConversationID, th.ID(), rec.TurnID, "", "native_receipt", receipt.Raw); err != nil {
			return err
		}
		state := protocol.AssistantCompleted
		if receipt.Status == "interrupted" {
			state = protocol.AssistantInterrupted
		}
		if receipt.Status == "failed" {
			state = protocol.AssistantFailed
		}
		for _, message := range receipt.Messages {
			if repair {
				continue
			}
			payload := map[string]string{"body": message.Text, "reply_to": rec.MessageID, "turn_id": rec.TurnID, "item_id": message.ItemID, "status": string(state)}
			if err := m.record(ctx, th, "native-receipt/"+rec.TurnID+"/"+message.ItemID, "conversation.record_agent_message", payload, app.RecordAgentMessage{Reconciled: true}); err != nil {
				return err
			}
		}
		// A reconnect interruption is not an owner stop. Restricted recovery may
		// finish metadata after the known native turn, without replaying its actions.
		rec.Status = receipt.Status
		rec.NativeReconciled = true

		if err := m.app.PutMainTurn(ctx, rec); err != nil {
			return err
		}

	}
	return nil
}

// Keep reconciling durable unknown receipts after transient transport failures.
// Reads are rate limited and bounded; no new native execution is issued. The
// delivery lock keeps history repair separate from an active turn or handoff.
func (m *Manager) pollNativeReceipts(ctx context.Context, th harness.Thread, interval time.Duration) {
	if _, ok := th.(harness.TurnReader); !ok {
		return
	}
	delay := interval
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !m.workMu.TryLock() {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := m.reconcileNativeReceipts(readCtx, th)
		cancel()
		m.workMu.Unlock()
		if err != nil {
			m.cfg.Log.WarnContext(ctx, "native receipt reconciliation will retry", "err", err)
			delay = min(delay*2, 10*time.Minute)
		} else {
			delay = interval
		}
	}
}
