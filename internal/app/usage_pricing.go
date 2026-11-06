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
	// Keep only catalog identities and rates, not descriptions or other data.
	canonical, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	return a.write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO token_usage_price_catalogs(collected_at,catalog_json) VALUES(?,?) ON CONFLICT(collected_at) DO NOTHING`, a.now().UnixMilli(), string(canonical))
		return err
	})
}
