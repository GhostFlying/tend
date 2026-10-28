package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// BackgroundCommand carries the authenticated harness identity separately from
// model arguments. A caller cannot nominate another turn or message.
type BackgroundCommand struct{ Action, MessageID, TurnID, DeliveryID string }

func (c BackgroundCommand) Operation() string { return "background." + c.Action }
func (c BackgroundCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if (x.Principal.Kind != protocol.PrincipalMainAgent && x.Principal.Kind != protocol.PrincipalBackgroundAgent) || c.TurnID == "" || c.MessageID == "" || c.DeliveryID == "" {
		return protocol.Errorf(protocol.CodeForbidden, "current hosted background turn required")
	}
	return nil
}
func (c BackgroundCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	turn, err := store.GetMainTurn(ctx, tx.Q(), c.DeliveryID)
	if err != nil {
		return Outcome{}, err
	}
	if turn.MessageID != c.MessageID || turn.ThreadID != x.Principal.Thread || (turn.TurnID != "" && turn.TurnID != c.TurnID) || (turn.Status != store.TurnStarting && turn.Status != store.TurnStarted) {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "background turn is not current")
	}
	batch, err := store.BackgroundByMessage(ctx, tx.Q(), c.MessageID)
	if err != nil {
		return Outcome{}, err
	}
	if batch.Status != "frozen" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "background batch is not frozen")
	}
	if x.Principal.Kind == protocol.PrincipalBackgroundAgent {
		conv, err := store.AgentConversation(ctx, tx.Q(), x.Principal.ConversationID)
		if err != nil {
			return Outcome{}, err
		}
		if conv.WorkItemID != batch.WorkItemID || c.Action != "sources" {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "background scope mismatch")
		}
	}
	if c.Action == "sources" {
		var p struct {
			After int64 `json:"after"`
		}
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if p.After < 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid source cursor")
		}
		page, err := store.BackgroundSources(ctx, tx.Q(), c.MessageID, p.After)
		if err != nil {
			return Outcome{}, err
		}
		for _, s := range page.Sources {
			if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO background_reads(batch_id,thread_id,turn_id,source_seq) VALUES(?,?,?,?)`, batch.BatchID, x.Principal.Thread, c.TurnID, s.Seq); err != nil {
				return Outcome{}, err
			}
		}
		// Cancelled, unclaimed instructions remain available to the owner's
		// audit API, but are not reintroduced through the model's source tool.
		for i := range page.Sources {
			if page.Sources[i].CancelledAt != 0 {
				page.Sources[i].Body = "[cancelled before delivery; instruction withheld]"
				page.Sources[i].Data = nil
			}
		}
		return Outcome{Result: page}, nil
	}
	var published bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_agent_messages a JOIN conversation_messages m ON m.message_id=a.message_id JOIN request_records r ON r.request_id=m.request_id WHERE a.thread_id=? AND a.turn_id=? AND r.operation='conversation.reply' AND r.status='succeeded')`, x.Principal.Thread, c.TurnID).Scan(&published); err != nil {
		return Outcome{}, err
	}
	if published {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "this turn already published a formal reply and cannot check in silently")
	}
	var p protocol.BackgroundCheckin
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.Note) == "" || len(p.Note) > 2000 || len(p.EvidenceIDs) > 100 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "bounded check-in note and evidence are required")
	}
	var unread int
	if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM background_sources s WHERE s.batch_id=? AND s.cancelled_at=0 AND NOT EXISTS(SELECT 1 FROM background_reads r WHERE r.batch_id=s.batch_id AND r.source_seq=s.seq AND r.thread_id=? AND r.turn_id=?)`, batch.BatchID, x.Principal.Thread, c.TurnID).Scan(&unread); err != nil {
		return Outcome{}, err
	}
	if unread != 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "read every source page before checking in")
	}
	var reviews int
	if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM work_item_completion_reviews r WHERE r.reviewed_revision IS NULL AND (r.work_item_id=? OR EXISTS(SELECT 1 FROM background_sources s WHERE s.batch_id=? AND s.event_id=r.event_id AND s.cancelled_at=0))`, batch.WorkItemID, batch.BatchID).Scan(&reviews); err != nil {
		return Outcome{}, err
	}
	if reviews != 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "unresolved completion reviews require explicit review")
	}
	for _, id := range p.EvidenceIDs {
		var n int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM background_sources WHERE batch_id=? AND cancelled_at=0 AND (message_id=? OR event_id=? OR rule_id=? OR occurrence_id=?)`, batch.BatchID, id, id, id, id).Scan(&n); err != nil {
			return Outcome{}, err
		}
		if n == 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "evidence must reference a retained batch source")
		}
	}
	p.ThreadID = x.Principal.Thread
	p.TurnID = c.TurnID
	p.CreatedAt = x.Now.UnixMilli()
	raw, err := json.Marshal(p)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE background_batches SET checkin_json=? WHERE batch_id=?`, string(raw), batch.BatchID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: p, Events: []Event{{ObjectType: "background_batch", ObjectID: batch.BatchID, Kind: "background.checked_in", Body: map[string]any{"note": p.Note}}}}, nil
}
func cancelBackgroundFollowups(ctx context.Context, x *Exec, tx *store.Tx, rule string) error {
	batches, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT s.batch_id FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.rule_id=? AND s.cancelled_at=0 AND b.status='pending'`, rule)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		messages, err := store.FollowupIDs(ctx, tx.Q(), `SELECT message_id FROM background_sources WHERE batch_id=? AND rule_id=? AND cancelled_at=0`, batch, rule)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: message, Kind: "conversation.message_state", BodyJSON: `{"state":"interrupted","followup_cancelled":true}`, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `UPDATE followup_occurrences SET status='cancelled' WHERE occurrence_id IN (SELECT occurrence_id FROM background_sources WHERE batch_id=? AND rule_id=?)`, batch, rule); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE background_sources SET cancelled_at=? WHERE batch_id=? AND rule_id=?`, x.Now.UnixMilli(), batch, rule); err != nil {
			return err
		}
		var remaining int
		var message string
		if err := tx.Q().QueryRowContext(ctx, `SELECT message_id,(SELECT COUNT(*) FROM background_sources WHERE batch_id=? AND cancelled_at=0) FROM background_batches WHERE batch_id=?`, batch, batch).Scan(&message, &remaining); err != nil {
			return err
		}
		if remaining == 0 {
			if _, err := tx.Exec(ctx, `UPDATE background_batches SET status='cancelled' WHERE batch_id=?`, batch); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE outbox SET status='cancelled' WHERE (destination='main_thread' OR destination LIKE 'background_thread:%') AND status='pending' AND json_extract(payload_json,'$.message_id')=?`, message); err != nil {
				return err
			}
			if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: message, Kind: "conversation.message_state", BodyJSON: `{"state":"interrupted"}`, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
				return err
			}
		}
	}
	return nil
}
