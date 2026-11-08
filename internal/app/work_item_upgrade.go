package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func workItemUpgradeEvidence(ctx context.Context, x *Exec, q store.Querier, id string) (*protocol.UpgradeEvidence, error) {
	proof, err := store.UpgradeInstallationEvidence(ctx, q, id, x.Now.UnixMilli())
	return proof, errNotFound(err, "upgrade batch does not exist")
}
