package app

import (
	"context"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// validateOrdinaryAgent checks identity and lifecycle, not delegation-role
// membership or observed adapter health. An unavailable entry remains an
// explicit scheduling blocker; it never selects a different transport.
func validateOrdinaryAgent(ctx context.Context, q store.Querier, x *Exec, ref string) error {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || !protocol.AgentInventoryID(parts[0]) || !protocol.AgentInventoryID(parts[1]) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "agent_id requires an environment/local identity")
	}
	if x.hostedConfiguration != nil && parts[0] == x.hostedConfiguration.Public().ExecutionEnvironmentID {
		if x.hostedConfiguration.Public().FormatVersion >= 3 && parts[1] == x.hostedConfiguration.MainAgentID() {
			return protocol.Errorf(protocol.CodeForbidden, "the server main identity is reserved for top-level coordination")
		}
		if x.hostedConfiguration.Public().FormatVersion < 3 {
			// Historical hosted assignment retains its original purpose contract.
			_, err := x.hostedConfiguration.Select(parts[1], agentconfig.IssueManager)
			return err
		}
		for _, a := range x.hostedConfiguration.Public().Agents {
			if a.Identity.LocalID == parts[1] && a.Enabled {
				return nil
			}
		}
		return protocol.Errorf(protocol.CodeNotFound, "ordinary Agent is not enabled in the registered environment")
	}
	inv, err := store.GetAgentInventory(ctx, q, parts[0])
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeNotFound, "Agent environment is not registered")
	}
	if err != nil {
		return err
	}
	for _, a := range inv.Projection.Agents {
		if a.Identity.LocalID == parts[1] && a.Enabled {
			return nil
		}
	}
	return protocol.Errorf(protocol.CodeNotFound, "ordinary Agent is not enabled in the registered environment")
}
