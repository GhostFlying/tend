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

type ResolvedExecution struct {
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
