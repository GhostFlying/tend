package app

import (
	"context"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The local controller reports only durable terminal records read from its
// configured supervisor. Retain each original batch even after latest advances.
func recordServerCancellationConfirmations(ctx context.Context, x *Exec, tx *store.Tx, observation ServerObservation) ([]Event, error) {
	var events []Event
	for _, id := range observation.ServerCancellations {
		batch, err := store.GetUpgrade(ctx, tx.Q(), id)
		if err != nil {
			return nil, err
		}
		if batch.View.Status != "cancelled" || batch.ServerCancellationConfirmed {
			continue
		}
		if observation.InstanceID == "" || batch.ServerInstance != observation.InstanceID {
			return nil, protocol.Errorf(protocol.CodeForbidden, "cancellation confirmation belongs to another server instance")
		}
		batch.ServerCancellationConfirmed = true
		batch.View.Revision++
		batch.View.UpdatedAt = x.Now
		if err := tx.PutUpgrade(ctx, batch); err != nil {
			return nil, err
		}
		events = append(events, upgradeEvent(batch))
	}
	return events, nil
}

// A fresh authenticated heartbeat acknowledges the actual original cancel
// marker. A future control revision or an unrelated ID supplies no evidence.
func recordRunnerCancellationConfirmation(ctx context.Context, tx *store.Tx, host string, runtime protocol.RunnerRuntime, now int64) error {
	id := runtime.ConfirmedCancelledUpgradeID
	if id == "" {
		return nil
	}
	control, err := store.GetHostControl(ctx, tx.Q(), host)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if control.CancelledUpgradeID != id || runtime.AppliedControlRevision != control.Revision {
		return nil
	}
	batch, err := store.GetUpgrade(ctx, tx.Q(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if batch.View.Status != "cancelled" {
		return nil
	}
	for i := range batch.Hosts {
		target := &batch.Hosts[i]
		if target.HostID != host || target.CancellationConfirmed {
			continue
		}
		target.CancellationConfirmed = true
		batch.View.Revision++
		batch.View.UpdatedAt = time.UnixMilli(now).UTC()
		return tx.PutUpgrade(ctx, batch)
	}
	return nil
}
