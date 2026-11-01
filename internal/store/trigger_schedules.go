package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

const scheduleProjection = `WITH schedules AS (
 SELECT f.rule_id id,f.work_item_id issue_id,json_extract(w.body_json,'$.title') issue_title,json_extract(f.body_json,'$.purpose') title,'followup' source,json_extract(f.body_json,'$.kind') kind,
 CASE WHEN w.status='closed' THEN 'cancelled' WHEN w.status='snoozed' AND json_extract(f.body_json,'$.kind')<>'at' AND f.status='active' THEN 'paused' ELSE f.status END status,
 COALESCE(json_extract(f.body_json,'$.reason'),'') reason,COALESCE(json_extract(f.body_json,'$.next_at'),0) next_at,f.updated_at,f.revision,f.body_json rule
 FROM followup_rules f JOIN work_items w ON w.work_item_id=f.work_item_id
 UNION ALL
 SELECT 'observation:'||w.work_item_id||':'||json_extract(d.value,'$.delivery_id'),w.work_item_id,json_extract(w.body_json,'$.title'),json_extract(d.value,'$.repository')||' #'||json_extract(d.value,'$.number'),'observation','observation',
 CASE WHEN w.status='closed' OR json_extract(o.observation_json,'$.state') IN ('closed','merged') THEN 'finished' WHEN j.status='claimed' THEN 'processing' WHEN j.status='pending' THEN 'pending' ELSE 'active' END,
 COALESCE(json_extract(o.observation_json,'$.error_kind'),''),COALESCE(o.next_check_at,0),COALESCE(j.updated_at,0),COALESCE(o.generation,0),''
 FROM work_items w JOIN json_each(w.body_json,'$.deliveries') d LEFT JOIN delivery_observations o ON o.work_item_id=w.work_item_id AND o.delivery_id=json_extract(d.value,'$.delivery_id') LEFT JOIN observation_jobs j ON j.issue_id=w.work_item_id AND j.delivery_id=json_extract(d.value,'$.delivery_id')
 UNION ALL
 SELECT s.id,s.issue_id,json_extract(w.body_json,'$.title'),json_extract(s.body_json,'$.title'),'webhook','webhook',CASE WHEN w.status='closed' THEN 'cancelled' ELSE s.status END,'',0,s.updated_at,s.revision,'' FROM webhook_subscriptions s JOIN work_items w ON w.work_item_id=s.issue_id
) `

func TriggerSchedules(ctx context.Context, q Querier, f protocol.TriggerQuery, now int64) (protocol.TriggerSchedulePage, error) {
	return triggerSchedules(ctx, q, f, now, "")
}
func GetTriggerSchedule(ctx context.Context, q Querier, id string, now int64) (protocol.TriggerSchedule, error) {
	page, err := triggerSchedules(ctx, q, protocol.TriggerQuery{Limit: 1}, now, id)
	if err != nil {
		return protocol.TriggerSchedule{}, err
	}
	if len(page.Items) == 0 {
		return protocol.TriggerSchedule{}, ErrNotFound
	}
	return page.Items[0], nil
}
func triggerSchedules(ctx context.Context, q Querier, f protocol.TriggerQuery, now int64, id string) (protocol.TriggerSchedulePage, error) {
	out := protocol.TriggerSchedulePage{Items: []protocol.TriggerSchedule{}, DataAt: now}
	limit := triggerLimit(f.Limit)
	rows, err := q.QueryContext(ctx, scheduleProjection+`SELECT id,issue_id,issue_title,title,source,kind,status,reason,next_at,updated_at,revision,rule FROM schedules WHERE id>? AND (?='' OR id=?) AND (?='' OR issue_id=?) AND (?='' OR source=?) AND (?='' OR status=?) AND (?='' OR instr(lower(title||' '||issue_title||' '||issue_id||' '||id),lower(?))>0) ORDER BY id LIMIT ?`, f.After, id, id, f.IssueID, f.IssueID, f.Source, f.Source, f.Status, f.Status, f.Search, f.Search, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v protocol.TriggerSchedule
		var raw string
		if err = rows.Scan(&v.ID, &v.IssueID, &v.IssueTitle, &v.Title, &v.Source, &v.Kind, &v.Status, &v.Reason, &v.NextAt, &v.UpdatedAt, &v.Revision, &raw); err != nil {
			return out, err
		}
		if raw != "" {
			var r protocol.FollowupRule
			if err = json.Unmarshal([]byte(raw), &r); err != nil {
				return out, err
			}
			rule := protocol.CanonicalFollowupRule(r, "")
			v.Rule = &rule
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	if len(out.Items) > limit {
		out.More = true
		out.Items = out.Items[:limit]
		out.Next = out.Items[limit-1].ID
	}
	out.EventSeq, err = LatestEventSeq(ctx, q)
	return out, err
}
