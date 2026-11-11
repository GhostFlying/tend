package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func projectedTokenUsage(ctx context.Context, q store.Querier, kind string, offset int, model, subject string, before int64, now time.Time, filter UsageFilter) (protocol.TokenUsageView, error) {
	start, end, err := UsageWindow(kind, offset, now)
	v := protocol.TokenUsageView{AsOf: now.UnixMilli(), Timezone: "Asia/Shanghai", Window: kind, Start: start, End: end, Trend: []protocol.TokenUsageBucket{}, Models: []protocol.TokenUsageGroup{}, Roles: []protocol.TokenUsageGroup{}, Records: []protocol.TokenUsageRecord{}, Coverage: "原生持久化请求用量，包含已归属的续接、压缩与子 Agent。仅已采集来源；缺失的历史不代表零消费。时间为原生请求完成记录时间；缓存及推理不重复累加。执行器归一化字段不证明供应商原始字段存在性。"}
	if err != nil {
		return v, err
	}
	var dirty bool
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_dirty) OR EXISTS(SELECT 1 FROM token_usage_overlap_dirty)`).Scan(&dirty); err != nil {
		return v, err
	}
	if dirty {
		return v, fmt.Errorf("usage projection requires app refresh")
	}
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM token_usage_facts`).Scan(&v.Revision); err != nil {
		return v, err
	}
	v.Pricing = protocol.UsagePricing{Source: "OpenRouter", Currency: "USD"}
	err = q.QueryRowContext(ctx, `SELECT collected_at FROM token_usage_latest_prices WHERE singleton=1`).Scan(&v.Pricing.QuotedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	cutoff := min(v.AsOf, end)
	dimensions := map[int64]UsageDimension{}
	matched := map[int64]bool{}
	rows, err := q.QueryContext(ctx, `SELECT p.dimension_key,`+usageDimensionColumns("d")+` FROM token_usage_partitions p JOIN token_usage_dimensions d USING(dimension_id)`)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var key int64
		var d UsageDimension
		var native bool
		if err = rows.Scan(append([]any{&key}, usageDimensionDestinations(&d, &native)...)...); err != nil {
			break
		}
		if native {
			d.Record.RecordID = "native"
		}
		d.Record.Attribution.Role = d.Record.Role
		dimensions[key] = d
		matched[key] = d.matches(model, subject, filter)
	}
	if err = closeUsageRows(rows, err); err != nil {
		return v, err
	}
	rows, err = q.QueryContext(ctx, `SELECT dimension_key,SUM(conflicts) FROM token_usage_summaries WHERE conflicts>0 GROUP BY dimension_key`)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var key int64
		var n int64
		if err = rows.Scan(&key, &n); err != nil {
			break
		}
		if matched[key] {
			v.Conflicts += n
		}
	}
	if err = closeUsageRows(rows, err); err != nil {
		return v, err
	}
	rows, err = q.QueryContext(ctx, `SELECT s.dimension_key,s.bucket,`+usageTotalsColumns("s")+`,s.first_at,s.last_at,s.max_seq FROM token_usage_summaries s WHERE bucket=-1 OR (? IS NULL OR bucket+?>?) AND bucket<? OR bucket+?>?`, start, UsageBucketWidth, start, cutoff, UsageBucketWidth, v.AsOf)
	if err != nil {
		return v, err
	}
	type edge struct {
		Dimension int64 `json:"dimension"`
		Bucket    int64 `json:"bucket"`
	}
	var edges []edge
	var aggregates []UsageAggregate
	for rows.Next() {
		var key, bucket, seq int64
		var a UsageAggregate
		if err = rows.Scan(append(append([]any{&key, &bucket}, usageTotalsDestinations(&a.Totals)...), &a.FirstAt, &a.LastAt, &seq)...); err != nil {
			break
		}
		if !matched[key] {
			continue
		}
		d := dimensions[key]
		a.Record = d.Record
		a.Record.Seq = seq
		if a.Totals.Requests == 0 {
			continue
		}
		if bucket == -1 || (bucket >= v.AsOf && d.Record.RecordID != "") {
			MergeUsageTotals(&v.Unattributed, a.Totals)
			if kind == "total" {
				a.FirstAt, a.LastAt = nil, nil
				aggregates = append(aggregates, a)
			}
		} else if d.Record.RecordID != "" && bucket+UsageBucketWidth > v.AsOf {
			edges = append(edges, edge{key, bucket})
		} else if bucket >= cutoff || (start != nil && bucket+UsageBucketWidth <= *start) {
			continue
		} else if (start == nil || bucket >= *start) && bucket+UsageBucketWidth <= cutoff {
			aggregates = append(aggregates, a)
		} else {
			edges = append(edges, edge{key, bucket})
		}
	}
	if err = closeUsageRows(rows, err); err != nil {
		return v, err
	}
	if len(edges) > 0 {
		raw, e := json.Marshal(edges)
		if e != nil {
			return v, e
		}
		rows, err = q.QueryContext(ctx, `SELECT c.dimension_key,f.settlement,f.input,f.cache_read,f.cache_write,f.output,f.total,c.estimated_usd,c.occurred_at,c.fact_seq,c.native FROM token_usage_contributions c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE (c.dimension_key,c.bucket) IN (SELECT json_extract(value,'$.dimension'),json_extract(value,'$.bucket') FROM json_each(?)) AND visible=1 AND conflict=0 ORDER BY c.fact_seq DESC`, string(raw))
		if err != nil {
			return v, err
		}
		for rows.Next() {
			var key, seq int64
			var at *int64
			var native bool
			var r protocol.TokenUsageRecord
			var estimate *string
			if err = rows.Scan(&key, &r.Settlement, &r.Input, &r.CacheRead, &r.CacheWrite, &r.Output, &r.Total, &estimate, &at, &seq, &native); err != nil {
				break
			}
			if estimate != nil {
				r.Estimate = &protocol.UsageEstimate{USD: *estimate}
			}
			a := UsageAggregate{Record: dimensions[key].Record, FirstAt: at, LastAt: at, Totals: UsageRecordTotals(r)}
			a.Record.Seq = seq
			if native && at != nil && *at >= v.AsOf {
				MergeUsageTotals(&v.Unattributed, a.Totals)
				if kind == "total" {
					a.FirstAt, a.LastAt = nil, nil
					aggregates = append(aggregates, a)
				}
			} else if at != nil && *at < cutoff && (start == nil || *at >= *start) {
				aggregates = append(aggregates, a)
			}
		}
		if err = closeUsageRows(rows, err); err != nil {
			return v, err
		}
	}
	sort.Slice(aggregates, func(i, j int) bool { return aggregates[i].Record.Seq > aggregates[j].Record.Seq })
	grouping := newUsageGrouping()
	models, roles := map[string]*protocol.TokenUsageGroup{}, map[string]*protocol.TokenUsageGroup{}
	buckets := map[int64]*protocol.TokenUsageBucket{}
	for _, a := range aggregates {
		grouping.addAggregate(a)
		MergeUsageTotals(&v.Totals, a.Totals)
		for _, group := range []struct {
			name   string
			groups map[string]*protocol.TokenUsageGroup
		}{{usageModel(a.Record.Model), models}, {a.Record.Attribution.Role, roles}} {
			name, groups := group.name, group.groups
			if groups[name] == nil {
				groups[name] = &protocol.TokenUsageGroup{Name: name}
			}
			MergeUsageTotals(&groups[name].TokenUsageTotals, a.Totals)
		}
		if a.FirstAt != nil && !filter.Summary {
			step := UsageBucketWidth
			if kind != "day" && kind != "24h" {
				step = 24 * 60 * 60 * 1000
			}
			zone := int64(8 * 60 * 60 * 1000)
			at := (*a.FirstAt+zone)/step*step - zone
			if buckets[at] == nil {
				begin := at
				if start != nil {
					begin = max(begin, *start)
				}
				buckets[at] = &protocol.TokenUsageBucket{Start: begin, End: min(at+step, end, v.AsOf)}
			}
			MergeUsageTotals(&buckets[at].TokenUsageTotals, a.Totals)
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
	{
		keys := []int64{}
		for key, match := range matched {
			if match {
				keys = append(keys, key)
			}
		}
		raw, e := json.Marshal(keys)
		if e != nil {
			return v, e
		}
		columns := store.UsageFactColumns("f") + `,c.dimension_key,c.estimated_usd,c.estimated_model`
		join := ` JOIN token_usage_fact_details f ON f.seq=c.fact_seq`
		if filter.Summary {
			columns = "c.fact_seq"
			join = ""
		}
		rows, err = q.QueryContext(ctx, `SELECT `+columns+` FROM token_usage_contributions c`+join+` WHERE c.visible=1 AND c.conflict=0 AND c.dimension_key IN (SELECT value FROM json_each(?)) AND (?=0 OR c.fact_seq<?) AND ((c.occurred_at<? AND (? IS NULL OR c.occurred_at>=?)) OR (?='total' AND (c.occurred_at IS NULL OR (c.native=1 AND c.occurred_at>=?)))) ORDER BY c.fact_seq DESC LIMIT 51`, string(raw), before, before, cutoff, start, start, kind, v.AsOf)
		if err != nil {
			return v, err
		}
		count := 0
		for rows.Next() {
			if count == 50 {
				v.More = true
				break
			}
			count++
			if filter.Summary {
				if err = rows.Scan(&v.Next); err != nil {
					break
				}
				continue
			}
			var r protocol.TokenUsageRecord
			var key int64
			var estimate, estimatedModel *string
			if err = rows.Scan(append(store.UsageFactDestinations(&r), &key, &estimate, &estimatedModel)...); err != nil {
				break
			}
			r.Attribution = dimensions[key].Record.Attribution
			r.Role = dimensions[key].Record.Role
			if estimate != nil && estimatedModel != nil {
				r.Estimate = &protocol.UsageEstimate{USD: *estimate, ModelID: *estimatedModel}
			}
			if r.RecordID != "" && r.OccurredAt != nil && *r.OccurredAt >= v.AsOf {
				r.OccurredAt = nil
			}
			v.Records = append(v.Records, r)
			v.Next = r.Seq
		}
		if err = closeUsageRows(rows, err); err != nil {
			return v, err
		}
	}
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
	if err = usageCollectionTotals(ctx, q, &v.Collection); err != nil {
		return v, err
	}
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(legacy_requests),0) FROM token_usage_summaries`).Scan(&v.Collection.LegacyRequests)
	return v, err
}

func closeUsageRows(rows *sql.Rows, err error) error {
	if err == nil {
		err = rows.Err()
	}
	return errors.Join(err, rows.Close())
}
