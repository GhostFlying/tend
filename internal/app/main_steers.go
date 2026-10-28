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

// SetMainThreadWithToolset records tools installed by a confirmed fresh native
// thread, including legacy mode which does not maintain handoff bindings.
func (a *App) SetMainThreadWithToolset(ctx context.Context, instance, threadID, hash string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.PutMainThread(ctx, instance, threadID, a.now().UnixMilli()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO main_thread_toolsets(instance,thread_id,toolset_hash) VALUES(?,?,?) ON CONFLICT(instance,thread_id) DO NOTHING`, instance, threadID, hash)
		return err
	})
}

func (a *App) MainThreadToolset(ctx context.Context, instance, threadID string) (hash string, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT toolset_hash FROM main_thread_toolsets WHERE instance=? AND thread_id=?`, instance, threadID).Scan(&hash)
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

// ClaimMainSteer is used only by the active serial main-thread delivery. It
// atomically fences withdrawal, records sending intent, and keeps queue order.
func (a *App) ClaimMainSteer(ctx context.Context, activeRequestID, threadID, turnID string) (Delivery, bool, error) {
	var d Delivery
	var ok bool
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		active, err := store.GetMainTurn(ctx, tx.Q(), activeRequestID)
		if err != nil {
			return err
		}
		if active.Status != store.TurnStarted || active.ThreadID != threadID || active.TurnID != turnID {
			return nil
		}
		var admitted bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turns l ON l.logical_turn_id=e.logical_turn_id WHERE e.request_id=? AND e.repair=0 AND l.status='processing') AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending WHERE request_id=?)`, activeRequestID, activeRequestID).Scan(&admitted); err != nil {
			return err
		}
		if !admitted {
			return nil
		}
		paused, err := upgradePausesMain(ctx, tx.Q())
		if err != nil || paused {
			return err
		}
		m, err := store.HeadSteer(ctx, tx.Q(), activeRequestID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if m.Operation != OpHandleMessage || m.Status != store.OutboxPending || m.NextAttemptAt > a.now().UnixMilli() {
			return nil
		}
		var p HandleMessage
		if err := json.Unmarshal([]byte(m.PayloadJSON), &p); err != nil {
			return err
		}
		msg, err := store.GetMessage(ctx, tx.Q(), p.MessageID)
		if err != nil {
			return err
		}
		if msg.Author != "owner" {
			return nil
		}
		now := a.now().UnixMilli()
		if err := tx.TransitionOutbox(ctx, store.OutboxTransition{OutboxID: m.OutboxID, From: store.OutboxPending, FromAttempts: m.Attempts, To: store.OutboxSending, Attempts: m.Attempts + 1, NextAttemptAt: m.NextAttemptAt, Now: now}); err != nil {
			return err
		}
		if err := tx.PutMainSteer(ctx, store.MainSteer{RequestID: m.RequestID, MessageID: p.MessageID, ThreadID: threadID, TurnID: turnID, Status: "sending"}, now); err != nil {
			return err
		}
		if err := a.queueChanged(ctx, tx, m.RequestID, now); err != nil {
			return err
		}
		m.Attempts++
		d, ok = deliveryOf(m), true
		return nil
	})
	if err == nil && ok {
		a.notifyChange()
	}
	return d, ok, err
}

// NoteMainSteerWaiting leaves unsupported input withdrawable and queued.
func (a *App) NoteMainSteerWaiting(ctx context.Context, activeRequestID, threadID, turnID, reason string) error {
	changed := false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		m, err := store.HeadSteer(ctx, tx.Q(), activeRequestID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if m.Operation != OpHandleMessage || m.Status != store.OutboxPending {
			return nil
		}
		old, err := store.GetMainSteer(ctx, tx.Q(), m.RequestID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil && old.Status == "waiting" && old.Reason == reason {
			return nil
		}
		var p HandleMessage
		if err := json.Unmarshal([]byte(m.PayloadJSON), &p); err != nil {
			return err
		}
		changed = true
		now := a.now().UnixMilli()
		if err := tx.PutMainSteer(ctx, store.MainSteer{RequestID: m.RequestID, MessageID: p.MessageID, ThreadID: threadID, TurnID: turnID, Status: "waiting", Reason: reason}, now); err != nil {
			return err
		}
		return a.insertMessageState(ctx, tx, p.MessageID, protocol.MessageQueued, now)
	})
	if err == nil && changed {
		a.notifyChange()
	}
	return err
}

func (a *App) MainSteer(ctx context.Context, requestID string) (store.MainSteer, bool, error) {
	var m store.MainSteer
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		m, err = store.GetMainSteer(ctx, tx.Q(), requestID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return m, false, nil
	}
	return m, err == nil, err
}

func (a *App) MainSteerByInput(ctx context.Context, threadID, turnID, text string) (store.MainSteer, bool, error) {
	var id string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT request_id FROM main_steers WHERE thread_id=? AND turn_id=? AND input_text=? AND input_text<>''`, threadID, turnID, text).Scan(&id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return store.MainSteer{}, false, nil
	}
	if err != nil {
		return store.MainSteer{}, false, err
	}
	return a.MainSteer(ctx, id)
}

// UpdateMainSteer does not downgrade an already witnessed native user item when
// the RPC answer arrives later, or overwrite a terminal recovery decision.
func (a *App) UpdateMainSteer(ctx context.Context, m store.MainSteer) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		old, err := store.GetMainSteer(ctx, tx.Q(), m.RequestID)
		if err != nil {
			return err
		}
		if old.Status != "sending" && old.Status != "accepted" && old.Status != "unknown" && old.Status != "waiting" {
			return nil
		}
		if old.ThreadID != m.ThreadID || old.TurnID != m.TurnID {
			return protocol.Errorf(protocol.CodeInvalidTransition, "steer turn mismatch")
		}
		now := a.now().UnixMilli()
		if err := tx.PutMainSteer(ctx, m, now); err != nil {
			return err
		}
		if state := steerState(m.Status); state != "" && (old.Status != m.Status || old.Reason != m.Reason) {
			return a.insertMessageState(ctx, tx, m.MessageID, state, now)
		}
		return nil
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

func steerState(status string) protocol.MessageState {
	switch status {
	case "accepted":
		return protocol.MessageAccepted
	case "consumed":
		return protocol.MessageProcessing
	case "completed":
		return protocol.MessageReplied
	case "interrupted":
		return protocol.MessageInterrupted
	case "failed":
		return protocol.MessageFailed
	case "unknown":
		return protocol.MessageUnknown
	case "waiting":
		return protocol.MessageQueued
	}
	return ""
}

// ConsumeMainSteer matches exact persisted input, thread and turn; a marker in
// a different conversation or a repeated notification cannot admit a message.
func (a *App) ConsumeMainSteer(ctx context.Context, threadID, turnID, text, itemID string) (store.MainSteer, bool, error) {
	var m store.MainSteer
	var ok bool
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		var requestID string
		err := tx.Q().QueryRowContext(ctx, `SELECT request_id FROM main_steers WHERE thread_id=? AND turn_id=? AND input_text=? AND input_text<>'' AND status IN ('sending','accepted','unknown')`, threadID, turnID, text).Scan(&requestID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		m, err = store.GetMainSteer(ctx, tx.Q(), requestID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO logical_turn_messages(logical_turn_id,message_id) SELECT e.logical_turn_id,? FROM logical_turn_executions e JOIN main_turns t ON t.request_id=e.request_id WHERE t.thread_id=? AND t.turn_id=?`, m.MessageID, threadID, turnID); err != nil {
			return err
		}
		m.Status, m.NativeItemID, m.Reason = "consumed", itemID, ""
		now := a.now().UnixMilli()
		if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='pending_repair',reason='A late owner supplement requires review.',updated_at=? WHERE status='completed' AND logical_turn_id IN (SELECT e.logical_turn_id FROM logical_turn_executions e JOIN main_turns t ON t.request_id=e.request_id WHERE t.thread_id=? AND t.turn_id=?)`, now, threadID, turnID); err != nil {
			return err
		}
		var stopped bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_messages lm JOIN logical_turns l ON l.logical_turn_id=lm.logical_turn_id WHERE lm.message_id=? AND l.status='stopped')`, m.MessageID).Scan(&stopped); err != nil {
			return err
		}
		if stopped {
			m.Status, m.Reason = "interrupted", "owner stopped the logical responsibility before its late consumption receipt"
		}
		if err := tx.PutMainSteer(ctx, m, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='delivered',updated_at=? WHERE request_id=? AND status='unknown'`, now, requestID); err != nil {
			return err
		}
		if err := a.insertMessageState(ctx, tx, m.MessageID, steerState(m.Status), now); err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err == nil && ok {
		a.notifyChange()
	}
	return m, ok, err
}

// ConsumedMainSteers identifies witnessed user items that retained history must
// include before it can settle the turn and reconstruct reply attribution.
func (a *App) ConsumedMainSteers(ctx context.Context, threadID, turnID string) ([]store.MainSteer, error) {
	var result []store.MainSteer
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT request_id FROM main_steers WHERE thread_id=? AND turn_id=? AND native_item_id<>''`, threadID, turnID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			m, err := store.GetMainSteer(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			result = append(result, m)
		}
		return nil
	})
	return result, err
}

func (a *App) SettleMainSteers(ctx context.Context, threadID, turnID, status string) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		return a.settleMainSteers(ctx, tx, threadID, turnID, status, a.now().UnixMilli())
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

func (a *App) settleMainSteers(ctx context.Context, tx *store.Tx, threadID, turnID, status string, now int64) error {
	var pending bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_turn_receipts_pending p JOIN main_turns t ON t.request_id=p.request_id WHERE t.thread_id=? AND t.turn_id=?)`, threadID, turnID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return a.markMainSteersUncertain(ctx, tx, threadID, turnID, now)
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT request_id FROM main_steers WHERE thread_id=? AND (?='' OR turn_id=?) AND status IN ('sending','accepted','consumed')`, threadID, turnID, turnID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
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
	for _, id := range ids {
		m, err := store.GetMainSteer(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		settledStatus := status
		if m.Status == "consumed" && status == store.TurnCompleted {
			var incomplete, stopped bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_messages lm JOIN logical_turns l ON l.logical_turn_id=lm.logical_turn_id WHERE lm.message_id=? AND l.status NOT IN ('completed','stopped')),EXISTS(SELECT 1 FROM logical_turn_messages lm JOIN logical_turns l ON l.logical_turn_id=lm.logical_turn_id WHERE lm.message_id=? AND l.status='stopped')`, m.MessageID, m.MessageID).Scan(&incomplete, &stopped); err != nil {
				return err
			}
			if stopped {
				settledStatus = store.TurnInterrupted
			}
			if incomplete {
				if m.Reason != "awaiting classification and fact review" {
					m.Reason = "awaiting classification and fact review"
					if err := tx.PutMainSteer(ctx, m, now); err != nil {
						return err
					}
					if err := a.insertMessageState(ctx, tx, m.MessageID, protocol.MessageProcessing, now); err != nil {
						return err
					}
				}
				continue
			}
		}
		if m.Status == "consumed" {
			m.Status = settledStatus
			m.Reason = ""
		} else {
			m.Status = "unknown"
			m.Reason = "native turn ended without a matching user-item consumption receipt; input will not be resent"
		}
		if err := tx.PutMainSteer(ctx, m, now); err != nil {
			return err
		}
		state := steerState(m.Status)
		if m.Status == "completed" {
			var replied bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_reply_sources s JOIN conversation_agent_messages r ON r.message_id=s.reply_id WHERE s.source_id=? AND r.thread_id=? AND r.turn_id=? AND r.status='completed')`, m.MessageID, m.ThreadID, m.TurnID).Scan(&replied); err != nil {
				return err
			}
			if !replied {
				state = protocol.MessageProcessed
			}
		}
		if err := a.insertMessageState(ctx, tx, m.MessageID, state, now); err != nil {
			return err
		}
	}
	return nil
}

// BindMainToolSource freezes the first callback's source across native replay.
func (a *App) BindMainToolSource(ctx context.Context, native, messageID, requestID string, item store.ToolUseRow) (string, string, error) {
	changed := false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO main_tool_sources(native_key,message_id,request_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, native, messageID, requestID); err != nil {
			return fmt.Errorf("app: bind native source: %w", err)
		}
		if err := tx.Q().QueryRowContext(ctx, `SELECT message_id,request_id FROM main_tool_sources WHERE native_key=?`, native).Scan(&messageID, &requestID); err != nil {
			return err
		}
		var id, previous, status string
		err := tx.Q().QueryRowContext(ctx, `SELECT u.message_id,COALESCE(m.reply_to,''),u.status FROM conversation_tool_uses u JOIN conversation_messages m ON m.message_id=u.message_id WHERE u.thread_id=? AND u.turn_id=? AND u.item_id=?`, item.ThreadID, item.TurnID, item.ItemID).Scan(&id, &previous, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if previous == messageID {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_messages SET reply_to=? WHERE message_id=?`, messageID, id); err != nil {
			return err
		}
		changed = true
		return a.insertToolUseEvent(ctx, tx, id, protocol.ToolStatus(status), a.now().UnixMilli())
	})
	if err == nil && changed {
		a.notifyChange()
	}
	return messageID, requestID, err
}

func (a *App) MainToolSource(ctx context.Context, native string) (string, bool, error) {
	var source string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT message_id FROM main_tool_sources WHERE native_key=?`, native).Scan(&source)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return source, err == nil, err
}

// NativeItemSources recovers an item's immutable reply binding for late native
// notifications, including replay after its active turn has been cleared.
func (a *App) NativeItemSources(ctx context.Context, threadID, turnID, itemID string) (replyTo string, sources []string, found bool, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		var id string
		err := tx.Q().QueryRowContext(ctx, `SELECT message_id FROM conversation_agent_messages WHERE thread_id=? AND turn_id=? AND item_id=? UNION ALL SELECT message_id FROM conversation_tool_uses WHERE thread_id=? AND turn_id=? AND item_id=? LIMIT 1`, threadID, turnID, itemID, threadID, turnID, itemID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.Q().QueryRowContext(ctx, `SELECT b.message_id FROM background_replies r JOIN background_batches b ON b.batch_id=r.batch_id WHERE r.thread_id=? AND r.turn_id=? AND r.item_id=? LIMIT 1`, threadID, turnID, itemID).Scan(&replyTo)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			found = err == nil
			return err
		}
		if err != nil {
			return err
		}
		row, err := store.GetConversationItem(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		replyTo, found = row.ReplyTo, true
		rows, err := tx.Q().QueryContext(ctx, `SELECT s.source_id FROM conversation_reply_sources s JOIN conversation_messages m ON m.message_id=s.source_id WHERE s.reply_id=? ORDER BY m.seq`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var source string
			if err := rows.Scan(&source); err != nil {
				return err
			}
			sources = append(sources, source)
		}
		return rows.Err()
	})
	return
}

// OwnerMessageDelivery selects only owner input with recorded admission. It
// exposes provenance, not additional tool permissions or future queued input.
func (a *App) OwnerMessageDelivery(ctx context.Context, messageID, threadID, turnID string, historical bool) (requestID string, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT request_id FROM main_turns WHERE message_id=m.message_id AND status IN ('started','completed','interrupted','failed') AND (? OR (thread_id=? AND turn_id=?)) ORDER BY updated_at DESC LIMIT 1),(SELECT request_id FROM main_steers WHERE message_id=m.message_id AND native_item_id<>'' AND (? OR (thread_id=? AND turn_id=?))),'') FROM conversation_messages m LEFT JOIN conversation_message_controls c ON c.message_id=m.message_id WHERE m.message_id=? AND m.author='owner' AND COALESCE(c.withdrawn_at,0)=0`, historical, threadID, turnID, historical, threadID, turnID, messageID).Scan(&requestID)
	})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && requestID == "") {
		err = protocol.Errorf(protocol.CodeInvalidRequest, "source must be an admitted owner message")
	}
	return
}

func (a *App) settleReviewedMainSteers(ctx context.Context, tx *store.Tx, request, status string, now int64) error {
	if status != store.TurnCompleted && status != store.TurnInterrupted && status != store.TurnFailed {
		return nil
	}
	var logical string
	err := tx.Q().QueryRowContext(ctx, `SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?`, request).Scan(&logical)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.settleMainSteerSources(ctx, tx, logical, now)
}

func (a *App) settleMainSteerSources(ctx context.Context, tx *store.Tx, logical string, now int64) error {
	rows, err := tx.Q().QueryContext(ctx, `WITH RECURSIVE responsibility(id) AS (
 SELECT ?
 UNION SELECT l.logical_turn_id FROM logical_turns l JOIN responsibility r ON l.resumed_by=r.id),
 reviewed(message_id) AS (
 SELECT DISTINCT m.message_id FROM logical_turn_messages m JOIN logical_turns l ON l.logical_turn_id=m.logical_turn_id
 WHERE l.status IN ('completed','stopped') AND l.logical_turn_id IN (SELECT id FROM responsibility)),
 native(thread_id,turn_id) AS (
 SELECT t.thread_id,t.turn_id FROM main_turns t JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE e.repair=0 AND e.logical_turn_id IN (SELECT id FROM responsibility)
 UNION SELECT s.thread_id,s.turn_id FROM main_steers s JOIN reviewed r ON r.message_id=s.message_id WHERE s.status='consumed')
 SELECT DISTINCT t.thread_id,t.turn_id,t.status FROM native n JOIN main_turns t ON t.thread_id=n.thread_id AND t.turn_id=n.turn_id
 JOIN logical_turn_executions e ON e.request_id=t.request_id JOIN logical_turns l ON l.logical_turn_id=e.logical_turn_id
 WHERE e.repair=0 AND l.status IN ('completed','stopped') AND t.status IN ('completed','interrupted','failed') AND t.turn_id IS NOT NULL AND t.turn_id<>''`, logical)
	if err != nil {
		return err
	}
	type nativeTurn struct{ thread, turn, status string }
	var turns []nativeTurn
	for rows.Next() {
		var turn nativeTurn
		if err := rows.Scan(&turn.thread, &turn.turn, &turn.status); err != nil {
			rows.Close()
			return err
		}
		turns = append(turns, turn)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, turn := range turns {
		if turn.status == store.TurnCompleted || turn.status == store.TurnInterrupted || turn.status == store.TurnFailed {
			if err := a.settleMainSteers(ctx, tx, turn.thread, turn.turn, turn.status, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// Losing a session preserves known consumption while fencing unknown native
// outcomes. Only matching terminal receipts may settle these steers later.
func (a *App) markMainSteersUncertain(ctx context.Context, tx *store.Tx, thread, turn string, now int64) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT request_id FROM main_steers WHERE thread_id=? AND turn_id=? AND status IN ('sending','accepted','consumed')`, thread, turn)
	if err != nil {
		return err
	}
	for _, id := range ids {
		m, err := store.GetMainSteer(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if m.Status != "consumed" {
			m.Status = "unknown"
		}
		m.Reason = "native execution history requires reconciliation; input will not be resent"
		if err := tx.PutMainSteer(ctx, m, now); err != nil {
			return err
		}
		if err := a.insertMessageState(ctx, tx, m.MessageID, protocol.MessageUnknown, now); err != nil {
			return err
		}
	}
	return nil
}

// Explicit stop closes metadata responsibility without certifying its facts.
// Known native outcomes can settle shared inputs; pending receipts stay unknown.
func (a *App) settleStoppedMainSources(ctx context.Context, tx *store.Tx, message string, now int64) error {
	ids, err := store.LogicalResponsibilities(ctx, tx.Q(), message)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var root, status string
		if err := tx.Q().QueryRowContext(ctx, `SELECT message_id,status FROM logical_turns WHERE logical_turn_id=?`, id).Scan(&root, &status); err != nil {
			return err
		}
		if status != "stopped" {
			continue
		}
		uncertain, err := logicalNativeUncertain(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if !uncertain {
			if err := a.insertMessageState(ctx, tx, root, protocol.MessageInterrupted, now); err != nil {
				return err
			}
		}
		if err := a.settleMainSteerSources(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

// resumeMainSteers reopens metadata-only stops, never interrupted native work.
func (a *App) resumeMainSteers(ctx context.Context, tx *store.Tx, logical string, now int64) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT s.request_id FROM main_steers s JOIN logical_turn_messages m ON m.message_id=s.message_id
 WHERE m.logical_turn_id=? AND s.status='interrupted' AND s.native_item_id<>''
 AND EXISTS(SELECT 1 FROM main_turns t JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE t.thread_id=s.thread_id AND t.turn_id=s.turn_id AND t.status='completed' AND e.repair=0 AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id))`, logical)
	if err != nil {
		return err
	}
	for _, id := range ids {
		steer, err := store.GetMainSteer(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		steer.Status, steer.Reason = "consumed", "awaiting classification and fact review"
		if err := tx.PutMainSteer(ctx, steer, now); err != nil {
			return err
		}
		if err := a.insertMessageState(ctx, tx, steer.MessageID, protocol.MessageProcessing, now); err != nil {
			return err
		}
	}
	return nil
}
