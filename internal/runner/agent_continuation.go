package runner

import (
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) acceptRetained(pins []protocol.RunExecutionPin) {
	pins = slices.DeleteFunc(slices.Clone(pins), func(p protocol.RunExecutionPin) bool { return p.Validate(r.cfg.HostID) != nil })
	// Hello and heartbeat can complete concurrently. Publish the resolver set and
	// its acknowledgment cache atomically, or an older setter could win forever
	// while subsequent identical heartbeats are suppressed by the newer cache.
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	if !r.agentContinuationSupported {
		return
	}
	resolver, ok := r.cfg.Executions.(ContinuationResolver)
	if !ok || slices.Equal(r.retainedExecutions, pins) {
		return
	}
	resolver.SetRetained(pins)
	r.retainedExecutions = slices.Clone(pins)
	r.requestReconcile()
}
