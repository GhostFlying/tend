package store

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
)

// AgentRollout pages durable raw records. Long records have a separate bounded window.
func AgentRollout(ctx context.Context, q Querier, id, turn string, after int64, thread ...string) (protocol.AgentRolloutPage, error) {
	id = ConversationID(id)
	threadID := ""
	if len(thread) > 0 {
		threadID = thread[0]
	}
	v := protocol.AgentRolloutPage{ConversationID: id, Coverage: "Persisted native events captured since this capability was enabled. Earlier or uncollected native history is unavailable; conversation_read retains Tend message history separately.", Records: []protocol.AgentRolloutRecord{}, Threads: []string{}}
	if _, err := AgentConversation(ctx, q, id); err != nil {
		return v, err
	}
	if after < 0 {
		return v, protocol.Errorf(protocol.CodeInvalidRequest, "invalid rollout cursor")
	}
	threads, err := FollowupIDs(ctx, q, `SELECT thread_id FROM agent_thread_history WHERE conversation_id=? AND (?='' OR thread_id=?) ORDER BY created_at,thread_id`, id, threadID, threadID)
	if err != nil {
		return v, err
	}
	v.Threads = threads
	rows, err := q.QueryContext(ctx, `SELECT seq,thread_id,turn_id,item_id,kind,data_json,created_at FROM agent_rollout_events WHERE conversation_id=? AND (?='' OR turn_id=?) AND seq>? AND (?='' OR thread_id=?) ORDER BY seq LIMIT 31`, id, turn, turn, after, threadID, threadID)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var r protocol.AgentRolloutRecord
		if err = rows.Scan(&r.Seq, &r.ThreadID, &r.TurnID, &r.ItemID, &r.Kind, &r.Data, &r.CreatedAt); err != nil {
			return v, err
		}
		r.Data, r.Characters, err = JSONTextWindow(ctx, q, r.Data, 0, 16000)
		if err != nil {
			return v, err
		}
		r.Truncated = r.Characters > 16000
		v.Records = append(v.Records, r)
	}
	if len(v.Records) > 30 {
		v.More = true
		v.Records = v.Records[:30]
	}
	if len(v.Records) > 0 {
		v.Next = v.Records[len(v.Records)-1].Seq
	}
	return v, rows.Err()
}
func AgentRolloutWindow(ctx context.Context, q Querier, id string, seq, offset int64) (protocol.AgentRolloutWindow, error) {
	var v protocol.AgentRolloutWindow
	if offset < 0 || seq <= 0 {
		return v, protocol.Errorf(protocol.CodeInvalidRequest, "invalid rollout window")
	}
	var length int64
	err := q.QueryRowContext(ctx, `SELECT data_json FROM agent_rollout_events WHERE conversation_id=? AND seq=?`, ConversationID(id), seq).Scan(&v.Text)
	if err != nil {
		return v, notFound(err, "rollout record")
	}
	v.Text, length, err = JSONTextWindow(ctx, q, v.Text, offset, 16000)
	if err != nil {
		return v, err
	}
	v.Next = min(offset+int64(len([]rune(v.Text))), length)
	v.More = v.Next < length
	return v, nil
}
