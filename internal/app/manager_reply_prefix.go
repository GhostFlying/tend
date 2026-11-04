package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AcknowledgeManagerReplyPrefix confirms only deltas whose cumulative terminal
// text is already durable. A successful no-op recording an older terminal item
// is insufficient evidence to discard a newly consumed prefix.
func (a *App) AcknowledgeManagerReplyPrefix(ctx context.Context, run string, sequence int64, thread, turn, item, body string) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		var original bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_events WHERE run_id=? AND seq=? AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND json_extract(body_json,'$.native.TurnID')=? AND json_extract(body_json,'$.native.ItemID')=?)`, run, sequence, thread, turn, item).Scan(&original); err != nil {
			return err
		}
		if !original {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "reply prefix has no matching original delta")
		}
		row, err := store.GetAgentMessage(ctx, tx.Q(), thread, turn, item)
		if err != nil {
			return err
		}
		message, err := store.GetConversationItem(ctx, tx.Q(), row.MessageID)
		if err != nil {
			return err
		}
		if row.Status == string(protocol.AssistantStreaming) || row.Status == "" || message.Body != body {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "terminal reply prefix has not been persisted")
		}
		_, err = tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq<=? AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND json_extract(body_json,'$.native.TurnID')=? AND json_extract(body_json,'$.native.ItemID')=?`, run, sequence, thread, turn, item)
		return err
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}
