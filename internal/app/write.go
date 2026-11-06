package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/store"
)

// write is the common app transaction boundary, including internal maintenance.
func (a *App) write(ctx context.Context, fn func(*store.Tx) error) error {
	return a.store.Write(ctx, fn)
}
