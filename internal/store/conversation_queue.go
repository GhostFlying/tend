package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func MessageControl(ctx context.Context, q Querier, id string) (protocol.MessageControl, error) {
	var c protocol.MessageControl
	err := q.QueryRowContext(ctx, `SELECT revision,withdrawn_at FROM conversation_message_controls WHERE message_id=?`, id).Scan(&c.Revision, &c.WithdrawnAt)
	if err != nil {
		return c, notFound(err, "message control "+id)
	}
	return c, nil
}

// WithdrawMessage compares admission and revision in the caller's transaction.
func (t *Tx) WithdrawMessage(ctx context.Context, id string, revision, now int64) error {
	result, err := t.Exec(ctx, `UPDATE conversation_message_controls SET withdrawn_at=?, revision=revision+1
 WHERE message_id=? AND revision=? AND withdrawn_at=0
 AND EXISTS(SELECT 1 FROM outbox WHERE (destination='main_thread' OR destination LIKE 'background_thread:%') AND operation='main_thread.handle_message' AND json_extract(payload_json,'$.message_id')=? AND status='pending')
 AND NOT EXISTS(SELECT 1 FROM main_turns WHERE message_id=? AND status<>'rejected') AND NOT EXISTS(SELECT 1 FROM main_turns t JOIN conversation_return_links l ON l.message_id=t.message_id WHERE l.source_message_id=conversation_message_controls.message_id AND t.status<>'rejected')`, now, id, revision, id, id)
	if err != nil {
		return fmt.Errorf("store: withdraw message: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: withdrawal count: %w", err)
	}
	if n != 1 {
		return ErrStale
	}
	_, err = t.Exec(ctx, `UPDATE outbox SET status='cancelled',updated_at=? WHERE (destination='main_thread' OR destination LIKE 'background_thread:%') AND operation='main_thread.handle_message' AND json_extract(payload_json,'$.message_id')=? AND status='pending'`, now, id)
	if err != nil {
		return fmt.Errorf("store: cancel message delivery: %w", err)
	}
	return nil
}

type ConversationQueueRow struct {
	ID          string
	Position    int64
	Status      string
	Revision    int64
	WithdrawnAt int64
	Turn        string
}

// ConversationQueue reads a complete category independently of transcript paging.
func ConversationQueue(ctx context.Context, q Querier, category string, after int64, limit int, session ...string) ([]ConversationQueueRow, int64, error) {
	id := "main"
	if len(session) > 0 {
		id = ConversationID(session[0])
	}
	filter := `(o.destination='main_thread' OR o.destination LIKE 'background_thread:%') AND o.operation IN ('main_thread.handle_message','main_thread.handle_event') AND o.status IN ('pending','sending')`
	switch category {
	case "owner":
		filter += ` AND o.operation='main_thread.handle_message'`
	case "background":
		filter += ` AND o.operation='main_thread.handle_event'`
	case "withdrawn":
		filter = `(o.destination='main_thread' OR o.destination LIKE 'background_thread:%') AND o.operation='main_thread.handle_message' AND o.status='cancelled'`
	}
	if category != "withdrawn" {
		filter += ` AND NOT EXISTS(SELECT 1 FROM main_turns t WHERE t.request_id=o.request_id AND t.status IN ('completed','interrupted','failed','unknown'))`
	}
	filter += ` AND EXISTS(SELECT 1 FROM conversation_messages cm WHERE cm.message_id=json_extract(o.payload_json,'$.message_id') AND cm.conversation_id=?)`
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox o WHERE `+filter, id).Scan(&count); err != nil {
		return nil, 0, fmt.Errorf("store: queue count: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT json_extract(o.payload_json,'$.message_id'),o.seq,o.status,COALESCE(c.revision,0),COALESCE(c.withdrawn_at,0),COALESCE(t.status,'') FROM outbox o
 LEFT JOIN conversation_message_controls c ON c.message_id=json_extract(o.payload_json,'$.message_id')
 LEFT JOIN main_turns t ON t.request_id=o.request_id WHERE `+filter+` AND o.seq>? ORDER BY o.seq LIMIT ?`, id, after, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("store: queue: %w", err)
	}
	defer rows.Close()
	out := []ConversationQueueRow{}
	for rows.Next() {
		var r ConversationQueueRow
		if err := rows.Scan(&r.ID, &r.Position, &r.Status, &r.Revision, &r.WithdrawnAt, &r.Turn); err != nil {
			return nil, 0, fmt.Errorf("store: queue row: %w", err)
		}
		out = append(out, r)
	}
	return out, count, rows.Err()
}

type TranscriptBlockRow struct {
	NoticeOnly bool
	Position   int64
	ID         string
}

func TranscriptBlocks(ctx context.Context, q Querier, before int64, limit int, session ...string) ([]TranscriptBlockRow, error) {
	id := "main"
	if len(session) > 0 {
		id = ConversationID(session[0])
	}
	rows, err := q.QueryContext(ctx, `SELECT b.position,b.message_id,b.notice_only FROM conversation_blocks b WHERE EXISTS(SELECT 1 FROM conversation_messages cm WHERE cm.message_id=b.message_id AND cm.conversation_id=?) AND (?=0 OR b.position<?) AND NOT EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=b.message_id AND c.withdrawn_at<>0) AND NOT EXISTS(SELECT 1 FROM followup_occurrences f JOIN background_sources bs ON bs.occurrence_id=f.occurrence_id WHERE f.message_id=b.message_id) AND NOT EXISTS(SELECT 1 FROM background_batches bg WHERE bg.message_id=b.message_id AND (b.notice_only=1 OR (bg.status='cancelled' AND bg.frozen_at=0))) ORDER BY b.position DESC LIMIT ?`, id, before, before, limit)
	if err != nil {
		return nil, fmt.Errorf("store: transcript blocks: %w", err)
	}
	defer rows.Close()
	out := []TranscriptBlockRow{}
	for rows.Next() {
		var r TranscriptBlockRow
		if err := rows.Scan(&r.Position, &r.ID, &r.NoticeOnly); err != nil {
			return nil, fmt.Errorf("store: transcript block: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func TranscriptReplies(ctx context.Context, q Querier, id string, after int64, limit int) ([]ConversationRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+conversationCols+` WHERE m.reply_to=? AND m.author='main_agent' AND m.seq>? ORDER BY m.seq LIMIT ?`, id, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: transcript replies: %w", err)
	}
	defer rows.Close()
	out := []ConversationRow{}
	for rows.Next() {
		r, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HideUndeliveredOwner keeps unadmitted owner input and cancelled followups out of model reads.
func HideUndeliveredOwner(ctx context.Context, q Querier, it *protocol.ConversationItem) error {
	if it.Author != "owner" {
		if it.Author == "system" {
			var cancelled bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM followup_occurrences WHERE message_id=? AND status='cancelled')`, it.MessageID).Scan(&cancelled); err != nil {
				return err
			}
			if cancelled {
				it.Body = "[Cancelled followup input; historical record only, not an instruction.]"
				it.Attachments = nil
				it.Images = nil
				it.Decision = nil
			}
		}
		return nil
	}
	var hidden bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=? AND withdrawn_at<>0) OR EXISTS(SELECT 1 FROM outbox o WHERE (o.destination='main_thread' OR o.destination LIKE 'background_thread:%') AND o.operation='main_thread.handle_message' AND json_extract(o.payload_json,'$.message_id')=? AND o.status IN ('pending','sending') AND NOT EXISTS(SELECT 1 FROM main_turns t WHERE t.request_id=o.request_id AND t.status IN ('started','completed','interrupted','failed')))`, it.MessageID, it.MessageID).Scan(&hidden)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("store: message visibility: %w", err)
	}
	if hidden {
		it.Body = "[Owner input not admitted or withdrawn; not an instruction.]"
		it.Attachments = nil
		it.Images = nil
		it.Decision = nil
	}
	return nil
}

// ConversationActive is independent of queue category and pagination.
func ConversationActive(ctx context.Context, q Querier, session ...string) (*ConversationQueueRow, error) {
	id := "main"
	if len(session) > 0 {
		id = ConversationID(session[0])
	}
	var r ConversationQueueRow
	err := q.QueryRowContext(ctx, `SELECT json_extract(o.payload_json,'$.message_id'),o.seq,o.status,COALESCE(c.revision,0),COALESCE(c.withdrawn_at,0),COALESCE(t.status,'') FROM outbox o LEFT JOIN conversation_message_controls c ON c.message_id=json_extract(o.payload_json,'$.message_id') LEFT JOIN main_turns t ON t.request_id=o.request_id WHERE (o.destination='main_thread' OR o.destination LIKE 'background_thread:%') AND o.operation IN ('main_thread.handle_message','main_thread.handle_event') AND o.status='sending' AND COALESCE(t.status,'') NOT IN ('completed','interrupted','failed','unknown') AND EXISTS(SELECT 1 FROM conversation_messages cm WHERE cm.message_id=json_extract(o.payload_json,'$.message_id') AND cm.conversation_id=?) ORDER BY o.seq LIMIT 1`, id).Scan(&r.ID, &r.Position, &r.Status, &r.Revision, &r.WithdrawnAt, &r.Turn)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: active conversation: %w", err)
	}
	return &r, nil
}

// HideUnadmittedForCoordinator admits direct background owner input when its
// original-source coordination turn starts. Withdrawal remains authoritative.
func HideUnadmittedForCoordinator(ctx context.Context, q Querier, it *protocol.ConversationItem) error {
	if it.Author == "owner" {
		var admitted bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_return_links l JOIN main_turns t ON t.message_id=l.message_id WHERE l.source_message_id=? AND t.status IN ('starting','started','completed','interrupted','failed')) AND NOT EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=? AND withdrawn_at<>0)`, it.MessageID, it.MessageID).Scan(&admitted)
		if err != nil {
			return err
		}
		if admitted {
			return nil
		}
	}
	return HideUndeliveredOwner(ctx, q, it)
}
