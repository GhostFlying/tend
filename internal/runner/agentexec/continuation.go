package agentexec

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/agentconfig"
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
	if !p.mu.TryLock() {
		return nil, nil
	}
	defer p.mu.Unlock()
	p.retainedMu.Lock()
	defer p.retainedMu.Unlock()
	out := slices.Clone(p.retainedObserved)
	for i := range out {
		if out[i].Pin.Purpose == "issue_manager" {
			continue
		}
		// Resolve retains the factory for native stop/recovery after a refusal.
		// Selection must reflect the live refusal even before the next refresh.
		if e := p.snapshots[out[i].Pin.SnapshotID][out[i].Pin.Identity.LocalID]; e != nil && e.reason != "" && !(nativeCodex(e) && canProbe(e.reason)) {
			out[i].Available = false
			out[i].Reason = e.reason
		} else if e != nil && e.environmentUnavailable && !nativeCodex(e) {
			out[i].Available = false
			out[i].Reason = "environment_unavailable"
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
			_, err = p.ResolveManagerRun(ctx, pin)
		} else {
			resolved, resolveErr := p.Resolve(ctx, pin)
			err = resolveErr
			if err == nil {
				p.mu.Lock()
				e := p.snapshots[pin.SnapshotID][pin.Identity.LocalID]
				if e != nil && !nativeCodex(e) && p.options.Environments.Resolve(e.input.Runtime.ID) != nil {
					err = p.environmentReady(ctx, e.input.Runtime.ID)
					e.environmentUnavailable = err != nil
					if err == nil && canProbe(e.reason) {
						err = p.initialize(ctx, e)
					}
				}
				factoryMissing := e == nil || (pin.Extensions == (protocol.ExtensionExecutionPin{}) && e.workers == nil) || (pin.Extensions != (protocol.ExtensionExecutionPin{}) && resolved.Workers == nil)
				if factoryMissing || e.reason != "" && !(nativeCodex(e) && canProbe(e.reason)) || e.environmentUnavailable && !nativeCodex(e) {
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

// Native Codex factories restore the run's persisted binding at startup. A
// current mapping probe cannot determine whether that original home is ready.
func nativeCodex(e *entry) bool {
	return e.input.Runtime.Kind == agentconfig.AppServerWorker && e.input.Runtime.CLI == "codex"
}
