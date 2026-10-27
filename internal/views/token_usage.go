package views

import (
	"context"
	"encoding/json"
	"sort"
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
func TokenUsage(ctx context.Context, q store.Querier, kind string, offset int, model, subject string, before int64, now time.Time) (protocol.TokenUsageView, error) {
	start, end, err := UsageWindow(kind, offset, now)
	v := protocol.TokenUsageView{AsOf: now.UnixMilli(), Timezone: "Asia/Shanghai", Window: kind, Start: start, End: end, Trend: []protocol.TokenUsageBucket{}, Models: []protocol.TokenUsageGroup{}, Roles: []protocol.TokenUsageGroup{}, Records: []protocol.TokenUsageRecord{}, Coverage: "仅已识别的原生请求；当前已验证 TraeX 新建会话请求；恢复会话与 Orca 未提供该请求通路。子 Agent 仅计有明确来源链的独立请求；未观测来源和采集前历史不在覆盖范围。未知时点不计入精确时间窗口；执行器的缓存数值不证明上游字段存在性。"}
	if err != nil {
		return v, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM token_usage_facts`).Scan(&v.Revision); err != nil {
		return v, err
	}
	rows, err := q.QueryContext(ctx, `SELECT f.seq,f.subject,f.role,f.received_at,c.conflict,f.payload_json FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE (?='' OR f.subject=?) ORDER BY f.seq DESC`, subject, subject)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	models := map[string]*protocol.TokenUsageGroup{}
	roles := map[string]*protocol.TokenUsageGroup{}
	buckets := map[int64]*protocol.TokenUsageBucket{}
	for rows.Next() {
		var r protocol.TokenUsageRecord
		var raw string
		if err = rows.Scan(&r.Seq, &r.Subject, &r.Role, &r.ReceivedAt, &r.Conflict, &raw); err != nil {
			return v, err
		}
		if err = json.Unmarshal([]byte(raw), &r.TokenUsageSample); err != nil {
			return v, err
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
		if r.OccurredAt == nil {
			addTokens(&v.Unattributed, r.TokenUsageSample)
			if kind != "total" {
				continue
			}
		} else if *r.OccurredAt >= v.AsOf || *r.OccurredAt >= end || (start != nil && *r.OccurredAt < *start) {
			continue
		}
		addTokens(&v.Totals, r.TokenUsageSample)
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
			addTokens(&buckets[at].TokenUsageTotals, r.TokenUsageSample)
		}
		if models[name] == nil {
			models[name] = &protocol.TokenUsageGroup{Name: name}
		}
		addTokens(&models[name].TokenUsageTotals, r.TokenUsageSample)
		if roles[r.Role] == nil {
			roles[r.Role] = &protocol.TokenUsageGroup{Name: r.Role}
		}
		addTokens(&roles[r.Role].TokenUsageTotals, r.TokenUsageSample)
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
	return v, rows.Err()
}
