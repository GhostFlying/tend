package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SubmitBackgroundAssessment accepts only the authenticated current delivery.
type SubmitBackgroundAssessment struct{ MessageID, TurnID, DeliveryID string }

func (SubmitBackgroundAssessment) Operation() string { return "background.submit" }
func (c SubmitBackgroundAssessment) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalBackgroundAgent)
}
func (c SubmitBackgroundAssessment) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.BackgroundAssessmentPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !slices.Contains([]string{"waiting", "briefing", "escalate"}, p.Disposition) || strings.TrimSpace(p.Note) == "" || len(p.Note) > 8000 || len(p.EvidenceIDs) > 100 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "bounded structured assessment required")
	}
	conv, err := store.AgentConversation(ctx, tx.Q(), x.Principal.ConversationID)
	if err != nil {
		return Outcome{}, err
	}
	turn, err := store.GetMainTurn(ctx, tx.Q(), c.DeliveryID)
	if err != nil {
		return Outcome{}, err
	}
	target, err := store.MessageConversation(ctx, tx.Q(), c.MessageID)
	if err != nil {
		return Outcome{}, err
	}
	if target != conv.ID || turn.MessageID != c.MessageID || turn.ThreadID != x.Principal.Thread || (turn.TurnID != "" && turn.TurnID != c.TurnID) || (turn.Status != store.TurnStarting && turn.Status != store.TurnStarted) {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "assessment must belong to current background turn")
	}
	var rev int64
	var unread, reviews int
	if err = tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, conv.WorkItemID).Scan(&rev); err != nil {
		return Outcome{}, err
	}
	if err = tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE b.message_id=? AND s.cancelled_at=0 AND NOT EXISTS(SELECT 1 FROM background_reads r WHERE r.batch_id=s.batch_id AND r.source_seq=s.seq AND r.thread_id=? AND r.turn_id=?)`, c.MessageID, x.Principal.Thread, c.TurnID).Scan(&unread); err != nil {
		return Outcome{}, err
	}
	if unread != 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "read all batch sources before submission")
	}
	if err = tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_completion_reviews WHERE work_item_id=? AND reviewed_revision IS NULL`, conv.WorkItemID).Scan(&reviews); err != nil {
		return Outcome{}, err
	}
	if reviews > 0 && p.Disposition != "escalate" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "completion review requires escalation to the main coordinator")
	}
	for _, id := range p.EvidenceIDs {
		var allowed bool
		if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_work_items WHERE message_id=? AND work_item_id=? AND active=1) OR EXISTS(SELECT 1 FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE b.message_id=? AND s.cancelled_at=0 AND (s.message_id=? OR s.event_id=?))`, id, conv.WorkItemID, c.MessageID, id, id).Scan(&allowed); err != nil {
			return Outcome{}, err
		}
		if !allowed {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "evidence must be a linked message or retained batch source")
		}
	}
	var deliveredRevision int64
	if err = tx.Q().QueryRowContext(ctx, `SELECT context_revision FROM agent_turn_contexts WHERE request_id=? AND conversation_id=?`, c.DeliveryID, conv.ID).Scan(&deliveredRevision); err != nil {
		return Outcome{}, err
	}
	if deliveredRevision != p.ContextRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "submit against the context revision delivered to this turn")
	}
	stale := rev != p.ContextRevision
	raw, _ := json.Marshal(p.EvidenceIDs)
	id := x.IDs.New(protocol.Prefix("assessment"))
	if _, err = tx.Exec(ctx, `INSERT INTO background_assessments(assessment_id,conversation_id,delivery_id,context_revision,disposition,note,evidence_json,stale,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, conv.ID, c.DeliveryID, p.ContextRevision, p.Disposition, p.Note, string(raw), stale, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if p.Disposition == "escalate" || stale {
		note := "后台核对需要主 Agent 处理：" + conv.Title + "\n" + p.Note
		if stale {
			note = "后台结果的上下文版本已过期，必须重新核对。\n" + note
		}
		returnID, err := createCoordinationNotice(ctx, x, tx, note, c.MessageID, conv.ID)
		if err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE background_assessments SET return_message_id=? WHERE assessment_id=?`, returnID, id); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Notify: true, Result: map[string]any{"assessment_id": id, "stale": stale, "context_revision": rev}, Events: []Event{{ObjectType: "conversation", ObjectID: conv.ID, Kind: "background.assessment", Body: map[string]any{"stale": stale, "disposition": p.Disposition}}}}, nil
}

// settleAgentAssessment refuses to equate a model turn ending with a valid check.
func (a *App) settleAgentAssessment(ctx context.Context, tx *store.Tx, message, status string, now int64) error {
	var request, conversation string
	var revision int64
	var settled bool
	err := tx.Q().QueryRowContext(ctx, `SELECT c.request_id,c.conversation_id,c.context_revision,c.settled FROM agent_turn_contexts c JOIN main_turns t ON t.request_id=c.request_id WHERE t.message_id=? ORDER BY t.created_at DESC LIMIT 1`, message).Scan(&request, &conversation, &revision, &settled)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if settled {
		return nil
	}
	var valid, escalated bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM background_assessments b JOIN agent_conversations c ON c.conversation_id=b.conversation_id JOIN work_item_contexts v ON v.work_item_id=c.work_item_id WHERE b.delivery_id=? AND b.context_revision=v.revision AND b.stale=0),EXISTS(SELECT 1 FROM background_assessments WHERE delivery_id=? AND (disposition='escalate' OR return_message_id<>''))`, request, request).Scan(&valid, &escalated); err != nil {
		return err
	}
	if (!valid || status != store.TurnCompleted) && !escalated {
		x := &Exec{Env: &protocol.Envelope{RequestID: request}, IDs: a.ids, Now: time.UnixMilli(now)}
		if err = queueCoordinationNotice(ctx, x, tx, "后台处理未形成有效的当前版本核对结果。请检查持久处理过程、失败或上下文变更，再决定下一步。", message, conversation); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE agent_turn_contexts SET settled=1 WHERE request_id=?`, request)
	return err
}
