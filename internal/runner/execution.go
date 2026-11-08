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
	OpenRun      func(context.Context, string, string, string, bool, bool) (harness.Session, error)
	OpenNative   func(context.Context, string) (harness.Session, error)
	Open         func(context.Context, string) (harness.Session, error)
	Instructions string
	Profile      protocol.ExecutionProfile
}
type ManagerExecutionResolver interface {
	ResolveManager(context.Context, protocol.RunExecutionPin) (ManagerExecution, error)
}

// ManagerRunExecutionResolver restores locally persisted run environment bindings
// before testing native readiness against current defaults.
type ManagerRunExecutionResolver interface {
	ResolveManagerRun(context.Context, protocol.RunExecutionPin) (ManagerExecution, error)
}

// NativeOwnerExecutionResolver advertises the optional device-default adapter.
// Legacy manager resolvers retain their read-only native boundary.
type NativeOwnerExecutionResolver interface {
	SupportsNativeOwnerTools() bool
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
	if resolver, ok := r.cfg.Executions.(ManagerRunExecutionResolver); ok {
		return resolver.ResolveManagerRun(ctx, *as.Execution)
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
