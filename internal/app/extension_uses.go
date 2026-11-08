package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
			if !managedExtensionAgent(projection, agent) {
				continue
			}
			for _, category := range agent.Categories {
				if !slices.ContainsFunc(r.Catalog.Categories, func(b protocol.ExtensionBinding) bool { return b.CategoryID == category }) && !slices.ContainsFunc(r.Catalog.Agents, func(b protocol.ExtensionBinding) bool { return b.Agent == agent.Identity }) {
					continue
				}
				out = append(out, extensions.ValidationUse{CategoryID: category, Agent: agent.Identity})
			}
		}
	}
	return out, nil
}

// managedExtensionAgent includes an owner runtime even when the worker uses a
// different CLI. Disabled definitions are not publication targets.
func managedExtensionAgent(p protocol.AgentInventoryProjection, a protocol.AgentInventoryDefinition) bool {
	if !a.Enabled {
		return false
	}
	for _, r := range p.Runtimes {
		if r.Enabled && r.CLI == "codex" && (r.RuntimeID == a.Binding.RuntimeID || a.OwnerBinding != nil && r.RuntimeID == a.OwnerBinding.RuntimeID) {
			return true
		}
	}
	return false
}

// ExtensionAffectedAgents previews changes for current Codex definitions only;
// old inventory versions and unrelated catalog changes are not adoptions.
func ExtensionAffectedAgents(ctx context.Context, q store.Querier, next protocol.ExtensionRelease, previous *protocol.ExtensionRelease) ([]protocol.AgentIdentity, error) {
	scope := next
	scope.Catalog.Categories = slices.Clone(next.Catalog.Categories)
	if previous != nil {
		for _, b := range previous.Catalog.Categories {
			if !slices.ContainsFunc(scope.Catalog.Categories, func(v protocol.ExtensionBinding) bool { return v.CategoryID == b.CategoryID }) {
				scope.Catalog.Categories = append(scope.Catalog.Categories, b)
			}
		}
	}
	uses, err := extensionValidationUses(ctx, q, scope)
	if err != nil {
		return nil, err
	}
	affected := map[protocol.AgentIdentity]bool{}
	for _, use := range uses {
		if previous == nil {
			affected[use.Agent] = true
			continue
		}
		before, err := extensions.Resolve(*previous, use.CategoryID, use.Agent)
		if err != nil {
			return nil, err
		}
		after, err := extensions.Resolve(next, use.CategoryID, use.Agent)
		if err != nil {
			return nil, err
		}
		// A release ID changes even when this execution's effective layers do not.
		before.Pin.ReleaseID, before.Pin.EffectiveSHA256 = "", ""
		after.Pin.ReleaseID, after.Pin.EffectiveSHA256 = "", ""
		if !reflect.DeepEqual(before, after) {
			affected[use.Agent] = true
		}
	}
	out := make([]protocol.AgentIdentity, 0, len(affected))
	for id := range affected {
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b protocol.AgentIdentity) int {
		if a.ExecutionEnvironmentID < b.ExecutionEnvironmentID {
			return -1
		}
		if a.ExecutionEnvironmentID > b.ExecutionEnvironmentID {
			return 1
		}
		if a.LocalID < b.LocalID {
			return -1
		}
		if a.LocalID > b.LocalID {
			return 1
		}
		return 0
	})
	return out, nil
}
