package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

const runMessageColumns = `message_id,run_id,task_id,action_id,spec_version,generation,control_revision,sequence,text,source_json,source_message_id,request_id,status,reason,receiver,thread_id,turn_id,created_at,updated_at`

func RunMessagesEnabled(ctx context.Context, q Querier, run string) (bool, error) {
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT run_messages_enabled FROM runs WHERE run_id=?`, run).Scan(&enabled)
	if err != nil {
		return false, notFound(err, "run message capability")
	}
	return enabled, nil
}

func (t *Tx) EnableRunMessages(ctx context.Context, run string) error {
	_, err := t.Exec(ctx, `UPDATE runs SET run_messages_enabled=1 WHERE run_id=?`, run)
	return err
}

func scanRunMessage(row interface{ Scan(...any) error }) (protocol.RunMessage, error) {
	var m protocol.RunMessage
	var source string
	err := row.Scan(&m.MessageID, &m.RunID, &m.TaskID, &m.ActionID, &m.SpecVersion, &m.Generation, &m.ControlRevision, &m.Sequence, &m.Text, &source, &m.SourceMessageID, &m.RequestID, &m.Status, &m.Reason, &m.Receiver, &m.ThreadID, &m.TurnID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal([]byte(source), &m.Source)
}

func GetRunMessage(ctx context.Context, q Querier, id string) (protocol.RunMessage, error) {
	m, err := scanRunMessage(q.QueryRowContext(ctx, `SELECT `+runMessageColumns+` FROM run_messages WHERE message_id=?`, id))
	if err != nil {
		return m, notFound(err, "run message")
	}
	return m, loadRunMessageHistory(ctx, q, &m)
}

func loadRunMessageHistory(ctx context.Context, q Querier, m *protocol.RunMessage) error {
	rows, err := q.QueryContext(ctx, `SELECT status,reason,created_at FROM run_message_history WHERE message_id=? ORDER BY seq`, m.MessageID)
	if err != nil {
		return fmt.Errorf("store: read message history: %w", err)
	}
	defer rows.Close()
	m.History = []protocol.RunMessageHistory{}
	for rows.Next() {
		var h protocol.RunMessageHistory
		if err := rows.Scan(&h.Status, &h.Reason, &h.CreatedAt); err != nil {
			return err
		}
		m.History = append(m.History, h)
	}
	return rows.Err()
}

func ListRunMessages(ctx context.Context, q Querier, run string, after int64, limit int, pending bool) (protocol.RunMessagePage, error) {
	filter := ""
	if pending {
		filter = " AND status IN ('queued','dispatching','unknown')"
	}
	rows, err := q.QueryContext(ctx, `SELECT `+runMessageColumns+` FROM run_messages WHERE run_id=? AND sequence>?`+filter+` ORDER BY sequence LIMIT ?`, run, after, limit+1)
	if err != nil {
		return protocol.RunMessagePage{}, fmt.Errorf("store: list run messages: %w", err)
	}
	out := protocol.RunMessagePage{Messages: []protocol.RunMessage{}, Next: after}
	for rows.Next() {
		m, err := scanRunMessage(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Messages = append(out.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Messages) > limit {
		out.More = true
		out.Messages = out.Messages[:limit]
	}
	for i := range out.Messages {
		if err := loadRunMessageHistory(ctx, q, &out.Messages[i]); err != nil {
			return out, err
		}
		out.Next = out.Messages[i].Sequence
	}
	return out, nil
}

func (t *Tx) InsertRunMessage(ctx context.Context, m protocol.RunMessage) error {
	source, err := json.Marshal(m.Source)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO run_messages (`+runMessageColumns+`) VALUES (?,?,?,?,?,?,?,(SELECT COALESCE(MAX(sequence),0)+1 FROM run_messages WHERE run_id=?),?,?,?,?,?,?,?,?,?,?,?)`,
		m.MessageID, m.RunID, m.TaskID, m.ActionID, m.SpecVersion, m.Generation, m.ControlRevision, m.RunID, m.Text, string(source), m.SourceMessageID, m.RequestID, m.Status, m.Reason, m.Receiver, m.ThreadID, m.TurnID, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: insert run message: %w", err)
	}
	return t.insertRunMessageHistory(ctx, m.MessageID, m.Status, m.Reason, m.CreatedAt)
}

func (t *Tx) TransitionRunMessage(ctx context.Context, m protocol.RunMessage, now int64) error {
	_, err := t.Exec(ctx, `UPDATE run_messages SET status=?,reason=?,receiver=?,thread_id=?,turn_id=?,updated_at=? WHERE message_id=?`, m.Status, m.Reason, m.Receiver, m.ThreadID, m.TurnID, now, m.MessageID)
	if err != nil {
		return fmt.Errorf("store: update run message: %w", err)
	}
	return t.insertRunMessageHistory(ctx, m.MessageID, m.Status, m.Reason, now)
}

func (t *Tx) insertRunMessageHistory(ctx context.Context, id string, status protocol.RunMessageStatus, reason string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO run_message_history(message_id,status,reason,created_at) VALUES (?,?,?,?)`, id, status, reason, now)
	return err
}
