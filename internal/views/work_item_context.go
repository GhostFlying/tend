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
