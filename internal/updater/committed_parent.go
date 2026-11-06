package updater

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func (s *Supervisor) maintenanceParent(p protocol.CommittedMaintenancePlan) (*CommittedMaintenance, error) {
	if s.state.Maintenance == nil || (s.state.Maintenance.Plan.MaintenanceID != p.ParentMaintenanceID && s.state.Maintenance.Plan.MaintenanceID != p.MaintenanceID) {
		return nil, errors.New("updater: failed parent is no longer the active responsibility")
	}
	seen := map[string]bool{p.MaintenanceID: true}
	for ancestor := p.ParentPlan; ancestor != nil; ancestor = ancestor.ParentPlan {
		if seen[ancestor.MaintenanceID] || p.RequestID == ancestor.RequestID || p.RequestID == ancestor.LinkRequestID || p.LinkRequestID == ancestor.RequestID || p.LinkRequestID == ancestor.LinkRequestID {
			return nil, errors.New("updater: independent failed successor identities required")
		}
		seen[ancestor.MaintenanceID] = true
	}
	var parent *CommittedMaintenance
	if s.state.Maintenance != nil && s.state.Maintenance.Plan.MaintenanceID == p.ParentMaintenanceID {
		parent = s.state.Maintenance
	} else if old, ok := s.state.MaintenanceHistory[p.ParentMaintenanceID]; ok {
		parent = &old
	}
	if p.ParentMaintenanceID == "" || parent == nil || p.ParentPlan == nil || p.ParentMaintenanceID == p.MaintenanceID || p.ParentPlan.MaintenanceID != p.ParentMaintenanceID || digest(parent.Plan) != p.ParentPlanSHA256 || digest(*p.ParentPlan) != p.ParentPlanSHA256 || !parent.Irreversible || parent.Plan.Successor != p.Previous || parent.Process != p.PreviousProcess || parent.ArchiveAuthorization == nil && ValidateBoundedMaintenanceParent(s.state, *parent) != nil || !release.ValidSHA(p.ParentArchiveSHA256) || digest(parent.ArchiveAuthorization) != p.ParentArchiveSHA256 {
		return nil, errors.New("updater: exact irreversible failed maintenance parent required")
	}
	knownProcess := (parent.Phase == "checking_successor" || parent.Phase == "successor_committed") && validIdentity(parent.Process)
	if !knownProcess && ValidateMaintenanceLaunchExit(*parent) != nil {
		return nil, errors.New("updater: failed parent has neither exact process nor owned exit proof")
	}

	if parent.Phase == "launch_exited" && (!p.FailedProcessExited || p.PreviousProcess != (Identity{}) || p.ParentLaunchExit == nil || *p.ParentLaunchExit != *parent.LaunchExit) {
		return nil, errors.New("updater: exited launch must retain distinct no-process provenance")
	}
	if parent.Phase != "launch_exited" && p.ParentLaunchExit != nil {
		return nil, errors.New("updater: unexpected maintenance launch exit proof")
	}
	if (p.ParentReceipt == nil) != (parent.Receipt == nil) || (p.ParentReceipt != nil && *p.ParentReceipt != *parent.Receipt) {
		return nil, errors.New("updater: failed parent receipt changed")
	}
	return parent, nil
}
func (s *Supervisor) maintenanceSourceGone(ctx context.Context, id Identity) error {
	actual, err := s.opts.Process.Inspect(ctx, id.PID)
	if err == nil && actual.StartTicks == id.StartTicks {
		return ErrUnknown
	}
	if err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	found, err := s.opts.Process.Find(ctx, id.LaunchID, id.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 0 {
		return ErrUnknown
	}
	return nil
}
func (s *Supervisor) validateFailedAdmission(ctx context.Context, p protocol.CommittedMaintenancePlan, parent *CommittedMaintenance) error {
	// The failed service need not expose any API. This is the original persisted
	// preparation/owner-drain/host-ack proof from before its predecessor stopped,
	// combined with a fresh operator resource handoff and exact physical writer.
	if digest(p.ReceiptRecoveryStop) != digest(parent.Plan.ReceiptRecoveryStop) {
		return errors.New("updater: failed parent receipt recovery inventory changed")
	}
	var admissionErr error
	if parent.ReceiptRecoveryAuthorized {
		admissionErr = ValidateReceiptRecoveryArchive(p, *parent.ArchiveAuthorization)
	} else {
		admissionErr = maintenanceDrain(p, *parent.ArchiveAuthorization)
	}
	if admissionErr != nil {
		return admissionErr
	}
	if err := verifyResourceEvidence(maintenanceResources(p), s.opts.Now()); err != nil {
		return err
	}
	if parent.Phase == "launch_exited" {
		return s.maintenanceLaunchGone(ctx, parent)
	}
	if p.FailedProcessExited {
		return s.maintenanceSourceGone(ctx, p.PreviousProcess)
	}
	actual, err := s.opts.Process.Inspect(ctx, p.PreviousProcess.PID)
	if err != nil {
		return err
	}
	if actual != p.PreviousProcess {
		return ErrUnknown
	}
	found, err := s.opts.Process.Find(ctx, actual.LaunchID, actual.SHA256)
	if err != nil {
		return err
	}
	if len(found) != 1 || found[0] != actual {
		return ErrUnknown
	}
	return nil
}
