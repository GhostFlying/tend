package serverkit

import (
	"fmt"
	"github.com/GhostFlying/tend/internal/domain"
	"time"
)

// Omitted fields retain the hosted defaults; zero max_attempts disables recovery.
type AgentRecoveryConfig struct {
	BaseDelaySeconds  *int64 `json:"base_delay_seconds"`
	MaxDelaySeconds   *int64 `json:"max_delay_seconds"`
	MaxElapsedSeconds *int64 `json:"max_elapsed_seconds"`
	MaxAttempts       *int64 `json:"max_attempts"`
}

func (c AgentRecoveryConfig) policy() (domain.AgentRecoveryPolicy, error) {
	p := domain.DefaultAgentRecoveryPolicy()
	for _, v := range []struct {
		value  *int64
		target *time.Duration
	}{
		{c.BaseDelaySeconds, &p.BaseDelay}, {c.MaxDelaySeconds, &p.MaxDelay}, {c.MaxElapsedSeconds, &p.MaxElapsed},
	} {
		if v.value != nil {
			if *v.value < 1 || *v.value > 86400 {
				return p, fmt.Errorf("main_agent.recovery durations must be between 1 and 86400 seconds")
			}
			*v.target = time.Duration(*v.value) * time.Second
		}
	}
	if c.MaxAttempts != nil {
		p.MaxAttempts = *c.MaxAttempts
	}
	if p.MaxAttempts < 0 || p.MaxAttempts > 100 || p.MaxDelay < p.BaseDelay || p.MaxElapsed < p.BaseDelay {
		return p, fmt.Errorf("invalid main_agent.recovery budget or delay bounds")
	}
	return p, nil
}
