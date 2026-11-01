package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetFollowup(ctx context.Context, q Querier, id string) (protocol.FollowupRule, error) {
	var raw string
	var r protocol.FollowupRule
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM followup_rules WHERE rule_id=?`, id).Scan(&raw); err != nil {
		return r, notFound(err, "followup")
	}
	err := json.Unmarshal([]byte(raw), &r)
	return r, err
}
func ListFollowups(ctx context.Context, q Querier, item, after string) (protocol.FollowupPage, error) {
	out := protocol.FollowupPage{Items: []protocol.FollowupRule{}}
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM followup_rules WHERE work_item_id=? AND rule_id>? ORDER BY rule_id LIMIT 51`, item, after)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r protocol.FollowupRule
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return out, err
		}
		out.Items = append(out.Items, r)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > 50 {
		out.Items = out.Items[:50]
		out.More = true
		out.Next = out.Items[49].RuleID
	}
	rows.Close()
	out.Latest = map[string]protocol.FollowupOccurrence{}
	for _, rule := range out.Items {
		var o protocol.FollowupOccurrence
		var checkin string
		err := q.QueryRowContext(ctx, occurrenceSelect+`WHERE f.rule_id=? ORDER BY f.revision DESC LIMIT 1`, rule.RuleID).Scan(&o.OccurrenceID, &o.RuleID, &o.Revision, &o.WorkItemID, &o.MessageID, &o.Reason, &o.Status, &o.Evidence, &o.CreatedAt, &o.BackgroundMessageID, &checkin)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		if checkin != "" {
			if err := json.Unmarshal([]byte(checkin), &o.Checkin); err != nil {
				return out, err
			}
		}
		out.Latest[rule.RuleID] = o
	}
	out.EventSeq, err = LatestEventSeq(ctx, q)
	return out, err
}
func (t *Tx) SaveFollowup(ctx context.Context, r protocol.FollowupRule, expected int64, requestID string) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	due := r.NextAt
	if due == 0 || (r.ExpiresAt > 0 && r.ExpiresAt < due) {
		due = r.ExpiresAt
	}
	if expected == 0 {
		_, err = t.Exec(ctx, `INSERT INTO followup_rules(rule_id,work_item_id,revision,status,next_due,body_json,updated_at) VALUES(?,NULLIF(?,''),?,?,?,?,?)`, r.RuleID, r.WorkItemID, r.Revision, r.Status, due, string(raw), r.UpdatedAt)
	} else {
		res, e := t.Exec(ctx, `UPDATE followup_rules SET revision=?,status=?,next_due=?,body_json=?,updated_at=? WHERE rule_id=? AND revision=?`, r.Revision, r.Status, due, string(raw), r.UpdatedAt, r.RuleID, expected)
		err = e
		if err == nil {
			n, e := res.RowsAffected()
			err = e
			if n != 1 && err == nil {
				return protocol.Errorf(protocol.CodeStaleRevision, "followup changed")
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO followup_revisions(rule_id,revision,body_json,request_id) VALUES(?,?,?,?)`, r.RuleID, r.Revision, string(raw), requestID)
	return err
}
func (t *Tx) FollowupTarget(ctx context.Context, id, kind, target string) error {
	_, err := t.Exec(ctx, `INSERT OR IGNORE INTO followup_targets(rule_id,kind,target_id) VALUES(?,?,?)`, id, kind, target)
	return err
}
func FollowupIDs(ctx context.Context, q Querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

const occurrenceSelect = `SELECT f.occurrence_id,f.rule_id,f.revision,COALESCE(f.work_item_id,''),f.message_id,f.reason,
 CASE WHEN f.status='cancelled' THEN 'cancelled'
 WHEN f.status='queued' AND EXISTS(SELECT 1 FROM trigger_events e WHERE e.message_id=f.message_id AND e.status='pending')
 AND NOT EXISTS(SELECT 1 FROM background_sources s WHERE s.message_id=f.message_id)
 AND NOT EXISTS(SELECT 1 FROM main_turns t WHERE t.message_id=f.message_id)
 AND NOT EXISTS(SELECT 1 FROM outbox o WHERE json_extract(o.payload_json,'$.message_id')=f.message_id) THEN 'queued'
 ELSE COALESCE(
  (SELECT CASE WHEN o.status IN ('unknown','failed','cancelled') THEN o.status END FROM outbox o WHERE (o.destination='main_thread' OR o.destination LIKE 'background_thread:%') AND json_extract(o.payload_json,'$.message_id')=COALESCE((SELECT b.message_id FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.occurrence_id=f.occurrence_id ORDER BY s.seq DESC LIMIT 1),f.message_id) ORDER BY o.seq DESC LIMIT 1),
  (SELECT CASE t.status WHEN 'started' THEN 'processing' WHEN 'starting' THEN 'starting' WHEN 'rejected' THEN NULL ELSE t.status END FROM main_turns t WHERE t.message_id=COALESCE((SELECT b.message_id FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.occurrence_id=f.occurrence_id ORDER BY s.seq DESC LIMIT 1),f.message_id) ORDER BY t.created_at DESC LIMIT 1),
  (SELECT CASE o.status WHEN 'pending' THEN 'queued' WHEN 'sending' THEN 'starting' ELSE o.status END FROM outbox o WHERE (o.destination='main_thread' OR o.destination LIKE 'background_thread:%') AND json_extract(o.payload_json,'$.message_id')=COALESCE((SELECT b.message_id FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.occurrence_id=f.occurrence_id ORDER BY s.seq DESC LIMIT 1),f.message_id) ORDER BY o.seq DESC LIMIT 1),'unknown') END,
 f.evidence,f.created_at,
 COALESCE((SELECT b.message_id FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.occurrence_id=f.occurrence_id ORDER BY s.seq DESC LIMIT 1),''),
 COALESCE((SELECT CASE WHEN b.status='completed' AND f.status<>'cancelled' AND s.cancelled_at=0 THEN b.checkin_json ELSE '' END FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.occurrence_id=f.occurrence_id ORDER BY s.seq DESC LIMIT 1),'')
 FROM followup_occurrences f `

func FollowupOccurrences(ctx context.Context, q Querier, id, after string) ([]protocol.FollowupOccurrence, bool, error) {
	rows, err := q.QueryContext(ctx, occurrenceSelect+`WHERE f.rule_id=? AND f.occurrence_id>? ORDER BY f.occurrence_id LIMIT 51`, id, after)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []protocol.FollowupOccurrence{}
	for rows.Next() {
		var o protocol.FollowupOccurrence
		var checkin string
		if err := rows.Scan(&o.OccurrenceID, &o.RuleID, &o.Revision, &o.WorkItemID, &o.MessageID, &o.Reason, &o.Status, &o.Evidence, &o.CreatedAt, &o.BackgroundMessageID, &checkin); err != nil {
			return nil, false, err
		}
		if checkin != "" {
			if err := json.Unmarshal([]byte(checkin), &o.Checkin); err != nil {
				return nil, false, err
			}
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > 50
	if more {
		out = out[:50]
	}
	return out, more, nil
}
func FollowupHistory(ctx context.Context, q Querier, id, afterOccurrence string, afterRevision int64) (protocol.FollowupHistoryPage, error) {
	var p protocol.FollowupHistoryPage
	var err error
	p.Rule, err = GetFollowup(ctx, q, id)
	if err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM followup_revisions WHERE rule_id=? AND revision>? ORDER BY revision LIMIT 51`, id, afterRevision)
	if err != nil {
		return p, err
	}
	p.Revisions = []protocol.FollowupRule{}
	for rows.Next() {
		var raw string
		var r protocol.FollowupRule
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &r)
		}
		if err != nil {
			rows.Close()
			return p, err
		}
		p.Revisions = append(p.Revisions, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if len(p.Revisions) > 50 {
		p.More = true
		p.Revisions = p.Revisions[:50]
		p.NextRevision = p.Revisions[49].Revision
	}
	var more bool
	p.Occurrences, more, err = FollowupOccurrences(ctx, q, id, afterOccurrence)
	if more {
		p.More = true
		p.NextOccurrence = p.Occurrences[len(p.Occurrences)-1].OccurrenceID
	}
	if p.More {
		p.NextRevision = afterRevision
		if len(p.Revisions) > 0 {
			p.NextRevision = p.Revisions[len(p.Revisions)-1].Revision
		}
		p.NextOccurrence = afterOccurrence
		if len(p.Occurrences) > 0 {
			p.NextOccurrence = p.Occurrences[len(p.Occurrences)-1].OccurrenceID
		}
	}
	return p, err
}
func FollowupOverview(ctx context.Context, q Querier, item string) (protocol.FollowupSummary, error) {
	var s protocol.FollowupSummary
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='active'),0),COALESCE(SUM(status='paused'),0),COALESCE(MIN(CASE WHEN status='active' THEN next_due END),0) FROM followup_rules WHERE work_item_id=?`, item).Scan(&s.Active, &s.Paused, &s.NextAt)
	if err != nil {
		return s, err
	}
	if s.Active+s.Paused == 0 {
		return s, nil
	}
	err = q.QueryRowContext(ctx, `SELECT rule_id,json_extract(body_json,'$.purpose') FROM followup_rules WHERE work_item_id=? AND status IN ('active','paused') ORDER BY CASE WHEN status='active' THEN 0 ELSE 1 END,next_due,rule_id LIMIT 1`, item).Scan(&s.RuleID, &s.Purpose)
	return s, err
}
func (t *Tx) InsertFollowupOccurrence(ctx context.Context, o protocol.FollowupOccurrence, key string) error {
	_, err := t.Exec(ctx, `INSERT INTO followup_occurrences(occurrence_id,rule_id,revision,work_item_id,message_id,status,reason,evidence,created_at,occurrence_key) VALUES(?,?,?,NULLIF(?,''),?,?,?,?,?,?)`, o.OccurrenceID, o.RuleID, o.Revision, o.WorkItemID, o.MessageID, o.Status, o.Reason, o.Evidence, o.CreatedAt, key)
	if err != nil {
		return fmt.Errorf("store: followup occurrence: %w", err)
	}
	return nil
}
