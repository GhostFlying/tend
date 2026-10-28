package domain

import (
	"testing"
	"time"
)

func TestAgentRecoveryDelayBoundedStableAndExponential(t *testing.T) {
	p := DefaultAgentRecoveryPolicy()
	for attempt := int64(1); attempt <= 100; attempt++ {
		base := p.BaseDelay
		for i := int64(1); i < attempt && base < p.MaxDelay; i++ {
			base = min(base*2, p.MaxDelay)
		}
		got := p.Delay("stable-chain", attempt)
		if got < base || got > base*120/100 || got != p.Delay("stable-chain", attempt) {
			t.Fatalf("attempt=%d delay=%s base=%s", attempt, got, base)
		}
	}
	if p.Delay("stable-chain", 1) >= p.Delay("stable-chain", 2) {
		t.Fatal("delay did not grow")
	}
	if p.Delay("stable-chain", 100) > 144*time.Second {
		t.Fatal("backoff exceeded cap plus jitter")
	}
}
