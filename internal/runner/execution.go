package runner

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// ExecutionResolver resolves accepted local snapshots, never current defaults.
type ExecutionResolver interface {
	Refresh(context.Context) error
	Observe(context.Context) ([]protocol.AgentExecutor, error)
	Resolve(context.Context, protocol.RunExecutionPin) (ResolvedExecution, error)
}

// CapabilityResolver supplies bounded, trusted adapter observations. It does not
// grant permission to run a model or prepare an unaccounted session.
type CapabilityResolver interface {
	ObserveCapability(context.Context, protocol.RunExecutionPin, protocol.CapabilityReference) (protocol.ExecutorCapabilityEvidence, error)
}

// ContinuationResolver probes only the pins the server still references. It
// keeps these observations separate from current selection candidates.
type ContinuationResolver interface {
	ContinuationBackend() string
	SetRetained([]protocol.RunExecutionPin)
	ObserveRetained(context.Context) ([]protocol.AgentExecutor, error)
}

type ResolvedExecution struct {
	// DeclaredCapabilities comes from the accepted snapshot resolved for this pin.
	DeclaredCapabilities []protocol.CapabilityReference

	Workers      harness.Workers
	Workspaces   Workspaces
	Instructions string
	Profile      *protocol.ExecutionProfile
}

// ManagerExecution opens the exact accepted local app-server configuration.
type ManagerExecution struct {
	Open         func(context.Context, string) (harness.Session, error)
	Instructions string
	Profile      protocol.ExecutionProfile
}
type ManagerExecutionResolver interface {
	ResolveManager(context.Context, protocol.RunExecutionPin) (ManagerExecution, error)
}

// ManagerReceiptExecutionResolver retains original configuration without probing
// readiness to start new business executions.
type ManagerReceiptExecutionResolver interface {
	ResolveManagerReceipt(context.Context, protocol.RunExecutionPin) (ManagerExecution, error)
}

func (r *Runner) resolveManagerExecution(ctx context.Context, as protocol.Assignment) (ManagerExecution, error) {
	if as.Execution == nil || as.Manager == nil {
		return ManagerExecution{}, errors.New("runner: manager executor is unavailable")
	}
	if as.Manager.RequireReceiptReader {
		resolver, ok := r.cfg.Executions.(ManagerReceiptExecutionResolver)
		if !ok {
			return ManagerExecution{}, errors.New("runner: original manager receipt resolver is unavailable")
		}
		return resolver.ResolveManagerReceipt(ctx, *as.Execution)
	}
	resolver, ok := r.cfg.Executions.(ManagerExecutionResolver)
	if !ok {
		return ManagerExecution{}, errors.New("runner: manager executor is unavailable")
	}
	return resolver.ResolveManager(ctx, *as.Execution)
}

func assignmentWorker(as protocol.Assignment, legacy protocol.WorkerKind) protocol.WorkerKind {
	if as.Execution != nil {
		return as.Execution.Worker
	}
	return legacy
}
func (r *Runner) resolveExecution(ctx context.Context, as protocol.Assignment) (ResolvedExecution, error) {
	if as.Execution == nil {
		return ResolvedExecution{Workers: r.cfg.Workers, Workspaces: r.cfg.Workspaces}, nil
	}
	if r.cfg.Executions == nil {
		return ResolvedExecution{}, errors.New("runner: original named execution resolver is unavailable")
	}
	if as.Execution.BackendID != as.Workspace.BackendID {
		return ResolvedExecution{}, errors.New("runner: execution and workspace backend differ")
	}
	return r.cfg.Executions.Resolve(ctx, *as.Execution)
}
