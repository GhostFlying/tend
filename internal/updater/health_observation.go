package updater

import "github.com/GhostFlying/tend/internal/protocol"

func mergeHealthDiagnostics(target, source *protocol.UpgradeHealthDiagnostics) {
	if source == nil {
		return
	}
	target.ServiceReady = source.ServiceReady
	target.AuthorityHealthy = source.AuthorityHealthy
	target.MainDrained = source.MainDrained
	target.Components = append([]protocol.UpgradeHealthComponent(nil), source.Components...)
	target.Failures = append(target.Failures, source.Failures...)
	for phase, millis := range source.PhaseMillis {
		target.PhaseMillis["server_"+phase] = millis
	}
}

func (s *Supervisor) observeOperationPhase() {
	op := s.state.Operation
	if op == nil {
		return
	}
	now := s.opts.Now()
	if len(op.PhaseTimings) == 0 {
		timing := OperationPhaseTiming{Phase: op.Phase}
		old := s.durable.Operation
		if old == nil || old.ID != op.ID || old.Phase != op.Phase {
			timing.EnteredAt = now
		}
		op.PhaseTimings = append(op.PhaseTimings, timing)
		return
	}
	current := &op.PhaseTimings[len(op.PhaseTimings)-1]
	if current.Phase == op.Phase {
		return
	}
	current.ExitedAt = now
	if !current.EnteredAt.IsZero() {
		elapsed := max(0, now.Sub(current.EnteredAt).Milliseconds())
		current.ElapsedMillis = &elapsed
	}
	op.PhaseTimings = append(op.PhaseTimings, OperationPhaseTiming{Phase: op.Phase, EnteredAt: now})
}
