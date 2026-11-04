package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AcknowledgeManagerReplyPrefix confirms only deltas whose cumulative terminal
// text is already durable. A successful no-op recording an older terminal item
// is insufficient evidence to discard a newly consumed prefix.
func (a *App) AcknowledgeManagerReplyPrefix(ctx context.Context, run string, sequence int64, thread, turn, item, body string, consumed []int64) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		covered := map[int64]bool{}
		for _, delta := range consumed {
			if delta <= 0 || delta > sequence {
				return protocol.Errorf(protocol.CodeUnknownOutcome, "reply prefix contains an invalid consumed cursor")
			}
			covered[delta] = true
		}
		if !covered[sequence] {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "reply prefix did not consume its last delta")
		}
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
		rows, err := tx.Q().QueryContext(ctx, `SELECT seq,applied FROM manager_native_events WHERE run_id=? AND seq<=? AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND json_extract(body_json,'$.native.TurnID')=? AND json_extract(body_json,'$.native.ItemID')=?`, run, sequence, thread, turn, item)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[int64]bool{}
		for rows.Next() {
			var delta int64
			var applied bool
			if err := rows.Scan(&delta, &applied); err != nil {
				return err
			}
			if !applied && !covered[delta] {
				return protocol.Errorf(protocol.CodeUnknownOutcome, "reply prefix contains an unconsumed delta")
			}
			seen[delta] = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for delta := range covered {
			if !seen[delta] {
				return protocol.Errorf(protocol.CodeUnknownOutcome, "consumed delta does not belong to the reply prefix")
			}
		}
		_, err = tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq<=? AND json_extract(body_json,'$.thread_id')=? AND json_extract(body_json,'$.native.Kind')='agent_message_delta' AND json_extract(body_json,'$.native.TurnID')=? AND json_extract(body_json,'$.native.ItemID')=?`, run, sequence, thread, turn, item)
		return err
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}
