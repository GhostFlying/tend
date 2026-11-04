package runner

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) capabilityWorkerStart(ctx context.Context, as protocol.Assignment, resolved ResolvedExecution) (func(context.Context, harness.WorkerSpec) (harness.Worker, error), error) {
	if as.CapabilityBinding == nil {
		return resolved.Workers.Start, nil
	}
	b := *as.CapabilityBinding
	if b.CurrentMatch != nil && b.CurrentMatch.State != "eligible" {
		return nil, failed("capability_lost", errors.New("current server evidence no longer qualifies this exact run"))
	}
	activeEvidence, activeIDs := b.Evidence, b.EvidenceIDs
	if v := b.Revalidation; v != nil {
		if v.Pin != b.Pin || v.RequiredSHA256 != b.RequiredSHA256 || v.RequestID == "" {
			return nil, failed("capability_lost", errors.New("invalid capability revalidation binding"))
		}
		activeEvidence, activeIDs = v.Evidence, v.EvidenceIDs
	}
	if as.Execution == nil || *as.Execution != b.Pin || len(b.Required) == 0 || b.RequiredSHA256 != protocol.CapabilityRequiredDigest(b.Required) || len(activeIDs) != len(b.Required) || len(activeEvidence) != len(b.Required) {
		return nil, failed("capability_lost", errors.New("invalid frozen capability binding"))
	}
	adapter, ok := resolved.Workers.(harness.CapabilityWorkers)
	if !ok {
		return nil, failed("capability_lost", errors.New("adapter cannot enforce capability requirements at session creation"))
	}
	now := r.cfg.Now().UnixMilli()
	if m := protocol.MatchExecutorCapabilities(b.Pin, b.Required, b.Required, activeEvidence, now); m.State != "eligible" {
		return nil, failed("capability_lost", errors.New("original tool evidence is no longer valid"))
	}
	r.mu.Lock()
	runtime := r.confirmedRuntime
	r.mu.Unlock()
	for _, e := range activeEvidence {
		if e.InstanceID != runtime.InstanceID || e.Generation != runtime.Generation || !slices.Contains(activeIDs, e.EvidenceID) {
			return nil, failed("capability_lost", errors.New("original evidence incarnation changed"))
		}
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		fresh, err := adapter.CapabilityInventory(probeCtx, b.Pin, e.Capability)
		cancel()
		if err != nil || fresh.Pin != b.Pin || fresh.Capability != e.Capability || fresh.Scope != "profile" || fresh.Kind != "tool_inventory" || fresh.State != "confirmed" || fresh.Reason != "tool_exposed" || !fresh.Complete || fresh.Source == nil || e.Source == nil || *fresh.Source != *e.Source || fresh.ObservedAtMillis > now || fresh.ExpiresAtMillis <= now {
			return nil, failed("capability_lost", errors.New("current tool provenance differs from the frozen capability evidence"))
		}
	}
	// The adapter must repeat the check atomically with native session creation.
	return func(ctx context.Context, s harness.WorkerSpec) (harness.Worker, error) {
		return adapter.StartWithCapabilities(ctx, s, b)
	}, nil
}
