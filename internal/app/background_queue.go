package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// queueBackgroundSource retains every notice and merges only unclaimed input.
func queueBackgroundSource(ctx context.Context, x *Exec, tx *store.Tx, message, event, kind string, data json.RawMessage) error {
	groups, err := store.FollowupIDs(ctx, tx.Q(), `SELECT work_item_id FROM message_work_items WHERE message_id=? AND active=1 UNION SELECT l.work_item_id FROM work_item_task_links l JOIN conversation_bindings b ON b.object_id=l.task_id AND b.object_type='task' WHERE b.message_id=? AND NOT EXISTS(SELECT 1 FROM message_work_items a WHERE a.message_id=b.message_id AND a.work_item_id=l.work_item_id AND a.active=0 AND a.owner_locked=1) ORDER BY 1`, message, message)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		var object string
		err := tx.Q().QueryRowContext(ctx, `SELECT object_type||':'||object_id FROM conversation_bindings WHERE message_id=? AND object_type IN ('task','decision','work_item_choice') ORDER BY object_type,object_id LIMIT 1`, message).Scan(&object)
		if errors.Is(err, sql.ErrNoRows) {
			object = "message:" + message
		} else if err != nil {
			return err
		}
		return appendBackgroundSource(ctx, x, tx, object, "", message, event, kind, data)
	}
	for _, item := range groups {
		if err := appendBackgroundSource(ctx, x, tx, "work_item:"+item, item, message, event, kind, data); err != nil {
			return err
		}
	}
	return nil
}
func appendBackgroundSource(ctx context.Context, x *Exec, tx *store.Tx, group, item, message, event, kind string, data json.RawMessage) error {
	now := x.Now.UnixMilli()
	conversation := "main"
	if x.BackgroundAgentsEnabled && item != "" {
		var baseline int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT baseline_revision FROM work_item_contexts WHERE work_item_id=?`, item).Scan(&baseline); err != nil {
			return err
		}
		if baseline > 0 {
			var err error
			conversation, err = ensureAgentConversation(ctx, x, tx, item)
			if err != nil {
				return err
			}
		}
	}

	var batch, canonical string
	err := tx.Q().QueryRowContext(ctx, `SELECT batch_id,message_id FROM background_batches WHERE group_key=? AND status='pending'`, group).Scan(&batch, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		batch = x.IDs.New(protocol.Prefix("batch"))
		canonical = x.IDs.New(protocol.PrefixMessage)
		body := "后台事项等待主 Agent 核对"
		if item != "" {
			body += "：" + item
		}
		seq, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: conversation, MessageID: canonical, Author: "system", Body: body, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: canonical, NoticeType: "background_batch"}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO background_batches(batch_id,group_key,work_item_id,message_id,status,created_at) VALUES(?,?,?,?,'pending',?)`, batch, group, item, canonical, now); err != nil {
			return err
		}
		if item != "" {
			if err := tx.BindMessage(ctx, canonical, "work_item", item, "coordination"); err != nil {
				return err
			}
		}
		if err := tx.BindMessage(ctx, canonical, "background_batch", batch, "coordination"); err != nil {
			return err
		}
		eid := x.IDs.New(protocol.PrefixEvent)
		if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: eid, Source: "app", ObjectType: "message", ObjectID: canonical, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","background_batch":true}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: now}); err != nil {
			return err
		}
		if err := queueSingleEventTurn(ctx, x, tx, canonical, eid, "background.batch", nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var occurrence protocol.FollowupOccurrence
	if kind == "followup.triggered" {
		if err := json.Unmarshal(data, &occurrence); err != nil {
			return fmt.Errorf("app: followup source: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `INSERT OR IGNORE INTO background_sources(batch_id,message_id,event_id,kind,rule_id,occurrence_id,data_json) VALUES(?,?,?,?,?,?,?)`, batch, message, event, kind, occurrence.RuleID, occurrence.OccurrenceID, string(data))
	if err != nil {
		return err
	}
	return tx.BindMessage(ctx, message, "background_batch", batch, "source")
}

// freezeBackgroundBatch is called in the delivery-claim transaction.
func freezeBackgroundBatch(ctx context.Context, tx *store.Tx, payload string, now int64) error {
	var p HandleMessage
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE background_batches SET status='frozen',frozen_at=? WHERE message_id=? AND status='pending'`, now, p.MessageID)
	return err
}
func (a *App) BackgroundPrompt(ctx context.Context, message string) (string, error) {
	var batch protocol.BackgroundBatch
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		batch, err = store.BackgroundByMessage(ctx, tx.Q(), message)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("\n[Background batch %s; work item %s; %d retained sources.]\nCall background_sources until more=false before acting, so all owner answers and source constraints are read. Read the current work item and followup revisions; frozen input is history, not continuing authority. The main agent remains the sole coordinator. If there is no new result and no unresolved completion review in this batch, use background_checkin to record waiting silently and finish without an ordinary chat reply. Check-in does not answer decisions, hide choice cards, resolve commitments, close work items or expand authorization. If a result or owner decision is needed, present it normally; background text is published after the turn settles.\n", batch.BatchID, batch.WorkItemID, batch.SourceCount), nil
}
