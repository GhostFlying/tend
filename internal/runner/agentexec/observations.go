package agentexec

import (
	"context"
	"maps"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

// entryObservation owns scalar readiness facts, never an adapter or mutable
// configuration. Native IO stays serialized by Pool.mu, independently of reads.
type entryObservation struct {
	worker                 protocol.WorkerKind
	workerSupported        bool
	workerReady            bool
	reason                 string
	environmentUnavailable bool
	nativeCodex            bool
	managerPurpose         bool
	managerReady           bool
	managerRuntime         protocol.AgentRuntimeReference
	managedWorkerReady     map[string]protocol.ExtensionExecutionPin
	managedOwnerReady      map[string]protocol.ExtensionExecutionPin
}

func snapshotObservations(entries map[string]*entry) map[string]entryObservation {
	out := make(map[string]entryObservation, len(entries))
	for id, e := range entries {
		worker := protocol.WorkerAppServer
		if e.input.Runtime.Kind == agentconfig.OrcaWorker || e.input.Runtime.Kind == agentconfig.CentralWorker {
			worker = protocol.WorkerOrca
		}
		o := entryObservation{
			worker: worker, workerSupported: workerAdapterSupported(e.input.Runtime.Kind),
			workerReady: e.reason == "" && e.workers != nil, reason: e.reason,
			environmentUnavailable: e.environmentUnavailable, nativeCodex: nativeCodex(e),
			managerPurpose: managerPurpose(e), managerReady: e.managerReady,
			managedWorkerReady: maps.Clone(e.managedWorkerReady), managedOwnerReady: maps.Clone(e.managedOwnerReady),
		}
		if o.managerPurpose {
			o.managerRuntime = protocol.AgentRuntimeReference{RuntimeID: e.ownerInput.Runtime.ID, Revision: e.ownerInput.Runtime.Revision}
		}
		out[id] = o
	}
	return out
}

// publishSnapshot is called while mu is held. Maps are replaced, never mutated
// after publication, and remain keyed by the original accepted snapshot.
func (p *Pool) publishSnapshot(id string, entries map[string]*entry) {
	observations := snapshotObservations(entries)
	p.observationMu.Lock()
	defer p.observationMu.Unlock()
	if p.observations == nil {
		p.observations = make(map[string]map[string]entryObservation)
	}
	p.observations[id] = observations
}

func (p *Pool) observeSnapshot(ctx context.Context, id string) (map[string]entryObservation, error) {
	p.observationMu.RLock()
	entries := p.observations[id]
	p.observationMu.RUnlock()
	if entries != nil {
		return entries, nil
	}
	// A heartbeat may encounter a new definition while an older probe is busy.
	// Read only its accepted definition: no adapter creation or readiness probe,
	// and no readiness borrowed from a different snapshot.
	definitions, err := p.readSnapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	return snapshotObservations(definitions), nil
}
