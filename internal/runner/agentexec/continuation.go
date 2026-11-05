package agentexec

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ContinuationBackend remains available even when current definitions are empty.
func (p *Pool) ContinuationBackend() string { return p.options.BackendID }

func (p *Pool) SetRetained(pins []protocol.RunExecutionPin) {
	p.retainedMu.Lock()
	defer p.retainedMu.Unlock()
	if slices.Equal(p.retained, pins) {
		return
	}
	p.retained = slices.Clone(pins)
	p.retainedObserved = nil
}

func (p *Pool) ObserveRetained(ctx context.Context) ([]protocol.AgentExecutor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.retainedMu.Lock()
	out := slices.Clone(p.retainedObserved)
	p.retainedMu.Unlock()
	p.observationMu.RLock()
	defer p.observationMu.RUnlock()
	for i := range out {
		if out[i].Pin.Purpose == "issue_manager" {
			continue
		}
		// Resolve retains the factory for native stop/recovery after a refusal.
		// Selection must reflect the live refusal even before the next refresh.
		if e, ok := p.observations[out[i].Pin.SnapshotID][out[i].Pin.Identity.LocalID]; ok && e.reason != "" {
			out[i].Available = false
			out[i].Reason = e.reason
		}
	}
	return out, nil
}

// refreshRetained runs outside heartbeats. Probes create no model turn or leased
// worker; ResolveManager closes its temporary native permission probe itself.
func (p *Pool) refreshRetained(ctx context.Context) error {
	p.retainedMu.Lock()
	pins := slices.Clone(p.retained)
	p.retainedMu.Unlock()
	observations := []protocol.AgentExecutor{}
	var retry error
	for _, pin := range pins {
		var err error
		if pin.Purpose == "issue_manager" {
			_, err = p.ResolveManager(ctx, pin)
		} else {
			_, err = p.Resolve(ctx, pin)
			if err == nil {
				p.mu.Lock()
				e := p.snapshots[pin.SnapshotID][pin.Identity.LocalID]
				if e == nil || e.reason != "" {
					err = errors.New("original worker is unavailable")
				}
				p.mu.Unlock()
			}
		}
		e := protocol.AgentExecutor{Pin: pin, Available: err == nil}
		if err != nil {
			e.Reason = "original_execution_unavailable"
			retry = errors.Join(retry, errors.New("retained execution is unavailable"))
		}
		observations = append(observations, e)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	p.retainedMu.Lock()
	defer p.retainedMu.Unlock()
	if slices.Equal(p.retained, pins) {
		p.retainedObserved = observations
	}
	return retry
}
