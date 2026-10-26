package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

func BackgroundByMessage(ctx context.Context, q Querier, message string) (protocol.BackgroundBatch, error) {
	var b protocol.BackgroundBatch
	var checkin string
	err := q.QueryRowContext(ctx, `SELECT b.batch_id,b.work_item_id,b.message_id,b.status,b.created_at,b.frozen_at,b.checkin_json,(SELECT COUNT(*) FROM background_sources s WHERE s.batch_id=b.batch_id AND s.cancelled_at=0) FROM background_batches b WHERE b.message_id=?`, message).Scan(&b.BatchID, &b.WorkItemID, &b.MessageID, &b.Status, &b.CreatedAt, &b.FrozenAt, &checkin, &b.SourceCount)
	if err != nil {
		return b, notFound(err, "background batch")
	}
	if checkin != "" {
		err = json.Unmarshal([]byte(checkin), &b.Checkin)
	}
	return b, err
}
func BackgroundSources(ctx context.Context, q Querier, message string, after int64) (protocol.BackgroundSourcePage, error) {
	var p protocol.BackgroundSourcePage
	var err error
	p.Batch, err = BackgroundByMessage(ctx, q, message)
	if err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT s.seq,s.message_id,s.event_id,s.kind,s.rule_id,s.occurrence_id,s.cancelled_at,m.body,s.data_json FROM background_sources s JOIN conversation_messages m ON m.message_id=s.message_id WHERE s.batch_id=? AND s.seq>? ORDER BY s.seq LIMIT 51`, p.Batch.BatchID, after)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Sources = []protocol.BackgroundSource{}
	for rows.Next() {
		var s protocol.BackgroundSource
		var data string
		if err := rows.Scan(&s.Seq, &s.MessageID, &s.EventID, &s.Kind, &s.RuleID, &s.OccurrenceID, &s.CancelledAt, &s.Body, &data); err != nil {
			return p, err
		}
		if data != "" {
			s.Data = json.RawMessage(data)
		}
		p.Sources = append(p.Sources, s)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if len(p.Sources) > 50 {
		p.More = true
		p.Sources = p.Sources[:50]
		p.Next = p.Sources[49].Seq
	}
	return p, nil
}
