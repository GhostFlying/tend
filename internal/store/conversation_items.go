package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ToolUseRow is a tool use of the main agent in the conversation.
type ToolUseRow struct {
	MessageID string
	// ThreadID is the main agent's thread, so the uses of a thread whose
	// harness went away are found even when tend never learned the
	// turn's ID.
	ThreadID  string
	TurnID    string
	ItemID    string
	Tool      string
	Input     string
	Output    string
	Status    string
	UpdatedAt int64
}

// GetToolUse returns the tool use of a turn's item, or ErrNotFound.
func GetToolUse(ctx context.Context, q Querier, turnID, itemID string) (ToolUseRow, error) {
	u := ToolUseRow{TurnID: turnID, ItemID: itemID}
	err := q.QueryRowContext(ctx, `SELECT message_id, thread_id, tool, input, output, status, updated_at FROM conversation_tool_uses
		WHERE turn_id = ? AND item_id = ?`, turnID, itemID).Scan(&u.MessageID, &u.ThreadID, &u.Tool, &u.Input, &u.Output, &u.Status, &u.UpdatedAt)
	if err != nil {
		return ToolUseRow{}, notFound(err, "tool use "+itemID)
	}
	return u, nil
}

// PutToolUse records a tool use's details; its conversation item must
// exist.
func (t *Tx) PutToolUse(ctx context.Context, u ToolUseRow) error {
	_, err := t.Exec(ctx, `INSERT INTO conversation_tool_uses (message_id, thread_id, turn_id, item_id, tool, input, output, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (message_id) DO UPDATE SET tool = excluded.tool, input = excluded.input,
		output = excluded.output, status = excluded.status, updated_at = excluded.updated_at`,
		u.MessageID, u.ThreadID, u.TurnID, u.ItemID, u.Tool, u.Input, u.Output, u.Status, u.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: put tool use: %w", err)
	}
	return nil
}

// GetConversationSeq returns the sequence number of a conversation item,
// or ErrNotFound.
func GetConversationSeq(ctx context.Context, q Querier, messageID string) (int64, error) {
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT seq FROM conversation_messages WHERE message_id = ?`, messageID).Scan(&seq); err != nil {
		return 0, notFound(err, "conversation item "+messageID)
	}
	return seq, nil
}

// SettleToolUses marks the tool uses of a thread's turns that were still
// running as interrupted, after the harness went away, and returns them.
func (t *Tx) SettleToolUses(ctx context.Context, threadID string, now int64) ([]string, error) {
	rows, err := t.Q().QueryContext(ctx, `UPDATE conversation_tool_uses SET status = 'interrupted', updated_at = ?
		WHERE status = 'running' AND thread_id = ?
		RETURNING message_id`, now, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: settle tool uses: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: settle tool uses: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ConversationRow is one item of the conversation as the owner reads it.
type ConversationRow struct {
	Seq       int64
	Kind      string
	MessageID string
	Author    string
	Body      string
	ReplyTo   string
	CreatedAt int64
	// State is the latest state recorded for an owner message, empty
	// when none was.
	State string
	// Tool is set for a tool use.
	Tool *ToolUseRow
	// DecisionJSON is the decision option an owner message chooses.
	DecisionJSON string
}

// conversationCols selects a ConversationRow from conversation_messages m.
const conversationCols = `m.seq, m.kind, m.message_id, m.author, m.body, COALESCE(m.reply_to, ''), m.created_at,
	COALESCE((SELECT json_extract(e.body_json, '$.state') FROM events e WHERE e.object_type = 'message'
		AND e.object_id = m.message_id AND e.kind = 'conversation.message_state' ORDER BY e.event_seq DESC LIMIT 1), ''),
	u.turn_id, u.item_id, u.tool, u.input, u.output, u.status, u.updated_at, COALESCE(m.decision_json, '')
	FROM conversation_messages m LEFT JOIN conversation_tool_uses u ON u.message_id = m.message_id`

// ListConversation returns up to limit conversation items in order:
// those after seq after when after is positive, else those before seq
// before when before is positive, else the latest.
func ListConversation(ctx context.Context, q Querier, before, after int64, limit int) ([]ConversationRow, error) {
	var query string
	var args []any
	switch {
	case after > 0:
		query, args = `SELECT `+conversationCols+` WHERE m.seq > ? ORDER BY m.seq LIMIT ?`, []any{after, limit}
	case before > 0:
		query, args = `SELECT * FROM (SELECT `+conversationCols+` WHERE m.seq < ? ORDER BY m.seq DESC LIMIT ?) ORDER BY 1`, []any{before, limit}
	default:
		query, args = `SELECT * FROM (SELECT `+conversationCols+` ORDER BY m.seq DESC LIMIT ?) ORDER BY 1`, []any{limit}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list conversation: %w", err)
	}
	defer rows.Close()
	var out []ConversationRow
	for rows.Next() {
		r, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetConversationItem returns one conversation item, or ErrNotFound.
func GetConversationItem(ctx context.Context, q Querier, messageID string) (ConversationRow, error) {
	r, err := scanConversation(q.QueryRowContext(ctx, `SELECT `+conversationCols+` WHERE m.message_id = ?`, messageID))
	if err != nil {
		return ConversationRow{}, notFound(err, "conversation item "+messageID)
	}
	return r, nil
}

func scanConversation(row interface{ Scan(...any) error }) (ConversationRow, error) {
	var r ConversationRow
	var turn, item, tool, input, output, status sql.NullString
	var updated sql.NullInt64
	if err := row.Scan(&r.Seq, &r.Kind, &r.MessageID, &r.Author, &r.Body, &r.ReplyTo, &r.CreatedAt, &r.State,
		&turn, &item, &tool, &input, &output, &status, &updated, &r.DecisionJSON); err != nil {
		return ConversationRow{}, fmt.Errorf("store: scan conversation: %w", err)
	}
	if tool.Valid {
		r.Tool = &ToolUseRow{MessageID: r.MessageID, TurnID: turn.String, ItemID: item.String, Tool: tool.String,
			Input: input.String, Output: output.String, Status: status.String, UpdatedAt: updated.Int64}
	}
	return r, nil
}

// ListEvents returns up to limit events after seq, in order.
func ListEvents(ctx context.Context, q Querier, after int64, limit int) ([]EventView, error) {
	rows, err := q.QueryContext(ctx, `SELECT event_seq, object_type, object_id, kind, created_at FROM events
		WHERE event_seq > ? ORDER BY event_seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()
	var out []EventView
	for rows.Next() {
		var e EventView
		if err := rows.Scan(&e.Seq, &e.ObjectType, &e.ObjectID, &e.Kind, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
