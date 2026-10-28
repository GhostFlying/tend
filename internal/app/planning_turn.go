package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/store"
)

// WithPlanningSource preserves the native turn's planning effect identity while
// the command's payload and parent request retain its actual owner source.
// The caller must obtain source from the turn's initial delivery, not model
// arguments. This works during turn/start before its native ID is persisted too.
func WithPlanningSource(cmd Command, source string) Command {
	return planningTurnCommand{Command: cmd, source: source}
}

type planningTurnCommand struct {
	Command
	source string
}

func (c planningTurnCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	previous := x.planningSource
	x.planningSource = c.source
	defer func() { x.planningSource = previous }()
	return c.Command.Apply(ctx, x, tx)
}

// WithExecutionRequest carries the host's current native execution independently
// of the owner source recorded as the audit parent.
func WithExecutionRequest(cmd Command, request string) Command {
	return executionCommand{Command: cmd, request: request}
}

type executionCommand struct {
	Command
	request string
}

func (c executionCommand) executionRequestID() string { return c.request }
func (x *Exec) reviewExecution(ctx context.Context, q store.Querier) (string, error) {
	if x.executionRequest != "" {
		return x.executionRequest, nil
	}
	return store.MainExecutionRequest(ctx, q, x.Env.ParentRequestID)
}
