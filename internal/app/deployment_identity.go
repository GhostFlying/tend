package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// DeploymentID returns this App's immutable ledger identity. Only a successful
// read or committed initialization is cached. A new App reloads the identity;
// replacing a live App's ledger is not a supported restore boundary.
func (a *App) DeploymentID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id := a.deploymentIdentity.Load(); id != nil {
		return *id, nil
	}
	// Waiting for the first load must honor this caller's cancellation. A
	// failed load releases the gate without publishing or retaining an error.
	select {
	case a.deploymentIdentityInit <- struct{}{}:
		defer func() { <-a.deploymentIdentityInit }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if id := a.deploymentIdentity.Load(); id != nil {
		return *id, nil
	}
	id, err := a.loadDeploymentID(ctx)
	if err != nil {
		return "", err
	}
	a.deploymentIdentity.Store(&id)
	return id, nil
}

func (a *App) loadDeploymentID(ctx context.Context) (string, error) {
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
