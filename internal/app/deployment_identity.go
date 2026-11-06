package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// DeploymentID reads the durable identity without acquiring the writer. Only a
// missing identity requires initialization, using the injected ID source.
func (a *App) DeploymentID(ctx context.Context) (string, error) {
	var id string
	read := func(q store.Querier) error {
		return q.QueryRowContext(ctx, `SELECT COALESCE((SELECT deployment_id FROM deployment_identity WHERE singleton=1),'')`).Scan(&id)
	}
	if err := a.store.Read(ctx, func(tx *store.ReadTx) error { return read(tx.Q()) }); err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	// The read transaction has ended. Recheck under the writer so concurrent
	// first callers cannot create or return different identities.
	err := a.write(ctx, func(tx *store.Tx) error {
		if err := read(tx.Q()); err != nil {
			return err
		}
		if id != "" {
			return nil
		}
		id = a.ids.New(protocol.Prefix("deployment"))
		_, err := tx.Exec(ctx, `INSERT INTO deployment_identity(singleton,deployment_id) VALUES(1,?)`, id)
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
