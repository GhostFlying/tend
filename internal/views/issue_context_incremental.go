package views

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Each delta page examines at most this many existing events. Unrelated traffic
// advances the read cursor without loading its bodies or the Issue's history.
const issueContextEventWindow = 1000

type issueContextCursor struct {
	Version          int    `json:"v"`
	Issue            string `json:"i"`
	Scope            string `json:"s"`
	Kind             string `json:"k"`
	From             int64  `json:"f"`
	Through          int64  `json:"t"`
	ScanThrough      int64  `json:"b"`
	After            string `json:"a,omitempty"`
	MessageThrough   int64  `json:"m,omitempty"`
	FactThrough      int64  `json:"r,omitempty"`
	TaskThrough      int64  `json:"l,omitempty"`
	DutyThrough      int64  `json:"d,omitempty"`
	ReviewThrough    int64  `json:"w,omitempty"`
	AttentionThrough int64  `json:"n,omitempty"`
}

func contextDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func contextCursor(c issueContextCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ReadCanonicalIssueContext keeps the legacy default and opt-in incremental
// projection on one query/validation path for the API, CLI and managed tools.
func ReadCanonicalIssueContext(ctx context.Context, q store.Querier, id string, query protocol.IssueContextQuery) (any, error) {
	query, err := protocol.NormalizeIssueContextQuery(query)
	if err != nil {
		return nil, err
	}
	if query.View == "incremental" {
		return CanonicalIssueContextIncremental(ctx, q, id, query)
	}
	return CanonicalIssueContext(ctx, q, id, *query.AfterSource)
}

// CanonicalIssueContextIncremental pins event membership, not historical values.
// An object edited during pagination may show its newer current version. Its
// event is still returned in the following interval, so no change is consumed
// merely by reading ahead. No source review or acknowledgement is written.
func CanonicalIssueContextIncremental(ctx context.Context, q store.Querier, id string, query protocol.IssueContextQuery) (protocol.IssueContextIncremental, error) {
	out := protocol.IssueContextIncremental{IssueID: id, Items: []protocol.IssueContextChange{}}
	query.View = "incremental"
	query, err := protocol.NormalizeIssueContextQuery(query)
	if err != nil {
		return out, err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, err
	}
	// Current review CAS is independent from the event-membership cutoff.
	if err := q.QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, id).Scan(&out.ContextRevision); err != nil {
		return out, err
	}

	ids, err := store.FollowupIDs(ctx, q, `WITH RECURSIVE ancestors(id) AS (SELECT ? UNION SELECT parent_issue_id FROM issue_controls JOIN ancestors ON issue_id=ancestors.id WHERE parent_issue_id IS NOT NULL) SELECT id FROM ancestors ORDER BY id`, id)
	if err != nil {
		return out, err
	}
	scope := contextDigest(struct {
		IDs     []string
		Project string
	}{ids, issue.Plan.Project})
	rawIDs, _ := json.Marshal(ids)
	latest, err := store.LatestEventSeq(ctx, q)
	if err != nil {
		return out, err
	}
	c := issueContextCursor{Version: 1, Issue: id, Scope: scope, Kind: "snapshot", Through: latest}
	if query.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(query.Cursor)
		if e != nil || len(raw) > 4096 || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Issue != id || c.Scope != scope || c.From < 0 || c.Through < c.From || c.Through > latest || c.ScanThrough < c.From || c.ScanThrough > c.Through || (c.Kind != "snapshot" && c.Kind != "delta") {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "context cursor is unavailable or scope changed; start a new incremental snapshot without cursor")
		}
		position := c.From
		if c.Kind == "snapshot" {
			position = c.Through
		}
		expired, err := store.RefreshCursorExpired(ctx, q, position)
		if err != nil {
			return out, err
		}
		if expired {
			return out, protocol.Errorf(protocol.CodeStaleRevision, "refresh context cursor expired; start a new incremental snapshot without cursor to reload authoritative records")
		}

		var retained bool
		// A boundary can be a current refresh position, or the last coalesced
		// position already consumed by this reader. Older positions require a
		// snapshot; a deleted business boundary still fails this check.
		if err := q.QueryRowContext(ctx, `WITH positions(seq) AS (
 SELECT event_seq FROM events WHERE event_seq IN (?,?)
 UNION ALL SELECT event_seq FROM refresh_notifications WHERE event_seq IN (?,?)
 UNION ALL SELECT expired_through FROM refresh_stream_state)
 SELECT (?=0 OR EXISTS(SELECT 1 FROM positions WHERE seq=?)) AND (?=0 OR EXISTS(SELECT 1 FROM positions WHERE seq=?))`, c.From, c.Through, c.From, c.Through, c.From, c.From, c.Through, c.Through).Scan(&retained); err != nil {
			return out, err
		}
		if !retained {
			return out, protocol.Errorf(protocol.CodeInvalidRequest, "context event history unavailable; start a new incremental snapshot without cursor")
		}
	} else {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM conversation_messages`).Scan(&c.MessageThrough); err != nil {
			return out, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM work_item_context_facts`).Scan(&c.FactThrough); err != nil {
			return out, err
		}
		for _, bound := range []struct {
			table string
			value *int64
		}{{"tasks", &c.TaskThrough}, {"issue_delivery_duties", &c.DutyThrough}, {"shared_delivery_review_links", &c.ReviewThrough}, {"attentions", &c.AttentionThrough}} {
			if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0) FROM "+bound.table).Scan(bound.value); err != nil {
				return out, err
			}
		}

	}
	if c.Kind == "delta" && c.From == c.Through && c.After == "" {
		c.Through = latest
		c.ScanThrough = c.From
	}
	out.Kind, out.From, out.Through = c.Kind, c.From, c.Through
	type entry struct {
		key, kind, id string
		seq           int64
	}
	var entries []entry
	if c.Kind == "snapshot" {
		rows, err := q.QueryContext(ctx, `WITH p AS (SELECT ? issue,? scope,? project,? messages,? facts,? tasks,? duties,? reviews,? attentions), objects(key,kind,id) AS (
 SELECT '0/'||value,'working',value FROM p,json_each(p.scope)
 UNION ALL SELECT '1/'||f.fact_id,'fact',f.fact_id FROM work_item_context_facts f,p WHERE f.superseded=0 AND f.rowid<=p.facts AND (f.work_item_id IN (SELECT value FROM json_each(p.scope)) OR f.scope='global' OR (f.scope='project' AND f.project<>'' AND f.project=p.project))
 UNION ALL SELECT '2/'||m.message_id,'source',m.message_id FROM conversation_messages m,p WHERE m.seq<=p.messages AND (EXISTS(SELECT 1 FROM message_work_items a WHERE a.message_id=m.message_id AND a.work_item_id=p.issue AND a.active=1) OR EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.message_id=m.message_id AND o.work_item_id=p.issue AND o.needs_review=1))
 UNION SELECT '3/'||t.task_id,'task',t.task_id FROM tasks t,p WHERE t.kind='execution' AND t.rowid<=p.tasks AND (EXISTS(SELECT 1 FROM task_work_item_owners o WHERE o.task_id=t.task_id AND o.work_item_id=p.issue) OR EXISTS(SELECT 1 FROM task_execution_contracts c WHERE c.task_id=t.task_id AND c.review_issue_id=p.issue) OR EXISTS(SELECT 1 FROM run_issue_responsibilities z JOIN runs r USING(run_id) WHERE r.task_id=t.task_id AND z.issue_id=p.issue))
 UNION SELECT '4/'||task_id,'delivery',task_id FROM issue_delivery_duties d,p WHERE issue_id=p.issue AND d.rowid<=p.duties
 UNION SELECT '5/'||review_id,'review',review_id FROM shared_delivery_review_links l,p WHERE issue_id=p.issue AND l.rowid<=p.reviews
 UNION SELECT '7/'||json_extract(j.value,'$.delivery_id'),'observation',json_extract(j.value,'$.delivery_id') FROM work_items w,json_each(w.body_json,'$.deliveries') j,p WHERE w.work_item_id=p.issue
 UNION SELECT '6/'||a.attention_id,'attention',a.attention_id FROM attentions a LEFT JOIN attention_responsibilities r USING(attention_id),p WHERE a.rowid<=p.attentions AND a.status='open' AND (a.issue_id=p.issue OR r.issue_id=p.issue))
 SELECT key,kind,id FROM objects WHERE key>? ORDER BY key LIMIT ?`, id, string(rawIDs), issue.Plan.Project, c.MessageThrough, c.FactThrough, c.TaskThrough, c.DutyThrough, c.ReviewThrough, c.AttentionThrough, c.After, query.Limit+1)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.key, &e.kind, &e.id); err != nil {
				rows.Close()
				return out, err
			}
			entries = append(entries, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	} else {
		if c.ScanThrough == c.From {
			if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq),?) FROM (
 SELECT event_seq FROM (SELECT event_seq FROM events WHERE event_seq>? AND event_seq<=?
 UNION ALL SELECT event_seq FROM refresh_notifications WHERE event_seq>? AND event_seq<=?)
 ORDER BY event_seq LIMIT ?)`, c.From, c.From, c.Through, c.From, c.Through, issueContextEventWindow).Scan(&c.ScanThrough); err != nil {
				return out, err
			}
			// Deleted/archived event intervals must not silently look synchronized.
			if c.ScanThrough == c.From && c.From < c.Through {
				return out, protocol.Errorf(protocol.CodeInvalidRequest, "context event history unavailable; start a new incremental snapshot without cursor")
			}
		}
		// Membership is based on persisted events and retained source links, including
		// inactive links. New fact writes identify exact changed facts. Historical events without
		// that detail conservatively refresh their effective scope;
		// facts are separate items, never repeated beside every source page.
		rows, err := q.QueryContext(ctx, `WITH p AS (SELECT ? issue,? scope,? project), changed AS (
 SELECT event_seq,object_type,object_id,kind,body_json FROM events WHERE event_seq>? AND event_seq<=?
 ), fact_refs AS (
 SELECT j.value fact_id,e.event_seq FROM changed e,json_each(e.body_json,'$.changed_fact_ids') j
 UNION ALL SELECT f.fact_id,e.event_seq FROM changed e JOIN work_item_context_facts f ON e.object_type='message' AND e.object_id=f.source_message_id
 UNION ALL SELECT f.fact_id,e.event_seq FROM changed e,p JOIN work_item_context_facts f ON e.kind='work_item.context_changed' AND json_type(e.body_json,'$.changed_fact_ids') IS NULL AND (e.object_id IN (SELECT value FROM json_each(p.scope)) OR f.scope IN ('project','global'))
 ), fact_changes AS (
 SELECT f.fact_id,MAX(r.event_seq) seq FROM fact_refs r JOIN work_item_context_facts f ON f.fact_id=r.fact_id,p
 WHERE f.work_item_id IN (SELECT value FROM json_each(p.scope)) OR f.scope='global' OR (f.scope='project' AND f.project<>'' AND f.project=p.project) GROUP BY f.fact_id
 ), task_members AS (
 SELECT o.task_id FROM task_work_item_owners o,p WHERE o.work_item_id=p.issue
 UNION SELECT c.task_id FROM task_execution_contracts c,p WHERE c.review_issue_id=p.issue
 UNION SELECT r.task_id FROM run_issue_responsibilities z JOIN runs r USING(run_id),p WHERE z.issue_id=p.issue
 ), task_changes AS (
 SELECT l.task_id,MAX(e.event_seq) seq FROM task_members l JOIN changed e ON
 (e.object_type='task' AND e.object_id=l.task_id)
 OR (e.object_type='run' AND EXISTS(SELECT 1 FROM runs r WHERE r.run_id=e.object_id AND r.task_id=l.task_id))
 GROUP BY l.task_id
 ), objects(key,kind,id,seq) AS (
 SELECT '0/'||e.object_id,'working',e.object_id,MAX(e.event_seq) FROM changed e,p WHERE e.object_type IN ('issue','work_item') AND e.object_id IN (SELECT value FROM json_each(p.scope)) GROUP BY e.object_id
 UNION ALL SELECT '1/'||fact_id,'fact',fact_id,seq FROM fact_changes
 UNION ALL SELECT '2/'||e.object_id,'source',e.object_id,e.event_seq FROM changed e,p WHERE e.object_type='message' AND (EXISTS(SELECT 1 FROM message_work_items a WHERE a.message_id=e.object_id AND a.work_item_id=p.issue) OR EXISTS(SELECT 1 FROM message_context_obligations o WHERE o.message_id=e.object_id AND o.work_item_id=p.issue))
 UNION ALL SELECT '2/'||json_extract(j.value,'$.message_id'),'source',json_extract(j.value,'$.message_id'),e.event_seq FROM changed e,json_each(e.body_json,'$.sources') j,p WHERE e.object_type='issue' AND e.object_id=p.issue AND e.kind IN ('issue.context_reviewed','issue.context_review_progress')
 UNION ALL SELECT '2/'||j.value,'source',j.value,e.event_seq FROM changed e,json_each(e.body_json,'$.changed_source_ids') j,p WHERE e.object_id=p.issue AND e.object_type IN ('issue','work_item')
 UNION ALL SELECT '3/'||task_id,'task',task_id,seq FROM task_changes
 UNION ALL SELECT '4/'||d.task_id,'delivery',d.task_id,MAX(e.event_seq) FROM issue_delivery_duties d,p JOIN changed e ON (e.object_type='issue' AND e.object_id=p.issue AND e.kind='issue.delivery_updated' AND (json_type(e.body_json,'$.source_task_id') IS NULL OR json_extract(e.body_json,'$.source_task_id')=d.task_id)) WHERE d.issue_id=p.issue GROUP BY d.task_id
 UNION ALL SELECT '5/'||json_extract(e.body_json,'$.review_id'),'review',json_extract(e.body_json,'$.review_id'),MAX(e.event_seq) FROM changed e,p WHERE e.object_type='issue' AND e.object_id=p.issue AND e.kind='issue.delivery_review_changed' AND COALESCE(json_extract(e.body_json,'$.review_id'),'')<>'' GROUP BY json_extract(e.body_json,'$.review_id')
 UNION ALL SELECT '6/'||a.attention_id,'attention',a.attention_id,MAX(e.event_seq) FROM attentions a LEFT JOIN attention_responsibilities ar USING(attention_id),p JOIN changed e ON e.object_type='attention' AND e.object_id=a.attention_id WHERE a.issue_id=p.issue OR ar.issue_id=p.issue GROUP BY a.attention_id
 UNION ALL SELECT '7/'||json_extract(e.body_json,'$.delivery_id'),'observation',json_extract(e.body_json,'$.delivery_id'),MAX(e.event_seq) FROM changed e,p WHERE e.kind='work_item.delivery_changed' AND e.object_id=p.issue AND json_extract(e.body_json,'$.delivery_id') IS NOT NULL GROUP BY json_extract(e.body_json,'$.delivery_id')
 ) SELECT key,kind,id,MAX(seq) FROM objects WHERE key>? GROUP BY key,kind,id ORDER BY key LIMIT ?`, id, string(rawIDs), issue.Plan.Project, c.From, c.ScanThrough, c.After, query.Limit+1)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.key, &e.kind, &e.id, &e.seq); err != nil {
				rows.Close()
				return out, err
			}
			entries = append(entries, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	pageMore := len(entries) > query.Limit
	if pageMore {
		entries = entries[:query.Limit]
	}
	// Queue state and its reason use the same current scheduling facts as
	// the ordinary Task view, loaded once only when this page contains Tasks.
	var execution *executionFacts
	for _, e := range entries {
		item := protocol.IssueContextChange{Kind: e.kind, ID: e.id, EventSeq: e.seq}
		switch e.kind {
		case "working":
			working, err := issueContextWorking(ctx, q, e.id)
			if err != nil {
				return out, err
			}
			item.Working = &working
			item.Revision = working.Issue.Revision
		case "task":
			t, err := store.GetTask(ctx, q, e.id)
			if err != nil {
				return out, err
			}
			item.Task = &protocol.IssueTaskSummary{TaskID: t.TaskID, Title: t.Title, Lifecycle: t.Lifecycle, ControlMode: t.ControlMode, ControlRevision: t.ControlRevision, SpecVersion: t.SpecVersion, Rating: t.Rating, BlockReason: t.BlockReason}
			if execution == nil {
				facts, e := readExecutionFacts(ctx, q)
				if e != nil {
					return out, e
				}
				execution = &facts
			}
			item.Task.Execution, err = execution.task(ctx, q, t)
			if err != nil {
				return out, err
			}
			item.Revision = t.ControlRevision
			item.State = string(t.Lifecycle)
		case "delivery":
			d, err := store.GetIssueDeliveryDuty(ctx, q, id, e.id)
			if err != nil {
				return out, err
			}
			candidate := d.Candidate
			if candidate != nil {
				v := *candidate
				v.Validation = ""
				v.DeploymentScope = ""
				candidate = &v
			}
			item.Delivery = &protocol.IssueContextDelivery{DutyID: d.EventID, Revision: d.Revision, Status: d.Status, SourceTaskID: d.TaskID, ExecutionTaskID: d.ExecutionTaskID, Candidate: candidate}
			item.Revision = d.Revision
			item.State = d.Status
		case "review":
			r := protocol.IssueContextReviewRef{ReviewID: e.id}
			var linked bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM shared_delivery_review_links WHERE review_id=? AND issue_id=?)`, e.id, id).Scan(&linked); err != nil {
				return out, err
			}
			if !linked {
				// The original Issue's unlink event is retained even after
				// its last current link disappears. Do not read the now
				// inaccessible review's current details from another Issue.
				if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(body_json,'$.review_revision'),0) FROM events WHERE event_seq=?`, e.seq).Scan(&r.Revision); err != nil {
					return out, err
				}
				r.Status = "unlinked"
				item.Review = &r
				item.Revision = r.Revision
				item.State = r.Status
				item.Removed = true
				break
			}
			if err := q.QueryRowContext(ctx, `SELECT revision,json_extract(body_json,'$.status') FROM shared_delivery_reviews WHERE review_id=?`, e.id).Scan(&r.Revision, &r.Status); err != nil {
				return out, err
			}
			item.Review = &r
			item.Revision = r.Revision
			item.State = r.Status
		case "attention":
			a, err := Attention(ctx, q, e.id)
			if err != nil {
				return out, err
			}
			item.Attention = &a
			item.Revision = a.Revision
			item.State = a.Status

		case "observation":
			delivery := protocol.IssueDeliveryView{}
			found := false
			for _, d := range issue.Plan.Deliveries {
				if d.DeliveryID == e.id {
					delivery.Delivery = d
					found = true
					break
				}
			}
			if !found {
				item.Removed = true
				break
			}
			delivery.Observation = protocol.IssueDeliveryObservation{IssueID: id, DeliveryID: e.id, DeliverySnapshot: protocol.DeliverySnapshot{State: "unknown"}}
			row, err := store.GetDeliveryObservation(ctx, q, id, e.id)
			if err == nil {
				delivery.Observation = protocol.CanonicalDeliveryObservation(row.Observation)
			} else if !errors.Is(err, store.ErrNotFound) {
				return out, err
			}
			item.Observation = &delivery
			item.State = delivery.Observation.State
			item.Revision = delivery.Observation.Generation
		case "fact":
			f := protocol.IssueMaterialFact{}
			if err := q.QueryRowContext(ctx, `SELECT fact_id,COALESCE(work_item_id,''),scope,project,kind,body,source_message_id,attribution,COALESCE(supersedes,''),needs_review,superseded,created_at FROM work_item_context_facts WHERE fact_id=?`, e.id).Scan(&f.ID, &f.IssueID, &f.Scope, &f.Project, &f.Kind, &f.Body, &f.SourceMessageID, &f.Attribution, &f.Supersedes, &f.NeedsReview, &f.Superseded, &f.CreatedAt); err != nil {
				return out, err
			}
			if f.Scope == "work_item" {
				f.Scope = "issue"
			}
			item.Fact = &f
			item.Removed = f.Superseded
		case "source":
			source, err := issueContextSource(ctx, q, id, e.id)
			if err != nil {
				return out, err
			}
			item.Source = &source
			item.Revision = source.ContentRevision
			item.Removed = !source.Linked || source.Withdrawn
		}
		out.Items = append(out.Items, item)
	}
	if pageMore {
		c.After = entries[len(entries)-1].key
		out.More = true
	} else if c.Kind == "snapshot" {
		c.Kind = "delta"
		c.From = c.Through
		c.ScanThrough = c.From
		c.After = ""
		c.MessageThrough = 0
		c.FactThrough = 0
	} else {
		c.From = c.ScanThrough
		c.After = ""
		out.More = c.From < c.Through
	}
	out.NextCursor = contextCursor(c)
	out.ContentID = contextDigest(out)
	return out, nil
}

func issueContextSource(ctx context.Context, q store.Querier, issue, id string) (protocol.IssueContextSource, error) {
	out := protocol.IssueContextSource{MessageID: id}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_context_obligations WHERE work_item_id=? AND message_id=? AND needs_review=1)`, issue, id).Scan(&out.NeedsReview); err != nil {
		return out, err
	}
	var err error
	out.Admitted, err = store.OwnerInputAdmitted(ctx, q, id)
	if err != nil {
		return out, err
	}

	if err := q.QueryRowContext(ctx, `SELECT m.seq,m.author,COALESCE(d.content_revision,1),COALESCE(a.revision,0),COALESCE(c.withdrawn_at,0)<>0,COALESCE(d.disposition,''),EXISTS(SELECT 1 FROM message_work_items s WHERE s.message_id=m.message_id AND s.work_item_id=? AND s.active=1) FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id) LEFT JOIN message_association_revisions a USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE m.message_id=?`, issue, id).Scan(&out.Seq, &out.Author, &out.ContentRevision, &out.AssociationRevision, &out.Withdrawn, &out.Disposition, &out.Linked); err != nil {
		return out, err
	}
	// Unlinked/withdrawn sources remain discoverable as tombstones, without body
	// or media from a no-longer-readable association.
	if !out.Linked || out.Withdrawn {
		return out, nil
	}
	row, err := store.GetConversationItem(ctx, q, id)
	if err != nil {
		return out, err
	}
	item, err := conversationItem(ctx, q, row)
	if err != nil {
		return out, err
	}
	if err := store.HideUndeliveredOwner(ctx, q, &item); err != nil {
		return out, err
	}
	out.State = item.State
	out.Attachments = item.Attachments
	out.Images = item.Images
	return out, nil
}

func issueContextWorking(ctx context.Context, q store.Querier, id string) (protocol.IssueContextWorking, error) {
	out := protocol.IssueContextWorking{}
	var err error
	out.Issue, err = store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, err
	}
	out.Progress, err = store.IssueProgress(ctx, q, id, out.Issue.Revision)
	return out, err
}
