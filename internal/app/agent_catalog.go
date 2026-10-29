package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AgentCategories reads current classifications, not category history or roles.
func (a *App) AgentCategories(ctx context.Context, state, after string, limit int) (protocol.AgentCategoryPage, error) {
	if state == "" {
		state = "active"
	}
	if state != "active" && state != "retired" && state != "all" {
		return protocol.AgentCategoryPage{}, protocol.Errorf(protocol.CodeInvalidRequest, "state must be active, retired or all")
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return protocol.AgentCategoryPage{}, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100")
	}
	var page protocol.AgentCategoryPage
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		page, err = store.ListAgentCategories(ctx, tx.Q(), state, after, limit)
		return err
	})
	return page, err
}

// RunnerAgentCatalog reads accepted public facts. Reading is not a heartbeat,
// reload, capability probe or inventory registration.
func (a *App) RunnerAgentCatalog(ctx context.Context, host string) (protocol.RunnerAgentCatalog, error) {
	var result protocol.RunnerAgentCatalog
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		result, err = readRunnerAgentCatalog(ctx, tx.Q(), host, a.now().UnixMilli())
		return err
	})
	return result, err
}

func readRunnerAgentCatalog(ctx context.Context, q store.Querier, host string, now int64) (protocol.RunnerAgentCatalog, error) {
	result := protocol.RunnerAgentCatalog{HostID: host}
	h, err := store.GetHost(ctx, q, host)
	if err != nil {
		return result, errNotFound(err, "host does not exist")
	}
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if err == nil {
		if runtimeSupportsAgentExecution(&runtime.Runtime) {
			result.Executors = runtime.Runtime.AgentExecutors
		}
		result.Online = h.Status == store.HostActive && runtime.Online && now-runtime.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds()
		result.LastHeartbeatAtMillis = runtime.LastHeartbeatAt
	} else if !errors.Is(err, store.ErrNotFound) {
		return result, err
	}
	current, err := store.GetAgentInventory(ctx, q, host)
	if err == nil {
		result.Current, result.Projection = &current.Receipt, &current.Projection
	} else if !errors.Is(err, store.ErrNotFound) {
		return result, err
	}
	return result, nil
}
