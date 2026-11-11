package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// RefreshUsageProjection drains migration work before opening server listeners.
// Ordinary app writes maintain the same projection before their own commit.
func (a *App) RefreshUsageProjection(ctx context.Context) error {
	return a.write(ctx, func(*store.Tx) error { return nil })
}

type usageSummaryKey struct {
	dimension int64
	bucket    int64
}

// A time bucket alone cannot bound retained unknown or future timestamps.
// Fact sequence partitions cap every summary at this many request identities.
const usageSummaryPartitionSize int64 = 128

const usageDirtyGroupsSQL = `SELECT DISTINCT p.dimension_key,p.bucket FROM token_usage_dirty d CROSS JOIN token_usage_contributions p USING(source,request_id)`

// Expand only the connected overlap component. Each recursive step starts from
// an affected legacy sequence and follows indexed native peer identities.
const expandUsageOverlapSQL = `WITH RECURSIVE affected(legacy_seq) AS (
 SELECT legacy_seq FROM token_usage_overlap_dirty
 UNION SELECT peer.legacy_seq FROM affected a
 CROSS JOIN token_usage_legacy_candidates edge ON edge.legacy_seq=a.legacy_seq
 CROSS JOIN token_usage_legacy_candidates peer ON peer.native_source=edge.native_source AND peer.native_request_id=edge.native_request_id
 UNION SELECT peer.legacy_seq FROM affected a
 CROSS JOIN token_usage_legacy_candidates edge ON edge.legacy_seq=a.legacy_seq
 CROSS JOIN token_usage_legacy_links peer ON peer.native_source=edge.native_source AND peer.native_request_id=edge.native_request_id
 UNION SELECT peer.legacy_seq FROM affected a
 CROSS JOIN token_usage_legacy_links edge ON edge.legacy_seq=a.legacy_seq
 CROSS JOIN token_usage_legacy_candidates peer ON peer.native_source=edge.native_source AND peer.native_request_id=edge.native_request_id
 UNION SELECT peer.legacy_seq FROM affected a
 CROSS JOIN token_usage_legacy_links edge ON edge.legacy_seq=a.legacy_seq
 CROSS JOIN token_usage_legacy_links peer ON peer.native_source=edge.native_source AND peer.native_request_id=edge.native_request_id
 ) INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT c.source,c.request_id FROM affected a CROSS JOIN token_usage_current c ON c.fact_seq=a.legacy_seq
 UNION SELECT e.native_source,e.native_request_id FROM affected a CROSS JOIN token_usage_legacy_candidates e ON e.legacy_seq=a.legacy_seq
 UNION SELECT e.native_source,e.native_request_id FROM affected a CROSS JOIN token_usage_legacy_links e ON e.legacy_seq=a.legacy_seq
 ) WHERE true ON CONFLICT(source,request_id) DO NOTHING`

func refreshUsageProjection(ctx context.Context, tx *store.Tx, changed ...func() error) error {
	var pending, overlap bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_dirty),EXISTS(SELECT 1 FROM token_usage_overlap_dirty)`).Scan(&pending, &overlap); err != nil {
		return err
	}
	if !pending && !overlap {
		return nil
	}
	if overlap {
		if _, err := tx.Exec(ctx, expandUsageOverlapSQL); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM token_usage_overlap_dirty`); err != nil {
			return err
		}
	}
	groups := map[usageSummaryKey]struct{}{}
	rows, err := tx.Q().QueryContext(ctx, usageDirtyGroupsSQL)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key usageSummaryKey
		if err = rows.Scan(&key.dimension, &key.bucket); err != nil {
			break
		}
		groups[key] = struct{}{}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	inputs, err := views.UsageProjectionInputs(ctx, tx.Q())
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM token_usage_contributions WHERE (source,request_id) IN (SELECT source,request_id FROM token_usage_dirty)`); err != nil {
		return err
	}
	for _, c := range inputs {
		r := c.Record
		dimension, err := internUsageDimension(ctx, tx, c.Dimension)
		if err != nil {
			return err
		}
		key := usageSummaryKey{dimension: dimension, bucket: -1}
		if _, err = tx.Exec(ctx, `INSERT INTO token_usage_partitions(dimension_id,partition) VALUES(?,?) ON CONFLICT DO NOTHING`, dimension, (r.Seq-1)/usageSummaryPartitionSize); err != nil {
			return err
		}
		if err = tx.Q().QueryRowContext(ctx, `SELECT dimension_key FROM token_usage_partitions WHERE dimension_id=? AND partition=?`, dimension, (r.Seq-1)/usageSummaryPartitionSize).Scan(&key.dimension); err != nil {
			return err
		}
		if r.OccurredAt != nil {
			key.bucket = *r.OccurredAt / views.UsageBucketWidth * views.UsageBucketWidth
		}
		var estimate, estimatedModel *string
		if r.Estimate != nil {
			estimate, estimatedModel = &r.Estimate.USD, &r.Estimate.ModelID
		}
		conversation := c.Dimension.BindingConversation
		if r.RecordID == "" && strings.HasPrefix(r.Subject, "conversation:") {
			conversation = strings.TrimPrefix(r.Subject, "conversation:")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO token_usage_contributions(source,request_id,fact_seq,thread_id,turn_id,root_turn_id,parent_thread_id,session_id,run_id,task_id,conversation_id,dimension_key,bucket,occurred_at,visible,conflict,native,estimated_usd,estimated_model) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.Source, c.RequestID, r.Seq, r.ThreadID, r.TurnID, r.RootTurnID, r.ParentThreadID, r.SessionID, r.Attribution.RunID, r.Attribution.TaskID, conversation, key.dimension, key.bucket, r.OccurredAt, c.Visible, r.Conflict, r.RecordID != "", estimate, estimatedModel); err != nil {
			return err
		}
		groups[key] = struct{}{}
	}
	for key := range groups {
		if err := rebuildUsageSummary(ctx, tx, key); err != nil {
			return err
		}
	}
	// Attribution corrections replace a projection, not a permanent history of
	// copied Issue titles. Reclaim only the touched, now-unreferenced dimensions.
	cleaned := map[int64]bool{}
	for key := range groups {
		if cleaned[key.dimension] {
			continue
		}
		cleaned[key.dimension] = true
		var dimension int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT dimension_id FROM token_usage_partitions WHERE dimension_key=?`, key.dimension).Scan(&dimension); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM token_usage_partitions WHERE dimension_key=? AND NOT EXISTS(SELECT 1 FROM token_usage_contributions WHERE dimension_key=?) AND NOT EXISTS(SELECT 1 FROM token_usage_summaries WHERE dimension_key=?)`, key.dimension, key.dimension, key.dimension); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM token_usage_dimensions WHERE dimension_id=? AND NOT EXISTS(SELECT 1 FROM token_usage_partitions WHERE dimension_id=?)`, dimension, dimension); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM token_usage_dirty`); err != nil {
		return err
	}
	for _, notify := range changed {
		if err := notify(); err != nil {
			return err
		}
	}
	return nil
}

func internUsageDimension(ctx context.Context, tx *store.Tx, d views.UsageDimension) (int64, error) {
	r, a := d.Record, d.Record.Attribution
	path, err := json.Marshal(a.IssuePath)
	if err != nil {
		return 0, err
	}
	categories, err := json.Marshal(a.Categories)
	if err != nil {
		return 0, err
	}
	values := []any{r.Subject, r.Role, d.BindingRun, d.BindingConversation, r.SessionID, r.ParentSessionID, r.Model, r.Provider, r.NativeVersion, r.RecordID != "", a.ExecutionState, string(path), a.TaskID, a.TaskTitle, a.RunID, a.Agent, a.AgentID, string(categories)}
	const columns = views.UsageDimensionFields
	if _, err = tx.Exec(ctx, `INSERT INTO token_usage_dimensions(`+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, values...); err != nil {
		return 0, err
	}
	var id int64
	err = tx.Q().QueryRowContext(ctx, `SELECT dimension_id FROM token_usage_dimensions WHERE (`+columns+`)=(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, values...).Scan(&id)
	return id, err
}

func rebuildUsageSummary(ctx context.Context, tx *store.Tx, key usageSummaryKey) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT f.settlement,f.input,f.cache_read,f.cache_write,f.output,f.total,c.estimated_usd,c.occurred_at,c.fact_seq,c.conflict,c.native FROM token_usage_contributions c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE c.dimension_key=? AND c.bucket=? AND c.visible=1 ORDER BY c.fact_seq DESC`, key.dimension, key.bucket)
	if err != nil {
		return err
	}
	var totals protocol.TokenUsageTotals
	var first, last *int64
	var conflicts, legacy, seq, count int64
	for rows.Next() {
		var r protocol.TokenUsageRecord
		var estimate *string
		var at *int64
		var current int64
		var conflict, native bool
		if err = rows.Scan(&r.Settlement, &r.Input, &r.CacheRead, &r.CacheWrite, &r.Output, &r.Total, &estimate, &at, &current, &conflict, &native); err != nil {
			break
		}
		count++
		if !native {
			legacy++
		}
		if conflict {
			conflicts++
			continue
		}
		seq = max(seq, current)
		if estimate != nil {
			r.Estimate = &protocol.UsageEstimate{USD: *estimate}
		}
		views.MergeUsageTotals(&totals, views.UsageRecordTotals(r))
		if at != nil {
			if first == nil || *at < *first {
				first = at
			}
			if last == nil || *at > *last {
				last = at
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if count == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM token_usage_summaries WHERE dimension_key=? AND bucket=?`, key.dimension, key.bucket)
		return err
	}
	values := []any{key.dimension, key.bucket, totals.Requests, totals.Final, totals.Input, totals.InputKnown, totals.CacheRead, totals.CacheReadKnown, totals.CacheWrite, totals.CacheWriteKnown, totals.Output, totals.OutputKnown, totals.Total, totals.TotalKnown, totals.EstimatedUSD, totals.EstimatedRequests, conflicts, legacy, first, last, seq}
	_, err = tx.Exec(ctx, `INSERT INTO token_usage_summaries(dimension_key,bucket,requests,final,input,input_known,cache_read,cache_read_known,cache_write,cache_write_known,output,output_known,total,total_known,estimated_usd,estimated_requests,conflicts,legacy_requests,first_at,last_at,max_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(dimension_key,bucket) DO UPDATE SET requests=excluded.requests,final=excluded.final,input=excluded.input,input_known=excluded.input_known,cache_read=excluded.cache_read,cache_read_known=excluded.cache_read_known,cache_write=excluded.cache_write,cache_write_known=excluded.cache_write_known,output=excluded.output,output_known=excluded.output_known,total=excluded.total,total_known=excluded.total_known,estimated_usd=excluded.estimated_usd,estimated_requests=excluded.estimated_requests,conflicts=excluded.conflicts,legacy_requests=excluded.legacy_requests,first_at=excluded.first_at,last_at=excluded.last_at,max_seq=excluded.max_seq`, values...)
	return err
}
