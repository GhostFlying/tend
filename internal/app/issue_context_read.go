package app

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// ReadIssueContext reads a page without acknowledging sources or persisting a
// session cursor. Managed caller scope is checked separately in its read Tx.
func (a *App) ReadIssueContext(ctx context.Context, id string, query protocol.IssueContextQuery) (any, error) {
	var out any
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out, err = views.ReadCanonicalIssueContext(ctx, tx.Q(), id, query)
		return err
	})
	return out, err
}
