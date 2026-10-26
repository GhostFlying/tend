package views

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func ConversationQueue(ctx context.Context, q store.Querier, category string, after int64, limit int) (protocol.ConversationQueuePage, error) {
	out := protocol.ConversationQueuePage{Items: []protocol.ConversationQueueEntry{}}
	rows, total, err := store.ConversationQueue(ctx, q, category, after, limit+1)
	if err != nil {
		return out, err
	}
	out.Total = total
	if len(rows) > limit {
		out.Next = rows[limit-1].Position
		rows = rows[:limit]
	}

	for _, r := range rows {
		entry, err := conversationQueueEntry(ctx, q, r, category)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, entry)
	}
	active, err := store.ConversationActive(ctx, q)
	if err != nil {
		return out, err
	}
	if active != nil {
		entry, err := conversationQueueEntry(ctx, q, *active, "owner")
		if err != nil {
			return out, err
		}
		out.Active = &entry
	}
	out.EventSeq, err = store.LatestEventSeq(ctx, q)
	return out, err
}

func TranscriptBlock(ctx context.Context, q store.Querier, id string, after int64, limit int) (protocol.TranscriptBlock, error) {
	out := protocol.TranscriptBlock{Items: []protocol.ConversationItem{}}
	if err := q.QueryRowContext(ctx, `SELECT position,notice_only FROM conversation_blocks WHERE message_id=? ORDER BY notice_only LIMIT 1`, id).Scan(&out.Position, &out.NoticeOnly); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, store.ErrNotFound
		}
		return out, fmt.Errorf("views: transcript block: %w", err)
	}
	row, err := store.GetConversationItem(ctx, q, id)
	if err != nil {
		return out, err
	}
	out.Trigger, err = conversationItem(ctx, q, row)
	if err != nil {
		return out, err
	}
	if out.NoticeOnly {
		return out, nil
	}
	rows, err := store.TranscriptReplies(ctx, q, id, after, limit+1)
	if err != nil {
		return out, err
	}
	if len(rows) > limit {
		out.More = true
		rows = rows[:limit]
	}
	for _, r := range rows {
		it, err := conversationItem(ctx, q, r)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, it)
	}
	if out.More {
		out.NextSeq = rows[len(rows)-1].Seq
	}
	return out, nil
}
func Transcript(ctx context.Context, q store.Querier, before int64, limit int) (protocol.TranscriptPage, error) {
	out := protocol.TranscriptPage{Blocks: []protocol.TranscriptBlock{}}
	rows, err := store.TranscriptBlocks(ctx, q, before, limit+1)
	if err != nil {
		return out, err
	}
	if len(rows) > limit {
		out.More = true
		rows = rows[:limit]
	}
	slices.Reverse(rows)
	for _, r := range rows {
		b, err := TranscriptBlock(ctx, q, r.ID, 0, 50)
		if err != nil {
			return out, err
		}
		b.Position = r.Position
		b.NoticeOnly = r.NoticeOnly
		if r.NoticeOnly {
			b.Items = []protocol.ConversationItem{}
			b.More = false
			b.NextSeq = 0
		}
		out.Blocks = append(out.Blocks, b)
	}
	if out.More {
		out.NextBefore = rows[0].Position
	}
	out.EventSeq, err = store.LatestEventSeq(ctx, q)
	return out, err
}

func conversationQueueEntry(ctx context.Context, q store.Querier, r store.ConversationQueueRow, category string) (protocol.ConversationQueueEntry, error) {
	row, err := store.GetConversationItem(ctx, q, r.ID)
	if err != nil {
		return protocol.ConversationQueueEntry{}, err
	}
	item, err := conversationItem(ctx, q, row)
	if err != nil {
		return protocol.ConversationQueueEntry{}, err
	}
	status := r.Status
	if status == "pending" {
		status = "queued"
	}
	if status == "sending" {
		status = "starting"
	}
	if r.Turn == "started" {
		status = "processing"
	}
	if r.WithdrawnAt != 0 {
		status = "withdrawn"
	}
	return protocol.ConversationQueueEntry{Item: item, Position: r.Position, Revision: r.Revision, Status: status, CanWithdraw: category == "owner" && r.Status == "pending" && (r.Turn == "" || r.Turn == "rejected") && r.WithdrawnAt == 0}, nil
}
