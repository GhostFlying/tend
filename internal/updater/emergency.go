package updater

import (
	"context"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// WindowAuthority remains reachable after the only accepting process exits.
type WindowAuthority interface {
	Target(context.Context, Identity) (string, error)
	Retire(context.Context, protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error)
}

func (s *Supervisor) watchEmergency(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.discoverEmergency(ctx); err != nil && ctx.Err() == nil {
			s.opts.Log.DebugContext(ctx, "runner maintenance discovery unavailable", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Supervisor) discoverEmergency(ctx context.Context) error {
	s.mu.Lock()
	process := s.state.Process
	eligible := process.StartTicks != 0 && done(s.state.Operation) && (s.state.EmergencyPrimary == nil || s.state.EmergencyPrimary.Process != process)
	s.mu.Unlock()
	if !eligible {
		return nil
	}
	id, err := s.opts.WindowAuthority.Target(ctx, process)
	if err != nil || id == "" {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if s.state.Process != process || !done(s.state.Operation) || !maintenanceTerminal(s.state.Maintenance) || s.state.Recovery != nil && s.state.Recovery.Phase != "ledger_linked" {
		return nil
	}
	return s.acquireEmergency(ctx, id, process)
}

// Called under the supervisor lock. Keep a discovered admission distinct from
// an actual stop grant, which must be persisted before any signal or receipt.
func (s *Supervisor) acquireEmergency(ctx context.Context, id string, process Identity) error {
	// No stop or restart suppression is durable until the server grants this
	// exact process. A cancelled discovery leaves ordinary supervision intact.
	stop := DrainingProcess{UpgradeID: id, JournalID: s.state.CurrentJournal, Process: process}
	s.state.EmergencyPrimary = &stop
	granted, err := s.requestRetirement(ctx, protocol.RunnerRetirement{UpgradeID: id, Process: process}, &stop)
	if !granted && s.persistenceErr == nil {
		s.state.EmergencyPrimary = s.durable.EmergencyPrimary
	}
	return err
}

// A dead primary can still be the server's pinned target. Reconcile it before
// allocating a new identity or dispatching an unlaunched crash restart.
func (s *Supervisor) checkCrashWindow(ctx context.Context) (bool, error) {
	if s.opts.WindowAuthority == nil {
		return false, nil
	}
	process := s.state.Process
	if stop := s.state.EmergencyPrimary; stop == nil || stop.Process != process {
		id, err := s.opts.WindowAuthority.Target(ctx, process)
		if err != nil || id == "" {
			return false, err
		}
		if err := s.acquireEmergency(ctx, id, process); err != nil {
			return true, err
		}
		if s.state.EmergencyPrimary == nil {
			return true, nil
		}
	}
	if op := s.state.Operation; op != nil && op.Kind == "restart" && op.Phase == "backed_up" {
		op.Phase = "cancelled"
		if err := s.save(); err != nil {
			return true, err
		}
	}
	return true, s.retirePredecessor(ctx, *s.state.EmergencyPrimary)
}
