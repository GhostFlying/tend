package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (t *Tx) InsertTrigger(ctx context.Context, e protocol.TriggerEvent) error {
	_, err := t.Exec(ctx, `INSERT INTO trigger_events(id,issue_id,schedule_id,title,source,status,reason,request_id,message_id,planned_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, e.ID, e.IssueID, e.ScheduleID, e.Title, e.Source, e.Status, e.Reason, e.RequestID, e.MessageID, e.PlannedAt, e.CreatedAt, e.UpdatedAt)
	if err != nil {
		return err
	}
	for _, s := range e.Steps {
		if err = t.TriggerStep(ctx, e.ID, s); err != nil {
			return err
		}
	}
	return nil
}
func (t *Tx) TriggerStep(ctx context.Context, id string, s protocol.TriggerStep) error {
	_, err := t.Exec(ctx, `INSERT INTO trigger_steps(event_id,kind,at,detail) VALUES(?,?,?,?)`, id, s.Kind, s.At, s.Detail)
	return err
}
func (t *Tx) FinishTrigger(ctx context.Context, id, status, reason string, now int64) error {
	if id == "" {
		return nil
	}
	if _, err := t.Exec(ctx, `UPDATE trigger_events SET status=?,reason=?,updated_at=? WHERE id=?`, status, reason, now, id); err != nil {
		return err
	}
	return t.TriggerStep(ctx, id, protocol.TriggerStep{Kind: status, At: now, Detail: reason})
}
func GetWebhookSubscription(ctx context.Context, q Querier, id string) (protocol.WebhookSubscription, error) {
	var v protocol.WebhookSubscription
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM webhook_subscriptions WHERE id=?`, id).Scan(&raw); err != nil {
		return v, notFound(err, "webhook subscription")
	}
	err := json.Unmarshal([]byte(raw), &v)
	return v, err
}
func (t *Tx) SaveWebhookSubscription(ctx context.Context, v protocol.WebhookSubscription, expected int64) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = t.Exec(ctx, `INSERT INTO webhook_subscriptions(id,issue_id,revision,status,body_json,updated_at) VALUES(?,?,?,?,?,?)`, v.ID, v.IssueID, v.Revision, v.Status, string(raw), v.UpdatedAt)
		return err
	}
	r, err := t.Exec(ctx, `UPDATE webhook_subscriptions SET revision=?,status=?,body_json=?,updated_at=? WHERE id=? AND revision=?`, v.Revision, v.Status, string(raw), v.UpdatedAt, v.ID, expected)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return protocol.Errorf(protocol.CodeStaleRevision, "subscription changed")
	}
	return err
}
func ListWebhookSubscriptions(ctx context.Context, q Querier, f protocol.TriggerQuery) (protocol.WebhookSubscriptionPage, error) {
	out := protocol.WebhookSubscriptionPage{Items: []protocol.WebhookSubscription{}}
	limit := triggerLimit(f.Limit)
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM webhook_subscriptions WHERE id>? AND (?='' OR issue_id=?) AND (?='' OR status=?) AND (?='' OR instr(lower(id||' '||issue_id||' '||json_extract(body_json,'$.title')),lower(?))>0) ORDER BY id LIMIT ?`, f.After, f.IssueID, f.IssueID, f.Status, f.Status, f.Search, f.Search, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var v protocol.WebhookSubscription
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if len(out.Items) > limit {
		out.More = true
		out.Items = out.Items[:limit]
		out.Next = out.Items[limit-1].ID
	}
	return out, rows.Err()
}
func triggerLimit(n int) int {
	if n < 1 || n > 100 {
		return 50
	}
	return n
}

const triggerStatus = `CASE WHEN s.cancelled_at>0 OR f.status='cancelled' THEN 'cancelled' WHEN b.status='pending' THEN 'queued' WHEN b.status='frozen' THEN 'processing' WHEN b.batch_id IS NULL AND f.status='queued' THEN 'pending' ELSE COALESCE(b.status,f.status,e.status) END`
const triggerSelect = `SELECT e.id,e.issue_id,COALESCE(json_extract(w.body_json,'$.title'),''),COALESCE(c.state,w.status),e.schedule_id,e.title,e.source,
 ` + triggerStatus + `,
 CASE WHEN b.checkin_json<>'' THEN COALESCE(json_extract(b.checkin_json,'$.note'),e.reason) ELSE e.reason END,
 e.request_id,e.message_id,COALESCE(b.batch_id,''),e.planned_at,e.created_at,e.updated_at,COALESCE(b.created_at,0),COALESCE(b.frozen_at,0)
 FROM trigger_events e JOIN work_items w ON w.work_item_id=e.issue_id LEFT JOIN issue_controls c ON c.issue_id=e.issue_id
 LEFT JOIN followup_occurrences f ON f.message_id=e.message_id
 LEFT JOIN background_sources s ON s.seq=(SELECT MAX(x.seq) FROM background_sources x JOIN background_batches y ON y.batch_id=x.batch_id WHERE x.message_id=e.message_id AND (y.work_item_id=e.issue_id OR y.work_item_id=''))
 LEFT JOIN background_batches b ON b.batch_id=s.batch_id `

func readTrigger(rows interface{ Scan(...any) error }) (protocol.TriggerEvent, int64, int64, error) {
	var e protocol.TriggerEvent
	var queued, frozen int64
	err := rows.Scan(&e.ID, &e.IssueID, &e.IssueTitle, &e.IssueState, &e.ScheduleID, &e.Title, &e.Source, &e.Status, &e.Reason, &e.RequestID, &e.MessageID, &e.BatchID, &e.PlannedAt, &e.CreatedAt, &e.UpdatedAt, &queued, &frozen)
	e.Steps = []protocol.TriggerStep{}
	return e, queued, frozen, err
}
func TriggerEvents(ctx context.Context, q Querier, f protocol.TriggerQuery, now int64) (protocol.TriggerEventPage, error) {
	out := protocol.TriggerEventPage{Items: []protocol.TriggerEvent{}, DataAt: now}
	limit := triggerLimit(f.Limit)
	var cursorAt int64
	var cursorID string
	if f.After != "" {
		b, e := base64.RawURLEncoding.DecodeString(f.After)
		if e != nil {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid event cursor")
		}
		parts := strings.SplitN(string(b), "|", 2)
		if len(parts) != 2 {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid event cursor")
		}
		cursorAt, e = strconv.ParseInt(parts[0], 10, 64)
		if e != nil {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid event cursor")
		}
		cursorID = parts[1]
	}
	rows, err := q.QueryContext(ctx, triggerSelect+` WHERE (?='' OR e.created_at<? OR (e.created_at=? AND e.id<?)) AND (?='' OR e.issue_id=?) AND (?='' OR e.schedule_id=?) AND (?='' OR e.source=?) AND (?='' OR `+triggerStatus+`=?) AND (?='' OR instr(lower(e.title||' '||e.id||' '||e.issue_id||' '||e.request_id||' '||e.schedule_id||' '||COALESCE(json_extract(w.body_json,'$.title'),'')),lower(?))>0) AND (?=0 OR e.created_at>=?) AND (?=0 OR e.created_at<=?) ORDER BY e.created_at DESC,e.id DESC LIMIT ?`, f.After, cursorAt, cursorAt, cursorID, f.IssueID, f.IssueID, f.ScheduleID, f.ScheduleID, f.Source, f.Source, f.Status, f.Status, f.Search, f.Search, f.Since, f.Since, f.Until, f.Until, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		e, _, _, err := readTrigger(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, e)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	if len(out.Items) > limit {
		out.More = true
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.Next = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", last.CreatedAt, last.ID)))
	}
	out.EventSeq, err = LatestEventSeq(ctx, q)
	return out, err
}
func GetTrigger(ctx context.Context, q Querier, id string) (protocol.TriggerEvent, error) {
	e, _, frozen, err := readTrigger(q.QueryRowContext(ctx, triggerSelect+` WHERE e.id=?`, id))
	if err != nil {
		return e, notFound(err, "trigger event")
	}
	rows, err := q.QueryContext(ctx, `SELECT kind,at,detail FROM trigger_steps WHERE event_id=? ORDER BY seq`, id)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var s protocol.TriggerStep
		if err = rows.Scan(&s.Kind, &s.At, &s.Detail); err != nil {
			return e, err
		}
		e.Steps = append(e.Steps, s)
	}
	if err = rows.Err(); err != nil {
		return e, err
	}
	rows.Close()
	if frozen > 0 {
		e.Steps = append(e.Steps, protocol.TriggerStep{Kind: "claimed", At: frozen, Detail: e.BatchID})
	}
	if e.BatchID != "" {
		var status string
		var at int64
		err = q.QueryRowContext(ctx, `SELECT t.status,t.updated_at FROM main_turns t JOIN background_batches b ON b.message_id=t.message_id WHERE b.batch_id=? ORDER BY t.updated_at DESC LIMIT 1`, e.BatchID).Scan(&status, &at)
		if err == nil {
			e.Steps = append(e.Steps, protocol.TriggerStep{Kind: status, At: at})
		} else if !errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
	}
	return e, nil
}
