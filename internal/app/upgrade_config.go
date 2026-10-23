package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A restarted candidate may sync the same configuration. Changing participants
// or their scheduling/authentication facts requires finishing the current batch.
func checkUpgradeHostConfig(ctx context.Context, q store.Querier, next []HostConfig) error {
	r, err := store.LatestUpgrade(ctx, q)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.View.Status != "running" && r.View.Status != "attention" {
		return nil
	}
	old, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return err
	}
	changed := len(old) != len(next)
	for _, n := range next {
		i := slices.IndexFunc(old, func(h store.HostRow) bool { return h.HostID == n.HostID })
		if i < 0 {
			changed = true
			break
		}
		h := old[i]
		if h.OS != n.OS || h.Arch != n.Arch || h.Slots != n.Slots || h.TokenHash != n.TokenHash || h.Worker != n.Worker || !slices.Equal(h.Labels, n.Labels) || !slices.Equal(h.Repos, n.Repos) || !slices.Equal(h.Nodes, n.Nodes) {
			changed = true
			break
		}
	}
	if changed {
		return protocol.Errorf(protocol.CodeInvalidTransition, "host configuration is fixed until the upgrade is completed or cancelled")
	}
	return nil
}
