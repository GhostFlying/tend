package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// checkRequiredHost validates stable configuration only: transient availability
// and free capacity belong to scheduling, so offline hosts remain valid targets.
func checkRequiredHost(ctx context.Context, q store.Querier, spec domain.Spec, resource *store.WorkspaceRow) error {
	if resource != nil && resource.BackendID != "" && spec.AgentSelection == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "spec.agent_selection: this workspace uses named execution; retain or provide both role categories")
	}
	id := spec.Requirements.HostID
	if id == "" {
		return nil
	}
	host, err := store.GetHost(ctx, q, id)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "spec.requirements.host_id: unknown host %q; use repos_list to find registered hosts", id)
	}
	if err != nil {
		return err
	}
	if host.Status != store.HostActive {
		return protocol.Errorf(protocol.CodeInvalidRequest, "spec.requirements.host_id: host %q is retired", id)
	}
	if resource != nil && resource.HostID != "" && resource.HostID != id {
		return protocol.Errorf(protocol.CodeInvalidRequest, "spec.requirements.host_id: host %q conflicts with workspace host %q", id, resource.HostID)
	}
	return nil
}
