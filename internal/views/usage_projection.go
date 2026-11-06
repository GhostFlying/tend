package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// UsageBucketWidth is shared by persisted summaries and exact window edges.
const UsageBucketWidth int64 = 4 * 60 * 60 * 1000

// UsageDimension contains only grouping and filtering metadata. Request
// identities and counters live in the contributions, not in summary keys.
type UsageDimension struct {
	Record                          protocol.TokenUsageRecord
	BindingRun, BindingConversation string
}

// UsageContribution is a read-only derivation for the app's write transaction.
type UsageContribution struct {
	Source, RequestID string
	Record            protocol.TokenUsageRecord
	Dimension         UsageDimension
	Visible           bool
}

type UsageAggregate struct {
	Record          protocol.TokenUsageRecord
	Totals          protocol.TokenUsageTotals
	FirstAt, LastAt *int64
}

func UsageRecordTotals(r protocol.TokenUsageRecord) protocol.TokenUsageTotals {
	var t protocol.TokenUsageTotals
	addUsageRecord(&t, r)
	return t
}

// MergeUsageTotals preserves known-field coverage and exact decimal prices.
func MergeUsageTotals(t *protocol.TokenUsageTotals, other protocol.TokenUsageTotals) {
	t.Requests += other.Requests
	t.Final += other.Final
	t.Input += other.Input
	t.InputKnown += other.InputKnown
	t.Output += other.Output
	t.OutputKnown += other.OutputKnown
	t.CacheRead += other.CacheRead
	t.CacheReadKnown += other.CacheReadKnown
	t.CacheWrite += other.CacheWrite
	t.CacheWriteKnown += other.CacheWriteKnown
	t.Total += other.Total
	t.TotalKnown += other.TotalKnown
	if other.EstimatedRequests > 0 {
		t.EstimatedUSD = domain.AddUsagePrice(t.EstimatedUSD, other.EstimatedUSD)
		t.EstimatedRequests += other.EstimatedRequests
	}
}

// UsageProjectionInputs resolves only queued current identities. It does not
// mutate either the ledger or the projection; the app owns both writes.
func UsageProjectionInputs(ctx context.Context, q store.Querier) ([]UsageContribution, error) {
	_, prices, err := usagePrices(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT f.source,f.request_id,f.seq,f.subject,f.role,f.received_at,
 (c.conflict OR `+store.UsagePendingOverlapSQLFor("f")+`),f.payload_json,
 COALESCE(b.run_id,''),COALESCE(b.conversation_id,''),COALESCE(b.attribution_conflict,0),
 `+store.UsageLegacyLinkedSQL("f.seq")+`
 FROM token_usage_dirty d JOIN token_usage_current c USING(source,request_id)
 JOIN token_usage_facts f ON f.seq=c.fact_seq
 LEFT JOIN token_usage_bindings b ON b.source=f.source AND b.request_id=f.request_id
 ORDER BY f.seq DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []UsageContribution
	resolver := newUsageResolver(q)
	// Dirty batches are small during normal writes. Indexed lookups avoid
	// scanning unrelated historical facts and sources for each new request.
	resolver.runsLoaded, resolver.actorsLoaded = true, true
	cache := map[string]protocol.UsageAttribution{}
	for rows.Next() {
		var c UsageContribution
		r := &c.Record
		var raw string
		var bindingConflict, linked bool
		if err = rows.Scan(&c.Source, &c.RequestID, &r.Seq, &r.Subject, &r.Role, &r.ReceivedAt, &r.Conflict, &raw, &c.Dimension.BindingRun, &c.Dimension.BindingConversation, &bindingConflict, &linked); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r.TokenUsageSample); err != nil {
			return nil, fmt.Errorf("decode usage fact: %w", err)
		}
		if bindingConflict {
			c.Dimension.BindingRun, c.Dimension.BindingConversation = "", ""
		}
		keyRaw, err := json.Marshal([]any{r.Subject, r.Role, r.Purpose, c.Dimension.BindingRun, c.Dimension.BindingConversation, bindingConflict, r.ThreadID, r.TurnID, r.RootTurnID, r.ParentThreadID, r.SessionID})
		if err != nil {
			return nil, err
		}
		key := string(keyRaw)
		attribution, found := cache[key]
		if !found {
			attribution = resolver.attribution(ctx, *r)
			cache[key] = attribution
		}
		r.Attribution, r.Role = attribution, attribution.Role
		if usd, model, ok := prices.EstimateUsagePrice(r.TokenUsageSample); ok {
			r.Estimate = &protocol.UsageEstimate{USD: usd, ModelID: model}
		}
		c.Visible = !linked
		c.Dimension.Record = protocol.TokenUsageRecord{Subject: r.Subject, Role: r.Role, Attribution: r.Attribution, TokenUsageSample: protocol.TokenUsageSample{
			SessionID: r.SessionID, ParentSessionID: r.ParentSessionID, Model: r.Model, Provider: r.Provider, NativeVersion: r.NativeVersion,
		}}
		if r.RecordID != "" {
			c.Dimension.Record.RecordID = "native"
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (d UsageDimension) matches(model, subject string, filter UsageFilter) bool {
	r := d.Record
	a := r.Attribution
	if subject != "" {
		match := r.Subject == subject || (d.BindingRun != "" && "run:"+d.BindingRun == subject) || (d.BindingConversation != "" && "conversation:"+d.BindingConversation == subject)
		if r.RecordID != "" && strings.HasPrefix(subject, "run:") {
			match = a.RunID == strings.TrimPrefix(subject, "run:")
		}
		if !match {
			return false
		}
	}
	if model != "" && usageModel(r.Model) != model {
		return false
	}
	if (filter.Agent != "" && a.Agent != filter.Agent && a.AgentID != filter.Agent) || (filter.Role != "" && a.Role != filter.Role) || (filter.Session != "" && sessionID(r) != filter.Session) {
		return false
	}
	if filter.Issue != "" {
		match := filter.Issue == "_unassigned" && len(a.IssuePath) == 0
		for _, issue := range a.IssuePath {
			match = match || issue.ID == filter.Issue
		}
		if !match {
			return false
		}
	}
	return true
}

func usageModel(model string) string {
	if model == "" {
		return "实际模型未提供"
	}
	return model
}

func usagePrices(ctx context.Context, q store.Querier) (protocol.UsagePricing, domain.UsagePrices, error) {
	p := protocol.UsagePricing{Source: "OpenRouter", Currency: "USD"}
	var raw string
	var prices domain.UsagePrices
	err := q.QueryRowContext(ctx, `SELECT collected_at,catalog_json FROM token_usage_price_catalogs ORDER BY collected_at DESC LIMIT 1`).Scan(&p.QuotedAt, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, prices, nil
	}
	if err != nil {
		return p, prices, err
	}
	prices, err = domain.ParseUsagePrices([]byte(raw))
	return p, prices, err
}
