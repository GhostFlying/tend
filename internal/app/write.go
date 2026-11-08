package app

import (
	"context"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"

	"github.com/GhostFlying/tend/internal/store"
)

// write is the common app transaction boundary, including internal maintenance.
func (a *App) write(ctx context.Context, fn func(*store.Tx) error) error {
	changed, scheduling := false, false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if err := refreshUsageProjection(ctx, tx, func() error { tx.MarkChanged("token_usage"); return nil }); err != nil {
			return fmt.Errorf("refresh usage projection: %w", err)
		}
		if tx.Changed("token_usage") {
			if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "app", ObjectType: "token_usage", ObjectID: "all", Kind: "token_usage.changed", BodyJSON: `{}`, CreatedAt: a.now().UnixMilli()}); err != nil {
				return err
			}
			changed = true
		}
		scheduling = tx.Changed("concurrency")
		changed = changed || scheduling
		return nil
	})
	if err == nil && changed {
		a.changes.Notify()
		if scheduling {
			a.scheduling.Notify()
		}
	}
	return err
}
