package store

import (
	"context"
	"database/sql"
	"fmt"
)

// MessageRow is a conversation message to store.
type MessageRow struct {
	MessageID   string
	Author      string
	ClientID    string
	Body        string
	ReplyTo     string
	LastSeenSeq int64
	RequestID   string
	CreatedAt   int64
}

// InsertMessage appends a conversation message and returns its sequence
// number, which orders the conversation.
func (t *Tx) InsertMessage(ctx context.Context, m MessageRow) (int64, error) {
	res, err := t.Exec(ctx, `INSERT INTO conversation_messages (message_id, author, client_id, body, reply_to, last_seen_seq,
		request_id, created_at) VALUES (?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?)`,
		m.MessageID, m.Author, nullString(m.ClientID), m.Body, nullString(m.ReplyTo), m.LastSeenSeq, nullString(m.RequestID), m.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("store: insert message: %w", err)
	}
	return res.LastInsertId()
}

// GetMessage returns a conversation message, or ErrNotFound.
func GetMessage(ctx context.Context, q Querier, messageID string) (MessageView, error) {
	m := MessageView{MessageID: messageID}
	err := q.QueryRowContext(ctx, `SELECT seq, author, body, COALESCE(reply_to, ''), created_at, COALESCE(last_seen_seq, 0)
		FROM conversation_messages WHERE message_id = ?`, messageID).
		Scan(&m.Seq, &m.Author, &m.Body, &m.ReplyTo, &m.CreatedAt, &m.LastSeenSeq)
	if err != nil {
		return MessageView{}, notFound(err, "message "+messageID)
	}
	return m, nil
}

// BindMessage ties a message to an object it concerns. Binding twice is a
// no-op.
func (t *Tx) BindMessage(ctx context.Context, messageID, objectType, objectID, kind string) error {
	_, err := t.Exec(ctx, `INSERT INTO conversation_bindings (message_id, object_type, object_id, kind) VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, messageID, objectType, objectID, kind)
	if err != nil {
		return fmt.Errorf("store: bind message: %w", err)
	}
	return nil
}

// GetMainThread returns the front thread of a harness instance, or
// ErrNotFound.
func GetMainThread(ctx context.Context, q Querier, instance string) (string, error) {
	var id string
	if err := q.QueryRowContext(ctx, `SELECT thread_id FROM main_threads WHERE instance = ?`, instance).Scan(&id); err != nil {
		return "", notFound(err, "main thread of "+instance)
	}
	return id, nil
}

// PutMainThread records the front thread of a harness instance.
func (t *Tx) PutMainThread(ctx context.Context, instance, threadID string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO main_threads (instance, thread_id, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (instance) DO UPDATE SET thread_id = excluded.thread_id, updated_at = excluded.updated_at`, instance, threadID, now, now)
	if err != nil {
		return fmt.Errorf("store: put main thread: %w", err)
	}
	return nil
}

// Main turn states.
const (
	TurnStarting    = "starting"
	TurnStarted     = "started"
	TurnCompleted   = "completed"
	TurnInterrupted = "interrupted"
	TurnFailed      = "failed"
	// TurnRejected marks a turn/start the harness refused: nothing
	// started, and the delivery may be tried again.
	TurnRejected = "rejected"
)

// MainTurn is a delivery of a message to the front thread.
type MainTurn struct {
	RequestID string
	MessageID string
	ThreadID  string
	TurnID    string
	Status    string
}

// GetMainTurn returns a delivery's turn, or ErrNotFound.
func GetMainTurn(ctx context.Context, q Querier, requestID string) (MainTurn, error) {
	m := MainTurn{RequestID: requestID}
	var turn sql.NullString
	err := q.QueryRowContext(ctx, `SELECT message_id, thread_id, turn_id, status FROM main_turns WHERE request_id = ?`, requestID).
		Scan(&m.MessageID, &m.ThreadID, &turn, &m.Status)
	if err != nil {
		return MainTurn{}, notFound(err, "main turn "+requestID)
	}
	m.TurnID = turn.String
	return m, nil
}

// GetMainTurnByMessage returns the delivery of a message, or ErrNotFound.
func GetMainTurnByMessage(ctx context.Context, q Querier, messageID string) (MainTurn, error) {
	var requestID string
	err := q.QueryRowContext(ctx, `SELECT request_id FROM main_turns WHERE message_id = ? ORDER BY created_at DESC, request_id DESC LIMIT 1`, messageID).Scan(&requestID)
	if err != nil {
		return MainTurn{}, notFound(err, "turn of message "+messageID)
	}
	return GetMainTurn(ctx, q, requestID)
}

// GetMainTurnByTurn returns the delivery a harness turn handled, or
// ErrNotFound.
func GetMainTurnByTurn(ctx context.Context, q Querier, threadID, turnID string) (MainTurn, error) {
	var requestID string
	err := q.QueryRowContext(ctx, `SELECT request_id FROM main_turns WHERE thread_id = ? AND turn_id = ?`, threadID, turnID).Scan(&requestID)
	if err != nil {
		return MainTurn{}, notFound(err, "turn "+turnID)
	}
	return GetMainTurn(ctx, q, requestID)
}

// PutMainTurn records or updates a delivery's turn.
func (t *Tx) PutMainTurn(ctx context.Context, m MainTurn, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO main_turns (request_id, message_id, thread_id, turn_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (request_id) DO UPDATE SET thread_id = excluded.thread_id,
		turn_id = excluded.turn_id, status = excluded.status, updated_at = excluded.updated_at`,
		m.RequestID, m.MessageID, m.ThreadID, nullString(m.TurnID), m.Status, now, now)
	if err != nil {
		return fmt.Errorf("store: put main turn: %w", err)
	}
	return nil
}

// SettleOpenTurns marks the turns of a thread that were still running as
// interrupted, for example after the harness restarted, and returns the
// messages they handled.
func (t *Tx) SettleOpenTurns(ctx context.Context, threadID string, now int64) ([]string, error) {
	rows, err := t.Q().QueryContext(ctx, `UPDATE main_turns SET status = 'interrupted', updated_at = ? WHERE thread_id = ? AND status = 'started'
		RETURNING message_id`, now, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: settle turns: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: settle turns: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: settle turns: %w", err)
	}
	return ids, nil
}
