package runner

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// refreshExecutionReadiness admits background native probes under the control
// lock. A maintenance observation retains the blocker until Refresh returns,
// including the resolver's temporary native session cleanup. Business recovery
// and retained executions keep their own journal and resource blockers.
func (r *Runner) refreshExecutionReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.cfg.Executions == nil {
		return nil
	}
	if err := r.loadControl(ctx); err != nil {
		return err
	}
	r.controlMu.Lock()
	if r.control.Mode == protocol.RunnerMaintenance || r.bootstrapMaintenance {
		r.controlMu.Unlock()
		return nil
	}
	r.readinessRefreshes++
	r.controlMu.Unlock()
	defer func() {
		r.controlMu.Lock()
		r.readinessRefreshes--
		r.controlMu.Unlock()
	}()
	return r.cfg.Executions.Refresh(ctx)
}
