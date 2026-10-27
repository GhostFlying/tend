package traex

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
)

// ContextUsageSnapshot is one coherent runtime revision, not billing totals.
type ContextUsageSnapshot struct {
	Revision               int64  `json:"revision"`
	ActiveContextTokens    *int64 `json:"activeContextTokens"`
	EffectiveContextWindow *int64 `json:"effectiveContextWindow"`
	UsedPercentage         *int64 `json:"usedPercentage"`
	Measurement            string `json:"measurement"`
	SampledAt              int64  `json:"sampledAt"`
}

func (s ContextUsageSnapshot) usage(thread string) protocol.AgentContextUsage {
	return protocol.AgentContextUsage{Available: true, CompactSupported: true, ThreadID: thread, Revision: s.Revision, ActiveTokens: s.ActiveContextTokens, EffectiveWindow: s.EffectiveContextWindow, UsedPercentage: s.UsedPercentage, Measurement: s.Measurement, SampledAt: s.SampledAt}
}
func (t *thread) ContextUsage(ctx context.Context) (protocol.AgentContextUsage, error) {
	var result ThreadContextUsageResponse
	err := t.s.conn.call(ctx, "thread/contextUsage", ThreadContextUsageParams{ThreadID: t.id}, &result)
	if err != nil {
		return protocol.AgentContextUsage{}, err
	}
	if result.Snapshot == nil {
		return protocol.AgentContextUsage{ThreadID: t.id, CompactSupported: true, Measurement: "pending"}, nil
	}
	return result.Snapshot.usage(t.id), nil
}
func (t *thread) Compact(ctx context.Context) error {
	return t.s.conn.call(ctx, "thread/compact/start", ThreadCompactStartParams{ThreadID: t.id}, nil)
}

type ThreadContextUsageParams struct {
	ThreadID string `json:"threadId"`
}
type ThreadContextUsageResponse struct {
	Snapshot *ContextUsageSnapshot `json:"snapshot"`
}
type ThreadCompactStartParams struct {
	ThreadID string `json:"threadId"`
}
