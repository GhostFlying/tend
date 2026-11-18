package views

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func Deadlines(ctx context.Context, q store.Querier, issue string) ([]protocol.IssueDeadline, error) {
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM issue_deadlines WHERE (?='' OR issue_id=?) ORDER BY deadline_id`, issue, issue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.IssueDeadline{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var d protocol.IssueDeadline
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if err := projectDeadline(ctx, q, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

const (
	plannerAttentionJoinSQL = ` LEFT JOIN attentions a ON a.attention_id=json_extract(p.body_json,'$.attention_id')
 LEFT JOIN attention_responsibilities r ON r.attention_id=a.attention_id`
	// Informational links and unrouted records do not override the action's recipient.
	ownerPlannerRecipientSQL = `(CASE WHEN r.attention_id IS NOT NULL AND ` + actionableAttentionSQL + `
 THEN ` + ownerAttentionRecipientSQL + `
 ELSE json_extract(p.body_json,'$.responsible') IN ('owner', ?) END)`
)

func NextActions(ctx context.Context, q store.Querier, kind, issue string) (protocol.NextActionPage, error) {
	return OwnerNextActions(ctx, q, kind, issue, "")
}

func OwnerNextActions(ctx context.Context, q store.Querier, kind, issue, owner string) (protocol.NextActionPage, error) {
	out := protocol.NextActionPage{Items: []protocol.PlannerEntry{}, Deadlines: []protocol.IssueDeadline{}}
	query := `SELECT p.body_json FROM planner_entries p` + plannerAttentionJoinSQL + `
 WHERE (?='' OR p.kind=?) AND (?='' OR p.issue_id=?)
 AND (p.kind<>'user_action' OR COALESCE(r.recipient_kind,'owner')='owner')`
	args := []any{kind, kind, issue, issue}
	if owner != "" {
		query += ` AND (p.kind<>'user_action' OR ` + ownerPlannerRecipientSQL + `)`
		args = append(args, owner, owner)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY p.kind,p.rank=0,p.rank,p.entry_id`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return out, err
		}
		var v protocol.PlannerEntry
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for i := range out.Items {
		if err := projectPlannerEntry(ctx, q, &out.Items[i]); err != nil {
			return out, err
		}
	}
	out.Deadlines, err = Deadlines(ctx, q, issue)
	return out, err
}

func Deadline(ctx context.Context, q store.Querier, id string) (protocol.IssueDeadline, error) {
	d, err := store.GetIssueDeadline(ctx, q, id)
	if err == nil {
		err = projectDeadline(ctx, q, &d)
	}
	return d, err
}
func projectDeadline(ctx context.Context, q store.Querier, d *protocol.IssueDeadline) error {
	if d.DueAt != nil && d.EstimateSeconds != nil && *d.EstimateSeconds <= 253402300799 {
		latest := *d.DueAt - *d.EstimateSeconds*1000
		d.LatestStartAt = &latest
	}
	if d.SourceMessageID != "" {
		var rev int64
		var withdrawn bool
		err := q.QueryRowContext(ctx, `SELECT COALESCE(s.content_revision,1),COALESCE(c.withdrawn_at,0)<>0 FROM conversation_messages m LEFT JOIN message_dispositions s USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE m.message_id=?`, d.SourceMessageID).Scan(&rev, &withdrawn)
		if err != nil {
			return err
		}
		d.SourceNeedsReview = withdrawn || rev != d.SourceRevision
	}
	return nil
}

// PlannerEntry and NextActions project the same original Attention endpoint.
func PlannerEntry(ctx context.Context, q store.Querier, id string) (protocol.PlannerEntry, error) {
	v, err := store.GetPlannerEntry(ctx, q, id)
	if err == nil {
		err = projectPlannerEntry(ctx, q, &v)
	}
	return v, err
}
func projectPlannerEntry(ctx context.Context, q store.Querier, v *protocol.PlannerEntry) error {
	if v.AttentionID == "" {
		return nil
	}
	a, err := store.GetAttention(ctx, q, v.AttentionID)
	if err != nil {
		return err
	}
	if a.Status == "resolved" {
		v.Status = "done"
	} else if a.Status == "withdrawn" {
		v.Status = "cancelled"
	}
	return nil
}
