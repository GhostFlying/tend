package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ErrRepairCancelled rejects a claimed repair whose logical responsibility
// was stopped, deferred or completed before native admission.
var ErrRepairCancelled = protocol.Errorf(protocol.CodeInvalidTransition, "logical turn no longer admits automatic repair")

// CommitLogicalTurn records explicit fact review, including a review finding no new facts.
// Native completion still performs the final content/version check afterwards.
type CommitLogicalTurn struct{ RequestID string }

func (CommitLogicalTurn) Operation() string { return "conversation.commit_turn" }
func (CommitLogicalTurn) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent)
}
func (c CommitLogicalTurn) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.LogicalTurnCommit
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	v, err := store.LogicalTurn(ctx, tx.Q(), c.RequestID)
	if err != nil {
		return Outcome{}, err
	}
	allowed := map[string]bool{}
	for _, m := range v.Messages {
		allowed[m.MessageID] = true
	}
	if len(p.Messages) == 0 || len(p.Messages) > 100 || len(p.Contexts) > 500 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "review between 1 and 100 exact message versions")
	}
	seen := map[string]bool{}
	for _, r := range p.Messages {
		if seen[r.MessageID] {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "duplicate reviewed message")
		}
		seen[r.MessageID] = true
		if !allowed[r.MessageID] {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "message outside this logical turn")
		}
		m, err := store.MessageAssociations(ctx, tx.Q(), r.MessageID)
		if err != nil {
			return Outcome{}, err
		}
		if m.ContentRevision != r.ContentRevision || m.Revision != r.AssociationRevision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "reviewed message changed")
		}
		if m.Disposition == "pending" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "classify the message before completing fact review")
		}
		if _, err := tx.Exec(ctx, `UPDATE message_dispositions SET facts_revision=content_revision WHERE message_id=?`, r.MessageID); err != nil {
			return Outcome{}, err
		}
	}
	contexts := map[string]bool{}
	for _, c := range v.Contexts {
		contexts[c.WorkItemID] = true
	}
	reviewed := map[string]bool{}
	for _, c := range p.Contexts {
		if !contexts[c.WorkItemID] || reviewed[c.WorkItemID] {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "review only distinct contexts belonging to this logical turn")
		}
		reviewed[c.WorkItemID] = true
		var rev, baseline int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT revision,baseline_revision FROM work_item_contexts WHERE work_item_id=?`, c.WorkItemID).Scan(&rev, &baseline); err != nil {
			return Outcome{}, err
		}
		if rev != c.Revision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "context changed during review")
		}
		var unresolved bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_context_facts WHERE superseded=0 AND needs_review=1 AND (work_item_id=? OR scope='global' OR (scope='project' AND project=(SELECT project FROM work_items WHERE work_item_id=?))))`, c.WorkItemID, c.WorkItemID).Scan(&unresolved); err != nil {
			return Outcome{}, err
		}
		if unresolved {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reconcile invalidated facts before completing context review")
		}
		if baseline == 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "initialize or reconcile the work item context first")
		}
		for _, message := range p.Messages {
			if _, err := tx.Exec(ctx, `UPDATE message_context_obligations SET needs_review=0 WHERE work_item_id=? AND message_id=?`, c.WorkItemID, message.MessageID); err != nil {
				return Outcome{}, err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_context_reviews(logical_turn_id,work_item_id,revision) VALUES(?,?,?) ON CONFLICT(logical_turn_id,work_item_id) DO UPDATE SET revision=excluded.revision`, v.ID, c.WorkItemID, c.Revision); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Result: map[string]any{"logical_turn_id": v.ID}, Notify: true, Events: []Event{{ObjectType: "message", ObjectID: v.Messages[0].MessageID, Kind: "conversation.turn_reviewed"}}}, nil
}

func (a *App) LogicalTurn(ctx context.Context, request string) (protocol.LogicalTurnState, error) {
	var v protocol.LogicalTurnState
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		v, err = store.LogicalTurn(ctx, tx.Q(), request)
		return err
	})
	return v, err
}
func (a *App) IsRepairExecution(ctx context.Context, request string) (bool, error) {
	var repair bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT repair FROM logical_turn_executions WHERE request_id=?),0)`, request).Scan(&repair)
	})
	return repair, err
}

func (a *App) registerLogicalTurn(ctx context.Context, tx *store.Tx, m store.MainTurn, now int64) error {
	conversation, err := store.MessageConversation(ctx, tx.Q(), m.MessageID)
	if err != nil {
		return err
	}
	if conversation != "main" {
		return nil
	}
	var exists bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions WHERE request_id=?)`, m.RequestID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var tracked bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_dispositions WHERE message_id=?)`, m.MessageID).Scan(&tracked); err != nil {
		return err
	}
	if !tracked {
		return nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO logical_turns(logical_turn_id,message_id,updated_at) VALUES(?,?,?)`, m.RequestID, m.MessageID, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id) VALUES(?,?)`, m.RequestID, m.RequestID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO logical_turn_messages(logical_turn_id,message_id) VALUES(?,?)`, m.RequestID, m.MessageID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT OR IGNORE INTO logical_turn_messages(logical_turn_id,message_id) SELECT ?,r.source_message_id FROM conversation_return_links r JOIN conversation_messages m ON m.message_id=r.source_message_id WHERE r.message_id=?`, m.RequestID, m.MessageID)
	return err
}

func (a *App) settleLogicalTurn(ctx context.Context, tx *store.Tx, m store.MainTurn, now int64) (bool, error) {
	var pendingReceipt bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_turn_receipts_pending WHERE request_id=?)`, m.RequestID).Scan(&pendingReceipt); err != nil {
		return false, err
	}
	if pendingReceipt {
		return true, nil
	}

	if m.Status != store.TurnCompleted && m.Status != store.TurnFailed && m.Status != store.TurnInterrupted {
		return false, nil
	}
	conversation, err := store.MessageConversation(ctx, tx.Q(), m.MessageID)
	if err != nil {
		return false, err
	}
	if conversation != "main" {
		return false, nil
	}
	var settled, registered bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions WHERE request_id=?), COALESCE((SELECT settled FROM logical_turn_executions WHERE request_id=?),0)`, m.RequestID, m.RequestID).Scan(&registered, &settled); err != nil {
		return false, err
	}
	if !registered {
		return false, nil
	}
	v, err := store.LogicalTurn(ctx, tx.Q(), m.RequestID)
	if err != nil {
		return false, err
	}
	// A reconciled adopted execution belongs to its final successor. Recheck
	// that responsibility rather than creating a competing repair for the ancestor.
	var successor string
	if err := tx.Q().QueryRowContext(ctx, `WITH RECURSIVE chain(id) AS (
 SELECT ? UNION SELECT l.resumed_by FROM logical_turns l JOIN chain c ON l.logical_turn_id=c.id WHERE l.resumed_by IS NOT NULL)
 SELECT l.logical_turn_id FROM logical_turns l JOIN chain c ON l.logical_turn_id=c.id WHERE l.resumed_by IS NULL`, v.ID).Scan(&successor); err != nil {
		return false, err
	}
	if successor != v.ID {
		var queued bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN outbox o ON o.request_id=e.request_id WHERE e.logical_turn_id=? AND e.repair=1 AND o.status IN ('pending','sending','unknown'))`, successor).Scan(&queued); err != nil {
			return false, err
		}
		if queued {
			return true, nil
		}
		var request string
		if err := tx.Q().QueryRowContext(ctx, `SELECT e.request_id FROM logical_turn_executions e JOIN main_turns t ON t.request_id=e.request_id WHERE e.logical_turn_id=? ORDER BY e.rowid DESC LIMIT 1`, successor).Scan(&request); err != nil {
			return false, err
		}
		rec, err := store.GetMainTurn(ctx, tx.Q(), request)
		if err != nil {
			return false, err
		}
		if rec.Status == store.TurnStarting || rec.Status == store.TurnStarted {
			return true, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE logical_turn_executions SET settled=0 WHERE request_id=?`, request); err != nil {
			return false, err
		}
		return a.settleLogicalTurn(ctx, tx, rec, now)
	}
	uncertain, err := logicalNativeUncertain(ctx, tx.Q(), v.ID)
	if err != nil {
		return false, err
	}
	if uncertain {
		if _, err := tx.Exec(ctx, `UPDATE logical_turn_executions SET settled=0 WHERE request_id=?`, m.RequestID); err != nil {
			return false, err
		}
		_, err := tx.Exec(ctx, `UPDATE logical_turns SET status='unknown',reason='An adopted native execution must be reconciled before completion.',updated_at=? WHERE logical_turn_id=? AND status NOT IN ('stopped','awaiting_clarification')`, now, v.ID)
		return true, err
	}
	if settled {
		var incomplete bool
		err := tx.Q().QueryRowContext(ctx, `SELECT status<>'completed' FROM logical_turns WHERE logical_turn_id=(SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?)`, m.RequestID).Scan(&incomplete)
		return incomplete, err
	}
	if _, err := tx.Exec(ctx, `UPDATE logical_turn_executions SET settled=1 WHERE request_id=?`, m.RequestID); err != nil {
		return false, err
	}
	var missing int
	if err = tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM logical_turn_messages lm LEFT JOIN message_dispositions d ON d.message_id=lm.message_id WHERE lm.logical_turn_id=? AND (d.message_id IS NULL OR d.disposition='pending' OR d.classified_revision<>d.content_revision OR d.facts_revision<>d.content_revision)`, v.ID).Scan(&missing); err != nil {
		return false, err
	}
	var unready int
	if err = tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_contexts c WHERE (c.baseline_revision=0 OR NOT EXISTS(SELECT 1 FROM logical_turn_context_reviews cr WHERE cr.logical_turn_id=? AND cr.work_item_id=c.work_item_id AND cr.revision=c.revision) OR EXISTS(SELECT 1 FROM message_context_obligations o JOIN logical_turn_messages lm ON lm.message_id=o.message_id WHERE lm.logical_turn_id=? AND o.work_item_id=c.work_item_id AND o.needs_review=1)) AND c.work_item_id IN (SELECT work_item_id FROM logical_turn_contexts WHERE logical_turn_id=?)`, v.ID, v.ID, v.ID).Scan(&unready); err != nil {
		return false, err
	}
	if v.Status == "stopped" || v.Status == "awaiting_clarification" {
		return true, nil
	}
	if missing == 0 && unready == 0 {
		_, err = tx.Exec(ctx, `UPDATE logical_turns SET status='completed',reason='',updated_at=? WHERE logical_turn_id=?`, now, v.ID)
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `WITH RECURSIVE children(id) AS (SELECT logical_turn_id FROM logical_turns WHERE resumed_by=? UNION ALL SELECT l.logical_turn_id FROM logical_turns l JOIN children c ON l.resumed_by=c.id) UPDATE logical_turns SET status='completed',reason='',updated_at=? WHERE logical_turn_id IN (SELECT id FROM children)`, v.ID, now)
		return false, err
	}
	reason := fmt.Sprintf("%d messages require classification or fact review; %d contexts require initialization", missing, unready)
	status := "recovering"
	if v.RepairAttempts >= 5 {
		status = "needs_review"
	}
	if _, err = tx.Exec(ctx, `UPDATE logical_turns SET status=?,reason=?,updated_at=? WHERE logical_turn_id=?`, status, reason, now, v.ID); err != nil {
		return true, err
	}
	if status != "recovering" {
		return true, nil
	}
	// The stable child identity and insertion are atomic with native settlement.
	attempt := v.RepairAttempts + 1
	request := a.ids.New(protocol.PrefixRequest)
	var exists bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions WHERE request_id=?)`, request).Scan(&exists); err != nil {
		return true, err
	}
	if exists {
		return true, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) VALUES(?,?,1)`, request, v.ID); err != nil {
		return true, err
	}
	payload, _ := json.Marshal(HandleMessage{MessageID: m.MessageID})
	outbox := a.ids.New(protocol.PrefixOutbox)
	if err = tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: outbox, RequestID: request, ParentRequestID: m.RequestID, Destination: MainThreadDestination, Operation: "conversation.repair_turn", PayloadJSON: string(payload), CreatedAt: now}); err != nil {
		return true, err
	}
	delay := []int64{0, 0, 30000, 120000, 600000}[attempt-1]
	if _, err = tx.Exec(ctx, `UPDATE outbox SET next_attempt_at=? WHERE outbox_id=?`, now+delay, outbox); err != nil {
		return true, err
	}
	_, err = tx.Exec(ctx, `UPDATE logical_turns SET repair_attempts=? WHERE logical_turn_id=?`, attempt, v.ID)
	return true, err
}

// ResumeLogicalTurn resumes only classification/context responsibility. It never
// replays the original business delivery, including after owner interruption.
type ResumeLogicalTurn struct{}

func (ResumeLogicalTurn) Operation() string { return "conversation.resume_processing" }
func (ResumeLogicalTurn) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (ResumeLogicalTurn) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var revision int64
	var message, status, request string
	if err := tx.Q().QueryRowContext(ctx, `SELECT revision,message_id,status FROM logical_turns WHERE logical_turn_id=?`, x.Env.Target.ID).Scan(&revision, &message, &status); err != nil {
		return Outcome{}, errNotFound(err, "logical turn does not exist")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "processing state changed")
	}
	if status == "completed" {
		return Outcome{NoEffect: true}, nil
	}
	var uncertain bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN outbox o ON o.request_id=e.request_id WHERE e.logical_turn_id=? AND o.status IN ('sending','unknown')) OR EXISTS(SELECT 1 FROM logical_turn_executions e JOIN main_turns t ON t.request_id=e.request_id WHERE e.logical_turn_id=? AND t.status IN ('starting','started')) OR EXISTS(SELECT 1 FROM logical_turn_executions e JOIN native_turn_receipts_pending p ON p.request_id=e.request_id WHERE e.logical_turn_id=?)`, x.Env.Target.ID, x.Env.Target.ID, x.Env.Target.ID).Scan(&uncertain); err != nil {
		return Outcome{}, err
	}
	if uncertain || status == "unknown" || status == "processing" || status == "recovering" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "execution is active or uncertain; reconcile before resuming")
	}
	if err := tx.Q().QueryRowContext(ctx, `SELECT request_id FROM logical_turn_executions WHERE logical_turn_id=? ORDER BY rowid DESC LIMIT 1`, x.Env.Target.ID).Scan(&request); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='processing',repair_attempts=0,updated_at=? WHERE logical_turn_id=?`, x.Now.UnixMilli(), x.Env.Target.ID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE logical_turn_executions SET settled=0 WHERE request_id=?`, request); err != nil {
		return Outcome{}, err
	}
	// Use the same transaction and injected dependencies as normal settlement.
	a := App{ids: x.IDs}
	if _, err := a.settleLogicalTurn(ctx, tx, store.MainTurn{RequestID: request, MessageID: message, Status: store.TurnFailed}, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"logical_turn_id": x.Env.Target.ID}, Notify: true, Events: []Event{{ObjectType: "message", ObjectID: message, Kind: "conversation.processing_resumed"}}}, nil
}

type AwaitTurnClarification struct{ RequestID string }

func (AwaitTurnClarification) Operation() string { return "conversation.await_clarification" }
func (AwaitTurnClarification) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent)
}
func (c AwaitTurnClarification) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.TurnClarification
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if len(p.Question) == 0 || len(p.Question) > 4000 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "a bounded unresolved question is required")
	}
	v, err := store.LogicalTurn(ctx, tx.Q(), c.RequestID)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='awaiting_clarification',reason=?,updated_at=? WHERE logical_turn_id=?`, p.Question, x.Now.UnixMilli(), v.ID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"logical_turn_id": v.ID}, Notify: true, Events: []Event{{ObjectType: "message", ObjectID: v.Messages[0].MessageID, Kind: "conversation.clarification_pending"}}}, nil
}

// recoverLateTurnMessages runs inside dispatcher admission. Triggers retain the
// responsibility even if the process dies after persisting a late text item.
func (a *App) recoverLateTurnMessages(ctx context.Context, tx *store.Tx) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT logical_turn_id FROM logical_turns WHERE status='pending_repair' ORDER BY rowid LIMIT 50`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var request string
		if err := tx.Q().QueryRowContext(ctx, `SELECT e.request_id FROM logical_turn_executions e JOIN main_turns t ON t.request_id=e.request_id WHERE e.logical_turn_id=? ORDER BY e.rowid DESC LIMIT 1`, id).Scan(&request); err != nil {
			return err
		}
		rec, err := store.GetMainTurn(ctx, tx.Q(), request)
		if err != nil {
			return err
		}
		if rec.Status == store.TurnStarting || rec.Status == store.TurnStarted {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE logical_turn_executions SET settled=0 WHERE request_id=?`, request); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE logical_turns SET repair_attempts=0 WHERE logical_turn_id=?`, id); err != nil {
			return err
		}
		if _, err := a.settleLogicalTurn(ctx, tx, rec, a.now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// AdoptClarification moves only unfinished review into the answering turn.
// The original responsibility is retained until its successor actually completes.
type AdoptClarification struct{ RequestID string }

func (AdoptClarification) Operation() string { return "conversation.adopt_clarification" }
func (AdoptClarification) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent)
}
func (c AdoptClarification) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	current, err := store.LogicalTurn(ctx, tx.Q(), c.RequestID)
	if err != nil {
		return Outcome{}, err
	}
	var status string
	var revision int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT status,revision FROM logical_turns WHERE logical_turn_id=?`, x.Env.Target.ID).Scan(&status, &revision); err != nil {
		return Outcome{}, errNotFound(err, "clarification does not exist")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "clarification changed")
	}
	if status != "awaiting_clarification" || current.ID == x.Env.Target.ID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only a separate pending clarification may be adopted")
	}
	uncertain, err := logicalNativeUncertain(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if uncertain {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reconcile the clarification's native executions before adopting it")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_messages(logical_turn_id,message_id) SELECT ?,message_id FROM logical_turn_messages WHERE logical_turn_id=? ON CONFLICT(logical_turn_id,message_id) DO NOTHING`, current.ID, x.Env.Target.ID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_contexts(logical_turn_id,work_item_id) SELECT ?,work_item_id FROM logical_turn_contexts WHERE logical_turn_id=? ON CONFLICT(logical_turn_id,work_item_id) DO NOTHING`, current.ID, x.Env.Target.ID); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='awaiting_completion',resumed_by=?,updated_at=? WHERE logical_turn_id=?`, current.ID, x.Now.UnixMilli(), x.Env.Target.ID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Notify: true, Result: map[string]string{"logical_turn_id": current.ID}, Events: []Event{{ObjectType: "message", ObjectID: current.Messages[0].MessageID, Kind: "conversation.clarification_resumed"}}}, nil
}

// UncertainNativeTurns contains only registered executions with a missing native receipt.
func (a *App) UncertainNativeTurns(ctx context.Context, thread string) ([]store.MainTurn, error) {
	var turns []store.MainTurn
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT request_id FROM main_turns WHERE thread_id=? AND (status='starting' OR request_id IN (SELECT request_id FROM native_turn_receipts_pending)) ORDER BY created_at LIMIT 50`, thread)
		if err != nil {
			return err
		}
		for _, id := range ids {
			t, err := store.GetMainTurn(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			turns = append(turns, t)
		}
		return nil
	})
	return turns, err
}

// attachReviewSource transfers an explicitly referenced source into the current
// responsibility. It never enumerates unrelated historical messages.
func attachReviewSource(ctx context.Context, x *Exec, tx *store.Tx, message string) error {
	if message == "" {
		return nil
	}
	request, err := x.reviewExecution(ctx, tx.Q())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO logical_turn_messages(logical_turn_id,message_id)
 SELECT e.logical_turn_id,? FROM logical_turn_executions e JOIN logical_turns l ON l.logical_turn_id=e.logical_turn_id
 WHERE e.request_id=? AND l.status<>'completed'
 ON CONFLICT(logical_turn_id,message_id) DO NOTHING`, message, request)
	return err
}

func markNativeReceiptsPending(ctx context.Context, tx *store.Tx, thread string, now int64) error {
	if _, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) SELECT t.request_id FROM main_turns t JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE t.thread_id=? AND t.status IN ('started','starting') ON CONFLICT(request_id) DO NOTHING`, thread); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE logical_turns SET status='unknown',reason='Native execution history must be reconciled before metadata repair.',updated_at=? WHERE status NOT IN ('stopped','awaiting_clarification') AND logical_turn_id IN (SELECT e.logical_turn_id FROM logical_turn_executions e JOIN native_turn_receipts_pending p ON p.request_id=e.request_id JOIN main_turns t ON t.request_id=e.request_id WHERE t.thread_id=?)`, now, thread)
	return err
}

// AwaitNativeReceipts fences completion before disconnect notifications wake Deliver.
func (a *App) AwaitNativeReceipts(ctx context.Context, thread string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error { return markNativeReceiptsPending(ctx, tx, thread, a.now().UnixMilli()) })
}

func (a *App) NativeReceiptPending(ctx context.Context, request string) (bool, error) {
	var pending bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_turn_receipts_pending WHERE request_id=?)`, request).Scan(&pending)
	})
	return pending, err
}

// logicalNativeUncertain checks the whole responsibility inherited by a turn.
func logicalNativeUncertain(ctx context.Context, q store.Querier, id string) (bool, error) {
	var uncertain bool
	err := q.QueryRowContext(ctx, `WITH RECURSIVE responsibility(id) AS (
 SELECT ? UNION SELECT l.logical_turn_id FROM logical_turns l JOIN responsibility r ON l.resumed_by=r.id)
 SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN responsibility r ON r.id=e.logical_turn_id
 LEFT JOIN main_turns t ON t.request_id=e.request_id
 WHERE t.status IN ('starting','started') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=e.request_id))`, id).Scan(&uncertain)
	return uncertain, err
}
