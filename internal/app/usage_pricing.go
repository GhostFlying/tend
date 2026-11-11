package app

import (
	"context"
	"encoding/json"
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
	return a.write(ctx, func(tx *store.Tx) error {
		result, err := tx.Exec(ctx, `INSERT INTO token_usage_latest_prices(singleton,collected_at,catalog_json) VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET collected_at=excluded.collected_at,catalog_json=excluded.catalog_json WHERE token_usage_latest_prices.collected_at IS NOT excluded.collected_at OR token_usage_latest_prices.catalog_json IS NOT excluded.catalog_json`, a.now().UnixMilli(), string(canonical))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if changed > 0 {
			// Even unchanged rates have a new successful retrieval instant.
			tx.MarkChanged("token_usage")
		}
		return err
	})
}
