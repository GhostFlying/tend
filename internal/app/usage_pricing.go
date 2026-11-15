package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/store"
)

func (a *App) RecordUsagePriceCatalog(ctx context.Context, raw json.RawMessage) error {
	catalog, err := domain.ParseUsagePrices(raw)
	if err != nil {
		return fmt.Errorf("decode public usage quotes: %w", err)
	}
	if len(catalog.Data) == 0 {
		return fmt.Errorf("public usage quote catalog is empty")
	}
	// Keep only the latest successful catalog identities and rates. Neither
	// provider descriptions nor old catalog/valuation snapshots are retained.
	canonical, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	ctx = store.WithWriteOperation(ctx, "usage_prices.refresh")
	return a.write(ctx, func(tx *store.Tx) error {
		defer tx.ObservePhase(store.WriteUsageQuotes)()
		var previousRaw string
		var previous domain.UsagePrices
		err := tx.Q().QueryRowContext(ctx, `SELECT catalog_json FROM token_usage_latest_prices WHERE singleton=1`).Scan(&previousRaw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			previous, err = domain.ParseUsagePrices([]byte(previousRaw))
			if err != nil {
				return fmt.Errorf("decode retained usage quotes: %w", err)
			}
		}
		var affected []string
		if previousRaw != string(canonical) {
			rows, err := tx.Q().QueryContext(ctx, `SELECT DISTINCT model FROM token_usage_contexts`)
			if err != nil {
				return err
			}
			for rows.Next() {
				var model string
				if err = rows.Scan(&model); err != nil {
					break
				}
				if !previous.SameModelQuote(catalog, model) {
					affected = append(affected, model)
				}
			}
			if err == nil {
				err = rows.Err()
			}
			closeErr := rows.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}

		result, err := tx.Exec(ctx, `INSERT INTO token_usage_latest_prices(singleton,collected_at,catalog_json) VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET collected_at=excluded.collected_at,catalog_json=excluded.catalog_json WHERE token_usage_latest_prices.collected_at IS NOT excluded.collected_at OR token_usage_latest_prices.catalog_json IS NOT excluded.catalog_json`, a.now().UnixMilli(), string(canonical))
		if err != nil {
			return err
		}
		// The index chain starts with shared model metadata, not every retained
		// request. The existing projection settles these identities in this commit.
		for _, model := range affected {
			if _, err := tx.Exec(ctx, usagePriceAffectedSQL, model); err != nil {
				return err
			}
		}
		changed, err := result.RowsAffected()
		if changed > 0 {
			// Even unchanged rates have a new successful retrieval instant.
			tx.MarkChanged("token_usage")
		}
		return err
	})
}

const usagePriceAffectedSQL = `INSERT INTO token_usage_dirty
 SELECT c.source,c.request_id FROM token_usage_contexts m
 CROSS JOIN token_usage_facts f ON f.context_id=m.context_id
 CROSS JOIN token_usage_current c ON c.fact_seq=f.seq
 WHERE m.model=? ON CONFLICT(source,request_id) DO NOTHING`
