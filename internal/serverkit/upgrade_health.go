package serverkit

import (
	"context"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type upgradeReadyComponent interface {
	upgradeDrainer
	RecoveryReadiness(context.Context) error
}

// Component readiness describes usable startup; drain separately describes
// absence of active work. A healthy busy agent is ready and not drained.
func upgradeComponentHealth(ctx context.Context, now func() time.Time, components map[string]upgradeReadyComponent) *protocol.UpgradeHealthDiagnostics {
	facts := &protocol.UpgradeHealthDiagnostics{PhaseMillis: map[string]int64{}}
	ready := true
	for _, name := range []string{"main_thread", "issue_managers"} {
		component := components[name]
		if component == nil {
			continue
		}
		started := now()
		readyErr := component.RecoveryReadiness(ctx)
		facts.PhaseMillis[name+"_ready"] = max(0, now().Sub(started).Milliseconds())
		value := readyErr == nil
		status := protocol.UpgradeHealthComponent{Name: name, Ready: &value}
		if readyErr != nil {
			ready = false
			status.Failure = name + "_not_ready"
			facts.Failures = append(facts.Failures, status.Failure)
		}
		started = now()
		drained, drainErr := component.UpgradeDrainStatus(ctx)
		facts.PhaseMillis[name+"_drain"] = max(0, now().Sub(started).Milliseconds())
		if drainErr == nil {
			status.Drained = &drained
		}
		if drainErr != nil {
			facts.Failures = append(facts.Failures, name+"_drain_observation_unavailable")
		} else if !drained {
			facts.Failures = append(facts.Failures, name+"_active_or_reconciling")
		}
		facts.Components = append(facts.Components, status)
	}
	facts.ServiceReady = &ready
	return facts
}
