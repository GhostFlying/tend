package app

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
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
	bindings, err := concurrencyEnvironmentBindings(ctx, q, host)
	if err != nil {
		return result, err
	}
	result.ConcurrencyBindings = bindings
	h, err := store.GetHost(ctx, q, host)
	if err != nil {
		return result, errNotFound(err, "host does not exist")
	}
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if err == nil {
		if runtimeSupportsAgentExecution(&runtime.Runtime) {
			result.Executors = runtime.Runtime.AgentExecutors
			result.ContinuationSupported = slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityAgentContinuation)
			if result.ContinuationSupported {
				result.Continuations = runtime.Runtime.AgentContinuations
			}
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
	result.RetainedExecutions, err = store.RequiredAgentExecutions(ctx, q, host)
	if err != nil {
		return result, err
	}
	type snapshotKey struct {
		Identity             protocol.AgentIdentity
		Snapshot, PublicHash string
	}
	seen := map[snapshotKey]int{}
	for _, pin := range result.RetainedExecutions {
		inv, e := store.GetAgentInventoryAt(ctx, q, pin)
		if errors.Is(e, store.ErrNotFound) {
			continue
		}
		if e != nil {
			return result, e
		}
		for _, a := range inv.Projection.Agents {
			if a.Identity != pin.Identity || a.Revision != pin.AgentRevision {
				continue
			}
			key := snapshotKey{a.Identity, pin.SnapshotID, pin.PublicSHA256}
			if index, ok := seen[key]; ok {
				if pin.InventoryRevision > result.RetainedDefinitions[index].Pin.InventoryRevision {
					result.RetainedDefinitions[index].Pin = pin
				}
				continue
			}
			seen[key] = len(result.RetainedDefinitions)
			result.RetainedDefinitions = append(result.RetainedDefinitions, protocol.RetainedAgentDefinition{Pin: pin, Definition: a, Runtimes: slices.Clone(inv.Projection.Runtimes)})
		}
	}
	if result.Projection != nil {
		result.Capabilities, _, err = views.CapabilityAgentsForHost(ctx, q, host, now)
		if err != nil {
			return result, err
		}
		// Histories have their own bounded endpoint; keep only current evidence
		// and the most recent actual execution in the directory projection.
		for i := range result.Capabilities {
			v := &result.Capabilities[i]
			all := v.Evidence
			v.Evidence = nil
			var latestExecution *protocol.ExecutorCapabilityEvidence
			for j := range all {
				e := all[j]
				if slices.Contains(v.Current.EvidenceIDs, e.EvidenceID) {
					v.Evidence = append(v.Evidence, e)
				}
				if e.Kind == "execution" {
					latestExecution = &e
				}
			}
			if latestExecution != nil {
				v.Evidence = append(v.Evidence, *latestExecution)
			}
		}
	}
	return result, nil
}
