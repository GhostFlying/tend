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
	out, err := issuePublicProcess(ctx, q, message, after, limit)
	if err != nil {
		// Process originals are auxiliary to the readable discussion. Keep a visible
		// failure and retry path without turning it into empty successful history.
		return &protocol.IssueTurnProcess{Items: []protocol.IssueConversationItem{}, Error: "公开过程读取失败，已保存的讨论仍可阅读。"}, nil
	}
	return out, nil
}

func issuePublicProcess(ctx context.Context, q store.Querier, message string, after int64, limit int) (*protocol.IssueTurnProcess, error) {
	out := &protocol.IssueTurnProcess{Items: []protocol.IssueConversationItem{}}
	var start, end sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MIN(created_at),CASE WHEN SUM(status IN ('starting','started'))=0 THEN MAX(updated_at) END FROM main_turns WHERE message_id=?`, message).Scan(&start, &end); err != nil {
		return nil, err
	}
	out.StartedAt, out.EndedAt = start.Int64, end.Int64
	var state string
	var stateAt int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(body_json,'$.state'),''),created_at FROM events WHERE object_type='message' AND object_id=? AND kind='conversation.message_state' ORDER BY event_seq DESC LIMIT 1`, message).Scan(&state, &stateAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// A completed native attempt does not end the responsibility while its
	// durable source remains processing, for example during rate-limit recovery.
	if state == "processing" || state == "queued" {
		out.EndedAt = 0
	} else if out.EndedAt > 0 && stateAt > out.EndedAt {
		out.EndedAt = stateAt
	}
	rows, err := q.QueryContext(ctx, `SELECT r.seq,r.thread_id,r.turn_id,r.item_id,r.tool_json,r.status,r.created_at,b.status
 FROM background_replies r JOIN background_batches b USING(batch_id)
 WHERE b.message_id=? AND r.kind='tool_use' AND r.seq>?
 AND EXISTS(SELECT 1 FROM main_turns t WHERE t.message_id=b.message_id AND t.thread_id=r.thread_id AND t.turn_id=r.turn_id)
 ORDER BY r.seq LIMIT ?`, message, after, limit+1)
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
