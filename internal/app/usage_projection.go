package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	dimension string
	bucket    int64
}

const usageDirtyGroupsSQL = `SELECT DISTINCT p.dimension_key,p.bucket,p.dimension_json FROM token_usage_dirty d CROSS JOIN token_usage_contributions p USING(source,request_id)`

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

func refreshUsageProjection(ctx context.Context, tx *store.Tx) error {
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
	groups := map[usageSummaryKey]string{}
	rows, err := tx.Q().QueryContext(ctx, usageDirtyGroupsSQL)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key usageSummaryKey
		var dimension string
		if err = rows.Scan(&key.dimension, &key.bucket, &dimension); err != nil {
			break
		}
		groups[key] = dimension
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
		dimension, err := json.Marshal(c.Dimension)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(dimension)
		key := usageSummaryKey{dimension: hex.EncodeToString(hash[:]), bucket: -1}
		if r.OccurredAt != nil {
			key.bucket = *r.OccurredAt / views.UsageBucketWidth * views.UsageBucketWidth
		}
		record, err := json.Marshal(r)
		if err != nil {
			return err
		}
		totals, err := json.Marshal(views.UsageRecordTotals(r))
		if err != nil {
			return err
		}
		conversation := c.Dimension.BindingConversation
		if r.RecordID == "" && strings.HasPrefix(r.Subject, "conversation:") {
			conversation = strings.TrimPrefix(r.Subject, "conversation:")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO token_usage_contributions(source,request_id,fact_seq,thread_id,turn_id,root_turn_id,parent_thread_id,session_id,run_id,task_id,conversation_id,dimension_key,bucket,occurred_at,visible,conflict,native,record_json,dimension_json,totals_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.Source, c.RequestID, r.Seq, r.ThreadID, r.TurnID, r.RootTurnID, r.ParentThreadID, r.SessionID, r.Attribution.RunID, r.Attribution.TaskID, conversation, key.dimension, key.bucket, r.OccurredAt, c.Visible, r.Conflict, r.RecordID != "", string(record), string(dimension), string(totals)); err != nil {
			return err
		}
		groups[key] = string(dimension)
	}
	for key, dimension := range groups {
		if err := rebuildUsageSummary(ctx, tx, key, dimension); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM token_usage_dirty`)
	return err
}

func rebuildUsageSummary(ctx context.Context, tx *store.Tx, key usageSummaryKey, dimension string) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT totals_json,occurred_at,fact_seq,conflict,native FROM token_usage_contributions WHERE dimension_key=? AND bucket=? AND visible=1 ORDER BY fact_seq DESC`, key.dimension, key.bucket)
	if err != nil {
		return err
	}
	var totals protocol.TokenUsageTotals
	var first, last *int64
	var conflicts, legacy, seq, count int64
	for rows.Next() {
		var raw string
		var at *int64
		var current int64
		var conflict, native bool
		if err = rows.Scan(&raw, &at, &current, &conflict, &native); err != nil {
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
		var contribution protocol.TokenUsageTotals
		if err = json.Unmarshal([]byte(raw), &contribution); err != nil {
			break
		}
		views.MergeUsageTotals(&totals, contribution)
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
	raw, err := json.Marshal(totals)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO token_usage_summaries(dimension_key,bucket,dimension_json,totals_json,conflicts,legacy_requests,first_at,last_at,max_seq) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(dimension_key,bucket) DO UPDATE SET dimension_json=excluded.dimension_json,totals_json=excluded.totals_json,conflicts=excluded.conflicts,legacy_requests=excluded.legacy_requests,first_at=excluded.first_at,last_at=excluded.last_at,max_seq=excluded.max_seq`, key.dimension, key.bucket, dimension, string(raw), conflicts, legacy, first, last, seq)
	return err
}
