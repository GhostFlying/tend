package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestUsagePriceRefreshOnlyRebuildsAffectedModels(t *testing.T) {
	h := newHarness(t)
	model := func(id, rate string) domain.UsagePriceModel {
		return domain.UsagePriceModel{ID: id, Pricing: domain.UsageRates{Prompt: rate, Completion: "0.2", CacheRead: "0", CacheWrite: "0"}}
	}
	record := func(models ...domain.UsagePriceModel) {
		t.Helper()
		raw, err := json.Marshal(domain.UsagePrices{Data: models})
		if err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Second)
		if err := h.app.RecordUsagePriceCatalog(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
	}
	a, b := model("provider/a", "0.1"), model("provider/b", "0.3")
	record(a, b)
	var samples []protocol.TokenUsageSample
	for i := range 96 {
		s := nativeSample(h.clock.Now().UnixMilli())
		s.RequestID = fmt.Sprint(i)
		s.RecordID = fmt.Sprint(i)
		s.Model = []string{"provider/a", "b", "missing"}[i%3]
		s.CacheRead, s.CacheWrite = usageNumber(0), usageNumber(0)
		samples = append(samples, s)
	}
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", samples); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TABLE price_refresh_test_rebuilds(source TEXT,request_id TEXT,PRIMARY KEY(source,request_id)) WITHOUT ROWID`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `CREATE TRIGGER price_refresh_test_rebuild AFTER DELETE ON token_usage_contributions BEGIN INSERT OR IGNORE INTO price_refresh_test_rebuilds VALUES(OLD.source,OLD.request_id); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		got := h.count("price_refresh_test_rebuilds")
		if got != want {
			t.Fatalf("price refresh rebuilt %d requests, want %d affected identities", got, want)
		}
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `DELETE FROM price_refresh_test_rebuilds`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	record(b, a) // Public directory order is not a rate or model selection change.
	check(0)
	// Provider descriptions are not retained pricing inputs. A successful
	// retrieval still advances freshness and emits the ordinary usage change.
	raw, err := json.Marshal(domain.UsagePrices{Data: []domain.UsagePriceModel{b, a}})
	if err != nil {
		t.Fatal(err)
	}
	var directory map[string]any
	if err := json.Unmarshal(raw, &directory); err != nil {
		t.Fatal(err)
	}
	directory["provider_description"] = "new descriptive metadata"
	raw, err = json.Marshal(directory)
	if err != nil {
		t.Fatal(err)
	}
	changesBefore := usageNotifications(t, h)
	h.clock.Advance(time.Second)
	if err := h.app.RecordUsagePriceCatalog(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	check(0)
	if got := usageNotifications(t, h); got != changesBefore+1 {
		t.Fatalf("successful quote retrieval lost ordinary freshness notification: before=%d after=%d", changesBefore, got)
	}

	record(b, a, model("unused/model", "0.9"))
	check(0)
	after, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if before.Totals != after.Totals || !reflect.DeepEqual(before.Collection, after.Collection) || *after.Pricing.QuotedAt != h.clock.Now().UnixMilli() {
		t.Fatal("unchanged quote lost freshness, measurement or valuation", before, after)
	}
	a.Pricing.Prompt = "0.4"
	record(a, b)
	check(32)
	changed, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if changed.Totals.EstimatedUSD == before.Totals.EstimatedUSD || changed.Totals.EstimatedRequests != 64 {
		t.Fatal("changed used rate did not update valuation", changed.Totals)
	}
	record(a, b, model("other/b", "0.7")) // A formerly unique native slug becomes ambiguous.
	check(32)
	ambiguous, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if ambiguous.Totals.EstimatedRequests != 32 {
		t.Fatal("ambiguous slug retained a made-up quote", ambiguous.Totals)
	}
	record(b) // Removal unprices a; resolving the slug reprices b.
	check(64)
	removed, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if removed.Totals.EstimatedRequests != 32 || h.count("token_usage_facts") != 96 || h.count("token_usage_dirty") != 0 {
		t.Fatal("removal lost measurements or dirty settlement", removed.Totals)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.app.RecordUsagePriceCatalog(ctx, json.RawMessage(`{"data":[{"id":"provider/a"}]}`)); err == nil {
		t.Fatal("cancelled refresh succeeded")
	}
	if err := h.app.RecordUsagePriceCatalog(t.Context(), json.RawMessage(`{"data":[]}`)); err == nil {
		t.Fatal("empty catalog replaced valid prices")
	}
	failed, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if !reflect.DeepEqual(removed.Pricing, failed.Pricing) || removed.Totals != failed.Totals {
		t.Fatal("failed refresh changed freshness or estimates", removed, failed)
	}
	check(0)
}

func TestUsagePriceRefreshPreservesFirstMatchAndRollsBack(t *testing.T) {
	h := newHarness(t)
	sample := nativeSample(h.clock.Now().UnixMilli())
	sample.Model = "model"
	sample.CacheRead, sample.CacheWrite = usageNumber(0), usageNumber(0)
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{sample}); err != nil {
		t.Fatal(err)
	}
	first := json.RawMessage(`{"data":[{"id":"model","pricing":{"prompt":"0.1","completion":"0.2"}},{"id":"model","pricing":{"prompt":"0.4","completion":"0.2"}}]}`)
	reversed := json.RawMessage(`{"data":[{"id":"model","pricing":{"prompt":"0.4","completion":"0.2"}},{"id":"model","pricing":{"prompt":"0.1","completion":"0.2"}}]}`)
	if err := h.app.RecordUsagePriceCatalog(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	before, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	h.clock.Advance(time.Second)
	if err := h.app.RecordUsagePriceCatalog(t.Context(), reversed); err != nil {
		t.Fatal(err)
	}
	after, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if before.Totals.EstimatedUSD == after.Totals.EstimatedUSD {
		t.Fatal("duplicate exact IDs lost first-match ordering")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_price_rebuild BEFORE DELETE ON token_usage_contributions BEGIN SELECT RAISE(ABORT,'price rebuild fault');END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Second)
	if err := h.app.RecordUsagePriceCatalog(t.Context(), first); err == nil {
		t.Fatal("injected projection failure succeeded")
	}
	failed, _ := projectionView(t, h, "total", 0, views.UsageFilter{})
	if after.Totals != failed.Totals || !reflect.DeepEqual(after.Pricing, failed.Pricing) || h.count("token_usage_dirty") != 0 {
		t.Fatal("failed transaction retained new freshness, prices or pending work", after, failed)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+usagePriceAffectedSQL, "model")
		if err != nil {
			return err
		}
		defer rows.Close()
		var details string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			details += detail + "\n"
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, index := range []string{"token_usage_context_model", "token_usage_fact_context", "token_usage_current_fact"} {
			if !strings.Contains(details, index) {
				t.Errorf("price invalidation does not use %s: %s", index, details)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
