package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (a *App) MessagingChannels(ctx context.Context) ([]protocol.MessagingChannel, error) {
	result := []protocol.MessagingChannel{}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT c.channel_id,c.status,c.revision,c.mark_read_mode,c.health,c.last_observed_at,(SELECT count(*) FROM telegram_inbox i WHERE i.bot_id=c.bot_id AND i.state='pending') FROM messaging_channels c ORDER BY channel_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c protocol.MessagingChannel
			var readMode string
			if err = rows.Scan(&c.ID, &c.Status, &c.Revision, &readMode, &c.Health, &c.LastObservedAt, &c.PendingInputs); err != nil {
				return err
			}
			c.BotMarkRead = c.ID == "web" || readMode != ""
			c.Text = true
			c.Files = true
			c.UserReadReceipts = c.ID == "web"
			result = append(result, c)
		}
		return rows.Err()
	})
	return result, err
}

// MessagingMessages reads communication, never arbitrary native prose or tools.
func (a *App) MessagingMessages(ctx context.Context, after int64, conversation, issue, channel string) (protocol.MessagingPage, error) {
	page := protocol.MessagingPage{Items: []protocol.MessagingMessage{}, Next: after}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT c.message_id FROM messaging_messages m JOIN conversation_messages c USING(message_id) WHERE c.seq>? AND (?='' OR c.conversation_id=?) AND (?='' OR m.channel_id=?) AND (?='' OR EXISTS(SELECT 1 FROM messaging_issue_links w WHERE w.message_id=c.message_id AND w.work_item_id=?)) ORDER BY c.seq LIMIT 101`, after, conversation, conversation, channel, channel, issue, issue)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		page.More = len(ids) > 100
		if page.More {
			ids = ids[:100]
		}
		for _, id := range ids {
			m, err := messagingMessage(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, m)
			page.Next = m.Sequence
		}
		return nil
	})
	return page, err
}
func (a *App) MessagingMessage(ctx context.Context, id string) (protocol.MessagingMessage, error) {
	var m protocol.MessagingMessage
	err := a.store.Read(ctx, func(tx *store.ReadTx) error { var err error; m, err = messagingMessage(ctx, tx.Q(), id); return err })
	return m, err
}

const messagingTurnPredicate = `(t.message_id=? OR t.request_id IN (SELECT e.request_id FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE m.message_id=?) OR EXISTS(SELECT 1 FROM main_steers s WHERE s.message_id=? AND s.thread_id=t.thread_id AND s.turn_id=t.turn_id) OR EXISTS(SELECT 1 FROM background_batches b JOIN background_sources s USING(batch_id) WHERE b.message_id=t.message_id AND s.message_id=? AND s.cancelled_at=0))`

func messagingMessage(ctx context.Context, q store.Querier, id string) (protocol.MessagingMessage, error) {
	m := protocol.MessagingMessage{Attachments: []protocol.ConversationAttachment{}, IssueIDs: []string{}}
	err := q.QueryRowContext(ctx, `SELECT c.message_id,c.seq,c.conversation_id,c.author,c.body,d.content_revision,m.direction,m.channel_id,m.reply_to,m.correction_of,m.purpose,m.reason,m.execution_id,c.created_at FROM messaging_messages m JOIN conversation_messages c USING(message_id) JOIN message_dispositions d USING(message_id) WHERE m.message_id=?`, id).Scan(&m.MessageID, &m.Sequence, &m.ConversationID, &m.Author, &m.Body, &m.ContentRevision, &m.Direction, &m.ChannelID, &m.ReplyTo, &m.CorrectionOf, &m.Purpose, &m.Reason, &m.ExecutionID, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, protocol.Errorf(protocol.CodeNotFound, "communication message not found")
	}
	if err != nil {
		return m, err
	}
	m.Attachments, err = store.ConversationAttachments(ctx, q, id)
	if err != nil {
		return m, err
	}
	if m.Attachments == nil {
		m.Attachments = []protocol.ConversationAttachment{}
	}
	m.IssueIDs, err = communicationIssues(ctx, q, id)
	if err != nil {
		return m, err
	}
	if m.IssueIDs == nil {
		m.IssueIDs = []string{}
	}
	m.SourceMessageIDs, err = store.FollowupIDs(ctx, q, `SELECT source_message_id FROM messaging_sources WHERE message_id=? ORDER BY source_message_id`, id)
	if err != nil {
		return m, err
	}
	if m.SourceMessageIDs == nil {
		m.SourceMessageIDs = []string{}
	}
	m.Interaction, err = interactionForMessage(ctx, q, id)
	if err != nil {
		return m, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(max(read_at),0) FROM messaging_reads WHERE message_id=? AND content_revision=? AND reader LIKE 'agent:%'`, id, m.ContentRevision).Scan(&m.AgentReadAt); err != nil {
		return m, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT CASE WHEN o.status='sending' THEN 'sending' ELSE d.state END FROM messaging_deliveries d LEFT JOIN outbox o ON o.outbox_id=d.delivery_id WHERE d.message_id=? AND d.kind IN ('text','file') ORDER BY CASE d.state WHEN 'unknown' THEN 0 WHEN 'failed' THEN 1 WHEN 'queued' THEN 2 WHEN 'unavailable' THEN 3 ELSE 4 END LIMIT 1),'')`, id).Scan(&m.DeliveryState); err != nil {
		return m, err
	}
	m.Processing = "pending"
	var messageState string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT json_extract(body_json,'$.state') FROM events WHERE object_id=? AND kind='conversation.message_state' ORDER BY event_seq DESC LIMIT 1),'')`, id).Scan(&messageState); err != nil {
		return m, err
	}
	switch messageState {
	case "failed", "interrupted", "unknown":
		m.Processing = messageState
	case "processing":
		m.Processing = "processing"
	case "replied":
		m.Processing = "processed"
	}
	var state string
	err = q.QueryRowContext(ctx, `SELECT t.status FROM main_turns t WHERE `+messagingTurnPredicate+` ORDER BY t.updated_at DESC,t.rowid DESC LIMIT 1`, id, id, id, id).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return m, err
	}
	switch state {
	case "starting", "started":
		m.Processing = "processing"
	case "completed":
		m.Processing = "processed"
	case "failed", "interrupted", "unknown":
		m.Processing = state
	}
	var logical string
	err = q.QueryRowContext(ctx, `SELECT l.status FROM logical_turns l JOIN logical_turn_executions e USING(logical_turn_id) JOIN main_turns t ON t.request_id=e.request_id WHERE l.resumed_by IS NULL AND `+messagingTurnPredicate+` ORDER BY CASE WHEN l.status IN ('completed','stopped') THEN 1 ELSE 0 END,l.updated_at DESC,l.rowid DESC LIMIT 1`, id, id, id, id).Scan(&logical)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return m, err
	}
	switch logical {
	case "processing":
		m.Processing = "processing"
	case "recovering", "pending_repair":
		m.Processing = "recovering"
	case "needs_review":
		m.Processing = "needs_review"
	case "awaiting_clarification":
		m.Processing = "waiting_user"
	case "unknown":
		m.Processing = "unknown"
	case "stopped":
		m.Processing = "interrupted"
	case "completed":
		m.Processing = "processed"
	}
	if m.Direction != "input" {
		m.Processing = "published"
	} else if len(m.IssueIDs) > 0 && m.Processing != "failed" && m.Processing != "interrupted" && m.Processing != "unknown" && m.Processing != "needs_review" {
		var waitingOwner, waitingExecution, waitingAssignment bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_work_items w JOIN task_work_item_owners o USING(work_item_id) JOIN decisions d USING(task_id) WHERE w.message_id=? AND w.active=1 AND d.status='open'),EXISTS(SELECT 1 FROM message_work_items w JOIN task_work_item_owners o USING(work_item_id) JOIN tasks t USING(task_id) WHERE w.message_id=? AND w.active=1 AND t.kind='execution' AND t.lifecycle IN ('queued','running','waiting','awaiting_acceptance','changes_required')),EXISTS(SELECT 1 FROM message_work_items w JOIN issue_controls i ON i.issue_id=w.work_item_id WHERE w.message_id=? AND w.active=1 AND (i.assignee_kind<>'agent' OR i.assignment_state<>'active'))`, id, id, id).Scan(&waitingOwner, &waitingExecution, &waitingAssignment); err != nil {
			return m, err
		}
		switch {
		case waitingOwner:
			m.Processing = "waiting_user"
		case waitingAssignment:
			m.Processing = "waiting_assignment"
		case waitingExecution && m.Processing == "processed":
			m.Processing = "waiting_execution"
		}
	}
	return m, nil
}
func (a *App) MessagingReceipts(ctx context.Context, id string) (protocol.MessagingReceipts, error) {
	var r protocol.MessagingReceipts
	err := a.store.Read(ctx, func(tx *store.ReadTx) error { var err error; r, err = messagingReceipts(ctx, tx.Q(), id); return err })
	return r, err
}
func messagingReceipts(ctx context.Context, q store.Querier, id string) (protocol.MessagingReceipts, error) {
	r := protocol.MessagingReceipts{Reads: []protocol.MessagingRead{}, Deliveries: []protocol.MessagingDelivery{}, UserReadCapability: "explicit_web_only"}
	if _, err := messagingMessage(ctx, q, id); err != nil {
		return r, err
	}
	rows, err := q.QueryContext(ctx, `SELECT message_id,content_revision,reader,endpoint,evidence,execution_id,read_at FROM messaging_reads WHERE message_id=? ORDER BY read_at,reader,endpoint`, id)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var v protocol.MessagingRead
		if err = rows.Scan(&v.MessageID, &v.ContentRevision, &v.Reader, &v.Endpoint, &v.Evidence, &v.ExecutionID, &v.ReadAt); err != nil {
			rows.Close()
			return r, err
		}
		r.Reads = append(r.Reads, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, err
	}
	rows, err = q.QueryContext(ctx, `SELECT d.delivery_id,d.message_id,d.channel_id,d.part,d.kind,CASE WHEN o.status='sending' THEN 'sending' ELSE d.state END,d.external_id,d.reason,COALESCE(o.attempts,0),d.updated_at FROM messaging_deliveries d LEFT JOIN outbox o ON o.outbox_id=d.delivery_id WHERE d.message_id=? ORDER BY d.channel_id,d.part`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var d protocol.MessagingDelivery
		if err = rows.Scan(&d.ID, &d.MessageID, &d.ChannelID, &d.Part, &d.Kind, &d.State, &d.ExternalID, &d.Reason, &d.Attempts, &d.UpdatedAt); err != nil {
			return r, err
		}
		r.Deliveries = append(r.Deliveries, d)
	}
	return r, rows.Err()
}
func (a *App) MessagingTrace(ctx context.Context, id string) (protocol.MessagingTrace, error) {
	t := protocol.MessagingTrace{Replies: []string{}, Turns: []protocol.MessagingTurn{}, Tasks: []protocol.MessagingTask{}}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		t.Message, err = messagingMessage(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		t.Receipts, err = messagingReceipts(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		t.Replies, err = store.FollowupIDs(ctx, tx.Q(), `SELECT message_id FROM messaging_messages WHERE reply_to=? ORDER BY rowid`, id)
		if err != nil {
			return err
		}
		if t.Replies == nil {
			t.Replies = []string{}
		}
		rows, err := tx.Q().QueryContext(ctx, `SELECT DISTINCT t.request_id,t.thread_id,t.turn_id,t.status FROM main_turns t WHERE `+messagingTurnPredicate+` OR t.request_id=? ORDER BY t.updated_at`, id, id, id, id, t.Message.ExecutionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v protocol.MessagingTurn
			var thread, turn sql.NullString
			if err = rows.Scan(&v.ExecutionID, &thread, &turn, &v.Status); err != nil {
				return err
			}
			v.ThreadID = thread.String
			v.TurnID = turn.String
			t.Turns = append(t.Turns, v)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		rows.Close()
		work, err := tx.Q().QueryContext(ctx, `SELECT DISTINCT t.task_id,COALESCE((SELECT r.run_id FROM runs r WHERE r.task_id=t.task_id ORDER BY r.rowid DESC LIMIT 1),''),t.lifecycle,t.control_mode FROM tasks t WHERE t.kind='execution' AND (EXISTS(SELECT 1 FROM conversation_bindings b WHERE b.message_id=? AND b.object_type='task' AND b.object_id=t.task_id) OR EXISTS(SELECT 1 FROM task_work_item_owners o JOIN message_work_items m USING(work_item_id) WHERE o.task_id=t.task_id AND m.message_id=? AND m.active=1)) ORDER BY t.task_id LIMIT 100`, id, id)
		if err != nil {
			return err
		}
		defer work.Close()
		for work.Next() {
			var task protocol.MessagingTask
			if err = work.Scan(&task.TaskID, &task.RunID, &task.Lifecycle, &task.ControlMode); err != nil {
				return err
			}
			t.Tasks = append(t.Tasks, task)
		}
		return work.Err()
	})
	return t, err
}

// MessagingRecentMessages pages backwards without changing the CLI's forward cursor.
func (a *App) MessagingRecentMessages(ctx context.Context, before int64) (protocol.MessagingPage, error) {
	page := protocol.MessagingPage{Items: []protocol.MessagingMessage{}}
	if before == 0 {
		before = 1<<63 - 1
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT c.message_id FROM messaging_messages m JOIN conversation_messages c USING(message_id) WHERE c.seq<? ORDER BY c.seq DESC LIMIT 101`, before)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		page.More = len(ids) > 100
		if page.More {
			ids = ids[:100]
		}
		for n := len(ids) - 1; n >= 0; n-- {
			m, e := messagingMessage(ctx, tx.Q(), ids[n])
			if e != nil {
				return e
			}
			page.Items = append(page.Items, m)
		}
		if len(page.Items) > 0 {
			page.Next = page.Items[0].Sequence
		}
		return nil
	})
	return page, err
}
