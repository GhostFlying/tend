package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func ConversationID(id string) string {
	if id == "" {
		return "main"
	}
	return id
}
func ConversationDestination(id string) string {
	if ConversationID(id) == "main" {
		return "main_thread"
	}
	return "background_thread:" + id
}
func MessageConversation(ctx context.Context, q Querier, id string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT conversation_id FROM conversation_messages WHERE message_id=?`, id).Scan(&v)
	if err != nil {
		return v, notFound(err, "message conversation")
	}
	return v, nil
}

const agentConversationQuery = `SELECT c.conversation_id AS conversation_id,COALESCE(c.work_item_id,''),COALESCE(json_extract(w.body_json,'$.title'),'主 Agent') AS title,c.revision,c.model,c.effort,c.last_message_seq,COALESCE(m.created_at,0),COALESCE(substr(m.body,1,160),''),CASE WHEN w.status='closed' THEN 'archived' WHEN c.work_item_id IS NOT NULL AND EXISTS(SELECT 1 FROM work_item_contexts wc WHERE wc.work_item_id=c.work_item_id AND wc.baseline_revision=0) THEN 'initializing' WHEN EXISTS(SELECT 1 FROM main_turns t JOIN conversation_messages x ON x.message_id=t.message_id WHERE x.conversation_id=c.conversation_id AND t.status='started') THEN 'running' WHEN EXISTS(SELECT 1 FROM outbox o WHERE o.destination=CASE WHEN c.conversation_id='main' THEN 'main_thread' ELSE 'background_thread:'||c.conversation_id END AND o.status IN ('unknown','sending')) THEN 'attention' WHEN EXISTS(SELECT 1 FROM outbox o WHERE o.destination=CASE WHEN c.conversation_id='main' THEN 'main_thread' ELSE 'background_thread:'||c.conversation_id END AND o.status='pending') THEN 'queued' ELSE 'idle' END AS status FROM agent_conversations c LEFT JOIN work_items w ON w.work_item_id=c.work_item_id LEFT JOIN conversation_messages m ON m.seq=c.last_message_seq`

func scanAgentConversation(row interface{ Scan(...any) error }) (protocol.AgentConversation, error) {
	var v protocol.AgentConversation
	err := row.Scan(&v.ID, &v.WorkItemID, &v.Title, &v.Revision, &v.Model, &v.Effort, &v.LastMessageSeq, &v.LastMessageAt, &v.Preview, &v.Status)
	if err != nil {
		return v, notFound(err, "agent conversation")
	}
	return v, nil
}
func AgentConversation(ctx context.Context, q Querier, id string) (protocol.AgentConversation, error) {
	return scanAgentConversation(q.QueryRowContext(ctx, agentConversationQuery+` WHERE c.conversation_id=?`, ConversationID(id)))
}
func AgentConversations(ctx context.Context, q Querier, search, status, cursor string, limit int) (protocol.AgentConversationPage, error) {
	out := protocol.AgentConversationPage{Items: []protocol.AgentConversation{}}
	if limit < 1 || limit > 100 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid page size")
	}
	var seq int64
	var id string
	if cursor != "" {
		a, b, ok := strings.Cut(cursor, ":")
		n, err := strconv.ParseInt(a, 10, 64)
		if !ok || err != nil || n < 0 || b == "" {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid conversation cursor")
		}
		seq, id = n, b
	}
	rows, err := q.QueryContext(ctx, `SELECT * FROM (`+agentConversationQuery+`) WHERE conversation_id<>'main' AND (?='' OR instr(lower(title),lower(?))>0) AND (?='' OR status=?) AND (?='' OR last_message_seq<? OR (last_message_seq=? AND conversation_id<?)) ORDER BY last_message_seq DESC,conversation_id DESC LIMIT ?`, search, search, status, status, cursor, seq, seq, id, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanAgentConversation(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.Next = fmt.Sprintf("%d:%s", last.LastMessageSeq, last.ID)
	}
	return out, rows.Err()
}
func MessageAssociations(ctx context.Context, q Querier, id string) (protocol.MessageAssociations, error) {
	out := protocol.MessageAssociations{MessageID: id, Items: []protocol.MessageWorkItem{}}
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM message_association_revisions WHERE message_id=m.message_id),0),COALESCE(d.disposition,'pending'),COALESCE(d.content_revision,1),COALESCE(d.facts_revision=d.content_revision,0),COALESCE(d.owner_locked,0) FROM conversation_messages m LEFT JOIN message_dispositions d ON d.message_id=m.message_id WHERE m.message_id=?`, id).Scan(&out.Revision, &out.Disposition, &out.ContentRevision, &out.FactsReviewed, &out.OwnerLocked)
	if err != nil {
		return out, notFound(err, "message")
	}
	var processing protocol.MessageProcessingState
	var hasProcessing bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_messages WHERE message_id=?)`, id).Scan(&hasProcessing); err != nil {
		return out, err
	}
	if hasProcessing {
		if err := q.QueryRowContext(ctx, `SELECT l.logical_turn_id,l.status,l.revision,l.reason FROM logical_turns l JOIN logical_turn_messages m ON m.logical_turn_id=l.logical_turn_id WHERE m.message_id=? ORDER BY CASE WHEN l.status='completed' THEN 2 WHEN l.resumed_by IS NOT NULL THEN 1 ELSE 0 END,l.updated_at DESC,l.rowid DESC LIMIT 1`, id).Scan(&processing.LogicalTurnID, &processing.Status, &processing.Revision, &processing.Reason); err != nil {
			return out, err
		}
		out.Processing = &processing
	}
	rows, err := q.QueryContext(ctx, `SELECT a.work_item_id,json_extract(w.body_json,'$.title'),a.source,a.owner_locked FROM message_work_items a JOIN work_items w ON w.work_item_id=a.work_item_id WHERE a.message_id=? AND a.active=1 ORDER BY a.work_item_id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v protocol.MessageWorkItem
		if err = rows.Scan(&v.WorkItemID, &v.Title, &v.Source, &v.OwnerLocked); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	return out, rows.Err()
}
func ContextUsage(ctx context.Context, q Querier, id string) (protocol.AgentContextUsage, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT snapshot_json FROM agent_context_usage WHERE conversation_id=?`, ConversationID(id)).Scan(&raw)
	if err != nil {
		return protocol.AgentContextUsage{}, notFound(err, "context usage")
	}
	var v protocol.AgentContextUsage
	err = json.Unmarshal([]byte(raw), &v)
	return v, err
}
func Compactions(ctx context.Context, q Querier, id string, before int64) (protocol.AgentCompactionPage, error) {
	out := protocol.AgentCompactionPage{Items: []protocol.AgentCompaction{}}
	rows, err := q.QueryContext(ctx, `SELECT rowid,operation_id,conversation_id,thread_id,status,source,created_at,updated_at,before_json,after_json FROM agent_compactions WHERE conversation_id=? AND (?=0 OR rowid<?) ORDER BY rowid DESC LIMIT 51`, ConversationID(id), before, before)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v protocol.AgentCompaction
		var a, b string
		var position int64
		if err = rows.Scan(&position, &v.ID, &v.ConversationID, &v.ThreadID, &v.Status, &v.Source, &v.CreatedAt, &v.UpdatedAt, &a, &b); err != nil {
			return out, err
		}
		if a != "" {
			if err = json.Unmarshal([]byte(a), &v.Before); err != nil {
				return out, err
			}
		}
		if b != "" {
			if err = json.Unmarshal([]byte(b), &v.After); err != nil {
				return out, err
			}
		}
		out.Items = append(out.Items, v)
		if len(out.Items) == 50 {
			out.Next = position
		}
	}
	if len(out.Items) > 50 {
		out.Items = out.Items[:50]

	}
	if len(out.Items) < 50 {
		out.Next = 0
	}
	return out, rows.Err()
}
