package app

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func maintenanceOperationSource(ctx context.Context, q store.Querier, p protocol.CommittedMaintenancePlan) (*store.UpgradeRow, error) {
	source := p.SourceOperation
	if source == nil {
		return nil, nil
	}
	if !release.ValidID(source.OperationID) || !release.ValidSHA(source.OperationSHA256) || source.LaunchID == "" || source.Protocol < 1 || source.StorageVersion < 1 || (source.Phase != "starting" && source.Phase != "checking" && source.Phase != "launch_exited") || p.ParentMaintenanceID != "" || p.ParentPlan != nil || p.ParentReceipt != nil || p.ParentLaunchExit != nil || p.ParentPlanSHA256 != "" || p.ParentArchiveSHA256 != "" || p.ParentGeneration != 0 {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "unfinished maintenance source binding changed")
	}
	if proof := source.LaunchExit; proof != nil {
		if !p.FailedProcessExited || p.PreviousProcess != (protocol.RecoveryIdentity{}) || proof.PID <= 1 || proof.LaunchID != source.LaunchID || proof.ArtifactSHA256 != p.Previous.ArtifactSHA256 || !release.ValidSHA(proof.ArtifactSHA256) || proof.ExitCode < -1 || proof.ExitCode > 255 || proof.Signal < 0 || proof.Signal > 64 || proof.Signal == 0 && proof.ExitCode < 0 || proof.Signal != 0 && proof.ExitCode != -1 {
			return nil, protocol.Errorf(protocol.CodeConflictPayload, "unfinished source owned exit binding changed")
		}
	} else if p.PreviousProcess.PID <= 1 || p.PreviousProcess.StartTicks == 0 || p.PreviousProcess.LaunchID != source.LaunchID || p.PreviousProcess.SHA256 != p.Previous.ArtifactSHA256 {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "unfinished source requires exact physical process or owned exit")
	}
	if source.Kind == "restart" {
		if source.OriginalLedger != nil || source.OriginalRequestID != "" {
			return nil, protocol.Errorf(protocol.CodeConflictPayload, "restart source cannot replace an unrelated upgrade ledger")
		}
		return nil, nil
	}
	if source.Kind != "upgrade" || source.LaunchID != p.Previous.ProcessInstanceID || source.OperationID != p.Previous.UpgradeID || source.OriginalLedger == nil || !protocol.ValidID(protocol.PrefixRequest, source.OriginalRequestID) {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "unfinished upgrade source requires its original request and ledger revision")
	}
	r, err := store.GetUpgrade(ctx, q, p.Previous.UpgradeID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(r.View, *source.OriginalLedger) || !bindingMatches(r, p.Previous) || r.ServerInstance != p.Previous.InstanceID || r.ServerProcess != source.LaunchID || r.ServerHash != p.Previous.ArtifactSHA256 || r.StartRequestID != source.OriginalRequestID || (r.View.Status != "running" && r.View.Status != "attention") || (r.View.Phase != "server_ready" && r.View.Phase != "server_checking") || r.SupersededByMaintenanceID != "" {
		return nil, protocol.Errorf(protocol.CodeStaleRevision, "unfinished upgrade source ledger or revision changed")
	}
	for _, host := range r.Hosts {
		// A server replacement cannot silently settle a separate runner launch.
		if host.CandidateInstance != "" {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "runner candidate requires independent reconciliation before server responsibility transfer")
		}
	}
	original, err := store.GetRequest(ctx, q, "system", source.OriginalRequestID)
	if err != nil {
		return nil, err
	}
	var result protocol.UpgradeView
	if original.Operation != "upgrades.start" || (original.Status != protocol.OpAccepted && original.Status != protocol.OpSucceeded) || json.Unmarshal([]byte(original.ResultJSON), &result) != nil || result.UpgradeID != r.View.UpgradeID {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "original start request does not identify unfinished installation")
	}
	return &r, nil
}

func supersedeMaintenanceOperation(ctx context.Context, x *Exec, tx *store.Tx, p protocol.CommittedMaintenancePlan) error {
	original, err := maintenanceOperationSource(ctx, tx.Q(), p)
	if err != nil || original == nil {
		return err
	}
	// Only installation responsibility changes. Original process health, runner
	// receipts and business unknowns remain facts, not a fabricated completion.
	original.SupersededByMaintenanceID = p.MaintenanceID
	original.View.Status = "cancelled"
	original.View.Phase = "cancelled"
	original.View.Reason = "installation responsibility transferred to independently committed maintenance"
	original.View.Revision++
	original.View.UpdatedAt = x.Now
	original.Deadline = 0
	return tx.PutUpgrade(ctx, *original)
}
