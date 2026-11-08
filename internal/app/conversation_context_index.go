package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// ConversationContextIndex does not admit a turn or acknowledge source review.
func (a *App) ConversationContextIndex(ctx context.Context, id string, query protocol.ConversationContextQuery) (protocol.ConversationContextIndex, error) {
	var v protocol.ConversationContextIndex
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		v, err = views.ConversationContextIndex(ctx, tx.Q(), id, query)
		return err
	})
	return v, err
}
