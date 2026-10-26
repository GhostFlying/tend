package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// bufferBackgroundReply keeps ordinary output out of the transcript until the
// background turn has made its final visible/silent choice.
func bufferBackgroundReply(ctx context.Context, x *Exec, tx *store.Tx, p recordAgentMessagePayload) (bool, Outcome, error) {
	b, handled, err := backgroundOutputBatch(ctx, x, tx, p.ReplyTo, p.TurnID)
	if err != nil || !handled {
		return handled, Outcome{}, err
	}
	if p.ItemID == "" {
		return true, Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "background reply requires item identity")
	}
	status := p.Status
	if status == "" {
		status = protocol.AssistantCompleted
	}
	var old, oldStatus string
	err = tx.Q().QueryRowContext(ctx, `SELECT body,status FROM background_replies WHERE batch_id=? AND thread_id=? AND turn_id=? AND item_id=?`, b.BatchID, x.Principal.Thread, p.TurnID, p.ItemID).Scan(&old, &oldStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return true, Outcome{}, err
	}
	result := Outcome{Result: map[string]any{"background_batch_id": b.BatchID, "retained": true}}
	if err == nil && (oldStatus != string(protocol.AssistantStreaming) || (status == protocol.AssistantStreaming && !strings.HasPrefix(p.Body, old))) {
		return true, result, nil
	}
	if p.Body == "" && errors.Is(err, sql.ErrNoRows) {
		return true, Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "body is required")
	}
	_, err = tx.Exec(ctx, `INSERT INTO background_replies(batch_id,thread_id,turn_id,item_id,body,status,request_id,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(batch_id,thread_id,turn_id,item_id) DO UPDATE SET body=excluded.body,status=excluded.status`, b.BatchID, x.Principal.Thread, p.TurnID, p.ItemID, p.Body, string(status), x.Env.RequestID, x.Now.UnixMilli())
	if err != nil {
		return true, Outcome{}, err
	}
	// Late terminal output follows the already settled batch disposition.
	if b.Status != "pending" && b.Status != "frozen" {
		err = publishBackgroundReplies(ctx, x, tx, b, b.Status)
	}
	return true, result, err
}

// backgroundOutputBatch validates both live and late native callbacks.
func backgroundOutputBatch(ctx context.Context, x *Exec, tx *store.Tx, message, turnID string) (protocol.BackgroundBatch, bool, error) {
	b, err := store.BackgroundByMessage(ctx, tx.Q(), message)
	if errors.Is(err, store.ErrNotFound) {
		return b, false, nil
	}
	if err != nil {
		return b, true, err
	}
	if turnID == "" {
		return b, true, protocol.Errorf(protocol.CodeInvalidRequest, "background output requires turn identity")
	}
	turn, err := store.GetOpenTurn(ctx, tx.Q(), message)
	if errors.Is(err, store.ErrNotFound) {
		turn, err = store.GetMainTurnByTurn(ctx, tx.Q(), x.Principal.Thread, turnID)
	}
	if err != nil {
		return b, true, err
	}
	if turn.MessageID != message || turn.ThreadID != x.Principal.Thread || (turn.TurnID != "" && turn.TurnID != turnID) {
		return b, true, protocol.Errorf(protocol.CodeForbidden, "background output turn mismatch")
	}
	return b, true, nil
}

// Tool records share the deferred arrival sequence with prose. They are always
// retained in the audit transcript, including after a silent check-in.
func bufferBackgroundTool(ctx context.Context, x *Exec, tx *store.Tx, p recordToolUsePayload) (bool, Outcome, error) {
	b, handled, err := backgroundOutputBatch(ctx, x, tx, p.ReplyTo, p.TurnID)
	if err != nil || !handled {
		return handled, Outcome{}, err
	}
	var old string
	err = tx.Q().QueryRowContext(ctx, `SELECT status FROM background_replies WHERE batch_id=? AND thread_id=? AND turn_id=? AND item_id=?`, b.BatchID, x.Principal.Thread, p.TurnID, p.ItemID).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return true, Outcome{}, err
	}
	out := Outcome{Result: map[string]any{"background_batch_id": b.BatchID, "retained": true}}
	if err == nil && old != string(protocol.ToolRunning) && (old != string(protocol.ToolInterrupted) || p.Status == protocol.ToolRunning) {
		return true, out, nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return true, Outcome{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO background_replies(batch_id,thread_id,turn_id,item_id,body,kind,tool_json,status,request_id,created_at) VALUES(?,?,?,?,'','tool_use',?,?,?,?) ON CONFLICT(batch_id,thread_id,turn_id,item_id) DO UPDATE SET tool_json=excluded.tool_json,status=excluded.status,published=0`, b.BatchID, x.Principal.Thread, p.TurnID, p.ItemID, string(raw), string(p.Status), x.Env.RequestID, x.Now.UnixMilli())
	if err == nil && b.Status != "pending" && b.Status != "frozen" {
		err = publishBackgroundReplies(ctx, x, tx, b, b.Status)
	}
	return true, out, err
}

func publishBackgroundReplies(ctx context.Context, x *Exec, tx *store.Tx, b protocol.BackgroundBatch, status string) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT thread_id,turn_id,item_id,body,status,request_id,kind,tool_json,created_at FROM background_replies WHERE batch_id=? AND published=0 ORDER BY seq`, b.BatchID)
	if err != nil {
		return err
	}
	type reply struct {
		thread, turn, item, body, status, request, kind, tool string
		created                                               int64
	}
	var replies []reply
	for rows.Next() {
		var r reply
		if err := rows.Scan(&r.thread, &r.turn, &r.item, &r.body, &r.status, &r.request, &r.kind, &r.tool, &r.created); err != nil {
			rows.Close()
			return err
		}
		replies = append(replies, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range replies {
		copy := *x
		env := *x.Env
		env.RequestID = r.request
		copy.Env = &env
		copy.Now = time.UnixMilli(r.created)
		copy.Principal = protocol.Principal{Kind: protocol.PrincipalMainAgent, Thread: r.thread}
		var out Outcome
		if r.kind == "tool_use" {
			var p recordToolUsePayload
			if err := json.Unmarshal([]byte(r.tool), &p); err != nil {
				return err
			}
			if p.Status == protocol.ToolRunning {
				p.Status = protocol.ToolInterrupted
			}
			out, err = recordVisibleToolUse(ctx, &copy, tx, p)
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE background_replies SET status=? WHERE batch_id=? AND thread_id=? AND turn_id=? AND item_id=?`, string(p.Status), b.BatchID, r.thread, r.turn, r.item)
			}
		} else if r.body != "" && !(status == store.TurnCompleted && b.Checkin != nil) {
			state := protocol.AssistantState(r.status)
			if state == protocol.AssistantStreaming {
				state = protocol.AssistantInterrupted
				if status == store.TurnCompleted {
					state = protocol.AssistantUnknown
				}
			}
			env.Payload, err = json.Marshal(recordAgentMessagePayload{Body: r.body, ReplyTo: b.MessageID, TurnID: r.turn, ItemID: r.item, Status: state})
			if err == nil {
				out, err = recordVisibleAgentMessage(ctx, &copy, tx)
			}
		}
		if err != nil {
			return err
		}
		for _, e := range out.Events {
			body, err := json.Marshal(e.Body)
			if err != nil {
				return err
			}
			if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: e.ObjectType, ObjectID: e.ObjectID, ObjectVersion: e.ObjectVersion, Kind: e.Kind, BodyJSON: string(body), RequestID: r.request, CreatedAt: x.Now.UnixMilli()}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE background_replies SET published=1 WHERE batch_id=? AND thread_id=? AND turn_id=? AND item_id=?`, b.BatchID, r.thread, r.turn, r.item); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) settleBackground(ctx context.Context, tx *store.Tx, message, status string, now int64) error {
	if status == store.TurnStarting || status == store.TurnStarted || status == store.TurnRejected {
		return nil
	}
	b, err := store.BackgroundByMessage(ctx, tx.Q(), message)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.Checkin != nil && status == store.TurnCompleted {
		var thread string
		var turn sql.NullString
		if err := tx.Q().QueryRowContext(ctx, `SELECT thread_id,turn_id FROM main_turns WHERE message_id=? ORDER BY created_at DESC LIMIT 1`, message).Scan(&thread, &turn); err != nil {
			return err
		}
		if b.Checkin.ThreadID != thread || b.Checkin.TurnID != turn.String {
			b.Checkin = nil
			if _, err := tx.Exec(ctx, `UPDATE background_batches SET checkin_json='' WHERE batch_id=?`, b.BatchID); err != nil {
				return err
			}
		}
	}
	if b.Checkin != nil && status == store.TurnCompleted {
		var pending int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_completion_reviews r WHERE r.reviewed_revision IS NULL AND (r.work_item_id=? OR EXISTS(SELECT 1 FROM background_sources s WHERE s.batch_id=? AND s.event_id=r.event_id AND s.cancelled_at=0))`, b.WorkItemID, b.BatchID).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			b.Checkin = nil
			if _, err := tx.Exec(ctx, `UPDATE background_batches SET checkin_json='' WHERE batch_id=?`, b.BatchID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE background_batches SET status=? WHERE batch_id=?`, status, b.BatchID); err != nil {
		return err
	}
	x := &Exec{Env: &protocol.Envelope{}, IDs: a.ids, Now: time.UnixMilli(now)}
	return publishBackgroundReplies(ctx, x, tx, b, status)
}
