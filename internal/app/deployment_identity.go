package app

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// DeploymentID initializes the durable identity once, using the injected ID source.
func (a *App) DeploymentID(ctx context.Context) (string, error) {
	var id string
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT deployment_id FROM deployment_identity WHERE singleton=1),'')`).Scan(&id); err != nil {
			return err
		}
		if id != "" {
			return nil
		}
		id = a.ids.New(protocol.Prefix("deployment"))
		_, err := tx.Exec(ctx, `INSERT INTO deployment_identity(singleton,deployment_id) VALUES(1,?)`, id)
		return err
	})
	return id, err
}
