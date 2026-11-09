package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Usage incompleteness survives local retirement. A positively proved empty
// source contributes no expected usage, and cannot hide another pending source.
func usageCollectionTotals(ctx context.Context, q store.Querier, v *protocol.UsageCoverage) error {
	return q.QueryRowContext(ctx, `WITH sources AS (
 SELECT s.run_id,s.conversation_id,s.thread_id,s.provider,s.coverage,s.updated_at,COALESCE(json_extract(r.collection_json,'$.state'),'unobserved') state,COALESCE(json_extract(r.collection_json,'$.retirement_reason'),'') reason FROM token_usage_sources s LEFT JOIN token_usage_remote_collection r ON r.source_key=s.source_key WHERE NOT EXISTS(SELECT 1 FROM token_usage_local_sources l WHERE l.run_id=s.run_id AND l.conversation_id=s.conversation_id AND json_extract(l.source_json,'$.thread_id')=s.thread_id)
 UNION ALL SELECT run_id,conversation_id,COALESCE(json_extract(source_json,'$.thread_id'),source_key),COALESCE(json_extract(source_json,'$.provider'),''),coverage,updated_at,collection_state,COALESCE(json_extract(collection_json,'$.retirement_reason'),'') FROM token_usage_local_sources
 ), grouped AS (
 SELECT MAX(coverage<>'caught_up' AND reason<>'empty') pending,MAX(updated_at) updated_at,MAX(state='retired' AND reason='unavailable' AND coverage<>'caught_up') unavailable,MIN(state='retired' AND reason='empty') empty,MAX(state='needs_reconcile') needs_reconcile,MAX(state='unobserved') unobserved,MAX(state='dormant') dormant,MAX(state IN ('active','settling')) active FROM sources GROUP BY run_id,conversation_id,thread_id,provider
 ) SELECT COUNT(*),COALESCE(SUM(pending),0),MAX(updated_at),COALESCE(SUM(unavailable),0),COALESCE(SUM(empty),0),COALESCE(SUM(needs_reconcile),0),COALESCE(SUM(unobserved),0),COALESCE(SUM(dormant),0),COALESCE(SUM(active),0) FROM grouped`).Scan(&v.Sources, &v.Pending, &v.LastSyncedAt, &v.RetiredIncomplete, &v.Empty, &v.NeedsReconcile, &v.Unobserved, &v.Dormant, &v.Active)
}
