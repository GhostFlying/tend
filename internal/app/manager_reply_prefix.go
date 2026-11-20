package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AcknowledgeManagerReplyPrefix confirms only deltas whose cumulative terminal
// text is already durable. A successful no-op recording an older terminal item
// is insufficient evidence to discard a newly consumed prefix.
func (a *App) AcknowledgeManagerReplyPrefix(ctx context.Context, run string, sequence int64, thread, turn, item, body string, consumed []int64) error {
	ctx = store.WithWriteOperation(store.WithWriteEvent(ctx, run, sequence), "manager.event.ack_reply_prefix")
	err := a.write(ctx, func(tx *store.Tx) error {
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
		root, err := managerPrefixDeliveryTurn(ctx, tx.Q(), run, sequence, thread, turn, item)
		if err != nil {
			return err
		}
		if err := durableManagerReplyPrefix(ctx, tx.Q(), thread, turn, root, item, body); err != nil {
			return err
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

// managerPrefixDeliveryTurn keeps native item identity while identifying the
// admitted delivery which owns its reply or repair.
func managerPrefixDeliveryTurn(ctx context.Context, q store.Querier, run string, sequence int64, thread, turn, item string) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM manager_native_events WHERE run_id=? AND seq=?`, run, sequence).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", protocol.Errorf(protocol.CodeUnknownOutcome, "reply prefix has no matching original delta")
	}
	if err != nil {
		return "", err
	}
	var e struct {
		Root   string `json:"root_turn_id"`
		Thread string `json:"thread_id"`
		Native *struct{ Kind, TurnID, ItemID string }
	}
	if err := store.DecodeJSON(ctx, q, raw, &e); err != nil {
		return "", err
	}
	if e.Thread != thread || e.Native == nil || e.Native.Kind != "agent_message_delta" || e.Native.TurnID != turn || e.Native.ItemID != item {
		return "", protocol.Errorf(protocol.CodeUnknownOutcome, "reply prefix has no matching original delta")
	}
	if e.Root != "" {
		return e.Root, nil
	}
	var root string
	err = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT root_turn FROM manager_continued_turns WHERE run_id=? AND thread_id=? AND native_turn=?),?)`, run, thread, turn, turn).Scan(&root)
	return root, err
}

// A deferred background reply is durable without being published or settling
// its batch. Its original turn/message binding must still match exactly.
func durableManagerReplyPrefix(ctx context.Context, q store.Querier, thread, turn, root, item, body string) error {
	row, err := store.GetAgentMessage(ctx, q, thread, turn, item)
	if err == nil {
		message, err := store.GetConversationItem(ctx, q, row.MessageID)
		if err != nil {
			return err
		}
		if row.Status != string(protocol.AssistantStreaming) && row.Status != "" && message.Body == body {
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	} else {
		var stored string
		err := q.QueryRowContext(ctx, `SELECT r.body FROM background_replies r JOIN background_batches b USING(batch_id) JOIN main_turns t ON t.message_id=b.message_id AND t.thread_id=r.thread_id AND t.turn_id=? WHERE r.thread_id=? AND r.turn_id=? AND r.item_id=? AND r.kind='message' AND r.status IN ('interrupted','unknown','completed','failed')`, root, thread, turn, item).Scan(&stored)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			stored, err = store.ResolveJSON(ctx, q, stored)
			if err != nil {
				return err
			}
			if stored == body {
				return nil
			}
		}

	}
	return protocol.Errorf(protocol.CodeUnknownOutcome, "terminal reply prefix has not been persisted")
}
