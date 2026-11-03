package updater

import (
	"context"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func maintenanceActiveResources(m *CommittedMaintenance) protocol.ForwardRecoveryPlan {
	p := maintenanceResources(m.Plan)
	if c := m.ActiveContinuation; c != nil {
		p.WindowClaim = c.WindowClaim
		p.ExpiresAtMillis = c.ExpiresAtMillis
		p.ResourceEvidence = c.ResourceEvidence
		p.ResourceSHA256 = c.ResourceSHA256
	}
	return p
}

// ContinueCommittedMaintenance never changes the candidate or repeats an
// ambiguous launch. Only a proven pre-launch state can receive fresh authority.
func (s *Supervisor) ContinueCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, c protocol.MaintenanceContinuation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) {
		return errors.New("updater: exact maintenance continuation plan required")
	}
	if old, ok := m.Continuations[c.RequestID]; ok {
		if old != c {
			return errors.New("updater: maintenance continuation payload changed")
		}
		return nil
	}
	if !protocol.ValidID(protocol.PrefixRequest, c.RequestID) || c.RequestID == p.RequestID || c.RequestID == p.LinkRequestID || c.RequestID == p.PreparationRequestID || c.RequestID == p.PreparationCancelRequestID || c.WindowClaim == "" || c.ExpectedGeneration != s.state.Generation || c.ExpiresAtMillis <= s.opts.Now().UnixMilli() || c.ExpiresAtMillis > s.opts.Now().Add(30*time.Minute).UnixMilli() {
		return errors.New("updater: invalid or stale maintenance continuation")
	}
	for _, h := range p.HostControls {
		if c.RequestID == h.RequestID || c.RequestID == h.RestoreRequestID {
			return errors.New("updater: continuation identity conflicts with host control")
		}
	}
	for parent := p.ParentPlan; parent != nil; parent = parent.ParentPlan {
		if c.RequestID == parent.RequestID || c.RequestID == parent.LinkRequestID {
			return errors.New("updater: continuation identity conflicts with parent")
		}
	}
	if !maintenancePrelaunchPhase(m.Phase) || m.Irreversible {
		return errors.New("updater: continuation requires a definitely pre-launch maintenance state")
	}
	if m.ArchiveAuthorization == nil {
		return errors.New("updater: original admission proof missing")
	}
	if err := maintenanceDrain(p, *m.ArchiveAuthorization); err != nil {
		return err
	}
	if err := s.maintenanceOldGone(ctx, m); err != nil {
		return err
	}
	found, err := s.opts.Process.Find(ctx, p.Successor.ProcessInstanceID, m.Target.Artifact.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 0 {
		return ErrUnknown
	}
	probe := *m
	probe.ActiveContinuation = &c
	if err := verifyResourceEvidence(maintenanceActiveResources(&probe), s.opts.Now()); err != nil {
		return err
	}
	if _, _, err := s.committedBundle(p); err != nil {
		return err
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	if m.Continuations == nil {
		m.Continuations = map[string]protocol.MaintenanceContinuation{}
	}
	m.Continuations[c.RequestID] = c
	m.ActiveContinuation = &c
	m.AuthorizationBootID = boot
	m.AuthorizationDeadlineMillis = mono + (c.ExpiresAtMillis - s.opts.Now().UnixMilli())
	switch m.Phase {
	case "launch_not_started":
		m.Phase = "backed_up"
	case "authorized", "stopping_old":
		m.Phase = "old_exited"
	}
	return s.save()
}

func maintenancePrelaunchPhase(phase string) bool {
	switch phase {
	case "authorized", "stopping_old", "old_exited", "backed_up", "launch_not_started":
		return true
	default:
		return false
	}
}
