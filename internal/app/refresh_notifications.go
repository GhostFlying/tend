package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/store"
)

// AcknowledgeRefreshes confirms only transport progress. Current scoped
// versions remain available; this neither acknowledges business understanding
// nor discards events or source facts.
func (a *App) AcknowledgeRefreshes(ctx context.Context, through int64) error {
	return a.store.Write(store.WithWriteOperation(ctx, "events.acknowledge_refreshes"), func(tx *store.Tx) error {
		return tx.AcknowledgeRefreshes(ctx, through)
	})
}
