package views

import (
	"context"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// UsageWindow fixes natural periods to UTC+08, with Monday as the first day.
// offset applies only to calendar periods; rolling windows always end at now.
func UsageWindow(kind string, offset int, now time.Time) (*int64, int64, error) {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	n := now.In(zone)
	end := now.UnixMilli()
	var start time.Time
	switch kind {
	case "total":
		return nil, end, nil
	case "24h":
		start = n.Add(-24 * time.Hour)
	case "7d":
		start = n.Add(-7 * 24 * time.Hour)
	case "30d":
		start = n.Add(-30 * 24 * time.Hour)
	case "day":
		start = time.Date(n.Year(), n.Month(), n.Day()+offset, 0, 0, 0, 0, zone)
		end = start.AddDate(0, 0, 1).UnixMilli()
	case "week":
		days := (int(n.Weekday()) + 6) % 7
		start = time.Date(n.Year(), n.Month(), n.Day()-days+7*offset, 0, 0, 0, 0, zone)
		end = start.AddDate(0, 0, 7).UnixMilli()
	case "month":
		start = time.Date(n.Year(), n.Month()+time.Month(offset), 1, 0, 0, 0, 0, zone)
		end = start.AddDate(0, 1, 0).UnixMilli()
	default:
		return nil, 0, protocol.Errorf(protocol.CodeInvalidRequest, "invalid usage window")
	}
	v := start.UnixMilli()
	return &v, end, nil
}

func addTokens(t *protocol.TokenUsageTotals, s protocol.TokenUsageSample) {
	t.Requests++
	if s.Settlement == "final" {
		t.Final++
	}
	for _, f := range []struct {
		v          *int64
		sum, known *int64
	}{{s.Input, &t.Input, &t.InputKnown}, {s.CacheRead, &t.CacheRead, &t.CacheReadKnown}, {s.CacheWrite, &t.CacheWrite, &t.CacheWriteKnown}, {s.Output, &t.Output, &t.OutputKnown}, {s.Total, &t.Total, &t.TotalKnown}} {
		if f.v != nil {
			*f.sum += *f.v
			*f.known++
		}
	}
}

// TokenUsage uses one read transaction for facts, contributions and watermark.
// Unknown occurrence times stay outside exact calendar/rolling totals.
type UsageFilter struct {
	Agent, Role, Issue, Session, Sort string
	Summary                           bool
}

func TokenUsage(ctx context.Context, q store.Querier, kind string, offset int, model, subject string, before int64, now time.Time) (protocol.TokenUsageView, error) {
	return TokenUsageFiltered(ctx, q, kind, offset, model, subject, before, now, UsageFilter{})
}
func TokenUsageFiltered(ctx context.Context, q store.Querier, kind string, offset int, model, subject string, before int64, now time.Time, filter UsageFilter) (protocol.TokenUsageView, error) {
	return projectedTokenUsage(ctx, q, kind, offset, model, subject, before, now, filter)
}

func addUsageRecord(t *protocol.TokenUsageTotals, r protocol.TokenUsageRecord) {
	addTokens(t, r.TokenUsageSample)
	if r.Estimate != nil {
		t.EstimatedUSD = domain.AddUsagePrice(t.EstimatedUSD, r.Estimate.USD)
		t.EstimatedRequests++
	}
}

func usageMetering(ctx context.Context, q store.Querier) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT session_id,MAX(coverage<>'caught_up' AND reason<>'empty'),MAX(state='retired' AND reason='unavailable' AND coverage<>'caught_up'),MAX(state='needs_reconcile' OR EXISTS(SELECT 1 FROM token_usage_contributions u WHERE u.session_id=sessions.session_id AND u.conflict=1)),MAX(state='unobserved') FROM (SELECT s.session_id,CASE WHEN r.source_key IS NULL AND s.coverage='caught_up' THEN 'unobserved' WHEN COALESCE(json_array_length(json_extract(r.observation_json,'$.pending_request_ids')),0)>0 THEN 'pending' ELSE s.coverage END coverage,COALESCE(json_extract(r.collection_json,'$.state'),'unobserved') state,COALESCE(json_extract(r.collection_json,'$.retirement_reason'),'') reason FROM token_usage_sources s LEFT JOIN token_usage_remote_collection r ON r.source_key=s.source_key UNION ALL SELECT COALESCE(NULLIF(json_extract(source_json,'$.session_id'),''),json_extract(source_json,'$.thread_id')),coverage,collection_state,COALESCE(json_extract(collection_json,'$.retirement_reason'),'') FROM token_usage_local_sources) sessions GROUP BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var session string
		var pending, unavailable, needsReconcile, unobserved bool
		if err = rows.Scan(&session, &pending, &unavailable, &needsReconcile, &unobserved); err != nil {
			return nil, err
		}
		result[session] = "caught_up"
		if pending {
			result[session] = "syncing"
		}
		if unobserved {
			result[session] = "unobserved"
		}
		if unavailable {
			result[session] = "unavailable"
		}
		if needsReconcile {
			result[session] = "needs_reconcile"
		}
	}
	return result, rows.Err()
}
