package domain

import (
	"fmt"
	"hash/fnv"
	"time"
)

// AgentRecoveryPolicy bounds automatic continuations after native rate limits.
type AgentRecoveryPolicy struct {
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	MaxElapsed  time.Duration
	MaxAttempts int64
}

// DefaultAgentRecoveryPolicy leaves native retries to the harness first.
func DefaultAgentRecoveryPolicy() AgentRecoveryPolicy {
	return AgentRecoveryPolicy{BaseDelay: 5 * time.Second, MaxDelay: 2 * time.Minute, MaxElapsed: 15 * time.Minute, MaxAttempts: 8}
}

// Delay adds stable positive jitter so a restart cannot shorten an existing wait.
// Identity is an injected request ID, rather than ambient randomness.
func (p AgentRecoveryPolicy) Delay(identity string, attempt int64) time.Duration {
	d := p.BaseDelay
	for i := int64(1); i < attempt && d < p.MaxDelay; i++ {
		d = min(d*2, p.MaxDelay)
	}
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s/%d", identity, attempt)
	return d + d*time.Duration(h.Sum32()%21)/100
}
