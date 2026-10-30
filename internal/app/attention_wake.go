package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func questionEvent(e Event) bool {
	return e.ObjectType == "decision" || e.ObjectType == "work_item_choice"
}
func questionAttentionIssue(ctx context.Context, q store.Querier, e Event) (string, error) {
	column := "decision_id"
	if e.ObjectType == "work_item_choice" {
		column = "choice_id"
	}
	var issue sql.NullString
	err := q.QueryRowContext(ctx, `SELECT issue_id FROM attentions WHERE `+column+`=?`, e.ObjectID).Scan(&issue)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return issue.String, err
}

// routeQuestionAttention separates the chat return link from business ownership.
func routeQuestionAttention(ctx context.Context, x *Exec, tx *store.Tx, e Event, message, event string, answer json.RawMessage) (bool, error) {
	issue, err := questionAttentionIssue(ctx, tx.Q(), e)
	if err != nil || issue == "" {
		return false, err
	}
	if e.Kind == "decision.opened" {
		if x.Principal.Kind == protocol.PrincipalMainAgent {
			return true, nil
		}
		return true, appendBackgroundSource(ctx, x, tx, "attention:"+e.ObjectID, "", message, event, e.Kind, answer)
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), issue)
	if err != nil {
		return true, err
	}
	if current.Assignee.Kind == protocol.IssueAssigneeAgent {
		return true, appendBackgroundSource(ctx, x, tx, "work_item:"+issue, issue, message, event, e.Kind, answer)
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		return true, nil
	}
	return true, appendBackgroundSource(ctx, x, tx, "attention:"+e.ObjectID, "", message, event, e.Kind, answer)
}
func recordAttentionNotice(ctx context.Context, x *Exec, tx *store.Tx, e Event, eventID string) error {
	a, err := store.GetAttention(ctx, tx.Q(), e.ObjectID)
	if err != nil {
		return err
	}
	if a.Kind == "decision" && x.Env.Operation == "attentions.create" {
		return nil
	} // Original opened decision owns this route.
	message := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, Author: "system", Body: "Attention " + a.AttentionID + " (" + a.Status + "): " + a.Title, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return err
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: message, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","attention":true}`, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, message, "attention", a.AttentionID, "notice"); err != nil {
		return err
	}
	if a.Status == "open" || a.IssueID == "" || a.Kind == "decision" && a.Status == "withdrawn" {
		if x.Principal.Kind == protocol.PrincipalMainAgent {
			return nil
		}
		return appendBackgroundSource(ctx, x, tx, "attention:"+a.AttentionID, "", message, eventID, e.Kind, nil)
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), a.IssueID)
	if err != nil {
		return err
	}
	if current.Assignee.Kind == protocol.IssueAssigneeAgent {
		return appendBackgroundSource(ctx, x, tx, "work_item:"+a.IssueID, a.IssueID, message, eventID, e.Kind, nil)
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		return nil
	}
	return appendBackgroundSource(ctx, x, tx, "attention:"+a.AttentionID, "", message, eventID, e.Kind, nil)
}

// redirectPendingIssueInputsToOwner transfers only never-claimed input when the
// concrete responsibility becomes human. Original source facts remain history;
// frozen/sending/unknown deliveries keep their existing reconciliation path.
func redirectPendingIssueInputsToOwner(ctx context.Context, x *Exec, tx *store.Tx, issue string) error {
	// Reversible status notices belong only to the Issue manager, never the
	// personal planner. Keep their pending group available for supersession;
	// canonical manager admission already waits while responsibility is human.
	messages, err := store.FollowupIDs(ctx, tx.Q(), `SELECT b.message_id FROM background_batches b WHERE b.work_item_id=? AND b.group_key<>? AND b.status='pending' AND EXISTS(SELECT 1 FROM outbox o WHERE json_extract(o.payload_json,'$.message_id')=b.message_id AND o.status='pending') AND NOT EXISTS(SELECT 1 FROM outbox o WHERE json_extract(o.payload_json,'$.message_id')=b.message_id AND o.status IN ('sending','unknown')) AND NOT EXISTS(SELECT 1 FROM main_turns t WHERE t.message_id=b.message_id) ORDER BY b.batch_id`, issue, "issue_status:"+issue)
	if err != nil {
		return err
	}
	for _, message := range messages {
		for after := int64(0); ; {
			page, err := store.BackgroundSources(ctx, tx.Q(), message, after)
			if err != nil {
				return err
			}
			for _, source := range page.Sources {
				if source.CancelledAt != 0 {
					continue
				}
				if err := appendBackgroundSource(ctx, x, tx, "owner-issue:"+issue, "", source.MessageID, source.EventID, source.Kind, source.Data); err != nil {
					return err
				}
			}
			if !page.More {
				break
			}
			after = page.Next
		}
		if _, err := tx.Exec(ctx, `UPDATE background_batches SET status='redirected' WHERE message_id=? AND status='pending'`, message); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='cancelled',updated_at=? WHERE status='pending' AND destination LIKE 'background_thread:%' AND json_extract(payload_json,'$.message_id')=?`, x.Now.UnixMilli(), message); err != nil {
			return err
		}
	}
	return nil
}
