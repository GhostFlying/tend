package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/store"
)

type boundedSummarySource struct {
	Instance          string `json:"instance"`
	ThreadID          string `json:"thread_id"`
	BindingGeneration int64  `json:"binding_generation"`
}

// A started legacy summary has no durable native fork identity. Freeze that
// source lineage after a forced predecessor exit, without changing its verdict.
func boundedSummarySources(ctx context.Context, q store.Querier) ([]boundedSummarySource, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT instance,source_thread_id,binding_generation FROM conversation_summary_attempts WHERE status='started' ORDER BY instance,source_thread_id,binding_generation`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []boundedSummarySource
	for rows.Next() {
		var source boundedSummarySource
		if err := rows.Scan(&source.Instance, &source.ThreadID, &source.BindingGeneration); err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	return result, rows.Err()
}

func boundedSummaryProtected(ctx context.Context, q store.Querier, snap store.HandoffSnapshot) (bool, error) {
	var protected bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events e,json_each(e.body_json,'$.summary_unknowns') s WHERE e.object_type='maintenance' AND e.kind='maintenance.bounded_force_registered' AND json_extract(s.value,'$.instance')=? AND json_extract(s.value,'$.thread_id')=? AND json_extract(s.value,'$.binding_generation')=?)`, snap.Instance, snap.ThreadID, snap.BindingGeneration).Scan(&protected)
	return protected, err
}
