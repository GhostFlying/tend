package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (a *App) MessagingChannels(ctx context.Context) ([]protocol.MessagingChannel, error) {
	result := []protocol.MessagingChannel{}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT c.channel_id,c.status,c.revision,c.mark_read_mode,c.health,c.last_observed_at,CASE WHEN c.channel_id='feishu' THEN (SELECT count(*) FROM feishu_inputs i WHERE i.message_id='' AND i.state IN ('pending','accepted','unknown')) ELSE (SELECT count(*) FROM telegram_inbox i WHERE i.bot_id=c.bot_id AND i.state='pending') END FROM messaging_channels c ORDER BY channel_id`)
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
			if c.ID == "feishu" {
				c.Files = false
			}
			c.UserReadReceipts = c.ID == "web"
			result = append(result, c)
		}
		return rows.Err()
	})
	return result, err
}

// MessagingMessages reads communication using the forward message cursor.
func (a *App) MessagingMessages(ctx context.Context, after int64, conversation, issue, channel string) (protocol.MessagingPage, error) {
	return a.messagingPage(ctx, after, false, `WHERE c.seq>? AND (?='' OR c.conversation_id=?) AND (?='' OR m.channel_id=?) AND (?='' OR EXISTS(SELECT 1 FROM messaging_issue_links w WHERE w.message_id=c.message_id AND w.work_item_id=?)) ORDER BY c.seq LIMIT 101`, after, conversation, conversation, channel, channel, issue, issue)
}

// MessagingRecentMessages pages backwards, returning messages in display order.
func (a *App) MessagingRecentMessages(ctx context.Context, before int64) (protocol.MessagingPage, error) {
	if before == 0 {
		before = 1<<63 - 1
	}
	return a.messagingPage(ctx, 0, true, `WHERE c.seq<? ORDER BY c.seq DESC LIMIT 101`, before)
}

func (a *App) messagingPage(ctx context.Context, emptyCursor int64, backwards bool, filter string, args ...any) (protocol.MessagingPage, error) {
	page := protocol.MessagingPage{Items: []protocol.MessagingMessage{}, Next: emptyCursor}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		items, err := scanMessagingMessages(ctx, tx.Q(), filter, args...)
		if err != nil {
			return err
		}
		page.More = len(items) > 100
		if page.More {
			items = items[:100]
		}
		if backwards {
			slices.Reverse(items)
		}
		if err := hydrateMessagingMessages(ctx, tx.Q(), items); err != nil {
			return err
		}
		page.Items = items
		if len(items) > 0 {
			page.Next = items[len(items)-1].Sequence
			if backwards {
				page.Next = items[0].Sequence
			}
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

func messagingMessage(ctx context.Context, q store.Querier, id string) (protocol.MessagingMessage, error) {
	items, err := scanMessagingMessages(ctx, q, `WHERE m.message_id=?`, id)
	if err != nil {
		return protocol.MessagingMessage{}, err
	}
	if len(items) == 0 {
		return protocol.MessagingMessage{}, protocol.Errorf(protocol.CodeNotFound, "communication message not found")
	}
	err = hydrateMessagingMessages(ctx, q, items)
	return items[0], err
}

func scanMessagingMessages(ctx context.Context, q store.Querier, filter string, args ...any) ([]protocol.MessagingMessage, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.message_id,c.seq,c.conversation_id,c.author,c.body,d.content_revision,m.direction,m.channel_id,m.reply_to,m.correction_of,m.purpose,m.reason,m.execution_id,c.created_at,
		COALESCE((SELECT max(read_at) FROM messaging_reads WHERE message_id=m.message_id AND content_revision=d.content_revision AND reader LIKE 'agent:%'),0),
		COALESCE((SELECT CASE WHEN o.status='sending' THEN 'sending' ELSE v.state END FROM messaging_deliveries v LEFT JOIN outbox o ON o.outbox_id=v.delivery_id WHERE v.message_id=m.message_id AND v.kind IN ('text','file') ORDER BY CASE v.state WHEN 'unknown' THEN 0 WHEN 'failed' THEN 1 WHEN 'queued' THEN 2 WHEN 'unavailable' THEN 3 ELSE 4 END LIMIT 1),'')
		FROM messaging_messages m JOIN conversation_messages c USING(message_id) JOIN message_dispositions d USING(message_id) `+filter, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []protocol.MessagingMessage{}
	for rows.Next() {
		m := protocol.MessagingMessage{Attachments: []protocol.ConversationAttachment{}, IssueIDs: []string{}, SourceMessageIDs: []string{}, Processing: "published"}
		if err := rows.Scan(&m.MessageID, &m.Sequence, &m.ConversationID, &m.Author, &m.Body, &m.ContentRevision, &m.Direction, &m.ChannelID, &m.ReplyTo, &m.CorrectionOf, &m.Purpose, &m.Reason, &m.ExecutionID, &m.CreatedAt, &m.AgentReadAt, &m.DeliveryState); err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}

func hydrateMessagingMessages(ctx context.Context, q store.Querier, items []protocol.MessagingMessage) error {
	if len(items) == 0 {
		return nil
	}
	index := make(map[string]*protocol.MessagingMessage, len(items))
	ids := make([]string, 0, len(items))
	var inputs []string
	bodies := make([]string, len(items))
	for n := range items {
		m := &items[n]
		ids = append(ids, m.MessageID)
		index[m.MessageID] = m
		if m.Direction == "input" {
			inputs = append(inputs, m.MessageID)
		}
		if m.Author == "main_agent" {
			bodies[n] = m.Body
		}
	}
	if err := store.ResolveMessageBodies(ctx, q, bodies); err != nil {
		return err
	}
	for n := range items {
		if items[n].Author == "main_agent" {
			items[n].Body = bodies[n]
		}
	}

	keys, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if err := store.VisitConversationAttachments(ctx, q, string(keys), func(id string, a protocol.ConversationAttachment) {
		index[id].Attachments = append(index[id].Attachments, a)
	}); err != nil {
		return err
	}
	if err := hydrateMessagingLinks(ctx, q, string(keys), index); err != nil {
		return err
	}
	if err := hydrateMessagingInteractions(ctx, q, string(keys), index); err != nil {
		return err
	}
	if len(inputs) == 0 {
		return nil
	}
	inputKeys, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	return hydrateMessagingProcessing(ctx, q, string(inputKeys), index)
}

func hydrateMessagingLinks(ctx context.Context, q store.Querier, keys string, index map[string]*protocol.MessagingMessage) error {
	rows, err := q.QueryContext(ctx, `WITH page(id) AS (SELECT value FROM json_each(?))
		SELECT message_id,work_item_id,0 FROM messaging_issue_links WHERE message_id IN page
		UNION ALL SELECT message_id,source_message_id,1 FROM messaging_sources WHERE message_id IN page
		ORDER BY 1,2`, keys)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, value string
		var source bool
		if err := rows.Scan(&id, &value, &source); err != nil {
			return err
		}
		m := index[id]
		if source {
			m.SourceMessageIDs = append(m.SourceMessageIDs, value)
		} else {
			m.IssueIDs = append(m.IssueIDs, value)
		}
	}
	return rows.Err()
}

const messagingTurnPredicate = `(t.message_id=? OR t.request_id IN (SELECT e.request_id FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE m.message_id=?) OR EXISTS(SELECT 1 FROM main_steers s WHERE s.message_id=? AND s.thread_id=t.thread_id AND s.turn_id=t.turn_id) OR EXISTS(SELECT 1 FROM background_batches b JOIN background_sources s USING(batch_id) WHERE b.message_id=t.message_id AND s.message_id=? AND s.cancelled_at=0))`

func hydrateMessagingProcessing(ctx context.Context, q store.Querier, keys string, index map[string]*protocol.MessagingMessage) error {
	// Start from each input's indexed links so unrelated Main history is not
	// scanned once for every input. UNION deduplicates overlapping routes.
	rows, err := q.QueryContext(ctx, `WITH inputs(id) AS (SELECT value FROM json_each(?)),
		turns(id, request_id) AS MATERIALIZED (
			SELECT i.id,t.request_id FROM inputs i CROSS JOIN main_turns t ON t.message_id=i.id
			UNION SELECT i.id,e.request_id FROM inputs i CROSS JOIN logical_turn_messages m ON m.message_id=i.id JOIN logical_turn_executions e USING(logical_turn_id)
			UNION SELECT i.id,t.request_id FROM inputs i CROSS JOIN main_steers s ON s.message_id=i.id JOIN main_turns t ON t.thread_id=s.thread_id AND t.turn_id=s.turn_id
			UNION SELECT i.id,t.request_id FROM inputs i CROSS JOIN background_sources s ON s.message_id=i.id JOIN background_batches b USING(batch_id) JOIN main_turns t ON t.message_id=b.message_id WHERE s.cancelled_at=0
		)
		SELECT i.id,
			COALESCE((SELECT json_extract(body_json,'$.state') FROM events WHERE object_type='message' AND object_id=i.id AND kind='conversation.message_state' ORDER BY event_seq DESC LIMIT 1),''),
			COALESCE((SELECT t.status FROM turns v JOIN main_turns t USING(request_id) WHERE v.id=i.id ORDER BY t.updated_at DESC,t.rowid DESC LIMIT 1),''),
			COALESCE((SELECT l.status FROM turns v JOIN main_turns t USING(request_id) JOIN logical_turn_executions e USING(request_id) JOIN logical_turns l USING(logical_turn_id) WHERE v.id=i.id AND l.resumed_by IS NULL ORDER BY CASE WHEN l.status IN ('completed','stopped') THEN 1 ELSE 0 END,l.updated_at DESC,l.rowid DESC LIMIT 1),''),
			EXISTS(SELECT 1 FROM message_work_items w JOIN task_work_item_owners o USING(work_item_id) JOIN decisions d USING(task_id) WHERE w.message_id=i.id AND w.active=1 AND d.status='open'),
			EXISTS(SELECT 1 FROM message_work_items w JOIN task_work_item_owners o USING(work_item_id) JOIN tasks t USING(task_id) WHERE w.message_id=i.id AND w.active=1 AND t.kind='execution' AND t.lifecycle IN ('queued','running','waiting','awaiting_acceptance','changes_required')),
			EXISTS(SELECT 1 FROM message_work_items w JOIN issue_controls c ON c.issue_id=w.work_item_id WHERE w.message_id=i.id AND w.active=1 AND (c.assignee_kind<>'agent' OR c.assignment_state<>'active'))
		FROM inputs i`, keys)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, event, turn, logical string
		var waitingOwner, waitingExecution, waitingAssignment bool
		if err := rows.Scan(&id, &event, &turn, &logical, &waitingOwner, &waitingExecution, &waitingAssignment); err != nil {
			return err
		}
		m := index[id]
		m.Processing = messagingProcessingState(event, turn, logical)
		if len(m.IssueIDs) == 0 {
			continue
		}
		switch m.Processing {
		case "failed", "interrupted", "unknown", "needs_review":
			continue
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
	return rows.Err()
}

func messagingProcessingState(event, turn, logical string) string {
	state := "pending"
	switch event {
	case "failed", "interrupted", "unknown", "processing":
		state = event
	case "replied":
		state = "processed"
	}
	switch turn {
	case "starting", "started":
		state = "processing"
	case "completed":
		state = "processed"
	case "failed", "interrupted", "unknown":
		state = turn
	}
	switch logical {
	case "processing", "needs_review", "unknown":
		return logical
	case "recovering", "pending_repair":
		return "recovering"
	case "awaiting_clarification":
		return "waiting_user"
	case "stopped":
		return "interrupted"
	case "completed":
		return "processed"
	}
	return state
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
	rows, err = q.QueryContext(ctx, `SELECT d.delivery_id,d.message_id,d.channel_id,d.part,d.kind,CASE WHEN o.status='sending' THEN 'sending' ELSE d.state END,d.external_id,d.reason,COALESCE(o.attempts,0),d.updated_at,COALESCE(f.external_message_id,'') FROM messaging_deliveries d LEFT JOIN outbox o ON o.outbox_id=d.delivery_id LEFT JOIN feishu_deliveries f ON f.delivery_id=d.delivery_id WHERE d.message_id=? ORDER BY d.channel_id,d.part`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var d protocol.MessagingDelivery
		if err = rows.Scan(&d.ID, &d.MessageID, &d.ChannelID, &d.Part, &d.Kind, &d.State, &d.ExternalID, &d.Reason, &d.Attempts, &d.UpdatedAt, &d.ExternalMessageReference); err != nil {
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
