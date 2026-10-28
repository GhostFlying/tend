package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const OpRecoverAgent = "conversation.recover_agent"

// WithAgentRecoveryPolicy configures the hosted recovery budget. Zero attempts disables it.
func WithAgentRecoveryPolicy(p domain.AgentRecoveryPolicy) Option {
	return func(a *App) { a.agentRecoveryPolicy = p }
}

// AgentRecovery resolves a native execution's recovery chain.
func (a *App) AgentRecovery(ctx context.Context, request string) (store.AgentRecovery, bool, error) {
	var r store.AgentRecovery
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		r, err = store.RecoveryForRequest(ctx, tx.Q(), request)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return r, false, nil
	}
	return r, err == nil, err
}

// scheduleAgentRecovery is called inside native settlement's transaction.
// Delivered remains truthful: the original input already reached the harness.
func (a *App) scheduleAgentRecovery(ctx context.Context, tx *store.Tx, m store.MainTurn, now int64) (bool, error) {
	r, err := store.RecoveryForRequest(ctx, tx.Q(), m.RequestID)
	exists := err == nil
	if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	if err != nil {
		return false, err
	}
	if exists && r.ActiveRequestID != m.RequestID {
		return r.Status == "waiting" || r.Status == "starting" || r.Status == "running" || r.Status == "unknown", nil
	}
	if m.Status != store.TurnFailed && m.Status != store.TurnCompleted && m.Status != store.TurnInterrupted {
		return false, nil
	}
	retryable := m.Status == store.TurnFailed && m.Failure != nil && !m.Failure.WillRetry && m.Failure.Code == "rate_limit_exceeded"
	if exists && (r.Status == "cancelled" || r.Status == "exhausted" || r.Status == "blocked") {
		return false, nil
	}
	if !retryable {
		if exists {
			_, err = tx.Exec(ctx, `UPDATE agent_recoveries SET status=?,next_attempt_at=0,updated_at=? WHERE root_request_id=?`, m.Status, now, r.RootRequestID)
		}
		return false, err
	}
	if !exists && a.agentRecoveryPolicy.MaxAttempts <= 0 {
		return false, nil
	}
	var stopped bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_recovery_stops WHERE message_id=?) OR EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turns l ON l.logical_turn_id=e.logical_turn_id WHERE e.request_id=? AND (l.status='stopped' OR l.resumed_by IS NOT NULL)) OR EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=? AND withdrawn_at<>0) OR EXISTS(SELECT 1 FROM conversation_messages m JOIN agent_conversations c ON c.conversation_id=m.conversation_id JOIN work_items w ON w.work_item_id=c.work_item_id WHERE m.message_id=? AND w.status='closed')`, m.MessageID, m.RequestID, m.MessageID, m.MessageID).Scan(&stopped); err != nil {
		return false, err
	}
	if stopped {
		return false, nil
	}
	var repair bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT repair FROM logical_turn_executions WHERE request_id=?),0)`, m.RequestID).Scan(&repair); err != nil {
		return false, err
	}
	if repair {
		return false, nil
	}

	var notice bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_recoveries WHERE notice_message_id=?)`, m.MessageID).Scan(&notice); err != nil {
		return false, err
	}
	if notice {
		return false, nil
	}
	if !exists {
		conversation, err := store.MessageConversation(ctx, tx.Q(), m.MessageID)
		if err != nil {
			return false, err
		}
		r = store.AgentRecovery{RootRequestID: m.RequestID, ConversationID: conversation, MessageID: m.MessageID, ThreadID: m.ThreadID, ActiveRequestID: m.RequestID, LastTurnID: m.TurnID}
		r.Deadline = now + a.agentRecoveryPolicy.MaxElapsed.Milliseconds()
		_, err = tx.Exec(ctx, `INSERT INTO agent_recoveries(root_request_id,conversation_id,message_id,thread_id,status,deadline,active_request_id,last_turn_id,updated_at) VALUES(?,?,?,?,'waiting',?,?,?,?)`, r.RootRequestID, r.ConversationID, r.MessageID, r.ThreadID, r.Deadline, r.ActiveRequestID, r.LastTurnID, now)
		if err != nil {
			return false, err
		}
	}
	uncertain, err := uncertainRecoveryInput(ctx, tx.Q(), m.RequestID)
	if err != nil {
		return false, err
	}
	if uncertain {
		if _, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?) ON CONFLICT DO NOTHING`, m.RequestID); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='unknown',reason='An owner input receipt requires reconciliation.',updated_at=? WHERE logical_turn_id=(SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?) AND status NOT IN ('completed','stopped')`, now, m.RequestID); err != nil {
			return false, err
		}
		if r.Status == "unknown" && r.Reason == "An accepted owner input has no confirmed consumption receipt; reconcile before continuing." {
			return true, nil
		}
		return true, a.endAgentRecovery(ctx, tx, r, "unknown", "An accepted owner input has no confirmed consumption receipt; reconcile before continuing.", now)
	}
	// A newer unconsumed owner instruction supersedes an idle continuation.
	successor, err := newerRecoveryOwner(ctx, tx.Q(), m.MessageID, m.RequestID)
	if err != nil {
		return false, err
	}
	if successor != "" {
		// The current native execution is confirmed terminal. Make its retained
		// responsibility eligible for the queued instruction to adopt.
		if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status='waiting',last_turn_id=?,updated_at=? WHERE root_request_id=?`, m.TurnID, now, r.RootRequestID); err != nil {
			return false, err
		}
		if err := supersedeAgentRecoveries(ctx, tx, r.ConversationID, successor, now); err != nil {
			return false, err
		}
		return true, nil
	}
	attempt := r.Attempts + 1
	due := now + a.agentRecoveryPolicy.Delay(r.RootRequestID, attempt).Milliseconds()
	reason := "Native rate limit; waiting to continue the retained conversation."
	status := "waiting"
	if a.agentRecoveryPolicy.MaxAttempts <= 0 || attempt > a.agentRecoveryPolicy.MaxAttempts || due > r.Deadline {
		status = "exhausted"
		reason = "Automatic rate-limit recovery budget exhausted; attention required."
	}
	if status == "exhausted" {
		if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status=?,next_attempt_at=0,last_turn_id=?,reason=?,updated_at=? WHERE root_request_id=?`, status, m.TurnID, reason, now, r.RootRequestID); err != nil {
			return false, err
		}
		if err := a.recoveryEvent(ctx, tx, r, now, status, reason); err != nil {
			return false, err
		}
		return false, nil
	}
	request := a.ids.New(protocol.PrefixRequest)
	if _, err := tx.Exec(ctx, `INSERT INTO agent_recovery_attempts(request_id,root_request_id,source_request_id) VALUES(?,?,?)`, request, r.RootRequestID, m.RequestID); err != nil {
		return false, err
	}
	// A continuation belongs to the original logical responsibility, not a new owner message.
	if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) SELECT ?,logical_turn_id,0 FROM logical_turn_executions WHERE request_id=?`, request, m.RequestID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE logical_turn_executions SET settled=1 WHERE request_id=?`, m.RequestID); err != nil {
		return false, err
	}
	payload, _ := json.Marshal(HandleMessage{MessageID: m.MessageID})
	destination := MainThreadDestination
	if r.ConversationID != "main" {
		destination = "background_thread:" + r.ConversationID
	}
	oid := a.ids.New(protocol.PrefixOutbox)
	if err := tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: oid, RequestID: request, ParentRequestID: m.RequestID, Destination: destination, Operation: OpRecoverAgent, PayloadJSON: string(payload), CreatedAt: now}); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE outbox SET next_attempt_at=? WHERE outbox_id=?`, due, oid); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status='waiting',attempts=?,next_attempt_at=?,active_request_id=?,last_turn_id=?,reason=?,updated_at=? WHERE root_request_id=?`, attempt, due, request, m.TurnID, reason, now, r.RootRequestID); err != nil {
		return false, err
	}
	return true, a.recoveryEvent(ctx, tx, r, now, "waiting", reason)
}

func (a *App) recoveryEvent(ctx context.Context, tx *store.Tx, r store.AgentRecovery, now int64, status, reason string) error {
	eventID := a.ids.New(protocol.PrefixEvent)
	body, _ := json.Marshal(map[string]string{"status": status, "reason": reason})
	_, err := tx.InsertEvent(ctx, store.EventRow{EventID: eventID, Source: "mainthread", ObjectType: "message", ObjectID: r.MessageID, Kind: "conversation.agent_recovery", BodyJSON: string(body), RequestID: r.RootRequestID, CreatedAt: now})
	if err != nil {
		return err
	}
	if status != "exhausted" && status != "unknown" && status != "blocked" {
		return nil
	}
	var notice string
	if err := tx.Q().QueryRowContext(ctx, `SELECT notice_message_id FROM agent_recoveries WHERE root_request_id=?`, r.RootRequestID).Scan(&notice); err != nil {
		return err
	}
	if notice != "" {
		return nil
	}
	notice = a.ids.New(protocol.PrefixMessage)
	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: notice, Author: "system", Body: "Agent 自动恢复需要处理：" + reason + "\nSource conversation: " + r.ConversationID + "\nSource message: " + r.MessageID, CreatedAt: now}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET notice_message_id=? WHERE root_request_id=?`, notice, r.RootRequestID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO conversation_return_links(message_id,conversation_id,source_message_id) VALUES(?,?,?)`, notice, r.ConversationID, r.MessageID); err != nil {
		return err
	}
	if !a.followupsEnabled {
		return nil
	}
	x := &Exec{IDs: a.ids, Now: time.UnixMilli(now), Env: &protocol.Envelope{RequestID: r.RootRequestID}}
	return queueSingleEventTurn(ctx, x, tx, notice, eventID, "conversation.agent_recovery", nil)
}

// AdmitAgentRecovery fences a claimed continuation immediately before native IO.
// The persisted starting state is never resent after an uncertain native call.
func (a *App) AdmitAgentRecovery(ctx context.Context, request, thread string, expectedRevision ...int64) (bool, error) {
	return a.admitAgentRecovery(ctx, request, thread, false, true, expectedRevision...)
}

// ConfirmAgentRecoveryStart repeats the control fence after context injection.
func (a *App) ConfirmAgentRecoveryStart(ctx context.Context, request, thread string, revision int64) (bool, error) {
	return a.admitAgentRecovery(ctx, request, thread, true, false, revision)
}

// CheckAgentRecoveryRead validates controls without reserving a native start.
func (a *App) CheckAgentRecoveryRead(ctx context.Context, request, thread string) (bool, error) {
	return a.admitAgentRecovery(ctx, request, thread, false, false)
}

func (a *App) admitAgentRecovery(ctx context.Context, request, thread string, confirm, reserve bool, expectedRevision ...int64) (bool, error) {
	admitted := false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		r, err := store.RecoveryForRequest(ctx, tx.Q(), request)
		if err != nil {
			return err
		}
		phase := "waiting"
		if confirm {
			phase = "starting"
		}
		if r.ActiveRequestID != request || r.Status != phase {
			return nil
		}
		now := a.now().UnixMilli()
		if r.NextAttemptAt > now {
			return nil
		}
		paused, err := upgradePausesMain(ctx, tx.Q())
		if err != nil {
			return err
		}
		if paused {
			return nil
		}
		if a.agentRecoveryPolicy.MaxAttempts <= 0 {
			return a.endAgentRecovery(ctx, tx, r, "blocked", "Automatic recovery is disabled by server configuration.", now)
		}
		successor, err := newerRecoveryOwner(ctx, tx.Q(), r.MessageID, request)
		if err != nil {
			return err
		}
		if successor != "" {
			if confirm {
				if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status='waiting' WHERE root_request_id=? AND status='starting'`, r.RootRequestID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE main_turns SET status='rejected',updated_at=? WHERE request_id=? AND status='starting' AND COALESCE(turn_id,'')=''`, now, request); err != nil {
					return err
				}
			}
			return supersedeAgentRecoveries(ctx, tx, r.ConversationID, successor, now)
		}
		if now > r.Deadline {
			return a.endAgentRecovery(ctx, tx, r, "exhausted", "Automatic rate-limit recovery deadline elapsed; attention required.", now)
		}
		if len(expectedRevision) != 0 {
			settings, err := store.GetConversationSettings(ctx, tx.Q(), r.ConversationID)
			if err != nil {
				return err
			}
			if settings.Revision != expectedRevision[0] {
				return nil
			}
		}
		if reserve || confirm {
			if uncertain, err := uncertainRecoveryInput(ctx, tx.Q(), request); err != nil {
				return err
			} else if uncertain {
				return a.endAgentRecovery(ctx, tx, r, "unknown", "An accepted owner input requires reconciliation before continuation.", now)
			}
			var uncertain bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_tool_uses WHERE thread_id=? AND turn_id=? AND status IN ('running','unknown','interrupted'))`, r.ThreadID, r.LastTurnID).Scan(&uncertain); err != nil {
				return err
			}
			if uncertain {
				return a.endAgentRecovery(ctx, tx, r, "unknown", "A recorded tool operation is still running or unknown; reconcile before continuing.", now)
			}
		}
		invalid := r.ThreadID != thread
		var stopped bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_recovery_stops WHERE message_id=?) OR EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=? AND withdrawn_at<>0) OR EXISTS(SELECT 1 FROM logical_turns l JOIN logical_turn_executions e ON e.logical_turn_id=l.logical_turn_id WHERE e.request_id=? AND l.status IN ('stopped','awaiting_clarification','completed')) OR EXISTS(SELECT 1 FROM agent_conversations c JOIN work_items w ON w.work_item_id=c.work_item_id WHERE c.conversation_id=? AND w.status='closed')`, r.MessageID, r.MessageID, request, r.ConversationID).Scan(&stopped); err != nil {
			return err
		}
		if invalid {
			return a.endAgentRecovery(ctx, tx, r, "blocked", "The native conversation binding changed; review the retained responsibility.", now)
		}
		if stopped {
			return a.endAgentRecovery(ctx, tx, r, "cancelled", "Recovery no longer matches the conversation or owner control.", now)
		}
		if confirm || !reserve {
			admitted = true
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE agent_recoveries SET status='starting',next_attempt_at=0,updated_at=? WHERE root_request_id=? AND status='waiting' AND active_request_id=?`, now, r.RootRequestID, request)
		admitted = err == nil
		return err
	})
	if err == nil {
		a.notifyChange()
	}
	return admitted, err
}

func (a *App) endAgentRecovery(ctx context.Context, tx *store.Tx, r store.AgentRecovery, status, reason string, now int64) error {
	if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status=?,reason=?,next_attempt_at=0,updated_at=? WHERE root_request_id=?`, status, reason, now, r.RootRequestID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE outbox SET status='failed',updated_at=? WHERE request_id=? AND status='pending'`, now, r.ActiveRequestID); err != nil {
		return err
	}
	if status == "blocked" || status == "exhausted" {
		if _, err := tx.Exec(ctx, `UPDATE main_turns SET status='rejected',updated_at=? WHERE request_id=? AND status='starting' AND COALESCE(turn_id,'')=''`, now, r.ActiveRequestID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE logical_turn_executions SET settled=1 WHERE request_id=?`, r.ActiveRequestID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='needs_review',reason=?,updated_at=? WHERE logical_turn_id=(SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?) AND status NOT IN ('completed','stopped','awaiting_clarification')`, reason, now, r.ActiveRequestID); err != nil {
			return err
		}
	}
	state := protocol.MessageFailed
	if status == "unknown" {
		state = protocol.MessageUnknown
	}
	if status == "cancelled" {
		state = protocol.MessageInterrupted
	}
	if err := a.insertMessageState(ctx, tx, r.MessageID, state, now); err != nil {
		return err
	}
	if status == "blocked" || status == "exhausted" || status == "cancelled" {
		if err := settleRetainedRecoveryBatch(ctx, tx, a.ids, r, now); err != nil {
			return err
		}
	}
	return a.recoveryEvent(ctx, tx, r, now, status, reason)
}

// BlockAgentRecovery retains an uncertain start or unconfirmed native operation.
func (a *App) BlockAgentRecovery(ctx context.Context, request, reason string) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		r, err := store.RecoveryForRequest(ctx, tx.Q(), request)
		if err != nil {
			return err
		}
		if r.ActiveRequestID != request || r.Status == "cancelled" || r.Status == "completed" || r.Status == "exhausted" {
			return nil
		}
		if r.Status == "unknown" {
			var notice string
			if err := tx.Q().QueryRowContext(ctx, `SELECT notice_message_id FROM agent_recoveries WHERE root_request_id=?`, r.RootRequestID).Scan(&notice); err != nil {
				return err
			}
			if notice != "" {
				return nil
			}
		}
		return a.endAgentRecovery(ctx, tx, r, "unknown", reason, a.now().UnixMilli())
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

// cancelAgentRecoveries stops pending continuations in the owner's transaction.
func cancelAgentRecoveries(ctx context.Context, tx *store.Tx, ids protocol.IDGen, message string, includeCancelled, nativeOpen bool, now int64) (int64, error) {
	roots, err := store.FollowupIDs(ctx, tx.Q(), `WITH RECURSIVE aliases(message_id) AS (SELECT ? UNION SELECT r.successor_message_id FROM agent_recoveries r JOIN aliases a ON r.message_id=a.message_id WHERE r.successor_message_id<>'' AND r.status='cancelled')
 SELECT r.root_request_id FROM agent_recoveries r WHERE r.conversation_id=(SELECT conversation_id FROM conversation_messages WHERE message_id=?) AND (r.message_id IN (SELECT message_id FROM aliases) OR r.successor_message_id IN (SELECT message_id FROM aliases) OR EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages lm ON lm.logical_turn_id=e.logical_turn_id WHERE e.request_id=r.active_request_id AND lm.message_id=?)) AND (r.status IN ('waiting','starting','running','unknown','blocked','exhausted') OR (? AND r.status='cancelled' AND r.successor_message_id<>''))`, message, message, message, includeCancelled)
	if err != nil {
		return 0, err
	}
	for _, root := range roots {
		r, err := store.RecoveryForRequest(ctx, tx.Q(), root)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_recovery_stops(message_id,stopped_at) VALUES(?,?) ON CONFLICT DO NOTHING`, r.MessageID, now); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status='cancelled',reason='Stopped by owner.',next_attempt_at=0,updated_at=? WHERE root_request_id=?`, now, root); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='failed',updated_at=? WHERE operation=? AND status='pending' AND request_id=?`, now, OpRecoverAgent, r.ActiveRequestID); err != nil {
			return 0, err
		}
		if !nativeOpen {
			if err := settleRetainedRecoveryBatch(ctx, tx, ids, r, now); err != nil {
				return 0, err
			}
		}
		if !nativeOpen && r.MessageID != message {
			if err := recordMessageState(ctx, tx, ids, r.MessageID, protocol.MessageInterrupted, now); err != nil {
				return 0, err
			}
		}
	}
	return int64(len(roots)), nil
}

// RequeueRejectedAgentRecovery only retries a definite native rejection.
func (a *App) RequeueRejectedAgentRecovery(ctx context.Context, request string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE main_turns SET status='rejected',updated_at=? WHERE request_id=? AND status='starting' AND COALESCE(turn_id,'')=''`, a.now().UnixMilli(), request); err != nil {
			return err
		}
		recovery, err := store.RecoveryForRequest(ctx, tx.Q(), request)
		if err != nil {
			return err
		}
		if recovery.Status == "cancelled" {
			return a.insertMessageState(ctx, tx, recovery.MessageID, protocol.MessageInterrupted, a.now().UnixMilli())
		}
		_, err = tx.Exec(ctx, `UPDATE agent_recoveries SET status='waiting',next_attempt_at=?,updated_at=? WHERE active_request_id=? AND status='starting'`, a.now().Add(time.Second*5).UnixMilli(), a.now().UnixMilli(), request)
		return err
	})
}

// RecoverySourceRequest distinguishes empty-input attempts from ordinary messages.
func (a *App) RecoverySourceRequest(ctx context.Context, request string) (string, error) {
	var source string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT source_request_id FROM agent_recovery_attempts WHERE request_id=?`, request).Scan(&source)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return source, err
}

// supersedeAgentRecoveries preserves the old responsibility for the new instruction.
func supersedeAgentRecoveries(ctx context.Context, tx *store.Tx, conversation, successor string, now int64) error {
	if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status='cancelled',successor_message_id=?,reason='Superseded by newer owner input; responsibility retained.',next_attempt_at=0,updated_at=? WHERE conversation_id=? AND (status IN ('waiting','blocked') OR (status='cancelled' AND EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=agent_recoveries.successor_message_id AND c.withdrawn_at<>0))) AND NOT EXISTS(SELECT 1 FROM agent_recovery_stops s WHERE s.message_id=agent_recoveries.message_id) AND message_id<>?`, successor, now, conversation, successor); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE outbox SET status='failed',updated_at=? WHERE operation=? AND status='pending' AND request_id IN (SELECT active_request_id FROM agent_recoveries WHERE successor_message_id=?)`, now, OpRecoverAgent, successor)
	return err
}

func adoptRecoveryResponsibilities(ctx context.Context, tx *store.Tx, m store.MainTurn, now int64) error {
	// Follow already-adopted responsibilities through a withdrawn, definitely
	// unadmitted successor instead of severing their lineage.
	const prior = `WITH RECURSIVE prior(id) AS (
 SELECT e.logical_turn_id FROM agent_recoveries r JOIN logical_turn_executions e ON e.request_id=r.root_request_id WHERE r.successor_message_id=? AND NOT EXISTS(SELECT 1 FROM agent_recovery_stops s WHERE s.message_id=r.message_id)
 UNION SELECT l.resumed_by FROM logical_turns l JOIN prior p ON p.id=l.logical_turn_id WHERE l.resumed_by IS NOT NULL) `
	for _, query := range []string{
		prior + `INSERT OR IGNORE INTO logical_turn_messages(logical_turn_id,message_id) SELECT ?,lm.message_id FROM logical_turn_messages lm JOIN prior p ON p.id=lm.logical_turn_id`,
		prior + `INSERT OR IGNORE INTO logical_turn_contexts(logical_turn_id,work_item_id) SELECT ?,lc.work_item_id FROM logical_turn_contexts lc JOIN prior p ON p.id=lc.logical_turn_id`,
	} {
		if _, err := tx.Exec(ctx, query, m.MessageID, m.RequestID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, prior+`UPDATE logical_turns SET status='awaiting_completion',resumed_by=?,updated_at=? WHERE logical_turn_id IN (SELECT id FROM prior) AND resumed_by IS NULL AND logical_turn_id<>?`, m.MessageID, m.RequestID, now, m.RequestID)
	return err
}

// AgentRecoveryUncertain fences subsequent input until an empty-input start is
// reconciled. A missing receipt must never authorize another native turn.
func (a *App) AgentRecoveryUncertain(ctx context.Context, conversation string) (bool, error) {
	var blocked bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_recoveries WHERE conversation_id=? AND status='unknown') OR EXISTS(SELECT 1 FROM agent_recoveries r JOIN native_turn_receipts_pending p ON p.request_id=r.root_request_id OR p.request_id IN (SELECT request_id FROM main_turns WHERE message_id=r.successor_message_id) OR p.request_id IN (SELECT request_id FROM agent_recovery_attempts e WHERE e.root_request_id=r.root_request_id) WHERE r.conversation_id=?)`, store.ConversationID(conversation), store.ConversationID(conversation)).Scan(&blocked)
	})
	return blocked, err
}

// RejectAgentRecovery records a known pre-start failure, separately from an
// uncertain native operation. Only call this before sending native input.
func (a *App) RejectAgentRecovery(ctx context.Context, request, reason string) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		r, err := store.RecoveryForRequest(ctx, tx.Q(), request)
		if err != nil {
			return err
		}
		if r.ActiveRequestID != request || (r.Status != "waiting" && r.Status != "starting") {
			return nil
		}
		now := a.now().UnixMilli()
		if _, err := tx.Exec(ctx, `UPDATE main_turns SET status='rejected',updated_at=? WHERE request_id=? AND status='starting' AND COALESCE(turn_id,'')=''`, now, request); err != nil {
			return err
		}
		return a.endAgentRecovery(ctx, tx, r, "blocked", reason, now)
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

func newerRecoveryOwner(ctx context.Context, q store.Querier, message, request string) (string, error) {
	var successor string
	err := q.QueryRowContext(ctx, `WITH RECURSIVE chain(request_id) AS (SELECT ? UNION SELECT r.source_request_id FROM agent_recovery_attempts r JOIN chain c ON c.request_id=r.request_id) SELECT n.message_id FROM conversation_messages n JOIN conversation_messages original ON original.message_id=? WHERE n.conversation_id=original.conversation_id AND n.author='owner' AND n.seq>original.seq AND EXISTS(SELECT 1 FROM outbox o WHERE o.operation='main_thread.handle_message' AND o.status='pending' AND json_extract(o.payload_json,'$.message_id')=n.message_id) AND NOT EXISTS(SELECT 1 FROM chain c JOIN main_turns t ON t.request_id=c.request_id JOIN main_steers s ON s.thread_id=t.thread_id AND s.turn_id=t.turn_id WHERE s.message_id=n.message_id AND s.native_item_id<>'') AND NOT EXISTS(SELECT 1 FROM conversation_message_controls c WHERE c.message_id=n.message_id AND c.withdrawn_at<>0) ORDER BY n.seq LIMIT 1`, request, message).Scan(&successor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return successor, err
}

func uncertainRecoveryInput(ctx context.Context, q store.Querier, request string) (bool, error) {
	var uncertain bool
	err := q.QueryRowContext(ctx, `WITH RECURSIVE chain(request_id) AS (SELECT ? UNION SELECT r.source_request_id FROM agent_recovery_attempts r JOIN chain c ON c.request_id=r.request_id)
 SELECT EXISTS(SELECT 1 FROM chain c JOIN main_turns t ON t.request_id=c.request_id JOIN main_steers s ON s.thread_id=t.thread_id AND s.turn_id=t.turn_id WHERE s.native_item_id='' AND s.status IN ('sending','accepted','unknown'))`, request).Scan(&uncertain)
	return uncertain, err
}

// DeferAgentReceiptRead persists read-only polling independently of model retries.
func (a *App) DeferAgentReceiptRead(ctx context.Context, conversation string) (bool, error) {
	pending := false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		res, err := tx.Exec(ctx, `UPDATE agent_recoveries SET next_reconcile_at=? WHERE conversation_id=? AND (active_request_id IN (SELECT request_id FROM native_turn_receipts_pending) OR successor_message_id IN (SELECT t.message_id FROM main_turns t JOIN native_turn_receipts_pending p ON p.request_id=t.request_id))`, a.now().Add(30*time.Second).UnixMilli(), conversation)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		pending = n > 0
		return err
	})
	return pending, err
}

// restoreWithdrawnRecovery restores only a definitely unstarted candidate. A
// prepared successor that already adopted work remains explicitly reviewable.
func restoreWithdrawnRecovery(ctx context.Context, x *Exec, tx *store.Tx, message string) error {
	roots, err := store.FollowupIDs(ctx, tx.Q(), `SELECT root_request_id FROM agent_recoveries WHERE status='cancelled' AND successor_message_id=? AND NOT EXISTS(SELECT 1 FROM agent_recovery_stops s WHERE s.message_id=agent_recoveries.message_id)`, message)
	if err != nil {
		return err
	}
	now := x.Now.UnixMilli()
	a := App{ids: x.IDs, followupsEnabled: x.FollowupsEnabled}
	for _, root := range roots {
		r, err := store.RecoveryForRequest(ctx, tx.Q(), root)
		if err != nil {
			return err
		}
		var adopted, unsafe bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turns l ON l.logical_turn_id=e.logical_turn_id WHERE e.request_id=? AND (l.resumed_by IS NOT NULL OR l.status='stopped')),EXISTS(SELECT 1 FROM main_turns WHERE request_id=? AND (status<>'rejected' OR COALESCE(turn_id,'')<>'')) OR EXISTS(SELECT 1 FROM native_turn_receipts_pending WHERE request_id=?)`, root, r.ActiveRequestID, r.ActiveRequestID).Scan(&adopted, &unsafe); err != nil {
			return err
		}
		if adopted || unsafe {
			if _, err := tx.Exec(ctx, `WITH RECURSIVE chain(id) AS (SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=? UNION SELECT l.resumed_by FROM logical_turns l JOIN chain c ON c.id=l.logical_turn_id WHERE l.resumed_by IS NOT NULL) UPDATE logical_turns SET status='needs_review',reason='Queued successor was withdrawn; retained responsibility needs review.',updated_at=? WHERE logical_turn_id IN (SELECT id FROM chain) AND status NOT IN ('completed','stopped')`, root, now); err != nil {
				return err
			}
			if err := a.endAgentRecovery(ctx, tx, r, "blocked", "Queued successor was withdrawn after responsibility preparation; review retained work.", now); err != nil {
				return err
			}
			continue
		}
		var due int64
		err = tx.Q().QueryRowContext(ctx, `SELECT next_attempt_at FROM outbox WHERE request_id=? AND operation=?`, r.ActiveRequestID, OpRecoverAgent).Scan(&due)
		if errors.Is(err, sql.ErrNoRows) {
			if err := a.endAgentRecovery(ctx, tx, r, "blocked", "The retained recovery has no candidate delivery; review responsibility.", now); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status='waiting',successor_message_id='',next_attempt_at=?,reason='Queued replacement withdrawn; continuing retained work.',updated_at=? WHERE root_request_id=?`, due, now, root); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='pending',updated_at=? WHERE request_id=? AND operation=? AND status='failed'`, now, r.ActiveRequestID, OpRecoverAgent); err != nil {
			return err
		}
		if err := recordMessageState(ctx, tx, x.IDs, r.MessageID, protocol.MessageProcessing, now); err != nil {
			return err
		}
		if err := a.recoveryEvent(ctx, tx, r, now, "waiting", "Queued replacement withdrawn; retained candidate restored without resetting its budget."); err != nil {
			return err
		}
	}
	return nil
}

// settleRecoverySuccessors projects the final responsibility outcome without
// rewriting native predecessor results or manufacturing formal assessments.
func (a *App) settleRecoverySuccessors(ctx context.Context, tx *store.Tx, message, status string, now int64) error {
	if status != store.TurnCompleted && status != store.TurnInterrupted && status != store.TurnFailed {
		return nil
	}
	roots, err := store.FollowupIDs(ctx, tx.Q(), `WITH RECURSIVE inherited(message_id) AS (SELECT ? UNION SELECT r.message_id FROM agent_recoveries r JOIN inherited i ON i.message_id=r.successor_message_id WHERE r.status='cancelled' AND r.successor_message_id<>'') SELECT r.root_request_id FROM agent_recoveries r JOIN inherited i ON i.message_id=r.message_id WHERE r.status='cancelled' AND r.successor_message_id<>'' AND r.successor_message_id IN (SELECT message_id FROM inherited)`, message)
	if err != nil {
		return err
	}
	state := turnStates[status]
	if status == store.TurnCompleted {
		state = protocol.MessageProcessed
	}
	for _, root := range roots {
		r, err := store.RecoveryForRequest(ctx, tx.Q(), root)
		if err != nil {
			return err
		}
		if err := a.insertMessageState(ctx, tx, r.MessageID, state, now); err != nil {
			return err
		}
		// Supersession closes the old assessment attempt, without claiming a result.
		if _, err := tx.Exec(ctx, `UPDATE agent_turn_contexts SET settled=1 WHERE request_id IN (SELECT request_id FROM main_turns WHERE message_id=?)`, r.MessageID); err != nil {
			return err
		}
		// Any old buffered batch still retains its actual native execution outcome.
		original, err := store.GetMainTurn(ctx, tx.Q(), root)
		if err != nil {
			return err
		}
		if err := a.settleBackground(ctx, tx, r.MessageID, original.Status, now); err != nil {
			return err
		}
	}
	return nil
}

// A failed replacement must retain the inherited responsibility visibly even
// when model selection failed before it acquired its own native execution.
func (a *App) failRecoverySuccessors(ctx context.Context, tx *store.Tx, message string, now int64) error {
	roots, err := store.FollowupIDs(ctx, tx.Q(), `SELECT root_request_id FROM agent_recoveries WHERE successor_message_id=? AND status='cancelled' AND NOT EXISTS(SELECT 1 FROM agent_recovery_stops s WHERE s.message_id=agent_recoveries.message_id)`, message)
	if err != nil {
		return err
	}
	for _, root := range roots {
		r, err := store.RecoveryForRequest(ctx, tx.Q(), root)
		if err != nil {
			return err
		}
		if err := a.endAgentRecovery(ctx, tx, r, "blocked", "Replacement owner input could not start; retained responsibility requires review.", now); err != nil {
			return err
		}
	}
	return nil
}

// Ending a wait releases retained batch output with its original native status.
// This does not manufacture a work-item assessment or settle an unknown start.
func settleRetainedRecoveryBatch(ctx context.Context, tx *store.Tx, ids protocol.IDGen, r store.AgentRecovery, now int64) error {
	b, err := store.BackgroundByMessage(ctx, tx.Q(), r.MessageID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	original, err := store.GetMainTurn(ctx, tx.Q(), r.RootRequestID)
	if err != nil {
		return err
	}
	if original.Status != store.TurnFailed && original.Status != store.TurnInterrupted {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE background_batches SET status=? WHERE batch_id=?`, original.Status, b.BatchID); err != nil {
		return err
	}
	return publishBackgroundReplies(ctx, &Exec{Env: &protocol.Envelope{}, IDs: ids, Now: time.UnixMilli(now)}, tx, b, original.Status)
}
