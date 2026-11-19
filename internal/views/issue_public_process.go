package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// IssuePublicProcess reads only public tool records from the original background
// producer. It never publishes deferred text, executes tools or reads reasoning.
func IssuePublicProcess(ctx context.Context, q store.Querier, message string, after int64, limit int) (*protocol.IssueTurnProcess, error) {
	return issueProcessProjection(ctx, q, message, after, limit, false)
}

func issueProcessProjection(ctx context.Context, q store.Querier, message string, after int64, limit int, readable bool) (*protocol.IssueTurnProcess, error) {
	out, err := issuePublicProcess(ctx, q, message, after, limit, readable)
	if err != nil {
		// Process originals are auxiliary to the readable discussion. Keep a visible
		// failure and retry path without turning it into empty successful history.
		return &protocol.IssueTurnProcess{Items: []protocol.IssueConversationItem{}, Error: "公开过程读取失败，已保存的讨论仍可阅读。"}, nil
	}
	return out, nil
}

func issuePublicProcess(ctx context.Context, q store.Querier, message string, after int64, limit int, readable bool) (*protocol.IssueTurnProcess, error) {
	out := &protocol.IssueTurnProcess{Items: []protocol.IssueConversationItem{}}
	var start sql.NullInt64
	var active int
	if err := q.QueryRowContext(ctx, `SELECT MIN(created_at),COUNT(CASE WHEN status IN ('starting','started') THEN 1 END) FROM main_turns WHERE message_id=?`, message).Scan(&start, &active); err != nil {
		return nil, err
	}
	out.StartedAt = start.Int64
	var state string
	var end sql.NullInt64
	err := q.QueryRowContext(ctx, `WITH states AS (
 SELECT event_seq,created_at,COALESCE(json_extract(body_json,'$.state'),'') AS state
 FROM events WHERE object_type='message' AND object_id=? AND kind='conversation.message_state')
 SELECT state,(SELECT created_at FROM states
 WHERE state IN ('replied','processed','failed','interrupted') AND event_seq>COALESCE(
 (SELECT MAX(event_seq) FROM states WHERE state NOT IN ('replied','processed','failed','interrupted')),0)
 ORDER BY event_seq LIMIT 1)
 FROM states ORDER BY event_seq DESC LIMIT 1`, message).Scan(&state, &end)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Record updates and repeated terminal receipts are not new end facts.
	// A recovery or uncertain source stays open until a confirmed transition.
	if active == 0 && end.Valid {
		switch protocol.MessageState(state) {
		case protocol.MessageReplied, protocol.MessageProcessed, protocol.MessageFailed, protocol.MessageInterrupted:
			out.EndedAt = end.Int64
		}
	}
	const publicTools = ` FROM background_replies r JOIN background_batches b USING(batch_id)
 WHERE b.message_id=? AND r.kind='tool_use'
 AND EXISTS(SELECT 1 FROM main_turns t WHERE t.message_id=b.message_id AND t.thread_id=r.thread_id AND t.turn_id=r.turn_id)`
	if readable {
		var running bool
		var toolStart sql.NullInt64
		err := q.QueryRowContext(ctx, `WITH linked(id, created_at) AS MATERIALIZED (
 SELECT message_id,created_at FROM conversation_messages WHERE reply_to=? AND kind='tool_use'
 UNION SELECT m.message_id,m.created_at FROM conversation_reply_sources s JOIN conversation_messages m ON m.message_id=s.reply_id
 WHERE s.source_id=? AND m.kind='tool_use'
 ), running(tool, updated, id) AS MATERIALIZED (
 SELECT substr(t.tool,1,128),t.updated_at,t.message_id FROM linked l JOIN conversation_tool_uses t ON t.message_id=l.id
 WHERE t.status='running'
 )
 SELECT EXISTS(SELECT 1 FROM linked) OR EXISTS(SELECT 1`+publicTools+`),
 EXISTS(SELECT 1 FROM running) OR EXISTS(SELECT 1`+publicTools+`
 AND b.status IN ('pending','frozen') AND COALESCE((SELECT NULLIF(t.status,'') FROM conversation_tool_uses t
 WHERE t.thread_id=r.thread_id AND t.turn_id=r.turn_id AND t.item_id=r.item_id),r.status)='running'),
 COALESCE((SELECT tool FROM running ORDER BY updated DESC,id DESC LIMIT 1),''),
 (SELECT MIN(created_at) FROM linked)`, message, message, message, message).
			Scan(&out.HasTools, &running, &out.ActiveTool, &toolStart)
		if err != nil || (!start.Valid && !out.HasTools) {
			return nil, err
		}
		if !start.Valid {
			out.StartedAt = toolStart.Int64
		}
		out.State = protocol.MessageState(state)
		if active > 0 || (out.State == "" && running) {
			out.State = protocol.MessageProcessing
		} else if out.State == "" && out.HasTools {
			out.State = protocol.MessageUnknown
		}
		if out.State != protocol.MessageProcessing {
			out.ActiveTool = ""
		}
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT r.seq,r.thread_id,r.turn_id,r.item_id,r.tool_json,r.status,r.created_at,b.status`+publicTools+`
 AND r.seq>? ORDER BY r.seq LIMIT ?`, message, after, limit+1)
	if err != nil {
		return nil, err
	}
	type record struct {
		seq, created                           int64
		thread, turn, item, raw, status, batch string
	}
	var records []record
	for rows.Next() {
		var r record
		if err = rows.Scan(&r.seq, &r.thread, &r.turn, &r.item, &r.raw, &r.status, &r.created, &r.batch); err != nil {
			break
		}
		records = append(records, r)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if len(records) > limit {
		out.More = true
		records = records[:limit]
	}
	for _, r := range records {
		var p struct {
			Tool, Input, Output string
			ReplyTo             string `json:"reply_to"`
		}
		if r.raw != "" {
			full, err := store.ResolveToolRecord(ctx, q, r.raw)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal([]byte(full), &p); err != nil {
				return nil, err
			}
		}
		id := protocol.NativeObjectID(protocol.PrefixMessage, "native-tool:"+r.thread, r.turn+"/"+r.item)
		updated := r.created
		u, err := store.GetToolUse(ctx, q, r.turn, r.item)
		if err == nil {
			if u.ThreadID != r.thread {
				return nil, protocol.Errorf(protocol.CodeConflictPayload, "public tool belongs to another session")
			}
			id = u.MessageID
			if u.Tool != "" {
				p.Tool, p.Input = u.Tool, u.Input
			}
			// A callback may precede the named public event. Its confirmed result is
			// authoritative; an empty placeholder cannot erase the actual tool name.
			if u.Status != "" {
				r.status, p.Output, updated = u.Status, u.Output, u.UpdatedAt
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if r.status == "running" && r.batch != "pending" && r.batch != "frozen" {
			r.status = "interrupted"
		}
		it := protocol.IssueConversationItem{ConversationContent: protocol.ConversationContent{
			Seq: r.seq, MessageID: id, Kind: store.KindToolUse, Author: "main_agent", ReplyTo: message, CreatedAt: time.UnixMilli(r.created).UTC(),
			Tool: &protocol.ToolUseView{Name: p.Tool, Input: protocol.RedactOwnerToolInput(p.Tool, p.Input), Output: p.Output, Status: protocol.ToolStatus(r.status), TurnID: r.turn, UpdatedAt: time.UnixMilli(updated).UTC()},
		}}
		out.Items = append(out.Items, it)
	}
	if out.More {
		out.Next = records[len(records)-1].seq
	}
	if !start.Valid && len(records) == 0 {
		return nil, nil
	}
	return out, nil
}
