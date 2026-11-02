package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/domain"
	"sort"
	"strings"
	"time"

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
type UsageFilter struct{ Agent, Role, Issue, Session, Sort string }

func TokenUsage(ctx context.Context, q store.Querier, kind string, offset int, model, subject string, before int64, now time.Time) (protocol.TokenUsageView, error) {
	return TokenUsageFiltered(ctx, q, kind, offset, model, subject, before, now, UsageFilter{})
}
func TokenUsageFiltered(ctx context.Context, q store.Querier, kind string, offset int, model, subject string, before int64, now time.Time, filter UsageFilter) (protocol.TokenUsageView, error) {
	start, end, err := UsageWindow(kind, offset, now)
	v := protocol.TokenUsageView{AsOf: now.UnixMilli(), Timezone: "Asia/Shanghai", Window: kind, Start: start, End: end, Trend: []protocol.TokenUsageBucket{}, Models: []protocol.TokenUsageGroup{}, Roles: []protocol.TokenUsageGroup{}, Records: []protocol.TokenUsageRecord{}, Coverage: "原生持久化请求用量，包含已归属的续接、压缩与子 Agent。仅已采集来源；缺失的历史不代表零消费。时间为原生请求完成记录时间；缓存及推理不重复累加。执行器归一化字段不证明供应商原始字段存在性。"}
	if err != nil {
		return v, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM token_usage_facts`).Scan(&v.Revision); err != nil {
		return v, err
	}
	v.Pricing = protocol.UsagePricing{Source: "OpenRouter", Currency: "USD"}
	var priceRaw string
	prices := domain.UsagePrices{}
	quoteErr := q.QueryRowContext(ctx, `SELECT collected_at,catalog_json FROM token_usage_price_catalogs ORDER BY collected_at DESC LIMIT 1`).Scan(&v.Pricing.QuotedAt, &priceRaw)
	if quoteErr != nil && !errors.Is(quoteErr, sql.ErrNoRows) {
		return v, quoteErr
	}
	if quoteErr == nil {
		prices, err = domain.ParseUsagePrices([]byte(priceRaw))
		if err != nil {
			return v, err
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT f.seq,f.subject,f.role,f.received_at,(c.conflict OR `+store.UsagePendingOverlapSQLFor("f")+`),f.payload_json,COALESCE(b.run_id||'/'||b.conversation_id||'/'||b.proof||'/'||b.attribution_conflict,''),COALESCE((?='' OR f.subject=? OR (b.attribution_conflict=0 AND ('conversation:'||b.conversation_id=? OR 'run:'||b.run_id=?))),0) FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq LEFT JOIN token_usage_bindings b ON b.source=f.source AND b.request_id=f.request_id WHERE (?='' OR f.subject=? OR json_extract(f.payload_json,'$.record_id') IS NOT NULL OR EXISTS(SELECT 1 FROM token_usage_bindings b WHERE b.source=f.source AND b.request_id=f.request_id AND b.attribution_conflict=0 AND ('conversation:'||b.conversation_id=? OR 'run:'||b.run_id=?))) AND NOT `+store.UsageLegacyLinkedSQL("f.seq")+` ORDER BY f.seq DESC`, subject, subject, subject, subject, subject, subject, subject, subject)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	grouping := newUsageGrouping()
	attributions := map[string]protocol.UsageAttribution{}
	models := map[string]*protocol.TokenUsageGroup{}
	roles := map[string]*protocol.TokenUsageGroup{}
	buckets := map[int64]*protocol.TokenUsageBucket{}
	for rows.Next() {
		var r protocol.TokenUsageRecord
		var raw, binding string
		var matchesSubject bool
		if err = rows.Scan(&r.Seq, &r.Subject, &r.Role, &r.ReceivedAt, &r.Conflict, &raw, &binding, &matchesSubject); err != nil {
			return v, err
		}
		if err = json.Unmarshal([]byte(raw), &r.TokenUsageSample); err != nil {
			return v, err
		}
		key := r.Subject + "/" + r.Purpose + "/" + binding
		if len(r.Subject) < 4 || r.Subject[:4] != "run:" {
			key += "/" + r.ThreadID + "/" + r.TurnID
		}
		attribution, exists := attributions[key]
		if !exists {
			attribution = usageAttribution(ctx, q, r)
			attributions[key] = attribution
		}
		r.Attribution = attribution
		if subject != "" && r.RecordID != "" {
			if strings.HasPrefix(subject, "run:") {
				if r.Attribution.RunID != strings.TrimPrefix(subject, "run:") {
					continue
				}
			} else if !matchesSubject {
				continue
			}
		}
		r.Role = r.Attribution.Role
		if (filter.Agent != "" && r.Attribution.Agent != filter.Agent && r.Attribution.AgentID != filter.Agent) || (filter.Role != "" && r.Attribution.Role != filter.Role) || (filter.Session != "" && sessionID(r) != filter.Session) {
			continue
		}
		if filter.Issue != "" {
			found := filter.Issue == "_unassigned" && len(r.Attribution.IssuePath) == 0
			for _, issue := range r.Attribution.IssuePath {
				found = found || issue.ID == filter.Issue
			}
			if !found {
				continue
			}
		}
		name := r.Model
		if name == "" {
			name = "实际模型未提供"
		}
		if model != "" && name != model {
			continue
		}
		if r.Conflict {
			v.Conflicts++
			continue
		}
		if r.RecordID != "" && r.OccurredAt != nil && *r.OccurredAt >= v.AsOf {
			r.OccurredAt = nil
		}
		if usd, id, ok := prices.EstimateUsagePrice(r.TokenUsageSample); ok {
			r.Estimate = &protocol.UsageEstimate{USD: usd, ModelID: id}
		}
		if r.OccurredAt == nil {
			addUsageRecord(&v.Unattributed, r)
			if kind != "total" {
				continue
			}
		} else if *r.OccurredAt >= v.AsOf || *r.OccurredAt >= end || (start != nil && *r.OccurredAt < *start) {
			continue
		}
		grouping.add(r)
		addUsageRecord(&v.Totals, r)
		if r.OccurredAt != nil {
			step := int64(24 * time.Hour / time.Millisecond)
			if kind == "day" || kind == "24h" {
				step = int64(4 * time.Hour / time.Millisecond)
			}
			zone := int64(8 * time.Hour / time.Millisecond)
			at := (*r.OccurredAt+zone)/step*step - zone
			if buckets[at] == nil {
				begin := at
				if start != nil && begin < *start {
					begin = *start
				}
				buckets[at] = &protocol.TokenUsageBucket{Start: begin, End: min(at+step, end, v.AsOf)}
			}
			addUsageRecord(&buckets[at].TokenUsageTotals, r)
		}
		if models[name] == nil {
			models[name] = &protocol.TokenUsageGroup{Name: name}
		}
		addUsageRecord(&models[name].TokenUsageTotals, r)
		if roles[r.Attribution.Role] == nil {
			roles[r.Attribution.Role] = &protocol.TokenUsageGroup{Name: r.Attribution.Role}
		}
		addUsageRecord(&roles[r.Attribution.Role].TokenUsageTotals, r)
		if before == 0 || r.Seq < before {
			if len(v.Records) < 50 {
				v.Records = append(v.Records, r)
				v.Next = r.Seq
			} else {
				v.More = true
			}
		}
	}
	for _, m := range models {
		v.Models = append(v.Models, *m)
	}
	for _, r := range roles {
		v.Roles = append(v.Roles, *r)
	}
	sort.Slice(v.Models, func(i, j int) bool { return v.Models[i].Name < v.Models[j].Name })
	sort.Slice(v.Roles, func(i, j int) bool { return v.Roles[i].Name < v.Roles[j].Name })
	for _, b := range buckets {
		v.Trend = append(v.Trend, *b)
	}
	sort.Slice(v.Trend, func(i, j int) bool { return v.Trend[i].Start < v.Trend[j].Start })
	grouping.finish(&v)
	metering, err := usageMetering(ctx, q)
	if err != nil {
		return v, err
	}
	for i := range v.Sessions {
		v.Sessions[i].MeteringStatus = metering[v.Sessions[i].ID]
	}
	for i := range v.Issues {
		for j := range v.Issues[i].Sessions {
			v.Issues[i].Sessions[j].MeteringStatus = metering[v.Issues[i].Sessions[j].ID]
		}
	}
	if filter.Sort == "total" {
		sort.SliceStable(v.Sessions, func(i, j int) bool { return v.Sessions[i].Totals.Total > v.Sessions[j].Totals.Total })
		sort.SliceStable(v.Issues, func(i, j int) bool { return v.Issues[i].Totals.Total > v.Issues[j].Totals.Total })
	}
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(coverage<>'caught_up'),0),MAX(updated_at) FROM (SELECT coverage,updated_at FROM token_usage_sources UNION ALL SELECT coverage,updated_at FROM token_usage_local_sources)`).Scan(&v.Collection.Sources, &v.Collection.Pending, &v.Collection.LastSyncedAt); err != nil {
		return v, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE json_extract(f.payload_json,'$.record_id') IS NULL AND NOT `+store.UsageLegacyLinkedSQL("f.seq")+``).Scan(&v.Collection.LegacyRequests); err != nil {
		return v, err
	}
	return v, rows.Err()
}

func addUsageRecord(t *protocol.TokenUsageTotals, r protocol.TokenUsageRecord) {
	addTokens(t, r.TokenUsageSample)
	if r.Estimate != nil {
		t.EstimatedUSD = domain.AddUsagePrice(t.EstimatedUSD, r.Estimate.USD)
		t.EstimatedRequests++
	}
}

func usageMetering(ctx context.Context, q store.Querier) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT session_id,MAX(coverage<>'caught_up') FROM (SELECT session_id,coverage FROM token_usage_sources UNION ALL SELECT COALESCE(NULLIF(json_extract(source_json,'$.session_id'),''),json_extract(source_json,'$.thread_id')),coverage FROM token_usage_local_sources) GROUP BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var session string
		var pending bool
		if err = rows.Scan(&session, &pending); err != nil {
			return nil, err
		}
		result[session] = "caught_up"
		if pending {
			result[session] = "syncing"
		}
	}
	return result, rows.Err()
}
