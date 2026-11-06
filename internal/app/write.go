package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/store"
)

// write is the common app transaction boundary, including internal maintenance.
func (a *App) write(ctx context.Context, fn func(*store.Tx) error) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if err := refreshUsageProjection(ctx, tx); err != nil {
			return fmt.Errorf("refresh usage projection: %w", err)
		}
		return nil
	})
}
