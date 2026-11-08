package domain

import (
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func priceNumber(n int64) *int64 { return &n }

func TestUsagePricingModelCaseNormalization(t *testing.T) {
	regular := UsagePriceModel{ID: "openai/gpt-6.1-sol", Pricing: UsageRates{Prompt: "0.000002"}}
	batch := UsagePriceModel{ID: "openai/gpt-6.1-sol:batch", Pricing: UsageRates{Prompt: "0.000001"}}
	other := UsagePriceModel{ID: "other/GPT-6.1-Sol", Pricing: UsageRates{Prompt: "0.000003"}}
	caseVariant := UsagePriceModel{ID: "OpenAI/GPT-6.1-Sol", Pricing: other.Pricing}
	for _, tc := range []struct {
		name    string
		model   string
		catalog []UsagePriceModel
		wantID  string
		wantUSD string
	}{
		{name: "exact id", model: regular.ID, wantID: regular.ID, wantUSD: "0.0002"},
		{name: "mixed case id", model: "OpenAI/GPT-6.1-Sol", wantID: regular.ID, wantUSD: "0.0002"},
		{name: "mixed case slug", model: "GPT-6.1-Sol", wantID: regular.ID, wantUSD: "0.0002"},
		{name: "lowercase slug", model: "gpt-6.1-sol", wantID: regular.ID, wantUSD: "0.0002"},
		{name: "batch slug", model: "GPT-6.1-Sol:BATCH", wantID: batch.ID, wantUSD: "0.0001"},
		{name: "batch id", model: "OPENAI/GPT-6.1-SOL:BATCH", wantID: batch.ID, wantUSD: "0.0001"},
		{name: "empty model"},
		{name: "unknown model", model: "missing"},
		{name: "unknown provider", model: "missing/GPT-6.1-Sol"},
		{name: "unknown variant", model: "GPT-6.1-Sol:missing"},
		{name: "ambiguous lowercase slug", model: "gpt-6.1-sol", catalog: []UsagePriceModel{regular, other}},
		{name: "ambiguous mixed case slug", model: "GPT-6.1-Sol", catalog: []UsagePriceModel{regular, other}},
		{name: "qualified id resolves provider", model: "OPENAI/GPT-6.1-SOL", catalog: []UsagePriceModel{other, regular}, wantID: regular.ID, wantUSD: "0.0002"},
		{name: "ambiguous folded id", model: "OPENAI/GPT-6.1-SOL", catalog: []UsagePriceModel{regular, caseVariant}},
		{name: "exact id wins", model: regular.ID, catalog: []UsagePriceModel{caseVariant, regular}, wantID: regular.ID, wantUSD: "0.0002"},
		{name: "exact variant wins", model: caseVariant.ID, catalog: []UsagePriceModel{regular, caseVariant}, wantID: caseVariant.ID, wantUSD: "0.0003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.catalog == nil {
				tc.catalog = []UsagePriceModel{regular, batch}
			}
			s := protocol.TokenUsageSample{Model: tc.model, Input: priceNumber(100), Output: priceNumber(0), CacheRead: priceNumber(0), CacheWrite: priceNumber(0)}
			usd, id, ok := (UsagePrices{Data: tc.catalog}).EstimateUsagePrice(s)
			if ok != (tc.wantID != "") || id != tc.wantID || usd != tc.wantUSD {
				t.Fatalf("EstimateUsagePrice(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.model, usd, id, ok, tc.wantUSD, tc.wantID, tc.wantID != "")
			}
		})
	}
}

func TestUsagePricingCalendarConditionsDoNotRejectCatalog(t *testing.T) {
	for _, days := range []string{`["saturday","sunday"]`, `[6,0]`} {
		t.Run(days, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"data":[
				{"id":"vendor/calendar","pricing":{"prompt":"0.01","completion":"0.02","overrides":[{"utc_days":%s,"prompt":"0.005"}]}},
				{"id":"vendor/ordinary","pricing":{"prompt":"0.01","completion":"0.02"}}
			]}`, days))
			catalog, err := ParseUsagePrices(raw)
			if err != nil {
				t.Fatal(err)
			}
			sample := protocol.TokenUsageSample{Model: "ordinary", Input: priceNumber(1), Output: priceNumber(1), CacheRead: priceNumber(0), CacheWrite: priceNumber(0)}
			if usd, id, ok := catalog.EstimateUsagePrice(sample); !ok || usd != "0.03" || id != "vendor/ordinary" {
				t.Fatal(usd, id, ok)
			}
			sample.Model = "calendar"
			if _, _, ok := catalog.EstimateUsagePrice(sample); ok {
				t.Fatal("unsupported calendar condition was priced")
			}
		})
	}
}

func TestUsagePricingSubtractsCacheAndUsesRequestTier(t *testing.T) {
	catalog := UsagePrices{Data: []UsagePriceModel{{ID: "openai/model", Pricing: UsageRates{Prompt: "0.000002", Completion: "0.00001", CacheRead: "0.0000001", CacheWrite: "0.0000025", Overrides: []UsageRateOverride{{MinPrompt: priceNumber(272000), Prompt: "0.000004", Completion: "0.000015", CacheRead: "0.0000002", CacheWrite: "0.000005"}}}}}}
	sample := protocol.TokenUsageSample{Model: "model", Input: priceNumber(90996), CacheRead: priceNumber(89856), CacheWrite: priceNumber(0), Output: priceNumber(30)}
	usd, id, ok := catalog.EstimateUsagePrice(sample)
	if !ok || usd != "0.0115656" || id != "openai/model" {
		t.Fatal(usd, id, ok)
	}
	// Two ordinary requests do not become a long-context request when summed.
	sample.Input = priceNumber(200000)
	sample.CacheRead = priceNumber(100000)
	sample.CacheWrite = priceNumber(20000)
	sample.Output = priceNumber(1000)
	usd, _, ok = catalog.EstimateUsagePrice(sample)
	if !ok || usd != "0.23" || AddUsagePrice(usd, usd) != "0.46" {
		t.Fatal(usd, ok)
	}
	sample.Input = priceNumber(300000)
	usd, _, ok = catalog.EstimateUsagePrice(sample)
	if !ok || usd != "0.855" {
		t.Fatal(usd, ok)
	}
}

func TestUsagePricingUnknownAndExactAggregation(t *testing.T) {
	catalog := UsagePrices{Data: []UsagePriceModel{{ID: "vendor/model", Pricing: UsageRates{Prompt: "0.0000000000001", Completion: "0"}}}}
	sample := protocol.TokenUsageSample{Model: "model", Input: priceNumber(1), CacheRead: priceNumber(0), CacheWrite: priceNumber(0), Output: priceNumber(0)}
	usd, _, ok := catalog.EstimateUsagePrice(sample)
	if !ok || AddUsagePrice(usd, usd) != "0.0000000000002" {
		t.Fatal(usd, ok)
	}
	sample.CacheRead = nil
	if _, _, ok = catalog.EstimateUsagePrice(sample); ok {
		t.Fatal("missing cache counter priced as zero")
	}
	sample.CacheRead = priceNumber(0)
	catalog.Data = append(catalog.Data, UsagePriceModel{ID: "other/model", Pricing: catalog.Data[0].Pricing})
	if _, _, ok = catalog.EstimateUsagePrice(sample); ok {
		t.Fatal("ambiguous slug guessed")
	}
	sample.Model = "vendor/model"
	if _, _, ok = catalog.EstimateUsagePrice(sample); !ok {
		t.Fatal("exact identity rejected")
	}
	sample.CacheRead = priceNumber(2)
	if _, _, ok = catalog.EstimateUsagePrice(sample); ok {
		t.Fatal("cache exceeding inclusive input accepted")
	}
}
