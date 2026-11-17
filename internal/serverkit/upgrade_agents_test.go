package serverkit

import (
	"context"
	"testing"
)

type testAgentDrain bool

func (d testAgentDrain) UpgradeDrainStatus(context.Context) (bool, error) { return bool(d), nil }

func TestUpgradeWaitsForActiveAgents(t *testing.T) {
	if drained, err := allAgentsDrained(t.Context(), testAgentDrain(true), testAgentDrain(false)); err != nil || drained {
		t.Fatalf("background activity ignored: %t %v", drained, err)
	}
	if drained, err := allAgentsDrained(t.Context(), testAgentDrain(true), testAgentDrain(true)); err != nil || !drained {
		t.Fatalf("stopped agents blocked installation: %t %v", drained, err)
	}
}
