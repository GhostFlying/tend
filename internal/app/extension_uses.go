package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Publication validates each currently declared category/Agent pair. It never
// combines the Agent's unrelated categories or invents cross-category uses.
func extensionValidationUses(ctx context.Context, q store.Querier, r protocol.ExtensionRelease) ([]extensions.ValidationUse, error) {
	projections := []protocol.AgentInventoryProjection{}
	rows, err := q.QueryContext(ctx, `SELECT projection_json FROM agent_inventory_versions v WHERE server_revision=(SELECT MAX(server_revision) FROM agent_inventory_versions x WHERE x.host_id=v.host_id)`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var projection protocol.AgentInventoryProjection
		if err = json.Unmarshal([]byte(raw), &projection); err != nil {
			_ = rows.Close()
			return nil, err
		}
		projections = append(projections, projection)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	hosted, err := store.GetHostedConfiguration(ctx, q, 0)
	if err == nil {
		projections = append(projections, hosted.Public)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	out := []extensions.ValidationUse{}
	for _, projection := range projections {
		for _, agent := range projection.Agents {
			for _, category := range agent.Categories {
				if !slices.ContainsFunc(r.Catalog.Categories, func(b protocol.ExtensionBinding) bool { return b.CategoryID == category }) {
					continue
				}
				out = append(out, extensions.ValidationUse{CategoryID: category, Agent: agent.Identity})
			}
		}
	}
	return out, nil
}
