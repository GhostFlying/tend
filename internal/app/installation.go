package app

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/store"
)

// InstallationVersion observes the opened, migrated ledger without waiting for
// optional model sessions or reconciling business operations.
func (a *App) InstallationVersion(ctx context.Context) (version int64, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	})
	return version, err
}

// InstallationSupersededUpgrade identifies the old local installation intent.
// An external installer carries this exact identity into its durable receipt.
func (a *App) InstallationSupersededUpgrade(ctx context.Context) (id string, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		row, e := store.LatestUpgrade(ctx, tx.Q())
		if errors.Is(e, store.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if row.View.Status == "running" || row.View.Status == "attention" {
			id = row.View.UpgradeID
		}
		return nil
	})
	return
}
