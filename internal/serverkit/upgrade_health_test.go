package serverkit

import (
	"context"
	"errors"
	"testing"
	"time"
)

type healthComponent struct {
	readyErr, errorDrain error
	drained              bool
	clock                *time.Time
}

func (c healthComponent) RecoveryReadiness(context.Context) error {
	*c.clock = c.clock.Add(11 * time.Millisecond)
	return c.readyErr
}
func (c healthComponent) UpgradeDrainStatus(context.Context) (bool, error) {
	*c.clock = c.clock.Add(7 * time.Millisecond)
	return c.drained, c.errorDrain
}

func TestUpgradeComponentHealthSeparatesReadyFromActiveDrain(t *testing.T) {
	now := time.Unix(1000, 0)
	facts := upgradeComponentHealth(t.Context(), func() time.Time { return now }, map[string]upgradeReadyComponent{"main_thread": healthComponent{clock: &now, drained: false}, "issue_managers": healthComponent{clock: &now, drained: true}})
	if facts.ServiceReady == nil || !*facts.ServiceReady || len(facts.Components) != 2 || facts.Components[0].Drained == nil || *facts.Components[0].Drained || facts.PhaseMillis["main_thread_ready"] != 11 || facts.PhaseMillis["main_thread_drain"] != 7 {
		t.Fatal("active agent was confused with unready startup", facts)
	}
	unknown := upgradeComponentHealth(t.Context(), func() time.Time { return now }, map[string]upgradeReadyComponent{"main_thread": healthComponent{clock: &now, readyErr: errors.New("startup incomplete"), errorDrain: context.DeadlineExceeded}})
	if unknown.ServiceReady == nil || *unknown.ServiceReady || unknown.Components[0].Drained != nil || len(unknown.Failures) != 2 {
		t.Fatal("failed drain observation fabricated a boolean fact", unknown)
	}
}
