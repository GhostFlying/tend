package domain

import (
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func priceNumber(n int64) *int64 { return &n }

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
