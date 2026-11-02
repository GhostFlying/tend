package domain

import (
	"encoding/json"
	"math/big"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// UsagePrices is a current public catalog, separate from native measurement.
// It estimates text-token cost only, never a provider invoice or add-on cost.
type UsagePrices struct {
	Data []UsagePriceModel `json:"data"`
}
type UsagePriceModel struct {
	ID      string     `json:"id"`
	Pricing UsageRates `json:"pricing"`
}
type UsageRates struct {
	Prompt     string              `json:"prompt"`
	Completion string              `json:"completion"`
	CacheRead  string              `json:"input_cache_read"`
	CacheWrite string              `json:"input_cache_write"`
	Overrides  []UsageRateOverride `json:"overrides,omitempty"`
}
type UsageRateOverride struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
	CacheRead  string `json:"input_cache_read"`
	CacheWrite string `json:"input_cache_write"`
	MinPrompt  *int64 `json:"min_prompt_tokens"`
	UTCStart   *int   `json:"utc_start"`
	UTCEnd     *int   `json:"utc_end"`
	UTCDays    []int  `json:"utc_days"`
}

func ParseUsagePrices(raw []byte) (UsagePrices, error) {
	var c UsagePrices
	err := json.Unmarshal(raw, &c)
	return c, err
}

// EstimateUsagePrice requires an exact catalog ID or one unique native slug.
// Unknown cache counters or unsupported quote conditions remain unpriced.
func (c UsagePrices) EstimateUsagePrice(s protocol.TokenUsageSample) (string, string, bool) {
	var chosen *UsagePriceModel
	for i := range c.Data {
		m := &c.Data[i]
		if m.ID == s.Model {
			chosen = m
			break
		}
	}
	if chosen == nil && s.Model != "" {
		for i := range c.Data {
			m := &c.Data[i]
			_, slug, ok := strings.Cut(m.ID, "/")
			if ok && slug == s.Model {
				if chosen != nil {
					return "", "", false
				}
				chosen = m
			}
		}
	}
	if chosen == nil || s.Input == nil || s.Output == nil || s.CacheRead == nil || s.CacheWrite == nil {
		return "", "", false
	}
	rates := chosen.Pricing
	for _, override := range rates.Overrides {
		if override.UTCStart != nil || override.UTCEnd != nil || len(override.UTCDays) > 0 {
			return "", "", false
		}
		if override.MinPrompt == nil || *s.Input > *override.MinPrompt {
			if override.Prompt != "" {
				rates.Prompt = override.Prompt
			}
			if override.Completion != "" {
				rates.Completion = override.Completion
			}
			if override.CacheRead != "" {
				rates.CacheRead = override.CacheRead
			}
			if override.CacheWrite != "" {
				rates.CacheWrite = override.CacheWrite
			}
		}
	}
	uncached := *s.Input - *s.CacheRead - *s.CacheWrite
	if uncached < 0 {
		return "", "", false
	}
	total := new(big.Rat)
	for _, term := range []struct {
		n    int64
		rate string
	}{{uncached, rates.Prompt}, {*s.CacheRead, rates.CacheRead}, {*s.CacheWrite, rates.CacheWrite}, {*s.Output, rates.Completion}} {
		if term.n < 0 {
			return "", "", false
		}
		if term.n == 0 {
			continue
		}
		rate, ok := new(big.Rat).SetString(term.rate)
		if !ok || rate.Sign() < 0 {
			return "", "", false
		}
		total.Add(total, new(big.Rat).Mul(rate, new(big.Rat).SetInt64(term.n)))
	}
	// Decimal rates are exact. Keep sub-cent precision until display formatting.
	value := exactUsagePrice(total)
	return value, chosen.ID, value != ""
}

func AddUsagePrice(left, right string) string {
	a, _ := new(big.Rat).SetString(left)
	b, _ := new(big.Rat).SetString(right)
	if a == nil {
		a = new(big.Rat)
	}
	if b == nil {
		b = new(big.Rat)
	}
	return exactUsagePrice(new(big.Rat).Add(a, b))
}

// Finite decimal rates remain exact through every aggregation. Formatting to
// cents belongs to the view, after requests have been summed.
func exactUsagePrice(r *big.Rat) string {
	denominator := new(big.Int).Set(r.Denom())
	places := 0
	for _, factor := range []int64{2, 5} {
		n := 0
		for n < 256 {
			q, remainder := new(big.Int), new(big.Int)
			q.QuoRem(denominator, big.NewInt(factor), remainder)
			if remainder.Sign() != 0 {
				break
			}
			denominator = q
			n++
		}
		places = max(places, n)
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return ""
	}
	return r.FloatString(places)
}
