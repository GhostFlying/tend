package upgradectl

import (
	"context"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func healthFact(value bool) *bool { return &value }

func (c *Controller) normalHealth(ctx context.Context, binding protocol.UpgradeBinding, diagnostics bool) (protocol.UpgradeHealth, error) {
	started := c.cfg.Now()
	health, err := c.app.ServerUpgradeHealth(ctx, binding)
	if err != nil {
		return health, err
	}
	authorityHealthy := health.Healthy
	authorityMillis := max(0, c.cfg.Now().Sub(started).Milliseconds())
	started = c.cfg.Now()
	observed, observeErr := c.observe(ctx)
	localMillis := max(0, c.cfg.Now().Sub(started).Milliseconds())
	if observeErr != nil || !observed.Healthy {
		health.Healthy = false
	}
	// Draining authorizes replacement of the previous process. It is not a
	// readiness requirement for the new process or its optional model sessions.
	readyAt := c.cfg.Now()
	var readinessErr error
	if c.cfg.RecoveryReady != nil {
		readinessErr = c.cfg.RecoveryReady(ctx)
		if readinessErr != nil {
			health.Healthy = false
		}
	}
	readinessMillis := max(0, c.cfg.Now().Sub(readyAt).Milliseconds())
	if !diagnostics {
		health.Diagnostics = nil
		return health, nil
	}
	diagnosticCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	var facts *protocol.UpgradeHealthDiagnostics
	if c.cfg.HealthDiagnostics != nil {
		facts = c.cfg.HealthDiagnostics(diagnosticCtx)

	}
	if facts == nil {
		facts = &protocol.UpgradeHealthDiagnostics{}
	}
	facts.AuthorityIdentityMatched = healthFact(health.Binding == binding)
	facts.ServerConfirmed = healthFact(health.ServerConfirmed)
	if observeErr == nil {
		facts.MainDrained = healthFact(observed.MainDrained)
	}
	facts.AuthorityHealthy = healthFact(authorityHealthy)
	facts.InstallationHealthy = healthFact(health.Healthy)
	if !authorityHealthy {
		facts.Failures = append(facts.Failures, "authority_health_not_confirmed")
	}
	if facts.PhaseMillis == nil {
		facts.PhaseMillis = map[string]int64{}
	}
	facts.PhaseMillis["server_authority"] = authorityMillis
	facts.PhaseMillis["local_observation"] = localMillis
	if !health.ServerConfirmed {
		facts.Failures = append(facts.Failures, "server_confirmation_missing")
	}
	if observeErr != nil {
		facts.Failures = append(facts.Failures, "local_observation_unavailable")
	}
	if observeErr == nil {
		if !observed.Healthy {
			facts.Failures = append(facts.Failures, "local_service_unhealthy")
		}
	}
	// Optional session diagnostics do not redefine installation readiness.
	facts.ServiceReady = healthFact(readinessErr == nil && observeErr == nil && observed.Healthy)
	facts.PhaseMillis["service_readiness"] = readinessMillis
	if readinessErr != nil {
		facts.Failures = append(facts.Failures, "startup_components_not_ready")
	}
	if diagnosticCtx.Err() != nil {
		facts.Failures = append(facts.Failures, "component_observation_budget_exhausted")
	}
	health.Diagnostics = facts
	return health, nil
}
