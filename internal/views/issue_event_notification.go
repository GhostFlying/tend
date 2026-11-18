package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const issueCompletionNotice = `e.object_type='task' AND e.kind='task.completed' AND EXISTS (
 SELECT 1 FROM background_sources s JOIN background_batches b USING(batch_id)
 WHERE s.event_id=e.event_id AND b.work_item_id=?)`

// IssueEventNotification reads exact links recorded by the notification writer.
// An event sequence inside a coalescing window does not prove batch membership.
func IssueEventNotification(ctx context.Context, q store.Querier, issue string, position, after int64) (protocol.IssueEventNotificationPage, error) {
	out := protocol.IssueEventNotificationPage{Notifications: []protocol.IssueEventNotification{}}
	err := q.QueryRowContext(ctx, `SELECT e.event_id FROM events e WHERE e.event_seq=? AND (
 (e.object_id=? AND ((e.object_type='issue' AND e.kind='issue.delivery_review_changed')
 OR (e.object_type='work_item' AND e.kind='work_item.delivery_changed')))
 OR (`+issueCompletionNotice+`))`, position, issue, issue).Scan(&out.EventID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, store.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT due_at FROM issue_change_notifications
 WHERE issue_id=? AND event_id=? AND due_at>0),0)`, issue, out.EventID).Scan(&out.CoalescingUntil); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `WITH sources AS (
 SELECT batch_id,seq,cancelled_at FROM background_sources WHERE event_id=?
 UNION
 SELECT s.batch_id,s.seq,s.cancelled_at FROM conversation_bindings c JOIN background_sources s USING(message_id)
 WHERE c.object_type='event' AND c.object_id=? AND c.kind='issue_change'
 ), matches AS (
 SELECT b.rowid AS position,b.batch_id,b.message_id,b.status,
 MAX(s.cancelled_at=0) AS has_active_source,
 MAX(s.cancelled_at=0 AND EXISTS(SELECT 1 FROM background_reads r
 JOIN main_turns t ON t.thread_id=r.thread_id AND t.turn_id=r.turn_id
 WHERE r.batch_id=s.batch_id AND r.source_seq=s.seq AND t.message_id=b.message_id)) AS source_read
 FROM sources s JOIN background_batches b USING(batch_id)
 WHERE b.work_item_id=? AND b.rowid>? GROUP BY b.rowid ORDER BY b.rowid LIMIT 21
 )
 SELECT m.position,m.batch_id,m.message_id,m.status,m.has_active_source,m.source_read,
 COALESCE((SELECT MIN(e.created_at) FROM events e
   WHERE e.object_type='message' AND e.object_id=m.message_id AND e.kind='conversation.message_state'
   AND json_extract(e.body_json,'$.state')='processing'),0),
 COALESCE(t.status,''),COALESCE(l.status,''),COALESCE(NULLIF(l.reason,''),(
   SELECT r.reason FROM logical_turn_executions e JOIN agent_recoveries r ON r.root_request_id=e.request_id
   WHERE e.logical_turn_id=l.logical_turn_id AND r.status='waiting' AND r.reason<>''
   ORDER BY r.updated_at DESC,r.root_request_id LIMIT 1),'')
 FROM matches m
 LEFT JOIN main_turns t ON t.request_id=(SELECT n.request_id FROM main_turns n
   WHERE n.message_id=m.message_id ORDER BY n.created_at DESC,n.rowid DESC LIMIT 1)
 LEFT JOIN logical_turns l ON l.logical_turn_id=(
   WITH RECURSIVE responsibility(id, successor) AS (
     SELECT n.logical_turn_id,n.resumed_by FROM logical_turns n
     WHERE n.logical_turn_id=(SELECT orig.logical_turn_id FROM logical_turns orig
       WHERE orig.message_id=m.message_id ORDER BY orig.updated_at DESC,orig.rowid DESC LIMIT 1)
     UNION
     SELECT n.logical_turn_id,n.resumed_by FROM logical_turns n JOIN responsibility r ON n.logical_turn_id=r.successor
   )
   SELECT id FROM responsibility WHERE successor IS NULL)
 ORDER BY m.position`, out.EventID, out.EventID, issue, after)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item protocol.IssueEventNotification
		var active, read bool
		if err := rows.Scan(&item.Position, &item.BatchID, &item.MessageID, &item.BatchStatus,
			&active, &read, &item.StartedAt, &item.NativeStatus, &item.Settlement, &item.Reason); err != nil {
			return out, err
		}
		item.Delivery = "unknown"
		item.Source = "unknown"
		switch {
		case read:
			item.Source = "read"
		case !active:
			item.Source = "cancelled"
		}
		if item.StartedAt > 0 || read {
			item.Delivery = "received"
		}
		if item.Settlement == "" {
			item.Settlement = "unknown"
		}
		out.Notifications = append(out.Notifications, item)
	}
	if len(out.Notifications) > 20 {
		out.Notifications = out.Notifications[:20]
		out.Next = out.Notifications[len(out.Notifications)-1].Position
	}
	return out, rows.Err()
}
