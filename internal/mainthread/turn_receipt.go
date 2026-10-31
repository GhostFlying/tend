package mainthread

import (
	"context"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"slices"
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
		source, err := m.app.RecoverySourceRequest(ctx, rec.RequestID)
		if err != nil {
			return err
		}
		// Empty continuations have no new user item to match an unknown start.
		if source != "" && rec.TurnID == "" {
			if err := m.app.BlockAgentRecovery(ctx, rec.RequestID, "Native continuation identity is missing; automatic resend is disabled."); err != nil {
				return err
			}
			continue
		}
		receipt, found, err := reader.FindTurn(ctx, identity, rec.TurnID)
		if err != nil {
			return err
		}
		if !found || (receipt.Status != "completed" && receipt.Status != "interrupted" && receipt.Status != "failed") {
			if source != "" {
				if err := m.app.BlockAgentRecovery(ctx, rec.RequestID, "Native continuation has no confirmed terminal receipt; reconciliation is required."); err != nil {
					return err
				}
			}
			continue
		}
		consumed, err := m.app.ConsumedMainSteers(ctx, th.ID(), receipt.ID)
		if err != nil {
			return err
		}
		completeSources := true
		for _, source := range consumed {
			if !slices.ContainsFunc(receipt.Items, func(item harness.Event) bool {
				return item.ItemType == harness.ItemUserMessage && item.ItemID == source.NativeItemID && item.Text == source.InputText
			}) {
				completeSources = false
				break
			}
		}
		if !completeSources {
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
		items := receipt.Items
		if items == nil {
			items = receipt.Messages
		}
		replyTo, sources := rec.MessageID, []string{rec.MessageID}
		if source != "" {
			var err error
			replyTo, sources, _, err = m.retainedRecoverySources(ctx, rec.RequestID, rec.MessageID)
			if err != nil {
				return err
			}
		}
		for _, message := range items {
			if repair {
				continue
			}
			if message.ItemType == harness.ItemUserMessage {
				if message.ItemID != "" {
					matches, err := m.app.MessagingNativeInputMatches(ctx, rec.RequestID, message.Text)
					if err != nil {
						return err
					}
					if matches {
						if err := m.app.RecordMessagingConsumption(ctx, rec.RequestID, rec.MessageID, "native_receipt:"+message.ItemID); err != nil {
							return err
						}
					}
				}
				if _, _, err := m.app.ConsumeMainSteer(ctx, th.ID(), rec.TurnID, message.Text, message.ItemID); err != nil {
					return err
				}
				source, ok, err := m.app.MainSteerByInput(ctx, th.ID(), rec.TurnID, message.Text)
				if err != nil {
					return err
				}
				if ok && source.NativeItemID == message.ItemID && message.ItemID != "" {
					if err := m.app.RecordMessagingConsumption(ctx, source.RequestID, source.MessageID, "native_receipt:"+message.ItemID); err != nil {
						return err
					}
					replyTo = source.MessageID
					if !slices.Contains(sources, replyTo) {
						sources = append(sources, replyTo)
					}
				}
				continue
			}
			itemReplyTo, itemSources := replyTo, sources
			if prior, refs, found, err := m.app.NativeItemSources(ctx, th.ID(), rec.TurnID, message.ItemID); err != nil {
				return err
			} else if found {
				itemReplyTo, itemSources = prior, refs
			}
			payload := map[string]any{"body": message.Text, "reply_to": itemReplyTo, "turn_id": rec.TurnID, "item_id": message.ItemID, "status": string(state)}
			if len(itemSources) > 1 {
				payload["reply_sources"] = itemSources
			}
			if err := m.record(ctx, th, "native-receipt/"+rec.TurnID+"/"+message.ItemID, "conversation.record_agent_message", payload, app.RecordAgentMessage{Reconciled: true}); err != nil {
				return err
			}
		}
		// A reconnect interruption is not an owner stop. Restricted recovery may
		// finish metadata after the known native turn, without replaying its actions.
		rec.Status = receipt.Status
		rec.NativeReconciled = true
		rec.Failure = receipt.Failure

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
