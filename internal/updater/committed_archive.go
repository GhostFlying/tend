package updater

import (
	"context"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

type MaintenanceArchiveAuthority interface {
	MaintenanceArchiveStatus(context.Context) (protocol.ArchiveOnlineStatus, error)
	MaintenanceArchiveControl(context.Context, string, any) error
}

func maintenanceTerminal(m *CommittedMaintenance) bool {
	return m == nil || m.Phase == "ledger_linked" || m.Phase == "cancelled"
}

func validateMaintenanceRequests(p protocol.CommittedMaintenancePlan) error {
	seen := map[string]bool{}
	for _, id := range []string{p.RequestID, p.LinkRequestID, p.PreparationRequestID, p.PreparationCancelRequestID} {
		if !protocol.ValidID(protocol.PrefixRequest, id) || seen[id] {
			return errors.New("updater: distinct maintenance request identities required")
		}
		seen[id] = true
	}
	hosts := map[string]bool{}
	for _, h := range p.HostControls {
		if h.HostID == "" || hosts[h.HostID] || h.Revision < 0 || (h.PreviousMode != "normal" && h.PreviousMode != "maintenance") {
			return errors.New("updater: invalid maintenance host baseline")
		}
		hosts[h.HostID] = true
		for _, id := range []string{h.RequestID, h.RestoreRequestID} {
			if !protocol.ValidID(protocol.PrefixRequest, id) || seen[id] {
				return errors.New("updater: distinct maintenance host requests required")
			}
			seen[id] = true
		}
	}
	return nil
}

// maintenanceDrain uses the old service's existing archive API. Open business
// runs are intentionally preserved; OnlineReady includes a different run-zero
// condition and cannot authorize this local committed service replacement.
func maintenanceDrain(p protocol.CommittedMaintenancePlan, v protocol.ArchiveOnlineStatus) error {
	if !v.HostedDrained {
		return errors.New("updater: prepared service has not drained hosted operations")
	}
	return maintenanceAdmission(p, v)
}

func maintenanceAdmission(p protocol.CommittedMaintenancePlan, v protocol.ArchiveOnlineStatus) error {
	if !v.Preparation.Prepared || v.Preparation.Revision != p.PreparationRevision || v.ActiveOwnerRequests != 0 {
		return errors.New("updater: prepared service admission is not confirmed")
	}
	if len(v.Hosts) != len(p.HostControls) {
		return errors.New("updater: maintenance host set changed")
	}
	byID := map[string]protocol.ArchiveHostStatus{}
	for _, h := range v.Hosts {
		byID[h.HostID] = h
	}
	for _, h := range p.HostControls {
		v, ok := byID[h.HostID]
		rev := h.Revision
		if h.PreviousMode != "maintenance" {
			rev++
		}
		if !ok || !v.Fresh || v.Control.Mode != "maintenance" || v.Control.Revision != rev || v.Control.UpgradeID != "" || v.Runtime == nil || (v.Runtime.Mode != "maintenance" && v.Runtime.Mode != "ready") || v.Runtime.AppliedControlRevision != rev || v.Runtime.PendingClaims != 0 {
			return errors.New("updater: host maintenance acknowledgment not confirmed")
		}
	}
	return nil
}

func (s *Supervisor) PrepareCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) {
		return errors.New("updater: unknown maintenance request")
	}
	if m.Phase != "updater_adopted" {
		return errors.New("updater: maintenance preparation is no longer available")
	}
	if generation != s.state.Generation {
		return errors.New("updater: stale maintenance generation")
	}
	if err := validateMaintenanceRequests(p); err != nil {
		return err
	}
	if p.ExpiresAtMillis <= s.opts.Now().UnixMilli() {
		return errors.New("updater: maintenance window expired")
	}
	a, ok := s.opts.Authority.(MaintenanceArchiveAuthority)
	if !ok {
		return errors.New("updater: maintenance archive authority unavailable")
	}
	if p.ParentMaintenanceID != "" {
		parent, err := s.maintenanceParent(p)
		if err != nil {
			return err
		}
		return s.validateFailedAdmission(ctx, p, parent)
	}
	return prepareMaintenanceArchive(ctx, a, p)
}

func (s *Supervisor) CancelCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelCommittedMaintenanceLocked(ctx, p, generation)
}

func (s *Supervisor) cancelCommittedMaintenanceLocked(ctx context.Context, p protocol.CommittedMaintenancePlan, generation uint64) error {
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) {
		return errors.New("updater: unknown maintenance request")
	}
	if p.ParentMaintenanceID != "" {
		return errors.New("updater: failed-parent admission remains owned; complete the forward successor")
	}
	if m.Phase == "cancelled" {
		return nil
	}
	if m.Phase != "updater_adopted" && m.Phase != "authorized" && m.Phase != "cancelling" {
		return errors.New("updater: service replacement already started; cancellation is unsafe")
	}
	if generation != s.state.Generation {
		return errors.New("updater: stale maintenance generation")
	}
	a, ok := s.opts.Authority.(MaintenanceArchiveAuthority)
	if !ok {
		return errors.New("updater: maintenance archive authority unavailable")
	}
	id, err := s.opts.Process.Inspect(ctx, p.PreviousProcess.PID)
	if err != nil || id != p.PreviousProcess {
		return ErrUnknown
	}
	if m.Phase != "cancelling" {
		m.Phase = "cancelling"
		if err := s.save(); err != nil {
			return err
		}
	}
	if err := restoreMaintenanceArchive(ctx, a, p); err != nil {
		return err
	}
	m.Phase = "cancelled"
	return s.save()
}

// maintenanceControlPosition identifies only the exact transitions authorized by
// the frozen plan. A matching poststate satisfies a transition; it does not
// identify which transport request produced it.
type maintenanceControlPosition int

const (
	maintenanceControlInitial maintenanceControlPosition = iota
	maintenanceControlApplied
	maintenanceControlRestored
)

type maintenanceControlState struct {
	archive maintenanceControlPosition
	hosts   []maintenanceControlPosition
}

func classifyMaintenanceControls(p protocol.CommittedMaintenancePlan, v protocol.ArchiveOnlineStatus) (maintenanceControlState, error) {
	state := maintenanceControlState{hosts: make([]maintenanceControlPosition, len(p.HostControls))}
	switch {
	case !v.Preparation.Prepared && v.Preparation.Revision == p.PreparationRevision-1:
		state.archive = maintenanceControlInitial
	case v.Preparation.Prepared && v.Preparation.Revision == p.PreparationRevision:
		state.archive = maintenanceControlApplied
	case !v.Preparation.Prepared && v.Preparation.Revision == p.PreparationRevision+1:
		state.archive = maintenanceControlRestored
	default:
		return state, errors.New("updater: archive preparation changed outside maintenance control boundary")
	}
	byID := make(map[string]protocol.RunnerControl, len(v.Hosts))
	for _, h := range v.Hosts {
		if _, duplicate := byID[h.HostID]; duplicate {
			return state, errors.New("updater: duplicate maintenance host observation")
		}
		byID[h.HostID] = h.Control
	}
	for i, h := range p.HostControls {
		actual, ok := byID[h.HostID]
		if !ok || actual.UpgradeID != "" {
			return state, errors.New("updater: maintenance host is missing or controlled by an upgrade")
		}
		switch {
		case actual.Mode == h.PreviousMode && actual.Revision == h.Revision:
			state.hosts[i] = maintenanceControlInitial
		case h.PreviousMode == protocol.RunnerNormal && actual.Mode == protocol.RunnerMaintenance && actual.Revision == h.Revision+1:
			state.hosts[i] = maintenanceControlApplied
		case h.PreviousMode == protocol.RunnerNormal && actual.Mode == protocol.RunnerNormal && actual.Revision == h.Revision+2:
			state.hosts[i] = maintenanceControlRestored
		default:
			return state, errors.New("updater: host changed outside maintenance control boundary")
		}
		// This preparation always precedes host writes. Do not adopt an unrelated
		// host transition while the archive is still at its untouched baseline.
		if state.archive == maintenanceControlInitial && state.hosts[i] != maintenanceControlInitial {
			return state, errors.New("updater: host control changed before maintenance preparation")
		}
	}
	return state, nil
}

func prepareMaintenanceArchive(ctx context.Context, a MaintenanceArchiveAuthority, p protocol.CommittedMaintenancePlan) error {
	v, err := a.MaintenanceArchiveStatus(ctx)
	if err != nil {
		return err
	}
	state, err := classifyMaintenanceControls(p, v)
	if err != nil {
		return err
	}
	if state.archive == maintenanceControlRestored {
		return errors.New("updater: maintenance preparation was already restored")
	}
	for _, position := range state.hosts {
		if position == maintenanceControlRestored {
			return errors.New("updater: maintenance host was already restored")
		}
	}
	if state.archive == maintenanceControlInitial {
		if err := a.MaintenanceArchiveControl(ctx, "prepare", protocol.ArchiveControlRequest{RequestID: p.PreparationRequestID, ExpectedControlRevision: p.PreparationRevision - 1}); err != nil {
			return fmt.Errorf("updater: prepare archive control: %w", err)
		}
	}
	for i, h := range p.HostControls {
		if h.PreviousMode == protocol.RunnerMaintenance || state.hosts[i] != maintenanceControlInitial {
			continue
		}
		if err := a.MaintenanceArchiveControl(ctx, "host", protocol.ArchiveHostControlRequest{RequestID: h.RequestID, ExpectedControlRevision: h.Revision, HostID: h.HostID, Mode: protocol.RunnerMaintenance}); err != nil {
			return fmt.Errorf("updater: prepare host control: %w", err)
		}
	}
	v, err = a.MaintenanceArchiveStatus(ctx)
	if err != nil {
		return err
	}
	state, err = classifyMaintenanceControls(p, v)
	if err != nil {
		return err
	}
	if state.archive != maintenanceControlApplied {
		return errors.New("updater: maintenance preparation did not reach its planned state")
	}
	for i, position := range state.hosts {
		if p.HostControls[i].PreviousMode == protocol.RunnerNormal && position != maintenanceControlApplied {
			return errors.New("updater: maintenance host did not reach its planned state")
		}
	}
	return nil
}

func restoreMaintenanceArchive(ctx context.Context, a MaintenanceArchiveAuthority, p protocol.CommittedMaintenancePlan) error {
	v, err := a.MaintenanceArchiveStatus(ctx)
	if err != nil {
		return err
	}
	state, err := classifyMaintenanceControls(p, v)
	if err != nil {
		return err
	}
	// Restore only controls this window could have changed. Never create a
	// preparation or maintenance transition just to cancel it afterwards.
	for i, h := range p.HostControls {
		if state.hosts[i] != maintenanceControlApplied {
			continue
		}
		if err := a.MaintenanceArchiveControl(ctx, "host", protocol.ArchiveHostControlRequest{RequestID: h.RestoreRequestID, ExpectedControlRevision: h.Revision + 1, HostID: h.HostID, Mode: h.PreviousMode}); err != nil {
			return fmt.Errorf("updater: restore host control: %w", err)
		}
	}
	if state.archive == maintenanceControlApplied {
		if err := a.MaintenanceArchiveControl(ctx, "cancel", protocol.ArchiveControlRequest{RequestID: p.PreparationCancelRequestID, ExpectedControlRevision: p.PreparationRevision}); err != nil {
			return fmt.Errorf("updater: cancel archive control: %w", err)
		}
	}
	v, err = a.MaintenanceArchiveStatus(ctx)
	if err != nil {
		return err
	}
	state, err = classifyMaintenanceControls(p, v)
	if err != nil {
		return err
	}
	if state.archive == maintenanceControlApplied {
		return errors.New("updater: archive preparation remains active")
	}
	for _, position := range state.hosts {
		if position == maintenanceControlApplied {
			return errors.New("updater: host maintenance control remains active")
		}
	}
	return nil
}

// A force may have explicitly installed without establishing its planned fence.
// Its later controls cannot retroactively become that fence merely by matching
// a revision. Only the existing unchanged-baseline completion can close it.
func (s *Supervisor) validateMaintenanceCompletionBaseline(p protocol.CommittedMaintenancePlan) error {
	b := s.state.Bounded
	if b == nil {
		return nil
	}
	for _, entry := range b.Audit {
		if entry.RequestID != boundedRecoveryID(b) || entry.Archive == nil || entry.ObservationError != "" || (entry.Action != "force" && entry.Action != "takeover") {
			continue
		}
		if _, err := classifyMaintenanceControls(p, *entry.Archive); err != nil {
			return errors.New("updater: original unestablished maintenance fence requires unchanged-baseline completion")
		}
		break
	}
	return nil
}

func (s *Supervisor) FinishCommittedMaintenance(ctx context.Context, p protocol.CommittedMaintenancePlan, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	m := s.state.Maintenance
	if m == nil || digest(m.Plan) != digest(p) || m.Phase != "ledger_linked" {
		return errors.New("updater: linked maintenance receipt required before release")
	}
	if generation != s.state.Generation {
		return errors.New("updater: stale maintenance generation")
	}
	if bounded := s.state.Bounded; bounded != nil {
		if bounded.Outcome == "completed" {
			return nil
		}
		remaining, err := s.boundedRemaining()
		if err != nil {
			bounded.Outcome = "installed_recovery_pending"
			if digest(s.state) != digest(s.durable) {
				if saveErr := s.save(); saveErr != nil {
					return saveErr
				}
			}
			return err
		}
		finishCtx, cancel := context.WithTimeout(ctx, remaining)
		defer cancel()
		ctx = finishCtx
	}
	a, ok := s.opts.Authority.(MaintenanceArchiveAuthority)
	if !ok {
		return errors.New("updater: maintenance archive authority unavailable")
	}
	completed, err := s.completeUnestablishedBoundedFence(ctx, a, p)
	if err == nil && !completed {
		err = s.validateMaintenanceCompletionBaseline(p)
		if err == nil {
			err = restoreMaintenanceArchive(ctx, a, p)
		}
	}
	if bounded := s.state.Bounded; bounded != nil {
		if err == nil {
			bounded.Outcome = "completed"
		} else {
			bounded.Outcome = "installed_recovery_pending"
		}
		if e := s.save(); e != nil {
			return e
		}
	}
	return err
}
