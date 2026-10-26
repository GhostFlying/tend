package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ReadCoordinationMetrics derives auditable counters from retained ledger rows.
// Depth/age describe pending deliveries, not the currently running turn.
func ReadCoordinationMetrics(ctx context.Context, q Querier) (protocol.CoordinationMetrics, error) {
	var p protocol.CoordinationMetrics
	err := q.QueryRowContext(ctx, `SELECT
 COALESCE(SUM(operation='main_thread.handle_message'),0),
 COALESCE(SUM(operation='main_thread.handle_event'),0),
 COALESCE(MIN(CASE WHEN operation='main_thread.handle_message' THEN created_at END),0),
 COALESCE(MIN(CASE WHEN operation='main_thread.handle_event' THEN created_at END),0)
 FROM outbox WHERE destination='main_thread' AND status='pending'`).Scan(&p.OwnerDepth, &p.BackgroundDepth, &p.OldestOwnerAt, &p.OldestBackgroundAt)
	if err != nil {
		return p, err
	}
	err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(MAX(0,f.created_at-CAST(json_extract(r.body_json,'$.next_at') AS INTEGER))),0) FROM followup_occurrences f JOIN followup_revisions r ON r.rule_id=f.rule_id AND r.revision=f.revision WHERE f.reason IN ('at','every','expired')`).Scan(&p.MaxDispatchDelayMS)
	if err != nil {
		return p, err
	}
	err = q.QueryRowContext(ctx, `SELECT
 (SELECT COALESCE(SUM(MAX(0,n-1)),0) FROM (SELECT COUNT(*) n FROM background_sources GROUP BY batch_id)),
 (SELECT COUNT(*) FROM background_batches WHERE status='completed' AND checkin_json<>''),
 (SELECT COUNT(*) FROM background_batches WHERE status='unknown')`).Scan(&p.CoalescedSources, &p.SilentChecks, &p.UnknownBatches)
	if err != nil {
		return p, err
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_records WHERE operation='conversation.withdraw_message' AND status='failed' AND error_code IN ('stale_revision','invalid_transition')`).Scan(&p.WithdrawalConflicts)
	return p, err
}
