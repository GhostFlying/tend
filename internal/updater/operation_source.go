package updater

import (
	"context"
	"errors"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func operationSourceBinding(state State, op *Operation) (Binding, error) {
	if op.Kind == "upgrade" {
		return op.Binding, nil
	}
	if op.Kind == "restart" && state.CommittedProof != nil && state.CommittedProof.Version == op.Candidate && state.Current == op.Candidate {
		return state.CommittedProof.Binding, nil
	}
	return Binding{}, errors.New("updater: source operation has no verified selected version")
}

// OperationMaintenanceSource constructs a proposal only; it grants no action.
func OperationMaintenanceSource(state State) (*protocol.MaintenanceOperationSource, Binding, error) {
	op := state.Operation
	if op == nil || (op.Phase != "starting" && op.Phase != "checking" && op.Phase != "launch_exited") || op.LaunchID == "" || !release.ValidSHA(op.Candidate.Artifact.SHA256) {
		return nil, Binding{}, errors.New("updater: exact unfinished launch operation required")
	}
	b, err := operationSourceBinding(state, op)
	if err != nil {
		return nil, Binding{}, err
	}
	return &protocol.MaintenanceOperationSource{OperationID: op.ID, Kind: op.Kind, Phase: op.Phase, OperationSHA256: digest(op), LaunchID: op.LaunchID, Protocol: op.Candidate.Protocol, StorageVersion: op.Candidate.StorageVersion, LaunchExit: op.LaunchExit}, b, nil
}

func validateOperationSourceSnapshot(state State, p protocol.CommittedMaintenancePlan) (*Operation, error) {
	source := p.SourceOperation
	op := state.Operation
	if source == nil || op == nil || source.OperationID != op.ID || source.Kind != op.Kind || source.Phase != op.Phase || source.OperationSHA256 != digest(op) || source.LaunchID != op.LaunchID || source.Protocol != op.Candidate.Protocol || source.StorageVersion != op.Candidate.StorageVersion || !reflect.DeepEqual(source.LaunchExit, op.LaunchExit) {
		return nil, errors.New("updater: unfinished source operation changed")
	}
	b, err := operationSourceBinding(state, op)
	if err != nil || b != p.Previous || op.Candidate.Artifact.SHA256 != p.Previous.ArtifactSHA256 || (op.Phase != "starting" && op.Phase != "checking" && op.Phase != "launch_exited") {
		return nil, errors.New("updater: unfinished source version or binding changed")
	}
	if p.ParentMaintenanceID != "" || p.ParentPlan != nil || p.ParentReceipt != nil || p.ParentLaunchExit != nil {
		return nil, errors.New("updater: operation source cannot impersonate a maintenance parent")
	}
	return op, nil
}

func (s *Supervisor) validateOperationSource(ctx context.Context, p protocol.CommittedMaintenancePlan) error {
	op, err := validateOperationSourceSnapshot(s.state, p)
	if err != nil {
		return err
	}
	if p.Successor.ProcessInstanceID == op.LaunchID {
		return errors.New("updater: replacement requires an independent launch")
	}
	if p.PreviousProcess == (Identity{}) {
		return s.operationSourceExited(ctx, p, op)
	}
	if p.SourceOperation.LaunchExit != nil || p.PreviousProcess.LaunchID != op.LaunchID || p.PreviousProcess.SHA256 != op.Candidate.Artifact.SHA256 || op.CandidateProcess.PID != 0 && op.CandidateProcess != p.PreviousProcess {
		return errors.New("updater: exact unfinished candidate process required")
	}
	if p.FailedProcessExited {
		return s.maintenanceSourceGone(ctx, p.PreviousProcess)
	}
	id, err := s.opts.Process.Inspect(ctx, p.PreviousProcess.PID)
	if err != nil || id != p.PreviousProcess {
		return errors.Join(ErrUnknown, err)
	}
	found, err := s.opts.Process.Find(ctx, op.LaunchID, op.Candidate.Artifact.SHA256)
	if err != nil || len(found) != 1 || found[0] != id {
		return errors.Join(ErrUnknown, err)
	}
	return nil
}

func (s *Supervisor) operationSourceExited(ctx context.Context, p protocol.CommittedMaintenancePlan, op *Operation) error {
	proof := p.SourceOperation.LaunchExit
	if !p.FailedProcessExited || proof == nil || op.LaunchExit == nil || !reflect.DeepEqual(proof, op.LaunchExit) || proof.LaunchID != op.LaunchID || proof.ArtifactSHA256 != op.Candidate.Artifact.SHA256 {
		return errors.New("updater: Find=0 is unknown; an owned launch exit or exact physical source is required")
	}
	stored, err := LoadOwnedLaunchExit(s.cfg, proof.LaunchID, proof.ArtifactSHA256)
	if err != nil || !reflect.DeepEqual(stored, proof) {
		return errors.New("updater: original owned launch exit receipt unavailable or changed")
	}
	found, err := s.opts.Process.Find(ctx, proof.LaunchID, proof.ArtifactSHA256)
	if err != nil || len(found) != 0 {
		return errors.Join(ErrUnknown, err)
	}
	return nil
}

func independentMaintenanceArtifact(p protocol.CommittedMaintenancePlan) bool {
	if p.Successor.ArtifactSHA256 != p.Previous.ArtifactSHA256 {
		return true
	}
	return p.SourceOperation != nil && p.SourceOperation.Kind == "restart"
}

func (s *Supervisor) archiveOperationSource(p protocol.CommittedMaintenancePlan) error {
	if p.SourceOperation == nil {
		return nil
	}
	op := s.state.Operation
	if op == nil || digest(op) != p.SourceOperation.OperationSHA256 {
		return errors.New("updater: source operation changed before ownership transfer")
	}
	if err := s.criticalState(); err != nil {
		return err
	}
	if s.state.OperationHistory == nil {
		s.state.OperationHistory = map[string]Operation{}
	}
	if old, exists := s.state.OperationHistory[op.ID]; exists && digest(old) != digest(op) {
		return errors.New("updater: conflicting source operation history")
	}
	s.state.OperationHistory[op.ID] = *op
	s.state.Operation = nil
	return nil
}

// ValidateOperationMaintenanceSource checks the immutable ownership transfer
// before a successor may register itself in the application ledger.
func ValidateOperationMaintenanceSource(state State, p protocol.CommittedMaintenancePlan) error {
	if p.SourceOperation == nil {
		return nil
	}
	m := state.Maintenance
	op, ok := state.OperationHistory[p.SourceOperation.OperationID]
	if state.Format != 5 || !ok || m == nil || digest(m.Plan) != digest(p) || state.Operation != nil && state.Operation.ID == op.ID {
		return errors.New("updater: unfinished predecessor ownership was not transferred")
	}
	original := State{Operation: &op, Current: op.Candidate, CommittedProof: &m.OriginalProof}
	_, err := validateOperationSourceSnapshot(original, p)
	return err
}
