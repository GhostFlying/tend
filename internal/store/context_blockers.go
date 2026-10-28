package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ContextBlockers names exact unresolved objects; callers never need to infer
// a review obligation from the fact that a coordinator is still running.
func ContextBlockers(ctx context.Context, q Querier, item string) ([]protocol.ContextBlocker, error) {
	rows, err := q.QueryContext(ctx, `SELECT 'initializing',work_item_id,revision,'initial context baseline is missing' FROM work_item_contexts WHERE work_item_id=? AND baseline_revision=0
 UNION ALL SELECT 'source_review',m.message_id,COALESCE(d.content_revision,1),'source content or classification requires review' FROM conversation_messages m LEFT JOIN message_dispositions d ON d.message_id=m.message_id WHERE m.conversation_id='main' AND EXISTS(SELECT 1 FROM message_work_items a WHERE a.message_id=m.message_id AND a.work_item_id=? AND a.active=1) AND (d.message_id IS NULL OR d.disposition='pending' OR d.facts_revision<>d.content_revision)
 UNION ALL SELECT 'source_correction',message_id,0,'source association or content changed' FROM message_context_obligations WHERE work_item_id=? AND needs_review=1
 UNION ALL SELECT 'fact_review',f.fact_id,0,'effective fact was invalidated by a source correction' FROM work_item_context_facts f JOIN work_items w ON w.work_item_id=? WHERE f.superseded=0 AND f.needs_review=1 AND (f.work_item_id=w.work_item_id OR f.scope='global' OR (f.scope='project' AND f.project=w.project))
 UNION ALL SELECT CASE WHEN l.status='processing' THEN 'context_revision' ELSE 'logical_turn_state' END,l.logical_turn_id,c.revision,l.status FROM logical_turn_contexts lc JOIN logical_turns l USING(logical_turn_id) JOIN work_item_contexts c ON c.work_item_id=lc.work_item_id WHERE lc.work_item_id=? AND l.status<>'completed' AND (l.status<>'processing' OR NOT EXISTS(SELECT 1 FROM logical_turn_context_reviews r WHERE r.logical_turn_id=l.logical_turn_id AND r.work_item_id=c.work_item_id AND r.revision=c.revision))
 UNION ALL SELECT 'legacy_message_review',lm.message_id,COALESCE(d.content_revision,1),'formal text in this logical turn requires classification and fact review' FROM logical_turn_contexts lc JOIN logical_turns l USING(logical_turn_id) JOIN logical_turn_messages lm USING(logical_turn_id) LEFT JOIN message_dispositions d ON d.message_id=lm.message_id WHERE lc.work_item_id=? AND l.status<>'completed' AND (d.message_id IS NULL OR d.disposition='pending' OR d.classified_revision<>d.content_revision OR d.facts_revision<>d.content_revision)
 UNION ALL SELECT 'native_receipt',p.request_id,0,'native execution outcome requires reconciliation' FROM native_turn_receipts_pending p JOIN logical_turn_executions e ON e.request_id=p.request_id JOIN logical_turn_contexts lc USING(logical_turn_id) WHERE lc.work_item_id=?`, item, item, item, item, item, item, item)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ContextBlocker{}
	for rows.Next() {
		var b protocol.ContextBlocker
		if err := rows.Scan(&b.Code, &b.ObjectID, &b.Revision, &b.Message); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
