package runner

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) capabilityWorkerStart(_ context.Context, as protocol.Assignment, resolved ResolvedExecution) (func(context.Context, harness.WorkerSpec) (harness.Worker, error), error) {
	if as.CapabilityBinding == nil {
		return resolved.Workers.Start, nil
	}
	b := *as.CapabilityBinding
	if b.MatchPolicy != "" && b.MatchPolicy != "declaration_v1" {
		return nil, failed("capability_lost", errors.New("unsupported capability match policy"))
	}
	if as.Execution == nil || *as.Execution != b.Pin || len(b.Required) == 0 || b.RequiredSHA256 != protocol.CapabilityRequiredDigest(b.Required) {
		return nil, failed("capability_lost", errors.New("invalid frozen capability binding"))
	}
	if b.CurrentMatch != nil && b.CurrentMatch.State != "eligible" {
		return nil, failed("capability_lost", errors.New("original executor is no longer eligible"))
	}
	if m := protocol.MatchExecutorCapabilities(b.Pin, b.Required, resolved.DeclaredCapabilities, nil, r.cfg.Now().UnixMilli()); m.State != "eligible" {
		return nil, failed("capability_lost", errors.New("original Agent revision does not declare the required capabilities"))
	}
	// The ordinary resolver has already checked the original accepted snapshot,
	// runtime, model, effort and backend. Configuration labels need no tool probe
	// or special native launch interface and do not establish tool availability.
	return resolved.Workers.Start, nil
}
