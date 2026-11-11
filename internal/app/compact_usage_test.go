package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestCompactUsageSharedMetadataConservesRepeatedCollection(t *testing.T) {
	h := newHarness(t)
	var samples []protocol.TokenUsageSample
	for i := range 260 {
		s := nativeSample(h.clock.Now().UnixMilli())
		s.RequestID, s.RecordID = fmt.Sprintf("response-%d", i), fmt.Sprintf("record-%d", i)
		s.Total = usageNumber(int64(i))
		if i%2 == 0 {
			s.Input = nil
		}
		samples = append(samples, s)
	}
	collect := func() {
		t.Helper()
		for begin := 0; begin < len(samples); begin += 128 {
			end := min(begin+128, len(samples))
			if err := h.app.RecordNativeConversationUsage(t.Context(), "main", samples[begin:end]); err != nil {
				t.Fatal(err)
			}
		}
	}
	collect()
	before, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	collect() // A repeated full source scan cannot add another measurement.
	after, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if !reflect.DeepEqual(before, after) || after.Totals.Requests != 260 || after.Totals.Total != 259*260/2 || after.Totals.InputKnown != 130 || after.Totals.TotalKnown != 260 {
		t.Fatal("typed ledger lost field coverage or repeated collection", before.Totals, after.Totals)
	}
	if h.count("token_usage_facts") != 260 || h.count("token_usage_contexts") != 1 || h.count("token_usage_dimensions") != 1 || h.count("token_usage_partitions") != 3 || h.count("token_usage_summaries") != 3 {
		t.Fatal("response metadata or attribution was copied per measurement")
	}
	filtered, _ := projectionView(t, h, "total", 0, views.UsageFilter{Role: "main"})
	if filtered.Totals != after.Totals || len(filtered.Records) == 0 || filtered.Records[0].Role != "main" || filtered.Records[0].Attribution.Role != "main" {
		t.Fatal("shared role dimension changed attribution or filtering")
	}
	// Secondary indexes carry the compact integer row identity, rather than the
	// long provider/request key in every b-tree entry.
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var typ string
		var primary int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT type,pk FROM pragma_table_info('token_usage_contributions') WHERE name='fact_seq'`).Scan(&typ, &primary); err != nil {
			return err
		}
		if typ != "INTEGER" || primary != 1 {
			t.Fatal("contribution indexes still replicate the long native key")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLatestUsagePricesReplaceWithoutChangingMeasurementOrCoverage(t *testing.T) {
	h := newHarness(t)
	sample := nativeSample(h.clock.Now().UnixMilli())
	sample.Model = "model"
	sample.CacheRead, sample.CacheWrite = usageNumber(0), usageNumber(0)
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{sample}); err != nil {
		t.Fatal(err)
	}
	quote := json.RawMessage(`{"data":[{"id":"model","pricing":{"prompt":"0.1","completion":"0.2","input_cache_read":"0","input_cache_write":"0"}}]}`)
	if err := h.app.RecordUsagePriceCatalog(t.Context(), quote); err != nil {
		t.Fatal(err)
	}
	before, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	h.clock.Advance(time.Second)
	quote = json.RawMessage(strings.ReplaceAll(string(quote), `"0.1"`, `"0.3"`))
	if err := h.app.RecordUsagePriceCatalog(t.Context(), quote); err != nil {
		t.Fatal(err)
	}
	after, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if h.count("token_usage_latest_prices") != 1 || h.count("token_usage_facts") != 1 || before.Totals.EstimatedUSD == after.Totals.EstimatedUSD || !reflect.DeepEqual(before.Records[0].TokenUsageSample, after.Records[0].TokenUsageSample) || !reflect.DeepEqual(before.Collection, after.Collection) {
		t.Fatal("catalog replacement retained history or changed measurement", before, after)
	}
	if after.Pricing.QuotedAt == nil || *after.Pricing.QuotedAt != h.clock.Now().UnixMilli() {
		t.Fatal("latest successful collection instant is missing")
	}
	h.clock.Advance(time.Second)
	if err := h.app.RecordUsagePriceCatalog(t.Context(), json.RawMessage(`{"data":[]}`)); err == nil {
		t.Fatal("failed price collection replaced the valid catalog")
	}
	failed, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if failed.Pricing.QuotedAt == nil || *failed.Pricing.QuotedAt != *after.Pricing.QuotedAt || failed.Totals != after.Totals {
		t.Fatal("failure discarded the last valid prices or changed their timestamp")
	}
}
