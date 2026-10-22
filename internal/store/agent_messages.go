package store

import (
	"context"
	"fmt"
)

// AgentMessageRow identifies a durable cumulative assistant message.
type AgentMessageRow struct {
	MessageID, ThreadID, TurnID, ItemID, Status string
	Revision                                    int64
}

// GetAgentMessage returns the assistant message of a native item.
func GetAgentMessage(ctx context.Context, q Querier, thread, turn, item string) (AgentMessageRow, error) {
	r := AgentMessageRow{ThreadID: thread, TurnID: turn, ItemID: item}
	err := q.QueryRowContext(ctx, `SELECT message_id, status, revision FROM conversation_agent_messages WHERE thread_id = ? AND turn_id = ? AND item_id = ?`, thread, turn, item).Scan(&r.MessageID, &r.Status, &r.Revision)
	if err != nil {
		return AgentMessageRow{}, notFound(err, "agent message "+item)
	}
	return r, nil
}

// PutAgentMessage updates the body and its monotonic metadata together.
func (t *Tx) PutAgentMessage(ctx context.Context, r AgentMessageRow, body string) error {
	if _, err := t.Exec(ctx, `UPDATE conversation_messages SET body = ? WHERE message_id = ?`, body, r.MessageID); err != nil {
		return fmt.Errorf("store: update assistant text: %w", err)
	}
	_, err := t.Exec(ctx, `INSERT INTO conversation_agent_messages (message_id, thread_id, turn_id, item_id, status, revision) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(message_id) DO UPDATE SET status = excluded.status, revision = excluded.revision`, r.MessageID, r.ThreadID, r.TurnID, r.ItemID, r.Status, r.Revision)
	if err != nil {
		return fmt.Errorf("store: put assistant metadata: %w", err)
	}
	return nil
}

// SettleAgentMessages preserves partial text when a harness session is lost.
func (t *Tx) SettleAgentMessages(ctx context.Context, thread string) ([]string, error) {
	rows, err := t.Q().QueryContext(ctx, `UPDATE conversation_agent_messages SET status = 'interrupted', revision = revision + 1 WHERE thread_id = ? AND status = 'streaming' RETURNING message_id`, thread)
	if err != nil {
		return nil, fmt.Errorf("store: settle assistant messages: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan settled assistant: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
