package views

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// WorkItemContext returns a coherent context revision and a bounded source page.
func WorkItemContext(ctx context.Context, q store.Querier, id string, after int64) (protocol.WorkItemContext, error) {
	v := protocol.WorkItemContext{WorkItemID: id, Facts: []protocol.ContextFact{}, Sources: []protocol.ConversationItem{}}
	w, err := store.GetWorkItem(ctx, q, id)
	if err != nil {
		return v, err
	}
	var baseline int64
	if err := q.QueryRowContext(ctx, `SELECT revision,baseline_revision,coverage,gaps FROM work_item_contexts WHERE work_item_id=?`, id).Scan(&v.Revision, &baseline, &v.Coverage, &v.Gaps); err != nil {
		return v, err
	}
	v.Prepared = baseline > 0
	v.SyncStatus = "synced"
	if baseline == 0 {
		v.SyncStatus = "initializing"
	}
	ready, err := store.WorkItemContextReady(ctx, q, id)
	if err != nil {
		return v, err
	}
	if baseline > 0 && !ready {
		v.SyncStatus = "needs_review"
	}
	var recovering bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_contexts lc JOIN logical_turns l ON l.logical_turn_id=lc.logical_turn_id WHERE lc.work_item_id=? AND l.status='recovering')`, id).Scan(&recovering); err != nil {
		return v, err
	}
	if recovering {
		v.SyncStatus = "recovering"
	}
	// A later reviewed message cannot hide an earlier unreviewed source.
	if err := q.QueryRowContext(ctx, `WITH sources AS (SELECT DISTINCT m.seq,d.content_revision,d.facts_revision,d.disposition FROM conversation_messages m JOIN (SELECT message_id FROM message_work_items WHERE work_item_id=? AND active=1 UNION SELECT message_id FROM message_context_obligations WHERE work_item_id=? AND needs_review=1) a ON a.message_id=m.message_id LEFT JOIN message_dispositions d ON d.message_id=m.message_id) SELECT COALESCE(MAX(seq),0) FROM sources WHERE seq<COALESCE((SELECT MIN(seq) FROM sources WHERE disposition IS NULL OR disposition='pending' OR facts_revision<>content_revision),9223372036854775807)`, id, id).Scan(&v.Watermark); err != nil {
		return v, err
	}

	rows, err := q.QueryContext(ctx, `SELECT fact_id,COALESCE(work_item_id,''),scope,project,kind,body,source_message_id,attribution,COALESCE(supersedes,''),needs_review FROM work_item_context_facts WHERE superseded=0 AND (work_item_id=? OR scope='global' OR (scope='project' AND project=?)) ORDER BY created_at,fact_id`, id, w.Project)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var f protocol.ContextFact
		if err = rows.Scan(&f.ID, &f.WorkItemID, &f.Scope, &f.Project, &f.Kind, &f.Body, &f.SourceMessageID, &f.Attribution, &f.Supersedes, &f.NeedsReview); err != nil {
			rows.Close()
			return v, err
		}
		v.Facts = append(v.Facts, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	rows, err = q.QueryContext(ctx, `SELECT m.message_id FROM conversation_messages m JOIN message_work_items a ON a.message_id=m.message_id WHERE a.work_item_id=? AND a.active=1 AND m.seq>? ORDER BY m.seq LIMIT 51`, id, after)
	if err != nil {
		return v, err
	}
	var ids []string
	for rows.Next() {
		var mid string
		if err = rows.Scan(&mid); err != nil {
			rows.Close()
			return v, err
		}
		ids = append(ids, mid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	if len(ids) > 50 {
		ids = ids[:50]
		v.More = true
	}
	for _, mid := range ids {
		r, err := store.GetConversationItem(ctx, q, mid)
		if err != nil {
			return v, err
		}
		it, err := conversationItem(ctx, q, r)
		if err != nil {
			return v, err
		}
		if err = store.HideUndeliveredOwner(ctx, q, &it); err != nil {
			return v, err
		}
		v.Sources = append(v.Sources, it)
		v.Next = it.Seq
	}
	return v, nil
}
