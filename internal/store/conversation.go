package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Conversation item kinds: a message, or a tool use of the main agent
// that takes a place in the conversation's order (D69).
const (
	KindMessage = "message"
	KindToolUse = "tool_use"
)

// MessageRow is a conversation item to store.
type MessageRow struct {
	ConversationID string
	// Kind is KindMessage when empty.
	Kind        string
	MessageID   string
	Author      string
	ClientID    string
	Body        string
	ReplyTo     string
	LastSeenSeq int64
	RequestID   string
	CreatedAt   int64
	// DecisionJSON is the decision option an owner message chooses, if
	// any.
	DecisionJSON string
}

// InsertMessage appends a conversation item and returns its sequence
// number, which orders the conversation.
func (t *Tx) InsertMessage(ctx context.Context, m MessageRow) (int64, error) {
	if m.Kind == "" {
		m.Kind = KindMessage
	}
	res, err := t.Exec(ctx, `INSERT INTO conversation_messages (kind, message_id, author, client_id, body, reply_to, last_seen_seq,
		request_id, created_at, decision_json,conversation_id) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?)`,
		m.Kind, m.MessageID, m.Author, nullString(m.ClientID), m.Body, nullString(m.ReplyTo), m.LastSeenSeq, nullString(m.RequestID), m.CreatedAt,
		nullString(m.DecisionJSON), ConversationID(m.ConversationID))
	if err != nil {
		return 0, fmt.Errorf("store: insert message: %w", err)
	}
	return res.LastInsertId()
}

// GetMessage returns a conversation message, or ErrNotFound.
func GetMessage(ctx context.Context, q Querier, messageID string) (MessageView, error) {
	m := MessageView{MessageID: messageID}
	err := q.QueryRowContext(ctx, `SELECT seq, author, body, COALESCE(reply_to, ''), created_at, COALESCE(last_seen_seq, 0),
		COALESCE(decision_json, '') FROM conversation_messages WHERE message_id = ? AND kind = 'message'`, messageID).
		Scan(&m.Seq, &m.Author, &m.Body, &m.ReplyTo, &m.CreatedAt, &m.LastSeenSeq, &m.DecisionJSON)
	if err != nil {
		return MessageView{}, notFound(err, "message "+messageID)
	}
	return m, nil
}

// HasMessageTurnOperation identifies a system message that actually initiated
// a turn through the named outbox operation, including a turn already settled.
func HasMessageTurnOperation(ctx context.Context, q Querier, messageID, operation string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns t JOIN outbox o ON o.request_id=t.request_id
		WHERE t.message_id=? AND o.operation=?)`, messageID, operation).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("store: read message turn operation: %w", err)
	}
	return found, nil
}

// BindMessage ties a message to an object it concerns. Binding twice is a
// no-op.
func (t *Tx) BindMessage(ctx context.Context, messageID, objectType, objectID, kind string) error {
	if objectType == "task" && objectID != "" {
		rows, err := t.Q().QueryContext(ctx, `SELECT DISTINCT work_item_id FROM work_item_task_links WHERE task_id=?`, objectID)
		if err != nil {
			return err
		}
		var items []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			items = append(items, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range items {
			if err := t.BindMessage(ctx, messageID, "work_item", id, kind); err != nil {
				return err
			}
		}
	}
	if objectType == "work_item" {
		var blocked bool
		if err := t.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_dispositions d WHERE d.message_id=? AND d.owner_locked=1 AND NOT EXISTS(SELECT 1 FROM message_work_items a WHERE a.message_id=d.message_id AND a.work_item_id=? AND a.active=1))`, messageID, objectID).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return protocol.Errorf(protocol.CodeInvalidRequest, "owner message classification cannot be overridden by a business binding")
		}
	}

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
	Failure                      *protocol.AgentFailure
	NativeReconciled             bool // Set only after reading the exact native execution history.
	RequestID                    string
	MessageID                    string
	ThreadID                     string
	TurnID                       string
	Status                       string
	Model, ModelProvider, Effort string
	SettingsRevision             int64
}

// GetMainTurn returns a delivery's turn, or ErrNotFound.
func GetMainTurn(ctx context.Context, q Querier, requestID string) (MainTurn, error) {
	m := MainTurn{RequestID: requestID}
	var turn sql.NullString
	var failure string
	err := q.QueryRowContext(ctx, `SELECT message_id, thread_id, turn_id, status, model, model_provider, effort, settings_revision, failure_json FROM main_turns WHERE request_id = ?`, requestID).
		Scan(&m.MessageID, &m.ThreadID, &turn, &m.Status, &m.Model, &m.ModelProvider, &m.Effort, &m.SettingsRevision, &failure)
	if err == nil && failure != "" {
		err = json.Unmarshal([]byte(failure), &m.Failure)
	}
	if err != nil {
		return MainTurn{}, notFound(err, "main turn "+requestID)
	}
	m.TurnID = turn.String
	return m, nil
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

// GetOpenTurn returns the delivery of a message whose turn is starting or
// running, or ErrNotFound.
func GetOpenTurn(ctx context.Context, q Querier, messageID string) (MainTurn, error) {
	var requestID string
	err := q.QueryRowContext(ctx, `SELECT request_id FROM main_turns WHERE message_id = ? AND status IN ('starting', 'started')
		ORDER BY created_at DESC LIMIT 1`, messageID).Scan(&requestID)
	if err == sql.ErrNoRows {
		err = q.QueryRowContext(ctx, `WITH RECURSIVE successors(message_id) AS (SELECT ? UNION SELECT r.successor_message_id FROM agent_recoveries r JOIN successors s ON s.message_id=r.message_id WHERE r.status='cancelled' AND r.successor_message_id<>'') SELECT t.request_id FROM main_turns t JOIN successors s ON s.message_id=t.message_id WHERE t.status IN ('starting','started') ORDER BY t.created_at DESC LIMIT 1`, messageID).Scan(&requestID)
	}
	if err == sql.ErrNoRows {
		ids, lookupErr := LogicalResponsibilities(ctx, q, messageID)
		if lookupErr != nil {
			return MainTurn{}, lookupErr
		}
		for _, id := range ids {
			err = q.QueryRowContext(ctx, `SELECT t.request_id FROM main_turns t JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE e.logical_turn_id=? AND t.status IN ('starting','started') ORDER BY t.created_at DESC,e.rowid DESC LIMIT 1`, id).Scan(&requestID)
			if err != sql.ErrNoRows {
				break
			}
		}
	}
	if err != nil {
		return MainTurn{}, notFound(err, "open turn of "+messageID)
	}
	return GetMainTurn(ctx, q, requestID)
}

// LogicalResponsibilities follows adopted input through its current successor.
// Accepted supplements retain this link before a consumption receipt arrives.
func LogicalResponsibilities(ctx context.Context, q Querier, messageID string) ([]string, error) {
	return FollowupIDs(ctx, q, `WITH RECURSIVE responsibility(id) AS (
 SELECT logical_turn_id FROM logical_turn_messages WHERE message_id=?
 UNION SELECT e.logical_turn_id FROM main_steers s JOIN main_turns t ON t.thread_id=s.thread_id AND t.turn_id=s.turn_id JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE s.message_id=? AND (s.status IN ('accepted','consumed') OR s.native_item_id<>'')
 UNION SELECT l.resumed_by FROM logical_turns l JOIN responsibility r ON r.id=l.logical_turn_id WHERE l.resumed_by IS NOT NULL)
 SELECT l.logical_turn_id FROM logical_turns l JOIN responsibility r ON r.id=l.logical_turn_id ORDER BY l.rowid DESC`, messageID, messageID)
}

// PutMainTurn records or updates a delivery's turn.
func (t *Tx) PutMainTurn(ctx context.Context, m MainTurn, now int64) error {
	failure := ""
	if m.Failure != nil {
		data, err := json.Marshal(m.Failure)
		if err != nil {
			return err
		}
		failure = string(data)
	}
	_, err := t.Exec(ctx, `INSERT INTO main_turns (request_id, message_id, thread_id, turn_id, status, created_at, updated_at,
		model, model_provider, effort, settings_revision, failure_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (request_id) DO UPDATE SET thread_id = excluded.thread_id, turn_id = excluded.turn_id,
		status = excluded.status, updated_at = excluded.updated_at, model = excluded.model,
		model_provider = excluded.model_provider, effort = excluded.effort, settings_revision = excluded.settings_revision, failure_json=excluded.failure_json`,
		m.RequestID, m.MessageID, m.ThreadID, nullString(m.TurnID), m.Status, now, now,
		m.Model, m.ModelProvider, m.Effort, m.SettingsRevision, failure)
	if err != nil {
		return fmt.Errorf("store: put main turn: %w", err)
	}
	return nil
}

// SettleOpenTurns marks the turns of a thread that were still running as
// interrupted, for example after the harness restarted, and returns the
// execution identities and the messages they handled.
func (t *Tx) SettleOpenTurns(ctx context.Context, threadID string, now int64) ([]MainTurn, error) {
	rows, err := t.Q().QueryContext(ctx, `UPDATE main_turns SET status = 'interrupted', updated_at = ? WHERE thread_id = ? AND status = 'started'
		RETURNING request_id,message_id,turn_id`, now, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: settle turns: %w", err)
	}
	defer rows.Close()
	var turns []MainTurn
	for rows.Next() {
		turn := MainTurn{ThreadID: threadID, Status: TurnInterrupted}
		if err := rows.Scan(&turn.RequestID, &turn.MessageID, &turn.TurnID); err != nil {
			return nil, fmt.Errorf("store: settle turns: %w", err)
		}
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: settle turns: %w", err)
	}
	return turns, nil
}
