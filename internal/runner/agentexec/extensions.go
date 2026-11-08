package agentexec

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (p *Pool) prepareManaged(ctx context.Context, input BuildInput, category string, identity protocol.AgentIdentity) (protocol.ExtensionExecutionPin, *codexenv.Binding, error) {
	if input.Runtime.Kind != agentconfig.AppServerWorker || input.Runtime.CLI != "codex" {
		return protocol.ExtensionExecutionPin{}, nil, errors.New("managed extensions require native Codex")
	}
	if err := p.options.Extensions.PrepareCurrent(ctx, input.Runtime.ID, category, identity); err != nil {
		return protocol.ExtensionExecutionPin{}, nil, err
	}
	pin, ready := p.options.Extensions.Current(input.Runtime.ID, category, identity)
	if !ready {
		return pin, nil, errors.New("managed combination is not ready")
	}
	binding, err := p.options.Extensions.Binding(ctx, pin, input.Runtime.ID)
	return pin, binding, err
}

func (p *Pool) refreshManaged(ctx context.Context, e *entry, host string) (bool, error) {
	if p.options.Extensions == nil {
		return false, nil
	}
	worker := p.options.Extensions.Consumer.Enabled(e.input.Runtime.ID)
	owner := e.ownerInput != nil && p.options.Extensions.Consumer.Enabled(e.ownerInput.Runtime.ID)
	if !worker && !owner {
		return false, nil
	}
	e.managedWorkerReady = map[string]bool{}
	e.managedOwnerReady = map[string]bool{}
	if !e.input.Agent.Enabled || !e.input.Runtime.Enabled || e.reason != "" && !canProbe(e.reason) {
		return true, nil
	}
	identity := protocol.AgentIdentity{ExecutionEnvironmentID: host, LocalID: e.input.Agent.LocalID}
	var pending error
	anyWorker := false
	anyOwner := false
	if !worker {
		pending = errors.Join(pending, p.initialize(ctx, e))
	}
	for _, category := range e.input.Agent.Categories {
		if worker {
			pin, binding, err := p.prepareManaged(ctx, e.input, category, identity)
			if err == nil {
				input := e.input
				input.Environment = binding
				key := managedWorkerKey(input, pin)
				workers := p.managedWorkers[key]
				if workers == nil || e.reason != "" {
					if err = p.options.Probe(ctx, input); err == nil {
						if workers == nil {
							workers, err = p.options.Build(input)
						}
						if err == nil {
							p.managedWorkers[key] = workers
						}
					}
				}
				if err == nil {
					e.workers = workers
					e.managedWorkerReady[category] = true
					anyWorker = true
				}
			}
			pending = errors.Join(pending, err)
		}
		if owner && managerPurpose(e) {
			_, binding, err := p.prepareManaged(ctx, *e.ownerInput, category, identity)
			if err == nil {
				input := *e.ownerInput
				input.Environment = binding
				copy := *e
				copy.ownerInput = &input
				pin, ready := p.options.Extensions.Current(input.Runtime.ID, category, identity)
				key := managedWorkerKey(input, pin)
				if ready && (p.managedOwners[key] || p.probeManager(ctx, &copy)) {
					p.managedOwners[key] = true
					e.managedOwnerReady[category] = true
					anyOwner = true
				} else {
					err = errors.New("managed owner isolation is not verified")
				}
			}
			pending = errors.Join(pending, err)
		}
	}
	if worker {
		e.environmentUnavailable = !anyWorker
		if anyWorker {
			e.reason = ""
		}
	}
	if owner {
		e.managerReady = anyOwner
	} else if managerPurpose(e) {
		e.managerReady = p.probeManager(ctx, e)
	}
	return true, pending
}
